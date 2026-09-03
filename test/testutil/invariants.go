package testutil

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	invariantPollInterval    = 250 * time.Millisecond
	invariantPollMinInterval = 50 * time.Millisecond

	operatorPodLabelKey   = "control-plane"
	operatorPodLabelValue = "controller-manager"
)

// Violation is a single invariant breach observed during a spec.
type Violation struct {
	Time      time.Time
	Invariant string
	Object    string
	Message   string
	Warning   bool
}

func (v Violation) String() string {
	return fmt.Sprintf("%s [%s] %s: %s", v.Time.Format(time.RFC3339Nano), v.Invariant, v.Object, v.Message)
}

// Invariant represents a single cluster property that should not be violated during the cluster lifetime.
type Invariant interface {
	Name() string
	Check(ctx context.Context, namespace string) []Violation
}

// InvariantSet enforces invariants in a background goroutine for one spec.
type InvariantSet struct {
	cancel  context.CancelFunc
	detach  []func() error
	wg      sync.WaitGroup
	stopped atomic.Bool

	// violations are written by the enforcement goroutine only and read after wg.Wait.
	violations []Violation
}

// StartInvariants launches background enforcement of all invariants; absence of violations is asserted in DeferCleanup.
func StartInvariants(env *Env, namespace string) *InvariantSet {
	GinkgoHelper()

	reader := env.Cache()
	invariants := []Invariant{
		&updateOrderInvariant{reader: reader, armed: map[string]bool{}, groups: map[string]*stsGroup{}},
		&readyStableInvariant{reader: reader, armed: map[string]clusterSize{}},
		&readyIsInitializedInvariant{reader: reader, dialer: env.Dialer, checked: map[types.UID]bool{}},
		&operatorStableInvariant{reader: reader},
	}

	ctx, cancel := context.WithCancel(context.Background())
	set := &InvariantSet{cancel: cancel}
	triggers := make(chan struct{}, 1)

	kick := func() {
		select {
		case triggers <- struct{}{}:
		default:
		}
	}

	handler := toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { kick() },
		UpdateFunc: func(any, any) { kick() },
		DeleteFunc: func(any) { kick() },
	}

	watch := func(ctx context.Context, obj client.Object) error {
		informer, err := reader.GetInformer(ctx, obj)
		if err != nil {
			return fmt.Errorf("get informer: %w", err)
		}

		registration, err := informer.AddEventHandler(handler)
		if err != nil {
			return fmt.Errorf("add event handler: %w", err)
		}

		set.detach = append(set.detach, func() error {
			return informer.RemoveEventHandler(registration)
		})

		return nil
	}

	set.wg.Add(1)

	go func() {
		defer set.wg.Done()
		defer GinkgoRecover()

		// Lazy init informers, so suite can create CRDs
		for _, obj := range []client.Object{
			&v1.ClickHouseCluster{},
			&v1.KeeperCluster{},
			&appsv1.StatefulSet{},
			&corev1.Pod{},
			&appsv1.Deployment{},
		} {
			for watch(ctx, obj) != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(invariantPollMinInterval):
				}
			}
		}

		timer := time.NewTimer(invariantPollInterval)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-triggers:
			case <-timer.C:
			}

			for _, invariant := range invariants {
				set.record(invariant.Check(ctx, namespace))
			}

			time.Sleep(invariantPollMinInterval)
			timer.Reset(invariantPollInterval)
		}
	}()

	DeferCleanup(set.StopAndCheck)

	return set
}

// StopAndCheck stops enforcement and fails the spec on recorded violations; extra calls are no-ops.
func (s *InvariantSet) StopAndCheck() {
	GinkgoHelper()

	if !s.stopped.CompareAndSwap(false, true) {
		return
	}

	s.cancel()
	s.wg.Wait()

	for _, detach := range s.detach {
		Expect(detach()).To(Succeed())
	}

	messages := make([]string, 0, len(s.violations))

	for _, violation := range s.violations {
		if violation.Warning {
			GinkgoWriter.Printf("invariant warning: %s\n", violation.String())
			continue
		}

		messages = append(messages, violation.String())
	}

	Expect(messages).To(BeEmpty(), "invariant violations recorded during the spec")
}

func (s *InvariantSet) record(violations []Violation) {
	if len(violations) == 0 {
		return
	}

	s.violations = append(s.violations, violations...)
}

// updateOrderInvariant enforces rolling-update ordering, armed per cluster at first full readiness.
type updateOrderInvariant struct {
	reader client.Reader
	armed  map[string]bool
	groups map[string]*stsGroup
}

