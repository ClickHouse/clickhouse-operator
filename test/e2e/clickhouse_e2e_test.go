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
	"context"
	"fmt"
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
	ClickHouseBaseVersion   = "25.3"
	ClickHouseUpdateVersion = "25.5"
)

var _ = Describe("ClickHouse controller", Label("clickhouse"), func() {
	When("clickhouse with single keeper", func() {
		var keeper v1.KeeperCluster

		BeforeEach(func() {
			keeper = v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("clickhouse-test-%d", rand.Uint32()),
				},
				Spec: v1.KeeperClusterSpec{
					// Use standalone keeper for ClickHouse tests to save resources in CI
					Replicas:            ptr.To[int32](1),
					DataVolumeClaimSpec: defaultStorage,
				},
			}
			Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
			WaitKeeperUpdatedAndReady(&keeper, 2*time.Minute)
		})

		AfterEach(func() {
			Expect(k8sClient.Get(ctx, keeper.NamespacedName(), &keeper)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &keeper)).To(Succeed())
		})

		DescribeTable("standalone ClickHouse updates", func(specUpdate v1.ClickHouseClusterSpec) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("test-%d", rand.Uint32()),
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: ptr.To[int32](1),
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: ClickHouseBaseVersion,
						},
					},
					DataVolumeClaimSpec: defaultStorage,
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
				},
			}
			checks := 0

			By("creating cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			DeferCleanup(func() {
				By("deleting cluster CR")
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			})
			WaitClickHouseUpdatedAndReady(&cr, time.Minute)
			ClickHouseRWChecks(&cr, &checks)

			By("updating cluster CR")
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			Expect(util.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
			cr.Spec = specUpdate
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

			WaitClickHouseUpdatedAndReady(&cr, 3*time.Minute)
			ClickHouseRWChecks(&cr, &checks)
		},
			Entry("update log level", v1.ClickHouseClusterSpec{Settings: v1.ClickHouseConfig{
				Logger: v1.LoggerConfig{Level: "warning"},
			}}),
			Entry("update server settings", v1.ClickHouseClusterSpec{Settings: v1.ClickHouseConfig{
				ExtraConfig: runtime.RawExtension{Raw: []byte(`{"background_pool_size": 20}`)},
			}}),
			Entry("upgrade version", v1.ClickHouseClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{Tag: ClickHouseUpdateVersion},
			}}),
			Entry("scale up to 2 replicas", v1.ClickHouseClusterSpec{Replicas: ptr.To[int32](2)}),
		)

		DescribeTable("ClickHouse cluster updates", func(baseReplicas int, specUpdate v1.ClickHouseClusterSpec) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("test-%d", rand.Uint32()),
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: ptr.To(int32(baseReplicas)),
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: ClickHouseBaseVersion,
						},
					},
					DataVolumeClaimSpec: defaultStorage,
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
				},
			}
			checks := 0

			By("creating cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			DeferCleanup(func() {
				By("deleting cluster CR")
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			})
			WaitClickHouseUpdatedAndReady(&cr, 2*time.Minute)
			ClickHouseRWChecks(&cr, &checks)

			// TODO ensure updates one-by-one
			By("updating cluster CR")
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			Expect(util.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
			cr.Spec = specUpdate
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

			WaitClickHouseUpdatedAndReady(&cr, 5*time.Minute)
			ClickHouseRWChecks(&cr, &checks)
		},
			Entry("update log level", 3, v1.ClickHouseClusterSpec{Settings: v1.ClickHouseConfig{
				Logger: v1.LoggerConfig{Level: "warning"},
			}}),
			Entry("update server settings", 3, v1.ClickHouseClusterSpec{Settings: v1.ClickHouseConfig{
				ExtraConfig: runtime.RawExtension{Raw: []byte(`{"background_pool_size": 20}`)},
			}}),
			Entry("upgrade version", 3, v1.ClickHouseClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{Tag: ClickHouseUpdateVersion},
			}}),
			Entry("scale up to 3 replicas", 2, v1.ClickHouseClusterSpec{Replicas: ptr.To[int32](3)}),
			Entry("scale down to 2 replicas", 3, v1.ClickHouseClusterSpec{Replicas: ptr.To[int32](2)}),
		)
	})

	Describe("secure cluster with secure keeper", func() {
		suffix := rand.Uint32()
		issuer := fmt.Sprintf("issuer-%d", suffix)

		keeperCertName := fmt.Sprintf("keeper-cert-%d", suffix)
		chCertName := fmt.Sprintf("ch-cert-%d", suffix)

		keeperCR := &v1.KeeperCluster{
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
				DataVolumeClaimSpec: defaultStorage,
				Settings: v1.KeeperConfig{
					TLS: v1.ClusterTLSSpec{
						Enabled:  true,
						Required: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: keeperCertName,
						},
					},
				},
			},
		}

		keeperCert := &certv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-cert-%d", suffix),
			},
			Spec: certv1.CertificateSpec{
				IssuerRef: mcertv1.ObjectReference{
					Name: issuer,
					Kind: "Issuer",
				},
				SecretName: keeperCertName,
				DNSNames: []string{
					fmt.Sprintf("*.%s.%s.svc", keeperCR.HeadlessServiceName(), keeperCR.Namespace),
					fmt.Sprintf("*.%s.%s.svc.cluster.local", keeperCR.HeadlessServiceName(), keeperCR.Namespace),
				},
			},
		}

		cr := &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()),
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas: ptr.To[int32](2),
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: keeperCR.Name,
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
				DataVolumeClaimSpec: defaultStorage,
				Settings: v1.ClickHouseConfig{
					TLS: v1.ClusterTLSSpec{
						Enabled:  true,
						Required: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: chCertName,
						},
					},
				},
			},
		}

		chCert := &certv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("ch-cert-%d", suffix),
			},
			Spec: certv1.CertificateSpec{
				IssuerRef: mcertv1.ObjectReference{
					Name: issuer,
					Kind: "Issuer",
				},
				SecretName: chCertName,
				DNSNames: []string{
					fmt.Sprintf("*.%s.%s.svc", cr.HeadlessServiceName(), cr.Namespace),
					fmt.Sprintf("*.%s.%s.svc.cluster.local", cr.HeadlessServiceName(), cr.Namespace),
				},
			},
		}

		It("should create secure cluster", func() {
			By("issuing certificates")
			utils.SetupCA(ctx, k8sClient, testNamespace, suffix)
			Expect(k8sClient.Create(ctx, keeperCert)).To(Succeed())
			Expect(k8sClient.Create(ctx, chCert)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, keeperCert)).To(Succeed())
				Expect(k8sClient.Delete(ctx, chCert)).To(Succeed())
			})
			By("creating keeper")
			Expect(k8sClient.Create(ctx, keeperCR)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, keeperCR)).To(Succeed())
			})
			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitKeeperUpdatedAndReady(keeperCR, 2*time.Minute)
			WaitClickHouseUpdatedAndReady(cr, 2*time.Minute)
			ClickHouseRWChecks(cr, ptr.To(0))
		})
	})
})

