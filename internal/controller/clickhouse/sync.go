package clickhouse

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
)

func compareReplicaID(a, b v1.ClickHouseReplicaID) int {
	if res := cmp.Compare(a.ShardID, b.ShardID); res != 0 {
		return res
	}

	return cmp.Compare(a.Index, b.Index)
}

type replicaState struct {
	Error       bool `json:"error"`
	StatefulSet *appsv1.StatefulSet
	PVC         *corev1.PersistentVolumeClaim
	Pinged      bool
	Version     string
}

func (r replicaState) Updated() bool {
	if r.StatefulSet == nil {
		return false
	}

	return r.StatefulSet.Generation == r.StatefulSet.Status.ObservedGeneration &&
		r.StatefulSet.Status.UpdateRevision == r.StatefulSet.Status.CurrentRevision
}

func (r replicaState) Ready() bool {
	if r.StatefulSet == nil {
		return false
	}

	return r.Pinged && r.StatefulSet.Status.ReadyReplicas == 1 // Not reliable, but allows to wait until pod is `green`
}

func (r replicaState) HasDiff(rev chctrl.RevisionState) bool {
	return rev.ReplicaHasDiff(r.StatefulSet, r.PVC)
}

func (r replicaState) UpdateStage(rev chctrl.RevisionState) chctrl.ReplicaUpdateStage {
	if r.StatefulSet == nil {
		return chctrl.StageNotExists
	}

	if r.Error {
		return chctrl.StageError
	}

	if !r.Updated() {
		return chctrl.StageUpdating
	}

	if r.HasDiff(rev) {
		return chctrl.StageHasDiff
	}

	if !r.Ready() {
		return chctrl.StageNotReadyUpToDate
	}

	return chctrl.StageUpToDate
}

type statusManager = chctrl.StatusManager[v1.ClickHouseClusterStatus, *v1.ClickHouseClusterStatus, *v1.ClickHouseCluster]

type clickhouseReconciler struct {
	chctrl.Controller
	statusManager
	chctrl.ResourceManager

	Dialer    ctrlutil.DialContextFunc
	Checker   *upgrade.Checker
	EnablePDB bool

	Cluster      *v1.ClickHouseCluster
	ReplicaState map[v1.ClickHouseReplicaID]replicaState

	// Populated by reconcileClusterRevisions.
	keeper v1.KeeperCluster
	// Loaded and templated by reconcileClusterSecret. Requires version probe to complete.
	secret    corev1.Secret
	commander *commander

	versionProbe   chctrl.VersionProbeResult
	readyReplicas  []v1.ClickHouseReplicaID
	revs           chctrl.RevisionState
	unsyncedShards map[int32]bool // Populated by reconcileDatabaseSync, consumed by reconcileCleanUp.
}

