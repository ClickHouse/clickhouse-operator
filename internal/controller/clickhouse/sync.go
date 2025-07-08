package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type replicaID struct {
	shardID int32
	index   int32
}

func idFromLabels(labels map[string]string) (replicaID, error) {
	shardIDStr, ok := labels[util.LabelClickHouseShardID]
	if !ok {
		return replicaID{}, fmt.Errorf("missing shard ID label")
	}

	shardID, err := strconv.ParseInt(shardIDStr, 10, 32)
	if err != nil {
		return replicaID{}, fmt.Errorf("invalid shard ID %q: %w", shardIDStr, err)
	}

	replicaIDStr, ok := labels[util.LabelClickHouseReplicaID]
	if !ok {
		return replicaID{}, fmt.Errorf("missing replica ID label")
	}

	index, err := strconv.ParseInt(replicaIDStr, 10, 32)
	if err != nil {
		return replicaID{}, fmt.Errorf("invalid replica ID %q: %w", replicaIDStr, err)
	}

	return replicaID{shardID: int32(shardID), index: int32(index)}, nil
}

func (id replicaID) Labels() map[string]string {
	return map[string]string{
		util.LabelClickHouseShardID:   strconv.Itoa(int(id.shardID)),
		util.LabelClickHouseReplicaID: strconv.Itoa(int(id.index)),
	}
}

func compareReplicaID(a, b replicaID) int {
	if a.shardID < b.shardID {
		return -1
	}
	if a.shardID > b.shardID {
		return 1
	}

	if a.index < b.index {
		return -1
	}
	if a.index > b.index {
		return 1
	}
	return 0
}

type replicaState struct {
	Error       bool `json:"error"`
	StatefulSet *appsv1.StatefulSet
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

	// TODO design better health check
	return r.StatefulSet.Status.ReadyReplicas == 1 // Not reliable, but allows to wait until pod is `green`
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

	if !r.Ready() {
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
	chctrl.ReconcileContextBase[*v1.ClickHouseCluster, replicaID, replicaState]

	// Should be populated after reconcileClusterRevisions with parsed extra config.
	keeper v1.KeeperCluster
	// Should be populated by reconcileCommonResources.
	secret corev1.Secret
}

type ReconcileFunc func(util.Logger, *reconcileContext) (*ctrl.Result, error)

func (r *ClusterReconciler) Sync(ctx context.Context, log util.Logger, cr *v1.ClickHouseCluster) (ctrl.Result, error) {
	log.Info("Enter ClickHouse Reconcile", "spec", cr.Spec, "status", cr.Status)

	recCtx := reconcileContext{
		ReconcileContextBase: chctrl.ReconcileContextBase[*v1.ClickHouseCluster, replicaID, replicaState]{
			Cluster:      cr,
			Context:      ctx,
			ExtraConfig:  map[string]any{},
			ReplicaState: map[replicaID]replicaState{},
		},
	}

	reconcileSteps := []ReconcileFunc{
		r.reconcileCommonResources,
		r.reconcileClusterRevisions,
		r.reconcileActiveReplicaStatus,
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
				recCtx.NewCondition(v1.ClickHouseConditionTypeReconcileSucceeded, metav1.ConditionFalse, v1.ClickHouseConditionReasonStepFailed, errMsg),
				// Operator did not finish reconciliation, some conditions may not be valid already.
				recCtx.NewCondition(v1.ClickHouseConditionTypeReady, metav1.ConditionUnknown, v1.ClickHouseConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ClickHouseConditionTypeHealthy, metav1.ConditionUnknown, v1.ClickHouseConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ClickHouseConditionTypeReplicaStartupSucceeded, metav1.ConditionUnknown, v1.ClickHouseConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ClickHouseConditionTypeConfigurationInSync, metav1.ConditionUnknown, v1.ClickHouseConditionReasonStepFailed, errMsg),
				recCtx.NewCondition(v1.ClickHouseConditionTypeClusterSizeAligned, metav1.ConditionUnknown, v1.ClickHouseConditionReasonStepFailed, errMsg),
			})

			return ctrl.Result{RequeueAfter: RequeueOnErrorTimeout}, r.upsertStatus(log, &recCtx)
		}

		if !stepResult.IsZero() {
			stepLog.Debug("reconcile step result", "result", stepResult)
			util.UpdateResult(&result, stepResult)
		}

		stepLog.Debug("reconcile step completed")
	}

	recCtx.SetCondition(log, v1.ClickHouseConditionTypeReconcileSucceeded, metav1.ConditionTrue, v1.ClickHouseConditionReasonReconcileFinished, "Reconcile succeeded")
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

	if err := r.Get(ctx.Context, types.NamespacedName{
		Namespace: ctx.Cluster.Namespace,
		Name:      ctx.Cluster.Spec.KeeperClusterRef.Name,
	}, &ctx.keeper); err != nil {
		return nil, fmt.Errorf("get keeper cluster: %w", err)
	}

	if cond := meta.FindStatusCondition(ctx.keeper.Status.Conditions, string(v1.KeeperConditionTypeReady)); cond == nil || cond.Status != metav1.ConditionTrue {
		if cond == nil {
			log.Warn("keeper cluster is not ready")
		} else {
			log.Warn("keeper cluster is not ready", "reason", cond.Reason, "message", cond.Message)
		}
	}

	configRevision, err := GetConfigurationRevision(ctx)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("get configuration revision: %w", err)
	}
	if configRevision != ctx.Cluster.Status.ConfigurationRevision {
		ctx.Cluster.Status.ConfigurationRevision = configRevision
		log.Debug(fmt.Sprintf("observed new configuration revision %q", configRevision))
	}

	stsRevision, err := GetStatefulSetRevision(ctx)
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
	appReq, err := labels.NewRequirement(util.LabelAppKey, selection.Equals, []string{ctx.Cluster.SpecificName()})
	if err != nil {
		return nil, fmt.Errorf("make %q requirement to list: %w", util.LabelAppKey, err)
	}
	listOpts := &client.ListOptions{
		Namespace:     ctx.Cluster.Namespace,
		LabelSelector: labels.NewSelector().Add(*appReq),
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx.Context, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		id, err := idFromLabels(sts.Labels)
		if err != nil {
			log.Error(err, "failed to get replica ID from StatefulSet labels", "stateful_set", sts.Name)
			continue
		}

		hasError, err := chctrl.CheckPodError(ctx.Context, log, r.Client, &sts)
		if err != nil {
			log.Warn("failed to check replica pod error", "stateful_set", sts.Name, "error", err)
			hasError = true
		}

		if exists := ctx.SetReplica(id, replicaState{
			StatefulSet: &sts,
			Error:       hasError,
		}); exists {
			log.Debug(fmt.Sprintf("multiple StatefulSets for single replica %v", id),
				"replica_id", id, "statefuleset", sts.Name)
		}
	}

	return nil, nil
}

