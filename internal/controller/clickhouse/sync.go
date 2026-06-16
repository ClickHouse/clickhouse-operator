package clickhouse

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strconv"
	"strings"
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
	replicaProbe
	chctrl.ReplicaState

	ReloadError error
	Error       bool
}

func (r replicaState) Ready() bool {
	if r.STS == nil {
		return false
	}

	return r.Version != "" && r.STS.Status.ReadyReplicas == 1 // Not reliable, but allows to wait until pod is `green`
}

func (r replicaState) HasDiff(rev chctrl.RevisionState) bool {
	return rev.ReplicaHasDiff(r.ReplicaState)
}

func (r replicaState) UpdateStage(rev chctrl.RevisionState) chctrl.ReplicaUpdateStage {
	if r.STS == nil {
		return chctrl.StageNotExists
	}

	if r.Error || r.ReloadError != nil {
		return chctrl.StageError
	}

	if !r.Updated() {
		return chctrl.StageUpdating
	}

	if r.HasDiff(rev) {
		return chctrl.StageHasDiff
	}

	if !r.Ready() || !r.Reloaded(rev.ReloadConfigRevision) {
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
		{Name: "ExternalSecret", Fn: r.reconcileExternalSecret, Always: true},
		{Name: "ActiveReplicaStatus", Fn: r.reconcileActiveReplicaStatus, Always: true},
		{Name: "Warnings", Fn: r.reconcileWarnings, Always: true},
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
	if r.Cluster.Spec.ExternalSecret != nil {
		return chctrl.StepContinue(), nil
	}

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

func (r *clickhouseReconciler) reconcileExternalSecret(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if r.Cluster.Spec.ExternalSecret == nil {
		meta.RemoveStatusCondition(r.Cluster.GetStatus().GetConditions(), v1.ClickHouseConditionTypeExternalSecretValid)

		return chctrl.StepContinue(), nil
	}

	fail := func(reason v1.ConditionReason, eventReason v1.EventReason, message string) {
		r.SetCondition(metav1.Condition{
			Type:    v1.ClickHouseConditionTypeExternalSecretValid,
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: message,
		}, chctrl.EventSpec{
			Type:    "Warning",
			Reason:  eventReason,
			Action:  v1.EventActionReconciling,
			Message: message,
		})
	}

	if err := r.GetClient().Get(ctx, types.NamespacedName{
		Namespace: r.Cluster.Namespace,
		Name:      r.Cluster.SecretName(),
	}, &r.secret); err != nil {
		var msg string
		if k8serrors.IsNotFound(err) {
			msg = fmt.Sprintf("external secret %q not found", r.Cluster.SecretName())
		} else {
			msg = fmt.Sprintf("failed to get external secret %q: %v", r.Cluster.SecretName(), err)
		}

		fail(v1.ClickHouseConditionReasonExternalSecretNotFound, v1.EventReasonExternalSecretNotFound, msg)
		log.Info(msg)

		return chctrl.StepBlocked(chctrl.RequeueOnRefreshTimeout), nil
	}

	if r.secret.Data == nil {
		r.secret.Data = make(map[string][]byte)
	}

	r.commander = newCommander(log, r.Cluster, &r.secret, r.Dialer)

	if !r.versionProbe.Completed() {
		log.Info("version probe is not completed yet, skipping external secret validation")

		return chctrl.StepBlocked(chctrl.RequeueOnRefreshTimeout), nil
	}

	var missingKeys []int
	for i, spec := range clusterSecrets {
		if spec.enabled(r.Cluster) && len(r.secret.Data[spec.Key]) == 0 {
			missingKeys = append(missingKeys, i)
		}
	}

	if len(missingKeys) == 0 {
		r.SetCondition(metav1.Condition{
			Type:   v1.ClickHouseConditionTypeExternalSecretValid,
			Status: metav1.ConditionTrue,
			Reason: v1.ClickHouseConditionReasonExternalSecretValid,
		})

		return chctrl.StepContinue(), nil
	}

	if r.Cluster.Spec.ExternalSecret.Policy == v1.ExternalSecretPolicyObserve {
		missingKeysWithHints := make([]string, 0, len(missingKeys))
		for _, k := range missingKeys {
			spec := clusterSecrets[k]
			missingKeysWithHints = append(missingKeysWithHints, fmt.Sprintf("%s (%s)", spec.Key, spec.Hint))
		}

		slices.Sort(missingKeysWithHints)
		message := fmt.Sprintf("external secret %q is missing required keys: %s",
			r.Cluster.SecretName(), strings.Join(missingKeysWithHints, ", "))
		fail(
			v1.ClickHouseConditionReasonExternalSecretInvalid,
			v1.EventReasonExternalSecretInvalid,
			message,
		)

		return chctrl.StepBlocked(chctrl.RequeueOnRefreshTimeout), nil
	}

	for _, k := range missingKeys {
		spec := clusterSecrets[k]
		r.secret.Data[spec.Key] = spec.generate()
	}

	if err := r.Update(ctx, &r.secret, v1.EventActionReconciling); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("fill external secret %q: %w", r.Cluster.SecretName(), err)
	}

	r.commander = newCommander(log, r.Cluster, &r.secret, r.Dialer)

	r.SetCondition(metav1.Condition{
		Type:   v1.ClickHouseConditionTypeExternalSecretValid,
		Status: metav1.ConditionTrue,
		Reason: v1.ClickHouseConditionReasonExternalSecretValid,
	})

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) reconcileVersionProbe(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	probeResult, err := r.VersionProbe(ctx, log, chctrl.VersionProbeConfig{
		Labels:            r.Cluster.Spec.Labels,
		Annotations:       r.Cluster.Spec.Annotations,
		PodTemplate:       r.Cluster.Spec.PodTemplate,
		ContainerTemplate: r.Cluster.Spec.ContainerTemplate,
		VersionProbe:      r.Cluster.Spec.VersionProbeTemplate,
		CachedVersion:     r.Cluster.Status.Version,
		CachedRevision:    r.Cluster.Status.VersionProbeRevision,
	})
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("run version probe: %w", err)
	}

	r.versionProbe = probeResult
	if probeResult.Completed() {
		r.Cluster.Status.Version = probeResult.Version
		r.Cluster.Status.VersionProbeRevision = probeResult.Revision
	} else {
		reason := v1.ConditionReasonVersionPending
		message := "Version probe has not completed yet"

		var event []chctrl.EventSpec
		if r.versionProbe.Err != nil {
			reason = v1.ConditionReasonVersionProbeFailed
			message = fmt.Sprintf("Version probe failed: %v", r.versionProbe.Err)
			event = []chctrl.EventSpec{{
				Type:    corev1.EventTypeWarning,
				Reason:  v1.EventReasonVersionProbeFailed,
				Action:  v1.EventActionVersionCheck,
				Message: message,
			}}
		}

		r.SetCondition(metav1.Condition{
			Type:    v1.ConditionTypeVersionInSync,
			Status:  metav1.ConditionUnknown,
			Reason:  reason,
			Message: message,
		}, event...)
		meta.RemoveStatusCondition(r.Cluster.GetStatus().GetConditions(), v1.ConditionTypeVersionUpgraded)
	}

	return chctrl.StepContinue(), nil
}

