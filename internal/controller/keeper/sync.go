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
	gcmp "github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

type replicaState struct {
	Error       bool `json:"error"`
	Status      serverStatus
	StatefulSet *appsv1.StatefulSet
}

func (r replicaState) Updated() bool {
	if r.StatefulSet == nil {
		return false
	}

	return r.StatefulSet.Generation == r.StatefulSet.Status.ObservedGeneration &&
		r.StatefulSet.Status.UpdateRevision == r.StatefulSet.Status.CurrentRevision
}

func (r replicaState) Ready(ctx *keeperReconciler) bool {
	if r.StatefulSet == nil {
		return false
	}

	stsReady := r.StatefulSet.Status.ReadyReplicas == 1 // Not reliable, but allows to wait until pod is `green`
	if len(ctx.ReplicaState) == 1 {
		return stsReady && r.Status.ServerState == ModeStandalone
	}

	return stsReady && slices.Contains(clusterModes, r.Status.ServerState)
}

func (r replicaState) HasStatefulSetDiff(rec *keeperReconciler) bool {
	if r.StatefulSet == nil {
		return true
	}

	return ctrlutil.GetSpecHashFromObject(r.StatefulSet) != rec.Cluster.Status.StatefulSetRevision
}

func (r replicaState) HasConfigMapDiff(rec *keeperReconciler) bool {
	if r.StatefulSet == nil {
		return true
	}

	return ctrlutil.GetConfigHashFromObject(r.StatefulSet) != rec.Cluster.Status.ConfigurationRevision
}

func (r replicaState) UpdateStage(rec *keeperReconciler) chctrl.ReplicaUpdateStage {
	if r.StatefulSet == nil {
		return chctrl.StageNotExists
	}

	if r.Error {
		return chctrl.StageError
	}

	if !r.Updated() {
		return chctrl.StageUpdating
	}

	if r.HasConfigMapDiff(rec) || r.HasStatefulSetDiff(rec) {
		return chctrl.StageHasDiff
	}

	if !r.Ready(rec) {
		return chctrl.StageNotReadyUpToDate
	}

	return chctrl.StageUpToDate
}

type reconcilerBase = chctrl.ResourceReconcilerBase[v1.KeeperClusterStatus, *v1.KeeperCluster, v1.KeeperReplicaID, replicaState]

type keeperReconciler struct {
	reconcilerBase

	// Should be populated after reconcileClusterRevisions with parsed extra config.
	ExtraConfig map[string]any
	// Computed by reconcileActiveReplicaStatus
	HorizontalScaleAllowed bool
}
type reconcileFunc func(context.Context, ctrlutil.Logger) (*ctrl.Result, error)

