package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// schemaSyncResult records the outcome of each schema sync step for the steps after it.
type schemaSyncResult struct {
	defaultDBReplicated map[v1.ClickHouseReplicaID]bool
	databasesSynced     map[v1.ClickHouseReplicaID]bool

	admissionFailed bool

	staleReplicasCleanedUp bool
}

func (r *clickhouseReconciler) reconcileSchemaSync(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if !r.Cluster.Spec.Settings.DatabaseSyncEnabled() {
		log.Debug("database sync is disabled, skipping")
		return chctrl.StepContinue(), nil
	}

	if r.commander == nil {
		log.Debug("commander is not ready, skipping")
		return chctrl.StepContinue(), nil
	}

	defaultDBRes := ctrlutil.ExecuteParallel(r.reachableReplicas, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, bool, error) {
		if r.ReplicaState[id].Initialized() {
			return id, true, nil
		}

		opCtx, cancel := context.WithTimeout(ctx, databaseOpsTimeout)
		defer cancel()

		replicated, err := r.commander.EnsureReplicaDefaultDatabaseEngine(opCtx, log, id)

		return id, replicated, err
	})

	r.schemaSync.defaultDBReplicated = map[v1.ClickHouseReplicaID]bool{}
	for id, res := range defaultDBRes {
		if res.Err != nil {
			log.Info("failed to recreate default database as Replicated", "replica_id", id, "error", res.Err)

			continue
		}

		r.schemaSync.defaultDBReplicated[id] = res.Result
	}

	// Initialized replicas served client traffic: they define the database list and only joining replicas receive it.
	var sources, targets []v1.ClickHouseReplicaID

	for _, id := range r.reachableReplicas {
		if r.ReplicaState[id].Initialized() {
			sources = append(sources, id)
		} else {
			targets = append(targets, id)
		}
	}

	if len(sources) == 0 {
		for id, state := range r.ReplicaState {
			if state.Initialized() && state.Pod.DeletionTimestamp == nil && !r.Cluster.ReplicaObsolete(id) {
				log.Info("no initialized replica is reachable to replicate schema from, holding joining replicas")

				r.schemaSync.databasesSynced = map[v1.ClickHouseReplicaID]bool{}

				return chctrl.StepContinue(), nil
			}
		}

		// No replica served traffic yet, e.g. a new cluster: merge the databases of all of them.
		sources = r.reachableReplicas
	}

	if len(targets) == 0 {
		r.schemaSync.databasesSynced = map[v1.ClickHouseReplicaID]bool{}

		return chctrl.StepContinue(), nil
	}

	syncCtx, cancel := context.WithTimeout(ctx, databaseSyncTimeout)
	defer cancel()

	r.schemaSync.databasesSynced = r.commander.SyncDatabases(syncCtx, log, sources, targets)

	return chctrl.StepContinue(), nil
}