func (r *clickhouseReconciler) sync(ctx context.Context, log ctrlutil.Logger) (ctrl.Result, error) {
	log.Info("Enter ClickHouse Reconcile", "spec", r.Cluster.Spec, "status", r.Cluster.Status)

	r.SetUnknownConditions(v1.ConditionReasonStepFailed, "Reconcile stopped before condition evaluation",
		[]v1.ConditionType{
			v1.ConditionTypeReplicaStartupSucceeded,
			v1.ConditionTypeHealthy,
			v1.ConditionTypeClusterSizeAligned,
			v1.ConditionTypeConfigurationInSync,
			v1.ConditionTypeVersionInSync,
			v1.ConditionTypeVersionUpgraded,
			v1.ConditionTypeReady,
			v1.ClickHouseConditionTypeSchemaInSync,
		})

	defer func() {
		if r.commander != nil {
			r.commander.Close()
		}
	}()

	steps := []chctrl.ReconcileStep{
		{Name: "VersionProbe", Fn: r.reconcileVersionProbe, Always: true},
		{Name: "Service", Fn: r.reconcileService, Always: true},
		{Name: "ClusterSecret", Fn: r.reconcileClusterSecret, Always: true},
		{Name: "ActiveReplicaStatus", Fn: r.reconcileActiveReplicaStatus, Always: true},
		{Name: "ClusterRevisions", Fn: r.reconcileClusterRevisions, Always: true},
		{Name: "ReplicaResources", Fn: r.reconcileReplicaResources},
		{Name: "DatabaseSync", Fn: r.reconcileDatabaseSync},
		{Name: "CleanUp", Fn: r.reconcileCleanUp},
	}

	if r.EnablePDB {
		steps = append(steps,
			chctrl.ReconcileStep{Name: "PodDisruptionBudget", Fn: r.reconcilePodDisruptionBudget, Always: true},
		)
	}

	result, err := chctrl.RunSteps(ctx, log, steps)
	if err != nil {
		if k8serrors.IsConflict(err) {
			log.Error(err, "update conflict for resource, reschedule to retry")
			return ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
		}

		if k8serrors.IsAlreadyExists(err) {
			log.Error(err, "create already existed resource, reschedule to retry")
			return ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
		}

		log.Error(err, "unexpected error, setting conditions to unknown and rescheduling reconciliation to try again")

		r.SetCondition(metav1.Condition{
			Type:    v1.ConditionTypeReconcileSucceeded,
			Status:  metav1.ConditionFalse,
			Reason:  v1.ConditionReasonStepFailed,
			Message: "Reconcile returned error",
		})

		if updateErr := r.UpsertStatus(ctx, log); updateErr != nil {
			log.Error(updateErr, "failed to update status")
		}

		return ctrl.Result{}, fmt.Errorf("reconcile steps: %w", err)
	}

	r.SetCondition(metav1.Condition{
		Type:    v1.ConditionTypeReconcileSucceeded,
		Status:  metav1.ConditionTrue,
		Reason:  v1.ConditionReasonReconcileFinished,
		Message: "Reconcile succeeded",
	})
	log.Info("reconciliation loop end", "result", result)

	if err := r.UpsertStatus(ctx, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after reconciliation: %w", err)
	}

	return result, nil
}