func (r *keeperReconciler) sync(ctx context.Context, log ctrlutil.Logger) (ctrl.Result, error) {
	log.Info("Enter Keeper Reconcile", "spec", r.Cluster.Spec, "status", r.Cluster.Status)

	reconcileSteps := []reconcileFunc{
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
		funcName := strings.TrimPrefix(ctrlutil.GetFunctionName(fn), "reconcile")
		stepLog := log.With("reconcile_step", funcName)
		stepLog.Debug("starting reconcile step")

		stepResult, err := fn(ctx, stepLog)
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
			r.SetConditions(log, []metav1.Condition{
				r.NewCondition(v1.ConditionTypeReconcileSucceeded, metav1.ConditionFalse, v1.ConditionReasonStepFailed, errMsg),
				// Operator did not finish reconciliation, some conditions may not be valid already.
				r.NewCondition(v1.ConditionTypeReady, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				r.NewCondition(v1.ConditionTypeHealthy, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				r.NewCondition(v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				r.NewCondition(v1.ConditionTypeConfigurationInSync, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				r.NewCondition(v1.ConditionTypeClusterSizeAligned, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
				r.NewCondition(v1.KeeperConditionTypeScaleAllowed, metav1.ConditionUnknown, v1.ConditionReasonStepFailed, errMsg),
			})

			if updateErr := r.UpsertStatus(ctx, log); updateErr != nil {
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

	r.SetCondition(log, r.NewCondition(v1.ConditionTypeReconcileSucceeded, metav1.ConditionTrue, v1.ConditionReasonReconcileFinished, "Reconcile succeeded"))
	log.Info("reconciliation loop end", "result", result)

	if err := r.UpsertStatus(ctx, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after reconciliation: %w", err)
	}

	return result, nil
}

func (r *keeperReconciler) reconcileClusterRevisions(_ context.Context, log ctrlutil.Logger) (*ctrl.Result, error) {
	if r.Cluster.Status.ObservedGeneration != r.Cluster.Generation {
		r.Cluster.Status.ObservedGeneration = r.Cluster.Generation
		log.Debug(fmt.Sprintf("observed new CR generation %d", r.Cluster.Generation))
	}

	updateRevision, err := ctrlutil.DeepHashObject(r.Cluster.Spec)
	if err != nil {
		return nil, fmt.Errorf("get current spec revision: %w", err)
	}

	if updateRevision != r.Cluster.Status.UpdateRevision {
		r.Cluster.Status.UpdateRevision = updateRevision
		log.Debug(fmt.Sprintf("observed new CR revision %q", updateRevision))
	}

	var extraConfig map[string]any
	if len(r.Cluster.Spec.Settings.ExtraConfig.Raw) > 0 {
		if err := json.Unmarshal(r.Cluster.Spec.Settings.ExtraConfig.Raw, &extraConfig); err != nil {
			return nil, fmt.Errorf("unmarshal extra config: %w", err)
		}

		r.ExtraConfig = extraConfig
	}

	configRevision, err := getConfigurationRevision(r.Cluster, r.ExtraConfig)
	if err != nil {
		return nil, fmt.Errorf("get configuration revision: %w", err)
	}

	if configRevision != r.Cluster.Status.ConfigurationRevision {
		r.Cluster.Status.ConfigurationRevision = configRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", configRevision))
	}

	stsRevision, err := getStatefulSetRevision(r.Cluster)
	if err != nil {
		return nil, fmt.Errorf("get StatefulSet revision: %w", err)
	}

	if stsRevision != r.Cluster.Status.StatefulSetRevision {
		r.Cluster.Status.StatefulSetRevision = stsRevision
		log.Debug(fmt.Sprintf("observed new StatefulSet revision %q", stsRevision))
	}

	return nil, nil
}

func (r *keeperReconciler) reconcileActiveReplicaStatus(ctx context.Context, log ctrlutil.Logger) (*ctrl.Result, error) {
	if r.Cluster.Replicas() == 0 {
		log.Debug("keeper replicaState count is zero")
		return nil, nil
	}

	listOpts := ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())

	var statefulSets appsv1.StatefulSetList
	if err := r.GetClient().List(ctx, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	tlsRequired := r.Cluster.Spec.Settings.TLS.Required

	execResults := ctrlutil.ExecuteParallel(statefulSets.Items, func(sts appsv1.StatefulSet) (v1.KeeperReplicaID, replicaState, error) {
		id, err := v1.KeeperReplicaIDFromLabels(sts.Labels)
		if err != nil {
			log.Error(err, "get replica ID from StatefulSet labels", "stateful_set", sts.Name)
			return -1, replicaState{}, fmt.Errorf("get StatefulSet replica ID: %w", err)
		}

		hasError, err := chctrl.CheckPodError(ctx, log, r.GetClient(), &sts)
		if err != nil {
			log.Warn("failed to check replica pod error", "stateful_set", sts.Name, "error", err)

			hasError = true
		}

		status := getServerStatus(ctx, log.With("replica_id", id), r.Cluster.HostnameByID(id), tlsRequired)

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

		if exists := r.SetReplica(id, res.Result); exists {
			log.Debug(fmt.Sprintf("multiple StatefulSets for single replica %v", id),
				"replica_id", id, "statefulset", res.Result.StatefulSet)
		}
	}

	// If replica existed before we need to mark it active as quorum expects it.
	if len(r.ReplicaState) > 0 && len(r.ReplicaState) < int(r.Cluster.Replicas()) {
		quorumReplicas, err := r.loadQuorumReplicas(ctx)
		if err != nil {
			return nil, fmt.Errorf("load quorum replicas: %w", err)
		}

		for id := range quorumReplicas {
			if _, exists := r.ReplicaState[id]; !exists {
				log.Info("adding missing replica from quorum config", "replica_id", id)
				r.SetReplica(id, replicaState{
					Error:       false,
					StatefulSet: nil,
					Status:      serverStatus{},
				})
			}
		}
	}

	if err := r.checkHorizontalScalingAllowed(ctx, log); err != nil {
		return nil, err
	}

	if !r.HorizontalScaleAllowed {
		return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
	}

	return nil, nil
}

func (r *keeperReconciler) reconcileQuorumMembership(ctx context.Context, log ctrlutil.Logger) (*ctrl.Result, error) {
	requestedReplicas := int(r.Cluster.Replicas())
	activeReplicas := len(r.ReplicaState)

	if activeReplicas == requestedReplicas {
		if _, err := r.UpsertConditionAndSendEvent(ctx, log,
			r.NewCondition(
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
		r.GetRecorder().Eventf(r.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaCreated,
			"Initial cluster creation, creating %d replicas", requestedReplicas)
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ConditionReasonUpToDate, ""))

		for id := range v1.KeeperReplicaID(requestedReplicas) { //nolint:gosec
			log.Info("creating new replica", "replica_id", id)
			r.SetReplica(id, replicaState{})
		}

		return nil, nil
	}

	// Scale to zero replica count could be applied without checks.
	if requestedReplicas == 0 && activeReplicas > 0 {
		log.Debug("deleting all replicas", "replicas", slices.Collect(maps.Keys(r.ReplicaState)))
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ConditionReasonUpToDate, ""))
		r.GetRecorder().Eventf(r.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaDeleted,
			"Cluster scaled to 0 nodes, removing all %d replicas", len(r.ReplicaState))
		r.ReplicaState = map[v1.KeeperReplicaID]replicaState{}

		return nil, nil
	}

	var err error
	if activeReplicas < requestedReplicas {
		_, err = r.UpsertConditionAndSendEvent(ctx, log,
			r.NewCondition(
				v1.ConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ConditionReasonScalingUp,
				"Cluster has less replicas than requested",
			), corev1.EventTypeNormal, v1.EventReasonHorizontalScaleStarted,
			"Cluster scale up is started: current replicas %d, requested %d",
			activeReplicas, requestedReplicas,
		)
	} else {
		_, err = r.UpsertConditionAndSendEvent(ctx, log,
			r.NewCondition(
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
	if !r.HorizontalScaleAllowed {
		log.Info("Delaying horizontal scaling, cluster is not in stable state")
		return &reconcile.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
	}

	// Add single replica in quorum, allocating the first free id.
	if activeReplicas < requestedReplicas {
		for id := v1.KeeperReplicaID(1); ; id++ {
			if _, ok := r.ReplicaState[id]; !ok {
				log.Info("creating new replica", "replica_id", id)
				r.GetRecorder().Eventf(r.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaCreated,
					"Adding new replica %q to the cluster", r.Cluster.HostnameByID(id))
				r.SetReplica(id, replicaState{})

				return nil, nil
			}
		}
	}

	// Remove single replica from the quorum. Prefer bigger id.
	if activeReplicas > requestedReplicas {
		chosenIndex := v1.KeeperReplicaID(-1)
		for id := range r.ReplicaState {
			if chosenIndex == -1 || chosenIndex < id {
				chosenIndex = id
			}
		}

		if chosenIndex == -1 {
			log.Warn(fmt.Sprintf("no replica in cluster, but requested scale down: %d < %d", requestedReplicas, activeReplicas))
		}

		log.Info("deleting replica", "replica_id", chosenIndex)
		r.GetRecorder().Eventf(r.Cluster, corev1.EventTypeNormal, v1.EventReasonReplicaDeleted,
			"Deleting replica %q from the cluster", r.Cluster.HostnameByID(chosenIndex))
		delete(r.ReplicaState, chosenIndex)

		return nil, nil
	}

	return nil, nil
}

func (r *keeperReconciler) reconcileCommonResources(ctx context.Context, log ctrlutil.Logger) (*ctrl.Result, error) {
	service := templateHeadlessService(r.Cluster)
	if _, err := r.ReconcileService(ctx, log, service); err != nil {
		return nil, fmt.Errorf("reconcile service resource: %w", err)
	}

	pdb := templatePodDisruptionBudget(r.Cluster)
	if _, err := r.ReconcilePodDisruptionBudget(ctx, log, pdb); err != nil {
		return nil, fmt.Errorf("reconcile PodDisruptionBudget resource: %w", err)
	}

	configMap, err := templateQuorumConfig(r)
	if err != nil {
		return nil, fmt.Errorf("template quorum config: %w", err)
	}

	if _, err = r.ReconcileConfigMap(ctx, log, configMap); err != nil {
		return nil, fmt.Errorf("reconcile quorum config: %w", err)
	}

	return nil, nil
}

// reconcileReplicaResources performs update on replicas ConfigMap and StatefulSet.
// If there are replicas that has no created StatefulSet, creates immediately.
// If all replicas exists performs rolling upgrade, with the following order preferences:
// NotExists -> CrashLoop/ImagePullErr -> HasDiff -> Any.
func (r *keeperReconciler) reconcileReplicaResources(ctx context.Context, log ctrlutil.Logger) (*ctrl.Result, error) {
	highestStage := chctrl.StageUpToDate

	var replicasInStatus []v1.KeeperReplicaID

	for id, state := range r.ReplicaState {
		stage := state.UpdateStage(r)
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

		result = ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}
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
		replicaResult, err := r.updateReplica(ctx, log, id)
		if err != nil {
			return nil, fmt.Errorf("update replica %q: %w", id, err)
		}

		ctrlutil.UpdateResult(&result, replicaResult)
	}

	return &result, nil
}

func (r *keeperReconciler) reconcileCleanUp(ctx context.Context, log ctrlutil.Logger) (*ctrl.Result, error) {
	listOpts := ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())

	var configMaps corev1.ConfigMapList
	if err := r.GetClient().List(ctx, &configMaps, listOpts); err != nil {
		return nil, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		if configMap.Name == r.Cluster.QuorumConfigMapName() {
			continue
		}

		id, err := v1.KeeperReplicaIDFromLabels(configMap.Labels)
		if err != nil {
			log.Warn("parse ConfigMap replica ID", "configmap", configMap.Name, "err", err)
			continue
		}

		if _, ok := r.ReplicaState[id]; !ok {
			log.Info("deleting stale ConfigMap", "replica_id", id, "configmap", configMap.Name)

			if err := r.Delete(ctx, &configMap); err != nil {
				log.Error(err, "delete stale replica", "replica_id", id, "configmap", configMap.Name)
			}
		}
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.GetClient().List(ctx, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		id, err := v1.KeeperReplicaIDFromLabels(sts.Labels)
		if err != nil {
			log.Warn("parse StatefulSet replica ID", "sts", sts.Name, "err", err)
			continue
		}

		if _, ok := r.ReplicaState[id]; !ok {
			log.Info("deleting stale StatefulSet", "replica_id", id, "statefuleset", sts.Name)

			if err := r.Delete(ctx, &sts); err != nil {
				log.Error(err, "delete stale replica", "replica_id", id, "statefuleset", sts.Name)
			}
		}
	}

	return nil, nil
}

func (r *keeperReconciler) reconcileConditions(ctx context.Context, log ctrlutil.Logger) (*ctrl.Result, error) {
	var (
		errorReplicas      []v1.KeeperReplicaID
		notReadyReplicas   []v1.KeeperReplicaID
		notUpdatedReplicas []v1.KeeperReplicaID
	)

	replicasByMode := map[string][]v1.KeeperReplicaID{}

	r.Cluster.Status.ReadyReplicas = 0
	for id, replica := range r.ReplicaState {
		if replica.Error {
			errorReplicas = append(errorReplicas, id)
		}

		if !replica.Ready(r) {
			notReadyReplicas = append(notReadyReplicas, id)
		} else {
			r.Cluster.Status.ReadyReplicas++
			replicasByMode[replica.Status.ServerState] = append(replicasByMode[replica.Status.ServerState], id)
		}

		if replica.HasConfigMapDiff(r) || replica.HasStatefulSetDiff(r) || !replica.Updated() {
			notUpdatedReplicas = append(notUpdatedReplicas, id)
		}
	}

	if len(errorReplicas) > 0 {
		slices.Sort(errorReplicas)
		message := fmt.Sprintf("Replicas has startup errors: %v", errorReplicas)
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionFalse, v1.ConditionReasonReplicaError, message))
	} else {
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeReplicaStartupSucceeded, metav1.ConditionTrue, v1.ConditionReasonReplicasRunning, ""))
	}

	if len(notReadyReplicas) > 0 {
		slices.Sort(notReadyReplicas)
		message := fmt.Sprintf("Not ready replicas: %v", notReadyReplicas)
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeHealthy, metav1.ConditionFalse, v1.ConditionReasonReplicasNotReady, message))
	} else {
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeHealthy, metav1.ConditionTrue, v1.ConditionReasonReplicasReady, ""))
	}

	if len(notUpdatedReplicas) > 0 {
		slices.Sort(notUpdatedReplicas)
		message := fmt.Sprintf("Replicas has pending updates: %v", notUpdatedReplicas)
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeConfigurationInSync, metav1.ConditionFalse, v1.ConditionReasonConfigurationChanged, message))
	} else {
		r.SetCondition(log, r.NewCondition(v1.ConditionTypeConfigurationInSync, metav1.ConditionTrue, v1.ConditionReasonUpToDate, ""))
	}

	exists := len(r.ReplicaState)
	expected := int(r.Cluster.Replicas())

	if len(notUpdatedReplicas) == 0 && exists == expected {
		r.Cluster.Status.CurrentRevision = r.Cluster.Status.UpdateRevision
	}

	var (
		status  metav1.ConditionStatus
		reason  v1.ConditionReason
		message string
	)

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

	if _, err := r.UpsertConditionAndSendEvent(ctx, log,
		r.NewCondition(v1.ConditionTypeReady, status, reason, message),
		eventType, eventReason, message,
	); err != nil {
		return nil, fmt.Errorf("update ready condition: %w", err)
	}

	return nil, nil
}

