package keeper

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/clickhouse-operator/internal/controller"
	"github.com/clickhouse-operator/internal/util"
	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
)

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

func (r replicaState) Ready(ctx *reconcileContext) bool {
	if r.StatefulSet == nil {
		return false
	}

	stsReady := r.StatefulSet.Status.ReadyReplicas == 1 // Not reliable, but allows to wait until pod is `green`
	if len(ctx.ReplicaState) == 1 {
		return stsReady && r.Status.ServerState == ModeStandalone
	}

	return stsReady && slices.Contains(ClusterModes, r.Status.ServerState)
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

	if !r.Ready(ctx) {
		return chctrl.StageNotReadyUpToDate
	}

	return chctrl.StageUpToDate
}

type reconcileContext struct {
	chctrl.ReconcileContextBase[*v1.KeeperCluster, v1.KeeperReplicaID, replicaState]

	// Should be populated after reconcileClusterRevisions with parsed extra config.
	ExtraConfig map[string]any
}

type ReconcileFunc func(util.Logger, *reconcileContext) (*ctrl.Result, error)

func (r *ClusterReconciler) Sync(ctx context.Context, log util.Logger, cr *v1.KeeperCluster) (ctrl.Result, error) {
	log.Info("Enter Keeper Reconcile", "spec", cr.Spec, "status", cr.Status)

	recCtx := reconcileContext{
		ReconcileContextBase: chctrl.ReconcileContextBase[*v1.KeeperCluster, v1.KeeperReplicaID, replicaState]{
			Cluster:      cr,
			Context:      ctx,
			ReplicaState: map[v1.KeeperReplicaID]replicaState{},
		},

		ExtraConfig: map[string]any{},
	}

	reconcileSteps := []ReconcileFunc{
		r.reconcileClusterRevisions,
		r.reconcileActiveReplicaStatus,
		r.reconcileQuorumMembership,
		r.reconcileCommonResources,
		r.reconcileReplicaResources,
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
				return ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}
			if k8serrors.IsAlreadyExists(err) {
				stepLog.Error(err, "create already existed resource, reschedule to retry")
				// retry immediately, as just creating already existed resource
				return ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}

			stepLog.Error(err, "unexpected error, setting conditions to unknown and rescheduling reconciliation to try again")
			errMsg := "Reconcile returned error"
			recCtx.SetConditions(log, []metav1.Condition{
				recCtx.NewCondition(v1.KeeperConditionTypeReconcileSucceeded, metav1.ConditionFalse, v1.KeeperConditionReasonStepFailed, errMsg),
				// Operator did not finish reconciliation, some conditions may not be valid already.
				recCtx.NewCondition(v1.KeeperConditionTypeReady, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeHealthy, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeReplicaStartupSucceeded, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeConfigurationInSync, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionUnknown, v1.KeeperConditionReasonStepFailed, errMsg),
			})

			return ctrl.Result{RequeueAfter: RequeueOnErrorTimeout}, r.upsertStatus(log, &recCtx)
		}

		if !stepResult.IsZero() {
			stepLog.Debug("reconcile step result", "result", stepResult)
			util.UpdateResult(&result, stepResult)
		}

		stepLog.Debug("reconcile step completed")
	}

	recCtx.SetCondition(log, v1.KeeperConditionTypeReconcileSucceeded, metav1.ConditionTrue, v1.KeeperConditionReasonReconcileFinished, "Reconcile succeeded")
	log.Info("reconciliation loop end", "result", result)

	if err := r.upsertStatus(log, &recCtx); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after reconciliation: %w", err)
	}

	return result, nil
}