func (r *ClusterReconciler) reconcileCommonResources(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	service := TemplateHeadlessService(ctx.Cluster)
	if _, err := util.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, service); err != nil {
		return &ctrl.Result{}, fmt.Errorf("reconcile service resource: %w", err)
	}

	for shard := range ctx.Cluster.Shards() {
		pdb := TemplatePodDisruptionBudget(ctx.Cluster, shard)
		if _, err := util.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, pdb); err != nil {
			return &ctrl.Result{}, fmt.Errorf("reconcile PodDisruptionBudget resource fr shard %d: %w", shard, err)
		}
	}

	getErr := r.Get(ctx.Context, types.NamespacedName{
		Namespace: ctx.Cluster.Namespace,
		Name:      ctx.Cluster.SecretName(),
	}, &ctx.secret)
	if getErr != nil && !k8serrors.IsNotFound(getErr) {
		return &ctrl.Result{}, fmt.Errorf("get ClickHouse cluster secret %q: %w", ctx.Cluster.SecretName(), getErr)
	}
	secretsUpdated, err := TemplateClusterSecrets(ctx.Cluster, &ctx.secret)
	if err != nil {
		return &ctrl.Result{}, fmt.Errorf("template cluster secrets: %w", err)
	}
	if err := ctrl.SetControllerReference(ctx.Cluster, &ctx.secret, r.Scheme); err != nil {
		return &ctrl.Result{}, fmt.Errorf("set controller reference for cluster secret %q: %w", ctx.Cluster.SecretName(), err)
	}

	if getErr != nil {
		log.Info("cluster secret not found, creating", "secret", ctx.Cluster.SecretName())
		if err = r.Create(ctx.Context, &ctx.secret); err != nil {
			return &ctrl.Result{}, fmt.Errorf("create cluster secret %q: %w", ctx.Cluster.SecretName(), err)
		}
	} else if secretsUpdated {
		if err := r.Update(ctx.Context, &ctx.secret); err != nil {
			return &ctrl.Result{}, fmt.Errorf("update cluster secret %q: %w", ctx.Cluster.SecretName(), err)
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
	var replicasInStatus []replicaID

	for shard := range ctx.Cluster.Shards() {
		for replica := range ctx.Cluster.Replicas() {
			id := replicaID{shardID: shard, index: replica}

			stage := ctx.Replica(id).UpdateStage(ctx)
			if stage == highestStage {
				replicasInStatus = append(replicasInStatus, id)
				continue
			}

			if stage > highestStage {
				highestStage = stage
				replicasInStatus = []replicaID{id}
			}
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
			if id.shardID > chosenReplica.shardID || (id.shardID == chosenReplica.shardID && id.index > chosenReplica.index) {
				chosenReplica = id
			}
		}
		log.Info(fmt.Sprintf("updating chosen replica %v with priority %s: %v", chosenReplica, highestStage.String(), replicasInStatus))
		replicasInStatus = []replicaID{chosenReplica}
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

	replicasToRemove := map[replicaID]struct {
		cfg corev1.ConfigMap
		sts appsv1.StatefulSet
	}{}

	var configMaps corev1.ConfigMapList
	if err := r.List(ctx.Context, &configMaps, listOpts); err != nil {
		return nil, fmt.Errorf("list ConfigMaps: %w", err)
	}

	for _, configMap := range configMaps.Items {
		id, err := idFromLabels(configMap.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from ConfigMap labels", "configmap", configMap.Name, "error", err)
			continue
		}

		if id.shardID < ctx.Cluster.Shards() && id.index < ctx.Cluster.Replicas() {
			continue
		}

		state := replicasToRemove[id]
		state.cfg = configMap
		replicasToRemove[id] = state
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx.Context, &statefulSets, listOpts); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		id, err := idFromLabels(sts.Labels)
		if err != nil {
			log.Warn("failed to get replica ID from ConfigMap labels", "statefulset", sts.Name, "error", err)
			continue
		}

		if id.shardID < ctx.Cluster.Shards() && id.index < ctx.Cluster.Replicas() {
			continue
		}

		state := replicasToRemove[id]
		state.sts = sts
		replicasToRemove[id] = state
	}

	for id, state := range replicasToRemove {
		// TODO check replica is synced

		log.Info("removing replica resources", "replica_id", id)
		if len(state.cfg.Name) > 0 {
			if err := r.Delete(ctx.Context, &state.cfg); err != nil {
				if !k8serrors.IsNotFound(err) {
					return nil, fmt.Errorf("delete ConfigMap %s: %w", state.cfg.Name, err)

				}
				log.Warn("ConfigMap already deleted", "configmap", state.cfg.Name)
			}
		}
		if len(state.sts.Name) > 0 {
			if err := r.Delete(ctx.Context, &state.sts); err != nil {
				if !k8serrors.IsNotFound(err) {
					return nil, fmt.Errorf("delete StatefulSet %s: %w", state.sts.Name, err)
				}
				log.Warn("StatefulSet already deleted", "statefulset", state.sts.Name)
			}
		}
	}

	// TODO cleanup shard disruption budgets

	return nil, nil
}

func (r *ClusterReconciler) reconcileConditions(log util.Logger, ctx *reconcileContext) (*ctrl.Result, error) {
	var errorReplicas []replicaID
	var notReadyReplicas []replicaID
	var notUpdatedReplicas []replicaID
	var notReadyShards []int32

	ctx.Cluster.Status.ReadyReplicas = 0
	for shard := range ctx.Cluster.Shards() {
		hasReady := false
		for index := range ctx.Cluster.Replicas() {
			id := replicaID{shardID: shard, index: index}
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
		ctx.SetCondition(log, v1.ClickHouseConditionTypeReplicaStartupSucceeded, metav1.ConditionFalse, v1.ClickHouseConditionReasonReplicaError, message)
	} else {
		ctx.SetCondition(log, v1.ClickHouseConditionTypeReplicaStartupSucceeded, metav1.ConditionTrue, v1.ClickHouseConditionReasonReplicasRunning, "")
	}

	if len(notReadyReplicas) > 0 {
		slices.SortFunc(notReadyReplicas, compareReplicaID)
		message := fmt.Sprintf("Not ready replicas: %v", notReadyReplicas)
		ctx.SetCondition(log, v1.ClickHouseConditionTypeHealthy, metav1.ConditionFalse, v1.ClickHouseConditionReasonReplicasNotReady, message)
	} else {
		ctx.SetCondition(log, v1.ClickHouseConditionTypeHealthy, metav1.ConditionTrue, v1.ClickHouseConditionReasonReplicasReady, "")
	}

	if len(notUpdatedReplicas) > 0 {
		slices.SortFunc(notUpdatedReplicas, compareReplicaID)
		message := fmt.Sprintf("Replicas has pending updates: %v", notUpdatedReplicas)
		ctx.SetCondition(log, v1.ClickHouseConditionTypeConfigurationInSync, metav1.ConditionFalse, v1.ClickHouseConditionReasonConfigurationChanged, message)
	} else {
		ctx.SetCondition(log, v1.ClickHouseConditionTypeConfigurationInSync, metav1.ConditionTrue, v1.ClickHouseConditionReasonUpToDate, "")
	}

	exists := len(ctx.ReplicaState)
	expected := int(ctx.Cluster.Replicas() * ctx.Cluster.Shards())

	if exists < expected {
		ctx.SetCondition(log, v1.ClickHouseConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ClickHouseConditionReasonScalingUp, "Cluster has less replicas that requested")
	} else if exists > expected {
		ctx.SetCondition(log, v1.ClickHouseConditionTypeClusterSizeAligned, metav1.ConditionFalse, v1.ClickHouseConditionReasonScalingDown, "Cluster has more replicas that requested")
	} else {
		ctx.SetCondition(log, v1.ClickHouseConditionTypeClusterSizeAligned, metav1.ConditionTrue, v1.ClickHouseConditionReasonUpToDate, "")
	}

	if len(notUpdatedReplicas) == 0 && exists == expected {
		ctx.Cluster.Status.CurrentRevision = ctx.Cluster.Status.UpdateRevision
	}

	if len(notReadyShards) == 0 {
		ctx.SetCondition(log, v1.ClickHouseConditionTypeReady, metav1.ConditionTrue, v1.ClickHouseConditionAllShardsReady, "All shards are ready")
	} else {
		slices.Sort(notReadyShards)
		message := fmt.Sprintf("Some shards are not ready: %v", notReadyShards)
		ctx.SetCondition(log, v1.ClickHouseConditionTypeReady, metav1.ConditionFalse, v1.ClickHouseConditionSomeShardsNotReady, message)
	}

	for _, condition := range ctx.Cluster.Status.Conditions {
		if condition.Status != metav1.ConditionTrue {
			return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
		}
	}

	return &ctrl.Result{}, nil
}

func (r *ClusterReconciler) updateReplica(log util.Logger, ctx *reconcileContext, id replicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", id)
	log.Info("updating replica")

	configMap, err := TemplateConfigMap(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %q ConfigMap: %w", id, err)
	}

	configChanged, err := util.ReconcileResource(ctx.Context, log, r.Client, r.Scheme, ctx.Cluster, configMap, "Data", "BinaryData")
	if err != nil {
		return nil, fmt.Errorf("update replica %q ConfigMap: %w", id, err)
	}

	statefulSet := TemplateStatefulSet(ctx, id)
	if err := ctrl.SetControllerReference(ctx.Cluster, statefulSet, r.Scheme); err != nil {
		return nil, fmt.Errorf("set replica %q StatefulSet controller reference: %w", id, err)
	}

	replica := ctx.Replica(id)
	if replica.StatefulSet == nil {
		log.Info("replica StatefulSet not found, creating", "stateful_set", statefulSet.Name)
		util.AddObjectConfigHash(statefulSet, ctx.Cluster.Status.ConfigurationRevision)
		util.AddHashWithKeyToAnnotations(statefulSet, util.AnnotationSpecHash, ctx.Cluster.Status.StatefulSetRevision)

		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			return r.Create(ctx.Context, statefulSet)
		})

		if err != nil {
			return nil, fmt.Errorf("create replica %q StatefulSet %q: %w", id, statefulSet.Name, err)
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
		return nil, fmt.Errorf("update replica %q StatefulSet %q: %w", id, statefulSet.Name, err)
	}

	return &ctrl.Result{RequeueAfter: RequeueOnRefreshTimeout}, nil
}

func (r *ClusterReconciler) upsertStatus(log util.Logger, ctx *reconcileContext) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		crdInstance := &v1.ClickHouseCluster{}
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
