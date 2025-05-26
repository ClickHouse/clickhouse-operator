package keeper

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type replicaUpdateStage int

const (
	stageUpToDate replicaUpdateStage = iota
	stageStsAndConfigDiff
	stageConfigDiff
	stageStsDiff
	stageNotReadyUpToDate
	stageNotReadyWithDiff
	stageUpdating
	stageError
	stageNotExists
)

var (
	mapStatusText = map[replicaUpdateStage]string{
		stageUpToDate:         "UpToDate",
		stageStsAndConfigDiff: "StatefulSetAndConfigDiff",
		stageConfigDiff:       "ConfigDiff",
		stageStsDiff:          "StatefulSetDiff",
		stageNotReadyUpToDate: "NotReadyUpToDate",
		stageNotReadyWithDiff: "NotReadyWithDiff",
		stageUpdating:         "Updating",
		stageError:            "Error",
		stageNotExists:        "NotExists",
	}
)

func (s replicaUpdateStage) String() string {
	return mapStatusText[s]
}

type replicaState struct {
	Error       bool `json:"error"`
	Status      ServerStatus
	StatefulSet *appsv1.StatefulSet
}

func (r replicaState) Updated() bool {
	if r.StatefulSet == nil {
		return false
	}

	return r.StatefulSet.Generation == r.StatefulSet.Status.ObservedGeneration &&
		r.StatefulSet.Status.UpdateRevision == r.StatefulSet.Status.CurrentRevision
}

func (r replicaState) Ready(ctx reconcileContext) bool {
	if r.StatefulSet == nil {
		return false
	}

	stsReady := r.StatefulSet.Status.ReadyReplicas == 1 // Not reliable, but allows to wait until pod is `green`
	if ctx.KeeperCluster.Replicas() == 1 {
		return stsReady && r.Status.ServerState == ModeStandalone
	}

	return stsReady && slices.Contains(ClusterModes, r.Status.ServerState)
}

func (r replicaState) HasStatefulSetDiff(ctx reconcileContext) bool {
	if r.StatefulSet == nil {
		return true
	}

	return util.GetSpecHashFromObject(r.StatefulSet) != ctx.KeeperCluster.Status.StatefulSetRevision
}

func (r replicaState) HasConfigMapDiff(ctx reconcileContext) bool {
	if r.StatefulSet == nil {
		return true
	}

	return util.GetConfigHashFromObject(r.StatefulSet) != ctx.KeeperCluster.Status.ConfigurationRevision
}

func (r replicaState) UpdateStage(ctx reconcileContext) replicaUpdateStage {
	if r.StatefulSet == nil {
		return stageNotExists
	}

	if r.Error {
		return stageError
	}

	if !r.Updated() {
		return stageUpdating
	}

	configDiff := r.HasConfigMapDiff(ctx)
	stsDiff := r.HasStatefulSetDiff(ctx)

	if !r.Ready(ctx) {
		if configDiff || stsDiff {
			return stageNotReadyWithDiff
		}
		return stageNotReadyUpToDate
	}

	switch {
	case configDiff && stsDiff:
		return stageStsAndConfigDiff
	case configDiff:
		return stageConfigDiff
	case stsDiff:
		return stageStsDiff
	}

	return stageUpToDate
}

type reconcileContext struct {
	KeeperCluster *v1.KeeperCluster
	Context       context.Context
	Log           util.Logger

	// Should be populated by reconcileActiveReplicaStatus
	stateByID map[string]replicaState
}

func (ctx *reconcileContext) NewCondition(
	condType v1.KeeperConditionType,
	status metav1.ConditionStatus,
	reason v1.KeeperConditionReason,
	message string,
) metav1.Condition {
	return metav1.Condition{
		Type:               string(condType),
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: ctx.KeeperCluster.Generation,
	}
}

func (ctx *reconcileContext) Replica(replicaID string) replicaState {
	return ctx.stateByID[replicaID]
}

func (ctx *reconcileContext) With(keysAndVals ...interface{}) reconcileContext {
	return reconcileContext{
		KeeperCluster: ctx.KeeperCluster,
		Context:       ctx.Context,
		Log:           ctx.Log.With(keysAndVals...),
		stateByID:     ctx.stateByID,
	}
}

