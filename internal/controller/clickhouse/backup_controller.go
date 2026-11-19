package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	// backupPollInterval is the interval between the checks of a running operation.
	backupPollInterval = 10 * time.Second
	// backupWaitInterval is the interval between the checks of the dependencies an operation waits for.
	backupWaitInterval = 30 * time.Second
	// maxBackupMessageLength limits the size of the error reported in the status.
	maxBackupMessageLength = 2048

	hiddenSecret = "[HIDDEN]"
)

// backupExecutor runs backup operations on the replicas of a cluster.
type backupExecutor interface {
	Ping(ctx context.Context, id v1.ClickHouseReplicaID) error
	HasSetting(ctx context.Context, id v1.ClickHouseReplicaID, name string) (bool, error)
	StartBackupOperation(ctx context.Context, id v1.ClickHouseReplicaID, query string) error
	BackupOperationState(ctx context.Context, id v1.ClickHouseReplicaID, operationID string) (backupOperationState, bool, error)
}

// backupExecutorFactory creates the executor for a cluster with the credentials stored in the cluster secret.
type backupExecutorFactory func(log controllerutil.Logger, cluster *v1.ClickHouseCluster, secret *corev1.Secret) backupExecutor

// BackupController reconciles ClickHouseBackup and ClickHouseRestore objects.
// Both are one-shot operations executed by ClickHouse itself: the controller submits
// an asynchronous statement to one replica and follows it until it finishes.
type BackupController struct {
	client.Client

	Logger      controllerutil.Logger
	Recorder    events.EventRecorder
	NewExecutor backupExecutorFactory
}

// backupBlocker explains why an operation cannot be started.
type backupBlocker struct {
	Reason  v1.ConditionReason
	Message string
	// Terminal blockers fail the operation, other ones are retried.
	Terminal bool
}

// backupOperation adapts ClickHouseBackup and ClickHouseRestore to the shared reconciliation flow.
type backupOperation struct {
	Object      client.Object
	Status      *v1.BackupOperationStatus
	ClusterName string
	Restore     bool
	// Plan builds the statement. A returned blocker means that the operation cannot be started now.
	Plan func(ctx context.Context) (backupStatement, *backupBlocker, error)
	// Finish stores the final operation metrics in the status.
	Finish func(state backupOperationState)
}

func (o *backupOperation) startedEvent() (v1.EventReason, v1.EventAction) {
	if o.Restore {
		return v1.EventReasonRestoreStarted, v1.EventActionRestore
	}

	return v1.EventReasonBackupStarted, v1.EventActionBackup
}

func (o *backupOperation) completedEvent() (v1.EventReason, v1.EventAction) {
	if o.Restore {
		return v1.EventReasonRestoreCompleted, v1.EventActionRestore
	}

	return v1.EventReasonBackupCompleted, v1.EventActionBackup
}

func (o *backupOperation) failedEvent() (v1.EventReason, v1.EventAction) {
	if o.Restore {
		return v1.EventReasonRestoreFailed, v1.EventActionRestore
	}

	return v1.EventReasonBackupFailed, v1.EventActionBackup
}

// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhousebackups;clickhouserestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhousebackups/status;clickhouserestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhousebackups/finalizers;clickhouserestores/finalizers,verbs=update

type backupReconciler struct {
	*BackupController
}

// Reconcile moves a ClickHouseBackup towards completion.
func (r backupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	backup := &v1.ClickHouseBackup{}
	if err := r.Get(ctx, req.NamespacedName, backup); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("get ClickHouseBackup %s: %w", req.String(), err)
	}

	return r.reconcileOperation(ctx, r.Logger.WithContext(ctx, backup), &backupOperation{
		Object:      backup,
		Status:      &backup.Status.BackupOperationStatus,
		ClusterName: backup.Spec.ClusterRef.Name,
		Plan: func(ctx context.Context) (backupStatement, *backupBlocker, error) {
			return r.planBackup(ctx, backup)
		},
		Finish: func(state backupOperationState) {
			backup.Status.NumFiles = int64(state.NumFiles)             //nolint:gosec // sizes never overflow int64
			backup.Status.TotalSize = int64(state.TotalSize)           //nolint:gosec // sizes never overflow int64
			backup.Status.CompressedSize = int64(state.CompressedSize) //nolint:gosec // sizes never overflow int64
		},
	})
}

