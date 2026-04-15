package keeper

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"time"

	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

type replicaState struct {
	Error       bool `json:"error"`
	Status      serverStatus
	StatefulSet *appsv1.StatefulSet
	PVC         *corev1.PersistentVolumeClaim
}

func (r replicaState) Updated() bool {
	if r.StatefulSet == nil {
		return false
	}

	return r.StatefulSet.Generation == r.StatefulSet.Status.ObservedGeneration &&
		r.StatefulSet.Status.UpdateRevision == r.StatefulSet.Status.CurrentRevision
}

func (r replicaState) Ready(replicaCount int) bool {
	if r.StatefulSet == nil {
		return false
	}

	stsReady := r.StatefulSet.Status.ReadyReplicas == 1 // Not reliable, but allows to wait until pod is `green`
	if replicaCount == 1 {
		return stsReady && r.Status.ServerState == ModeStandalone
	}

	return stsReady && slices.Contains(clusterModes, r.Status.ServerState)
}

func (r replicaState) HasDiff(rev chctrl.RevisionState) bool {
	return rev.ReplicaHasDiff(r.StatefulSet, r.PVC)
}

func (r replicaState) UpdateStage(rev chctrl.RevisionState, replicaCount int) chctrl.ReplicaUpdateStage {
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

	if !r.Ready(replicaCount) {
		return chctrl.StageNotReadyUpToDate
	}

	return chctrl.StageUpToDate
}

type statusManager = chctrl.StatusManager[v1.KeeperClusterStatus, *v1.KeeperClusterStatus, *v1.KeeperCluster]

type keeperReconciler struct {
	chctrl.Controller
	statusManager
	chctrl.ResourceManager

	Dialer    ctrlutil.DialContextFunc
	Checker   *upgrade.Checker
	EnablePDB bool

	Cluster      *v1.KeeperCluster
	ReplicaState map[v1.KeeperReplicaID]replicaState

	versionProbe chctrl.VersionProbeResult
	revs         chctrl.RevisionState
	// Computed by reconcileActiveReplicaStatus
	HorizontalScaleAllowed bool
}

func (r *keeperReconciler) sync(ctx context.Context, log ctrlutil.Logger) (ctrl.Result, error) {
	log.Info("Enter Keeper Reconcile", "spec", r.Cluster.Spec, "status", r.Cluster.Status)

	r.SetUnknownConditions(v1.ConditionReasonStepFailed, "Reconcile stopped before condition evaluation",
		[]v1.ConditionType{
			v1.ConditionTypeReplicaStartupSucceeded,
			v1.ConditionTypeHealthy,
			v1.ConditionTypeClusterSizeAligned,
			v1.ConditionTypeConfigurationInSync,
			v1.ConditionTypeVersionInSync,
			v1.ConditionTypeVersionUpgraded,
			v1.ConditionTypeReady,
			v1.KeeperConditionTypeScaleAllowed,
		})

	steps := []chctrl.ReconcileStep{
		{Name: "ClusterRevisions", Fn: r.reconcileClusterRevisions, Always: true},
		{Name: "ActiveReplicaStatus", Fn: r.reconcileActiveReplicaStatus, Always: true},
		{Name: "QuorumMembership", Fn: r.reconcileQuorumMembership},
		{Name: "CommonResources", Fn: r.reconcileCommonResources},
	}
	if r.EnablePDB {
		steps = append(steps, chctrl.ReconcileStep{Name: "PodDisruptionBudget", Fn: r.reconcilePodDisruptionBudget})
	}

	steps = append(steps,
		chctrl.ReconcileStep{Name: "ReplicaResources", Fn: r.reconcileReplicaResources},
		chctrl.ReconcileStep{Name: "CleanUp", Fn: r.reconcileCleanUp},
	)

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

		r.SetCondition(metav1.Condition{Type: v1.ConditionTypeReconcileSucceeded, Status: metav1.ConditionFalse, Reason: v1.ConditionReasonStepFailed, Message: "Reconcile returned error"})

		if updateErr := r.UpsertStatus(ctx, log); updateErr != nil {
			log.Error(updateErr, "failed to update status")
		}

		return ctrl.Result{}, fmt.Errorf("reconcile steps: %w", err)
	}

	r.SetCondition(metav1.Condition{Type: v1.ConditionTypeReconcileSucceeded, Status: metav1.ConditionTrue, Reason: v1.ConditionReasonReconcileFinished, Message: "Reconcile succeeded"})
	log.Info("reconciliation loop end", "result", result)

	if err := r.UpsertStatus(ctx, log); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status after reconciliation: %w", err)
	}

	return result, nil
}

