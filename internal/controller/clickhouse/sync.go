package clickhouse

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/clickhouse-operator/internal/controller"
	"github.com/clickhouse-operator/internal/controller/keeper"
	"github.com/clickhouse-operator/internal/util"
	gcmp "github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
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
	Pinged      bool
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

func (r replicaState) HasStatefulSetDiff(ctx *reconcileContext) bool {
	if r.StatefulSet == nil {
		return true
	}

	return util.GetSpecHashFromObject(r.StatefulSet) != ctx.Cluster.Status.StatefulSetRevision
}

func (r replicaState) HasConfigMapDiff(ctx *reconcileContext) bool {
	if r.StatefulSet == nil {
		return true
	}

	return util.GetConfigHashFromObject(r.StatefulSet) != ctx.Cluster.Status.ConfigurationRevision
}

func (r replicaState) UpdateStage(ctx *reconcileContext) chctrl.ReplicaUpdateStage {
	if r.StatefulSet == nil {
		return chctrl.StageNotExists
	}

	if r.Error {
		return chctrl.StageError
	}

	if !r.Updated() {
		return chctrl.StageUpdating
	}

	if r.HasConfigMapDiff(ctx) || r.HasStatefulSetDiff(ctx) {
		return chctrl.StageHasDiff
	}

	if !r.Ready() {
		return chctrl.StageNotReadyUpToDate
	}

	return chctrl.StageUpToDate
}

type ReconcileContextBase = chctrl.ReconcileContextBase[v1.ClickHouseClusterStatus, *v1.ClickHouseCluster, v1.ClickHouseReplicaID, replicaState]

type reconcileContext struct {
	ReconcileContextBase

	// Should be populated after reconcileClusterRevisions.
	keeper v1.KeeperCluster
	// Should be populated by reconcileCommonResources.
	secret    corev1.Secret
	commander *Commander

	databasesInSync        bool
	staleReplicasCleanedUp bool
}

type ReconcileFunc func(util.Logger, *reconcileContext) (*ctrl.Result, error)

func (r *ClusterReconciler) Sync(ctx context.Context, log util.Logger, cr *v1.ClickHouseCluster) (ctrl.Result, error) {
	log.Info("Enter ClickHouse Reconcile", "spec", cr.Spec, "status", cr.Status)

	recCtx := reconcileContext{
		ReconcileContextBase: chctrl.ReconcileContextBase[
			v1.ClickHouseClusterStatus,
			*v1.ClickHouseCluster,
			v1.ClickHouseReplicaID,
			replicaState,
		]{
			Cluster:      cr,
			Context:      ctx,
			ReplicaState: map[v1.ClickHouseReplicaID]replicaState{},
		},
	}
	defer func() {
		if recCtx.commander != nil {
			recCtx.commander.Close()
		}
	}()

	reconcileSteps := []ReconcileFunc{
		r.reconcileCommonResources,
		r.reconcileClusterRevisions,
		r.reconcileActiveReplicaStatus,
		r.reconcileReplicaResources,
		r.reconcileReplicateSchema,
		r.reconcileCleanUp,
		r.reconcileConditions,
	}

	var result ctrl.Result
	for _, fn := range reconcileSteps {
		funcName := strings.TrimPrefix(util.GetFunctionName(fn), "reconcile")
		stepLog := log.With("reconcile_step", funcName)
		stepLog.Debug("starting reconcile step")

		stepResult, err := fn(stepLog, &recCtx)
		if err != nil {
			if k8serrors.IsConflict(err) {
				stepLog.Error(err, "update conflict for resource, reschedule to retry")
				// retry immediately, as just the update to the CR failed
				return ctrl.Result{RequeueAfter: keeper.RequeueOnRefreshTimeout}, nil
			}
			if k8serrors.IsAlreadyExists(err) {
				stepLog.Error(err, "create already existed resource, reschedule to retry")
				// retry immediately, as just creating already existed resource
				return ctrl.Result{RequeueAfter: keeper.RequeueOnRefreshTimeout}, nil
			}

			stepLog.Error(err, "unexpected error, setting conditions to unknown and rescheduling reconciliation to try again")
			errMsg := "Reconcile returned error"

			var unknownConditions []metav1.Condition
			for _, cond := range v1.AllClickHouseConditionTypes {
				unknownConditions = append(unknownConditions, recCtx.NewCondition(cond, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg))
			}
			meta.SetStatusCondition(&unknownConditions, recCtx.NewCondition(v1.ConditionTypeReconcileSucceeded, metav1.ConditionFalse, v1.ConditionReasonStepFailed, errMsg))
			recCtx.SetConditions(log, unknownConditions)

			if updateErr := recCtx.UpsertStatus(r, log); updateErr != nil {
				log.Error(updateErr, "failed to update status")
			}
			return ctrl.Result{}, err
		}

		if !stepResult.IsZero() {
			stepLog.Debug("reconcile step result", "result", stepResult)
			util.UpdateResult(&result, stepResult)
		}

		stepLog.Debug("reconcile step completed")
	}

	recCtx.SetCondition(log, v1.ConditionTypeReconcileSucceeded, metav1.ConditionTrue, v1.ConditionReasonReconcileFinished, "Reconcile succeeded")
	log.Info("reconciliation loop end", "result", result)

	if err := recCtx.UpsertStatus(r, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after reconciliation: %w", err)
	}

	return result, nil
}