// reconcileReplicaReadiness opens client traffic for ready replicas and withdraws it from replicas scheduled for deletion.
func (r *clickhouseReconciler) reconcileReplicaReadiness(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if r.Cluster.Spec.Settings.DatabaseSyncEnabled() && r.commander == nil {
		log.Debug("commander is not ready, skipping")
		return chctrl.StepContinue(), nil
	}

	trafficAdmitted := r.trafficAdmission()

	changed := slices.DeleteFunc(slices.Collect(maps.Keys(trafficAdmitted)), func(id v1.ClickHouseReplicaID) bool {
		return r.ReplicaState[id].Initialized() == trafficAdmitted[id]
	})

	res := ctrlutil.ExecuteParallel(changed, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, struct{}, error) {
		state := r.ReplicaState[id]
		if state.Pod == nil {
			return id, struct{}{}, errors.New("pod does not exist yet")
		}

		cond := corev1.PodCondition{
			Type:               v1.ReplicaInitializedCondition,
			LastTransitionTime: metav1.Now(),
			Status:             corev1.ConditionTrue,
			Reason:             v1.ReplicaInitializedReason,
			Message:            "Replica initialization completed",
		}

		switch {
		case r.Cluster.ReplicaObsolete(id):
			cond.Status, cond.Reason, cond.Message = corev1.ConditionFalse, v1.ReplicaDeletionReason, "Replica is scheduled for deletion"
		case !trafficAdmitted[id]:
			cond.Status, cond.Reason, cond.Message = corev1.ConditionFalse, v1.ReplicaNotInitializedReason, "Replica is not initialized"
		}

		if err := r.UpdatePodCondition(ctx, state.Pod, cond); err != nil {
			return id, struct{}{}, fmt.Errorf("update replica initialized condition: %w", err)
		}

		return id, struct{}{}, nil
	})

	failed := false
	for id, result := range res {
		if result.Err != nil {
			log.Info("failed to set replica initialized condition", "replica_id", id, "error", result.Err)

			failed = true
			// A failed withdrawal is retried, but it is not a schema problem for SchemaInSync.
			r.schemaSync.admissionFailed = r.schemaSync.admissionFailed || !r.Cluster.ReplicaObsolete(id)
		}
	}

	if failed {
		return chctrl.StepRequeue(chctrl.RequeueProbePoll), nil
	}

	return chctrl.StepContinue(), nil
}

// reconcilePreScaleDownSync syncs the surviving replicas of shards that lose replicas, so CleanUp may delete them.
func (r *clickhouseReconciler) reconcilePreScaleDownSync(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if !r.Cluster.Spec.Settings.DatabaseSyncEnabled() || r.commander == nil {
		return chctrl.StepContinue(), nil
	}

	shardHasReachableReplica := map[int32]bool{}
	for _, id := range r.reachableReplicas {
		shardHasReachableReplica[id.ShardID] = true
	}

	// Sync only shards that are going to drop replicas partially.
	shardsToSyncSet := map[int32]struct{}{}

	for id := range r.ReplicaState {
		if !r.Cluster.ReplicaObsolete(id) {
			continue
		}

		switch {
		case id.ShardID >= r.Cluster.Shards():
			// Whole shard removal drops the shard data by design, no surviving replicas to sync.
		case shardHasReachableReplica[id.ShardID]:
			shardsToSyncSet[id.ShardID] = struct{}{}
		default:
			log.Debug("no reachable replicas in shard, skipping sync", "shard", id.ShardID)
			r.unsyncedShards[id.ShardID] = true
		}
	}

	syncRes := ctrlutil.ExecuteParallel(slices.Collect(maps.Keys(shardsToSyncSet)), func(shardID int32) (int32, struct{}, error) {
		log.Info("pre scale-down shard sync", "shard_id", shardID)

		syncCtx, cancel := context.WithTimeout(ctx, shardSyncTimeout)
		defer cancel()

		return shardID, struct{}{}, r.commander.SyncShard(syncCtx, log, shardID)
	})

	for id, res := range syncRes {
		if res.Err != nil {
			log.Info("failed to sync shard", "shard_id", id, "error", res.Err)
			r.unsyncedShards[id] = true
		}
	}

	return chctrl.StepContinue(), nil
}

// reconcileReplicaMetadataCleanup drops removed replicas from their Replicated databases once CleanUp deleted them.
func (r *clickhouseReconciler) reconcileReplicaMetadataCleanup(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if !r.Cluster.Spec.Settings.DatabaseSyncEnabled() || r.commander == nil {
		return chctrl.StepContinue(), nil
	}

	r.schemaSync.staleReplicasCleanedUp = true

	if len(r.reachableReplicas) == 0 {
		return chctrl.StepContinue(), nil
	}

	runningReplicas := map[v1.ClickHouseReplicaID]struct{}{}
	for id := range r.ReplicaState {
		runningReplicas[id] = struct{}{}
	}

	cleanupCtx, cancel := context.WithTimeout(ctx, databaseOpsTimeout)
	defer cancel()

	if err := r.commander.CleanupDatabaseReplicas(cleanupCtx, log, runningReplicas); err != nil {
		log.Warn("failed to cleanup database replicas", "error", err)

		r.schemaSync.staleReplicasCleanedUp = false
	}

	return chctrl.StepContinue(), nil
}