func (r *keeperReconciler) reconcileClusterRevisions(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
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

	r.revs.ConfigurationRevision, err = getConfigurationRevision(r.Cluster)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("get configuration revision: %w", err)
	}

	if r.revs.ConfigurationRevision != r.Cluster.Status.ConfigurationRevision {
		r.Cluster.Status.ConfigurationRevision = r.revs.ConfigurationRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", r.revs.ConfigurationRevision))
	}

	r.revs.StatefulSetRevision, err = getStatefulSetRevision(r.Cluster)
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

	probeResult, err := r.VersionProbe(ctx, log, chctrl.VersionProbeConfig{
		Binary:            "clickhouse-keeper",
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
	r.Cluster.Status.Version = r.versionProbe.Version

	if r.Checker != nil {
		cond, event := chctrl.GetUpgradeCondition(*r.Checker, r.versionProbe, r.Cluster.Spec.UpgradeChannel)
		r.SetCondition(cond, event...)
	} else {
		meta.RemoveStatusCondition(r.Cluster.GetStatus().GetConditions(), v1.ConditionTypeVersionUpgraded)
	}

	return chctrl.StepContinue(), nil
}

func (r *keeperReconciler) reconcileActiveReplicaStatus(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if r.Cluster.Replicas() == 0 {
		log.Debug("keeper replicaState count is zero")
		return chctrl.StepContinue(), nil
	}

	listOpts := ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())

	var statefulSets appsv1.StatefulSetList
	if err := r.GetClient().List(ctx, &statefulSets, listOpts); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list StatefulSets: %w", err)
	}

	tlsRequired := r.Cluster.Spec.Settings.TLS.Required

	execResults := ctrlutil.ExecuteParallel(statefulSets.Items, func(sts appsv1.StatefulSet) (v1.KeeperReplicaID, replicaState, error) {
		id, err := v1.KeeperReplicaIDFromLabels(sts.Labels)
		if err != nil {
			log.Error(err, "get replica ID from StatefulSet labels", "statefulset", sts.Name)
			return -1, replicaState{}, fmt.Errorf("get StatefulSet replica ID: %w", err)
		}

		hasError, err := chctrl.CheckPodError(ctx, log, r.GetClient(), &sts)
		if err != nil {
			log.Warn("failed to check replica pod error", "statefulset", sts.Name, "error", err)

			hasError = true
		}

		var status serverStatus
		if !hasError && sts.Status.ReadyReplicas > 0 {
			ctx, cancel := context.WithTimeout(ctx, chctrl.LoadReplicaStateTimeout)
			defer cancel()

			status = getServerStatus(ctx, log.With("replica_id", id), r.Dialer, r.Cluster.HostnameByID(id), tlsRequired)
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
			Status:      status,
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
				"replica_id", id, "statefulset", res.Result.StatefulSet)
		}
	}

	// If replica existed before we need to mark it active as quorum expects it.
	if len(r.ReplicaState) > 0 && len(r.ReplicaState) < int(r.Cluster.Replicas()) {
		quorumReplicas, err := r.loadQuorumReplicas(ctx)
		if err != nil {
			return chctrl.StepResult{}, fmt.Errorf("load quorum replicas: %w", err)
		}

		for id := range quorumReplicas {
			if _, exists := r.ReplicaState[id]; !exists {
				log.Info("adding missing replica from quorum config", "replica_id", id)
				r.ReplicaState[id] = replicaState{
					Error:       false,
					StatefulSet: nil,
					Status:      serverStatus{},
				}
			}
		}
	}

	if err := r.checkHorizontalScalingAllowed(ctx, log); err != nil {
		return chctrl.StepResult{}, err
	}

	r.evaluateReplicaConditions()

	if !r.HorizontalScaleAllowed {
		return chctrl.StepRequeue(chctrl.RequeueOnRefreshTimeout), nil
	}

	return chctrl.StepContinue(), nil
}

