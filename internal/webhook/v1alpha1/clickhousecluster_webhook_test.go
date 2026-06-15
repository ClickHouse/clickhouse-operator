package v1alpha1

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	chv1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
)

var _ = Describe("ClickHouseCluster Webhook", func() {
	Context("When creating ClickHouseCluster under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func(ctx context.Context) {
			By("Setting the default value")

			chCluster := &chv1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-default",
				},
				Spec: chv1.ClickHouseClusterSpec{
					KeeperClusterRef: chv1.KeeperClusterReference{
						Name: "some-keeper-cluster",
					},
				},
			}
			Expect(k8sClient.Create(ctx, chCluster)).Should(Succeed())
			deferCleanup(chCluster)
			Expect(k8sClient.Get(ctx, chCluster.NamespacedName(), chCluster)).Should(Succeed())

			Expect(chCluster.Spec.ContainerTemplate.Image.Repository).Should(Equal(chv1.DefaultClickHouseContainerRepository))
			Expect(chCluster.Spec.ContainerTemplate.Resources.Requests).Should(Equal(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(chv1.DefaultClickHouseCPURequest),
				corev1.ResourceMemory: resource.MustParse(chv1.DefaultClickHouseMemoryRequest),
			}))
			Expect(chCluster.Spec.ContainerTemplate.Resources.Limits).Should(Equal(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(chv1.DefaultClickHouseCPULimit),
				corev1.ResourceMemory: resource.MustParse(chv1.DefaultClickHouseMemoryLimit),
			}))
		})

		It("Should not inject default resources when the user provided a partial spec", func(ctx context.Context) {
			By("Respecting user-provided requests with no limits")

			chCluster := &chv1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-partial-resources",
				},
				Spec: chv1.ClickHouseClusterSpec{
					KeeperClusterRef: chv1.KeeperClusterReference{
						Name: "some-keeper-cluster",
					},
					ContainerTemplate: chv1.ContainerTemplateSpec{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2"),
								corev1.ResourceMemory: resource.MustParse("10Gi"),
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, chCluster)).Should(Succeed())
			deferCleanup(chCluster)
			Expect(k8sClient.Get(ctx, chCluster.NamespacedName(), chCluster)).Should(Succeed())
			Expect(chCluster.Spec.ContainerTemplate.Resources.Limits).Should(BeNil())
		})

		It("Should set default access modes if data volume enabled", func(ctx context.Context) {
			chCluster := &chv1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-default",
				},
				Spec: chv1.ClickHouseClusterSpec{
					KeeperClusterRef: chv1.KeeperClusterReference{
						Name: "some-keeper-cluster",
					},
					DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
					},
				},
			}
			Expect(k8sClient.Create(ctx, chCluster)).Should(Succeed())
			deferCleanup(chCluster)
			Expect(k8sClient.Get(ctx, chCluster.NamespacedName(), chCluster)).Should(Succeed())

			Expect(chCluster.Spec.DataVolumeClaimSpec.AccessModes).To(ContainElement(chv1.DefaultAccessMode))
		})
	})

	Context("When creating ClickHouseCluster under Validating Webhook", func() {
		chCluster := &chv1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test-validate",
			},
			Spec: chv1.ClickHouseClusterSpec{
				KeeperClusterRef: chv1.KeeperClusterReference{
					Name: "some-keeper-cluster",
				},
			},
		}

		It("Should check TLS enabled if required", func(ctx context.Context) {
			By("Rejecting wrong settings")

			cluster := chCluster.DeepCopy()
			cluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled:  false,
				Required: true,
			}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TLS cannot be required"))
		})

		It("Should allow explicit keeper namespace", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.KeeperClusterRef.Namespace = "keeper-ns"

			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)
		})

		It("Should reject invalid keeper namespace", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.KeeperClusterRef.Namespace = "Keeper_NS"

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("keeperClusterRef.namespace"))
		})

		It("Should check certificate passed if TLS enabled", func(ctx context.Context) {
			By("Rejecting wrong settings")

			cluster := chCluster.DeepCopy()
			cluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled: true,
			}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverCertSecret must be specified"))
		})

		It("Should check default password fields if set", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()

			By("Rejecting cr without source")

			cluster.Spec.Settings.DefaultUserPassword = &chv1.DefaultPasswordSelector{}
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exactly one of secret or configMap must be specified"))

			By("Rejecting cr with empty secret name")

			cluster.Spec.Settings.DefaultUserPassword = &chv1.DefaultPasswordSelector{Secret: &chv1.SecretKeySelector{Key: "smth"}}
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.settings.defaultUserPassword.secret.name: Required value"))

			By("Rejecting cr with empty configmap key")

			cluster.Spec.Settings.DefaultUserPassword = &chv1.DefaultPasswordSelector{ConfigMap: &chv1.ConfigMapKeySelector{Name: "smth"}}
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.settings.defaultUserPassword.configMap.key: Required value"))

			By("Warning on empty field")

			cluster.Spec.Settings.DefaultUserPassword = nil
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(Succeed())
			deferCleanup(cluster)
			Expect(warnings).To(ContainElement(ContainSubstring("defaultUserPassword is empty")))
		})

		It("Should check that all volumes from volume mounts are exists", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()

			cluster.Spec.ContainerTemplate.VolumeMounts = []corev1.VolumeMount{{
				Name: "non-existing-volume",
			}}

			By("Rejecting cr with non existing volume")

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("the volume mount 'non-existing-volume' is invalid because the volume is not defined"))

			cluster.Spec.ContainerTemplate.VolumeMounts = nil
			cluster.Spec.PodTemplate.Volumes = []corev1.Volume{{
				Name: "clickhouse-storage-volume",
			}}
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved and cannot be used"))
		})

		It("Should reject cluster with custom volume mounted at data path when DataVolumeClaimSpec is defined", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.DataVolumeClaimSpec = &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
			}
			cluster.Spec.PodTemplate.Volumes = []corev1.Volume{{
				Name: "custom-data",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}}
			cluster.Spec.ContainerTemplate.VolumeMounts = []corev1.VolumeMount{{
				Name:      "custom-data",
				MountPath: "/var/lib/clickhouse",
			}}

			By("Rejecting cr with data volume mount collision")

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot mount a custom volume at the data path"))
		})

		It("Should warn when no volume is mounted at data path without DataVolumeClaimSpec", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.DataVolumeClaimSpec = nil

			By("Creating diskless cluster with warning")
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)

			By("Verifying warning about data loss")
			Expect(warnings).To(ContainElement(ContainSubstring("no volume is mounted at the data path")))
		})

		It("Should check that data volume cannot be added after creation", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.DataVolumeClaimSpec = nil
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)
			cluster.Spec.DataVolumeClaimSpec = &corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			}}

			By("Rejecting cr with added data volume")

			err := k8sClient.Update(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be added"))
		})

		It("Should check that data volume cannot be removed after creation", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.DataVolumeClaimSpec = &corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)
			cluster.Spec.DataVolumeClaimSpec = nil

			By("Rejecting cr with added data volume")

			err := k8sClient.Update(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be removed"))
		})

		It("Should reject invalid PodDisruptionBudged", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.PodDisruptionBudget = &chv1.PodDisruptionBudgetSpec{
				Policy:         chv1.PDBPolicyEnabled,
				MinAvailable:   new(intstr.FromInt32(1)),
				MaxUnavailable: new(intstr.FromInt32(1)),
			}
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("only one of"))
		})

		It("Should accept a valid AdditionalPorts list", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.AdditionalPorts = []chv1.AdditionalPort{
				{Name: "mysql", Port: 9004},
				{Name: "postgres", Port: 9005},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)
		})

		It("Should reject AdditionalPorts with duplicate names (enforced by CRD list-map key)", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.AdditionalPorts = []chv1.AdditionalPort{
				{Name: "mysql", Port: 9004},
				{Name: "mysql", Port: 19004},
			}
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mysql"))
		})

		It("Should reject AdditionalPorts with duplicate ports", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.AdditionalPorts = []chv1.AdditionalPort{
				{Name: "alpha", Port: 12345},
				{Name: "beta", Port: 12345},
			}
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.additionalPorts[1].port"))
			Expect(err.Error()).To(ContainSubstring("duplicates"))
		})

		It("Should reject AdditionalPorts colliding with a reserved port", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.AdditionalPorts = []chv1.AdditionalPort{
				{Name: "custom-http", Port: 8123},
			}
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.additionalPorts[0].port"))
			Expect(err.Error()).To(ContainSubstring("reserved"))
		})

		It("Should reject AdditionalPorts using a reserved name", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()
			cluster.Spec.AdditionalPorts = []chv1.AdditionalPort{
				{Name: "http", Port: 18123},
			}
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.additionalPorts[0].name"))
			Expect(err.Error()).To(ContainSubstring("reserved"))
		})
	})

	Context("When validating additionalVolumeClaimTemplates (JBOD)", func() {
		dataVolume := func() *corev1.PersistentVolumeClaimSpec {
			return &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			}
		}

		disk := func(name, storage string) chv1.PersistentVolumeClaimTemplate {
			return chv1.PersistentVolumeClaimTemplate{
				NamedTemplateMeta: chv1.NamedTemplateMeta{Name: name},
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(storage)},
					},
				},
			}
		}

		base := func(name string) *chv1.ClickHouseCluster {
			return &chv1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
				Spec: chv1.ClickHouseClusterSpec{
					KeeperClusterRef:    chv1.KeeperClusterReference{Name: "some-keeper-cluster"},
					DataVolumeClaimSpec: dataVolume(),
				},
			}
		}

		It("Should reject additional disks without a data volume", func(ctx context.Context) {
			cluster := base("jbod-no-data")
			cluster.Spec.DataVolumeClaimSpec = nil
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "1Gi")}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("dataVolumeClaimSpec should be set"))
		})

		It("Should reject an additional disk colliding with the primary data volume name", func(ctx context.Context) {
			cluster := base("jbod-collision")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk(internal.PersistentVolumeName, "1Gi")}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("collides with primary data volume name"))
		})

		It("Should reject duplicate additional disk names", func(ctx context.Context) {
			cluster := base("jbod-duplicate")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "1Gi"), disk("disk1", "2Gi")}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate name"))
		})

		It("Should reject an additional disk name reserved by the ClickHouse default disk", func(ctx context.Context) {
			cluster := base("jbod-default")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("default", "1Gi")}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved by the ClickHouse default disk"))
		})

		It("Should reject an additional disk name longer than 63 characters", func(ctx context.Context) {
			cluster := base("jbod-long-name")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk(strings.Repeat("a", 64), "1Gi")}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(ContainSubstring("Too long"), ContainSubstring("should be at most")))
		})

		It("Should accept valid additional disks alongside a data volume", func(ctx context.Context) {
			cluster := base("jbod-valid")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "1Gi"), disk("disk2", "2Gi")}

			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)
		})

		It("Should reject adding an additional disk after creation", func(ctx context.Context) {
			cluster := base("jbod-add")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "1Gi")}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)

			Expect(k8sClient.Get(ctx, cluster.NamespacedName(), cluster)).To(Succeed())
			cluster.Spec.AdditionalVolumeClaimTemplates = append(cluster.Spec.AdditionalVolumeClaimTemplates, disk("disk2", "1Gi"))

			err := k8sClient.Update(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be added or removed"))
		})

		It("Should reject removing an additional disk after creation", func(ctx context.Context) {
			cluster := base("jbod-remove")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "1Gi")}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)

			Expect(k8sClient.Get(ctx, cluster.NamespacedName(), cluster)).To(Succeed())
			cluster.Spec.AdditionalVolumeClaimTemplates = nil

			err := k8sClient.Update(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be added or removed"))
		})

		It("Should reject renaming an additional disk after creation", func(ctx context.Context) {
			cluster := base("jbod-rename")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "1Gi")}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)

			Expect(k8sClient.Get(ctx, cluster.NamespacedName(), cluster)).To(Succeed())
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk-renamed", "1Gi")}

			err := k8sClient.Update(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be changed"))
		})

		It("Should allow resizing an existing additional disk", func(ctx context.Context) {
			cluster := base("jbod-resize")
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "1Gi")}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			deferCleanup(cluster)

			Expect(k8sClient.Get(ctx, cluster.NamespacedName(), cluster)).To(Succeed())
			cluster.Spec.AdditionalVolumeClaimTemplates = []chv1.PersistentVolumeClaimTemplate{disk("disk1", "5Gi")}

			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
		})
	})
})