type ReconcileFunc func(reconcileContext) (*ctrl.Result, error)

func (r *ClusterReconciler) Sync(ctx context.Context, log util.Logger, cr *v1.KeeperCluster) (ctrl.Result, error) {
	log.Info("Enter Keeper Reconcile", "spec", cr.Spec, "status", cr.Status)

	recCtx := reconcileContext{
		KeeperCluster: cr,
		Context:       ctx,
		Log:           log,
		stateByID:     map[string]replicaState{},
	}

	reconcileSteps := []ReconcileFunc{
		r.reconcileHeadlessService,
		r.reconcileClusterRevisions,
		r.reconcileActiveReplicaStatus,
		r.reconcileQuorumMembership,
		r.reconcileQuorumConfig,
		r.reconcileReplicaResources,
		r.reconcileCleanUp,
		r.reconcileConditions,
	}

	var result ctrl.Result
	for _, fn := range reconcileSteps {
		funcName := strings.TrimPrefix(util.GetFunctionName(fn), "reconcile")
		stepCtx := recCtx.With("reconcile_step", funcName)
		stepCtx.Log.Debug("starting reconcile step")

		stepResult, err := fn(recCtx)
		if err != nil {
			if k8serrors.IsConflict(err) {
				stepCtx.Log.Error(err, "update conflict for resource, reschedule to retry")
				// retry immediately, as just the update to the CR failed
				return ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}
			if k8serrors.IsAlreadyExists(err) {
				stepCtx.Log.Error(err, "create already existed resource, reschedule to retry")
				// retry immediately, as just creating already existed resource
				return ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}

			stepCtx.Log.Error(err, "unexpected error, setting conditions to unknown and rescheduling reconciliation to try again")
			errMsg := "Reconcile returned error"
			setConditions(recCtx, []metav1.Condition{
				recCtx.NewCondition(v1.KeeperConditionTypeReconcileSucceeded, metav1.ConditionFalse, v1.KeeperConditionReasonStepFailed, errMsg),
				// Operator did not finish reconciliation, some conditions may not be valid already.
				recCtx.NewCondition(v1.KeeperConditionTypeReady, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeDegraded, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeReplicaStartupFailure, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeConfigurationInSync, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
			})

			return ctrl.Result{RequeueAfter: RequeueOnErrorTimeout}, r.upsertStatus(recCtx)
		}

		if !stepResult.IsZero() {
			stepCtx.Log.Debug("reconcile step result", "result", stepResult)
			util.UpdateResult(&result, stepResult)
		}

		stepCtx.Log.Debug("reconcile step completed")
	}

	setCondition(recCtx, v1.KeeperConditionTypeReconcileSucceeded, metav1.ConditionTrue, v1.KeeperConditionReasonReconcileFinished, "Reconcile succeeded")
	log.Info("reconciliation loop end", "result", result)

	if err := r.upsertStatus(recCtx); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after reconciliation: %w", err)
	}

	return result, nil
}