func (r *keeperReconciler) reconcileQuorumMembership(_ context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	requestedReplicas := int(r.Cluster.Replicas())
	activeReplicas := len(r.ReplicaState)

	if activeReplicas == requestedReplicas {
		r.SetCondition(
			metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionTrue, Reason: v1.ConditionReasonUpToDate, Message: ""},
			chctrl.EventSpec{
				Type:    corev1.EventTypeNormal,
				Reason:  v1.EventReasonHorizontalScaleCompleted,
				Action:  v1.EventActionScaling,
				Message: fmt.Sprintf("Cluster is scaled to the requested size: %d replicas", requestedReplicas),
			},
		)

		return chctrl.StepContinue(), nil
	}

	// New cluster creation, creates all replicas.
	if requestedReplicas > 0 && activeReplicas == 0 {
		log.Debug("creating all replicas")
		r.GetRecorder().Eventf(r.Cluster, nil, corev1.EventTypeNormal, v1.EventReasonReplicaCreated, "InitialCreate",
			"Initial cluster creation, creating %d replicas", requestedReplicas)
		r.SetCondition(metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionTrue, Reason: v1.ConditionReasonUpToDate, Message: ""})

		for id := range v1.KeeperReplicaID(requestedReplicas) { //nolint:gosec
			log.Info("creating new replica", "replica_id", id)
			r.ReplicaState[id] = replicaState{}
		}

		return chctrl.StepContinue(), nil
	}

	// Scale to zero replica count could be applied without checks.
	if requestedReplicas == 0 && activeReplicas > 0 {
		log.Debug("deleting all replicas", "replicas", slices.Collect(maps.Keys(r.ReplicaState)))
		r.SetCondition(metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionTrue, Reason: v1.ConditionReasonUpToDate, Message: ""})
		r.GetRecorder().Eventf(r.Cluster, nil, corev1.EventTypeNormal, v1.EventReasonReplicaDeleted, v1.EventActionScaling,
			"Cluster scaled to 0 nodes, removing all %d replicas", len(r.ReplicaState))
		r.ReplicaState = map[v1.KeeperReplicaID]replicaState{}

		return chctrl.StepContinue(), nil
	}

	if activeReplicas < requestedReplicas {
		r.SetCondition(
			metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionFalse, Reason: v1.ConditionReasonScalingUp, Message: "Cluster has less replicas than requested"},
			chctrl.EventSpec{
				Type:    corev1.EventTypeNormal,
				Reason:  v1.EventReasonHorizontalScaleStarted,
				Action:  v1.EventActionScaling,
				Message: fmt.Sprintf("Cluster scale up is started: current replicas %d, requested %d", activeReplicas, requestedReplicas),
			},
		)
	} else {
		r.SetCondition(
			metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionFalse, Reason: v1.ConditionReasonScalingDown, Message: "Cluster has more replicas than requested"},
			chctrl.EventSpec{
				Type:    corev1.EventTypeNormal,
				Reason:  v1.EventReasonHorizontalScaleStarted,
				Action:  v1.EventActionScaling,
				Message: fmt.Sprintf("Cluster scale down is started: current replicas %d, requested %d", activeReplicas, requestedReplicas),
			},
		)
	}

	// For running cluster, allow quorum membership changes only in stable state.
	if !r.HorizontalScaleAllowed {
		log.Info("Delaying horizontal scaling, cluster is not in stable state")
		return chctrl.StepRequeue(chctrl.RequeueOnRefreshTimeout), nil
	}

	// Add single replica in quorum, allocating the first free id.
	if activeReplicas < requestedReplicas {
		for id := v1.KeeperReplicaID(1); ; id++ {
			if _, ok := r.ReplicaState[id]; !ok {
				log.Info("creating new replica", "replica_id", id)
				r.GetRecorder().Eventf(r.Cluster, nil, corev1.EventTypeNormal, v1.EventReasonReplicaCreated,
					v1.EventActionScaling, "Adding new replica %q to the cluster", r.Cluster.HostnameByID(id))
				r.ReplicaState[id] = replicaState{}

				return chctrl.StepContinue(), nil
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
		r.GetRecorder().Eventf(r.Cluster, nil, corev1.EventTypeNormal, v1.EventReasonReplicaDeleted, v1.EventActionScaling,
			"Deleting replica %q from the cluster", r.Cluster.HostnameByID(chosenIndex))
		delete(r.ReplicaState, chosenIndex)

		return chctrl.StepContinue(), nil
	}

	return chctrl.StepContinue(), nil
}

func (r *keeperReconciler) reconcilePodDisruptionBudget(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if !r.Cluster.Spec.PodDisruptionBudget.Ignored() {
		pdb := templatePodDisruptionBudget(r.Cluster)
		if r.Cluster.Spec.PodDisruptionBudget.Enabled() {
			if _, err := r.ReconcilePodDisruptionBudget(ctx, log, pdb, v1.EventActionReconciling); err != nil {
				return chctrl.StepResult{}, fmt.Errorf("reconcile PodDisruptionBudget resource: %w", err)
			}
		} else {
			if err := r.Delete(ctx, pdb, v1.EventActionReconciling); err != nil {
				return chctrl.StepResult{}, fmt.Errorf("delete disabled PodDisruptionBudget resource: %w", err)
			}
		}
	}

	return chctrl.StepContinue(), nil
}

func (r *keeperReconciler) reconcileCommonResources(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	service := templateHeadlessService(r.Cluster)
	if _, err := r.ReconcileService(ctx, log, service, v1.EventActionReconciling); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("reconcile service resource: %w", err)
	}

	configMap, err := templateQuorumConfig(r)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("template quorum config: %w", err)
	}

	if _, err = r.ReconcileConfigMap(ctx, log, configMap, v1.EventActionReconciling); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("reconcile quorum config: %w", err)
	}

	return chctrl.StepContinue(), nil
}

