package testutil

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

// clusterStatus is a snapshot of the status fields shared by both cluster kinds.
type clusterStatus struct {
	observedGeneration int64
	currentRevision    string
	updateRevision     string
	readyReplicas      int32
	conditions         []metav1.Condition
}

func statusOf(cluster client.Object) clusterStatus {
	switch cr := cluster.(type) {
	case *v1.ClickHouseCluster:
		return clusterStatus{
			observedGeneration: cr.Status.ObservedGeneration,
			currentRevision:    cr.Status.CurrentRevision,
			updateRevision:     cr.Status.UpdateRevision,
			readyReplicas:      cr.Status.ReadyReplicas,
			conditions:         cr.Status.Conditions,
		}

	case *v1.KeeperCluster:
		return clusterStatus{
			observedGeneration: cr.Status.ObservedGeneration,
			currentRevision:    cr.Status.CurrentRevision,
			updateRevision:     cr.Status.UpdateRevision,
			readyReplicas:      cr.Status.ReadyReplicas,
			conditions:         cr.Status.Conditions,
		}

	default:
		Fail(fmt.Sprintf("unsupported cluster type %T", cluster))
		return clusterStatus{}
	}
}

// expectedReplicas derives the expected replica count from the cluster's current spec.
func expectedReplicas(cluster client.Object) int {
	switch cr := cluster.(type) {
	case *v1.ClickHouseCluster:
		return int(cr.Replicas() * cr.Shards())
	case *v1.KeeperCluster:
		return int(cr.Replicas())
	default:
		Fail(fmt.Sprintf("unsupported cluster type %T", cluster))
		return 0
	}
}

// WaitClickHouseUpdatedAndReady waits until the cluster rollout completes and all replicas are ready.
func (e *Env) WaitClickHouseUpdatedAndReady(ctx context.Context, cr *v1.ClickHouseCluster, timeout time.Duration) {
	GinkgoHelper()

	e.waitUpdatedAndReady(ctx, cr, timeout,
		v1.ConditionTypeReady,
		v1.ConditionTypeHealthy,
		v1.ConditionTypeClusterSizeAligned,
		v1.ConditionTypeConfigurationInSync,
		v1.ClickHouseConditionTypeSchemaInSync,
	)
}

// WaitKeeperUpdatedAndReady waits until the keeper rollout completes and all replicas are ready.
func (e *Env) WaitKeeperUpdatedAndReady(ctx context.Context, cr *v1.KeeperCluster, timeout time.Duration) {
	GinkgoHelper()

	e.waitUpdatedAndReady(ctx, cr, timeout,
		v1.ConditionTypeReady,
		v1.ConditionTypeHealthy,
		v1.ConditionTypeClusterSizeAligned,
		v1.ConditionTypeConfigurationInSync,
	)
}

func (e *Env) waitUpdatedAndReady(
	ctx context.Context, cr client.Object, timeout time.Duration, conditions ...v1.ConditionType,
) {
	GinkgoHelper()

	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.GetName()))
	Eventually(func(g Gomega) {
		cluster, ok := cr.DeepCopyObject().(client.Object)
		g.Expect(ok).To(BeTrue())
		g.Expect(e.Client.Get(ctx, client.ObjectKeyFromObject(cr), cluster)).To(Succeed())

		status := statusOf(cluster)
		replicas := expectedReplicas(cluster)

		g.Expect(cluster.GetGeneration()).To(Equal(status.observedGeneration))
		g.Expect(status.currentRevision).
			To(Equal(status.updateRevision), "rollout should eventually complete")
		g.Expect(status.readyReplicas).
			To(BeEquivalentTo(replicas), "all replicas should be ready")

		// Conditions and status are the operator's contract with the user: trust
		// them here so the RW checks that follow validate they are not lying.
		expectConditionsTrue(g, status.conditions, conditions...)
	}, timeout).WithPolling(PollInterval).Should(Succeed())
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
		g.Expect(deploy.Status.Replicas).To(Equal(replicas))
		g.Expect(deploy.Status.UpdatedReplicas).To(Equal(replicas))
		g.Expect(deploy.Status.AvailableReplicas).To(Equal(replicas))
	}, timeout, PollInterval).Should(Succeed())
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
