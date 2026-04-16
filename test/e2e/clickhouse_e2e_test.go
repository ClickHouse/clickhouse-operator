package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path"
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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller/clickhouse"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

var _ = Describe("ClickHouse controller", Label("clickhouse"), func() {
	When("manage clickhouse with single keeper", func() {
		var (
			ns     string
			keeper v1.KeeperCluster
		)

		BeforeEach(func(ctx context.Context) {
			ns = testNamespace(ctx)

			keeper = v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("clickhouse-test-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.KeeperClusterSpec{
					// Use standalone keeper for ClickHouse tests to save resources in CI
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

		DescribeTable("standalone ClickHouse updates", func(ctx context.Context, specUpdate v1.ClickHouseClusterSpec) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("test-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(1)),
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: BaseVersion,
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
			ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			Expect(cr.Status.Version).To(HavePrefix(cr.Spec.ContainerTemplate.Image.Tag))
			ClickHouseRWChecks(ctx, &cr, &checks)
		},
			Entry("update log level", v1.ClickHouseClusterSpec{Settings: v1.ClickHouseSettings{
				Logger: v1.LoggerConfig{Level: "warning"},
			}}),
			Entry("update server settings", v1.ClickHouseClusterSpec{Settings: v1.ClickHouseSettings{
				ExtraConfig: runtime.RawExtension{Raw: []byte(`{"background_pool_size": 20}`)},
			}}),
			Entry("upgrade version", v1.ClickHouseClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
				Image: v1.ContainerImage{Tag: UpdateVersion},
			}}),
			Entry("scale up to 2 replicas", v1.ClickHouseClusterSpec{Replicas: new(int32(2))}),
		)

		DescribeTable("ClickHouse cluster updates", func(
			ctx context.Context,
			baseReplicas int,
			specUpdate v1.ClickHouseClusterSpec,
		) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("test-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(baseReplicas)),
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: BaseVersion,
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
				Image: v1.ContainerImage{Tag: UpdateVersion},
			}}),
			Entry("scale up to 3 replicas", 2, v1.ClickHouseClusterSpec{Replicas: new(int32(3))}),
			Entry("scale down to 2 replicas", 3, v1.ClickHouseClusterSpec{Replicas: new(int32(2))}),
		)

		It("should work with custom data folder mount", func(ctx context.Context) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("custom-disk-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas:            new(int32(1)),
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
							Tag: BaseVersion,
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
			ClickHouseRWChecks(ctx, &cr, new(0))
		})

		It("should recreate stuck pods", func(ctx context.Context) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("custom-disk-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas:            new(int32(1)),
					DataVolumeClaimSpec: nil, // Diskless configuration
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{Tag: BaseVersion},
						Env: []corev1.EnvVar{{
							Name: "BROKEN_REF",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "nonexistent-secret",
									},
									Key: "key",
								},
							},
						}},
					},
				},
			}

			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
				cond := meta.FindStatusCondition(cr.Status.Conditions, v1.ConditionTypeReplicaStartupSucceeded)
				g.Expect(cond).ToNot(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(BeEquivalentTo(v1.ConditionReasonReplicaError))
			}).WithPolling(pollingInterval).WithTimeout(time.Minute).Should(Succeed())

			cr.Spec.ContainerTemplate.Env = nil
			Expect(k8sClient.Update(ctx, &cr)).To(Succeed())
			WaitClickHouseUpdatedAndReady(ctx, &cr, 2*time.Minute, true)
			ClickHouseRWChecks(ctx, &cr, new(0))
		})

		It("should correctly configure access", func(ctx context.Context) {
			var (
				password    = fmt.Sprintf("test-password-%d", rand.Uint32()) //nolint:gosec
				passwordSHA = controllerutil.Sha256Hash([]byte(password))
				auth        = clickhouse.Auth{
					Username: "default",
					Password: password,
				}
			)

			secret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("default-pass-%d", rand.Uint32()), //nolint:gosec
					Namespace: ns,
				},
				Data: map[string][]byte{
					"password": []byte(passwordSHA),
				},
			}

			cr := &v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(2)),
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: BaseVersion,
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
							Raw: []byte(fmt.Sprintf(`{"users": {"custom": {"password_sha256_hex": "%s"}}}`, passwordSHA)),
						},
					},
				},
			}

			By("creating secret")
			Expect(k8sClient.Create(ctx, &secret)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, &secret)).To(Succeed())
			})

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})
			WaitClickHouseUpdatedAndReady(ctx, cr, 2*time.Minute, false)

			checks := 0

			By("checking accessible with default user credentials")
			ClickHouseRWChecks(ctx, cr, &checks, auth)

			By("checking accessible with operator management user credentials")

			var managementSecret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      cr.SecretName(),
				Namespace: cr.Namespace,
			}, &managementSecret)).To(Succeed())
			ClickHouseRWChecks(ctx, cr, &checks, clickhouse.Auth{
				Username: chctrl.OperatorManagementUsername,
				Password: string(managementSecret.Data[chctrl.SecretKeyManagementPassword]),
			})

			By("checking accessible with custom user credentials")
			ClickHouseRWChecks(ctx, cr, &checks, clickhouse.Auth{
				Username: "custom",
				Password: password,
			})

			newPassword := fmt.Sprintf("test-password-%d", rand.Uint32()) //nolint:gosec
			By("creating sql user and granting permissions", func() {
				chClient, err := testutil.NewClickHouseClient(ctx, podDialer, cr, auth)
				Expect(err).NotTo(HaveOccurred())

				defer chClient.Close()

				Expect(chClient.Exec(ctx, fmt.Sprintf(
					"CREATE USER sql_test IDENTIFIED WITH sha256_password BY '%s'", newPassword),
				)).To(Succeed())
				Expect(chClient.Exec(ctx, "GRANT ALL ON *.* TO sql_test")).To(Succeed())
			})

			By("checking accessible with sql user credentials")
			ClickHouseRWChecks(ctx, cr, &checks, clickhouse.Auth{
				Username: "sql_test",
				Password: newPassword,
			})
		})

		It("should correctly mount custom configs", func(ctx context.Context) {
			auth := clickhouse.Auth{
				Username: "custom",
				Password: "test-password",
			}

			customConfigMap := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("custom-config-%d", rand.Uint32()), //nolint:gosec
					Namespace: ns,
				},
				Data: map[string]string{
					"user.yaml": fmt.Sprintf(`{"users": {"%s": {
					"password_sha256_hex": "%s",
					"grants": [{"query": "GRANT ALL ON *.*"}]
				}}}`, auth.Username, controllerutil.Sha256Hash([]byte(auth.Password))),
					"config.yaml": `{"max_table_size_to_drop": 7}`,
				},
			}

			cr := &v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(2)),
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
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
							Tag: BaseVersion,
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

			WaitClickHouseUpdatedAndReady(ctx, cr, 2*time.Minute, false)

			By("checking custom user access works")
			ClickHouseRWChecks(ctx, cr, new(0), auth)

			chClient, err := testutil.NewClickHouseClient(ctx, podDialer, cr, auth)
			Expect(err).NotTo(HaveOccurred())

			defer chClient.Close()

			var maxTableSizeToDrop string

			query := "SELECT value FROM system.server_settings WHERE name = 'max_table_size_to_drop'"

			By("checking custom setting applied")
			Expect(chClient.QueryRow(ctx, query, &maxTableSizeToDrop)).To(Succeed())
			Expect(maxTableSizeToDrop).To(Equal("7"))
		})

		It("should generate correct sharded cluster configuration", func(ctx context.Context) {
			password := fmt.Sprintf("test-password-%d", rand.Uint32()) //nolint:gosec
			secret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("default-pass-%d", rand.Uint32()), //nolint:gosec
					Namespace: ns,
				},
				Data: map[string][]byte{
					"password_sha": []byte(controllerutil.Sha256Hash([]byte(password))),
				},
			}
			Expect(k8sClient.Create(ctx, &secret)).To(Succeed())

			ch := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(2)),
					Shards:   new(int32(2)),
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeper.Name,
					},
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: BaseVersion,
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

			chClient, err := testutil.NewClickHouseClient(ctx, podDialer, &ch, clickhouse.Auth{
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

				Expect(rows.Err()).ToNot(HaveOccurred())

				return gcmp.Diff(response, map[string]int64{
					"0": 20,
					"1": 25,
				})
			}, "10s").WithPolling(pollingInterval).Should(BeEmpty())
		})

		It("should support named collections in Keeper with encryption", func(ctx context.Context) {
			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("named-colls-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(1)),
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{Tag: BaseVersion},
					},
					DataVolumeClaimSpec: &defaultStorage,
					KeeperClusterRef:    &corev1.LocalObjectReference{Name: keeper.Name},
				},
			}

			By("creating cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			})
			WaitClickHouseUpdatedAndReady(ctx, &cr, time.Minute, false)

			By("verifying named collections secret key exists")

			var secret corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: cr.Namespace,
				Name:      cr.SecretName(),
			}, &secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey(chctrl.SecretKeyNamedCollectionsKey))
			Expect(secret.Data[chctrl.SecretKeyNamedCollectionsKey]).NotTo(BeEmpty())

			By("verifying named collections config exists")

			var configMap corev1.ConfigMap

			cfgName := cr.ConfigMapNameByReplicaID(v1.ClickHouseReplicaID{ShardID: 0, Index: 0})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cfgName}, &configMap)).To(Succeed())

			ncConfigKey := controllerutil.PathToName(
				path.Join(chctrl.ConfigPath, chctrl.ConfigDPath, "00-named-collections.yaml"),
			)
			Expect(configMap.Data).To(HaveKey(ncConfigKey))
			Expect(configMap.Data[ncConfigKey]).NotTo(BeEmpty())

			By("connecting to cluster")

			chClient, err := testutil.NewClickHouseClient(ctx, podDialer, &cr)
			Expect(err).NotTo(HaveOccurred())

			defer chClient.Close()

			By("creating named collection")
			Expect(chClient.Exec(ctx, "CREATE NAMED COLLECTION e2e_test_named_coll AS test_key = 'test_value'")).To(Succeed())

			By("verifying named collection exists")

			var name string

			query := "SELECT name FROM system.named_collections WHERE name = 'e2e_test_named_coll'"
			Expect(chClient.QueryRow(ctx, query, &name)).To(Succeed())
			Expect(name).To(Equal("e2e_test_named_coll"))
		})

		It("should use external secret instead of creating a managed one", func(ctx context.Context) {
			secretName := fmt.Sprintf("ext-secret-%d", rand.Uint32()) //nolint:gosec

			cr := v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("ext-secret-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas:            new(int32(1)),
					ContainerTemplate:   v1.ContainerTemplateSpec{Image: v1.ContainerImage{Tag: BaseVersion}},
					DataVolumeClaimSpec: &defaultStorage,
					KeeperClusterRef:    &corev1.LocalObjectReference{Name: keeper.Name},
					ExternalSecret: &v1.ExternalSecret{
						Name:   secretName,
						Policy: v1.ExternalSecretPolicyObserve,
					},
				},
			}

			By("creating cluster CR referencing a not-yet-existing external secret")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
			})

			By("verifying ExternalSecretValid=False while secret is absent")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
				cond := meta.FindStatusCondition(cr.Status.Conditions, v1.ClickHouseConditionTypeExternalSecretValid)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(v1.ClickHouseConditionReasonExternalSecretNotFound))
				g.Expect(cond.Message).To(ContainSubstring("not found"))
			}).WithPolling(pollingInterval).WithTimeout(30 * time.Second).Should(Succeed())

			By("verifying operator did not create a managed secret")

			var secretList corev1.SecretList
			Expect(k8sClient.List(ctx, &secretList, client.InNamespace(ns),
				controllerutil.AppRequirements(ns, cr.SpecificName()))).To(Succeed())
			Expect(secretList.Items).To(BeEmpty())

			By("creating external secret with all required keys")

			password := "e2e-test-password"
			extSecret := corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      secretName,
				},
				Data: map[string][]byte{
					chctrl.SecretKeyInterserverPassword: []byte(password),
					chctrl.SecretKeyManagementPassword:  []byte(password),
					chctrl.SecretKeyKeeperIdentity:      []byte("clickhouse:" + password),
					chctrl.SecretKeyClusterSecret:       []byte(password),
					chctrl.SecretKeyNamedCollectionsKey: []byte("0123456789abcdef0123456789abcdef"),
				},
			}
			Expect(k8sClient.Create(ctx, &extSecret)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, &extSecret)).To(Succeed())
			})

			By("waiting for cluster to become ready")
			WaitClickHouseUpdatedAndReady(ctx, &cr, 2*time.Minute, false)
			ClickHouseRWChecks(ctx, &cr, new(0))

			By("verifying ExternalSecretValid=True")
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			cond := meta.FindStatusCondition(cr.Status.Conditions, v1.ClickHouseConditionTypeExternalSecretValid)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			By("verifying operator still has not created a managed secret")
			Expect(k8sClient.List(ctx, &secretList, client.InNamespace(ns),
				controllerutil.AppRequirements(ns, cr.SpecificName()))).To(Succeed())
			Expect(secretList.Items).To(BeEmpty())
		})
	})

	Describe("is handling TLS settings correctly", Ordered, func() {
		var (
			suffix         = rand.Uint32() //nolint:gosec
			issuer         = fmt.Sprintf("issuer-%d", suffix)
			keeperCertName = fmt.Sprintf("keeper-cert-%d", suffix)
			chCertName     = fmt.Sprintf("ch-cert-%d", suffix)

			ns         string
			keeperCR   *v1.KeeperCluster
			keeperCert *certv1.Certificate
			baseCr     *v1.ClickHouseCluster
			chCert     *certv1.Certificate
		)

		BeforeEach(func(ctx context.Context) {
			ns = testNamespace(ctx)
			testutil.SetupCA(ctx, k8sClient, ns, suffix)

			keeperCR = &v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.KeeperClusterSpec{
					Replicas: new(int32(1)),
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: BaseVersion,
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
					Namespace: ns,
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
					Namespace: ns,
					Name:      fmt.Sprintf("clickhouse-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(2)),
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: keeperCR.Name,
					},
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: BaseVersion,
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
					Namespace: ns,
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
			ClickHouseRWChecks(ctx, cr, new(0))
		})

		It("should use custom ca bundle to connect to the keeper", func(ctx context.Context) {
			cr := baseCr.DeepCopy()
			cr.Spec.Settings.TLS = v1.ClusterTLSSpec{
				CABundle: &v1.SecretKeySelector{
					Name: keeperCertName,
					Key:  "ca.crt",
				},
			}

			By("creating clickhouse")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			DeferCleanup(func(ctx context.Context) {
				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			})

			WaitClickHouseUpdatedAndReady(ctx, cr, 2*time.Minute, false)
			ClickHouseRWChecks(ctx, cr, new(0))
		})
	})

	It("should set default affinity settings", func(ctx context.Context) {
		var (
			ns          = testNamespace(ctx)
			nodeToZone  = map[string]string{}
			keeperName  = fmt.Sprintf("test-%d", rand.Uint32()) //nolint:gosec
			keeperZones = map[string]struct{}{}
			nodes       corev1.NodeList
			pods        corev1.PodList
		)

		By("ensuring that cluster has at least 3 nodes in different zones")

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

		By("creating Keeper with affinity settings")

		keeper := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      keeperName,
			},
			Spec: v1.KeeperClusterSpec{
				Replicas:            new(int32(3)),
				DataVolumeClaimSpec: &defaultStorage,
				PodTemplate: v1.PodTemplateSpec{
					TopologyZoneKey: new("topology.kubernetes.io/zone"),
					NodeHostnameKey: new("kubernetes.io/hostname"),
				},
			},
		}

		Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &keeper)).To(Succeed())
		})
		WaitKeeperUpdatedAndReady(ctx, &keeper, 2*time.Minute, false)

		By("checking keeper pod affinity")

		Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns),
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

		By("creating ClickHouse with affinity settings")

		cluster := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      keeperName,
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:            new(int32(3)),
				DataVolumeClaimSpec: &defaultStorage,
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: keeperName,
				},
				PodTemplate: v1.PodTemplateSpec{
					TopologyZoneKey: new("topology.kubernetes.io/zone"),
					NodeHostnameKey: new("kubernetes.io/hostname"),
				},
			},
		}

		Expect(k8sClient.Create(ctx, &cluster)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &cluster)).To(Succeed())
		})
		WaitClickHouseUpdatedAndReady(ctx, &cluster, 2*time.Minute, false)

		By("checking clickhouse pod affinity")

		Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns),
			client.MatchingLabels{controllerutil.LabelAppKey: cluster.SpecificName()})).To(Succeed())
		Expect(pods.Items).To(HaveLen(int(cluster.Replicas())))

		zones = map[string]struct{}{}
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
})

