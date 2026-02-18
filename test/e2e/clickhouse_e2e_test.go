package e2e

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	mcertv1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	gcmp "github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller/clickhouse"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	ClickHouseBaseVersion   = "25.12"
	ClickHouseUpdateVersion = "26.1"
)

var _ = Describe("ClickHouse controller", Label("clickhouse"), func() {
	When("manage clickhouse with single keeper", func() {
		var keeper v1.KeeperCluster

		BeforeEach(func(ctx context.Context) {
			keeper = v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("clickhouse-test-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.KeeperClusterSpec{
					// Use standalone keeper for ClickHouse tests to save resources in CI
					Replicas:            ptr.To[int32](1),
					DataVolumeClaimSpec: &defaultStorage,
				},
			}
			Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
			WaitKeeperUpdatedAndReady(ctx, &keeper, 2*time.Minute, false)
		})

		AfterEach(func(ctx context.Context) {
			Expect(k8sClient.Get(ctx, keeper.NamespacedName(), &keeper)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &keeper)).To(Succeed())
		})

		DescribeTable("standalone ClickHouse updates", func(ctx context.Context, specUpdate v1.ClickHouseClusterSpec) {
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
					DataVolumeClaimSpec: &defaultStorage,
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
				},
			}
			checks := 0

			By("creating cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				By("deleting cluster CR")
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			})
			WaitClickHouseUpdatedAndReady(ctx, &cr, time.Minute, false)
			ClickHouseRWChecks(ctx, &cr, &checks)

			By("updating cluster CR")
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			Expect(controllerutil.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
			cr.Spec = specUpdate
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

			WaitClickHouseUpdatedAndReady(ctx, &cr, 3*time.Minute, true)
			ClickHouseRWChecks(ctx, &cr, &checks)
		},
			Entry("update log level", v1.ClickHouseClusterSpec{Settings: v1.ClickHouseSettings{
				Logger: v1.LoggerConfig{Level: "warning"},
			}}),
			Entry("update server settings", v1.ClickHouseClusterSpec{Settings: v1.ClickHouseSettings{
				ExtraConfig: runtime.RawExtension{Raw: []byte(`{"background_pool_size": 20}`)},
			}}),
			Entry("upgrade version", v1.ClickHouseClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{Tag: ClickHouseUpdateVersion},
			}}),
			Entry("scale up to 2 replicas", v1.ClickHouseClusterSpec{Replicas: ptr.To[int32](2)}),
		)

		DescribeTable("ClickHouse cluster updates", func(
			ctx context.Context,
			baseReplicas int,
			specUpdate v1.ClickHouseClusterSpec,
		) {
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
					DataVolumeClaimSpec: &defaultStorage,
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
				},
			}
			checks := 0

			By("creating cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				By("deleting cluster CR")
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			})
			WaitClickHouseUpdatedAndReady(ctx, &cr, 2*time.Minute, false)
			ClickHouseRWChecks(ctx, &cr, &checks)

			By("updating cluster CR")
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			Expect(controllerutil.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
			cr.Spec = specUpdate
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

			WaitClickHouseUpdatedAndReady(ctx, &cr, 5*time.Minute, true)
			ClickHouseRWChecks(ctx, &cr, &checks)
		},
			Entry("update log level", 3, v1.ClickHouseClusterSpec{Settings: v1.ClickHouseSettings{
				Logger: v1.LoggerConfig{Level: "warning"},
			}}),
			Entry("update server settings", 3, v1.ClickHouseClusterSpec{Settings: v1.ClickHouseSettings{
				ExtraConfig: runtime.RawExtension{Raw: []byte(`{"background_pool_size": 20}`)},
			}}),
			Entry("upgrade version", 3, v1.ClickHouseClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{Tag: ClickHouseUpdateVersion},
			}}),
			Entry("scale up to 3 replicas", 2, v1.ClickHouseClusterSpec{Replicas: ptr.To[int32](3)}),
			Entry("scale down to 2 replicas", 3, v1.ClickHouseClusterSpec{Replicas: ptr.To[int32](2)}),
		)

		It("should work with custom data folder mount", func(ctx context.Context) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testNamespace,
					Name:      fmt.Sprintf("custom-disk-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas:            ptr.To[int32](1),
					DataVolumeClaimSpec: nil, // Diskless configuration
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
					PodTemplate: v1.PodTemplateSpec{
						Volumes: []corev1.Volume{{
							Name: "custom-data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						}},
					},
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: ClickHouseBaseVersion,
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "custom-data",
							MountPath: "/var/lib/clickhouse",
						}},
					},
				},
			}

			By("creating diskless ClickHouse cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				By("deleting diskless ClickHouse cluster CR")
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			})

			WaitClickHouseUpdatedAndReady(ctx, &cr, 2*time.Minute, false)
			ClickHouseRWChecks(ctx, &cr, ptr.To(0))
		})
	})

	Describe("is handling TLS settings correctly", Ordered, func() {
		suffix := rand.Uint32() //nolint:gosec
		issuer := fmt.Sprintf("issuer-%d", suffix)

		keeperCertName := fmt.Sprintf("keeper-cert-%d", suffix)
		chCertName := fmt.Sprintf("ch-cert-%d", suffix)

		var (
			keeperCR   *v1.KeeperCluster
			keeperCert *certv1.Certificate
			baseCr     *v1.ClickHouseCluster
			chCert     *certv1.Certificate
		)

		BeforeAll(func(ctx context.Context) {
			testutil.SetupCA(ctx, k8sClient, testNamespace, suffix)
		})

		BeforeEach(func(ctx context.Context) {
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
					DataVolumeClaimSpec: &defaultStorage,
					Settings: v1.KeeperSettings{
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
					IssuerRef: mcertv1.IssuerReference{
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
					DataVolumeClaimSpec: &defaultStorage,
					Settings: v1.ClickHouseSettings{
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
					IssuerRef: mcertv1.IssuerReference{
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
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, keeperCert)).To(Succeed())
			})

			Expect(k8sClient.Create(ctx, chCert)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, chCert)).To(Succeed())
			})

			By("creating keeper")
			Expect(k8sClient.Create(ctx, keeperCR)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, keeperCR)).To(Succeed())
			})
			WaitKeeperUpdatedAndReady(ctx, keeperCR, 2*time.Minute, false)
		})

		It("should use server cert ca bundle to connect to the keeper", func(ctx context.Context) {
			cr := baseCr.DeepCopy()

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitClickHouseUpdatedAndReady(ctx, cr, 2*time.Minute, false)
			ClickHouseRWChecks(ctx, cr, ptr.To(0))
		})

		It("should use custom ca bundle to connect to the keeper", func(ctx context.Context) {
			cr := baseCr.DeepCopy()
			cr.Spec.Settings.TLS = v1.ClusterTLSSpec{
				CABundle: &v1.SecretKeySelector{
					Name: keeperCertName,
				},
			}

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitClickHouseUpdatedAndReady(ctx, cr, 2*time.Minute, false)
			ClickHouseRWChecks(ctx, cr, ptr.To(0))
		})
	})

	Describe("default and management users works", Ordered, func() {
		password := fmt.Sprintf("test-password-%d", rand.Uint32()) //nolint:gosec
		passwordSha := controllerutil.Sha256Hash([]byte(password))
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("default-pass-%d", rand.Uint32()), //nolint:gosec
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"password": []byte(passwordSha),
			},
		}
		auth := clickhouse.Auth{
			Username: "default",
			Password: password,
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
				DataVolumeClaimSpec: &defaultStorage,
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
				DataVolumeClaimSpec: &defaultStorage,
				Settings: v1.ClickHouseSettings{
					DefaultUserPassword: &v1.DefaultPasswordSelector{
						PasswordType: "password_sha256_hex",
						Secret: &v1.SecretKeySelector{
							Name: secret.Name,
							Key:  "password",
						},
					},
					ExtraUsersConfig: runtime.RawExtension{
						Raw: []byte(fmt.Sprintf(`{"users": {"custom": {"password_sha256_hex": "%s"}}}`, passwordSha)),
					},
				},
			},
		}

		checks := 0

		BeforeAll(func(ctx context.Context) {
			By("creating secret")
			Expect(k8sClient.Create(ctx, &secret)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, &secret)).To(Succeed())
			})

			By("creating keeper")
			Expect(k8sClient.Create(ctx, keeperCR)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, keeperCR)).To(Succeed())
			})

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})
			WaitKeeperUpdatedAndReady(ctx, keeperCR, 2*time.Minute, false)
			WaitClickHouseUpdatedAndReady(ctx, cr, 2*time.Minute, false)
		})

		It("should be accessible with default user credentials", func(ctx context.Context) {
			ClickHouseRWChecks(ctx, cr, &checks, auth)
		})

		It("should be accessible with operator management user credentials", func(ctx context.Context) {
			var managementSecret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      cr.SecretName(),
				Namespace: cr.Namespace,
			}, &managementSecret)).To(Succeed())

			ClickHouseRWChecks(ctx, cr, &checks, clickhouse.Auth{
				Username: chctrl.OperatorManagementUsername,
				Password: string(managementSecret.Data[chctrl.SecretKeyManagementPassword]),
			})
		})

		It("should be accessible with custom user credentials", func(ctx context.Context) {
			ClickHouseRWChecks(ctx, cr, &checks, clickhouse.Auth{
				Username: "custom",
				Password: password,
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
				}}}`, auth.Username, controllerutil.Sha256Hash([]byte(auth.Password))),
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
				DataVolumeClaimSpec: &defaultStorage,
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
					Volumes: []corev1.Volume{
						{
							Name: "custom-user",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: customConfigMap.Name,
									},
									Items: []corev1.KeyToPath{
										{
											Key:  "user.yaml",
											Path: "custom.yaml",
										},
									},
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
						},
					},
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
				DataVolumeClaimSpec: &defaultStorage,
			},
		}

		checks := 0

		It("should mount custom configmap", func(ctx context.Context) {
			By("creating keeper")
			Expect(k8sClient.Create(ctx, keeperCR)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, keeperCR)).To(Succeed())
			})

			By("creating custom configmap")
			Expect(k8sClient.Create(ctx, &customConfigMap)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, &customConfigMap)).To(Succeed())
			})

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitKeeperUpdatedAndReady(ctx, keeperCR, 2*time.Minute, false)
			WaitClickHouseUpdatedAndReady(ctx, cr, 2*time.Minute, false)

			By("checking custom user access works")
			ClickHouseRWChecks(ctx, cr, &checks, auth)

			chClient, err := testutil.NewClickHouseClient(ctx, config, cr, auth)
			Expect(err).NotTo(HaveOccurred())

			defer chClient.Close()

			var maxTableSizeToDrop string

			query := "SELECT value FROM system.server_settings WHERE name = 'max_table_size_to_drop'"

			By("checking custom setting applied")
			Expect(chClient.QueryRow(ctx, query, &maxTableSizeToDrop)).To(Succeed())
			Expect(maxTableSizeToDrop).To(Equal("7"))
		})
	})

	Describe("default affinity settings works", Ordered, func() {
		nodeToZone := map[string]string{}
		keeperName := fmt.Sprintf("test-%d", rand.Uint32()) //nolint:gosec
		keeperZones := map[string]struct{}{}
		keeper := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      keeperName,
			},
			Spec: v1.KeeperClusterSpec{
				Replicas:            ptr.To[int32](3),
				DataVolumeClaimSpec: &defaultStorage,
				PodTemplate: v1.PodTemplateSpec{
					TopologyZoneKey: ptr.To("topology.kubernetes.io/zone"),
					NodeHostnameKey: ptr.To("kubernetes.io/hostname"),
				},
			},
		}
		cluster := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      keeperName,
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:            ptr.To[int32](3),
				DataVolumeClaimSpec: &defaultStorage,
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: keeperName,
				},
				PodTemplate: v1.PodTemplateSpec{
					TopologyZoneKey: ptr.To("topology.kubernetes.io/zone"),
					NodeHostnameKey: ptr.To("kubernetes.io/hostname"),
				},
			},
		}

		It("should be executed on cluster with at least 3 nodes in different zones", func(ctx context.Context) {
			var nodes corev1.NodeList
			Expect(k8sClient.List(ctx, &nodes)).To(Succeed())
			Expect(len(nodes.Items)).To(BeNumerically(">=", 3), "Too few nodes in the cluster to test affinity")

			zones := map[string]struct{}{}
			for _, node := range nodes.Items {
				if zone, ok := node.Labels["topology.kubernetes.io/zone"]; ok {
					zones[zone] = struct{}{}
					nodeToZone[node.Name] = zone
				} else {
					GinkgoWriter.Printf("Node %s has no zone label\n", node.Name)
				}
			}

			Expect(len(zones)).To(BeNumerically(">=", 3), "Too few zones in the cluster to test affinity")
		})

		It("should create keeper with default affinity settings", func(ctx context.Context) {
			By("creating keeper cluster")
			Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
			WaitKeeperUpdatedAndReady(ctx, &keeper, 2*time.Minute, false)

			By("checking keeper pod affinity")

			var pods corev1.PodList
			Expect(k8sClient.List(ctx, &pods, client.InNamespace(testNamespace),
				client.MatchingLabels{controllerutil.LabelAppKey: keeper.SpecificName()})).To(Succeed())
			Expect(pods.Items).To(HaveLen(int(keeper.Replicas())))

			for _, pod := range pods.Items {
				zone, ok := nodeToZone[pod.Spec.NodeName]
				Expect(ok).To(BeTrue(), "Keeper pod %s on node %s without zone label", pod.Name, pod.Spec.NodeName)

				keeperZones[zone] = struct{}{}
				affinity := pod.Spec.Affinity
				Expect(affinity.PodAntiAffinity).NotTo(BeNil())
				Expect(affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution).NotTo(BeEmpty())
				Expect(pod.Spec.TopologySpreadConstraints).NotTo(BeEmpty())
			}
		})

		It("should create ClickHouse with default affinity settings", func(ctx context.Context) {
			By("creating clickhouse cluster")
			Expect(k8sClient.Create(ctx, &cluster)).To(Succeed())
			WaitClickHouseUpdatedAndReady(ctx, &cluster, 2*time.Minute, false)

			By("checking clickhouse pod affinity")

			var pods corev1.PodList
			Expect(k8sClient.List(ctx, &pods, client.InNamespace(testNamespace),
				client.MatchingLabels{controllerutil.LabelAppKey: cluster.SpecificName()})).To(Succeed())
			Expect(pods.Items).To(HaveLen(int(cluster.Replicas())))

			zones := map[string]struct{}{}
			for _, pod := range pods.Items {
				zone, ok := nodeToZone[pod.Spec.NodeName]
				Expect(ok).To(BeTrue(), "ClickHouse pod %s on node %s without zone label", pod.Name, pod.Spec.NodeName)

				zones[zone] = struct{}{}
				affinity := pod.Spec.Affinity
				Expect(affinity.PodAffinity).NotTo(BeNil())
				Expect(affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution).NotTo(BeEmpty())
				Expect(affinity.PodAntiAffinity).NotTo(BeNil())
				Expect(affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution).NotTo(BeEmpty())
				Expect(pod.Spec.TopologySpreadConstraints).NotTo(BeEmpty())
				Expect(keeperZones).To(HaveKey(zone), "ClickHouse pod %s in zone %s without keeper", pod.Name, zone)
			}
		})

		AfterAll(func(ctx context.Context) {
			if cluster.UID != "" {
				By("deleting clickhouse cluster")
				Expect(k8sClient.Delete(ctx, &cluster)).To(Succeed())
			}

			if keeper.UID != "" {
				By("deleting keeper cluster")
				Expect(k8sClient.Delete(ctx, &keeper)).To(Succeed())
			}
		})
	})

	It("should generate correct sharded cluster configuration", func(ctx context.Context) {
		keeper := createTestKeeperCluster(ctx, 1)
		WaitKeeperUpdatedAndReady(ctx, keeper, 2*time.Minute, false)

		password := fmt.Sprintf("test-password-%d", rand.Uint32()) //nolint:gosec
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("default-pass-%d", rand.Uint32()), //nolint:gosec
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"password_sha": []byte(controllerutil.Sha256Hash([]byte(password))),
			},
		}
		Expect(k8sClient.Create(ctx, &secret)).To(Succeed())

		ch := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas: ptr.To[int32](2),
				Shards:   ptr.To[int32](2),
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: keeper.Name,
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: ClickHouseBaseVersion,
					},
				},
				DataVolumeClaimSpec: &defaultStorage,
				Settings: v1.ClickHouseSettings{
					DefaultUserPassword: &v1.DefaultPasswordSelector{
						PasswordType: "password_sha256_hex",
						Secret: &v1.SecretKeySelector{
							Name: secret.Name,
							Key:  "password_sha",
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &ch)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &ch)).To(Succeed())
		})
		WaitClickHouseUpdatedAndReady(ctx, &ch, 2*time.Minute, false)

		chClient, err := testutil.NewClickHouseClient(ctx, config, &ch, clickhouse.Auth{
			Username: "default",
			Password: password,
		})
		Expect(err).NotTo(HaveOccurred())

		defer chClient.Close()

		By("creating schema and inserting data")

		setupQueries := `
			CREATE DATABASE test_dist ON CLUSTER default Engine=Replicated;
			CREATE TABLE test_dist.data (id Int32) ENGINE = ReplicatedMergeTree() ORDER BY id;
			CREATE TABLE test_dist.dist (id Int32) ENGINE = Distributed(default, test_dist, data, id%2);
			INSERT INTO test_dist.dist SELECT number FROM numbers(10)`
		for q := range strings.SplitSeq(setupQueries, ";") {
			Expect(chClient.Exec(ctx, q)).To(Succeed())
		}

		By("ensuring data is not duplicated and remote hosts could be queried")
		Eventually(func() string {
			rows, err := chClient.Query(ctx, "SELECT getMacro('shard') shard, sum(id) sum FROM test_dist.dist GROUP BY shard")

			Expect(err).NotTo(HaveOccurred())
			defer func() {
				Expect(rows.Close()).To(Succeed())
			}()

			response := map[string]int64{}
			for rows.Next() {
				var (
					shard string
					sum   int64
				)

				Expect(rows.Scan(&shard, &sum)).To(Succeed())
				response[shard] = sum
			}

			return gcmp.Diff(response, map[string]int64{
				"0": 20,
				"1": 25,
			})
		}, "10s").Should(BeEmpty())
	})
})

func WaitClickHouseUpdatedAndReady(
	ctx context.Context,
	cr *v1.ClickHouseCluster,
	timeout time.Duration,
	isUpdate bool,
) {
	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.Name))
	EventuallyWithOffset(1, func() bool {
		var cluster v1.ClickHouseCluster
		ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())

		if cluster.Generation != cluster.Status.ObservedGeneration ||
			cluster.Status.CurrentRevision != cluster.Status.UpdateRevision ||
			cluster.Status.ReadyReplicas != cluster.Replicas()*cluster.Shards() {
			return false
		}

		for _, cond := range cluster.Status.Conditions {
			if cond.Status != metav1.ConditionTrue {
				return false
			}
		}

		if isUpdate {
			for shardID := range cluster.Shards() {
				CheckClickHouseUpdateOrder(ctx, cluster, shardID)
			}
		}

		return true
	}, timeout).Should(BeTrue())
	// Needed for replica deletion to not forward deleting pods.
	By(fmt.Sprintf("waiting for cluster %s replicas count match", cr.Name))
	count := int(cr.Replicas() * cr.Shards())
	ExpectWithOffset(1, testutil.WaitReplicaCount(ctx, k8sClient, cr.Namespace, cr.SpecificName(), count)).To(Succeed())
	By(fmt.Sprintf("waiting for cluster %s all replicas ready", cr.Name))
	EventuallyWithOffset(1, func() bool {
		var pods corev1.PodList
		ExpectWithOffset(2, k8sClient.List(ctx, &pods, client.InNamespace(testNamespace),
			client.MatchingLabels{controllerutil.LabelAppKey: cr.SpecificName()})).To(Succeed())

		for _, pod := range pods.Items {
			if !CheckPodReady(&pod) {
				return false
			}
		}

		return true
	}).Should(BeTrue())
}

func ClickHouseRWChecks(ctx context.Context, cr *v1.ClickHouseCluster, checksDone *int, auth ...clickhouse.Auth) {
	ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")
	Expect(len(auth)).To(Or(Equal(0), Equal(1)))
	chClient, err := testutil.NewClickHouseClient(ctx, config, cr, auth...)
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

// Validates that updates are applied in the correct order and for single replica at a time.
// Allows to the single replica to be in updating state (not ready but updated).
// Which id must be between the latest not updated and earliest updated replicas.
func CheckClickHouseUpdateOrder(ctx context.Context, cluster v1.ClickHouseCluster, shard int32) {
	var pods corev1.PodList
	Expect(k8sClient.List(ctx, &pods, controllerutil.AppRequirements(cluster.Namespace, cluster.SpecificName()),
		client.MatchingLabels{controllerutil.LabelClickHouseShardID: strconv.FormatInt(int64(shard), 10)})).To(Succeed())

	maxNotUpdated := int32(-1)
	minUpdated := int32(math.MaxInt32)

	updatingReplica := int32(-1)
	for _, pod := range pods.Items {
		replicaID, err := v1.ClickHouseIDFromLabels(pod.Labels)
		Expect(err).NotTo(HaveOccurred())
		Expect(replicaID.ShardID).To(Equal(shard))

		ready := CheckPodReady(&pod)
		updated := CheckReplicaUpdated(
			ctx,
			cluster.ConfigMapNameByReplicaID(replicaID),
			cluster.Status.ConfigurationRevision,
			cluster.StatefulSetNameByReplicaID(replicaID),
			cluster.Status.StatefulSetRevision,
		)

		switch {
		// Replica waiting for update
		case !updated && ready:
			maxNotUpdated = max(maxNotUpdated, replicaID.Index)
		// Broken before update
		case !updated:
			Fail(fmt.Sprintf("pod %q is broken before update", pod.Name))
		// Not ready after update, allow one
		case !ready:
			Expect(updatingReplica).To(Equal(int32(-1)),
				"shard %d more than one replica is updating: %d and %d",
				shard, updatingReplica, replicaID.Index)
			updatingReplica = replicaID.Index

		// Successfully updated replica
		default:
			minUpdated = min(minUpdated, replicaID.Index)
		}
	}

	formatErr := func(r1 int32, r2 int32) string {
		return fmt.Sprintf("shard %d, replica %d updated before replica %d", shard, r1, r2)
	}

	Expect(maxNotUpdated < minUpdated).To(BeTrue(), formatErr(minUpdated, maxNotUpdated))

	if updatingReplica != -1 {
		Expect(maxNotUpdated < updatingReplica).To(BeTrue(), formatErr(updatingReplica, maxNotUpdated))
		Expect(updatingReplica < minUpdated).To(BeTrue(), formatErr(minUpdated, updatingReplica))
	}
}

func createTestKeeperCluster(ctx context.Context, replicas int32) *v1.KeeperCluster {
	By("creating keeper cluster")

	keeper := &v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNamespace,
			Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
		},
		Spec: v1.KeeperClusterSpec{
			Replicas:            new(replicas),
			DataVolumeClaimSpec: &defaultStorage,
			ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{
					Tag: KeeperBaseVersion,
				},
			},
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, keeper)).To(Succeed())
	DeferCleanup(func(ctx context.Context) {
		ExpectWithOffset(1, k8sClient.Delete(ctx, keeper)).To(Succeed())
	})

	return keeper
}