// stsGroup carries per-StatefulSet-group state across check passes.
type stsGroup struct {
	// settledRevision is the last revision observed fully rolled out, separating fresh rollouts from stale CR status.
	settledRevision string
	// present remembers desired indexes from the previous pass to catch silent deletions.
	present map[int]bool
	// everReady exempts still-provisioning replicas from the unready budget: batch creation is legitimate.
	everReady map[int]bool
	// lastReady gates the wrong-order check: error recovery legitimately updates unready replicas first.
	lastReady map[int]bool
}

func (u *updateOrderInvariant) Name() string { return "UpdateOrder" }

func (u *updateOrderInvariant) Check(ctx context.Context, namespace string) []Violation {
	var out []Violation

	var chList v1.ClickHouseClusterList
	if err := u.reader.List(ctx, &chList, client.InNamespace(namespace)); err != nil {
		return nil
	}

	for i := range chList.Items {
		cr := &chList.Items[i]
		key := "ClickHouseCluster/" + cr.Namespace + "/" + cr.Name

		if cr.DeletionTimestamp != nil {
			delete(u.armed, key)

			for shard := range cr.Shards() {
				delete(u.groups, fmt.Sprintf("%s shard=%d", key, shard))
			}

			continue
		}

		if !u.arm(key, cr.Status.ReadyReplicas, cr.Replicas()*cr.Shards(), cr.Status.Conditions) {
			continue
		}

		for shard := range cr.Shards() {
			out = append(out, u.checkStatefulSets(ctx, fmt.Sprintf("%s shard=%d", key, shard), client.MatchingLabels{
				controllerutil.LabelAppKey:            cr.SpecificName(),
				controllerutil.LabelClickHouseShardID: strconv.FormatInt(int64(shard), 10),
			}, cr.Namespace, controllerutil.LabelClickHouseReplicaID,
				cr.Status.StatefulSetRevision, int(cr.Replicas()), CheckPodReady)...)
		}
	}

	var keeperList v1.KeeperClusterList
	if err := u.reader.List(ctx, &keeperList, client.InNamespace(namespace)); err != nil {
		return out
	}

	for i := range keeperList.Items {
		cr := &keeperList.Items[i]
		key := "KeeperCluster/" + cr.Namespace + "/" + cr.Name

		if cr.DeletionTimestamp != nil {
			delete(u.armed, key)
			delete(u.groups, key)

			continue
		}

		if !u.arm(key, cr.Status.ReadyReplicas, cr.Replicas(), cr.Status.Conditions) {
			continue
		}

		// Keeper probes reflect quorum: elections flip all pods unready, so pod presence is the clock.
		out = append(out, u.checkStatefulSets(ctx, key, client.MatchingLabels{
			controllerutil.LabelAppKey: cr.SpecificName(),
		}, cr.Namespace, controllerutil.LabelKeeperReplicaID,
			cr.Status.StatefulSetRevision, int(cr.Replicas()), podRunning)...)
	}

	return out
}

func podRunning(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning
}

// arm latches at full readiness: Healthy alone is vacuously true on an empty replica state.
func (u *updateOrderInvariant) arm(key string, readyReplicas, expected int32, conditions []metav1.Condition) bool {
	if u.armed[key] {
		return true
	}

	if readyReplicas == expected && meta.IsStatusConditionTrue(conditions, v1.ConditionTypeHealthy) {
		u.armed[key] = true
	}

	return u.armed[key]
}

