package clickhouse

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/randfill"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
	"github.com/ClickHouse/clickhouse-operator/internal/controller"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/testutil"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("BuildVolumes", func() {
	ctx := clickhouseReconciler{}

	It("should generate default volumes", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
			},
		}
		volumes := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		mounts := buildMounts(&ctx)

		Expect(volumes).To(HaveLen(3))
		Expect(mounts).To(HaveLen(5))
		checkVolumeMounts(volumes, mounts)
	})

	It("should generate mounts for TLS", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				Settings: v1.ClickHouseSettings{
					TLS: v1.ClusterTLSSpec{
						Enabled: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: "serverCertSecret",
						},
					},
				},
			},
		}
		volumes := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		mounts := buildMounts(&ctx)

		Expect(volumes).To(HaveLen(5))
		Expect(mounts).To(HaveLen(5))
		checkVolumeMounts(volumes, mounts)

		var tlsItems []corev1.KeyToPath
		for _, volume := range volumes {
			if volume.Name == internal.TLSVolumeName {
				tlsItems = volume.Secret.Items
			}
		}

		Expect(tlsItems).To(ConsistOf(
			corev1.KeyToPath{Key: "tls.crt", Path: CertificateFilename},
			corev1.KeyToPath{Key: "tls.key", Path: KeyFilename},
		))
	})

	It("should generate a custom CA volume when caBundle is set", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				Settings: v1.ClickHouseSettings{
					TLS: v1.ClusterTLSSpec{
						Enabled:          true,
						ServerCertSecret: &corev1.LocalObjectReference{Name: "serverCertSecret"},
						CABundle:         &v1.CABundleSelector{Name: "ca-secret", Key: "ca.crt"},
					},
				},
			},
		}
		volumes := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		mounts := buildMounts(&ctx)

		var caItems []corev1.KeyToPath
		for _, volume := range volumes {
			if volume.Name == internal.CustomCAVolumeName {
				Expect(volume.Secret.SecretName).To(Equal("ca-secret"))
				caItems = volume.Secret.Items
			}
		}

		Expect(caItems).To(ConsistOf(corev1.KeyToPath{Key: "ca.crt", Path: CustomCAFilename}))
		Expect(mounts).To(ContainElement(corev1.VolumeMount{
			Name:      internal.CustomCAVolumeName,
			MountPath: TLSConfigPath,
			ReadOnly:  true,
		}))
	})

	It("should mount additionalVolumeClaimTemplates at their default JBOD path", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
				AdditionalVolumeClaimTemplates: []v1.PersistentVolumeClaimTemplate{
					{Name: "disk1", Spec: corev1.PersistentVolumeClaimSpec{}},
					{Name: "disk2", Spec: corev1.PersistentVolumeClaimSpec{}},
				},
			},
		}

		// Additional disks are backed by StatefulSet volumeClaimTemplates (not pod
		// volumes), so they appear as mounts only; the volume is provided by the STS.
		mounts := buildMounts(&ctx)
		Expect(mounts).To(HaveLen(7)) // 5 from data+config + 2 additional

		mountPaths := make(map[string]string)
		for _, m := range mounts {
			mountPaths[m.MountPath] = m.Name
		}

		Expect(mountPaths["/var/lib/clickhouse/disks/disk1"]).To(Equal("disk1"))
		Expect(mountPaths["/var/lib/clickhouse/disks/disk2"]).To(Equal("disk2"))
	})

	It("should add volumes provided by user", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "my-extra-volume",
						ConfigMap: &corev1.ConfigMapVolumeSource{
							Name: "my-extra-config",
						}},
					},
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "my-extra-volume",
						MountPath: "/etc/my-extra-volume",
					}},
				},
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
			},
		}
		podSpec, err := templatePodSpec(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(podSpec.Volumes).To(HaveLen(4))
		Expect(podSpec.Containers[0].VolumeMounts).To(HaveLen(6))
		checkVolumeMounts(podSpec.Volumes, podSpec.Containers[0].VolumeMounts)
	})

	It("should project volumes with colliding path", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "my-extra-volume",
						ConfigMap: &corev1.ConfigMapVolumeSource{
							Name: "my-extra-config",
						}},
					},
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "my-extra-volume",
						MountPath: "/etc/clickhouse-server/config.d/",
					}},
				},
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
			},
		}
		podSpec, err := templatePodSpec(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(podSpec.Volumes).To(HaveLen(3))
		Expect(podSpec.Containers[0].VolumeMounts).To(HaveLen(5))
		checkVolumeMounts(podSpec.Volumes, podSpec.Containers[0].VolumeMounts)

		projectedVolumeFound := false

		volumeName := controllerutil.PathToName("/etc/clickhouse-server/config.d/")
		for _, volume := range podSpec.Volumes {
			if volume.Name == volumeName {
				Expect(volume.Projected).ToNot(BeNil())

				projectedVolumeFound = true
				break
			}
		}

		Expect(projectedVolumeFound).To(BeTrue())
	})

	It("should project colliding TLS volumes", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				Settings: v1.ClickHouseSettings{
					TLS: v1.ClusterTLSSpec{
						Enabled: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: "serverCertSecret",
						},
					},
				},
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "my-extra-volume",
						ConfigMap: &corev1.ConfigMapVolumeSource{
							Name: "my-extra-config",
						}},
					},
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "my-extra-volume",
						MountPath: "/etc/clickhouse-server/tls/",
					}},
				},
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
			},
		}
		podSpec, err := templatePodSpec(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(podSpec.Volumes).To(HaveLen(4))
		Expect(podSpec.Containers[0].VolumeMounts).To(HaveLen(6))
		checkVolumeMounts(podSpec.Volumes, podSpec.Containers[0].VolumeMounts)

		projectedVolumeFound := false

		volumeName := controllerutil.PathToName("/etc/clickhouse-server/tls/")
		for _, volume := range podSpec.Volumes {
			if volume.Name == volumeName {
				Expect(volume.Projected).ToNot(BeNil())

				projectedVolumeFound = true
				break
			}
		}

		Expect(projectedVolumeFound).To(BeTrue())
	})

	It("should work with custom volume", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			Name: "test",
			Spec: v1.ClickHouseClusterSpec{
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name:     "custom-data",
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					}},
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "custom-data",
						MountPath: "/var/lib/clickhouse",
					}},
				},
			},
		}
		podSpec, err := templatePodSpec(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		checkVolumeMounts(podSpec.Volumes, podSpec.Containers[0].VolumeMounts)
	})
})