func (r *clickhouseReconciler) reconcileActiveReplicaStatus(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	listOpts := ctrlutil.AppRequirements(r.Cluster.Namespace, r.Cluster.SpecificName())

	var statefulSets appsv1.StatefulSetList
	if err := r.GetClient().List(ctx, &statefulSets, listOpts); err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list StatefulSets: %w", err)
	}

	configMaps, err := chctrl.ListReplicaResources[v1.ClickHouseReplicaID, *corev1.ConfigMap, *corev1.ConfigMapList](ctx, &r.ResourceManager, v1.ClickHouseIDFromLabels)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list replicas ConfigMaps: %w", err)
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

		var (
			probe     replicaProbe
			reloadErr error
		)

		if !hasError && sts.Status.ReadyReplicas > 0 && r.commander != nil {
			ctx, cancel := context.WithTimeout(ctx, chctrl.LoadReplicaStateTimeout)
			defer cancel()

			probe, err = r.commander.Probe(ctx, id)
			if err != nil {
				log.Debug("failed to probe replica", "replica_id", id, "error", err)
			}

			if cfg, ok := configMaps[id]; ok && !probe.Reloaded(cfg.Annotations[ctrlutil.AnnotationReloadableConfigHash]) {
				if reloadErr = r.commander.ReloadConfig(ctx, id); reloadErr != nil {
					log.Debug("replica config reload failed", "replica_id", id, "error", reloadErr)
				} else if probe, err = r.commander.Probe(ctx, id); err != nil {
					log.Debug("failed to re-probe replica after reload", "replica_id", id, "error", err)
				}
			}
		}

		pvcs, pvcErr := r.GetReplicaPVCs(ctx, &sts)
		if pvcErr != nil {
			log.Error(pvcErr, "failed to get PVCs for replica", "replica_id", id)
		}

		log.Debug("load replica state done", "replica_id", id, "statefulset", sts.Name)

		return id, replicaState{
			ReloadError:  reloadErr,
			Error:        hasError,
			replicaProbe: probe,
			ReplicaState: chctrl.ReplicaState{
				STS:  &sts,
				CFG:  configMaps[id],
				PVCs: pvcs,
			},
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
			log.Debug(fmt.Sprintf("multiple StatefulSets for single replica %s, %s",
				r.ReplicaState[id].STS.Name, res.Result.STS.Name), "replica_id", id)
		}
	}

	r.evaluateReplicaConditions()

	return chctrl.StepContinue(), nil
}

