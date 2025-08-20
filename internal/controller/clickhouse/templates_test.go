package clickhouse

import (
	"testing"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildVolumes(t *testing.T) {
	ctx := reconcileContext{}

	t.Run("EmptyCluster", func(t *testing.T) {
		g := NewWithT(t)
		RegisterFailHandler(g.Fail)
		ctx.Cluster = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.ClickHouseClusterSpec{},
		}
		volumes, mounts, err := buildVolumes(&ctx, v1.ReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(3))
		Expect(mounts).To(HaveLen(5))
		checkVolumeMounts(volumes, mounts)
	})

	t.Run("TLSCluster", func(t *testing.T) {
		g := NewWithT(t)
		RegisterFailHandler(g.Fail)
		ctx.Cluster = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.ClickHouseClusterSpec{
				Settings: v1.ClickHouseConfig{
					TLS: v1.ClusterTLSSpec{
						Enabled: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: "serverCertSecret",
						},
					},
				},
			},
		}
		volumes, mounts, err := buildVolumes(&ctx, v1.ReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(4))
		Expect(mounts).To(HaveLen(6))
		checkVolumeMounts(volumes, mounts)
	})

	t.Run("ExtraMount", func(t *testing.T) {
		g := NewWithT(t)
		RegisterFailHandler(g.Fail)
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
			},
		}
		volumes, mounts, err := buildVolumes(&ctx, v1.ReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(4))
		Expect(mounts).To(HaveLen(6))
		checkVolumeMounts(volumes, mounts)
	})

	t.Run("ExtraMountCollide", func(t *testing.T) {
		g := NewWithT(t)
		RegisterFailHandler(g.Fail)
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
			},
		}
		volumes, mounts, err := buildVolumes(&ctx, v1.ReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(3))
		Expect(mounts).To(HaveLen(5))
		checkVolumeMounts(volumes, mounts)
	})

	t.Run("TLSExtraMountCollide", func(t *testing.T) {
		g := NewWithT(t)
		RegisterFailHandler(g.Fail)
		ctx.Cluster = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.ClickHouseClusterSpec{
				Settings: v1.ClickHouseConfig{
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
						MountPath: "/etc/clickhouse-server/config.d/",
					}},
				},
			},
		}
		volumes, mounts, err := buildVolumes(&ctx, v1.ReplicaID{})
		Expect(err).To(Not(HaveOccurred()))
		Expect(volumes).To(HaveLen(4))
		Expect(mounts).To(HaveLen(6))
		checkVolumeMounts(volumes, mounts)
	})
}

func checkVolumeMounts(volumes []corev1.Volume, mounts []corev1.VolumeMount) {
	volumeMap := map[string]struct{}{
		PersistentVolumeName: {},
	}
	for _, volume := range volumes {
		Expect(volumeMap).NotTo(HaveKey(volume.Name))
		volumeMap[volume.Name] = struct{}{}
	}

	mountPaths := map[string]struct{}{}
	for _, mount := range mounts {
		Expect(mountPaths).NotTo(HaveKey(mount.MountPath))
		mountPaths[mount.MountPath] = struct{}{}
		Expect(volumeMap).To(HaveKey(mount.Name), "Mount %s is not in volumes", mount.Name)
	}
}