var _ = Describe("SecurityContext defaults", func() {
	newCtx := func() clickhouseReconciler {
		return clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				Name: "test",
				Spec: v1.ClickHouseClusterSpec{
					DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
				},
			},
		}
	}

	Context("on vanilla Kubernetes", func() {
		BeforeEach(func() { controllerutil.SetOpenShiftForTest(false) })
		AfterEach(func() { controllerutil.SetOpenShiftForTest(false) })

		It("should pin pod FSGroup to the ClickHouse user", func() {
			ctx := newCtx()
			podSpec, err := templatePodSpec(&ctx, v1.ClickHouseReplicaID{})
			Expect(err).NotTo(HaveOccurred())
			Expect(podSpec.SecurityContext).NotTo(BeNil())
			Expect(podSpec.SecurityContext.FSGroup).NotTo(BeNil())
			Expect(*podSpec.SecurityContext.FSGroup).To(BeEquivalentTo(101))
		})
	})

	Context("on OpenShift", func() {
		BeforeEach(func() { controllerutil.SetOpenShiftForTest(true) })
		AfterEach(func() { controllerutil.SetOpenShiftForTest(false) })

		It("should leave pod UID/GID/FSGroup unset so SCC can inject them", func() {
			ctx := newCtx()
			podSpec, err := templatePodSpec(&ctx, v1.ClickHouseReplicaID{})
			Expect(err).NotTo(HaveOccurred())
			Expect(podSpec.SecurityContext).NotTo(BeNil())
			Expect(podSpec.SecurityContext.FSGroup).To(BeNil())
			Expect(podSpec.SecurityContext.RunAsUser).To(BeNil())
			Expect(podSpec.SecurityContext.RunAsGroup).To(BeNil())
		})
	})
})