func warningAction(id v1.ClickHouseReplicaID, msg string) string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d/%d/%s", id.ShardID, id.Index, msg)
	return fmt.Sprintf("Warning-%d-%d-%016x", id.ShardID, id.Index, h.Sum64())
}

func (r *clickhouseReconciler) reconcileWarnings(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	if r.commander == nil {
		return chctrl.StepRequeue(chctrl.WarningsPollInterval), nil
	}

	ids := slices.Collect(maps.Keys(r.ReplicaState))

	results := ctrlutil.ExecuteParallel(ids, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, struct{}, error) {
		replica := r.ReplicaState[id]
		if replica.Error || replica.STS == nil || replica.STS.Status.ReadyReplicas == 0 {
			return id, struct{}{}, nil
		}

		ctx, cancel := context.WithTimeout(ctx, chctrl.LoadReplicaStateTimeout)
		defer cancel()

		warnings, err := r.commander.Warnings(ctx, id)
		if err != nil {
			return id, struct{}{}, fmt.Errorf("fetch warnings from replica %s: %w", id, err)
		}

		log.Debug("system.warnings fetched", "replica_id", id, "count", len(warnings))

		for _, warning := range warnings {
			r.GetRecorder().Eventf(r.Cluster, nil, corev1.EventTypeWarning,
				v1.EventReasonClickHouseWarning, warningAction(id, warning),
				"Replica %s: %s", r.Cluster.HostnameByID(id), warning)
		}

		return id, struct{}{}, nil
	})

	for id, res := range results {
		if res.Err != nil {
			log.Warn("failed to publish replica warnings", "replica_id", id, "error", res.Err)
		}
	}

	return chctrl.StepRequeue(chctrl.WarningsPollInterval), nil
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

	keeperNamespacedName := r.Cluster.KeeperClusterNamespacedName()
	if err := r.GetClient().Get(ctx, keeperNamespacedName, &r.keeper); err != nil {
		if k8serrors.IsNotFound(err) {
			log.Debug("keeper cluster not found, waiting", "keeper", keeperNamespacedName.String())

			return chctrl.StepBlocked(chctrl.RequeueOnRefreshTimeout), nil
		}

		return chctrl.StepResult{}, fmt.Errorf("get keeper cluster %q: %w", keeperNamespacedName.String(), err)
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

	cfgRev, err := getConfigurationRevisions(r)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("get configuration revisions: %w", err)
	}

	r.revs.ReloadConfigRevision = cfgRev.Reload
	r.revs.RestartConfigRevision = cfgRev.Restart
	r.revs.ConfigurationRevision = cfgRev.Config

	if cfgRev.Config != r.Cluster.Status.ConfigurationRevision {
		r.Cluster.Status.ConfigurationRevision = cfgRev.Config
		log.Debug(fmt.Sprintf("observed new configuration revision %q", cfgRev.Config))
	}

	r.revs.StatefulSetRevision, err = getStatefulSetRevision(r, r.revs.RestartConfigRevision)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("get StatefulSet revision: %w", err)
	}

	if r.revs.StatefulSetRevision != r.Cluster.Status.StatefulSetRevision {
		r.Cluster.Status.StatefulSetRevision = r.revs.StatefulSetRevision
		log.Debug(fmt.Sprintf("observed new StatefulSet revision %q", r.revs.StatefulSetRevision))
	}

	r.revs.PVCRevisions, err = chctrl.PVCRevisions(
		chctrl.DesiredPVCs(r.Cluster.Spec.DataVolumeClaimSpec, r.Cluster.Spec.AdditionalVolumeClaimTemplates),
	)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("compute PVC revisions: %w", err)
	}

	var notUpdated, reloadErr, notReloaded []string
	for shard := range r.Cluster.Shards() {
		for index := range r.Cluster.Replicas() {
			id := v1.ClickHouseReplicaID{ShardID: shard, Index: index}
			replica := r.ReplicaState[id]

			if replica.HasDiff(r.revs) || !replica.Updated() {
				notUpdated = append(notUpdated, id.String())
			}

			if replica.ReloadError != nil {
				reloadErr = append(reloadErr, id.String())
			}

			if !replica.Reloaded(r.revs.ReloadConfigRevision) {
				notReloaded = append(notReloaded, id.String())
			}
		}
	}

	r.SetCondition(chctrl.ConfigSyncCondition(reloadErr, notUpdated, notReloaded))

	exists := len(r.ReplicaState)
	expected := int(r.Cluster.Replicas() * r.Cluster.Shards())

	if len(notUpdated) == 0 && len(reloadErr) == 0 && len(notReloaded) == 0 && exists == expected {
		r.Cluster.Status.CurrentRevision = r.Cluster.Status.UpdateRevision
	}

	replicaVersions := make(map[string]string, len(r.ReplicaState))
	for id, replica := range r.ReplicaState {
		replicaVersions[id.String()] = replica.Version
	}

	{
		cond, event := chctrl.GetVersionSyncCondition(r.versionProbe.Version, replicaVersions, len(notUpdated) > 0)
		r.SetCondition(cond, event...)
	}

	if r.Checker != nil {
		cond, event := chctrl.GetUpgradeCondition(*r.Checker, r.versionProbe.Version, r.Cluster.Spec.UpgradeChannel)
		r.SetCondition(cond, event...)
	} else {
		meta.RemoveStatusCondition(r.Cluster.GetStatus().GetConditions(), v1.ConditionTypeVersionUpgraded)
	}

	return chctrl.StepContinue(), nil
}