func (r backupReconciler) planBackup(ctx context.Context, backup *v1.ClickHouseBackup) (backupStatement, *backupBlocker, error) {
	location, blocker, err := r.resolveLocation(ctx, backup.Namespace, backup.Spec.Storage, backup.Name)
	if blocker != nil || err != nil {
		return backupStatement{}, blocker, err
	}

	stmt := backupStatement{
		Scope:    backup.Spec.Scope,
		Location: location,
		Settings: backup.Spec.Settings,
	}

	if backup.Spec.BaseBackupRef != nil {
		base, blocker, err := r.resolveBackup(ctx, backup.Namespace, backup.Spec.BaseBackupRef.Name)
		if blocker != nil || err != nil {
			return backupStatement{}, blocker, err
		}

		stmt.Base = &base
		backup.Status.BaseLocation = base.URL
	}

	return stmt, nil, nil
}

type restoreReconciler struct {
	*BackupController
}

// Reconcile moves a ClickHouseRestore towards completion.
func (r restoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	restore := &v1.ClickHouseRestore{}
	if err := r.Get(ctx, req.NamespacedName, restore); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("get ClickHouseRestore %s: %w", req.String(), err)
	}

	return r.reconcileOperation(ctx, r.Logger.WithContext(ctx, restore), &backupOperation{
		Object:      restore,
		Status:      &restore.Status.BackupOperationStatus,
		ClusterName: restore.Spec.ClusterRef.Name,
		Restore:     true,
		Plan: func(ctx context.Context) (backupStatement, *backupBlocker, error) {
			return r.planRestore(ctx, restore)
		},
		Finish: func(backupOperationState) {},
	})
}

func (r restoreReconciler) planRestore(ctx context.Context, restore *v1.ClickHouseRestore) (backupStatement, *backupBlocker, error) {
	var (
		location s3Location
		blocker  *backupBlocker
		err      error
	)

	switch {
	case restore.Spec.BackupRef != nil:
		location, blocker, err = r.resolveBackup(ctx, restore.Namespace, restore.Spec.BackupRef.Name)
	case restore.Spec.Source != nil:
		location, blocker, err = r.resolveLocation(ctx, restore.Namespace, restore.Spec.Source.Storage, restore.Spec.Source.Name)
	default:
		blocker = &backupBlocker{
			Reason:   v1.BackupConditionReasonBackupNotFound,
			Message:  "neither backupRef nor source is set",
			Terminal: true,
		}
	}

	if blocker != nil || err != nil {
		return backupStatement{}, blocker, err
	}

	return backupStatement{
		Restore:  true,
		Scope:    restore.Spec.Scope,
		Location: location,
		Settings: restore.Spec.Settings,
	}, nil, nil
}

// resolveBackup returns the location of a completed ClickHouseBackup.
func (c *BackupController) resolveBackup(ctx context.Context, namespace, name string) (s3Location, *backupBlocker, error) {
	backup := &v1.ClickHouseBackup{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, backup); err != nil {
		if k8serrors.IsNotFound(err) {
			return s3Location{}, &backupBlocker{
				Reason:  v1.BackupConditionReasonBackupNotFound,
				Message: fmt.Sprintf("ClickHouseBackup %q is not found", name),
			}, nil
		}

		return s3Location{}, nil, fmt.Errorf("get ClickHouseBackup %s/%s: %w", namespace, name, err)
	}

	switch backup.Status.Phase {
	case v1.BackupPhaseCompleted:
	case v1.BackupPhaseFailed:
		return s3Location{}, &backupBlocker{
			Reason:   v1.BackupConditionReasonBackupFailed,
			Message:  fmt.Sprintf("ClickHouseBackup %q failed", name),
			Terminal: true,
		}, nil

	case v1.BackupPhasePending, v1.BackupPhaseRunning:
		fallthrough
	default:
		return s3Location{}, &backupBlocker{
			Reason:  v1.BackupConditionReasonBackupNotCompleted,
			Message: fmt.Sprintf("ClickHouseBackup %q is not completed yet", name),
		}, nil
	}

	location, blocker, err := c.resolveLocation(ctx, namespace, backup.Spec.Storage, backup.Name)
	if blocker != nil || err != nil {
		return s3Location{}, blocker, err
	}

	// The backup stays where it was written even if the resource naming rules change.
	location.URL = backup.Status.Location

	return location, nil, nil
}

