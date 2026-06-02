package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("ClickHouse NetworkPolicy", Label("clickhouse"), func() {
	var (
		ns     string
		keeper v1.KeeperCluster
	)

	BeforeEach(func(ctx context.Context) {
		ns = testNamespace(ctx)

		keeper = v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      fmt.Sprintf("np-keeper-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas:            new(int32(1)),
				DataVolumeClaimSpec: &defaultStorage,
			},
		}
		Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &keeper)).To(Succeed())
		})

		WaitKeeperUpdatedAndReady(ctx, &keeper, 2*time.Minute, false)
	})

	It("manages and enforces the cluster NetworkPolicy", func(ctx context.Context) {
		cr := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      fmt.Sprintf("np-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:            new(int32(1)),
				ContainerTemplate:   v1.ContainerTemplateSpec{Image: v1.ContainerImage{Tag: BaseVersion}},
				DataVolumeClaimSpec: &defaultStorage,
				KeeperClusterRef:    v1.KeeperClusterReference{Name: keeper.Name},
				NetworkPolicy: &v1.NetworkPolicySpec{
					Enabled: true,
					// Pods labelled role=allowed (in this namespace) may reach the client ports.
					AllowedClients: []networkingv1.NetworkPolicyPeer{{
						PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "allowed"}},
					}},
				},
			},
		}

		By("creating cluster CR with networkPolicy enabled")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) { Expect(k8sClient.Delete(ctx, &cr)).To(Succeed()) })

		npKey := types.NamespacedName{Namespace: ns, Name: cr.SpecificName()}

		By("operator creates the NetworkPolicy with the expected shape")
		Eventually(func(g Gomega) {
			var np networkingv1.NetworkPolicy
			g.Expect(k8sClient.Get(ctx, npKey, &np)).To(Succeed())
			g.Expect(np.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue(controllerutil.LabelAppKey, cr.SpecificName()))
			g.Expect(np.Spec.PolicyTypes).To(Equal([]networkingv1.PolicyType{networkingv1.PolicyTypeIngress}))
			g.Expect(np.Spec.Ingress).To(HaveLen(2)) // intra-cluster + clients
			g.Expect(np.OwnerReferences).To(HaveLen(1))
			g.Expect(np.OwnerReferences[0].Name).To(Equal(cr.Name))
		}).WithTimeout(2 * time.Minute).WithPolling(pollingInterval).Should(Succeed())

		By("waiting until the cluster is ready")
		WaitClickHouseUpdatedAndReady(ctx, &cr, 3*time.Minute, false)

		By("resolving the ClickHouse pod IP")
		var pods corev1.PodList
		Expect(k8sClient.List(ctx, &pods,
			client.InNamespace(ns),
			client.MatchingLabels{controllerutil.LabelAppKey: cr.SpecificName()},
		)).To(Succeed())
		Expect(pods.Items).NotTo(BeEmpty())
		targetIP := pods.Items[0].Status.PodIP
		Expect(targetIP).NotTo(BeEmpty())

		// Positive check: a client matching AllowedClients reaches the native port.
		// This holds with or without an enforcing CNI, so it always runs.
		By("a client matching AllowedClients (role=allowed) can reach the native port")
		allowed := runConnectivityProbe(ctx, ns, "probe-allowed", targetIP, 9000, map[string]string{"role": "allowed"})
		Eventually(probePhase(ctx, allowed)).
			WithTimeout(time.Minute).WithPolling(pollingInterval).
			Should(Equal(corev1.PodSucceeded))

		// Negative checks: ports that must stay closed even for an allowed client.
		// The cluster namespace is always permitted on the client ports (operator access),
		// so in-namespace probes can only be blocked on ports outside that allowance.
		// Blocking only happens under a NetworkPolicy-enforcing CNI (kindnet ignores policies).
		if os.Getenv("NP_ENFORCING_CNI") != "" {
			cases := []struct {
				name string
				port int
			}{
				{name: "metrics port is closed without monitoringPeers", port: 9363},
				{name: "interserver port is not open to clients", port: 9009},
			}

			for i, tc := range cases {
				By("blocked: " + tc.name)

				probe := runConnectivityProbe(ctx, ns, fmt.Sprintf("probe-block-%d", i), targetIP, tc.port, map[string]string{"role": "allowed"})
				Eventually(probePhase(ctx, probe)).
					WithTimeout(time.Minute).WithPolling(pollingInterval).
					Should(Equal(corev1.PodFailed)) // nc -w3 times out -> non-zero exit -> Failed
			}
		}

		By("disabling networkPolicy removes the object")
		Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
		cr.Spec.NetworkPolicy.Enabled = false
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

		Eventually(func() bool {
			var np networkingv1.NetworkPolicy
			return k8serrors.IsNotFound(k8sClient.Get(ctx, npKey, &np))
		}).WithTimeout(time.Minute).WithPolling(pollingInterval).Should(BeTrue())
	})
})

func runConnectivityProbe(ctx context.Context, ns, name, targetIP string, port int, labels map[string]string) *corev1.Pod {
	GinkgoHelper()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c", fmt.Sprintf("nc -z -w3 %s %d", targetIP, port)},
			}},
		},
	}

	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func(ctx context.Context) { _ = k8sClient.Delete(ctx, pod) })

	return pod
}

func probePhase(ctx context.Context, pod *corev1.Pod) func(Gomega) corev1.PodPhase {
	return func(g Gomega) corev1.PodPhase {
		var p corev1.Pod
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &p)).To(Succeed())

		return p.Status.Phase
	}
}
