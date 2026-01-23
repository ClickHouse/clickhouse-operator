package keeper

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/clickhouse-operator/internal/controller"
	"github.com/clickhouse-operator/internal/util"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

type ReconcileContextBase = chctrl.ReconcileContextBase[v1.KeeperClusterStatus, *v1.KeeperCluster, v1.KeeperReplicaID, replicaState]

type reconcileContext struct {
	ReconcileContextBase

	// Should be populated after reconcileClusterRevisions with parsed extra config.
	ExtraConfig map[string]any
	// Computed by reconcileActiveReplicaStatus
	HorizontalScaleAllowed bool
}

type ReconcileFunc func(util.Logger, *reconcileContext) (*ctrl.Result, error)

func (r *ClusterReconciler) Sync(ctx context.Context, log util.Logger, cr *v1.KeeperCluster) (ctrl.Result, error) {
	log.Info("Enter Keeper Reconcile", "spec", cr.Spec, "status", cr.Status)

	recCtx := reconcileContext{
		ReconcileContextBase: ReconcileContextBase{
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
				recCtx.NewCondition(v1.ConditionTypeReconcileSucceeded, metav1.ConditionFalse, v1.ConditionReasonStepFailed, errMsg),
				// Operator did not finish reconciliation, some conditions may not be valid already.
				recCtx.NewCondition(v1.ConditionTypeReady, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ConditionTypeHealthy, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ConditionTypeConfigurationInSync, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ConditionTypeClusterSizeAligned, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.KeeperConditionTypeScaleAllowed, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
			})

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

	var extraConfig map[string]interface{}
	if len(ctx.Cluster.Spec.Settings.ExtraConfig.Raw) > 0 {
		if err := json.Unmarshal(ctx.Cluster.Spec.Settings.ExtraConfig.Raw, &extraConfig); err != nil {
			return nil, fmt.Errorf("unmarshal extra config: %w", err)
		}

		ctx.ExtraConfig = extraConfig
	}

	configRevision, err := GetConfigurationRevision(ctx.Cluster, ctx.ExtraConfig)
	if err != nil {
		return nil, fmt.Errorf("get configuration revision: %w", err)
	}
	if configRevision != ctx.Cluster.Status.ConfigurationRevision {
		ctx.Cluster.Status.ConfigurationRevision = configRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", configRevision))
	}

	stsRevision, err := GetStatefulSetRevision(ctx.Cluster)
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

	if err := r.checkHorizontalScalingAllowed(log, ctx); err != nil {
		return nil, err
	}
	if !ctx.HorizontalScaleAllowed {
		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileQuorumMembership(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	requestedReplicas := int(ctx.Cluster.Replicas())
	activeReplicas := len(ctx.ReplicaState)

	if activeReplicas == requestedReplicas {
		if _, err := ctx.UpsertConditionAndSendEvent(r, log,
			ctx.NewCondition(
				v1.ConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "",
			), corev1.EventTypeNormal, v1.EventReasonHorizontalScaleCompleted,
			"Cluster is scaled to the requested size: %d replicas", requestedReplicas,
		); err != nil {
			return nil, fmt.Errorf("update cluster size aligned condition: %w", err)
		}

		return nil, nil
	}

	// New cluster creation, creates all replicas.
	if requestedReplicas > 0 && activeReplicas == 0 {
		log.Debug("creating all replicas")
		r.Recorder.Eventf(ctx.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaCreated,
			"Initial cluster creation, creating %d replicas", requestedReplicas)
		ctx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "")
		for id := range v1.KeeperReplicaID(requestedReplicas) { //nolint:gosec
			log.Info("creating new replica", "replica_id", id)
			ctx.SetReplica(id, replicaState{})
		}

		return nil, nil
	}

	// Scale to zero replica count could be applied without checks.
	if requestedReplicas == 0 && activeReplicas > 0 {
		log.Debug("deleting all replicas", "replicas", slices.Collect(maps.Keys(ctx.ReplicaState)))
		ctx.SetCondition(log, v1.ConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "")
		r.Recorder.Eventf(ctx.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaDeleted,
			"Cluster scaled to 0 nodes, removing all %d replicas", len(ctx.ReplicaState))
		ctx.ReplicaState = map[v1.KeeperReplicaID]replicaState{}
		return nil, nil
	}

	var err error
	if activeReplicas < requestedReplicas {
		_, err = ctx.UpsertConditionAndSendEvent(r, log,
			ctx.NewCondition(
				v1.ConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ConditionReasonScalingUp,
				"Cluster has less replicas than requested",
			), corev1.EventTypeNormal, v1.EventReasonHorizontalScaleStarted,
			"Cluster scale up is started: current replicas %d, requested %d",
			activeReplicas, requestedReplicas,
		)
	} else {
		_, err = ctx.UpsertConditionAndSendEvent(r, log,
			ctx.NewCondition(
				v1.ConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ConditionReasonScalingDown,
				"Cluster has more replicas than requested",
			), corev1.EventTypeNormal, v1.EventReasonHorizontalScaleStarted,
			"Cluster scale down is started: current replicas %d, requested %d",
			activeReplicas, requestedReplicas,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("update cluster size aligned condition: %w", err)
	}

	// For running cluster, allow quorum membership changes only in stable state.
	if !ctx.HorizontalScaleAllowed {
		log.Info("Delaying horizontal scaling, cluster is not in stable state")
		return &reconcile.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// Add single replica in quorum, allocating the first free id.
	if activeReplicas < requestedReplicas {
		for id := v1.KeeperReplicaID(1); ; id++ {
			if _, ok := ctx.ReplicaState[id]; !ok {
				log.Info("creating new replica", "replica_id", id)
				r.Recorder.Eventf(ctx.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaCreated,
					"Adding new replica %q to the cluster", ctx.Cluster.HostnameById(id))
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
		r.Recorder.Eventf(ctx.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaDeleted,
			"Deleting replica %q from the cluster", ctx.Cluster.HostnameById(chosenIndex))
		delete(ctx.ReplicaState, chosenIndex)
		return nil, nil
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileCommonResources(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	service := TemplateHeadlessService(ctx.Cluster)
	if _, err := chctrl.ReconcileService(ctx.Context, log, r, ctx.Cluster, service); err != nil {
		return nil, fmt.Errorf("reconcile service resource: %w", err)
	}

	pdb := TemplatePodDisruptionBudget(ctx.Cluster)
	if _, err := chctrl.ReconcilePodDisruptionBudget(ctx.Context, log, r, ctx.Cluster, pdb); err != nil {
		return nil, fmt.Errorf("reconcile PodDisruptionBudget resource: %w", err)
	}

	configMap, err := TemplateQuorumConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("template quorum config: %w", err)
	}

	if _, err = chctrl.ReconcileConfigMap(ctx.Context, log, r, ctx.Cluster, configMap); err != nil {
		return nil, fmt.Errorf("reconcile quorum config: %w", err)
	}

	return nil, nil
}

// reconcileReplicaResources performs update on replicas ConfigMap and StatefulSet.
// If there are replicas that has no created StatefulSet, creates immediately.
// If all replicas exists performs rolling upgrade, with the following order preferences:
// NotExists -> CrashLoop/ImagePullErr -> HasDiff -> Any.
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
			if err := chctrl.Delete(ctx.Context, r, ctx.Cluster, &configMap); err != nil {
				return nil, err
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
			if err := chctrl.Delete(ctx.Context, r, ctx.Cluster, &sts); err != nil {
				return nil, err
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
		ctx.SetCondition(log, v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionFalse, v1.ConditionReasonReplicaError, message)
	} else {
		ctx.SetCondition(log, v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionTrue, v1.ConditionReasonReplicasRunning, "")
	}

	if len(notReadyReplicas) > 0 {
		slices.Sort(notReadyReplicas)
		message := fmt.Sprintf("Not ready replicas: %v", notReadyReplicas)
		ctx.SetCondition(log, v1.ConditionTypeHealthy, metav1.ConditionFalse, v1.ConditionReasonReplicasNotReady, message)
	} else {
		ctx.SetCondition(log, v1.ConditionTypeHealthy, metav1.ConditionTrue, v1.ConditionReasonReplicasReady, "")
	}

	if len(notUpdatedReplicas) > 0 {
		slices.Sort(notUpdatedReplicas)
		message := fmt.Sprintf("Replicas has pending updates: %v", notUpdatedReplicas)
		ctx.SetCondition(log, v1.ConditionTypeConfigurationInSync, metav1.ConditionFalse, v1.ConditionReasonConfigurationChanged, message)
	} else {
		ctx.SetCondition(log, v1.ConditionTypeConfigurationInSync, metav1.ConditionTrue, v1.ConditionReasonUpToDate, "")
	}

	exists := len(ctx.ReplicaState)
	expected := int(ctx.Cluster.Replicas())

	if len(notUpdatedReplicas) == 0 && exists == expected {
		ctx.Cluster.Status.CurrentRevision = ctx.Cluster.Status.UpdateRevision
	}

	var status metav1.ConditionStatus
	var reason v1.ConditionReason
	var message string
	eventType := corev1.EventTypeWarning
	eventReason := v1.EventReasonClusterNotReady

	switch exists {
	case 0:
		status = metav1.ConditionFalse
		reason = v1.KeeperConditionReasonNoLeader
		message = "No replicas"
	case 1:
		if len(replicasByMode[ModeStandalone]) == 1 {
			status = metav1.ConditionTrue
			reason = v1.KeeperConditionReasonStandaloneReady
			eventType = corev1.EventTypeNormal
			eventReason = v1.EventReasonClusterReady
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
			message = fmt.Sprintf("Not enough followers in cluster: %d/%d", len(replicasByMode[ModeFollower]), requiredFollowersForQuorum)
		default:
			status = metav1.ConditionTrue
			reason = v1.KeeperConditionReasonClusterReady
			eventType = corev1.EventTypeNormal
			eventReason = v1.EventReasonClusterReady
			message = "Cluster is ready"
		}
	}

	if _, err := ctx.UpsertConditionAndSendEvent(r, log,
		ctx.NewCondition(v1.ConditionTypeReady, status, reason, message),
		eventType, eventReason, message,
	); err != nil {
		return nil, fmt.Errorf("update ready condition: %w", err)
	}

	return nil, nil
}

func (r *ClusterReconciler) updateReplica(log util.Logger, ctx *reconcileContext, replicaID v1.KeeperReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", replicaID)
	log.Info("updating replica")

	configMap, err := TemplateConfigMap(ctx.Cluster, ctx.ExtraConfig, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q ConfigMap: %w", replicaID, err)
	}

	configChanged, err := chctrl.ReconcileConfigMap(ctx.Context, log, r, ctx.Cluster, configMap)
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
		if err := chctrl.Create(ctx.Context, r, ctx.Cluster, statefulSet); err != nil {
			return nil, err
		}

		return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
	}

	// Check if the StatefulSet is outdated and needs to be recreated
	v, err := semver.Parse(replica.StatefulSet.Annotations[util.AnnotationStatefulSetVersion])
	if err != nil || BreakingStatefulSetVersion.GT(v) {
		log.Warn(fmt.Sprintf("Removing the StatefulSet because of a breaking change. Found version: %v, expected version: %v", v, BreakingStatefulSetVersion))
		if err := chctrl.Delete(ctx.Context, r, ctx.Cluster, replica.StatefulSet); err != nil {
			return nil, err
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
	if err := chctrl.Update(ctx.Context, r, ctx.Cluster, replica.StatefulSet); err != nil {
		return nil, err
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

func (r *ClusterReconciler) checkHorizontalScalingAllowed(log util.Logger, ctx *reconcileContext) error {
	var leader v1.KeeperReplicaID = -1
	activeReplicas := len(ctx.ReplicaState)
	leaderMode := ModeLeader
	if activeReplicas == 1 {
		leaderMode = ModeStandalone
	}

	// Allow any scaling from 0 replicas
	if activeReplicas == 0 {
		ctx.HorizontalScaleAllowed = true
		ctx.SetCondition(log, v1.KeeperConditionTypeScaleAllowed, metav1.ConditionTrue,
			v1.KeeperConditionReasonReadyToScale, "")
		return nil
	}

	scaleBlocked := func(conditionReason v1.ConditionReason, format string, formatArgs ...any) error {
		ctx.HorizontalScaleAllowed = false
		_, err := ctx.UpsertConditionAndSendEvent(r, log,
			ctx.NewCondition(
				v1.KeeperConditionTypeScaleAllowed, metav1.ConditionFalse, conditionReason,
				fmt.Sprintf(format, formatArgs...),
			),
			corev1.EventTypeWarning, v1.EventReasonHorizontalScaleBlocked, format, formatArgs...,
		)
		if err != nil {
			return fmt.Errorf("update cluster scale blocked: %w", err)
		}
		return nil
	}

	updatedReplicas := 0
	readyReplicas := 0
	for id, replica := range ctx.ReplicaState {
		if !replica.HasConfigMapDiff(ctx) && !replica.HasStatefulSetDiff(ctx) {
			updatedReplicas++
		}
		if replica.Ready(ctx) {
			readyReplicas++
		}
		if replica.Status.ServerState == leaderMode {
			if leader != -1 {
				return scaleBlocked(v1.KeeperConditionReasonNoQuorum,
					"Multiple leaders in the cluster: %q, %q", leader, id)
			}

			leader = id
			// Wait for deleted replicas to leave the quorum.
			if replica.Status.Followers > activeReplicas-1 {
				return scaleBlocked(v1.KeeperConditionReasonWaitingFollowers,
					"Leader has more followers than expected: %d/%d. "+
						"Obsolete replica has not left the quorum yet", replica.Status.Followers, activeReplicas-1,
				)
			} else if replica.Status.Followers < activeReplicas-1 {
				return scaleBlocked(v1.KeeperConditionReasonWaitingFollowers,
					"Leader has less followers than expected: %d/%d. "+
						"Some replicas unavailable or not joined the quorum yet",
					replica.Status.Followers, activeReplicas-1,
				)
			}
		}
	}
	if leader == -1 {
		return scaleBlocked(v1.KeeperConditionReasonNoQuorum, "No leader in the cluster")
	}
	if updatedReplicas != activeReplicas {
		return scaleBlocked(v1.KeeperConditionReasonReplicaHasPendingChanges,
			"Waiting for %d/%d to be updated", updatedReplicas, activeReplicas)
	}
	if readyReplicas != activeReplicas {
		return scaleBlocked(v1.KeeperConditionReasonReplicaNotReady,
			"Waiting for %d/%d to be Ready", readyReplicas, activeReplicas)
	}

	ctx.HorizontalScaleAllowed = true
	ctx.SetCondition(log, v1.KeeperConditionTypeScaleAllowed, metav1.ConditionTrue,
		v1.KeeperConditionReasonReadyToScale, "")
	return nil
}
