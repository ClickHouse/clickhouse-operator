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

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/clickhouse-operator/internal/controller"
	ctrlutil "github.com/clickhouse-operator/internal/controllerutil"
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

func (r replicaState) HasStatefulSetDiff(recCtx *reconcileContext) bool {
	if r.StatefulSet == nil {
		return true
	}

	return ctrlutil.GetSpecHashFromObject(r.StatefulSet) != recCtx.Cluster.Status.StatefulSetRevision
}

func (r replicaState) HasConfigMapDiff(recCtx *reconcileContext) bool {
	if r.StatefulSet == nil {
		return true
	}

	return ctrlutil.GetConfigHashFromObject(r.StatefulSet) != recCtx.Cluster.Status.ConfigurationRevision
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

type reconcileContextBase = chctrl.ReconcileContextBase[v1.ClickHouseClusterStatus, *v1.ClickHouseCluster, v1.ClickHouseReplicaID, replicaState]

type reconcileContext struct {
	reconcileContextBase

	// Should be populated after reconcileClusterRevisions.
	keeper v1.KeeperCluster
	// Should be populated by reconcileCommonResources.
	secret    corev1.Secret
	commander *commander

	databasesInSync        bool
	staleReplicasCleanedUp bool
}

type reconcileFunc func(context.Context, ctrlutil.Logger, *reconcileContext) (*ctrl.Result, error)

func (r *ClusterReconciler) sync(ctx context.Context, log ctrlutil.Logger, cr *v1.ClickHouseCluster) (ctrl.Result, error) {
	log.Info("Enter ClickHouse Reconcile", "spec", cr.Spec, "status", cr.Status)

	recCtx := reconcileContext{
		reconcileContextBase: chctrl.ReconcileContextBase[
			v1.ClickHouseClusterStatus,
			*v1.ClickHouseCluster,
			v1.ClickHouseReplicaID,
			replicaState,
		]{
			Cluster:      cr,
			ReplicaState: map[v1.ClickHouseReplicaID]replicaState{},
		},
	}
	defer func() {
		if recCtx.commander != nil {
			recCtx.commander.Close()
		}
	}()

	reconcileSteps := []reconcileFunc{
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
		funcName := strings.TrimPrefix(ctrlutil.GetFunctionName(fn), "reconcile")
		stepLog := log.With("reconcile_step", funcName)
		stepLog.Debug("starting reconcile step")

		stepResult, err := fn(ctx, stepLog, &recCtx)
		if err != nil {
			if k8serrors.IsConflict(err) {
				stepLog.Error(err, "update conflict for resource, reschedule to retry")
				// retry immediately, as just the update to the CR failed
				return ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
			}

			if k8serrors.IsAlreadyExists(err) {
				stepLog.Error(err, "create already existed resource, reschedule to retry")
				// retry immediately, as just creating already existed resource
				return ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
			}

			stepLog.Error(err, "unexpected error, setting conditions to unknown and rescheduling reconciliation to try again")

			errMsg := "Reconcile returned error"

			var unknownConditions []metav1.Condition
			for _, cond := range v1.AllClickHouseConditionTypes {
				unknownConditions = append(unknownConditions, recCtx.NewCondition(cond, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg))
			}

			meta.SetStatusCondition(&unknownConditions, recCtx.NewCondition(v1.ConditionTypeReconcileSucceeded, metav1.ConditionFalse, v1.ConditionReasonStepFailed, errMsg))
			recCtx.SetConditions(log, unknownConditions)

			if updateErr := recCtx.UpsertStatus(ctx, log, r); updateErr != nil {
				log.Error(updateErr, "failed to update status")
			}

			return ctrl.Result{}, err
		}

		if !stepResult.IsZero() {
			stepLog.Debug("reconcile step result", "result", stepResult)
			ctrlutil.UpdateResult(&result, stepResult)
		}

		stepLog.Debug("reconcile step completed")
	}

	recCtx.SetCondition(log, v1.ConditionTypeReconcileSucceeded, metav1.ConditionTrue, v1.ConditionReasonReconcileFinished, "Reconcile succeeded")
	log.Info("reconciliation loop end", "result", result)

	if err := recCtx.UpsertStatus(ctx, log, r); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after reconciliation: %w", err)
	}

	return result, nil
}