func (r *ClusterReconciler) reconcileCommonResources(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	service := TemplateHeadlessService(ctx.Cluster)
	if _, err := chctrl.ReconcileService(ctx.Context, log, r, ctx.Cluster, service); err != nil {
		return nil, fmt.Errorf("reconcile service resource: %w", err)
	}

	for shard := range ctx.Cluster.Shards() {
		pdb := TemplatePodDisruptionBudget(ctx.Cluster, shard)
		if _, err := chctrl.ReconcilePodDisruptionBudget(ctx.Context, log, r, ctx.Cluster, pdb); err != nil {
			return nil, fmt.Errorf("reconcile PodDisruptionBudget resource for shard %d: %w", shard, err)
		}
	}

	var disruptionBudgets policyv1.PodDisruptionBudgetList
	if err := r.List(ctx.Context, &disruptionBudgets,
		util.AppRequirements(ctx.Cluster.Namespace, ctx.Cluster.SpecificName())); err != nil {
		return nil, fmt.Errorf("list PodDisruptionBudgets: %w", err)
	}
	for _, pdb := range disruptionBudgets.Items {
		shardID, err := strconv.Atoi(pdb.Labels[util.LabelClickHouseShardID])
		if err != nil {
			log.Warn("failed to get shard ID from PodDisruptionBudget labels", "pdb", pdb.Name, "error", err)
			continue
		}

		if shardID >= int(ctx.Cluster.Shards()) {
			log.Info("removing PodDisruptionBudget", "pdb", pdb.Name)
			if err := chctrl.Delete(ctx.Context, r, ctx.Cluster, &pdb); err != nil {
				return nil, err
			}
		}
	}

	getErr := r.Get(ctx.Context, types.NamespacedName{
		Namespace: ctx.Cluster.Namespace,
		Name:      ctx.Cluster.SecretName(),
	}, &ctx.secret)
	if getErr != nil && !k8serrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("get ClickHouse cluster secret %q: %w", ctx.Cluster.SecretName(), getErr)
	}
	isSecretUpdated, err := TemplateClusterSecrets(ctx.Cluster, &ctx.secret)
	if err != nil {
		return nil, fmt.Errorf("template cluster secrets: %w", err)
	}
	if err := ctrl.SetControllerReference(ctx.Cluster, &ctx.secret, r.Scheme); err != nil {
		return nil, fmt.Errorf("set controller reference for cluster secret %q: %w", ctx.Cluster.SecretName(), err)
	}

	if getErr != nil {
		log.Info("cluster secret not found, creating", "secret", ctx.Cluster.SecretName())
		if err := chctrl.Create(ctx.Context, r, ctx.Cluster, &ctx.secret); err != nil {
			return nil, fmt.Errorf("create cluster secret: %w", err)
		}
	} else if isSecretUpdated {
		if err = chctrl.Update(ctx.Context, r, ctx.Cluster, &ctx.secret); err != nil {
			return nil, fmt.Errorf("update cluster secret: %w", err)
		}
	} else {
		log.Debug("cluster secret is up to date")
	}

	ctx.commander = NewCommander(log, ctx.Cluster, &ctx.secret)
	return nil, nil
}