var _ = Describe("Service templates", func() {
	cr := &v1.ClickHouseCluster{
		Name:      "test",
		Namespace: "default",
		Spec: v1.ClickHouseClusterSpec{
			AdditionalPorts: []v1.AdditionalPort{{
				Name: "extra",
				Port: 19000,
			}},
		},
	}

	It("should expose client ports through the public headless service and gate them on readiness", func() {
		service := templateHeadlessService(cr)

		ports := map[string]int32{}
		for _, port := range service.Spec.Ports {
			ports[port.Name] = port.Port
		}

		Expect(service.Name).To(Equal(cr.HeadlessServiceName()))
		Expect(service.Spec.PublishNotReadyAddresses).To(BeFalse())
		Expect(service.Spec.Selector).To(SatisfyAll(
			HaveKeyWithValue(controllerutil.LabelAppKey, cr.SpecificName()),
			HaveKeyWithValue(controllerutil.LabelRoleKey, controllerutil.LabelClickHouseValue),
		))
		Expect(ports).To(HaveKeyWithValue(protocolTypeTCP, int32(PortNative)))
		Expect(ports).To(HaveKeyWithValue(protocolTypeHTTP, int32(PortHTTP)))
		Expect(ports).To(HaveKeyWithValue(protocolTypePrometheus, int32(PortPrometheusScrape)))
		Expect(ports).To(HaveKeyWithValue("extra", int32(19000)))
	})

	It("should expose internal ports through a per-replica internal service", func() {
		id := v1.ClickHouseReplicaID{ShardID: 0, Index: 1}
		service := templateInternalService(cr, id)

		ports := map[string]int32{}
		for _, port := range service.Spec.Ports {
			ports[port.Name] = port.Port
		}

		Expect(service.Name).To(Equal(cr.InternalServiceNameByReplicaID(id)))
		Expect(service.Spec.ClusterIP).To(Equal(corev1.ClusterIPNone))
		Expect(service.Spec.PublishNotReadyAddresses).To(BeTrue())
		Expect(service.Labels).To(SatisfyAll(
			HaveKeyWithValue(controllerutil.LabelClusterKey, cr.Name),
			HaveKeyWithValue(controllerutil.LabelClickHouseShardID, "0"),
			HaveKeyWithValue(controllerutil.LabelClickHouseReplicaID, "1"),
		))
		Expect(service.Spec.Selector).To(SatisfyAll(
			HaveKeyWithValue(controllerutil.LabelAppKey, cr.SpecificName()),
			HaveKeyWithValue(controllerutil.LabelRoleKey, controllerutil.LabelClickHouseValue),
			HaveKeyWithValue(controllerutil.LabelClickHouseShardID, "0"),
			HaveKeyWithValue(controllerutil.LabelClickHouseReplicaID, "1"),
		))
		Expect(ports).To(Equal(map[string]int32{
			protocolTypeInterserver:    PortInterserver,
			protocolTypeManagement:     PortManagement,
			protocolTypeManagementHTTP: PortManagementHTTP,
		}))

		for index := 1; index < len(service.Spec.Ports); index++ {
			Expect(service.Spec.Ports[index-1].Name < service.Spec.Ports[index].Name).To(BeTrue())
		}
	})

	It("should keep the public headless service as the StatefulSet governing service", func() {
		r := &clickhouseReconciler{Cluster: cr}
		sts, err := templateStatefulSet(r, v1.ClickHouseReplicaID{}, "fixed-cfg-rev")
		Expect(err).ToNot(HaveOccurred())
		Expect(sts.Spec.ServiceName).To(Equal(cr.HeadlessServiceName()))
	})

	It("should always add the initialization readiness gate", func() {
		gate := corev1.PodReadinessGate{ConditionType: v1.ReplicaInitializedCondition}

		sts, err := templateStatefulSet(&clickhouseReconciler{Cluster: cr}, v1.ClickHouseReplicaID{}, "fixed-cfg-rev")
		Expect(err).ToNot(HaveOccurred())
		Expect(sts.Spec.Template.Spec.ReadinessGates).To(ContainElement(gate))

		// The gate must survive disabling database sync, or the pods would never become Ready.
		disabled := cr.DeepCopy()
		disabled.Spec.Settings.EnableDatabaseSync = new(false)

		sts, err = templateStatefulSet(&clickhouseReconciler{Cluster: disabled}, v1.ClickHouseReplicaID{}, "fixed-cfg-rev")
		Expect(err).ToNot(HaveOccurred())
		Expect(sts.Spec.Template.Spec.ReadinessGates).To(ContainElement(gate))
	})

	It("should expose secure user ports only through the public headless service", func() {
		secure := cr.DeepCopy()
		secure.Spec.Settings.TLS.Enabled = true
		secure.Spec.Settings.TLS.Required = true

		publicPorts := map[string]int32{}
		for _, port := range templateHeadlessService(secure).Spec.Ports {
			publicPorts[port.Name] = port.Port
		}

		Expect(publicPorts).To(HaveKeyWithValue(protocolTypeTCPSecure, int32(PortNativeSecure)))
		Expect(publicPorts).To(HaveKeyWithValue(protocolTypeHTTPSecure, int32(PortHTTPSecure)))
		Expect(publicPorts).NotTo(HaveKey(protocolTypeTCP))
		Expect(publicPorts).NotTo(HaveKey(protocolTypeHTTP))
	})

	It("should not expose internal ports through the public headless service", func() {
		publicPorts := map[string]int32{}
		for _, port := range templateHeadlessService(cr).Spec.Ports {
			publicPorts[port.Name] = port.Port
		}

		Expect(publicPorts).To(HaveKeyWithValue("extra", int32(19000)))
		Expect(publicPorts).NotTo(HaveKey(protocolTypeInterserver))
		Expect(publicPorts).NotTo(HaveKey(protocolTypeManagement))
		Expect(publicPorts).NotTo(HaveKey(protocolTypeManagementHTTP))
	})
})