// checkStatefulSets validates one group: no desired-replica deletions, at most one unready, top-first updates.
func (u *updateOrderInvariant) checkStatefulSets(
	ctx context.Context, object string,
	labels client.MatchingLabels, namespace, replicaLabel, stsRev string, desired int,
	podReady func(*corev1.Pod) bool,
) []Violation {
	var stsList appsv1.StatefulSetList
	if err := u.reader.List(ctx, &stsList, client.InNamespace(namespace), labels); err != nil {
		return nil
	}

	var pods corev1.PodList
	if err := u.reader.List(ctx, &pods, client.InNamespace(namespace), labels); err != nil {
		return nil
	}

	// Pod readiness is the rollout clock: StatefulSet status lags it by a kube-controller-manager sync.
	ready := map[int]bool{}

	for i := range pods.Items {
		pod := &pods.Items[i]

		index, err := strconv.Atoi(pod.Labels[replicaLabel])
		if err != nil {
			continue
		}

		if pod.DeletionTimestamp == nil && podReady(pod) {
			ready[index] = true
		}
	}

	group, ok := u.groups[object]
	if !ok {
		group = &stsGroup{everReady: map[int]bool{}}
		u.groups[object] = group
	}

	var out []Violation

	violate := func(message string) {
		out = append(out, Violation{Time: time.Now(), Invariant: u.Name(), Object: object, Message: message})
	}

	present := map[int]bool{}
	items := make([]indexedReplica, 0, len(stsList.Items))

	for i := range stsList.Items {
		sts := &stsList.Items[i]

		index, err := strconv.Atoi(sts.Labels[replicaLabel])
		if err != nil || index < 0 {
			violate(fmt.Sprintf("invalid replica label of %s: %q", sts.Name, sts.Labels[replicaLabel]))
			return out
		}

		if index >= desired {
			continue
		}

		present[index] = true

		if sts.DeletionTimestamp != nil {
			violate(fmt.Sprintf("replica %d (%s) is being deleted while still desired", index, sts.Name))
			continue
		}

		items = append(items, indexedReplica{sts: sts, index: index})
	}

	for index := range group.present {
		if index < desired && !present[index] {
			violate(fmt.Sprintf("replica %d disappeared", index))
		}
	}

	group.present = present

	if message := group.rollingOrderViolation(items, ready, stsRev, desired); message != "" {
		violate(message)
	}

	for index := range ready {
		group.everReady[index] = true
	}

	group.lastReady = ready

	return out
}

type indexedReplica struct {
	sts   *appsv1.StatefulSet
	index int
}

// rollingOrderViolation reports an unready-budget or ordering breach, settling the revision on full rollout.
func (g *stsGroup) rollingOrderViolation(
	items []indexedReplica, ready map[int]bool, stsRev string, desired int,
) string {
	if len(items) < 2 {
		return ""
	}

	notReadyCount := 0
	updated := make([]bool, len(items))
	states := make([]string, 0, len(items))

	for _, item := range items {
		if item.index >= len(updated) {
			return fmt.Sprintf(
				"replica index %d of %s out of range for %d StatefulSets", item.index, item.sts.Name, len(updated))
		}

		if !ready[item.index] && g.everReady[item.index] {
			notReadyCount++
		}

		updated[item.index] = controllerutil.GetSpecHashFromObject(item.sts) == stsRev
		states = append(states,
			fmt.Sprintf("%s(ready=%t,updated=%t)", item.sts.Name, ready[item.index], updated[item.index]))
	}

	if notReadyCount > 1 {
		return fmt.Sprintf("%d replicas not ready, expected at most 1: %s", notReadyCount, strings.Join(states, ", "))
	}

	allUpdated := true
	for _, isUpdated := range updated {
		allUpdated = allUpdated && isUpdated
	}

	// A fully rolled out and ready group settles the target revision.
	if allUpdated && notReadyCount == 0 && len(items) == desired {
		g.settledRevision = stsRev
		return ""
	}

	switch {
	case g.settledRevision == "":
		// Without a settled baseline matches are ambiguous unless the highest replica leads.
		if !updated[len(updated)-1] {
			return ""
		}
	case stsRev == g.settledRevision:
		// The CR status lags a starting rollout: matches identify not-yet-updated replicas.
		return ""
	case !updated[len(updated)-1]:
		// Fresh target: the highest replica updates first, its event arrives first on the watch stream.
		for _, item := range items {
			if updated[item.index] && g.lastReady[item.index] {
				return fmt.Sprintf("replica %d updated before the highest replica: %s", item.index, strings.Join(states, ", "))
			}
		}

		return ""
	}

	// find the first updated replica (lowest index that matches target)
	updatedID := 0
	for i, isUpdated := range updated {
		if isUpdated {
			updatedID = i
			break
		}
	}

	// all replicas above the first updated one must also be updated
	for i := updatedID + 1; i < len(updated); i++ {
		if !updated[i] {
			return fmt.Sprintf("replica %d updated before %d", updatedID, i)
		}
	}

	return ""
}

// readyStableInvariant enforces that an armed cluster stays Ready; single-node (per-shard) clusters are exempt.
type readyStableInvariant struct {
	reader client.Reader
	armed  map[string]clusterSize
}

type clusterSize struct {
	replicas int32
	shards   int32
}

func (r *readyStableInvariant) Name() string { return "ReadyStable" }