func (r *ClusterReconciler) reconcileClusterRevisions(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	if ctx.Cluster.Status.ObservedGeneration != ctx.Cluster.Generation {
		ctx.Cluster.Status.ObservedGeneration = ctx.Cluster.Generation
		log.Debug(fmt.Sprintf("observed new CR generation %d", ctx.Cluster.Generation))
	}

	updateRevision, err := util.DeepHashObject(ctx.Cluster.Spec)
	if err != nil {
		return nil, fmt.Errorf("get current spec revision: %w", err)
	}
	if updateRevision != ctx.Cluster.Status.UpdateRevision {
		ctx.Cluster.Status.UpdateRevision = updateRevision
		log.Debug(fmt.Sprintf("observed new CR revision %q", updateRevision))
	}

	if err := r.Get(ctx.Context, types.NamespacedName{
		Namespace: ctx.Cluster.Namespace,
		Name:      ctx.Cluster.Spec.KeeperClusterRef.Name,
	}, &ctx.keeper); err != nil {
		return nil, fmt.Errorf("get keeper cluster: %w", err)
	}

	if cond := meta.FindStatusCondition(ctx.keeper.Status.Conditions, string(v1.ConditionTypeReady)); cond == nil || cond.Status != metav1.ConditionTrue {
		if cond == nil {
			log.Warn("keeper cluster is not ready")
		} else {
			log.Warn("keeper cluster is not ready", "reason", cond.Reason, "message", cond.Message)
		}
	}

	configRevision, err := GetConfigurationRevision(ctx)
	if err != nil {
		return nil, fmt.Errorf("get configuration revision: %w", err)
	}
	if configRevision != ctx.Cluster.Status.ConfigurationRevision {
		ctx.Cluster.Status.ConfigurationRevision = configRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", configRevision))
	}

	stsRevision, err := GetStatefulSetRevision(ctx)
	if err != nil {
		return nil, fmt.Errorf("get StatefulSet revision: %w", err)
	}
	if stsRevision != ctx.Cluster.Status.StatefulSetRevision {
		ctx.Cluster.Status.StatefulSetRevision = stsRevision
		log.Debug(fmt.Sprintf("observed new StatefulSet revision %q", stsRevision))
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileActiveReplicaStatus(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	listOpts := util.AppRequirements(ctx.Cluster.Namespace, ctx.Cluster.SpecificName())

	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx.Context, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	execResults := util.ExecuteParallel(statefulSets.Items, func(sts appsv1.StatefulSet) (v1.ClickHouseReplicaID, replicaState, error) {
		id, err := v1.ClickHouseIDFromLabels(sts.Labels)
		if err != nil {
			log.Error(err, "failed to get replica ID from StatefulSet labels", "stateful_set", sts.Name)
			return v1.ClickHouseReplicaID{}, replicaState{}, err
		}

		hasError, err := chctrl.CheckPodError(ctx.Context, log, r.Client, &sts)
		if err != nil {
			log.Warn("failed to check replica pod error", "stateful_set", sts.Name, "error", err)
			hasError = true
		}

		pingErr := ctx.commander.Ping(ctx.Context, id)
		if pingErr != nil {
			log.Debug("failed to ping replica", "replica_id", id, "error", pingErr)
		}

		log.Debug("load replica state done", "replica_id", id, "statefulset", sts.Name)
		return id, replicaState{
			StatefulSet: &sts,
			Error:       hasError,
			Pinged:      pingErr == nil,
		}, nil
	})
	states := map[v1.ClickHouseReplicaID]replicaState{}
	for id, res := range execResults {
		if res.Err != nil {
			log.Info("failed to load replica state", "error", res.Err, "replica_id", id)
			continue
		}

		states[id] = res.Result
	}

	for id, state := range states {
		if exists := ctx.SetReplica(id, state); exists {
			log.Debug(fmt.Sprintf("multiple StatefulSets for single replica %v", id),
				"replica_id", id, "statefuleset", state.StatefulSet.Name)
		}
	}

	return nil, nil
}

// reconcileReplicaResources performs update on replicas ConfigMap and StatefulSet.
// If there are replicas that has no created StatefulSet, creates immediately.
// If all replicas exists performs rolling upgrade, with the following order preferences:
// NotExists -> CrashLoop/ImagePullErr -> OnlySts -> OnlyConfig -> Any.
func (r *ClusterReconciler) reconcileReplicaResources(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	highestStage := chctrl.StageUpToDate
	var replicasInStatus []v1.ClickHouseReplicaID

	for id := range ctx.Cluster.ReplicaIDs() {
		stage := ctx.Replica(id).UpdateStage(ctx)
		if stage == highestStage {
			replicasInStatus = append(replicasInStatus, id)
			continue
		}

		if stage > highestStage {
			highestStage = stage
			replicasInStatus = []v1.ClickHouseReplicaID{id}
		}
	}

	result := ctrl.Result{}

	switch highestStage {
	case chctrl.StageUpToDate:
		log.Info("all replicas are up to date")
		return nil, nil
	case chctrl.StageNotReadyUpToDate, chctrl.StageUpdating:
		log.Info("waiting for updated replicas to become ready", "replicas", replicasInStatus, "priority", highestStage.String())
		result = ctrl.Result{RequeueAfter: keeper.RequeueOnRefreshTimeout}
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
		replicaResult, err := r.updateReplica(log, ctx, id)
		if err != nil {
			return nil, fmt.Errorf("update replica %s: %w", id, err)
		}

		util.UpdateResult(&result, replicaResult)
	}

	return &result, nil
}

func (r *ClusterReconciler) reconcileReplicateSchema(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	if !ctx.Cluster.Spec.Settings.EnableDatabaseSync {
		log.Info("database sync is disabled, skipping")
		return nil, nil
	}

	var readyReplicas []v1.ClickHouseReplicaID
	for id, replica := range ctx.ReplicaState {
		if replica.Ready() {
			readyReplicas = append(readyReplicas, id)
		}
	}

	if readyReplicas == nil {
		log.Info("no ready replicas to replicate schema, skipping")
		return nil, nil
	}

	hasNotSynced := false
	replicaDatabases := util.ExecuteParallel(readyReplicas, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, map[string]DatabaseDescriptor, error) {
		if err := ctx.commander.EnsureDefaultDatabaseEngine(ctx.Context, log, ctx.Cluster, id); err != nil {
			log.Info("failed to ensure default database engine for replica", "replica", id, "error", err)
			hasNotSynced = true
		}

		databases, err := ctx.commander.Databases(ctx.Context, id)
		return id, databases, err
	})
	databases := map[string]DatabaseDescriptor{}
	for id, replDBs := range replicaDatabases {
		if replDBs.Err != nil {
			log.Warn("failed to get databases from replica", "replica_id", id, "error", replDBs.Err)
			return &ctrl.Result{RequeueAfter: keeper.RequeueOnErrorTimeout}, nil
		}
		databases = util.MergeMaps(databases, replDBs.Result)
	}

	_ = util.ExecuteParallel(readyReplicas, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, struct{}, error) {
		if len(databases) == len(replicaDatabases[id].Result) {
			log.Debug("replica is in sync", "replica_id", id)
			return id, struct{}{}, nil
		}
		dbsToSync := map[string]DatabaseDescriptor{}
		for name, desc := range databases {
			if _, ok := replicaDatabases[id].Result[name]; !ok {
				dbsToSync[name] = desc
			}
		}
		log.Info("replicating databases to replica", "replica_id", id, "databases", slices.Collect(maps.Keys(dbsToSync)))
		err := ctx.commander.CreateDatabases(ctx.Context, id, dbsToSync)
		if err != nil {
			log.Info("failed to create databases on replica", "error", err, "replica_id", id)
			hasNotSynced = true
		}
		return id, struct{}{}, nil
	})

	ctx.databasesInSync = !hasNotSynced
	if hasNotSynced {
		return &ctrl.Result{RequeueAfter: keeper.RequeueOnRefreshTimeout}, nil
	}

	return nil, nil
}