var _ = Describe("PDB", func() {
	var cr *v1.ClickHouseCluster

	BeforeEach(func() {
		cr = &v1.ClickHouseCluster{
			Name:      "test",
			Namespace: "default",
			Spec: v1.ClickHouseClusterSpec{
				Replicas: new(int32(3)),
				Shards:   new(int32(2)),
			},
		}
	})

	It("should default to minAvailable=1 for multi-replica shards", func() {
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MinAvailable).NotTo(BeNil())
		Expect(pdb.Spec.MinAvailable.IntValue()).To(Equal(1))
		Expect(pdb.Spec.MaxUnavailable).To(BeNil())
	})

	It("should default to maxUnavailable=1 for single-replica shards", func() {
		cr.Spec.Replicas = new(int32(1))
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(1))
		Expect(pdb.Spec.MinAvailable).To(BeNil())
	})

	It("should respect custom maxUnavailable", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MaxUnavailable: new(intstr.FromInt32(2)),
		}
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(2))
		Expect(pdb.Spec.MinAvailable).To(BeNil())
	})

	It("should respect custom minAvailable", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromInt32(2)),
		}
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MinAvailable).NotTo(BeNil())
		Expect(pdb.Spec.MinAvailable.IntValue()).To(Equal(2))
		Expect(pdb.Spec.MaxUnavailable).To(BeNil())
	})

	It("should support percentage-based values", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MinAvailable: new(intstr.FromString("50%")),
		}
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MinAvailable).NotTo(BeNil())
		Expect(pdb.Spec.MinAvailable.String()).To(Equal("50%"))
	})

	It("should set correct name per shard", func() {
		pdb0 := templatePodDisruptionBudget(cr, 0)
		pdb1 := templatePodDisruptionBudget(cr, 1)

		Expect(pdb0.Name).To(Equal("test-clickhouse-0"))
		Expect(pdb1.Name).To(Equal("test-clickhouse-1"))
	})

	It("should set correct labels and selector for shard", func() {
		cr.Spec.Labels = map[string]string{"env": "test"}
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Labels).To(HaveKeyWithValue("env", "test"))
		Expect(pdb.Labels).To(HaveKeyWithValue(controllerutil.LabelClickHouseShardID, "0"))
		Expect(pdb.Spec.Selector.MatchLabels).To(HaveKeyWithValue(controllerutil.LabelAppKey, "test-clickhouse"))
		Expect(pdb.Spec.Selector.MatchLabels).To(HaveKeyWithValue(controllerutil.LabelClickHouseShardID, "0"))
	})

	It("should set unhealthyPodEvictionPolicy when specified", func() {
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			UnhealthyPodEvictionPolicy: new(policyv1.AlwaysAllow),
		}
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.UnhealthyPodEvictionPolicy).NotTo(BeNil())
		Expect(*pdb.Spec.UnhealthyPodEvictionPolicy).To(Equal(policyv1.AlwaysAllow))
	})

	It("should not set unhealthyPodEvictionPolicy when not specified", func() {
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.UnhealthyPodEvictionPolicy).To(BeNil())
	})
})