// resolveLocation builds the location of the named backup and loads the storage credentials.
func (c *BackupController) resolveLocation(
	ctx context.Context,
	namespace string,
	storage v1.BackupStorage,
	name string,
) (s3Location, *backupBlocker, error) {
	if storage.S3 == nil {
		return s3Location{}, &backupBlocker{
			Reason:   v1.BackupConditionReasonCredentialsInvalid,
			Message:  "storage is not configured",
			Terminal: true,
		}, nil
	}

	location := s3Location{URL: storage.S3.Location(name)}
	if storage.S3.Credentials == nil {
		return location, nil, nil
	}

	for _, ref := range []struct {
		selector v1.SecretKeySelector
		target   *string
	}{
		{storage.S3.Credentials.AccessKeyID, &location.AccessKeyID},
		{storage.S3.Credentials.SecretAccessKey, &location.SecretAccessKey},
	} {
		value, blocker, err := c.secretValue(ctx, namespace, ref.selector)
		if blocker != nil || err != nil {
			return s3Location{}, blocker, err
		}

		*ref.target = value
	}

	return location, nil, nil
}

func (c *BackupController) secretValue(
	ctx context.Context,
	namespace string,
	selector v1.SecretKeySelector,
) (string, *backupBlocker, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: selector.Name}, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return "", &backupBlocker{
				Reason:  v1.BackupConditionReasonCredentialsInvalid,
				Message: fmt.Sprintf("Secret %q is not found", selector.Name),
			}, nil
		}

		return "", nil, fmt.Errorf("get Secret %s/%s: %w", namespace, selector.Name, err)
	}

	value := string(secret.Data[selector.Key])
	if value == "" {
		return "", &backupBlocker{
			Reason:  v1.BackupConditionReasonCredentialsInvalid,
			Message: fmt.Sprintf("Secret %q has no value for key %q", selector.Name, selector.Key),
		}, nil
	}

	return value, nil, nil
}

func (c *BackupController) reconcileOperation(ctx context.Context, log controllerutil.Logger, op *backupOperation) (ctrl.Result, error) {
	if op.Status.Phase.Finished() || !op.Object.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}

	namespace := op.Object.GetNamespace()
	started := op.Status.ID != ""

	cluster := &v1.ClickHouseCluster{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: op.ClusterName}, cluster); err != nil {
		if !k8serrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get ClickHouseCluster %s/%s: %w", namespace, op.ClusterName, err)
		}

		if started {
			return c.fail(ctx, op, v1.BackupConditionReasonStateLost,
				fmt.Sprintf("ClickHouseCluster %q was deleted while the operation was running", op.ClusterName))
		}

		return c.wait(ctx, op, v1.BackupConditionReasonClusterNotFound,
			fmt.Sprintf("ClickHouseCluster %q is not found", op.ClusterName))
	}

	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cluster.SecretName()}, secret); err != nil {
		if !k8serrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get cluster secret %s/%s: %w", namespace, cluster.SecretName(), err)
		}

		if started {
			log.Warn("cluster secret is not found, operation state cannot be checked")

			return ctrl.Result{RequeueAfter: backupPollInterval}, nil
		}

		return c.wait(ctx, op, v1.BackupConditionReasonClusterNotReady, "cluster secret is not created yet")
	}

	executor := c.NewExecutor(log, cluster, secret)

	if !started {
		return c.start(ctx, log, op, cluster, executor)
	}

	return c.poll(ctx, log, op, cluster, executor)
}

