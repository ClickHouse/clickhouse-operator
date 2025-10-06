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
	"math/rand/v2"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	mcertv1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/clickhouse-operator/internal/controller/clickhouse"
	"github.com/clickhouse-operator/internal/util"
	"github.com/clickhouse-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ClickHouseBaseVersion   = "25.3"
	ClickHouseUpdateVersion = "25.5"
)

var _ = Describe("ClickHouse controller", Label("clickhouse"), func() {
	When("manage clickhouse with single keeper", func() {
		var keeper v1.KeeperCluster

		BeforeEach(func() {
			keeper = v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("clickhouse-test-%d", rand.Uint32()), //nolint:gosec
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
					Name:      fmt.Sprintf("test-%d", rand.Uint32()), //nolint:gosec
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
					Name:      fmt.Sprintf("test-%d", rand.Uint32()), //nolint:gosec
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

	Describe("is handling TLS settings correctly", Ordered, func() {
		suffix := rand.Uint32() //nolint:gosec
		issuer := fmt.Sprintf("issuer-%d", suffix)

		keeperCertName := fmt.Sprintf("keeper-cert-%d", suffix)
		chCertName := fmt.Sprintf("ch-cert-%d", suffix)

		var keeperCR *v1.KeeperCluster
		var keeperCert *certv1.Certificate
		var baseCr *v1.ClickHouseCluster
		var chCert *certv1.Certificate

		BeforeAll(func() {
			utils.SetupCA(ctx, k8sClient, testNamespace, suffix)
		})

		BeforeEach(func() {
			keeperCR = &v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
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
			keeperCert = &certv1.Certificate{
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
			baseCr = &v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: ptr.To[int32](2),
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeperCR.Name,
					},
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: ClickHouseBaseVersion,
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
			chCert = &certv1.Certificate{
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
						fmt.Sprintf("*.%s.%s.svc", baseCr.HeadlessServiceName(), baseCr.Namespace),
						fmt.Sprintf("*.%s.%s.svc.cluster.local", baseCr.HeadlessServiceName(), baseCr.Namespace),
					},
				},
			}

			By("issuing certificates")

			Expect(k8sClient.Create(ctx, keeperCert)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, keeperCert)).To(Succeed())
			})

			Expect(k8sClient.Create(ctx, chCert)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, chCert)).To(Succeed())
			})

			By("creating keeper")
			Expect(k8sClient.Create(ctx, keeperCR)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, keeperCR)).To(Succeed())
			})
			WaitKeeperUpdatedAndReady(keeperCR, 2*time.Minute)
		})

		It("should use server cert ca bundle to connect to the keeper", func() {
			cr := baseCr.DeepCopy()

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitClickHouseUpdatedAndReady(cr, 2*time.Minute)
			ClickHouseRWChecks(cr, ptr.To(0))
		})

		It("should use custom ca bundle to connect to the keeper", func() {
			cr := baseCr.DeepCopy()
			cr.Spec.Settings.TLS = v1.ClusterTLSSpec{
				CABundle: &v1.SecretKeySelector{
					Name: keeperCertName,
				},
			}

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitClickHouseUpdatedAndReady(cr, 2*time.Minute)
			ClickHouseRWChecks(cr, ptr.To(0))
		})
	})

	Describe("default and management users works", Ordered, func() {
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("default-pass-%d", rand.Uint32()), //nolint:gosec
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"password": []byte(fmt.Sprintf("test-password-%d", rand.Uint32())), //nolint:gosec
			},
		}
		auth := clickhouse.Auth{
			Username: "default",
			Password: string(secret.Data["password"]),
		}

		keeperCR := &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
				DataVolumeClaimSpec: defaultStorage,
			},
		}

		cr := &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
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
					DefaultUserPassword: &v1.SecretKeySelector{
						Name: secret.Name,
						Key:  "password",
					},
				},
			},
		}

		checks := 0

		BeforeAll(func() {
			By("creating secret")
			Expect(k8sClient.Create(ctx, &secret)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, &secret)).To(Succeed())
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
		})

		It("should be accessible with default user credentials", func() {
			ClickHouseRWChecks(cr, &checks, auth)
		})

		It("should be accessible with operator management user credentials", func() {
			var managementSecret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      cr.SecretName(),
				Namespace: cr.Namespace,
			}, &managementSecret)).To(Succeed())

			ClickHouseRWChecks(cr, &checks, clickhouse.Auth{
				Username: chctrl.OperatorManagementUsername,
				Password: string(managementSecret.Data[chctrl.SecretKeyManagementPassword]),
			})
		})
	})

	Describe("custom data mount works", Ordered, func() {
		auth := clickhouse.Auth{
			Username: "custom",
			Password: "test-password",
		}
		customConfigMap := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("custom-config-%d", rand.Uint32()), //nolint:gosec
				Namespace: testNamespace,
			},
			Data: map[string]string{
				"user.yaml": fmt.Sprintf(`{"users": {"%s": {
					"password_sha256_hex": "%s",
					"grants": [{"query": "GRANT ALL ON *.*"}]
				}}}`, auth.Username, util.Sha256Hash([]byte(auth.Password))),
				"config.yaml": `{"max_table_size_to_drop": 7}`,
			},
		}

		keeperCR := &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
				DataVolumeClaimSpec: defaultStorage,
			},
		}

		cr := &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas: ptr.To[int32](2),
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: keeperCR.Name,
				},
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "custom-user",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: customConfigMap.Name,
								},
								Items: []corev1.KeyToPath{{
									Key:  "user.yaml",
									Path: "custom.yaml",
								}},
							},
						},
					},
						{
							Name: "custom-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: customConfigMap.Name,
									},
									Items: []corev1.KeyToPath{{
										Key:  "config.yaml",
										Path: "max_size.yaml",
									}},
								},
							},
						}},
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "custom-user",
						MountPath: "/etc/clickhouse-server/users.d/",
						ReadOnly:  true,
					}, {
						Name:      "custom-config",
						MountPath: "/etc/clickhouse-server/config.d/",
						ReadOnly:  true,
					}},
				},
				DataVolumeClaimSpec: defaultStorage,
			},
		}

		checks := 0

		It("should mount custom configmap", func() {
			By("creating keeper")
			Expect(k8sClient.Create(ctx, keeperCR)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, keeperCR)).To(Succeed())
			})

			By("creating custom configmap")
			Expect(k8sClient.Create(ctx, &customConfigMap)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, &customConfigMap)).To(Succeed())
			})

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitKeeperUpdatedAndReady(keeperCR, 2*time.Minute)
			WaitClickHouseUpdatedAndReady(cr, 2*time.Minute)

			By("checking custom user access works")
			ClickHouseRWChecks(cr, &checks, auth)

			chClient, err := utils.NewClickHouseClient(ctx, config, cr, auth)
			Expect(err).NotTo(HaveOccurred())
			defer chClient.Close()
			var maxTableSizeToDrop string
			query := "SELECT value FROM system.server_settings WHERE name = 'max_table_size_to_drop'"
			By("checking custom setting applied")
			Expect(chClient.QueryRow(ctx, query, &maxTableSizeToDrop)).To(Succeed())
			Expect(maxTableSizeToDrop).To(Equal("7"))
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
		if cluster.Generation != cluster.Status.ObservedGeneration ||
			cluster.Status.CurrentRevision != cluster.Status.UpdateRevision ||
			cluster.Status.ReadyReplicas != cluster.Replicas() {
			return false
		}
		for _, cond := range cluster.Status.Conditions {
			if cond.Status != metav1.ConditionTrue {
				return false
			}
		}

		return true
	}, timeout).Should(BeTrue())
	// Needed for replica deletion to not forward deleting pods.
	By(fmt.Sprintf("waiting for cluster %s replicas count match", cr.Name))
	count := int(cr.Replicas() * cr.Shards())
	ExpectWithOffset(1, utils.WaitReplicaCount(ctx, k8sClient, cr.Namespace, cr.SpecificName(), count)).To(Succeed())
	By(fmt.Sprintf("waiting for cluster %s all replicas ready", cr.Name))
	EventuallyWithOffset(1, func() bool {
		var pods corev1.PodList
		ExpectWithOffset(2, k8sClient.List(ctx, &pods, client.InNamespace(testNamespace),
			client.MatchingLabels{util.LabelAppKey: cr.SpecificName()})).To(Succeed())
		for _, pod := range pods.Items {
			ready := false
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if !ready {
				return false
			}
		}

		return true
	}).Should(BeTrue())
}

func ClickHouseRWChecks(cr *v1.ClickHouseCluster, checksDone *int, auth ...clickhouse.Auth) {
	ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")
	Expect(len(auth)).To(Or(Equal(0), Equal(1)))
	chClient, err := utils.NewClickHouseClient(ctx, config, cr, auth...)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer chClient.Close()

	if *checksDone == 0 {
		By("creating test database")
		Expect(chClient.CreateDatabase(ctx)).To(Succeed())
		By("checking default database replicated")
		Expect(chClient.CheckDefaultDatabasesReplicated(ctx)).To(Succeed())
	}

	By("writing new test data")
	ExpectWithOffset(1, chClient.CheckWrite(ctx, *checksDone)).To(Succeed())
	*checksDone++

	By("reading all test data")
	for i := range *checksDone {
		ExpectWithOffset(1, chClient.CheckRead(ctx, i)).To(Succeed(), "check read %d failed", i)
	}
}