var _ = Describe("getStatefulSetRevision", func() {
	It("should not depend on data disk spec", func() {
		r := clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				Name: "test",
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(1)),
					DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("10Gi"),
							},
						},
					},
				},
			},
		}

		rev, err := getStatefulSetRevision(&r, "fixed-cfg-rev")
		Expect(err).ToNot(HaveOccurred())
		Expect(rev).ToNot(BeEmpty())

		r.Cluster.Spec.DataVolumeClaimSpec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("20Gi")
		rev2, err := getStatefulSetRevision(&r, "fixed-cfg-rev")
		Expect(err).ToNot(HaveOccurred())

		Expect(rev2).To(Equal(rev), "StatefulSet revision should not change when data disk spec changes")
	})
})

var _ = Describe("getConfigurationRevisions", func() {
	It("should generate idempotent non-empty revisions", func() {
		r := &clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				Name: "test",
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(1)),
				},
			},
		}

		revsFirst, err := getConfigurationRevisions(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(revsFirst.Config).To(Not(BeEmpty()))
		Expect(revsFirst.Restart).To(Not(BeEmpty()))
		Expect(revsFirst.Reload).To(Not(BeEmpty()))

		revsSecond, err := getConfigurationRevisions(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(revsFirst).To(Equal(revsSecond))
	})

	It("reload revision should not depend on restartable configs", func() {
		r := &clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				Name: "test",
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(1)),
				},
			},
		}

		revsBefore, err := getConfigurationRevisions(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(revsBefore.Config).To(Not(BeEmpty()))
		Expect(revsBefore.Restart).To(Not(BeEmpty()))
		Expect(revsBefore.Reload).To(Not(BeEmpty()))

		// User-provided config always triggers restart for safety.
		r.Cluster.Spec.Settings.ExtraConfig = runtime.RawExtension{Raw: []byte("{}")}

		revsAfter, err := getConfigurationRevisions(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(revsAfter.Config).To(Not(Equal(revsBefore.Config)))
		Expect(revsAfter.Reload).To(Equal(revsBefore.Reload))
		Expect(revsAfter.Restart).To(Not(Equal(revsBefore.Restart)))
	})

	It("restart revision should not depend on reloadable configs", func() {
		r := &clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				Name: "test",
				Spec: v1.ClickHouseClusterSpec{
					Replicas: new(int32(1)),
				},
			},
		}

		revsBefore, err := getConfigurationRevisions(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(revsBefore.Config).To(Not(BeEmpty()))
		Expect(revsBefore.Restart).To(Not(BeEmpty()))
		Expect(revsBefore.Reload).To(Not(BeEmpty()))

		// Changes shard replicas list, reloadable
		*r.Cluster.Spec.Replicas = 2

		revsAfter, err := getConfigurationRevisions(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(revsAfter.Config).To(Not(Equal(revsBefore.Config)))
		Expect(revsAfter.Reload).To(Not(Equal(revsBefore.Reload)))
		Expect(revsAfter.Restart).To(Equal(revsBefore.Restart))
	})
})