func (r *clickhouseReconciler) reconcileService(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	service := templateHeadlessService(r.Cluster)
	if _, err := r.ReconcileService(ctx, log, service, v1.EventActionReconciling); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("reconcile service resource: %w", err)
	}

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) reconcilePodDisruptionBudget(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	pdbIgnored := r.Cluster.Spec.PodDisruptionBudget.Ignored()
	pdbEnabled := r.Cluster.Spec.PodDisruptionBudget.Enabled()

	if pdbEnabled {
		for shard := range r.Cluster.Shards() {
			pdb := templatePodDisruptionBudget(r.Cluster, shard)
			if _, err := r.ReconcilePodDisruptionBudget(ctx, log, pdb, v1.EventActionReconciling); err != nil {
				return chctrl.StepResult{}, fmt.Errorf("reconcile PodDisruptionBudget resource for shard %d: %w", shard, err)
			}
		}
	}

	if !pdbIgnored {
		var disruptionBudgets policyv1.PodDisruptionBudgetList
		if err := r.GetClient().List(ctx, &disruptionBudgets,
			ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())); err != nil {
			return chctrl.StepResult{}, fmt.Errorf("list PodDisruptionBudgets: %w", err)
		}

		for _, pdb := range disruptionBudgets.Items {
			shardID, err := strconv.Atoi(pdb.Labels[ctrlutil.LabelClickHouseShardID])
			if err != nil {
				log.Warn("failed to get shard ID from PodDisruptionBudget labels", "pdb", pdb.Name, "error", err)
				continue
			}

			if !pdbEnabled || shardID >= int(r.Cluster.Shards()) {
				log.Info("removing PodDisruptionBudget", "pdb", pdb.Name)

				if err := r.Delete(ctx, &pdb, v1.EventActionReconciling); err != nil {
					return chctrl.StepResult{}, fmt.Errorf("remove shard %d: %w", shardID, err)
				}
			}
		}
	}

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) reconcileClusterSecret(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	secretExists := true
	if err := r.GetClient().Get(ctx, types.NamespacedName{
		Namespace: r.Cluster.Namespace,
		Name:      r.Cluster.SecretName(),
	}, &r.secret); err != nil {
		if !k8serrors.IsNotFound(err) {
			return chctrl.StepResult{}, fmt.Errorf("get ClickHouse cluster secret %q: %w", r.Cluster.SecretName(), err)
		}

		secretExists = false
	} else {
		r.commander = newCommander(log, r.Cluster, &r.secret, r.Dialer)
	}

	if !r.versionProbe.Completed() {
		log.Info("version probe is not completed yet, skipping cluster secret templating")

		return chctrl.StepBlocked(chctrl.RequeueOnRefreshTimeout), nil
	}

	var isSecretUpdated bool

	r.secret, isSecretUpdated = templateClusterSecrets(r.Cluster, r.secret)
	if !isSecretUpdated {
		log.Debug("cluster secret is up to date")
		return chctrl.StepContinue(), nil
	}

	if err := ctrl.SetControllerReference(r.Cluster, &r.secret, r.GetScheme()); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("set controller reference for cluster secret %q: %w", r.Cluster.SecretName(), err)
	}

	// Create or recreate commander with new credentials
	r.commander = newCommander(log, r.Cluster, &r.secret, r.Dialer)

	if !secretExists {
		log.Info("cluster secret not found, creating", "secret", r.Cluster.SecretName())

		if err := r.Create(ctx, &r.secret, v1.EventActionReconciling); err != nil {
			return chctrl.StepResult{}, fmt.Errorf("create cluster secret: %w", err)
		}

		return chctrl.StepContinue(), nil
	}

	if err := r.Update(ctx, &r.secret, v1.EventActionReconciling); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("update cluster secret: %w", err)
	}

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) reconcileVersionProbe(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	probeResult, err := r.VersionProbe(ctx, log, chctrl.VersionProbeConfig{
		Binary:            "clickhouse-server",
		Labels:            r.Cluster.Spec.Labels,
		Annotations:       r.Cluster.Spec.Annotations,
		PodTemplate:       r.Cluster.Spec.PodTemplate,
		ContainerTemplate: r.Cluster.Spec.ContainerTemplate,
		VersionProbe:      r.Cluster.Spec.VersionProbeTemplate,
	})
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("run version probe: %w", err)
	}

	r.versionProbe = probeResult
	if probeResult.Completed() {
		r.Cluster.Status.Version = probeResult.Version
	}

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) reconcileActiveReplicaStatus(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	listOpts := ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())

	var statefulSets appsv1.StatefulSetList
	if err := r.GetClient().List(ctx, &statefulSets, listOpts); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list StatefulSets: %w", err)
	}

	execResults := ctrlutil.ExecuteParallel(statefulSets.Items, func(sts appsv1.StatefulSet) (v1.ClickHouseReplicaID, replicaState, error) {
		id, err := v1.ClickHouseIDFromLabels(sts.Labels)
		if err != nil {
			log.Error(err, "get replica ID from StatefulSet labels", "statefulset", sts.Name)
			return v1.ClickHouseReplicaID{}, replicaState{}, fmt.Errorf("get replica ID from StatefulSet labels: %w", err)
		}

		hasError, err := chctrl.CheckPodError(ctx, log, r.GetClient(), &sts)
		if err != nil {
			log.Warn("failed to check replica pod error", "statefulset", sts.Name, "error", err)

			hasError = true
		}

		pinged := false
		version := ""

		if !hasError && sts.Status.ReadyReplicas > 0 && r.commander != nil {
			ctx, cancel := context.WithTimeout(ctx, chctrl.LoadReplicaStateTimeout)
			defer cancel()

			version, err = r.commander.Version(ctx, id)
			if err != nil {
				log.Debug("failed to query version on replica", "replica_id", id, "error", err)
			} else {
				pinged = true
			}
		}

		var pvc *corev1.PersistentVolumeClaim
		if r.Cluster.Spec.DataVolumeClaimSpec != nil {
			pvc, err = r.GetPVCByStatefulSet(ctx, log.With("replica_id", id), &sts)
			if err != nil {
				log.Error(err, "failed to get PVC for replica", "replica_id", id)
			}
		}

		log.Debug("load replica state done", "replica_id", id, "statefulset", sts.Name)

		return id, replicaState{
			StatefulSet: &sts,
			PVC:         pvc,
			Error:       hasError,
			Pinged:      pinged,
			Version:     version,
		}, nil
	})

	for id, res := range execResults {
		if res.Err != nil {
			log.Info("failed to load replica state", "error", res.Err, "replica_id", id)
			continue
		}

		if _, ok := r.ReplicaState[id]; !ok {
			r.ReplicaState[id] = res.Result
		} else {
			log.Debug(fmt.Sprintf("multiple StatefulSets for single replica %v", id),
				"replica_id", id, "statefulset", res.Result.StatefulSet.Name)
		}
	}

	r.evaluateReplicaConditions()

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) reconcileClusterRevisions(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if r.Cluster.Status.ObservedGeneration != r.Cluster.Generation {
		r.Cluster.Status.ObservedGeneration = r.Cluster.Generation
		log.Debug(fmt.Sprintf("observed new CR generation %d", r.Cluster.Generation))
	}

	updateRevision, err := ctrlutil.DeepHashObject(r.Cluster.Spec)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("get current spec revision: %w", err)
	}

	if updateRevision != r.Cluster.Status.UpdateRevision {
		r.Cluster.Status.UpdateRevision = updateRevision
		log.Debug(fmt.Sprintf("observed new CR revision %q", updateRevision))
	}

	if err := r.GetClient().Get(ctx, types.NamespacedName{
		Namespace: r.Cluster.Namespace,
		Name:      r.Cluster.Spec.KeeperClusterRef.Name,
	}, &r.keeper); err != nil {
		if k8serrors.IsNotFound(err) {
			log.Debug("keeper cluster not found, waiting")

			return chctrl.StepBlocked(chctrl.RequeueOnRefreshTimeout), nil
		}

		return chctrl.StepResult{}, fmt.Errorf("get keeper cluster: %w", err)
	}

	if cond := meta.FindStatusCondition(r.keeper.Status.Conditions, v1.ConditionTypeReady); cond == nil || cond.Status != metav1.ConditionTrue {
		if cond == nil {
			log.Info("keeper cluster is not ready")
		} else {
			log.Info("keeper cluster is not ready", "reason", cond.Reason, "message", cond.Message)
		}
	}

	if !r.versionProbe.Completed() {
		log.Info("version probe is not completed yet, waiting")

		return chctrl.StepBlocked(chctrl.RequeueOnRefreshTimeout), nil
	}

	r.revs.ConfigurationRevision, err = getConfigurationRevision(r)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("get configuration revision: %w", err)
	}

	if r.revs.ConfigurationRevision != r.Cluster.Status.ConfigurationRevision {
		r.Cluster.Status.ConfigurationRevision = r.revs.ConfigurationRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", r.revs.ConfigurationRevision))
	}

	r.revs.StatefulSetRevision, err = getStatefulSetRevision(r)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("get StatefulSet revision: %w", err)
	}

	if r.revs.StatefulSetRevision != r.Cluster.Status.StatefulSetRevision {
		r.Cluster.Status.StatefulSetRevision = r.revs.StatefulSetRevision
		log.Debug(fmt.Sprintf("observed new StatefulSet revision %q", r.revs.StatefulSetRevision))
	}

	if r.Cluster.Spec.DataVolumeClaimSpec != nil {
		r.revs.HasPVCSpec = true

		r.revs.PVCRevision, err = ctrlutil.DeepHashObject(r.Cluster.Spec.DataVolumeClaimSpec)
		if err != nil {
			return chctrl.StepResult{}, fmt.Errorf("get PVC revision: %w", err)
		}
	}

	var notUpdatedIDs []string
	for shard := range r.Cluster.Shards() {
		for index := range r.Cluster.Replicas() {
			id := v1.ClickHouseReplicaID{ShardID: shard, Index: index}
			replica := r.ReplicaState[id]

			if replica.HasDiff(r.revs) || !replica.Updated() {
				notUpdatedIDs = append(notUpdatedIDs, id.String())
			}
		}
	}

	r.SetCondition(chctrl.ConfigSyncCondition(notUpdatedIDs))

	exists := len(r.ReplicaState)
	expected := int(r.Cluster.Replicas() * r.Cluster.Shards())

	if len(notUpdatedIDs) == 0 && exists == expected {
		r.Cluster.Status.CurrentRevision = r.Cluster.Status.UpdateRevision
	}

	replicaVersions := make(map[string]string, len(r.ReplicaState))
	for id, replica := range r.ReplicaState {
		replicaVersions[id.String()] = replica.Version
	}

	{
		cond, event := chctrl.GetVersionSyncCondition(r.versionProbe, replicaVersions, len(notUpdatedIDs) > 0)
		r.SetCondition(cond, event...)
	}

	if r.Checker != nil {
		cond, event := chctrl.GetUpgradeCondition(*r.Checker, r.versionProbe, r.Cluster.Spec.UpgradeChannel)
		r.SetCondition(cond, event...)
	} else {
		meta.RemoveStatusCondition(r.Cluster.GetStatus().GetConditions(), v1.ConditionTypeVersionUpgraded)
	}

	return chctrl.StepContinue(), nil
}