type replicaResources struct {
	cfg *corev1.ConfigMap
	sts *appsv1.StatefulSet
}

func (r *ClusterReconciler) reconcileCleanUp(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	var result *ctrl.Result
	listOpts := util.AppRequirements(ctx.Cluster.Namespace, ctx.Cluster.SpecificName())
	replicasToRemove := map[int32]map[int32]replicaResources{}

	var configMaps corev1.ConfigMapList
	if err := r.List(ctx.Context, &configMaps, listOpts); err != nil {
		return nil, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		id, err := v1.ClickHouseIDFromLabels(configMap.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from ConfigMap labels", "configmap", configMap.Name, "error", err)
			continue
		}

		if id.ShardID < ctx.Cluster.Shards() && id.Index < ctx.Cluster.Replicas() {
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
	if err := r.List(ctx.Context, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		id, err := v1.ClickHouseIDFromLabels(sts.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from ConfigMap labels", "statefulset", sts.Name, "error", err)
			continue
		}

		if id.ShardID < ctx.Cluster.Shards() && id.Index < ctx.Cluster.Replicas() {
			continue
		}

		if _, ok := replicasToRemove[id.ShardID]; !ok {
			replicasToRemove[id.ShardID] = map[int32]replicaResources{}
		}
		state := replicasToRemove[id.ShardID][id.Index]
		state.sts = &sts
		replicasToRemove[id.ShardID][id.Index] = state
	}

	shardsInSync := util.ExecuteParallel(slices.Collect(maps.Keys(replicasToRemove)), func(shardID int32) (int32, struct{}, error) {
		log.Info("Pre scale-down shard sync", "shard_id", shardID)
		err := ctx.commander.SyncShard(ctx.Context, log, shardID)
		if err != nil {
			log.Info("failed to sync shard", "shard_id", shardID, "error", err)
		}
		return shardID, struct{}{}, nil
	})

	runningStaleReplicas := map[v1.ClickHouseReplicaID]struct{}{}
	for shardID, replicas := range replicasToRemove {
		if shardID < ctx.Cluster.Shards() {
			if shardsInSync[shardID].Err != nil {
				log.Info("shard is not in sync, skipping replica deletion", "replicas", slices.Collect(maps.Keys(replicas)))
				result = &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}
				for index := range replicas {
					runningStaleReplicas[v1.ClickHouseReplicaID{ShardID: shardID, Index: index}] = struct{}{}
				}
				continue
			}
		}

		for index, res := range replicas {
			id := v1.ClickHouseReplicaID{ShardID: shardID, Index: index}
			if res.sts != nil {
				log.Info("removing replica statefulset", "replica_id", id, "statefulset", res.sts.Name)
				if err := chctrl.Delete(ctx.Context, r, ctx.Cluster, res.sts); err != nil {
					return nil, err
				}
			}

			if res.cfg != nil {
				log.Info("removing replica configmap", "replica_id", id, "configmap", res.cfg.Name)
				if err := chctrl.Delete(ctx.Context, r, ctx.Cluster, res.cfg); err != nil {
					return nil, err
				}
			}
		}
	}

	if ctx.Cluster.Spec.Settings.EnableDatabaseSync {
		if err := ctx.commander.CleanupDatabaseReplicas(ctx.Context, log, runningStaleReplicas); err != nil {
			log.Warn("failed to cleanup database replicas", "error", err)
			result = &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}
		} else {
			ctx.staleReplicasCleanedUp = true
		}
	}

	return result, nil
}