var _ = Describe("TemplateStatefulSet", func() {
	It("should mount additional JBOD disks from explicit PVC volumes", func() {
		r := &clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				Name: "jbod", Namespace: "default",
				Spec: v1.ClickHouseClusterSpec{
					Shards:           new(int32(2)),
					Replicas:         new(int32(2)),
					KeeperClusterRef: v1.KeeperClusterReference{Name: "keeper"},
					DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")},
						},
					},
					AdditionalVolumeClaimTemplates: []v1.PersistentVolumeClaimTemplate{
						{
							Name: "disk1",
							Spec: corev1.PersistentVolumeClaimSpec{
								AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")},
								},
							},
						},
						{
							Name: "disk2",
							Spec: corev1.PersistentVolumeClaimSpec{
								AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")},
								},
							},
						},
					},
				},
			},
			keeper: v1.KeeperCluster{ObjectMeta: metav1.ObjectMeta{Name: "keeper"}},
		}
		r.Cluster.Spec.WithDefaults()

		sts, err := templateStatefulSet(r, v1.ClickHouseReplicaID{ShardID: 0, Index: 0}, "fixed-cfg-rev")
		Expect(err).To(Not(HaveOccurred()))

		// Primary + both additional disks are reconciled identically, as volumeClaimTemplates.
		vctNames := make([]string, 0, len(sts.Spec.VolumeClaimTemplates))
		for _, vct := range sts.Spec.VolumeClaimTemplates {
			vctNames = append(vctNames, vct.Name)
		}

		Expect(vctNames).To(ConsistOf(internal.PersistentVolumeName, "disk1", "disk2"))

		podSpec, err := templatePodSpec(r, v1.ClickHouseReplicaID{ShardID: 0, Index: 0})
		Expect(err).To(Not(HaveOccurred()))

		mountPaths := make(map[string]string)
		for _, c := range podSpec.Containers {
			for _, m := range c.VolumeMounts {
				mountPaths[m.MountPath] = m.Name
			}
		}

		Expect(mountPaths["/var/lib/clickhouse/disks/disk1"]).To(Equal("disk1"))
		Expect(mountPaths["/var/lib/clickhouse/disks/disk2"]).To(Equal("disk2"))
	})
})

func checkVolumeMounts(volumes []corev1.Volume, mounts []corev1.VolumeMount) {
	volumeMap := map[string]struct{}{}
	for _, volume := range volumes {
		ExpectWithOffset(1, volumeMap).NotTo(HaveKey(volume.Name))
		volumeMap[volume.Name] = struct{}{}
	}

	volumeMap[internal.PersistentVolumeName] = struct{}{}

	mountPaths := map[string]struct{}{}
	for _, mount := range mounts {
		ExpectWithOffset(1, mountPaths).NotTo(HaveKey(mount.MountPath))
		mountPaths[mount.MountPath] = struct{}{}
		ExpectWithOffset(1, volumeMap).To(HaveKey(mount.Name), "Mount %s is not in volumes", mount.Name)
	}
}