// reconcileReplicaResources performs update on replicas ConfigMap and StatefulSet.
// If there are replicas that has no created StatefulSet, creates immediately.
// If all replicas exists performs rolling upgrade, with the following order preferences:
// NotExists -> CrashLoop/ImagePullErr -> OnlySts -> OnlyConfig -> Any.
func (r *clickhouseReconciler) reconcileReplicaResources(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	highestStage := chctrl.StageUpToDate

	var replicasInStatus []v1.ClickHouseReplicaID

	for id := range r.Cluster.ReplicaIDs() {
		stage := r.ReplicaState[id].UpdateStage(r.revs)
		if stage == highestStage {
			replicasInStatus = append(replicasInStatus, id)
			continue
		}

		if stage > highestStage {
			highestStage = stage
			replicasInStatus = []v1.ClickHouseReplicaID{id}
		}
	}

	var requeueAfter time.Duration

	switch highestStage {
	case chctrl.StageUpToDate:
		log.Info("all replicas are up to date")
		return chctrl.StepContinue(), nil
	case chctrl.StageNotReadyUpToDate, chctrl.StageUpdating:
		log.Info("waiting for updated replicas to become ready", "replicas", replicasInStatus, "priority", highestStage.String())

		requeueAfter = chctrl.RequeueOnRefreshTimeout
	case chctrl.StageHasDiff:
		// Leave one replica to rolling update. replicasInStatus must not be empty.
		// Prefer replicas with higher id.
		chosenReplica := replicasInStatus[0]
		for _, id := range replicasInStatus {
			if compareReplicaID(id, chosenReplica) == 1 {
				chosenReplica = id
			}
		}

		log.Info(fmt.Sprintf("updating chosen replica %v with priority %s: %v", chosenReplica, highestStage.String(), replicasInStatus))
		replicasInStatus = []v1.ClickHouseReplicaID{chosenReplica}

	case chctrl.StageNotExists, chctrl.StageError:
		log.Info(fmt.Sprintf("updating replicas with priority %s: %v", highestStage.String(), replicasInStatus))
	}

	for _, id := range replicasInStatus {
		replicaResult, err := r.updateReplica(ctx, log, id)
		if err != nil {
			return chctrl.StepResult{}, fmt.Errorf("update replica %s: %w", id, err)
		}

		if replicaResult != nil && replicaResult.RequeueAfter > 0 {
			if requeueAfter == 0 || replicaResult.RequeueAfter < requeueAfter {
				requeueAfter = replicaResult.RequeueAfter
			}
		}
	}

	return chctrl.StepRequeue(requeueAfter), nil
}

