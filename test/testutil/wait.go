package testutil

import (
	"context"
	"fmt"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// ClickHouseValidator checks invariants on every polling tick of WaitClickHouseUpdatedAndReady:
// violations fail hard (Expect). Non-informative errors are skipped.
type ClickHouseValidator func(ctx context.Context, cr *v1.ClickHouseCluster)

// WaitClickHouseUpdatedAndReady waits until the cluster rollout completes and all replicas are ready.
func (e *Env) WaitClickHouseUpdatedAndReady(
	ctx context.Context,
	cr *v1.ClickHouseCluster,
	timeout time.Duration,
	validators ...ClickHouseValidator,
) {
	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.Name))
	EventuallyWithOffset(1, func(g Gomega) {
		var cluster v1.ClickHouseCluster
		g.Expect(e.Client.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())
		g.Expect(cluster.Generation).To(Equal(cluster.Status.ObservedGeneration))

		for _, val := range validators {
			val(ctx, &cluster)
		}

		count := int(cr.Replicas() * cr.Shards())

		g.Expect(cluster.Status.CurrentRevision).
			To(Equal(cluster.Status.UpdateRevision), "rollout should eventually complete")
		g.Expect(cluster.Status.ReadyReplicas).
			To(BeEquivalentTo(count), "all replicas should be ready")

		var pods corev1.PodList
		g.Expect(e.Client.List(ctx, &pods, client.InNamespace(cr.Namespace), client.MatchingLabels{
			controllerutil.LabelAppKey: cr.SpecificName(),
		})).To(Succeed())
		g.Expect(pods.Items).To(HaveLen(count))

		expectConditionsTrue(g, cluster.Status.Conditions,
			v1.ConditionTypeReady,
			v1.ConditionTypeHealthy,
			v1.ConditionTypeClusterSizeAligned,
			v1.ConditionTypeConfigurationInSync,
			v1.ClickHouseConditionTypeSchemaInSync,
		)

		for _, pod := range pods.Items {
			g.Expect(CheckPodReady(&pod)).To(BeTrue(), fmt.Sprintf("pod %s is not ready", pod.Name))
		}
	}, timeout).WithPolling(PollInterval).Should(Succeed())
}

// WaitKeeperUpdatedAndReady waits until the keeper rollout completes and all replicas are ready.
func (e *Env) WaitKeeperUpdatedAndReady(
	ctx context.Context, cr *v1.KeeperCluster, timeout time.Duration, isUpdate bool,
) {
	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.Name))
	EventuallyWithOffset(1, func(g Gomega) {
		var cluster v1.KeeperCluster
		g.Expect(e.Client.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())
		g.Expect(cluster.Generation).To(Equal(cluster.Status.ObservedGeneration))

		if isUpdate {
			// Intentional global assertion to fail suite if update order is wrong.
			Expect(e.CheckUpdateOrder(ctx, &client.ListOptions{
				Namespace: cluster.Namespace,
				LabelSelector: labels.SelectorFromSet(map[string]string{
					controllerutil.LabelAppKey: cluster.SpecificName(),
				}),
			}, controllerutil.LabelKeeperReplicaID, cluster.Status.StatefulSetRevision)).To(Succeed())
		}

		g.Expect(cluster.Status.CurrentRevision).To(Equal(cluster.Status.UpdateRevision))
		g.Expect(cluster.Status.ReadyReplicas).To(Equal(cluster.Replicas()))

		expectConditionsTrue(g, cluster.Status.Conditions,
			v1.ConditionTypeReady,
			v1.ConditionTypeHealthy,
			v1.ConditionTypeClusterSizeAligned,
			v1.ConditionTypeConfigurationInSync,
		)
	}, timeout).WithPolling(PollInterval).Should(Succeed())
	// Needed for replica deletion to not forward deleting pods.
	By(fmt.Sprintf("waiting for cluster %s replicas count match", cr.Name))
	e.WaitReplicaCount(ctx, cr.Namespace, cr.SpecificName(), int(cr.Replicas()))
}