func WaitClickHouseUpdatedAndReady(cr *v1.ClickHouseCluster, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.Name))
	EventuallyWithOffset(1, func() bool {
		var cluster v1.ClickHouseCluster
		ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())
		return cluster.Generation == cluster.Status.ObservedGeneration &&
			cluster.Status.CurrentRevision == cluster.Status.UpdateRevision &&
			cluster.Status.ReadyReplicas == cluster.Replicas()
	}, timeout).Should(BeTrue())
	// Needed for replica deletion to not forward deleting pods.
	By(fmt.Sprintf("waiting for cluster %s replicas count match", cr.Name))
	count := int(cr.Replicas() * cr.Shards())
	ExpectWithOffset(1, utils.WaitReplicaCount(ctx, k8sClient, cr.Namespace, cr.SpecificName(), count)).To(Succeed())
}

func ClickHouseRWChecks(cr *v1.ClickHouseCluster, checksDone *int) {
	ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")
	client, err := utils.NewClickHouseClient(ctx, k8sClient, cr)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer client.Close()

	By("writing new test data")
	ExpectWithOffset(1, client.CheckWrite(ctx, *checksDone)).To(Succeed())
	*checksDone++

	By("reading all test data")
	for i := range *checksDone {
		ExpectWithOffset(1, client.CheckRead(ctx, i)).To(Succeed(), "check read %d failed", i)
	}
}