func (r *clickhouseReconciler) reconcileDatabaseSync(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if !r.Cluster.Spec.Settings.EnableDatabaseSync {
		log.Debug("database sync is disabled, skipping")
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionTrue,
			Reason:  v1.ClickHouseConditionSchemaSyncDisabled,
			Message: "Database schema sync is disabled",
		})

		return chctrl.StepContinue(), nil
	}

	if r.commander == nil {
		log.Debug("commander is not ready, skipping")
		return chctrl.StepContinue(), nil
	}

	var (
		allReplicasReady       = len(r.readyReplicas) == len(r.ReplicaState)
		allDefaultDatabaseSet  = allReplicasReady
		allDatabasesSynced     = allReplicasReady
		staleReplicasCleanedUp = true
	)

	if !r.commander.EnsureDefaultDatabaseEngine(ctx, log, r.readyReplicas) {
		allDefaultDatabaseSet = false
	}

	if len(r.readyReplicas) >= 2 {
		if !r.commander.SyncDatabases(ctx, log, r.readyReplicas) {
			allDatabasesSynced = false
		}
	} else {
		log.Info("no replicas to replicate schema, skipping")
	}

	shardHasReadyReplica := map[int32]bool{}
	for _, id := range r.readyReplicas {
		shardHasReadyReplica[id.ShardID] = true
	}

	// Sync only shards that are going to drop some replicas.
	runningReplicas := map[v1.ClickHouseReplicaID]struct{}{}
	shardsToSyncSet := map[int32]struct{}{}

	for id := range r.ReplicaState {
		runningReplicas[id] = struct{}{}

		if id.ShardID < r.Cluster.Shards() && id.Index >= r.Cluster.Replicas() {
			if shardHasReadyReplica[id.ShardID] {
				shardsToSyncSet[id.ShardID] = struct{}{}
			} else {
				log.Debug("no ready replicas in shard, skipping sync", "shard", id.ShardID)
				r.unsyncedShards[id.ShardID] = true
			}
		}
	}

	syncRes := ctrlutil.ExecuteParallel(slices.Collect(maps.Keys(shardsToSyncSet)), func(shardID int32) (int32, struct{}, error) {
		log.Info("pre scale-down shard sync", "shard_id", shardID)
		return shardID, struct{}{}, r.commander.SyncShard(ctx, log, shardID)
	})

	for id, res := range syncRes {
		if res.Err != nil {
			log.Info("failed to sync shard", "shard_id", id, "error", res.Err)
			r.unsyncedShards[id] = true
		}
	}

	if len(r.readyReplicas) > 0 {
		if err := r.commander.CleanupDatabaseReplicas(ctx, log, runningReplicas); err != nil {
			log.Warn("failed to cleanup database replicas", "error", err)

			staleReplicasCleanedUp = false
		}
	}

	switch {
	case !allDefaultDatabaseSet || !allDatabasesSynced:
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionFalse,
			Reason:  v1.ClickHouseConditionDatabasesNotCreated,
			Message: "Some databases are not created on all replicas",
		})

		return chctrl.StepRequeue(chctrl.RequeueOnRefreshTimeout), nil

	case !staleReplicasCleanedUp:
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionFalse,
			Reason:  v1.ClickHouseConditionReplicasNotCleanedUp,
			Message: "Some stale replicas are not cleaned up",
		})

		return chctrl.StepRequeue(chctrl.RequeueOnRefreshTimeout), nil

	default:
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionTrue,
			Reason:  v1.ClickHouseConditionReplicasInSync,
			Message: "All replicas are in sync",
		})
	}

	return chctrl.StepContinue(), nil
}

