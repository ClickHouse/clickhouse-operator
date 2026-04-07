package controller

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

func TestOverrides(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Overrides Suite")
}

var _ = Describe("ApplyContainerTemplateOverrides", func() {
	Describe("Resources", func() {
		It("should deep-merge: user cpu limit overrides operator cpu, operator memory is preserved", func() {
			container, err := ApplyContainerTemplateOverrides(
				&corev1.Container{
					Name: "server",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				}, &v1.ContainerTemplateSpec{
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(container.Resources.Limits).To(HaveLen(2))
			Expect(container.Resources.Limits[corev1.ResourceCPU]).To(Equal(resource.MustParse("2")))
			Expect(container.Resources.Limits[corev1.ResourceMemory]).To(Equal(resource.MustParse("1Gi")))
		})

		It("should preserve operator resources when user Resources is empty", func() {
			container, err := ApplyContainerTemplateOverrides(
				&corev1.Container{
					Name: "server",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("500m"),
						},
					},
				},
				&v1.ContainerTemplateSpec{},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(container.Resources.Limits[corev1.ResourceCPU]).To(Equal(resource.MustParse("500m")))
		})
	})

	Describe("SecurityContext", func() {
		It("should preserve operator SecurityContext when user SecurityContext is nil", func() {
			container, err := ApplyContainerTemplateOverrides(
				&corev1.Container{
					Name: "server",
					SecurityContext: &corev1.SecurityContext{
						RunAsNonRoot: new(true),
					},
				},
				&v1.ContainerTemplateSpec{},
			)

			Expect(err).To(Not(HaveOccurred()))
			Expect(container.SecurityContext).NotTo(BeNil())
			Expect(container.SecurityContext.RunAsNonRoot).NotTo(BeNil())
			Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
		})

		It("should deep-merge: user fields added to operator SecurityContext", func() {
			container, err := ApplyContainerTemplateOverrides(
				&corev1.Container{
					Name: "server",
					SecurityContext: &corev1.SecurityContext{
						RunAsNonRoot: new(true),
					},
				},
				&v1.ContainerTemplateSpec{
					SecurityContext: &corev1.SecurityContext{
						RunAsUser: new(int64(1000)),
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(container.SecurityContext).NotTo(BeNil())
			Expect(container.SecurityContext.RunAsNonRoot).NotTo(BeNil())
			Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue(), "operator RunAsNonRoot should be preserved")
			Expect(container.SecurityContext.RunAsUser).NotTo(BeNil())
			Expect(*container.SecurityContext.RunAsUser).To(Equal(int64(1000)), "user RunAsUser should be applied")
		})
	})
})

var _ = Describe("ApplyPodTemplateOverrides", func() {
	Describe("Volumes", func() {
		It("should add a new user volume without corrupting existing operator volumes", func() {
			podSpec, err := ApplyPodTemplateOverrides(
				&corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "operator-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									DefaultMode: new(corev1.ConfigMapVolumeSourceDefaultMode),
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "my-configmap",
									},
								},
							},
						},
					},
				},
				&v1.PodTemplateSpec{
					Volumes: []corev1.Volume{
						{
							Name: "custom-data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(podSpec.Volumes).To(HaveLen(2))

			var customVol *corev1.Volume
			for i := range podSpec.Volumes {
				if podSpec.Volumes[i].Name == "custom-data" {
					customVol = &podSpec.Volumes[i]
				}
			}

			Expect(customVol).NotTo(BeNil(), "custom-data volume should be present")
			Expect(customVol.VolumeSource.EmptyDir).NotTo(BeNil(), "emptyDir should be set")
			Expect(customVol.VolumeSource.ConfigMap).To(BeNil(), "configMap must not bleed into emptyDir volume")
		})

		It("should replace an operator volume when the user provides one with the same name", func() {
			defaultMode := corev1.ConfigMapVolumeSourceDefaultMode
			podSpec, err := ApplyPodTemplateOverrides(
				&corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									DefaultMode:          &defaultMode,
									LocalObjectReference: corev1.LocalObjectReference{Name: "original"},
								},
							},
						},
					},
				},
				&v1.PodTemplateSpec{
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(podSpec.Volumes).To(HaveLen(1))
			Expect(podSpec.Volumes[0].VolumeSource.EmptyDir).NotTo(BeNil(), "user emptyDir should replace operator configMap")
			Expect(podSpec.Volumes[0].VolumeSource.ConfigMap).To(BeNil(), "original configMap must be nil")
		})
	})

	Describe("SecurityContext", func() {
		It("should preserve operator PodSecurityContext when user SecurityContext is nil", func() {
			podSpec, err := ApplyPodTemplateOverrides(
				&corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: new(int64(1000)),
					},
				},
				&v1.PodTemplateSpec{},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(podSpec.SecurityContext).NotTo(BeNil())
			Expect(podSpec.SecurityContext.FSGroup).NotTo(BeNil())
			Expect(*podSpec.SecurityContext.FSGroup).To(Equal(int64(1000)))
		})

		It("should deep-merge: user PodSecurityContext fields added to operator defaults", func() {
			podSpec, err := ApplyPodTemplateOverrides(
				&corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: new(int64(1000)),
					},
				},
				&v1.PodTemplateSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser: new(int64(500)),
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(podSpec.SecurityContext).NotTo(BeNil())
			Expect(podSpec.SecurityContext.FSGroup).NotTo(BeNil())
			Expect(*podSpec.SecurityContext.FSGroup).To(Equal(int64(1000)), "operator FSGroup should be preserved")
			Expect(podSpec.SecurityContext.RunAsUser).NotTo(BeNil())
			Expect(*podSpec.SecurityContext.RunAsUser).To(Equal(int64(500)), "user RunAsUser should be applied")
		})
	})
})
