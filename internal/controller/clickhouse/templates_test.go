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

		Expect(volumes).To(HaveLen(4))
		Expect(mounts).To(HaveLen(4))
		checkVolumeMounts(volumes, mounts)
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
			reconcilerBase: reconcilerBase{
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
			},
		}

		rev, err := getStatefulSetRevision(&r)
		Expect(err).ToNot(HaveOccurred())
		Expect(rev).ToNot(BeEmpty())

		r.Cluster.Spec.DataVolumeClaimSpec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("20Gi")
		rev2, err := getStatefulSetRevision(&r)
		Expect(err).ToNot(HaveOccurred())

		Expect(rev2).To(Equal(rev), "StatefulSet revision should not change when data disk spec changes")
	})
})

func checkVolumeMounts(volumes []corev1.Volume, mounts []corev1.VolumeMount) {
	volumeMap := map[string]struct{}{
		internal.PersistentVolumeName: {},
	}
	for _, volume := range volumes {
		ExpectWithOffset(1, volumeMap).NotTo(HaveKey(volume.Name))
		volumeMap[volume.Name] = struct{}{}
	}

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
			reconcilerBase: reconcilerBase{
				Cluster: newClickHouseCluster(fill),
			},
			keeper: v1.KeeperCluster{ObjectMeta: metav1.ObjectMeta{Name: "keeper"}},
		}
		id := v1.ClickHouseReplicaID{ShardID: 1, Index: 1}

		crBefore := r.Cluster.DeepCopy()

		stsFirst, err1 := templateStatefulSet(r, id)
		if diff := cmp.Diff(crBefore.Spec, r.Cluster.Spec); diff != "" {
			t.Errorf("ClusterSpec mutated:\n%s", diff)
		}

		stsSecond, err2 := templateStatefulSet(r, id)
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