func (r *ClusterReconciler) reconcileConditions(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	var errorReplicas []v1.ClickHouseReplicaID
	var notReadyReplicas []v1.ClickHouseReplicaID
	var notUpdatedReplicas []v1.ClickHouseReplicaID
	var notReadyShards []int32

	ctx.Cluster.Status.ReadyReplicas = 0
	for shard := range ctx.Cluster.Shards() {
		hasReady := false
		for index := range ctx.Cluster.Replicas() {
			id := v1.ClickHouseReplicaID{ShardID: shard, Index: index}
			replica := ctx.Replica(id)

			if replica.Error {
				errorReplicas = append(errorReplicas, id)
			}

			if !replica.Ready() {
				notReadyReplicas = append(notReadyReplicas, id)
			} else {
				ctx.Cluster.Status.ReadyReplicas++
				hasReady = true
			}

			if replica.HasConfigMapDiff(ctx) || replica.HasStatefulSetDiff(ctx) || !replica.Updated() {
				notUpdatedReplicas = append(notUpdatedReplicas, id)
			}
		}

		if !hasReady {
			notReadyShards = append(notReadyShards, shard)
		}
	}

	if len(errorReplicas) > 0 {
		slices.SortFunc(errorReplicas, compareReplicaID)
		message := fmt.Sprintf("Replicas has startup errors: %v", errorReplicas)
		ctx.SetCondition(log, v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionFalse, v1.ConditionReasonReplicaError, message)
	} else {
		ctx.SetCondition(log, v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionTrue, v1.ConditionReasonReplicasRunning, "")
	}

	if len(notReadyReplicas) > 0 {
		slices.SortFunc(notReadyReplicas, compareReplicaID)
		message := fmt.Sprintf("Not ready replicas: %v", notReadyReplicas)
		ctx.SetCondition(log, v1.ConditionTypeHealthy, metav1.ConditionFalse, v1.ConditionReasonReplicasNotReady, message)
	} else {
		ctx.SetCondition(log, v1.ConditionTypeHealthy, metav1.ConditionTrue, v1.ConditionReasonReplicasReady, "")
	}

	if len(notUpdatedReplicas) > 0 {
		slices.SortFunc(notUpdatedReplicas, compareReplicaID)
		message := fmt.Sprintf("Replicas has pending updates: %v", notUpdatedReplicas)
		ctx.SetCondition(log, v1.ConditionTypeConfigurationInSync, metav1.ConditionFalse, v1.ConditionReasonConfigurationChanged, message)
	} else {
		ctx.SetCondition(log, v1.ConditionTypeConfigurationInSync, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "")
	}

	exists := len(ctx.ReplicaState)
	expected := int(ctx.Cluster.Replicas() * ctx.Cluster.Shards())

	if exists < expected {
		ctx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ConditionReasonScalingUp, "Cluster has less replicas than requested")
	} else if exists > expected {
		ctx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ConditionReasonScalingDown, "Cluster has more replicas than requested")
	} else {
		ctx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "")
	}

	if len(notUpdatedReplicas) == 0 && exists == expected {
		ctx.Cluster.Status.CurrentRevision = ctx.Cluster.Status.UpdateRevision
	}

	var err error
	if len(notReadyShards) == 0 {
		_, err = ctx.UpsertConditionAndSendEvent(r, log,
			ctx.NewCondition(v1.ConditionTypeReady, metav1.ConditionTrue, v1.ClickHouseConditionAllShardsReady, "All shards are ready"),
			corev1.EventTypeNormal, v1.EventReasonClusterReady, "ClickHouse cluster is ready",
		)
	} else {
		slices.Sort(notReadyShards)
		message := fmt.Sprintf("Not Ready shards: %v", notReadyShards)
		_, err = ctx.UpsertConditionAndSendEvent(r, log,
			ctx.NewCondition(v1.ConditionTypeReady, metav1.ConditionFalse, v1.ClickHouseConditionSomeShardsNotReady, message),
			corev1.EventTypeWarning, v1.EventReasonClusterNotReady, message,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("update ready condition: %w", err)
	}

	{
		condType := metav1.ConditionTrue
		condReason := v1.ClickHouseConditionSchemaSyncDisabled
		condMessage := "Database schema sync is disabled"
		if ctx.Cluster.Spec.Settings.EnableDatabaseSync {
			if !ctx.databasesInSync {
				condType = metav1.ConditionFalse
				condReason = v1.ClickHouseConditionDatabasesNotCreated
				condMessage = "Some databases are not created on all replicas"
			} else if !ctx.staleReplicasCleanedUp {
				condType = metav1.ConditionFalse
				condReason = v1.ClickHouseConditionReplicasNotCleanedUp
				condMessage = "Some stale replicas are not cleaned up"
			} else {
				condType = metav1.ConditionTrue
				condReason = v1.ClickHouseConditionReplicasInSync
				condMessage = "Databases are sync on all replicas"
			}
		}

		ctx.SetCondition(log, v1.ClickHouseConditionTypeSchemaInSync, condType, condReason, condMessage)
	}

	return nil, nil
}

