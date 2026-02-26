package clickhouse

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
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
		volumes, mounts, err := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
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
		volumes, mounts, err := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
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
		volumes, mounts, err := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(4))
		Expect(mounts).To(HaveLen(6))
		checkVolumeMounts(volumes, mounts)
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
		volumes, mounts, err := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(3))
		Expect(mounts).To(HaveLen(5))
		checkVolumeMounts(volumes, mounts)

		projectedVolumeFound := false

		volumeName := controllerutil.PathToName("/etc/clickhouse-server/config.d/")
		for _, volume := range volumes {
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
		volumes, mounts, err := buildVolumes(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(4))
		Expect(mounts).To(HaveLen(6))
		checkVolumeMounts(volumes, mounts)

		projectedVolumeFound := false

		volumeName := controllerutil.PathToName("/etc/clickhouse-server/tls/")
		for _, volume := range volumes {
			if volume.Name == volumeName {
				Expect(volume.Projected).ToNot(BeNil())

				projectedVolumeFound = true
				break
			}
		}

		Expect(projectedVolumeFound).To(BeTrue())
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
				Replicas: ptr.To[int32](3),
				Shards:   ptr.To[int32](2),
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
		cr.Spec.Replicas = ptr.To[int32](1)
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(1))
		Expect(pdb.Spec.MinAvailable).To(BeNil())
	})

	It("should respect custom maxUnavailable", func() {
		maxUnavailable := intstr.FromInt32(2)
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MaxUnavailable: &maxUnavailable,
		}
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MaxUnavailable).NotTo(BeNil())
		Expect(pdb.Spec.MaxUnavailable.IntValue()).To(Equal(2))
		Expect(pdb.Spec.MinAvailable).To(BeNil())
	})

	It("should respect custom minAvailable", func() {
		minAvailable := intstr.FromInt32(2)
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
		}
		pdb := templatePodDisruptionBudget(cr, 0)

		Expect(pdb.Spec.MinAvailable).NotTo(BeNil())
		Expect(pdb.Spec.MinAvailable.IntValue()).To(Equal(2))
		Expect(pdb.Spec.MaxUnavailable).To(BeNil())
	})

	It("should support percentage-based values", func() {
		minAvailable := intstr.FromString("50%")
		cr.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
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