func (r *keeperReconciler) updateReplica(ctx context.Context, log ctrlutil.Logger, replicaID v1.KeeperReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", replicaID)
	log.Info("updating replica")

	configMap, err := templateConfigMap(r.Cluster, r.ExtraConfig, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q ConfigMap: %w", replicaID, err)
	}

	configChanged, err := r.ReconcileConfigMap(ctx, log, configMap)
	if err != nil {
		return nil, fmt.Errorf("update replica %q ConfigMap: %w", replicaID, err)
	}

	statefulSet, err := templateStatefulSet(r.Cluster, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q StatefulSet: %w", replicaID, err)
	}

	if err := ctrl.SetControllerReference(r.Cluster, statefulSet, r.GetScheme()); err != nil {
		return nil, fmt.Errorf("set replica %q StatefulSet controller reference: %w", replicaID, err)
	}

	replica := r.Replica(replicaID)
	if replica.StatefulSet == nil {
		log.Info("replica StatefulSet not found, creating", "stateful_set", statefulSet.Name)
		ctrlutil.AddObjectConfigHash(statefulSet, r.Cluster.Status.ConfigurationRevision)
		ctrlutil.AddHashWithKeyToAnnotations(statefulSet, ctrlutil.AnnotationSpecHash, r.Cluster.Status.StatefulSetRevision)

		if err := r.Create(ctx, statefulSet); err != nil {
			return nil, fmt.Errorf("create replica %q: %w", replicaID, err)
		}

		return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
	}

	// Check if the StatefulSet is outdated and needs to be recreated
	v, err := semver.Parse(replica.StatefulSet.Annotations[ctrlutil.AnnotationStatefulSetVersion])
	if err != nil || breakingStatefulSetVersion.GT(v) {
		log.Warn(fmt.Sprintf("Removing the StatefulSet because of a breaking change. Found version: %v, expected version: %v", v, breakingStatefulSetVersion))

		if err := r.Delete(ctx, replica.StatefulSet); err != nil {
			return nil, fmt.Errorf("recreate replica %d: %w", replicaID, err)
		}

		return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
	}

	stsNeedsUpdate := replica.HasStatefulSetDiff(r)

	// Trigger Pod restart if config changed
	if replica.HasConfigMapDiff(r) {
		// Use same way as Kubernetes for force restarting Pods one by one
		// (https://github.com/kubernetes/kubernetes/blob/22a21f974f5c0798a611987405135ab7e62502da/staging/src/k8s.io/kubectl/pkg/polymorphichelpers/objectrestarter.go#L41)
		// Not included by default in the StatefulSet so that hash-diffs work correctly
		log.Info("forcing keeper Pod restart, because of config changes")

		statefulSet.Spec.Template.Annotations[ctrlutil.AnnotationRestartedAt] = time.Now().Format(time.RFC3339)

		ctrlutil.AddObjectConfigHash(replica.StatefulSet, r.Cluster.Status.ConfigurationRevision)

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

	if !gcmp.Equal(replica.StatefulSet.Spec.VolumeClaimTemplates[0].Spec, r.Cluster.Spec.DataVolumeClaimSpec) {
		if err = r.UpdatePVC(ctx, log, replicaID, r.Cluster.Spec.DataVolumeClaimSpec); err != nil {
			return nil, fmt.Errorf("update replica %d PVC: %w", replicaID, err)
		}

		statefulSet.Spec.VolumeClaimTemplates = replica.StatefulSet.Spec.VolumeClaimTemplates
	}

	log.Info("updating replica StatefulSet", "stateful_set", statefulSet.Name)
	replica.StatefulSet.Spec = statefulSet.Spec
	replica.StatefulSet.Annotations = ctrlutil.MergeMaps(replica.StatefulSet.Annotations, statefulSet.Annotations)
	replica.StatefulSet.Labels = ctrlutil.MergeMaps(replica.StatefulSet.Labels, statefulSet.Labels)
	ctrlutil.AddHashWithKeyToAnnotations(replica.StatefulSet, ctrlutil.AnnotationSpecHash, r.Cluster.Status.StatefulSetRevision)

	if err := r.Update(ctx, replica.StatefulSet); err != nil {
		return nil, fmt.Errorf("update replica %q: %w", replicaID, err)
	}

	return &ctrl.Result{RequeueAfter: chctrl.RequeueOnRefreshTimeout}, nil
}

func (r *keeperReconciler) loadQuorumReplicas(ctx context.Context) (map[v1.KeeperReplicaID]struct{}, error) {
	configMap := corev1.ConfigMap{}

	err := r.GetClient().Get(
		ctx,
		types.NamespacedName{Namespace: r.Cluster.Namespace, Name: r.Cluster.QuorumConfigMapName()},
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
				Server quorumConfig `yaml:"server"`
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

func (r *keeperReconciler) checkHorizontalScalingAllowed(ctx context.Context, log ctrlutil.Logger) error {
	var leader v1.KeeperReplicaID = -1

	activeReplicas := len(r.ReplicaState)

	leaderMode := ModeLeader
	if activeReplicas == 1 {
		leaderMode = ModeStandalone
	}

	// Allow any scaling from 0 replicas
	if activeReplicas == 0 {
		r.HorizontalScaleAllowed = true
		r.SetCondition(log, r.NewCondition(v1.KeeperConditionTypeScaleAllowed, metav1.ConditionTrue,
			v1.KeeperConditionReasonReadyToScale, ""))
		return nil
	}

	scaleBlocked := func(conditionReason v1.ConditionReason, format string, formatArgs ...any) error {
		r.HorizontalScaleAllowed = false

		_, err := r.UpsertConditionAndSendEvent(ctx, log,
			r.NewCondition(
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
	for id, replica := range r.ReplicaState {
		if !replica.HasConfigMapDiff(r) && !replica.HasStatefulSetDiff(r) {
			updatedReplicas++
		}

		if replica.Ready(r) {
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

	r.HorizontalScaleAllowed = true
	r.SetCondition(log, r.NewCondition(v1.KeeperConditionTypeScaleAllowed, metav1.ConditionTrue,
		v1.KeeperConditionReasonReadyToScale, ""))

	return nil
}
