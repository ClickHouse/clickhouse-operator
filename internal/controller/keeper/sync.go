package keeper

import (
	"context"
	"encoding/json"
	"fmt"
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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	if len(ctx.Cluster.Status.Replicas) == 1 {
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

	configDiff := r.HasConfigMapDiff(ctx)
	stsDiff := r.HasStatefulSetDiff(ctx)

	if !r.Ready(ctx) {
		if configDiff || stsDiff {
			return chctrl.StageNotReadyWithDiff
		}
		return chctrl.StageNotReadyUpToDate
	}

	switch {
	case configDiff && stsDiff:
		return chctrl.StageStsAndConfigDiff
	case configDiff:
		return chctrl.StageConfigDiff
	case stsDiff:
		return chctrl.StageStsDiff
	}

	return chctrl.StageUpToDate
}

type reconcileContext struct {
	chctrl.ReconcileContextBase[*v1.KeeperCluster, string, replicaState]

	// Should be populated after reconcileClusterRevisions with parsed extra config.
	ExtraConfig map[string]any
}

type ReconcileFunc func(util.Logger, *reconcileContext) (*ctrl.Result, error)

func (r *ClusterReconciler) Sync(ctx context.Context, log util.Logger, cr *v1.KeeperCluster) (ctrl.Result, error) {
	log.Info("Enter Keeper Reconcile", "spec", cr.Spec, "status", cr.Status)

	recCtx := reconcileContext{
		ReconcileContextBase: chctrl.ReconcileContextBase[*v1.KeeperCluster, string, replicaState]{
			Cluster:      cr,
			Context:      ctx,
			ReplicaState: map[string]replicaState{},
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

	hostnamesByID := map[string]string{}

	for _, id := range ctx.Cluster.Status.Replicas {
		var replicaSts appsv1.StatefulSet
		if err := r.Get(ctx.Context, types.NamespacedName{
			Namespace: ctx.Cluster.Namespace,
			Name:      ctx.Cluster.StatefulSetNameByReplicaID(id),
		}, &replicaSts); err != nil {
			if !k8serrors.IsNotFound(err) {
				return &ctrl.Result{}, fmt.Errorf("get replicas %q sts: %w", id, err)
			}

			log.Debug("StatefulSet not found", "replica_id", id, "statefuleset", ctx.Cluster.StatefulSetNameByReplicaID(id))
		} else {
			hostnamesByID[id] = ctx.Cluster.HostnameById(id)
			isError, err := chctrl.CheckPodError(ctx.Context, log, r.Client, &replicaSts)
			if err != nil {
				return &ctrl.Result{}, fmt.Errorf("get replica %q error state: %w", id, err)
			}

			ctx.SetReplica(id, replicaState{
				Error:       isError,
				StatefulSet: &replicaSts,
			})
		}
	}

	checkCtx, cancel := context.WithTimeout(ctx.Context, StatusRequestTimeout)
	defer cancel()

	for id, state := range getServersStates(checkCtx, log, hostnamesByID, ctx.Cluster.Spec.Settings.TLS.Required) {
		replica := ctx.Replica(id)
		replica.Status = state
		ctx.SetReplica(id, replica)
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileQuorumMembership(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	requestedReplicas := int(ctx.Cluster.Replicas())

	// New cluster creation, creates all replicas.
	if requestedReplicas > 0 && len(ctx.Cluster.Status.Replicas) == 0 {
		log.Debug("creating all replicas")
		for i := range requestedReplicas {
			id := strconv.Itoa(i + 1)
			log.Info("creating new replica", "replica_id", id)
			ctx.Cluster.Status.Replicas = append(ctx.Cluster.Status.Replicas, id)
		}

		return &ctrl.Result{}, nil
	}

	// Scale to zero replica count could be applied without checks.
	if requestedReplicas == 0 && len(ctx.Cluster.Status.Replicas) > 0 {
		log.Debug("deleting all replicas", "replicas", ctx.Cluster.Status.Replicas)
		ctx.Cluster.Status.Replicas = nil
		return &ctrl.Result{}, nil
	}

	if requestedReplicas == len(ctx.Cluster.Status.Replicas) {
		return nil, nil
	}

	// For running cluster, allow quorum membership changes only in stable state.
	leaderMode := ModeLeader
	if len(ctx.Cluster.Status.Replicas) == 1 {
		leaderMode = ModeStandalone
	}

	hasLeader := false
	for _, id := range ctx.Cluster.Status.Replicas {
		replica := ctx.Replica(id)

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
			if replica.Status.Followers > len(ctx.Cluster.Status.Replicas)-1 {
				log.Info(fmt.Sprintf("leader has more followers than expected: %d > %d, delaying quorum membership changes", replica.Status.Followers, len(ctx.Cluster.Status.Replicas)-1), "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			} else if replica.Status.Followers < len(ctx.Cluster.Status.Replicas)-1 {
				log.Info(fmt.Sprintf("leader has less followers than expected: %d < %d, delaying quorum membership changes", replica.Status.Followers, len(ctx.Cluster.Status.Replicas)-1), "replica_id", id)
				return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
			}
		}
	}
	if !hasLeader {
		log.Info("no leader in cluster, delaying quorum membership changes")
		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// Add single replica in quorum, allocating the first free id.
	if len(ctx.Cluster.Status.Replicas) < requestedReplicas {
		for i := 1; ; i++ {
			id := strconv.Itoa(i)
			if !slices.Contains(ctx.Cluster.Status.Replicas, id) {
				log.Info("creating new replica", "replica_id", id)
				ctx.Cluster.Status.Replicas = append(ctx.Cluster.Status.Replicas, id)
				return nil, nil
			}
		}
	}

	// Remove single replica from the quorum. Prefer bigger id.
	if len(ctx.Cluster.Status.Replicas) > requestedReplicas {
		chosenIndex := -1
		for i, id := range ctx.Cluster.Status.Replicas {
			if chosenIndex == -1 || ctx.Cluster.Status.Replicas[chosenIndex] < id {
				chosenIndex = i
			}
		}

		if chosenIndex == -1 {
			log.Warn(fmt.Sprintf("no replica in cluster, but requested scale down: %d < %d", requestedReplicas, len(ctx.Cluster.Status.Replicas)))
		}

		log.Info("deleting replica", "replica_id", ctx.Cluster.Status.Replicas[chosenIndex])
		ctx.Cluster.Status.Replicas = slices.Delete(ctx.Cluster.Status.Replicas, chosenIndex, chosenIndex+1)
		return nil, nil
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileCommonResources(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	service := TemplateHeadlessService(ctx.Cluster)
	if _, err := util.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, service); err != nil {
		return &ctrl.Result{}, fmt.Errorf("reconcile service resource: %w", err)
	}

	pdb := TemplatePodDisruptionBudget(ctx.Cluster)
	if _, err := util.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, pdb); err != nil {
		return &ctrl.Result{}, fmt.Errorf("reconcile PodDisruptionBudget resource: %w", err)
	}

	configMap, err := TemplateQuorumConfig(ctx.Cluster)
	if err != nil {
		return nil, fmt.Errorf("template quorum config: %w", err)
	}

	if _, err = util.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, configMap, "Data", "BinaryData"); err != nil {
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
	var replicasInStatus []string

	for _, id := range ctx.Cluster.Status.Replicas {
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
	case chctrl.StageUpToDate:
		log.Info("all replicas are up to date")
		return nil, nil
	case chctrl.StageNotReadyUpToDate, chctrl.StageUpdating:
		log.Info("waiting for updated replicas to become ready", "replicas", replicasInStatus, "priority", highestStage.String())
		result = ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}
	case chctrl.StageStsAndConfigDiff, chctrl.StageConfigDiff, chctrl.StageStsDiff:
		// Leave one replica to rolling update. replicasInStatus must not be empty.
		// Prefer replicas with higher id.
		chosenReplica := replicasInStatus[0]
		for _, id := range replicasInStatus {
			if id > chosenReplica {
				chosenReplica = id
			}
		}
		log.Info(fmt.Sprintf("updating chosen replica %s with priority %s: %v", chosenReplica, highestStage.String(), replicasInStatus))
		replicasInStatus = []string{chosenReplica}
	case chctrl.StageNotReadyWithDiff, chctrl.StageNotExists, chctrl.StageError:
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
	appReq, err := labels.NewRequirement(util.LabelAppKey, selection.Equals, []string{ctx.Cluster.SpecificName()})
	if err != nil {
		return nil, fmt.Errorf("make %q requirement to list: %w", util.LabelAppKey, err)
	}
	listOpts := &client.ListOptions{
		Namespace:     ctx.Cluster.Namespace,
		LabelSelector: labels.NewSelector().Add(*appReq),
	}

	var configMaps corev1.ConfigMapList
	if err := r.List(ctx.Context, &configMaps, listOpts); err != nil {
		return nil, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		if configMap.Name == ctx.Cluster.QuorumConfigMapName() {
			continue
		}

		replicaID := configMap.Labels[util.LabelKeeperReplicaID]
		if !slices.Contains(ctx.Cluster.Status.Replicas, replicaID) {
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
		replicaID := sts.Labels[util.LabelKeeperReplicaID]
		if !slices.Contains(ctx.Cluster.Status.Replicas, replicaID) {
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
	var errorReplicas []string
	var notReadyReplicas []string
	var notUpdatedReplicas []string
	replicasByMode := map[string][]string{}

	ctx.Cluster.Status.ReadyReplicas = 0
	for _, id := range ctx.Cluster.Status.Replicas {
		replica := ctx.Replica(id)

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

	exists := len(ctx.Cluster.Status.Replicas)
	expected := int(ctx.Cluster.Replicas())

	if exists < expected {
		ctx.SetCondition(log, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.KeeperConditionReasonScalingUp, "Cluster has less replicas that requested")
	} else if exists > expected {
		ctx.SetCondition(log, v1.KeeperConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.KeeperConditionReasonScalingDown, "Cluster has more replicas that requested")
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

func (r *ClusterReconciler) updateReplica(log util.Logger, ctx *reconcileContext, replicaID string) (*ctrl.Result, error) {
	log = log.With("replica_id", replicaID)
	log.Info("updating replica")

	configMap, err := TemplateConfigMap(ctx.Cluster, ctx.ExtraConfig, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q ConfigMap: %w", replicaID, err)
	}

	configChanged, err := util.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, configMap, "Data", "BinaryData")
	if err != nil {
		return nil, fmt.Errorf("update replica %q ConfigMap: %w", replicaID, err)
	}

	statefulSet := TemplateStatefulSet(ctx.Cluster, replicaID)
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
