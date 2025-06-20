/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"fmt"
	"strconv"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	mcertv1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	"github.com/clickhouse-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/exp/rand"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

const (
	KeeperBaseVersion   = "25.3"
	KeeperUpdateVersion = "25.5"
)

var _ = Describe("Keeper controller", func() {
	DescribeTable("standalone keeper updates", func(specUpdate v1.KeeperClusterSpec) {
		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()),
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
			},
		}
		checks := 0

		By("creating cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		defer func() {
			By("deleting cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		}()
		WaitUpdatedAndReady(&cr, time.Minute)
		RWChecks(&cr, &checks)

		By("updating cluster CR")
		Expect(k8sClient.Get(ctx, cr.GetNamespacedName(), &cr)).To(Succeed())
		Expect(util.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
		cr.Spec = specUpdate
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

		WaitUpdatedAndReady(&cr, 3*time.Minute)
		RWChecks(&cr, &checks)
	},
		Entry("update log level", v1.KeeperClusterSpec{Settings: v1.KeeperConfig{
			Logger: v1.LoggerConfig{Level: "warning"},
		}}),
		Entry("update coordination settings", v1.KeeperClusterSpec{Settings: v1.KeeperConfig{
			ExtraConfig: runtime.RawExtension{Raw: []byte(`{"keeper_server": {
				"coordination_settings":{"quorum_reads": true}}}`,
			)},
		}}),
		Entry("upgrade version", v1.KeeperClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
			Image: v1.ContainerImage{Tag: KeeperUpdateVersion},
		}}),
		Entry("scale up to 3 replicas", v1.KeeperClusterSpec{Replicas: ptr.To[int32](3)}),
	)

	DescribeTable("keeper cluster updates", func(baseReplicas int, specUpdate v1.KeeperClusterSpec) {
		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()),
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To(int32(baseReplicas)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
			},
		}
		checks := 0

		By("creating cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		defer func() {
			By("deleting cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		}()
		WaitUpdatedAndReady(&cr, 2*time.Minute)
		RWChecks(&cr, &checks)

		// TODO ensure updates one-by-one
		By("updating cluster CR")
		Expect(k8sClient.Get(ctx, cr.GetNamespacedName(), &cr)).To(Succeed())
		Expect(util.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
		cr.Spec = specUpdate
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

		WaitUpdatedAndReady(&cr, 5*time.Minute)
		RWChecks(&cr, &checks)
	},
		Entry("update log level", 3, v1.KeeperClusterSpec{Settings: v1.KeeperConfig{
			Logger: v1.LoggerConfig{Level: "warning"},
		}}),
		Entry("update coordination settings", 3, v1.KeeperClusterSpec{Settings: v1.KeeperConfig{
			ExtraConfig: runtime.RawExtension{Raw: []byte(`{"keeper_server": {
				"coordination_settings":{"quorum_reads": true}}}`,
			)},
		}}),
		Entry("upgrade version", 3, v1.KeeperClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
			Image: v1.ContainerImage{Tag: KeeperUpdateVersion},
		}}),
		Entry("scale up to 5 replicas", 3, v1.KeeperClusterSpec{Replicas: ptr.To[int32](5)}),
		Entry("scale down to 3 replicas", 5, v1.KeeperClusterSpec{Replicas: ptr.To[int32](3)}),
	)

	Describe("secure keeper cluster", func() {
		suffix := rand.Uint32()
		certName := fmt.Sprintf("keeper-cert-%d", suffix)

		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()),
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](3),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
				Settings: v1.KeeperConfig{
					TLS: v1.ClusterTLSSpec{
						Enabled:  true,
						Required: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: certName,
						},
					},
				},
			},
		}

		issuer := &certv1.Issuer{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-test-issuer-%d", suffix),
			},
			Spec: certv1.IssuerSpec{
				IssuerConfig: certv1.IssuerConfig{
					SelfSigned: &certv1.SelfSignedIssuer{},
				},
			},
		}

		var hostnames []string
		for i := 0; i < int(cr.Replicas()); i++ {
			hostnames = append(hostnames, cr.HostnameById(strconv.Itoa(i)))
		}

		cert := &certv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-cert-%d", suffix),
			},
			Spec: certv1.CertificateSpec{
				IssuerRef: mcertv1.ObjectReference{
					Kind: "Issuer",
					Name: issuer.Name,
				},
				DNSNames:   hostnames,
				SecretName: certName,
			},
		}

		It("should create secure cluster", func() {
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
				Expect(k8sClient.Delete(ctx, cert)).To(Succeed())
				Expect(k8sClient.Delete(ctx, issuer)).To(Succeed())
			})

			By("creating certificate")
			Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
			Expect(k8sClient.Create(ctx, cert)).To(Succeed())
			By("creating secure keeper cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			By("ensuring secure port is working")
			WaitUpdatedAndReady(&cr, 2*time.Minute)
			RWChecks(&cr, ptr.To(0))
		})
	})
})

func WaitUpdatedAndReady(cr *v1.KeeperCluster, timeout time.Duration) {
	By("waiting for cluster to be ready")
	Eventually(func() bool {
		var cluster v1.KeeperCluster
		Expect(k8sClient.Get(ctx, cr.GetNamespacedName(), &cluster)).To(Succeed())
		return cluster.Generation == cluster.Status.ObservedGeneration &&
			cluster.Status.CurrentRevision == cluster.Status.UpdateRevision &&
			cluster.Status.ReadyReplicas == cluster.Replicas()
	}, timeout).Should(BeTrue())
}

func RWChecks(cr *v1.KeeperCluster, checksDone *int) {
	Expect(k8sClient.Get(ctx, cr.GetNamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")
	client, err := utils.NewKeeperClient(ctx, cr)
	Expect(err).NotTo(HaveOccurred())
	defer client.Close()

	By("writing new test data")
	Expect(client.CheckWrite(*checksDone)).To(Succeed())
	*checksDone++

	By("reading all test data")
	for i := range *checksDone {
		Expect(client.CheckRead(i)).To(Succeed(), "check read %d failed", i)
	}
}