// WaitClusterReady waits for the Ready condition only, reporting all conditions on failure.
func (e *Env) WaitClusterReady(ctx context.Context, cluster client.Object, timeout time.Duration) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		g.Expect(e.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())

		var conditions []metav1.Condition

		switch cr := cluster.(type) {
		case *v1.ClickHouseCluster:
			conditions = cr.Status.Conditions
		case *v1.KeeperCluster:
			conditions = cr.Status.Conditions
		}

		g.Expect(meta.IsStatusConditionTrue(conditions, v1.ConditionTypeReady)).
			To(BeTrue(), "%T %s not ready: %s", cluster, cluster.GetName(), formatConditions(conditions))
	}, timeout, "5s").Should(Succeed())
}

// WaitDeploymentAvailable waits until the deployment rollout completes and it reports Available.
func (e *Env) WaitDeploymentAvailable(ctx context.Context, namespace, name string, timeout time.Duration) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		var deploy appsv1.Deployment
		g.Expect(e.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &deploy)).To(Succeed())

		replicas := int32(1)
		if deploy.Spec.Replicas != nil {
			replicas = *deploy.Spec.Replicas
		}

		g.Expect(deploy.Status.ObservedGeneration).To(BeNumerically(">=", deploy.Generation))
		g.Expect(deploy.Status.UpdatedReplicas).To(Equal(replicas))
		g.Expect(deploy.Status.AvailableReplicas).To(Equal(replicas))
	}, timeout, PollInterval).Should(Succeed())
}

// WaitReplicaCount waits until the pod count for the app label matches replicas.
func (e *Env) WaitReplicaCount(ctx context.Context, namespace, app string, replicas int) {
	Eventually(func(g Gomega) int {
		var pods corev1.PodList
		g.Expect(e.Client.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
			controllerutil.LabelAppKey: app,
		})).To(Succeed())

		return len(pods.Items)
	}).WithTimeout(time.Minute).WithPolling(PollInterval).Should(Equal(replicas))
}

// CheckPodReady returns true when the pod's Ready condition is true.
func CheckPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// CheckUpdateOrder lists StatefulSets for the given app and validates rolling update invariants:
// 1. Updated StatefulSets form a contiguous group from the highest replica ID
// 2. At most one StatefulSet has zero ready replicas (the one currently being updated).
func (e *Env) CheckUpdateOrder(ctx context.Context, selector *client.ListOptions, replicaLabel, stsRev string) error {
	var stsList appsv1.StatefulSetList
	Expect(e.Client.List(ctx, &stsList, selector)).To(Succeed())

	if len(stsList.Items) < 2 {
		return nil
	}

	notReadyCount := 0
	updated := make([]bool, len(stsList.Items))

	for _, sts := range stsList.Items {
		index, err := strconv.Atoi(sts.Labels[replicaLabel])
		Expect(err).NotTo(HaveOccurred())

		if sts.Status.ReadyReplicas != 1 {
			notReadyCount++
		}

		updated[index] = controllerutil.GetSpecHashFromObject(&sts) == stsRev
	}

	if notReadyCount > 1 {
		return fmt.Errorf("%d replicas not ready, expected at most 1", notReadyCount)
	}

	// The controller updates the highest-index replica first.
	// If it doesn't match the target revisions, either the rollout hasn't started
	// or the revisions are stale (cluster status read before the STS list) — skip.
	if !updated[len(updated)-1] {
		return nil
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
			return fmt.Errorf("replica %d updated before %d", updatedID, i)
		}
	}

	return nil
}

func expectConditionsTrue(g Gomega, conditions []metav1.Condition, types ...v1.ConditionType) {
	for _, conditionType := range types {
		cond := meta.FindStatusCondition(conditions, conditionType)
		g.Expect(cond).ToNot(BeNil())
		g.Expect(cond.Status).To(
			Equal(metav1.ConditionTrue),
			fmt.Sprintf("condition %s is false: %s", cond.Type, cond.Message),
		)
	}
}