// reconcileReplicaResources performs update on replicas ConfigMap, StatefulSet, PVC.
// If there are replicas that has no created StatefulSet, creates immediately.
// For existing and Ready replicas performs rolling update. Update priority:
// NotExists -> StartupErrors -> UpdateInProgress -> HasDiff(rolling).
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

		requeueAfter = chctrl.RequeueOnRefreshTimeout
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

func (r *clickhouseReconciler) reconcileCleanUp(ctx context.Context, log ctrlutil.Logger) (chctrl.StepResult, error) {
	var replicasToRemove = map[v1.ClickHouseReplicaID]chctrl.ReplicaState{}

	configMaps, err := chctrl.ListReplicaResources[v1.ClickHouseReplicaID, *corev1.ConfigMap, *corev1.ConfigMapList](ctx, &r.ResourceManager, v1.ClickHouseIDFromLabels)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list replicas ConfigMaps: %w", err)
	}

	for id, configMap := range configMaps {
		if id.ShardID < r.Cluster.Shards() && id.Index < r.Cluster.Replicas() {
			continue
		}

		replicasToRemove[id] = chctrl.ReplicaState{
			CFG: configMap,
		}
	}

	statefulSets, err := chctrl.ListReplicaResources[v1.ClickHouseReplicaID, *appsv1.StatefulSet, *appsv1.StatefulSetList](ctx, &r.ResourceManager, v1.ClickHouseIDFromLabels)
	if err != nil {
		return chctrl.StepResult{}, fmt.Errorf("list StatefulSets: %w", err)
	}

	for id, sts := range statefulSets {
		if id.ShardID < r.Cluster.Shards() && id.Index < r.Cluster.Replicas() {
			continue
		}

		state := replicasToRemove[id]
		state.STS = sts
		replicasToRemove[id] = state
	}

	for id, res := range replicasToRemove {
		inSync := !r.unsyncedShards[id.ShardID]

		// Always delete orphaned ConfigMaps.
		if res.CFG != nil && (inSync || res.STS == nil) {
			log.Info("removing replica configmap", "replica_id", id, "configmap", res.CFG.Name)

			if err := r.Delete(ctx, res.CFG, v1.EventActionReconciling); err != nil {
				log.Error(err, "failed to delete replica configmap", "replica_id", id, "configmap", res.CFG.Name)
			}
		}

		// Delete StatefulSets only if the entire shard is removed or shard sync succeeded.
		if res.STS == nil {
			continue
		}

		if !inSync {
			log.Info("shard sync failed, skipping replica deletion", "replica_id", id)
			continue
		}

		log.Info("removing replica statefulset", "replica_id", id, "statefulset", res.STS.Name)

		if err := r.Delete(ctx, res.STS, v1.EventActionReconciling); err != nil {
			log.Error(err, "failed to delete replica statefulset", "replica_id", id, "statefulset", res.STS.Name)
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
	r.SetCondition(chctrl.ClusterSizeCondition(exists, expected))

	if r.commander == nil {
		r.SetCondition(metav1.Condition{
			Type:    v1.ConditionTypeHealthy,
			Status:  metav1.ConditionUnknown,
			Reason:  v1.ClickHouseConditionReasonPingImpossible,
			Message: "Cannot probe replicas",
		})
		r.SetCondition(metav1.Condition{
			Type:    v1.ConditionTypeReady,
			Status:  metav1.ConditionUnknown,
			Reason:  v1.ClickHouseConditionReasonPingImpossible,
			Message: "Cannot probe replicas",
		})
	} else {
		r.SetCondition(chctrl.HealthyCondition(notReadyIDs))

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
}

func (r *clickhouseReconciler) updateReplica(ctx context.Context, log ctrlutil.Logger, id v1.ClickHouseReplicaID) (*ctrl.Result, error) {
	log = log.With("replica_id", id)
	log.Info("updating replica")

	configMap, err := templateConfigMap(r, id)
	if err != nil {
		return nil, fmt.Errorf("template replica %s ConfigMap: %w", id, err)
	}

	statefulSet, err := templateStatefulSet(r, id, r.revs.RestartConfigRevision)
	if err != nil {
		return nil, fmt.Errorf("template replica %s StatefulSet: %w", id, err)
	}

	replica := r.ReplicaState[id]

	result, err := r.ReconcileReplicaResources(ctx, log, chctrl.ReplicaUpdateInput{
		Revisions: r.revs,
		Existing:  replica.ReplicaState,
		HasError:  replica.Error,
		Desired: chctrl.ReplicaState{
			CFG:  configMap,
			STS:  statefulSet,
			PVCs: chctrl.DesiredPVCs(r.Cluster.Spec.DataVolumeClaimSpec, r.Cluster.Spec.AdditionalVolumeClaimTemplates),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile replica %s resources: %w", id, err)
	}

	return result, nil
}