func (r *ClusterReconciler) reconcileHeadlessService(ctx reconcileContext) (*ctrl.Result, error) {
	service := TemplateHeadlessService(ctx.KeeperCluster)
	log := ctx.Log.With("service", service.Name)

	if err := ctrl.SetControllerReference(ctx.KeeperCluster, service, r.Scheme); err != nil {
		return nil, err
	}

	foundService := &corev1.Service{}
	err := r.Get(ctx.Context, types.NamespacedName{
		Namespace: ctx.KeeperCluster.GetNamespace(),
		Name:      ctx.KeeperCluster.HeadlessServiceName(),
	}, foundService)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("get Headless Service: %w", err)
		}

		if err := util.AddSpecHashToAnnotations(service, service.Spec); err != nil {
			return nil, fmt.Errorf("add Headless Service spec hash to annotations: %w", err)
		}

		ctx.Log.Info("Headless Service not found, creating")
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			return r.Create(ctx.Context, service)
		}); err != nil {
			return nil, fmt.Errorf("create Headless Service: %w", err)
		}
		return nil, nil
	}

	if util.IsEqualSpecHash(foundService, service.Spec) {
		log.Debug("Headless Service is up to date")
		return nil, nil
	}

	log.Debug("Headless Service changed", "service_diff", cmp.Diff(foundService, service))

	foundService.Spec = service.Spec
	if err := util.AddSpecHashToAnnotations(service, service.Spec); err != nil {
		return nil, fmt.Errorf("add Headless Service spec hash to annotations: %w", err)
	}
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return r.Update(ctx.Context, foundService)
	}); err != nil {
		return nil, fmt.Errorf("update Headless Service: %w", err)
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileClusterRevisions(ctx reconcileContext) (*ctrl.Result, error) {
	var err error

	ctx.KeeperCluster.Status.ObservedGeneration = ctx.KeeperCluster.Generation

	ctx.KeeperCluster.Status.UpdateRevision, err = util.DeepHashObject(ctx.KeeperCluster.Spec)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("get current spec revision: %w", err)
	}

	ctx.KeeperCluster.Status.ConfigurationRevision, err = GetConfigurationRevision(ctx.KeeperCluster)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("get configuration revision: %w", err)
	}

	ctx.KeeperCluster.Status.StatefulSetRevision, err = GetStatefulSetRevision(ctx.KeeperCluster)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("get StatefulSet revision: %w", err)
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileActiveReplicaStatus(ctx reconcileContext) (*ctrl.Result, error) {
	if ctx.KeeperCluster.Replicas() == 0 {
		ctx.Log.Debug("keeper replicaState count is zero")
		return nil, nil
	}

	hostnamesByID := map[string]string{}

	for _, id := range ctx.KeeperCluster.Status.Replicas {
		var replicaSts appsv1.StatefulSet
		if err := r.Get(ctx.Context, types.NamespacedName{
			Namespace: ctx.KeeperCluster.Namespace,
			Name:      ctx.KeeperCluster.StatefulSetNameByReplicaID(id),
		}, &replicaSts); err != nil {
			if !k8serrors.IsNotFound(err) {
				return &ctrl.Result{}, fmt.Errorf("get replicas %q sts: %w", id, err)
			}

			ctx.Log.Debug("StatefulSet not found", "replica_id", id, "statefuleset", ctx.KeeperCluster.StatefulSetNameByReplicaID(id))
		} else {
			hostnamesByID[id] = ctx.KeeperCluster.HostnameById(id)
			isError, err := r.checkReplicaPodError(ctx, id, &replicaSts)
			if err != nil {
				return &ctrl.Result{}, fmt.Errorf("get replica %q error state: %w", id, err)
			}

			ctx.stateByID[id] = replicaState{
				Error:       isError,
				StatefulSet: &replicaSts,
			}
		}
	}

	checkCtx, cancel := context.WithTimeout(ctx.Context, StatusRequestTimeout)
	defer cancel()

	for id, state := range getServersStates(checkCtx, ctx.Log, hostnamesByID) {
		replica := ctx.stateByID[id]
		replica.Status = state
		ctx.stateByID[id] = replica
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileQuorumMembership(ctx reconcileContext) (*ctrl.Result, error) {
	requestedReplicas := int(ctx.KeeperCluster.Replicas())

	// New cluster creation, creates all replicas.
	if requestedReplicas > 0 && len(ctx.KeeperCluster.Status.Replicas) == 0 {
		ctx.Log.Debug("creating all replicas")
		for i := range requestedReplicas {
			id := strconv.Itoa(i + 1)
			ctx.Log.Info("creating new replica", "replica_id", id)
			ctx.KeeperCluster.Status.Replicas = append(ctx.KeeperCluster.Status.Replicas, id)
		}

		return &ctrl.Result{}, nil
	}

	// Scale to zero replica count could be applied without checks.
	if requestedReplicas == 0 && len(ctx.KeeperCluster.Status.Replicas) > 0 {
		ctx.Log.Debug("deleting all replicas", "replicas", ctx.KeeperCluster.Status.Replicas)
		ctx.KeeperCluster.Status.Replicas = nil
		return &ctrl.Result{}, nil
	}

	if requestedReplicas == len(ctx.KeeperCluster.Status.Replicas) {
		return nil, nil
	}

	// For running cluster, allow quorum membership changes only in stable state.
	leaderMode := ModeLeader
	if len(ctx.KeeperCluster.Status.Replicas) == 1 {
		leaderMode = ModeStandalone
	}

	hasLeader := false
	for _, id := range ctx.KeeperCluster.Status.Replicas {
		replica := ctx.Replica(id)

		if replica.HasConfigMapDiff(ctx) {
			ctx.Log.Info("replica configurationRevision is stale, delaying quorum membership changes", "replica_id", id)
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
		if replica.HasStatefulSetDiff(ctx) {
			ctx.Log.Info("replica statefulSetRevision is stale, delaying quorum membership changes", "replica_id", id)
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
		if !replica.Ready(ctx) {
			ctx.Log.Info("replica is not Ready, delaying quorum membership changes", "replica_id", id)
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
		if replica.Status.ServerState == leaderMode {
			if hasLeader {
				ctx.Log.Info("multiple leaders in cluster, delaying quorum membership changes", "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}

			hasLeader = true
			// Wait for deleted replicas to leave the quorum.
			if replica.Status.Followers > len(ctx.KeeperCluster.Status.Replicas)-1 {
				ctx.Log.Info(fmt.Sprintf("leader has more followers than expected: %d > %d, delaying quorum membership changes", replica.Status.Followers, len(ctx.KeeperCluster.Status.Replicas)-1), "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			} else if replica.Status.Followers < len(ctx.KeeperCluster.Status.Replicas)-1 {
				ctx.Log.Info(fmt.Sprintf("leader has less followers than expected: %d < %d, delaying quorum membership changes", replica.Status.Followers, len(ctx.KeeperCluster.Status.Replicas)-1), "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}
		}
	}
	if !hasLeader {
		ctx.Log.Info("no leader in cluster, delaying quorum membership changes")
		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// Add single replica in quorum, allocating the first free id.
	if len(ctx.KeeperCluster.Status.Replicas) < requestedReplicas {
		for i := 1; ; i++ {
			id := strconv.Itoa(i)
			if !slices.Contains(ctx.KeeperCluster.Status.Replicas, id) {
				ctx.Log.Info("creating new replica", "replica_id", id)
				ctx.KeeperCluster.Status.Replicas = append(ctx.KeeperCluster.Status.Replicas, id)
				return nil, nil
			}
		}
	}

	// Remove single replica from the quorum. Prefer bigger id.
	if len(ctx.KeeperCluster.Status.Replicas) > requestedReplicas {
		chosenIndex := -1
		for i, id := range ctx.KeeperCluster.Status.Replicas {
			if chosenIndex == -1 || ctx.KeeperCluster.Status.Replicas[chosenIndex] < id {
				chosenIndex = i
			}
		}

		if chosenIndex == -1 {
			ctx.Log.Warn(fmt.Sprintf("no replica in cluster, but requested scale down: %d < %d", requestedReplicas, len(ctx.KeeperCluster.Status.Replicas)))
		}

		ctx.Log.Info("deleting replica", "replica_id", ctx.KeeperCluster.Status.Replicas[chosenIndex])
		ctx.KeeperCluster.Status.Replicas = slices.Delete(ctx.KeeperCluster.Status.Replicas, chosenIndex, chosenIndex+1)
		return nil, nil
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileQuorumConfig(ctx reconcileContext) (*ctrl.Result, error) {
	configMap, err := TemplateQuorumConfig(ctx.KeeperCluster)
	if err != nil {
		return nil, fmt.Errorf("template quorum config: %w", err)
	}

	_, err = r.updateConfigMap(ctx, configMap)
	return nil, err
}

// reconcileReplicaResources performs update on replicas ConfigMap and StatefulSet.
// If there are replicas that has no created StatefulSet, creates immediately.
// If all replicas exists performs rolling upgrade, with the following order preferences:
// NotExists -> CrashLoop/ImagePullErr -> OnlySts -> OnlyConfig -> Any.
func (r *ClusterReconciler) reconcileReplicaResources(ctx reconcileContext) (*ctrl.Result, error) {
	highestStage := stageUpToDate
	var replicasInStatus []string

	for _, id := range ctx.KeeperCluster.Status.Replicas {
		stage := ctx.Replica(id).UpdateStage(ctx)
		if stage == highestStage {
			replicasInStatus = append(replicasInStatus, id)
			continue
		}

		if stage > highestStage {
			highestStage = stage
			replicasInStatus = []string{id}
		}
	}

	result := ctrl.Result{}

	switch highestStage {
	case stageUpToDate:
		ctx.Log.Info("all replicas are up to date")
		return nil, nil
	case stageNotReadyUpToDate, stageUpdating:
		ctx.Log.Info("waiting for updated replicas to become ready", "replicas", replicasInStatus, "priority", highestStage.String())
		result = ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}
	case stageStsAndConfigDiff, stageConfigDiff, stageStsDiff:
		// Leave one replica to rolling update. replicasInStatus must not be empty.
		// Prefer replicas with higher id.
		chosenReplica := replicasInStatus[0]
		for _, id := range replicasInStatus {
			if id > chosenReplica {
				chosenReplica = id
			}
		}
		ctx.Log.Info(fmt.Sprintf("updating chosen replica %s with priority %s: %v", chosenReplica, highestStage.String(), replicasInStatus))
		replicasInStatus = []string{chosenReplica}
	case stageNotReadyWithDiff, stageNotExists, stageError:
		ctx.Log.Info(fmt.Sprintf("updating replicas with priority %s: %v", highestStage.String(), replicasInStatus))
	}

	for _, id := range replicasInStatus {
		replicaResult, err := r.updateReplica(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("update replica %q: %w", id, err)
		}

		util.UpdateResult(&result, replicaResult)
	}

	return &result, nil
}

func (r *ClusterReconciler) reconcileCleanUp(ctx reconcileContext) (*ctrl.Result, error) {
	appReq, err := labels.NewRequirement(util.LabelAppKey, selection.Equals, []string{ctx.KeeperCluster.SpecificName()})
	if err != nil {
		return nil, fmt.Errorf("make %q requirement to list: %w", util.LabelAppKey, err)
	}
	listOpts := &client.ListOptions{
		Namespace:     ctx.KeeperCluster.Namespace,
		LabelSelector: labels.NewSelector().Add(*appReq),
	}

	var configMaps corev1.ConfigMapList
	if err := r.List(ctx.Context, &configMaps, listOpts); err != nil {
		return nil, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		if configMap.Name == ctx.KeeperCluster.QuorumConfigMapName() {
			continue
		}

		replicaID := configMap.Labels[util.LabelKeeperReplicaID]
		if !slices.Contains(ctx.KeeperCluster.Status.Replicas, replicaID) {
			ctx.Log.Info("deleting stale ConfigMap", "configmap", configMap.Name)
			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				return r.Delete(ctx.Context, &configMap)
			}); err != nil {
				return nil, fmt.Errorf("delete ConfigMap %q: %w", configMap.Name, err)
			}
		}
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx.Context, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		replicaID := sts.Labels[util.LabelKeeperReplicaID]
		if !slices.Contains(ctx.KeeperCluster.Status.Replicas, replicaID) {
			ctx.Log.Info("deleting stale StatefulSet", "statefuleset", sts.Name)
			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				return r.Delete(ctx.Context, &sts)
			}); err != nil {
				return nil, fmt.Errorf("delete StatefulSet %q: %w", sts.Name, err)
			}
		}
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileConditions(ctx reconcileContext) (*ctrl.Result, error) {
	var errorReplicas []string
	var notReadyReplicas []string
	var notUpdatedReplicas []string
	replicasByMode := map[string][]string{}

	ctx.KeeperCluster.Status.ReadyReplicas = 0
	for _, id := range ctx.KeeperCluster.Status.Replicas {
		replica := ctx.Replica(id)

		if replica.Error {
			errorReplicas = append(errorReplicas, id)
		}

		if !replica.Ready(ctx) {
			notReadyReplicas = append(notReadyReplicas, id)
		} else {
			ctx.KeeperCluster.Status.ReadyReplicas++
			replicasByMode[replica.Status.ServerState] = append(replicasByMode[replica.Status.ServerState], id)
		}

		if replica.HasConfigMapDiff(ctx) || replica.HasStatefulSetDiff(ctx) || !replica.Updated() {
			notUpdatedReplicas = append(notUpdatedReplicas, id)
		}
	}

	if len(errorReplicas) > 0 {
		slices.Sort(errorReplicas)
		message := fmt.Sprintf("Replicas has startup errors: %v", errorReplicas)
		setCondition(ctx, v1.KeeperConditionTypeReplicaStartupFailure, metav1.ConditionTrue, v1.KeeperConditionReasonReplicaError, message)
	} else {
		setCondition(ctx, v1.KeeperConditionTypeReplicaStartupFailure, metav1.ConditionFalse, v1.KeeperConditionReasonReplicasRunning, "")
	}

	if len(notReadyReplicas) > 0 {
		slices.Sort(notReadyReplicas)
		message := fmt.Sprintf("Not ready replicas: %v", notReadyReplicas)
		setCondition(ctx, v1.KeeperConditionTypeDegraded, metav1.ConditionTrue, v1.KeeperConditionReasonReplicasNotReady, message)
	} else {
		setCondition(ctx, v1.KeeperConditionTypeDegraded, metav1.ConditionFalse, v1.KeeperConditionReasonReplicasReady, "")
	}

	if len(notUpdatedReplicas) > 0 {
		slices.Sort(notUpdatedReplicas)
		message := fmt.Sprintf("Replicas has pending updates: %v", notUpdatedReplicas)
		setCondition(ctx, v1.KeeperConditionTypeConfigurationInSync, metav1.ConditionFalse, v1.KeeperConditionReasonConfigurationChanged, message)
	} else {
		setCondition(ctx, v1.KeeperConditionTypeConfigurationInSync, metav1.ConditionTrue, v1.KeeperConditionReasonUpToDate, "")
	}

	exists := len(ctx.KeeperCluster.Status.Replicas)
	expected := int(ctx.KeeperCluster.Replicas())

	if exists < expected {
		setCondition(ctx, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.KeeperConditionReasonScalingUp, "Cluster has less replicas that requested")
	} else if exists > expected {
		setCondition(ctx, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.KeeperConditionReasonScalingDown, "Cluster has more replicas that requested")
	} else {
		setCondition(ctx, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.KeeperConditionReasonUpToDate, "")
	}

	if len(notUpdatedReplicas) == 0 && exists == expected {
		ctx.KeeperCluster.Status.CurrentRevision = ctx.KeeperCluster.Status.UpdateRevision
	}

	var status metav1.ConditionStatus
	var reason v1.KeeperConditionReason
	var message string

	switch exists {
	case 0:
		status = metav1.ConditionFalse
		reason = v1.KeeperConditionReasonNoLeader
		message = "No replicas"
	case 1:
		if len(replicasByMode[ModeStandalone]) == 1 {
			status = metav1.ConditionTrue
			reason = v1.KeeperConditionReasonStandaloneReady
			message = "Standalone Keeper is ready"
		} else {
			status = metav1.ConditionFalse
			reason = v1.KeeperConditionReasonNoLeader
			message = "No replicas in standalone mode"
		}
	default:
		requiredFollowersForQuorum := int(math.Ceil(float64(exists)/2)) - 1

		switch {
		case len(replicasByMode[ModeStandalone]) > 0:
			status = metav1.ConditionFalse
			reason = v1.KeeperConditionReasonInconsistentState
			slices.Sort(replicasByMode[ModeStandalone])
			message = fmt.Sprintf("Standalone replica in cluster: %v", replicasByMode[ModeStandalone])
		case len(replicasByMode[ModeLeader]) > 1:
			status = metav1.ConditionFalse
			reason = v1.KeeperConditionReasonInconsistentState
			slices.Sort(replicasByMode[ModeLeader])
			message = fmt.Sprintf("Multiple leaders in cluster: %v", replicasByMode[ModeLeader])
		case len(replicasByMode[ModeLeader]) == 0:
			status = metav1.ConditionFalse
			reason = v1.KeeperConditionReasonNoLeader
			message = "No leader in the cluster"
		case len(replicasByMode[ModeFollower]) < requiredFollowersForQuorum:
			status = metav1.ConditionFalse
			reason = v1.KeeperConditionReasonNotEnoughFollowers
			message = fmt.Sprintf("Not enough followers in cluster: %d, required: %d", len(replicasByMode[ModeFollower]), requiredFollowersForQuorum)
		default:
			status = metav1.ConditionTrue
			reason = v1.KeeperConditionReasonClusterReady
			message = "Cluster is ready"
		}
	}

	setCondition(ctx, v1.KeeperConditionTypeReady, status, reason, message)
	return &ctrl.Result{}, nil
}

func (r *ClusterReconciler) updateConfigMap(ctx reconcileContext, configMap *corev1.ConfigMap) (bool, error) {
	log := ctx.Log.With("configmap", configMap.Name)

	if err := ctrl.SetControllerReference(ctx.KeeperCluster, configMap, r.Scheme); err != nil {
		return false, fmt.Errorf("set ConfigMap %q controller reference: %w", configMap.Name, err)
	}

	foundConfigMap := corev1.ConfigMap{}
	if err := r.Get(ctx.Context, types.NamespacedName{
		Namespace: configMap.Namespace,
		Name:      configMap.Name,
	}, &foundConfigMap); err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get ConfigMap %q: %w", configMap.Name, err)
		}

		ctx.Log.Info("ConfigMap not found, creating", "configmap", configMap.Name)
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			return r.Create(ctx.Context, configMap)
		})

		if err != nil {
			return false, fmt.Errorf("create ConfigMap %q: %w", configMap.Name, err)
		}

		return true, nil
	}

	if reflect.DeepEqual(foundConfigMap.Data, configMap.Data) &&
		reflect.DeepEqual(foundConfigMap.BinaryData, configMap.BinaryData) {
		log.Debug("ConfigMap is up to date")
		return false, nil
	}
	log.Debug(fmt.Sprintf("ConfigMap changed:\nData diff:\n%s\nBinary Data diff:\n%s",
		cmp.Diff(foundConfigMap.Data, configMap.Data),
		cmp.Diff(foundConfigMap.BinaryData, configMap.BinaryData)),
	)

	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return r.Update(ctx.Context, configMap)
	}); err != nil {
		return false, fmt.Errorf("update ConfigMap %q: %w", configMap.Name, err)
	}

	return true, nil
}

func (r *ClusterReconciler) updateReplica(ctx reconcileContext, replicaID string) (*ctrl.Result, error) {
	log := ctx.Log.With("replica_id", replicaID)
	log.Info("updating replica")

	configMap, err := TemplateConfigMap(ctx.KeeperCluster, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q ConfigMap: %w", replicaID, err)
	}

	configChanged, err := r.updateConfigMap(ctx, configMap)
	if err != nil {
		return nil, fmt.Errorf("update replica %q ConfigMap: %w", replicaID, err)
	}

	statefulSet := TemplateStatefulSet(ctx.KeeperCluster, replicaID)
	if err := ctrl.SetControllerReference(ctx.KeeperCluster, statefulSet, r.Scheme); err != nil {
		return nil, fmt.Errorf("set replica %q StatefulSet controller reference: %w", replicaID, err)
	}

	replica := ctx.Replica(replicaID)
	if replica.StatefulSet == nil {
		log.Info("replica StatefulSet not found, creating", "stateful_set", statefulSet.Name)
		util.AddObjectConfigHash(statefulSet, ctx.KeeperCluster.Status.ConfigurationRevision)
		util.AddHashWithKeyToAnnotations(statefulSet, util.AnnotationSpecHash, ctx.KeeperCluster.Status.StatefulSetRevision)

		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			return r.Create(ctx.Context, statefulSet)
		})

		if err != nil {
			return nil, fmt.Errorf("create replica %q StatefulSet %q: %w", replicaID, statefulSet.Name, err)
		}

		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// Check if the StatefulSet is outdated and needs to be recreated
	v, err := semver.Parse(replica.StatefulSet.Annotations[util.AnnotationStatefulSetVersion])
	if err != nil || BreakingStatefulSetVersion.GT(v) {
		log.Warn(fmt.Sprintf("Removing the StatefulSet because of a breaking change. Found version: %v, expected version: %v", v, BreakingStatefulSetVersion))
		if err := r.Delete(ctx.Context, replica.StatefulSet); err != nil {
			return nil, fmt.Errorf("delete StatefulSet: %w", err)
		}

		return &ctrl.Result{Requeue: true}, nil
	}

	stsNeedsUpdate := replica.HasStatefulSetDiff(ctx)

	// Trigger Pod restart if config changed
	if replica.HasConfigMapDiff(ctx) {
		// Use same way as Kubernetes for force restarting Pods one by one
		// (https://github.com/kubernetes/kubernetes/blob/22a21f974f5c0798a611987405135ab7e62502da/staging/src/k8s.io/kubectl/pkg/polymorphichelpers/objectrestarter.go#L41)
		// Not included by default in the StatefulSet so that hash-diffs work correctly
		log.Info("forcing keeper Pod restart, because of config changes")
		statefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt] = time.Now().Format(time.RFC3339)
		util.AddObjectConfigHash(replica.StatefulSet, ctx.KeeperCluster.Status.ConfigurationRevision)
		stsNeedsUpdate = true
	} else if restartedAt, ok := replica.StatefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt]; ok {
		statefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt] = restartedAt
	}

	if !stsNeedsUpdate {
		log.Debug("StatefulSet is up to date")
		if configChanged {
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}

		return nil, nil
	}

	log.Info("updating replica StatefulSet", "stateful_set", statefulSet.Name)
	replica.StatefulSet.Spec = statefulSet.Spec
	replica.StatefulSet.Annotations = util.MergeMaps(replica.StatefulSet.Annotations, statefulSet.Annotations)
	replica.StatefulSet.Labels = util.MergeMaps(replica.StatefulSet.Labels, statefulSet.Labels)
	util.AddHashWithKeyToAnnotations(replica.StatefulSet, util.AnnotationSpecHash, ctx.KeeperCluster.Status.StatefulSetRevision)
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return r.Update(ctx.Context, replica.StatefulSet)
	}); err != nil {
		return nil, fmt.Errorf("update replica %q StatefulSet %q: %w", replicaID, statefulSet.Name, err)
	}

	return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
}