// start submits the operation to the first reachable replica.
func (c *BackupController) start(
	ctx context.Context,
	log controllerutil.Logger,
	op *backupOperation,
	cluster *v1.ClickHouseCluster,
	executor backupExecutor,
) (ctrl.Result, error) {
	stmt, blocker, err := op.Plan(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	if blocker != nil {
		if blocker.Terminal {
			return c.fail(ctx, op, blocker.Reason, blocker.Message)
		}

		return c.wait(ctx, op, blocker.Reason, blocker.Message)
	}

	replica, ok := pickBackupReplica(ctx, cluster, executor)
	if !ok {
		return c.wait(ctx, op, v1.BackupConditionReasonClusterNotReady, "no replica of the cluster is reachable")
	}

	if stmt.usesServerCredentials() {
		stmt.AllowServerCredentials, err = executor.HasSetting(ctx, replica, settingAllowServerCredentials)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("check server credentials setting: %w", err)
		}
	}

	// The object UID makes the submission idempotent: a repeated statement is rejected by the replica.
	stmt.ID = string(op.Object.GetUID())

	if err := executor.StartBackupOperation(ctx, replica, stmt.SQL()); err != nil {
		if exc, ok := errors.AsType[*clickhouse.Exception](err); ok {
			return c.fail(ctx, op, v1.BackupConditionReasonFailed, redactSecrets(exc.Message, stmt))
		}

		return ctrl.Result{}, fmt.Errorf("start operation: %w", errors.New(redactSecrets(err.Error(), stmt)))
	}

	log.Info("operation started", "operation_id", stmt.ID, "replica_id", replica, "location", stmt.Location.URL)

	now := metav1.Now()
	op.Status.ID = stmt.ID
	op.Status.Replica = &v1.BackupReplica{ShardID: replica.ShardID, Index: replica.Index}
	op.Status.Location = stmt.Location.URL
	op.Status.StartTime = &now
	op.Status.Phase = v1.BackupPhaseRunning
	c.setCompleted(op, metav1.ConditionFalse, v1.BackupConditionReasonInProgress, "")

	if err := c.Status().Update(ctx, op.Object); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	reason, action := op.startedEvent()
	c.Recorder.Eventf(op.Object, nil, corev1.EventTypeNormal, reason, action, "Started on replica %s", replica)

	return ctrl.Result{RequeueAfter: backupPollInterval}, nil
}

// poll follows a submitted operation until it reaches a final state.
func (c *BackupController) poll(
	ctx context.Context,
	log controllerutil.Logger,
	op *backupOperation,
	cluster *v1.ClickHouseCluster,
	executor backupExecutor,
) (ctrl.Result, error) {
	if op.Status.Replica == nil {
		return c.fail(ctx, op, v1.BackupConditionReasonStateLost, "replica that executes the operation is unknown")
	}

	replica := v1.ClickHouseReplicaID{ShardID: op.Status.Replica.ShardID, Index: op.Status.Replica.Index}
	if replica.ShardID >= cluster.Shards() || replica.Index >= cluster.Replicas() {
		return c.fail(ctx, op, v1.BackupConditionReasonStateLost,
			fmt.Sprintf("replica %s that executed the operation was removed from the cluster", replica))
	}

	stateCtx, cancel := context.WithTimeout(ctx, chctrl.LoadReplicaStateTimeout)
	defer cancel()

	state, found, err := executor.BackupOperationState(stateCtx, replica, op.Status.ID)
	if err != nil {
		// The replica may be restarting: the operation state is checked again once it is back.
		log.Warn("failed to read operation state", "replica_id", replica, "error", err)

		return ctrl.Result{RequeueAfter: backupPollInterval}, nil
	}

	switch {
	case !found:
		return c.fail(ctx, op, v1.BackupConditionReasonStateLost,
			fmt.Sprintf("replica %s does not know the operation: the server was restarted while it was running", replica))
	case state.running():
		return ctrl.Result{RequeueAfter: backupPollInterval}, nil
	case state.succeeded():
		return c.complete(ctx, log, op, state)
	default:
		return c.fail(ctx, op, v1.BackupConditionReasonFailed, fmt.Sprintf("%s: %s", state.Status, errorSummary(state.Error)))
	}
}

func (c *BackupController) complete(
	ctx context.Context,
	log controllerutil.Logger,
	op *backupOperation,
	state backupOperationState,
) (ctrl.Result, error) {
	now := metav1.Now()
	op.Status.Phase = v1.BackupPhaseCompleted
	op.Status.CompletionTime = &now
	op.Finish(state)
	c.setCompleted(op, metav1.ConditionTrue, v1.BackupConditionReasonSucceeded, "")

	if err := c.Status().Update(ctx, op.Object); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	log.Info("operation completed", "operation_id", op.Status.ID)

	reason, action := op.completedEvent()
	c.Recorder.Eventf(op.Object, nil, corev1.EventTypeNormal, reason, action, "Completed at %s", op.Status.Location)

	return ctrl.Result{}, nil
}

// fail marks the operation as failed. Failed operations are never retried.
func (c *BackupController) fail(
	ctx context.Context,
	op *backupOperation,
	reason v1.ConditionReason,
	message string,
) (ctrl.Result, error) {
	now := metav1.Now()
	op.Status.Phase = v1.BackupPhaseFailed
	op.Status.CompletionTime = &now
	c.setCompleted(op, metav1.ConditionFalse, reason, message)

	if err := c.Status().Update(ctx, op.Object); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	eventReason, action := op.failedEvent()
	c.Recorder.Eventf(op.Object, nil, corev1.EventTypeWarning, eventReason, action, "%s: %s", reason, message)

	return ctrl.Result{}, nil
}