// reconcileSyncCondition reports the SchemaInSync condition from the outcome of the schema sync steps.
func (r *clickhouseReconciler) reconcileSyncCondition(_ context.Context, _ ctrlutil.Logger) (chctrl.StepResult, error) {
	if !r.Cluster.Spec.Settings.DatabaseSyncEnabled() {
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionTrue,
			Reason:  v1.ClickHouseConditionSchemaSyncDisabled,
			Message: "Database schema sync is disabled",
		})

		return chctrl.StepContinue(), nil
	}

	// The schema sync steps did not run, the condition keeps the state set at the start of the reconcile.
	if r.schemaSync.databasesSynced == nil {
		return chctrl.StepContinue(), nil
	}

	var nonReplicatedDefaultDatabaseIDs []string

	for id, replicated := range r.schemaSync.defaultDBReplicated {
		if !replicated {
			nonReplicatedDefaultDatabaseIDs = append(nonReplicatedDefaultDatabaseIDs, id.String())
		}
	}

	allReplicasReachable := len(r.reachableReplicas) == len(r.ReplicaState)
	allReplicasInitialized := allReplicasReachable && !r.schemaSync.admissionFailed
	allDatabasesSynced := allReplicasReachable

	for _, id := range r.reachableReplicas {
		if _, prepared := r.schemaSync.defaultDBReplicated[id]; !prepared {
			allReplicasInitialized = false
		}

		if !r.ReplicaState[id].Initialized() && !r.schemaSync.databasesSynced[id] {
			allDatabasesSynced = false
		}
	}

	switch {
	case len(nonReplicatedDefaultDatabaseIDs) > 0:
		slices.Sort(nonReplicatedDefaultDatabaseIDs)
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionFalse,
			Reason:  v1.ClickHouseConditionDefaultDatabaseNotReplicated,
			Message: "Default database is not Replicated on replicas: " + strings.Join(nonReplicatedDefaultDatabaseIDs, ", "),
		})

		return chctrl.StepRequeue(chctrl.RequeueProbePoll), nil

	case !allReplicasInitialized || !allDatabasesSynced:
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionFalse,
			Reason:  v1.ClickHouseConditionDatabasesNotCreated,
			Message: "Some databases are not created on all replicas",
		})

		return chctrl.StepRequeue(chctrl.RequeueProbePoll), nil

	case !r.schemaSync.staleReplicasCleanedUp:
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeSchemaInSync,
			Status:  metav1.ConditionFalse,
			Reason:  v1.ClickHouseConditionReplicasNotCleanedUp,
			Message: "Some stale replicas are not cleaned up",
		})

		return chctrl.StepRequeue(chctrl.RequeueProbePoll), nil

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

// trafficAdmission decides which replicas may serve client traffic.
func (r *clickhouseReconciler) trafficAdmission() map[v1.ClickHouseReplicaID]bool {
	trafficAdmitted := map[v1.ClickHouseReplicaID]bool{}

	for id, state := range r.ReplicaState {
		switch {
		// Withdraw traffic from obsolete replicas
		case r.Cluster.ReplicaObsolete(id):
			trafficAdmitted[id] = false
		// Keep traffic on initialized replicas
		case state.Initialized():
			trafficAdmitted[id] = true
		// Admit all replicas if sync disabled
		case !r.Cluster.Spec.Settings.DatabaseSyncEnabled():
			trafficAdmitted[id] = true
		default:
			// Presence, not the value: a populated non-Replicated default database is preserved and still admitted.
			_, prepared := r.schemaSync.defaultDBReplicated[id]
			trafficAdmitted[id] = prepared && r.schemaSync.databasesSynced[id]
		}
	}

	return trafficAdmitted
}