type replicaResources struct {
	cfg *corev1.ConfigMap
	sts *appsv1.StatefulSet
}

func (r *clickhouseReconciler) reconcileCleanUp(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	var (
		configMaps corev1.ConfigMapList

		listOpts         = ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())
		replicasToRemove = map[int32]map[int32]replicaResources{}
	)

	if err := r.GetClient().List(ctx, &configMaps, listOpts); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		id, err := v1.ClickHouseIDFromLabels(configMap.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from ConfigMap labels", "configmap", configMap.Name, "error", err)
			continue
		}

		if id.ShardID < r.Cluster.Shards() && id.Index < r.Cluster.Replicas() {
			continue
		}

		if _, ok := replicasToRemove[id.ShardID]; !ok {
			replicasToRemove[id.ShardID] = map[int32]replicaResources{}
		}

		state := replicasToRemove[id.ShardID][id.Index]
		state.cfg = &configMap
		replicasToRemove[id.ShardID][id.Index] = state
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.GetClient().List(ctx, &statefulSets, listOpts); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		id, err := v1.ClickHouseIDFromLabels(sts.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from StatefulSet labels", "statefulset", sts.Name, "error", err)
			continue
		}

		if id.ShardID < r.Cluster.Shards() && id.Index < r.Cluster.Replicas() {
			continue
		}

		if _, ok := replicasToRemove[id.ShardID]; !ok {
			replicasToRemove[id.ShardID] = map[int32]replicaResources{}
		}

		state := replicasToRemove[id.ShardID][id.Index]
		state.sts = &sts
		replicasToRemove[id.ShardID][id.Index] = state
	}

	for shardID, replicas := range replicasToRemove {
		inSync := !r.unsyncedShards[shardID]

		for index, res := range replicas {
			id := v1.ClickHouseReplicaID{ShardID: shardID, Index: index}

			// Always delete orphaned ConfigMaps.
			if res.cfg != nil && (inSync || res.sts == nil) {
				log.Info("removing replica configmap", "replica_id", id, "configmap", res.cfg.Name)

				if err := r.Delete(ctx, res.cfg, v1.EventActionReconciling); err != nil {
					log.Error(err, "failed to delete replica configmap", "replica_id", id, "configmap", res.cfg.Name)
				}
			}

			// Delete StatefulSets only if the entire shard is removed or shard sync succeeded.
			if res.sts == nil {
				continue
			}

			if !inSync {
				log.Info("shard sync failed, skipping replica deletion", "replica_id", id)
				continue
			}

			log.Info("removing replica statefulset", "replica_id", id, "statefulset", res.sts.Name)

			if err := r.Delete(ctx, res.sts, v1.EventActionReconciling); err != nil {
				log.Error(err, "failed to delete replica statefulset", "replica_id", id, "statefulset", res.sts.Name)
			}
		}
	}

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) evaluateReplicaConditions() {
	var (
		errorIDs, notReadyIDs []string
		notReadyShards        []int32
	)

	for shard := range r.Cluster.Shards() {
		hasReady := false
		for index := range r.Cluster.Replicas() {
			id := v1.ClickHouseReplicaID{ShardID: shard, Index: index}
			replica := r.ReplicaState[id]

			if replica.Error {
				errorIDs = append(errorIDs, id.String())
			}

			if !replica.Ready() {
				notReadyIDs = append(notReadyIDs, id.String())
			} else {
				r.readyReplicas = append(r.readyReplicas, id)

				hasReady = true
			}
		}

		if !hasReady {
			notReadyShards = append(notReadyShards, shard)
		}
	}

	r.Cluster.Status.ReadyReplicas = int32(len(r.readyReplicas)) //nolint:gosec

	exists := len(r.ReplicaState)
	expected := int(r.Cluster.Replicas() * r.Cluster.Shards())

	r.SetCondition(chctrl.ReplicaStartupCondition(errorIDs))
	r.SetCondition(chctrl.HealthyCondition(notReadyIDs))
	r.SetCondition(chctrl.ClusterSizeCondition(exists, expected))

	if len(notReadyShards) == 0 {
		r.SetCondition(
			metav1.Condition{Type: v1.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: v1.ClickHouseConditionAllShardsReady, Message: "All shards are ready"},
			chctrl.EventSpec{Type: corev1.EventTypeNormal, Reason: v1.EventReasonClusterReady, Action: v1.EventActionBecameReady, Message: "ClickHouse cluster is ready"},
		)
	} else {
		slices.Sort(notReadyShards)
		message := fmt.Sprintf("Not Ready shards: %v", notReadyShards)
		r.SetCondition(
			metav1.Condition{Type: v1.ConditionTypeReady, Status: metav1.ConditionFalse, Reason: v1.ClickHouseConditionSomeShardsNotReady, Message: message},
			chctrl.EventSpec{Type: corev1.EventTypeWarning, Reason: v1.EventReasonClusterNotReady, Action: v1.EventActionBecameNotReady, Message: message},
		)
	}
}

func (r *clickhouseReconciler) updateReplica(ctx context.Context, log ctrlutil.Logger, id v1.ClickHouseReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", id)
	log.Info("updating replica")

	configMap, err := templateConfigMap(r, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %s ConfigMap: %w", id, err)
	}

	statefulSet, err := templateStatefulSet(r, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %s StatefulSet: %w", id, err)
	}

	replica := r.ReplicaState[id]

	result, err := r.ReconcileReplicaResources(ctx, log, chctrl.ReplicaUpdateInput{
		Revisions: r.revs,

		DesiredConfigMap: configMap,

		ExistingSTS:        replica.StatefulSet,
		DesiredSTS:         statefulSet,
		HasError:           replica.Error,
		BreakingSTSVersion: breakingStatefulSetVersion,

		ExistingPVC:    replica.PVC,
		DesiredPVCSpec: r.Cluster.Spec.DataVolumeClaimSpec,
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile replica %s resources: %w", id, err)
	}

	return result, nil
}