func (r *ClusterReconciler) reconcileCommonResources(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext) (*ctrl.Result, error) {
	service := templateHeadlessService(recCtx.Cluster)
	if _, err := chctrl.ReconcileService(ctx, log, r, recCtx.Cluster, service); err != nil {
		return nil, fmt.Errorf("reconcile service resource: %w", err)
	}

	for shard := range recCtx.Cluster.Shards() {
		pdb := templatePodDisruptionBudget(recCtx.Cluster, shard)
		if _, err := chctrl.ReconcilePodDisruptionBudget(ctx, log, r, recCtx.Cluster, pdb); err != nil {
			return nil, fmt.Errorf("reconcile PodDisruptionBudget resource for shard %d: %w", shard, err)
		}
	}

	var disruptionBudgets policyv1.PodDisruptionBudgetList
	if err := r.List(ctx, &disruptionBudgets,
		ctrlutil.AppRequirements(recCtx.Cluster.Namespace, recCtx.Cluster.SpecificName())); err != nil {
		return nil, fmt.Errorf("list PodDisruptionBudgets: %w", err)
	}

	for _, pdb := range disruptionBudgets.Items {
		shardID, err := strconv.Atoi(pdb.Labels[ctrlutil.LabelClickHouseShardID])
		if err != nil {
			log.Warn("failed to get shard ID from PodDisruptionBudget labels", "pdb", pdb.Name, "error", err)
			continue
		}

		if shardID >= int(recCtx.Cluster.Shards()) {
			log.Info("removing PodDisruptionBudget", "pdb", pdb.Name)

			if err := chctrl.Delete(ctx, r, recCtx.Cluster, &pdb); err != nil {
				return nil, fmt.Errorf("remove shard %d: %w", shardID, err)
			}
		}
	}

	getErr := r.Get(ctx, types.NamespacedName{
		Namespace: recCtx.Cluster.Namespace,
		Name:      recCtx.Cluster.SecretName(),
	}, &recCtx.secret)
	if getErr != nil && !k8serrors.IsNotFound(getErr) {
		return nil, fmt.Errorf("get ClickHouse cluster secret %q: %w", recCtx.Cluster.SecretName(), getErr)
	}

	isSecretUpdated := templateClusterSecrets(recCtx.Cluster, &recCtx.secret)
	if err := ctrl.SetControllerReference(recCtx.Cluster, &recCtx.secret, r.Scheme); err != nil {
		return nil, fmt.Errorf("set controller reference for cluster secret %q: %w", recCtx.Cluster.SecretName(), err)
	}

	switch {
	case getErr != nil:
		log.Info("cluster secret not found, creating", "secret", recCtx.Cluster.SecretName())

		if err := chctrl.Create(ctx, r, recCtx.Cluster, &recCtx.secret); err != nil {
			return nil, fmt.Errorf("create cluster secret: %w", err)
		}

	case isSecretUpdated:
		if err := chctrl.Update(ctx, r, recCtx.Cluster, &recCtx.secret); err != nil {
			return nil, fmt.Errorf("update cluster secret: %w", err)
		}
	default:
		log.Debug("cluster secret is up to date")
	}

	recCtx.commander = newCommander(log, recCtx.Cluster, &recCtx.secret)

	return nil, nil
}

