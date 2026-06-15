package clickhouse

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/randfill"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/testutil"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("BuildVolumes", func() {
	ctx := clickhouseReconciler{}

	It("should generate default volumes", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
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
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
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
	})

	It("should mount additionalVolumeClaimTemplates at their default JBOD path", func() {
		ctx.Cluster = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.ClickHouseClusterSpec{
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
				AdditionalVolumeClaimTemplates: []v1.PersistentVolumeClaimTemplate{
					{NamedTemplateMeta: v1.NamedTemplateMeta{Name: "disk1"}, Spec: corev1.PersistentVolumeClaimSpec{}},
					{NamedTemplateMeta: v1.NamedTemplateMeta{Name: "disk2"}, Spec: corev1.PersistentVolumeClaimSpec{}},
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
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.ClickHouseClusterSpec{
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "my-extra-volume",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-extra-config",
								},
							},
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
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.ClickHouseClusterSpec{
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "my-extra-volume",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-extra-config",
								},
							},
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
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
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
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "my-extra-config",
								},
							},
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
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.ClickHouseClusterSpec{
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "custom-data",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
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
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
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

var _ = Describe("PDB", func() {
	var cr *v1.ClickHouseCluster

	BeforeEach(func() {
		cr = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: "default",
			},
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
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
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
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
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
				ObjectMeta: metav1.ObjectMeta{Name: "jbod", Namespace: "default"},
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
							NamedTemplateMeta: v1.NamedTemplateMeta{Name: "disk1"},
							Spec: corev1.PersistentVolumeClaimSpec{
								AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("100Gi")},
								},
							},
						},
						{
							NamedTemplateMeta: v1.NamedTemplateMeta{Name: "disk2"},
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Labels: map[string]string{
				"app": "clickhouse-operator",
			},
			Annotations: map[string]string{
				"annotation1": "value1",
			},
		},
	}
	f.Fill(&cr.Spec)
	cr.Spec.WithDefaults()

	return cr
}