func (r *ClusterReconciler) updateReplica(log util.Logger, ctx *reconcileContext, id v1.ClickHouseReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", id)
	log.Info("updating replica")

	configMap, err := TemplateConfigMap(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %s ConfigMap: %w", id, err)
	}

	configChanged, err := chctrl.ReconcileConfigMap(ctx.Context, log, r, ctx.Cluster, configMap)
	if err != nil {
		return nil, fmt.Errorf("update replica %s ConfigMap: %w", id, err)
	}

	statefulSet, err := TemplateStatefulSet(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %s StatefulSet: %w", id, err)
	}
	if err := ctrl.SetControllerReference(ctx.Cluster, statefulSet, r.Scheme); err != nil {
		return nil, fmt.Errorf("set replica %s StatefulSet controller reference: %w", id, err)
	}

	replica := ctx.Replica(id)
	if replica.StatefulSet == nil {
		log.Info("replica StatefulSet not found, creating", "stateful_set", statefulSet.Name)
		util.AddObjectConfigHash(statefulSet, ctx.Cluster.Status.ConfigurationRevision)
		util.AddHashWithKeyToAnnotations(statefulSet, util.AnnotationSpecHash, ctx.Cluster.Status.StatefulSetRevision)

		if err := chctrl.Create(ctx.Context, r, ctx.Cluster, statefulSet); err != nil {
			return nil, err
		}

		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// Check if the StatefulSet is outdated and needs to be recreated
	v, err := semver.Parse(replica.StatefulSet.Annotations[util.AnnotationStatefulSetVersion])
	if err != nil || keeper.BreakingStatefulSetVersion.GT(v) {
		log.Warn(fmt.Sprintf("Removing the StatefulSet because of a breaking change. Found version: %v, expected version: %v", v, keeper.BreakingStatefulSetVersion))
		if err := chctrl.Delete(ctx.Context, r, ctx.Cluster, replica.StatefulSet); err != nil {
			return nil, err
		}

		return &ctrl.Result{RequeueAfter: keeper.RequeueOnRefreshTimeout}, nil
	}

	stsNeedsUpdate := replica.HasStatefulSetDiff(ctx)

	// Trigger Pod restart if config changed
	if replica.HasConfigMapDiff(ctx) {
		// TODO detect if settings do not require server restart
		// Use same way as Kubernetes for force restarting Pods one by one
		// (https://github.com/kubernetes/kubernetes/blob/22a21f974f5c0798a611987405135ab7e62502da/staging/src/k8s.io/kubectl/pkg/polymorphichelpers/objectrestarter.go#L41)
		// Not included by default in the StatefulSet so that hash-diffs work correctly
		log.Info("forcing ClickHouse Pod restart, because of config changes")
		statefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt] = time.Now().Format(time.RFC3339)
		util.AddObjectConfigHash(replica.StatefulSet, ctx.Cluster.Status.ConfigurationRevision)
		stsNeedsUpdate = true
	} else if restartedAt, ok := replica.StatefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt]; ok {
		statefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt] = restartedAt
	}

	if !stsNeedsUpdate {
		log.Debug("StatefulSet is up to date")
		if configChanged {
			return &ctrl.Result{RequeueAfter: keeper.RequeueOnRefreshTimeout}, nil
		}

		return nil, nil
	}

	if !gcmp.Equal(replica.StatefulSet.Spec.VolumeClaimTemplates[0].Spec, ctx.Cluster.Spec.DataVolumeClaimSpec) {
		if err = r.updateReplicaPVC(log, ctx, id); err != nil {
			return nil, fmt.Errorf("update replica %s PVC: %w", id, err)
		}
		statefulSet.Spec.VolumeClaimTemplates = replica.StatefulSet.Spec.VolumeClaimTemplates
	}

	log.Info("updating replica StatefulSet", "stateful_set", statefulSet.Name)
	log.Info("replica StatefulSet diff", "diff", gcmp.Diff(replica.StatefulSet.Spec, statefulSet.Spec))
	replica.StatefulSet.Spec = statefulSet.Spec
	replica.StatefulSet.Annotations = util.MergeMaps(replica.StatefulSet.Annotations, statefulSet.Annotations)
	replica.StatefulSet.Labels = util.MergeMaps(replica.StatefulSet.Labels, statefulSet.Labels)
	util.AddHashWithKeyToAnnotations(replica.StatefulSet, util.AnnotationSpecHash, ctx.Cluster.Status.StatefulSetRevision)
	if err := chctrl.Update(ctx.Context, r, ctx.Cluster, replica.StatefulSet); err != nil {
		return nil, err
	}

	return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
}