func (r *ClusterReconciler) reconcileClusterRevisions(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext) (*ctrl.Result, error) {
	if recCtx.Cluster.Status.ObservedGeneration != recCtx.Cluster.Generation {
		recCtx.Cluster.Status.ObservedGeneration = recCtx.Cluster.Generation
		log.Debug(fmt.Sprintf("observed new CR generation %d", recCtx.Cluster.Generation))
	}

	updateRevision, err := ctrlutil.DeepHashObject(recCtx.Cluster.Spec)
	if err != nil {
		return nil, fmt.Errorf("get current spec revision: %w", err)
	}

	if updateRevision != recCtx.Cluster.Status.UpdateRevision {
		recCtx.Cluster.Status.UpdateRevision = updateRevision
		log.Debug(fmt.Sprintf("observed new CR revision %q", updateRevision))
	}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: recCtx.Cluster.Namespace,
		Name:      recCtx.Cluster.Spec.KeeperClusterRef.Name,
	}, &recCtx.keeper); err != nil {
		return nil, fmt.Errorf("get keeper cluster: %w", err)
	}

	if cond := meta.FindStatusCondition(recCtx.keeper.Status.Conditions, string(v1.ConditionTypeReady)); cond == nil || cond.Status != metav1.ConditionTrue {
		if cond == nil {
			log.Warn("keeper cluster is not ready")
		} else {
			log.Warn("keeper cluster is not ready", "reason", cond.Reason, "message", cond.Message)
		}
	}

	configRevision, err := getConfigurationRevision(recCtx)
	if err != nil {
		return nil, fmt.Errorf("get configuration revision: %w", err)
	}

	if configRevision != recCtx.Cluster.Status.ConfigurationRevision {
		recCtx.Cluster.Status.ConfigurationRevision = configRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", configRevision))
	}

	stsRevision, err := getStatefulSetRevision(recCtx)
	if err != nil {
		return nil, fmt.Errorf("get StatefulSet revision: %w", err)
	}

	if stsRevision != recCtx.Cluster.Status.StatefulSetRevision {
		recCtx.Cluster.Status.StatefulSetRevision = stsRevision
		log.Debug(fmt.Sprintf("observed new StatefulSet revision %q", stsRevision))
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileActiveReplicaStatus(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext) (*ctrl.Result, error) {
	listOpts := ctrlutil.AppRequirements(recCtx.Cluster.Namespace, recCtx.Cluster.SpecificName())

	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	execResults := ctrlutil.ExecuteParallel(statefulSets.Items, func(sts appsv1.StatefulSet) (v1.ClickHouseReplicaID, replicaState, error) {
		id, err := v1.ClickHouseIDFromLabels(sts.Labels)
		if err != nil {
			log.Error(err, "failed to get replica ID from StatefulSet labels", "stateful_set", sts.Name)
			return v1.ClickHouseReplicaID{}, replicaState{}, fmt.Errorf("get replica ID from StatefulSet labels: %w", err)
		}

		hasError, err := chctrl.CheckPodError(ctx, log, r.Client, &sts)
		if err != nil {
			log.Warn("failed to check replica pod error", "stateful_set", sts.Name, "error", err)

			hasError = true
		}

		pingErr := recCtx.commander.Ping(ctx, id)
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
		if exists := recCtx.SetReplica(id, state); exists {
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
func (r *ClusterReconciler) reconcileReplicaResources(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext) (*ctrl.Result, error) {
	highestStage := chctrl.StageUpToDate

	var replicasInStatus []v1.ClickHouseReplicaID

	for id := range recCtx.Cluster.ReplicaIDs() {
		stage := recCtx.Replica(id).UpdateStage(recCtx)
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

		result = ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}
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
		replicaResult, err := r.updateReplica(ctx, log, recCtx, id)
		if err != nil {
			return nil, fmt.Errorf("update replica %s: %w", id, err)
		}

		ctrlutil.UpdateResult(&result, replicaResult)
	}

	return &result, nil
}

func (r *ClusterReconciler) reconcileReplicateSchema(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext) (*ctrl.Result, error) {
	if !recCtx.Cluster.Spec.Settings.EnableDatabaseSync {
		log.Info("database sync is disabled, skipping")
		return nil, nil
	}

	var readyReplicas []v1.ClickHouseReplicaID
	for id, replica := range recCtx.ReplicaState {
		if replica.Ready() {
			readyReplicas = append(readyReplicas, id)
		}
	}

	if readyReplicas == nil {
		log.Info("no ready replicas to replicate schema, skipping")
		return nil, nil
	}

	hasNotSynced := false
	replicaDatabases := ctrlutil.ExecuteParallel(readyReplicas, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, map[string]databaseDescriptor, error) {
		if err := recCtx.commander.EnsureDefaultDatabaseEngine(ctx, log, recCtx.Cluster, id); err != nil {
			log.Info("failed to ensure default database engine for replica", "replica", id, "error", err)

			hasNotSynced = true
		}

		databases, err := recCtx.commander.Databases(ctx, id)

		return id, databases, err
	})

	databases := map[string]databaseDescriptor{}
	for id, replDBs := range replicaDatabases {
		if replDBs.Err != nil {
			log.Warn("failed to get databases from replica", "replica_id", id, "error", replDBs.Err)

			hasNotSynced = true
			continue
		}

		databases = ctrlutil.MergeMaps(databases, replDBs.Result)
	}

	_ = ctrlutil.ExecuteParallel(readyReplicas, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, struct{}, error) {
		if len(databases) == len(replicaDatabases[id].Result) {
			log.Debug("replica is in sync", "replica_id", id)
			return id, struct{}{}, nil
		}

		dbsToSync := map[string]databaseDescriptor{}
		for name, desc := range databases {
			if _, ok := replicaDatabases[id].Result[name]; !ok {
				dbsToSync[name] = desc
			}
		}

		log.Info("replicating databases to replica", "replica_id", id, "databases", slices.Collect(maps.Keys(dbsToSync)))

		err := recCtx.commander.CreateDatabases(ctx, id, dbsToSync)
		if err != nil {
			log.Info("failed to create databases on replica", "error", err, "replica_id", id)

			hasNotSynced = true
		}

		return id, struct{}{}, nil
	})

	recCtx.databasesInSync = !hasNotSynced
	if hasNotSynced {
		return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
	}

	return nil, nil
}

type replicaResources struct {
	cfg *corev1.ConfigMap
	sts *appsv1.StatefulSet
}

func (r *ClusterReconciler) reconcileCleanUp(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext) (*ctrl.Result, error) {
	var (
		result     *ctrl.Result
		configMaps corev1.ConfigMapList

		listOpts         = ctrlutil.AppRequirements(recCtx.Cluster.Namespace, recCtx.Cluster.SpecificName())
		replicasToRemove = map[int32]map[int32]replicaResources{}
	)

	if err := r.List(ctx, &configMaps, listOpts); err != nil {
		return nil, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		id, err := v1.ClickHouseIDFromLabels(configMap.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from ConfigMap labels", "configmap", configMap.Name, "error", err)
			continue
		}

		if id.ShardID < recCtx.Cluster.Shards() && id.Index < recCtx.Cluster.Replicas() {
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
	if err := r.List(ctx, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		id, err := v1.ClickHouseIDFromLabels(sts.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from ConfigMap labels", "statefulset", sts.Name, "error", err)
			continue
		}

		if id.ShardID < recCtx.Cluster.Shards() && id.Index < recCtx.Cluster.Replicas() {
			continue
		}

		if _, ok := replicasToRemove[id.ShardID]; !ok {
			replicasToRemove[id.ShardID] = map[int32]replicaResources{}
		}

		state := replicasToRemove[id.ShardID][id.Index]
		state.sts = &sts
		replicasToRemove[id.ShardID][id.Index] = state
	}

	shardsInSync := ctrlutil.ExecuteParallel(slices.Collect(maps.Keys(replicasToRemove)), func(shardID int32) (int32, struct{}, error) {
		log.Info("Pre scale-down shard sync", "shard_id", shardID)

		err := recCtx.commander.SyncShard(ctx, log, shardID)
		if err != nil {
			log.Info("failed to sync shard", "shard_id", shardID, "error", err)
		}

		return shardID, struct{}{}, nil
	})

	runningStaleReplicas := map[v1.ClickHouseReplicaID]struct{}{}
	for shardID, replicas := range replicasToRemove {
		if shardID < recCtx.Cluster.Shards() {
			if shardsInSync[shardID].Err != nil {
				log.Info("shard is not in sync, skipping replica deletion", "replicas", slices.Collect(maps.Keys(replicas)))

				result = &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}

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

				if err := chctrl.Delete(ctx, r, recCtx.Cluster, res.sts); err != nil {
					log.Error(err, "failed to delete replica statefulset", "replica_id", id, "statefulset", res.sts.Name)
				}
			}

			if res.cfg != nil {
				log.Info("removing replica configmap", "replica_id", id, "configmap", res.cfg.Name)

				if err := chctrl.Delete(ctx, r, recCtx.Cluster, res.cfg); err != nil {
					log.Error(err, "failed to delete replica configmap", "replica_id", id, "statefulset", res.sts.Name)
				}
			}
		}
	}

	if recCtx.Cluster.Spec.Settings.EnableDatabaseSync {
		if err := recCtx.commander.CleanupDatabaseReplicas(ctx, log, runningStaleReplicas); err != nil {
			log.Warn("failed to cleanup database replicas", "error", err)

			result = &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}
		} else {
			recCtx.staleReplicasCleanedUp = true
		}
	}

	return result, nil
}

func (r *ClusterReconciler) reconcileConditions(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext) (*ctrl.Result, error) {
	var (
		errorReplicas      []v1.ClickHouseReplicaID
		notReadyReplicas   []v1.ClickHouseReplicaID
		notUpdatedReplicas []v1.ClickHouseReplicaID
		notReadyShards     []int32
	)

	recCtx.Cluster.Status.ReadyReplicas = 0
	for shard := range recCtx.Cluster.Shards() {
		hasReady := false
		for index := range recCtx.Cluster.Replicas() {
			id := v1.ClickHouseReplicaID{ShardID: shard, Index: index}
			replica := recCtx.Replica(id)

			if replica.Error {
				errorReplicas = append(errorReplicas, id)
			}

			if !replica.Ready() {
				notReadyReplicas = append(notReadyReplicas, id)
			} else {
				recCtx.Cluster.Status.ReadyReplicas++
				hasReady = true
			}

			if replica.HasConfigMapDiff(recCtx) || replica.HasStatefulSetDiff(recCtx) || !replica.Updated() {
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
		recCtx.SetCondition(log, v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionFalse, v1.ConditionReasonReplicaError, message)
	} else {
		recCtx.SetCondition(log, v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionTrue, v1.ConditionReasonReplicasRunning, "")
	}

	if len(notReadyReplicas) > 0 {
		slices.SortFunc(notReadyReplicas, compareReplicaID)
		message := fmt.Sprintf("Not ready replicas: %v", notReadyReplicas)
		recCtx.SetCondition(log, v1.ConditionTypeHealthy, metav1.ConditionFalse, v1.ConditionReasonReplicasNotReady, message)
	} else {
		recCtx.SetCondition(log, v1.ConditionTypeHealthy, metav1.ConditionTrue, v1.ConditionReasonReplicasReady, "")
	}

	if len(notUpdatedReplicas) > 0 {
		slices.SortFunc(notUpdatedReplicas, compareReplicaID)
		message := fmt.Sprintf("Replicas has pending updates: %v", notUpdatedReplicas)
		recCtx.SetCondition(log, v1.ConditionTypeConfigurationInSync, metav1.ConditionFalse, v1.ConditionReasonConfigurationChanged, message)
	} else {
		recCtx.SetCondition(log, v1.ConditionTypeConfigurationInSync, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "")
	}

	exists := len(recCtx.ReplicaState)
	expected := int(recCtx.Cluster.Replicas() * recCtx.Cluster.Shards())

	switch {
	case exists < expected:
		recCtx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ConditionReasonScalingUp, "Cluster has less replicas than requested")
	case exists > expected:
		recCtx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ConditionReasonScalingDown, "Cluster has more replicas than requested")
	default:
		recCtx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "")
	}

	if len(notUpdatedReplicas) == 0 && exists == expected {
		recCtx.Cluster.Status.CurrentRevision = recCtx.Cluster.Status.UpdateRevision
	}

	var err error
	if len(notReadyShards) == 0 {
		_, err = recCtx.UpsertConditionAndSendEvent(ctx, log, r,
			recCtx.NewCondition(v1.ConditionTypeReady, metav1.ConditionTrue, v1.ClickHouseConditionAllShardsReady, "All shards are ready"),
			corev1.EventTypeNormal, v1.EventReasonClusterReady, "ClickHouse cluster is ready",
		)
	} else {
		slices.Sort(notReadyShards)
		message := fmt.Sprintf("Not Ready shards: %v", notReadyShards)
		_, err = recCtx.UpsertConditionAndSendEvent(ctx, log, r,
			recCtx.NewCondition(v1.ConditionTypeReady, metav1.ConditionFalse, v1.ClickHouseConditionSomeShardsNotReady, message),
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
		if recCtx.Cluster.Spec.Settings.EnableDatabaseSync {
			switch {
			case !recCtx.databasesInSync:
				condType = metav1.ConditionFalse
				condReason = v1.ClickHouseConditionDatabasesNotCreated
				condMessage = "Some databases are not created on all replicas"
			case !recCtx.staleReplicasCleanedUp:
				condType = metav1.ConditionFalse
				condReason = v1.ClickHouseConditionReplicasNotCleanedUp
				condMessage = "Some stale replicas are not cleaned up"
			default:
				condType = metav1.ConditionTrue
				condReason = v1.ClickHouseConditionReplicasInSync
				condMessage = "Databases are sync on all replicas"
			}
		}

		recCtx.SetCondition(log, v1.ClickHouseConditionTypeSchemaInSync, condType, condReason, condMessage)
	}

	return nil, nil
}

func (r *ClusterReconciler) updateReplica(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext, id v1.ClickHouseReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", id)
	log.Info("updating replica")

	configMap, err := templateConfigMap(recCtx, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %s ConfigMap: %w", id, err)
	}

	configChanged, err := chctrl.ReconcileConfigMap(ctx, log, r, recCtx.Cluster, configMap)
	if err != nil {
		return nil, fmt.Errorf("update replica %s ConfigMap: %w", id, err)
	}

	statefulSet, err := templateStatefulSet(recCtx, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %s StatefulSet: %w", id, err)
	}

	if err := ctrl.SetControllerReference(recCtx.Cluster, statefulSet, r.Scheme); err != nil {
		return nil, fmt.Errorf("set replica %s StatefulSet controller reference: %w", id, err)
	}

	replica := recCtx.Replica(id)
	if replica.StatefulSet == nil {
		log.Info("replica StatefulSet not found, creating", "stateful_set", statefulSet.Name)
		ctrlutil.AddObjectConfigHash(statefulSet, recCtx.Cluster.Status.ConfigurationRevision)
		ctrlutil.AddHashWithKeyToAnnotations(statefulSet, ctrlutil.AnnotationSpecHash, recCtx.Cluster.Status.StatefulSetRevision)

		if err := chctrl.Create(ctx, r, recCtx.Cluster, statefulSet); err != nil {
			return nil, fmt.Errorf("create replica %s: %w", id, err)
		}

		return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
	}

	// Check if the StatefulSet is outdated and needs to be recreated
	v, err := semver.Parse(replica.StatefulSet.Annotations[ctrlutil.AnnotationStatefulSetVersion])
	if err != nil || breakingStatefulSetVersion.GT(v) {
		log.Warn(fmt.Sprintf("Removing the StatefulSet because of a breaking change. Found version: %v, expected version: %v", v, breakingStatefulSetVersion))

		if err := chctrl.Delete(ctx, r, recCtx.Cluster, replica.StatefulSet); err != nil {
			return nil, fmt.Errorf("recreate replica %s: %w", id, err)
		}

		return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
	}

	stsNeedsUpdate := replica.HasStatefulSetDiff(recCtx)

	// Trigger Pod restart if config changed
	if replica.HasConfigMapDiff(recCtx) {
		// Always restarts the Pod when ConfigMap changed. Need to add checks on whether restart is needed.
		// Use same way as Kubernetes for force restarting Pods one by one
		// (https://github.com/kubernetes/kubernetes/blob/22a21f974f5c0798a611987405135ab7e62502da/staging/src/k8s.io/kubectl/pkg/polymorphichelpers/objectrestarter.go#L41)
		// Not included by default in the StatefulSet so that hash-diffs work correctly
		log.Info("forcing ClickHouse Pod restart, because of config changes")

		statefulSet.Spec.Template.Annotations[ctrlutil.AnnotationRestartedAt] = time.Now().Format(time.RFC3339)

		ctrlutil.AddObjectConfigHash(replica.StatefulSet, recCtx.Cluster.Status.ConfigurationRevision)

		stsNeedsUpdate = true
	} else if restartedAt, ok := replica.StatefulSet.Spec.Template.Annotations[ctrlutil.AnnotationRestartedAt]; ok {
		statefulSet.Spec.Template.Annotations[ctrlutil.AnnotationRestartedAt] = restartedAt
	}

	if !stsNeedsUpdate {
		log.Debug("StatefulSet is up to date")

		if configChanged {
			return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
		}

		return nil, nil
	}

	if !gcmp.Equal(replica.StatefulSet.Spec.VolumeClaimTemplates[0].Spec, recCtx.Cluster.Spec.DataVolumeClaimSpec) {
		if err = r.updateReplicaPVC(ctx, log, recCtx, id); err != nil {
			return nil, fmt.Errorf("update replica %s PVC: %w", id, err)
		}

		statefulSet.Spec.VolumeClaimTemplates = replica.StatefulSet.Spec.VolumeClaimTemplates
	}

	log.Info("updating replica StatefulSet", "stateful_set", statefulSet.Name)
	log.Info("replica StatefulSet diff", "diff", gcmp.Diff(replica.StatefulSet.Spec, statefulSet.Spec))
	replica.StatefulSet.Spec = statefulSet.Spec
	replica.StatefulSet.Annotations = ctrlutil.MergeMaps(replica.StatefulSet.Annotations, statefulSet.Annotations)
	replica.StatefulSet.Labels = ctrlutil.MergeMaps(replica.StatefulSet.Labels, statefulSet.Labels)
	ctrlutil.AddHashWithKeyToAnnotations(replica.StatefulSet, ctrlutil.AnnotationSpecHash, recCtx.Cluster.Status.StatefulSetRevision)

	if err := chctrl.Update(ctx, r, recCtx.Cluster, replica.StatefulSet); err != nil {
		return nil, fmt.Errorf("update replica %s: %w", id, err)
	}

	return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
}

func (r *ClusterReconciler) updateReplicaPVC(ctx context.Context, log ctrlutil.Logger, recCtx *reconcileContext, id v1.ClickHouseReplicaID) error {
	var pvcs corev1.PersistentVolumeClaimList

	req := ctrlutil.AppRequirements(recCtx.Cluster.Namespace, recCtx.Cluster.SpecificName())
	for k, v := range labelsFromID(id) {
		idReq, _ := labels.NewRequirement(k, selection.Equals, []string{v})
		req.LabelSelector = req.LabelSelector.Add(*idReq)
	}

	log.Debug("listing replica PVCs", "replica_id", id, "selector", req.LabelSelector.String())

	if err := r.List(ctx, &pvcs, req); err != nil {
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

	if gcmp.Equal(pvcs.Items[0].Spec, recCtx.Cluster.Spec.DataVolumeClaimSpec) {
		log.Debug("replica PVC is up to date", "pvc", pvcs.Items[0].Name)
		return nil
	}

	targetSpec := recCtx.Cluster.Spec.DataVolumeClaimSpec.DeepCopy()
	if err := ctrlutil.ApplyDefault(targetSpec, pvcs.Items[0].Spec); err != nil {
		return fmt.Errorf("apply patch to replica PVC %q: %w", id, err)
	}

	log.Info("updating replica PVC", "pvc", pvcs.Items[0].Name, "diff", gcmp.Diff(pvcs.Items[0].Spec, targetSpec))

	pvcs.Items[0].Spec = *targetSpec
	if err := chctrl.Update(ctx, r, recCtx.Cluster, &pvcs.Items[0]); err != nil {
		return fmt.Errorf("update replica PVC %q: %w", id, err)
	}

	return nil
}