var podErrorStatuses = []string{"ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff"}

func (r *ClusterReconciler) checkReplicaPodError(ctx reconcileContext, replicaID string, sts *appsv1.StatefulSet) (bool, error) {
	var keeperPod corev1.Pod
	if err := r.Get(ctx.Context, types.NamespacedName{
		Namespace: sts.Namespace,
		Name:      fmt.Sprintf("%s-0", sts.Name),
	}, &keeperPod); err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get keeper %q pod: %w", replicaID, err)
		}

		ctx.Log.Info("pod is not exists", "replica_id", replicaID, "stateful_set", sts.Name)
		return false, nil
	}

	isError := false
	for _, status := range keeperPod.Status.ContainerStatuses {
		if status.State.Waiting != nil && slices.Contains(podErrorStatuses, status.State.Waiting.Reason) {
			ctx.Log.Info("pod in error state", "pod", keeperPod.Name, "reason", status.State.Waiting.Reason)
			isError = true
			break
		}
	}

	return isError, nil
}

func (r *ClusterReconciler) upsertStatus(ctx reconcileContext) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		crdInstance := &v1.KeeperCluster{}
		if err := r.Reader.Get(ctx.Context, ctx.KeeperCluster.GetNamespacedName(), crdInstance); err != nil {
			return err
		}
		preStatus := crdInstance.Status.DeepCopy()

		if reflect.DeepEqual(*preStatus, ctx.KeeperCluster.Status) {
			ctx.Log.Info("statuses are equal, nothing to do")
			return nil
		}
		ctx.Log.Debug(fmt.Sprintf("status difference:\n%s", cmp.Diff(*preStatus, ctx.KeeperCluster.Status)))
		crdInstance.Status = ctx.KeeperCluster.Status
		return r.Status().Update(ctx.Context, crdInstance)
	})
}

func setConditions(
	ctx reconcileContext,
	conditions []metav1.Condition,
) {
	if ctx.KeeperCluster.Status.Conditions == nil {
		ctx.KeeperCluster.Status.Conditions = []metav1.Condition{}
	}

	for _, condition := range conditions {
		if meta.SetStatusCondition(&ctx.KeeperCluster.Status.Conditions, condition) {
			ctx.Log.Debug("condition changed", "condition", condition.Type, "condition_value", condition.Status)
		}
	}
}

func setCondition(
	ctx reconcileContext,
	condType v1.KeeperConditionType,
	status metav1.ConditionStatus,
	reason v1.KeeperConditionReason,
	message string,
) {
	setConditions(ctx, []metav1.Condition{ctx.NewCondition(condType, status, reason, message)})
}