// wait reports why the operation is not started yet and schedules the next check.
func (c *BackupController) wait(
	ctx context.Context,
	op *backupOperation,
	reason v1.ConditionReason,
	message string,
) (ctrl.Result, error) {
	current := meta.FindStatusCondition(op.Status.Conditions, v1.BackupConditionTypeCompleted)
	if op.Status.Phase == v1.BackupPhasePending && current != nil && current.Reason == reason && current.Message == message {
		return ctrl.Result{RequeueAfter: backupWaitInterval}, nil
	}

	op.Status.Phase = v1.BackupPhasePending
	c.setCompleted(op, metav1.ConditionFalse, reason, message)

	if err := c.Status().Update(ctx, op.Object); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{RequeueAfter: backupWaitInterval}, nil
}

func (*BackupController) setCompleted(op *backupOperation, status metav1.ConditionStatus, reason v1.ConditionReason, message string) {
	chctrl.SetStatusCondition(&op.Status.Conditions, metav1.Condition{
		Type:               v1.BackupConditionTypeCompleted,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: op.Object.GetGeneration(),
	})
}

// pickBackupReplica returns the first replica that accepts connections.
// The order is stable, so a repeated submission reaches the replica that already knows the operation.
func pickBackupReplica(ctx context.Context, cluster *v1.ClickHouseCluster, executor backupExecutor) (v1.ClickHouseReplicaID, bool) {
	for id := range cluster.ReplicaIDs() {
		pingCtx, cancel := context.WithTimeout(ctx, chctrl.LoadReplicaStateTimeout)
		err := executor.Ping(pingCtx, id)

		cancel()

		if err == nil {
			return id, true
		}
	}

	return v1.ClickHouseReplicaID{}, false
}

// errorSummary drops the stack trace ClickHouse appends to the stored error and limits the message size.
func errorSummary(message string) string {
	if idx := strings.Index(message, ", Stack trace"); idx >= 0 {
		message = message[:idx]
	}

	message = strings.TrimSpace(message)
	if len(message) > maxBackupMessageLength {
		message = strings.ToValidUTF8(message[:maxBackupMessageLength], "") + "..."
	}

	return message
}

// redactSecrets removes the storage credentials from a message that may quote the statement.
func redactSecrets(message string, stmt backupStatement) string {
	locations := []s3Location{stmt.Location}
	if stmt.Base != nil {
		locations = append(locations, *stmt.Base)
	}

	for _, location := range locations {
		if location.SecretAccessKey == "" {
			continue
		}

		message = strings.ReplaceAll(message, location.SecretAccessKey, hiddenSecret)
		message = strings.ReplaceAll(message, stringEscaper.Replace(location.SecretAccessKey), hiddenSecret)
	}

	return errorSummary(message)
}

// setupBackupControllers registers the controllers of the backup resources.
func setupBackupControllers(mgr ctrl.Manager, deps chctrl.Dependencies, settings chctrl.Settings, cache *connCache) error {
	backupController := &BackupController{
		Client:   mgr.GetClient(),
		Logger:   deps.Logger.Named("backup"),
		Recorder: mgr.GetEventRecorder("clickhouse-backup-controller"),
		NewExecutor: func(log controllerutil.Logger, cluster *v1.ClickHouseCluster, secret *corev1.Secret) backupExecutor {
			return newCommander(log, cluster, secret, deps.Dialer, cache)
		},
	}

	options := controller.Options{MaxConcurrentReconciles: settings.MaxConcurrentReconciles}

	if err := ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1.ClickHouseBackup{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(backupReconciler{backupController}); err != nil {
		return fmt.Errorf("setup ClickHouseBackup controller: %w", err)
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1.ClickHouseRestore{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(restoreReconciler{backupController}); err != nil {
		return fmt.Errorf("setup ClickHouseRestore controller: %w", err)
	}

	scheduledController := &ScheduledBackupController{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Logger:   deps.Logger.Named("scheduled-backup"),
		Recorder: mgr.GetEventRecorder("clickhouse-scheduled-backup-controller"),
		Now:      time.Now,
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1.ClickHouseScheduledBackup{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&v1.ClickHouseBackup{}).
		Complete(scheduledController); err != nil {
		return fmt.Errorf("setup ClickHouseScheduledBackup controller: %w", err)
	}

	return nil
}