func WaitClickHouseUpdatedAndReady(
	ctx context.Context,
	cr *v1.ClickHouseCluster,
	timeout time.Duration,
	isUpdate bool,
) {
	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.Name))
	EventuallyWithOffset(1, func(g Gomega) {
		var cluster v1.ClickHouseCluster
		g.Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())
		g.Expect(cluster.Generation).To(Equal(cluster.Status.ObservedGeneration))

		if isUpdate {
			for shard := range cluster.Shards() {
				// Intentional global assertion to fail suite if update order is wrong.
				Expect(CheckUpdateOrder(ctx, &client.ListOptions{
					Namespace: cluster.Namespace,
					LabelSelector: labels.SelectorFromSet(map[string]string{
						controllerutil.LabelAppKey:            cluster.SpecificName(),
						controllerutil.LabelClickHouseShardID: strconv.FormatInt(int64(shard), 10),
					}),
				}, controllerutil.LabelClickHouseReplicaID, cluster.Status.StatefulSetRevision,
					cluster.Status.ConfigurationRevision)).To(Succeed())
			}
		}

		g.Expect(cluster.Status.CurrentRevision).To(Equal(cluster.Status.UpdateRevision))
		g.Expect(cluster.Status.ReadyReplicas).To(Equal(cluster.Replicas() * cluster.Shards()))

		for _, conditionType := range []v1.ConditionType{
			v1.ConditionTypeReady,
			v1.ConditionTypeHealthy,
			v1.ConditionTypeClusterSizeAligned,
			v1.ConditionTypeConfigurationInSync,
			v1.ClickHouseConditionTypeSchemaInSync,
		} {
			cond := meta.FindStatusCondition(cluster.Status.Conditions, conditionType)
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(
				Equal(metav1.ConditionTrue),
				fmt.Sprintf("condition %s is false: %s", cond.Type, cond.Message),
			)
		}
	}, timeout).WithPolling(pollingInterval).Should(Succeed())
	// Needed for replica deletion to not forward deleting pods.
	By(fmt.Sprintf("waiting for cluster %s replicas count match", cr.Name))
	count := int(cr.Replicas() * cr.Shards())
	WaitReplicaCount(ctx, k8sClient, cr.Namespace, cr.SpecificName(), count)
	By(fmt.Sprintf("waiting for cluster %s all replicas ready", cr.Name))
	EventuallyWithOffset(1, func(g Gomega) {
		var pods corev1.PodList
		g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(cr.Namespace),
			client.MatchingLabels{controllerutil.LabelAppKey: cr.SpecificName()})).To(Succeed())

		for _, pod := range pods.Items {
			g.Expect(CheckPodReady(&pod)).To(BeTrue(), fmt.Sprintf("pod %s is not ready", pod.Name))
		}
	}).WithPolling(pollingInterval).Should(Succeed())
}

func ClickHouseRWChecks(ctx context.Context, cr *v1.ClickHouseCluster, checksDone *int, auth ...clickhouse.Auth) {
	ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")
	Expect(len(auth)).To(Or(Equal(0), Equal(1)))
	chClient, err := testutil.NewClickHouseClient(ctx, podDialer, cr, auth...)
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