// reconcileReplicaResources performs update on replicas ConfigMap and StatefulSet.
// If there are replicas that has no created StatefulSet, creates immediately.
// If all replicas exists performs rolling upgrade, with the following order preferences:
// NotExists -> CrashLoop/ImagePullErr -> HasDiff -> Any.
func (r *keeperReconciler) reconcileReplicaResources(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	highestStage := chctrl.StageUpToDate

	var replicasInStatus []v1.KeeperReplicaID

	for id, state := range r.ReplicaState {
		stage := state.UpdateStage(r.revs, len(r.ReplicaState))
		if stage == highestStage {
			replicasInStatus = append(replicasInStatus, id)
			continue
		}

		if stage > highestStage {
			highestStage = stage
			replicasInStatus = []v1.KeeperReplicaID{id}
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
			return chctrl.StepResult{}, fmt.Errorf("update replica %q: %w", id, err)
		}

		if replicaResult != nil && replicaResult.RequeueAfter > 0 {
			if requeueAfter == 0 || replicaResult.RequeueAfter < requeueAfter {
				requeueAfter = replicaResult.RequeueAfter
			}
		}
	}

	return chctrl.StepRequeue(requeueAfter), nil
}

func (r *keeperReconciler) reconcileCleanUp(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	listOpts := ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())

	var configMaps corev1.ConfigMapList
	if err := r.GetClient().List(ctx, &configMaps, listOpts); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list ConfigMaps: %w", err)
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

			if err := r.Delete(ctx, &configMap, v1.EventActionReconciling); err != nil {
				log.Error(err, "delete stale replica", "replica_id", id, "configmap", configMap.Name)
			}
		}
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.GetClient().List(ctx, &statefulSets, listOpts); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		id, err := v1.KeeperReplicaIDFromLabels(sts.Labels)
		if err != nil {
			log.Warn("parse StatefulSet replica ID", "sts", sts.Name, "err", err)
			continue
		}

		if _, ok := r.ReplicaState[id]; !ok {
			log.Info("deleting stale StatefulSet", "replica_id", id, "statefulset", sts.Name)

			if err := r.Delete(ctx, &sts, v1.EventActionReconciling); err != nil {
				log.Error(err, "delete stale replica", "replica_id", id, "statefulset", sts.Name)
			}
		}
	}

	return chctrl.StepContinue(), nil
}