func (r *readyStableInvariant) Check(ctx context.Context, namespace string) []Violation {
	var out []Violation

	var chList v1.ClickHouseClusterList
	if err := r.reader.List(ctx, &chList, client.InNamespace(namespace)); err != nil {
		return nil
	}

	for i := range chList.Items {
		cr := &chList.Items[i]
		key := "ClickHouseCluster/" + cr.Namespace + "/" + cr.Name

		if cr.DeletionTimestamp != nil || cr.Replicas() <= 1 {
			delete(r.armed, key)
			continue
		}

		size := clusterSize{replicas: cr.Replicas(), shards: cr.Shards()}
		out = append(out, r.check(key, size, cr.Status.ReadyReplicas,
			cr.Generation, cr.Status.ObservedGeneration, cr.Status.Conditions, "")...)
	}

	var keeperList v1.KeeperClusterList
	if err := r.reader.List(ctx, &keeperList, client.InNamespace(namespace)); err != nil {
		return out
	}

	for i := range keeperList.Items {
		cr := &keeperList.Items[i]
		keeperKey := "KeeperCluster/" + cr.Namespace + "/" + cr.Name

		if cr.DeletionTimestamp != nil || cr.Replicas() <= 1 {
			delete(r.armed, keeperKey)
			continue
		}

		// Leader restarts (no handoff today) elect through a NoLeader window: warn instead of fail.
		key := "KeeperCluster/" + cr.Namespace + "/" + cr.Name
		size := clusterSize{replicas: cr.Replicas(), shards: 1}
		out = append(out, r.check(key, size, cr.Status.ReadyReplicas,
			cr.Generation, cr.Status.ObservedGeneration, cr.Status.Conditions,
			v1.KeeperConditionReasonNoLeader)...)
	}

	return out
}

func (r *readyStableInvariant) check(
	key string, size clusterSize, readyReplicas int32,
	generation, observedGeneration int64, conditions []metav1.Condition, warnReason string,
) []Violation {
	armed, wasArmed := r.armed[key]

	if wasArmed && armed != size {
		// Enforce through replica-count changes; re-arm when leaving a single node or changing shards.
		if armed.replicas > 1 && armed.shards == size.shards {
			r.armed[key] = size
		} else {
			delete(r.armed, key)

			wasArmed = false
		}
	}

	if !wasArmed {
		// ReadyReplicas guards against vacuously-true conditions written from an empty replica state.
		if readyReplicas == size.replicas*size.shards &&
			generation == observedGeneration &&
			meta.IsStatusConditionTrue(conditions, v1.ConditionTypeHealthy) &&
			meta.IsStatusConditionTrue(conditions, v1.ConditionTypeClusterSizeAligned) {
			r.armed[key] = size
		}

		return nil
	}

	cond := meta.FindStatusCondition(conditions, v1.ConditionTypeReady)
	if cond != nil && cond.Status == metav1.ConditionFalse {
		return []Violation{{Time: time.Now(), Invariant: r.Name(), Object: key,
			Message: "cluster became unready: " + cond.Message, Warning: cond.Reason == warnReason}}
	}

	return nil
}

// readyIsInitializedInvariant: a gate-passing replica must already have a Replicated default database.
type readyIsInitializedInvariant struct {
	reader  client.Reader
	dialer  controllerutil.DialContextFunc
	checked map[types.UID]bool
}

func (r *readyIsInitializedInvariant) Name() string { return "ReadyIsInitialized" }

func (r *readyIsInitializedInvariant) Check(ctx context.Context, namespace string) []Violation {
	var chList v1.ClickHouseClusterList
	if err := r.reader.List(ctx, &chList, client.InNamespace(namespace)); err != nil {
		return nil
	}

	var out []Violation

	for i := range chList.Items {
		out = append(out, r.checkCluster(ctx, &chList.Items[i])...)
	}

	return out
}