func FuzzClusterSpec(f *testing.F) {
	// Manually added cases
	f.Add([]byte("02"))

	f.Fuzz(func(t *testing.T, data []byte) {
		fill := testutil.NewSpecFiller(data)
		r := &clickhouseReconciler{
			Cluster: newClickHouseCluster(fill),
			keeper:  v1.KeeperCluster{ObjectMeta: metav1.ObjectMeta{Name: "keeper"}},
		}
		id := v1.ClickHouseReplicaID{ShardID: 1, Index: 1}

		crBefore := r.Cluster.DeepCopy()

		stsFirst, err1 := templateStatefulSet(r, id, "fixed-cfg-rev")
		if diff := cmp.Diff(crBefore.Spec, r.Cluster.Spec); diff != "" {
			t.Errorf("ClusterSpec mutated:\n%s", diff)
		}

		stsSecond, err2 := templateStatefulSet(r, id, "fixed-cfg-rev")
		if diff := cmp.Diff(crBefore.Spec, r.Cluster.Spec); diff != "" {
			t.Errorf("ClusterSpec mutated:\n%s", diff)
		}

		if err1 == nil {
			if diff := cmp.Diff(stsFirst, stsSecond); diff != "" {
				t.Errorf("result differs:\n%s", diff)
			}
		} else {
			if err1.Error() != err2.Error() {
				t.Errorf("errors differ: %v vs %v", err1, err2)
			}
		}
	})
}

func newClickHouseCluster(f *randfill.Filler) *v1.ClickHouseCluster {
	cr := &v1.ClickHouseCluster{
		Name:      "test",
		Namespace: "default",
		Labels: map[string]string{
			"app": "clickhouse-operator",
		},
		Annotations: map[string]string{
			"annotation1": "value1",
		},
	}
	f.Fill(&cr.Spec)
	cr.Spec.WithDefaults()

	return cr
}

var _ = Describe("TemplateNetworkPolicy", func() {
	newCluster := func() *v1.ClickHouseCluster {
		return &v1.ClickHouseCluster{
			Name: "test", Namespace: "test-ns",
			Spec: v1.ClickHouseClusterSpec{
				NetworkPolicy: &v1.ClickHouseNetworkPolicySpec{Policy: v1.NetworkPolicyEnabled},
			},
		}
	}

	rulePorts := func(rule networkingv1.NetworkPolicyIngressRule) []int32 {
		ports := make([]int32, 0, len(rule.Ports))
		for _, p := range rule.Ports {
			ports = append(ports, p.Port.IntVal)
		}

		return ports
	}

	It("should restrict internal traffic to replicas and the operator", func() {
		np := templateNetworkPolicy(newCluster())

		podLabels := map[string]string{
			controllerutil.LabelAppKey:  "test-clickhouse",
			controllerutil.LabelRoleKey: controllerutil.LabelClickHouseValue,
		}
		Expect(np.Spec.PodSelector.MatchLabels).To(Equal(podLabels))
		Expect(np.Spec.PolicyTypes).To(Equal([]networkingv1.PolicyType{networkingv1.PolicyTypeIngress}))
		Expect(np.Spec.Ingress).To(HaveLen(2))

		Expect(np.Spec.Ingress[0].From).To(HaveLen(1))
		Expect(np.Spec.Ingress[0].From[0].PodSelector.MatchLabels).To(Equal(podLabels))
		Expect(rulePorts(np.Spec.Ingress[0])).To(Equal([]int32{PortInterserver, PortManagement}))

		Expect(np.Spec.Ingress[1].From).To(Equal([]networkingv1.NetworkPolicyPeer{
			controller.RolePeer(controllerutil.LabelOperatorValue),
		}))
		Expect(rulePorts(np.Spec.Ingress[1])).To(Equal([]int32{PortManagement, PortManagementHTTP}))
	})

	It("should generate identical policies across invocations", func() {
		cluster := newCluster()

		Expect(templateNetworkPolicy(cluster)).To(Equal(templateNetworkPolicy(cluster)))
	})
})