func (r *ClusterReconciler) updateReplicaPVC(log util.Logger, ctx *reconcileContext, id v1.ClickHouseReplicaID) error {
	var pvcs corev1.PersistentVolumeClaimList
	req := util.AppRequirements(ctx.Cluster.Namespace, ctx.Cluster.SpecificName())
	for k, v := range labelsFromID(id) {
		idReq, _ := labels.NewRequirement(k, selection.Equals, []string{v})
		req.LabelSelector = req.LabelSelector.Add(*idReq)
	}
	log.Debug("listing replica PVCs", "replica_id", id, "selector", req.LabelSelector.String())
	if err := r.List(ctx.Context, &pvcs, req); err != nil {
		return fmt.Errorf("list replica %s PVCs: %w", id, err)
	}

	if len(pvcs.Items) == 0 {
		log.Info("no PVCs found for replica, skipping update", "replica_id", id)
		return nil
	}

	if len(pvcs.Items) > 1 {
		pvcNames := make([]string, len(pvcs.Items))
		for i, pvc := range pvcs.Items {
			pvcNames[i] = pvc.Name
		}
		return fmt.Errorf("found multiple PVCs for replica %s: %v", id, pvcNames)
	}

	if gcmp.Equal(pvcs.Items[0].Spec, ctx.Cluster.Spec.DataVolumeClaimSpec) {
		log.Debug("replica PVC is up to date", "pvc", pvcs.Items[0].Name)
		return nil
	}

	targetSpec := ctx.Cluster.Spec.DataVolumeClaimSpec.DeepCopy()
	if err := util.ApplyDefault(targetSpec, pvcs.Items[0].Spec); err != nil {
		return fmt.Errorf("apply patch to replica PVC %q: %w", id, err)
	}
	log.Info("updating replica PVC", "pvc", pvcs.Items[0].Name, "diff", gcmp.Diff(pvcs.Items[0].Spec, targetSpec))
	pvcs.Items[0].Spec = *targetSpec
	if err := chctrl.Update(ctx.Context, r, ctx.Cluster, &pvcs.Items[0]); err != nil {
		return fmt.Errorf("update replica PVC %q: %w", id, err)
	}

	return nil
}