func (r *ClusterReconciler) reconcileClusterRevisions(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	if ctx.Cluster.Status.ObservedGeneration != ctx.Cluster.Generation {
		ctx.Cluster.Status.ObservedGeneration = ctx.Cluster.Generation
		log.Debug(fmt.Sprintf("observed new CR generation %d", ctx.Cluster.Generation))
	}

	updateRevision, err := util.DeepHashObject(ctx.Cluster.Spec)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("get current spec revision: %w", err)
	}
	if updateRevision != ctx.Cluster.Status.UpdateRevision {
		ctx.Cluster.Status.UpdateRevision = updateRevision
		log.Debug(fmt.Sprintf("observed new CR revision %q", updateRevision))
	}

	var extraConfig map[string]interface{}
	if len(ctx.Cluster.Spec.Settings.ExtraConfig.Raw) > 0 {
		if err := json.Unmarshal(ctx.Cluster.Spec.Settings.ExtraConfig.Raw, &extraConfig); err != nil {
			return &ctrl.Result{}, fmt.Errorf("unmarshal extra config: %w", err)
		}

		ctx.ExtraConfig = extraConfig
	}

	configRevision, err := GetConfigurationRevision(ctx.Cluster, ctx.ExtraConfig)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("get configuration revision: %w", err)
	}
	if configRevision != ctx.Cluster.Status.ConfigurationRevision {
		ctx.Cluster.Status.ConfigurationRevision = configRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", configRevision))
	}

	stsRevision, err := GetStatefulSetRevision(ctx.Cluster)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("get StatefulSet revision: %w", err)
	}
	if stsRevision != ctx.Cluster.Status.StatefulSetRevision {
		ctx.Cluster.Status.StatefulSetRevision = stsRevision
		log.Debug(fmt.Sprintf("observed new StatefulSet revision %q", stsRevision))
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileActiveReplicaStatus(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	if ctx.Cluster.Replicas() == 0 {
		log.Debug("keeper replicaState count is zero")
		return nil, nil
	}

	listOpts := util.AppRequirements(ctx.Cluster.Namespace, ctx.Cluster.SpecificName())

	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx.Context, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	tlsRequired := ctx.Cluster.Spec.Settings.TLS.Required
	execResults := util.ExecuteParallel(statefulSets.Items, func(sts appsv1.StatefulSet) (v1.KeeperReplicaID, replicaState, error) {
		id, err := v1.KeeperReplicaIDFromLabels(sts.Labels)
		if err != nil {
			log.Error(err, "get replica ID from StatefulSet labels", "stateful_set", sts.Name)
			return -1, replicaState{}, err
		}

		hasError, err := chctrl.CheckPodError(ctx.Context, log, r.Client, &sts)
		if err != nil {
			log.Warn("failed to check replica pod error", "stateful_set", sts.Name, "error", err)
			hasError = true
		}

		status := getServerStatus(ctx.Context, log.With("replica_id", id), ctx.Cluster.HostnameById(id), tlsRequired)

		log.Debug("load replica state done", "replica_id", id, "statefulset", sts.Name)
		return id, replicaState{
			StatefulSet: &sts,
			Error:       hasError,
			Status:      status,
		}, nil
	})
	for id, res := range execResults {
		if res.Err != nil {
			log.Info("failed to load replica state", "error", res.Err, "replica_id", id)
			continue
		}

		if exists := ctx.SetReplica(id, res.Result); exists {
			log.Debug(fmt.Sprintf("multiple StatefulSets for single replica %v", id),
				"replica_id", id, "statefulset", res.Result.StatefulSet)
		}
	}

	// If replica existed before we need to mark it active as quorum expects it.
	if len(ctx.ReplicaState) > 0 && len(ctx.ReplicaState) < int(ctx.Cluster.Replicas()) {
		quorumReplicas, err := r.loadQuorumReplicas(ctx)
		if err != nil {
			return nil, fmt.Errorf("load quorum replicas: %w", err)
		}

		for id := range quorumReplicas {
			if _, exists := ctx.ReplicaState[id]; !exists {
				log.Info("adding missing replica from quorum config", "replica_id", id)
				ctx.SetReplica(id, replicaState{
					Error:       false,
					StatefulSet: nil,
					Status:      ServerStatus{},
				})
			}
		}
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileQuorumMembership(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	requestedReplicas := int(ctx.Cluster.Replicas())
	activeReplicas := len(ctx.ReplicaState)

	// New cluster creation, creates all replicas.
	if requestedReplicas > 0 && activeReplicas == 0 {
		log.Debug("creating all replicas")
		for id := range v1.KeeperReplicaID(requestedReplicas) { //nolint:gosec
			log.Info("creating new replica", "replica_id", id)
			ctx.SetReplica(id, replicaState{})
		}

		return &ctrl.Result{}, nil
	}

	// Scale to zero replica count could be applied without checks.
	if requestedReplicas == 0 && activeReplicas > 0 {
		log.Debug("deleting all replicas", "replicas", slices.Collect(maps.Keys(ctx.ReplicaState)))
		ctx.ReplicaState = map[v1.KeeperReplicaID]replicaState{}
		return &ctrl.Result{}, nil
	}

	if requestedReplicas == activeReplicas {
		return nil, nil
	}

	// For running cluster, allow quorum membership changes only in stable state.
	leaderMode := ModeLeader
	if activeReplicas == 1 {
		leaderMode = ModeStandalone
	}

	hasLeader := false
	for id, replica := range ctx.ReplicaState {
		if replica.HasConfigMapDiff(ctx) {
			log.Info("replica configurationRevision is stale, delaying quorum membership changes", "replica_id", id)
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
		if replica.HasStatefulSetDiff(ctx) {
			log.Info("replica statefulSetRevision is stale, delaying quorum membership changes", "replica_id", id)
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
		if !replica.Ready(ctx) {
			log.Info("replica is not Ready, delaying quorum membership changes", "replica_id", id)
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
		if replica.Status.ServerState == leaderMode {
			if hasLeader {
				log.Info("multiple leaders in cluster, delaying quorum membership changes", "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}

			hasLeader = true
			// Wait for deleted replicas to leave the quorum.
			if replica.Status.Followers > activeReplicas-1 {
				log.Info(fmt.Sprintf("leader has more followers than expected: %d > %d, delaying quorum membership changes", replica.Status.Followers, activeReplicas-1), "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			} else if replica.Status.Followers < activeReplicas-1 {
				log.Info(fmt.Sprintf("leader has less followers than expected: %d < %d, delaying quorum membership changes", replica.Status.Followers, activeReplicas-1), "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}
		}
	}
	if !hasLeader {
		log.Info("no leader in cluster, delaying quorum membership changes")
		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// Add single replica in quorum, allocating the first free id.
	if activeReplicas < requestedReplicas {
		for id := v1.KeeperReplicaID(1); ; id++ {
			if _, ok := ctx.ReplicaState[id]; !ok {
				log.Info("creating new replica", "replica_id", id)
				ctx.SetReplica(id, replicaState{})
				return nil, nil
			}
		}
	}

	// Remove single replica from the quorum. Prefer bigger id.
	if activeReplicas > requestedReplicas {
		chosenIndex := v1.KeeperReplicaID(-1)
		for id := range ctx.ReplicaState {
			if chosenIndex == -1 || chosenIndex < id {
				chosenIndex = id
			}
		}

		if chosenIndex == -1 {
			log.Warn(fmt.Sprintf("no replica in cluster, but requested scale down: %d < %d", requestedReplicas, activeReplicas))
		}

		log.Info("deleting replica", "replica_id", chosenIndex)
		delete(ctx.ReplicaState, chosenIndex)
		return nil, nil
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileCommonResources(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	service := TemplateHeadlessService(ctx.Cluster)
	if _, err := chctrl.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, service); err != nil {
		return &ctrl.Result{}, fmt.Errorf("reconcile service resource: %w", err)
	}

	pdb := TemplatePodDisruptionBudget(ctx.Cluster)
	if _, err := chctrl.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, pdb); err != nil {
		return &ctrl.Result{}, fmt.Errorf("reconcile PodDisruptionBudget resource: %w", err)
	}

	configMap, err := TemplateQuorumConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("template quorum config: %w", err)
	}

	if _, err = chctrl.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, configMap, "Data", "BinaryData"); err != nil {
		return nil, fmt.Errorf("reconcile quorum config: %w", err)
	}

	return nil, nil
}

// reconcileReplicaResources performs update on replicas ConfigMap and StatefulSet.
// If there are replicas that has no created StatefulSet, creates immediately.
// If all replicas exists performs rolling upgrade, with the following order preferences:
// NotExists -> CrashLoop/ImagePullErr -> OnlySts -> OnlyConfig -> Any.
func (r *ClusterReconciler) reconcileReplicaResources(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	highestStage := chctrl.StageUpToDate
	var replicasInStatus []v1.KeeperReplicaID

	for id, state := range ctx.ReplicaState {
		stage := state.UpdateStage(ctx)
		if stage == highestStage {
			replicasInStatus = append(replicasInStatus, id)
			continue
		}

		if stage > highestStage {
			highestStage = stage
			replicasInStatus = []v1.KeeperReplicaID{id}
		}
	}

	result := ctrl.Result{}

	switch highestStage {
	case chctrl.StageUpToDate:
		log.Info("all replicas are up to date")
		return nil, nil
	case chctrl.StageNotReadyUpToDate, chctrl.StageUpdating:
		log.Info("waiting for updated replicas to become ready", "replicas", replicasInStatus, "priority", highestStage.String())
		result = ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}
	case chctrl.StageHasDiff:
		// Leave one replica to rolling update. replicasInStatus must not be empty.
		// Prefer replicas with higher id.
		chosenReplica := replicasInStatus[0]
		for _, id := range replicasInStatus {
			if id > chosenReplica {
				chosenReplica = id
			}
		}
		log.Info(fmt.Sprintf("updating chosen replica %d with priority %s: %v", chosenReplica, highestStage.String(), replicasInStatus))
		replicasInStatus = []v1.KeeperReplicaID{chosenReplica}
	case chctrl.StageNotExists, chctrl.StageError:
		log.Info(fmt.Sprintf("updating replicas with priority %s: %v", highestStage.String(), replicasInStatus))
	}

	for _, id := range replicasInStatus {
		replicaResult, err := r.updateReplica(log, ctx, id)
		if err != nil {
			return nil, fmt.Errorf("update replica %q: %w", id, err)
		}

		util.UpdateResult(&result, replicaResult)
	}

	return &result, nil
}

func (r *ClusterReconciler) reconcileCleanUp(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	listOpts := util.AppRequirements(ctx.Cluster.Namespace, ctx.Cluster.SpecificName())
	var configMaps corev1.ConfigMapList
	if err := r.List(ctx.Context, &configMaps, listOpts); err != nil {
		return nil, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		if configMap.Name == ctx.Cluster.QuorumConfigMapName() {
			continue
		}

		id, err := v1.KeeperReplicaIDFromLabels(configMap.Labels)
		if err != nil {
			log.Warn("parse ConfigMap replica ID", "configmap", configMap.Name, "err", err)
			continue
		}

		if _, ok := ctx.ReplicaState[id]; !ok {
			log.Info("deleting stale ConfigMap", "configmap", configMap.Name)
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
		id, err := v1.KeeperReplicaIDFromLabels(sts.Labels)
		if err != nil {
			log.Warn("parse StatefulSet replica ID", "sts", sts.Name, "err", err)
			continue
		}

		if _, ok := ctx.ReplicaState[id]; !ok {
			log.Info("deleting stale StatefulSet", "statefuleset", sts.Name)
			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				return r.Delete(ctx.Context, &sts)
			}); err != nil {
				return nil, fmt.Errorf("delete StatefulSet %q: %w", sts.Name, err)
			}
		}
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileConditions(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	var errorReplicas []v1.KeeperReplicaID
	var notReadyReplicas []v1.KeeperReplicaID
	var notUpdatedReplicas []v1.KeeperReplicaID
	replicasByMode := map[string][]v1.KeeperReplicaID{}

	ctx.Cluster.Status.ReadyReplicas = 0
	for id, replica := range ctx.ReplicaState {
		if replica.Error {
			errorReplicas = append(errorReplicas, id)
		}

		if !replica.Ready(ctx) {
			notReadyReplicas = append(notReadyReplicas, id)
		} else {
			ctx.Cluster.Status.ReadyReplicas++
			replicasByMode[replica.Status.ServerState] = append(replicasByMode[replica.Status.ServerState], id)
		}

		if replica.HasConfigMapDiff(ctx) || replica.HasStatefulSetDiff(ctx) || !replica.Updated() {
			notUpdatedReplicas = append(notUpdatedReplicas, id)
		}
	}

	if len(errorReplicas) > 0 {
		slices.Sort(errorReplicas)
		message := fmt.Sprintf("Replicas has startup errors: %v", errorReplicas)
		ctx.SetCondition(log, v1.KeeperConditionTypeReplicaStartupSucceeded, metav1.ConditionFalse, v1.KeeperConditionReasonReplicaError, message)
	} else {
		ctx.SetCondition(log, v1.KeeperConditionTypeReplicaStartupSucceeded, metav1.ConditionTrue, v1.KeeperConditionReasonReplicasRunning, "")
	}

	if len(notReadyReplicas) > 0 {
		slices.Sort(notReadyReplicas)
		message := fmt.Sprintf("Not ready replicas: %v", notReadyReplicas)
		ctx.SetCondition(log, v1.KeeperConditionTypeHealthy, metav1.ConditionFalse, v1.KeeperConditionReasonReplicasNotReady, message)
	} else {
		ctx.SetCondition(log, v1.KeeperConditionTypeHealthy, metav1.ConditionTrue, v1.KeeperConditionReasonReplicasReady, "")
	}

	if len(notUpdatedReplicas) > 0 {
		slices.Sort(notUpdatedReplicas)
		message := fmt.Sprintf("Replicas has pending updates: %v", notUpdatedReplicas)
		ctx.SetCondition(log, v1.KeeperConditionTypeConfigurationInSync, metav1.ConditionFalse, v1.KeeperConditionReasonConfigurationChanged, message)
	} else {
		ctx.SetCondition(log, v1.KeeperConditionTypeConfigurationInSync, metav1.ConditionTrue, v1.KeeperConditionReasonUpToDate, "")
	}

	exists := len(ctx.ReplicaState)
	expected := int(ctx.Cluster.Replicas())

	if exists < expected {
		ctx.SetCondition(log, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.KeeperConditionReasonScalingUp, "Cluster has less replicas than requested")
	} else if exists > expected {
		ctx.SetCondition(log, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.KeeperConditionReasonScalingDown, "Cluster has more replicas than requested")
	} else {
		ctx.SetCondition(log, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.KeeperConditionReasonUpToDate, "")
	}

	if len(notUpdatedReplicas) == 0 && exists == expected {
		ctx.Cluster.Status.CurrentRevision = ctx.Cluster.Status.UpdateRevision
	}

	var status metav1.ConditionStatus
	var reason v1.ConditionReason
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

	ctx.SetCondition(log, v1.KeeperConditionTypeReady, status, reason, message)

	for _, condition := range ctx.Cluster.Status.Conditions {
		if condition.Status != metav1.ConditionTrue {
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
	}

	return &ctrl.Result{}, nil
}

func (r *ClusterReconciler) updateReplica(log util.Logger, ctx *reconcileContext, replicaID v1.KeeperReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", replicaID)
	log.Info("updating replica")

	configMap, err := TemplateConfigMap(ctx.Cluster, ctx.ExtraConfig, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q ConfigMap: %w", replicaID, err)
	}

	configChanged, err := chctrl.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, configMap, "Data", "BinaryData")
	if err != nil {
		return nil, fmt.Errorf("update replica %q ConfigMap: %w", replicaID, err)
	}

	statefulSet, err := TemplateStatefulSet(ctx.Cluster, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q StatefulSet: %w", replicaID, err)
	}
	if err := ctrl.SetControllerReference(ctx.Cluster, statefulSet, r.Scheme); err != nil {
		return nil, fmt.Errorf("set replica %q StatefulSet controller reference: %w", replicaID, err)
	}

	replica := ctx.Replica(replicaID)
	if replica.StatefulSet == nil {
		log.Info("replica StatefulSet not found, creating", "stateful_set", statefulSet.Name)
		util.AddObjectConfigHash(statefulSet, ctx.Cluster.Status.ConfigurationRevision)
		util.AddHashWithKeyToAnnotations(statefulSet, util.AnnotationSpecHash, ctx.Cluster.Status.StatefulSetRevision)

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

		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	stsNeedsUpdate := replica.HasStatefulSetDiff(ctx)

	// Trigger Pod restart if config changed
	if replica.HasConfigMapDiff(ctx) {
		// Use same way as Kubernetes for force restarting Pods one by one
		// (https://github.com/kubernetes/kubernetes/blob/22a21f974f5c0798a611987405135ab7e62502da/staging/src/k8s.io/kubectl/pkg/polymorphichelpers/objectrestarter.go#L41)
		// Not included by default in the StatefulSet so that hash-diffs work correctly
		log.Info("forcing keeper Pod restart, because of config changes")
		statefulSet.Spec.Template.Annotations[util.AnnotationRestartedAt] = time.Now().Format(time.RFC3339)
		util.AddObjectConfigHash(replica.StatefulSet, ctx.Cluster.Status.ConfigurationRevision)
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
	util.AddHashWithKeyToAnnotations(replica.StatefulSet, util.AnnotationSpecHash, ctx.Cluster.Status.StatefulSetRevision)
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return r.Update(ctx.Context, replica.StatefulSet)
	}); err != nil {
		return nil, fmt.Errorf("update replica %q StatefulSet %q: %w", replicaID, statefulSet.Name, err)
	}

	return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
}

func (r *ClusterReconciler) loadQuorumReplicas(ctx *reconcileContext) (map[v1.KeeperReplicaID]struct{}, error) {
	configMap := corev1.ConfigMap{}
	err := r.Get(
		ctx.Context,
		types.NamespacedName{Namespace: ctx.Cluster.Namespace, Name: ctx.Cluster.QuorumConfigMapName()},
		&configMap,
	)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return map[v1.KeeperReplicaID]struct{}{}, nil
		}
		return nil, fmt.Errorf("get quorum configmap: %w", err)
	}

	var config struct {
		KeeperServer struct {
			RaftConfiguration struct {
				Server QuorumConfig `yaml:"server"`
			} `yaml:"raft_configuration"`
		} `yaml:"keeper_server"`
	}
	if err := yaml.Unmarshal([]byte(configMap.Data[QuorumConfigFileName]), &config); err != nil {
		return nil, fmt.Errorf("unmarshal quorum config: %w", err)
	}
	replicas := map[v1.KeeperReplicaID]struct{}{}
	for _, member := range config.KeeperServer.RaftConfiguration.Server {
		id, err := strconv.ParseInt(member.ID, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parse replica ID %q: %w", member.ID, err)
		}
		replicas[v1.KeeperReplicaID(id)] = struct{}{}
	}

	return replicas, nil
}

func (r *ClusterReconciler) upsertStatus(log util.Logger, ctx *reconcileContext) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		crdInstance := &v1.KeeperCluster{}
		if err := r.Reader.Get(ctx.Context, ctx.Cluster.NamespacedName(), crdInstance); err != nil {
			return err
		}
		preStatus := crdInstance.Status.DeepCopy()

		if reflect.DeepEqual(*preStatus, ctx.Cluster.Status) {
			log.Info("statuses are equal, nothing to do")
			return nil
		}
		log.Debug(fmt.Sprintf("status difference:\n%s", cmp.Diff(*preStatus, ctx.Cluster.Status)))
		crdInstance.Status = ctx.Cluster.Status
		return r.Status().Update(ctx.Context, crdInstance)
	})
}