func (r *keeperReconciler) evaluateReplicaConditions() {
	var errorIDs, notReadyIDs, notUpdatedIDs []string

	replicasByMode := map[string][]v1.KeeperReplicaID{}
	replicaVersions := map[string]string{}

	r.Cluster.Status.ReadyReplicas = 0
	for id, replica := range r.ReplicaState {
		idStr := fmt.Sprintf("%d", id)

		if replica.Error {
			errorIDs = append(errorIDs, idStr)
		}

		if !replica.Ready(len(r.ReplicaState)) {
			notReadyIDs = append(notReadyIDs, idStr)
		} else {
			r.Cluster.Status.ReadyReplicas++
			replicasByMode[replica.Status.ServerState] = append(replicasByMode[replica.Status.ServerState], id)
		}

		if replica.HasDiff(r.revs) || !replica.Updated() {
			notUpdatedIDs = append(notUpdatedIDs, idStr)
		}

		replicaVersions[idStr] = replica.Status.Version
	}

	r.SetCondition(chctrl.ReplicaStartupCondition(errorIDs))
	r.SetCondition(chctrl.HealthyCondition(notReadyIDs))
	r.SetCondition(chctrl.ConfigSyncCondition(notUpdatedIDs))
	{
		cond, event := chctrl.GetVersionSyncCondition(r.versionProbe, replicaVersions, len(notUpdatedIDs) > 0)
		r.SetCondition(cond, event...)
	}

	// Ready condition — keeper-specific logic.
	exists := len(r.ReplicaState)

	if len(notUpdatedIDs) == 0 && exists == int(r.Cluster.Replicas()) {
		r.Cluster.Status.CurrentRevision = r.Cluster.Status.UpdateRevision
	}

	var (
		status  metav1.ConditionStatus
		reason  v1.ConditionReason
		message string
	)

	eventType := corev1.EventTypeWarning
	eventReason := v1.EventReasonClusterNotReady
	eventAction := v1.EventActionBecameNotReady

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
			eventAction = v1.EventActionBecameReady
			message = "Cluster is ready"
		}
	}

	r.SetCondition(
		metav1.Condition{Type: v1.ConditionTypeReady, Status: status, Reason: reason, Message: message},
		chctrl.EventSpec{Type: eventType, Reason: eventReason, Action: eventAction, Message: message},
	)
}

func (r *keeperReconciler) updateReplica(ctx context.Context, log ctrlutil.Logger, replicaID v1.KeeperReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", replicaID)
	log.Info("updating replica")

	configMap, err := templateConfigMap(r.Cluster, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q ConfigMap: %w", replicaID, err)
	}

	statefulSet, err := templateStatefulSet(r.Cluster, replicaID)
	if err != nil {
		return nil, fmt.Errorf("template replica %q StatefulSet: %w", replicaID, err)
	}

	replica := r.ReplicaState[replicaID]

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
		return nil, fmt.Errorf("reconcile replica %q resources: %w", replicaID, err)
	}

	return result, nil
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

func (r *keeperReconciler) checkHorizontalScalingAllowed(_ context.Context, _ ctrlutil.Logger) error {
	var leader v1.KeeperReplicaID = -1

	activeReplicas := len(r.ReplicaState)

	leaderMode := ModeLeader
	if activeReplicas == 1 {
		leaderMode = ModeStandalone
	}

	// Allow any scaling from 0 replicas
	if activeReplicas == 0 {
		r.HorizontalScaleAllowed = true
		r.SetCondition(metav1.Condition{
			Type:    v1.KeeperConditionTypeScaleAllowed,
			Status:  metav1.ConditionTrue,
			Reason:  v1.KeeperConditionReasonReadyToScale,
			Message: "",
		})

		return nil
	}

	scaleBlocked := func(conditionReason v1.ConditionReason, format string, formatArgs ...any) error {
		r.HorizontalScaleAllowed = false

		r.SetCondition(
			metav1.Condition{
				Type:    v1.KeeperConditionTypeScaleAllowed,
				Status:  metav1.ConditionFalse,
				Reason:  conditionReason,
				Message: fmt.Sprintf(format, formatArgs...),
			},
			chctrl.EventSpec{
				Type:    corev1.EventTypeWarning,
				Reason:  v1.EventReasonHorizontalScaleBlocked,
				Action:  v1.EventActionScaling,
				Message: fmt.Sprintf(format, formatArgs...),
			},
		)

		return nil
	}

	updatedReplicas := 0

	readyReplicas := 0
	for id, replica := range r.ReplicaState {
		if !replica.HasDiff(r.revs) {
			updatedReplicas++
		}

		if replica.Ready(len(r.ReplicaState)) {
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
	r.SetCondition(metav1.Condition{
		Type:    v1.KeeperConditionTypeScaleAllowed,
		Status:  metav1.ConditionTrue,
		Reason:  v1.KeeperConditionReasonReadyToScale,
		Message: "",
	})

	return nil
}