func (r *readyIsInitializedInvariant) checkCluster(
	ctx context.Context, cr *v1.ClickHouseCluster,
) []Violation {
	if cr.DeletionTimestamp != nil {
		return nil
	}

	var pods corev1.PodList
	if err := r.reader.List(ctx, &pods, client.InNamespace(cr.Namespace), client.MatchingLabels{
		controllerutil.LabelAppKey: cr.SpecificName(),
	}); err != nil {
		return nil
	}

	object := "ClickHouseCluster/" + cr.Namespace + "/" + cr.Name
	initialized := map[types.UID]v1.ClickHouseReplicaID{}

	var out []Violation

	for i := range pods.Items {
		pod := &pods.Items[i]
		if r.checked[pod.UID] || !chctrl.PodConditionTrue(pod, v1.ReplicaInitializedCondition) {
			continue
		}

		id, err := v1.ClickHouseIDFromLabels(pod.Labels)
		if err != nil {
			r.checked[pod.UID] = true

			out = append(out, Violation{Time: time.Now(), Invariant: r.Name(), Object: object,
				Message: fmt.Sprintf("gated pod %s has invalid replica labels: %s", pod.Name, err)})

			continue
		}

		initialized[pod.UID] = id
	}

	if len(initialized) == 0 {
		return out
	}

	// Client construction pings every replica: skip the pass while Pods are provisioning.
	chClient, err := NewClickHouseClient(ctx, r.dialer, cr)
	if err != nil {
		return out
	}

	defer chClient.Close()

	for uid, id := range initialized {
		var isReplicated bool
		if err := chClient.QueryRowReplica(ctx, id,
			"SELECT engine='Replicated' FROM system.databases WHERE name='default'", &isReplicated); err != nil {
			continue
		}

		r.checked[uid] = true

		if !isReplicated {
			out = append(out, Violation{Time: time.Now(), Invariant: r.Name(), Object: object,
				Message: fmt.Sprintf("replica %s passed the readiness gate before initialization", id)})
		}
	}

	return out
}

// operatorStableInvariant: operator pods must not restart or get replaced; Deployment changes re-baseline.
type operatorStableInvariant struct {
	reader      client.Reader
	deployments map[string]deploymentIdentity
	pods        map[string]podIdentity
}

type deploymentIdentity struct {
	uid        string
	generation int64
}

type podIdentity struct {
	uid      string
	restarts int32
}

func (o *operatorStableInvariant) Name() string { return "OperatorStable" }

func (o *operatorStableInvariant) Check(ctx context.Context, _ string) []Violation {
	var deployList appsv1.DeploymentList

	err := o.reader.List(ctx, &deployList, client.MatchingLabels{operatorPodLabelKey: operatorPodLabelValue})
	if err != nil {
		return nil
	}

	deployments := make(map[string]deploymentIdentity, len(deployList.Items))
	settled := true

	for i := range deployList.Items {
		deploy := &deployList.Items[i]
		deployments[deploy.Namespace+"/"+deploy.Name] = deploymentIdentity{
			uid:        string(deploy.UID),
			generation: deploy.Generation,
		}

		replicas := int32(1)
		if deploy.Spec.Replicas != nil {
			replicas = *deploy.Spec.Replicas
		}

		if deploy.Status.ObservedGeneration < deploy.Generation ||
			deploy.Status.Replicas != replicas ||
			deploy.Status.UpdatedReplicas != replicas ||
			deploy.Status.AvailableReplicas != replicas {
			settled = false
		}
	}

	if !maps.Equal(o.deployments, deployments) {
		o.deployments = deployments
		o.pods = nil

		return nil
	}

	if !settled || len(deployments) == 0 {
		o.pods = nil

		return nil
	}

	current := o.listPods(ctx)
	if current == nil {
		return nil
	}

	if o.pods == nil {
		if len(current) > 0 {
			o.pods = current
		}

		return nil
	}

	var out []Violation

	violate := func(object, message string) {
		out = append(out, Violation{Time: time.Now(), Invariant: o.Name(), Object: object, Message: message})
	}

	for name, was := range o.pods {
		now, ok := current[name]

		switch {
		case !ok:
			violate(name, "operator pod disappeared")
		case now.uid != was.uid:
			violate(name, "operator pod was replaced")
		case now.restarts > was.restarts:
			violate(name, fmt.Sprintf("operator pod restarted %d time(s)", now.restarts-was.restarts))
		}
	}

	for name := range current {
		if _, ok := o.pods[name]; !ok {
			violate(name, "unexpected new operator pod")
		}
	}

	return out
}

func (o *operatorStableInvariant) listPods(ctx context.Context) map[string]podIdentity {
	var pods corev1.PodList
	if err := o.reader.List(ctx, &pods, client.MatchingLabels{operatorPodLabelKey: operatorPodLabelValue}); err != nil {
		return nil
	}

	current := make(map[string]podIdentity, len(pods.Items))

	for i := range pods.Items {
		pod := &pods.Items[i]

		// Terminating leftovers from a previous deployment are not a stability signal.
		if pod.DeletionTimestamp != nil {
			continue
		}

		var restarts int32
		for _, cs := range pod.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}

		current[pod.Namespace+"/"+pod.Name] = podIdentity{uid: string(pod.UID), restarts: restarts}
	}

	return current
}
