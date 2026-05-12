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

		It("should fully replace operator SecurityContext when user provides one", func() {
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
			Expect(container.SecurityContext.RunAsNonRoot).To(BeNil(),
				"operator RunAsNonRoot must NOT survive: a user-provided SecurityContext fully replaces operator defaults")
			Expect(container.SecurityContext.RunAsUser).NotTo(BeNil())
			Expect(*container.SecurityContext.RunAsUser).To(Equal(int64(1000)), "user RunAsUser should be applied")
		})

		Describe("Capabilities", func() {
			// Repro for #174: without whole-SC replacement, SMP deep-merges Capabilities
			// and operator-defaulted Add survives the user's Drop:[ALL], breaking `restricted`.
			It("should let user Drop:[ALL] override operator-defaulted Add", func() {
				container, err := ApplyContainerTemplateOverrides(
					&corev1.Container{
						Name: "server",
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
							},
						},
					},
					&v1.ContainerTemplateSpec{
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
					},
				)

				Expect(err).ToNot(HaveOccurred())
				Expect(container.SecurityContext).NotTo(BeNil())
				Expect(container.SecurityContext.Capabilities).NotTo(BeNil())
				Expect(container.SecurityContext.Capabilities.Drop).To(Equal([]corev1.Capability{"ALL"}))
				Expect(container.SecurityContext.Capabilities.Add).To(BeEmpty(),
					"operator-defaulted Add must not survive when the user sets Drop:[ALL]")
			})

			It("should let user-set Add replace operator-defaulted Add", func() {
				container, err := ApplyContainerTemplateOverrides(
					&corev1.Container{
						Name: "server",
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
							},
						},
					},
					&v1.ContainerTemplateSpec{
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"NET_BIND_SERVICE"},
							},
						},
					},
				)

				Expect(err).ToNot(HaveOccurred())
				Expect(container.SecurityContext.Capabilities).NotTo(BeNil())
				Expect(container.SecurityContext.Capabilities.Add).To(Equal([]corev1.Capability{"NET_BIND_SERVICE"}),
					"user Add list should replace, not merge with, operator defaults")
			})

			It("should drop operator capabilities when user-provided SecurityContext omits them", func() {
				// Whole-SC replacement: operator caps don't survive even when the user
				// only sets unrelated fields.
				container, err := ApplyContainerTemplateOverrides(
					&corev1.Container{
						Name: "server",
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
							},
						},
					},
					&v1.ContainerTemplateSpec{
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot: new(true),
						},
					},
				)

				Expect(err).ToNot(HaveOccurred())
				Expect(container.SecurityContext.Capabilities).To(BeNil(),
					"operator capabilities must NOT survive when the user provides any SecurityContext")
				Expect(container.SecurityContext.RunAsNonRoot).NotTo(BeNil())
				Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
			})

			It("should preserve operator capabilities when user SecurityContext is nil", func() {
				container, err := ApplyContainerTemplateOverrides(
					&corev1.Container{
						Name: "server",
						SecurityContext: &corev1.SecurityContext{
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
							},
						},
					},
					&v1.ContainerTemplateSpec{},
				)

				Expect(err).ToNot(HaveOccurred())
				Expect(container.SecurityContext.Capabilities).NotTo(BeNil())
				Expect(container.SecurityContext.Capabilities.Add).To(Equal(
					[]corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"}),
					"operator capabilities must survive when the user does not provide SecurityContext at all")
			})
		})

		It("should not mutate the caller's container", func() {
			base := &corev1.Container{
				Name: "server",
				SecurityContext: &corev1.SecurityContext{
					Capabilities: &corev1.Capabilities{
						Add: []corev1.Capability{"IPC_LOCK", "PERFMON", "SYS_PTRACE"},
					},
				},
			}
			patch := &v1.ContainerTemplateSpec{
				SecurityContext: &corev1.SecurityContext{
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
			}

			baseCopy := base.DeepCopy()
			patched, err := ApplyContainerTemplateOverrides(base, patch)

			Expect(err).ToNot(HaveOccurred())
			Expect(base).To(Equal(baseCopy), "caller's container should be untouched")
			Expect(patched.SecurityContext).To(Equal(patch.SecurityContext))
		})
	})

	Describe("Probes", func() {
		It("should preserve operator liveness probe when user LivenessProbe is nil", func() {
			container, err := ApplyContainerTemplateOverrides(
				&corev1.Container{
					Name: "server",
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{},
						},
					},
				},
				&v1.ContainerTemplateSpec{},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(container.LivenessProbe).NotTo(BeNil())
			Expect(container.LivenessProbe.Exec).NotTo(BeNil())
		})

		It("should replace operator liveness probe when user provides a httpGet probe", func() {
			container, err := ApplyContainerTemplateOverrides(
				&corev1.Container{
					Name: "server",
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{},
						},
					},
				},
				&v1.ContainerTemplateSpec{
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{},
						},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(container.LivenessProbe).NotTo(BeNil())
			Expect(container.LivenessProbe.HTTPGet).NotTo(BeNil(), "user httpGet probe should replace operator exec probe")
			Expect(container.LivenessProbe.Exec).To(BeNil(), "operator exec probe must be removed")
		})

		It("should replace operator readiness probe when user provides a httpGet probe", func() {
			container, err := ApplyContainerTemplateOverrides(
				&corev1.Container{
					Name: "server",
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{},
						},
					},
				},
				&v1.ContainerTemplateSpec{
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{},
						},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(container.ReadinessProbe).NotTo(BeNil())
			Expect(container.ReadinessProbe.HTTPGet).NotTo(BeNil(), "user httpGet probe should replace operator exec probe")
			Expect(container.ReadinessProbe.Exec).To(BeNil(), "operator exec probe must be removed")
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

		It("should fully replace operator PodSecurityContext when user provides one", func() {
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
			Expect(podSpec.SecurityContext.FSGroup).To(BeNil(),
				"operator FSGroup must NOT survive: a user-provided PodSecurityContext fully replaces operator defaults")
			Expect(podSpec.SecurityContext.RunAsUser).NotTo(BeNil())
			Expect(*podSpec.SecurityContext.RunAsUser).To(Equal(int64(500)), "user RunAsUser should be applied")
		})

		It("should not mutate the caller's PodSpec", func() {
			base := &corev1.PodSpec{
				SecurityContext: &corev1.PodSecurityContext{FSGroup: new(int64(1000))},
				Volumes: []corev1.Volume{
					{Name: "operator-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				},
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
							{Weight: 1, Preference: corev1.NodeSelectorTerm{}},
						},
					},
				},
			}
			patch := &v1.PodTemplateSpec{
				SecurityContext: &corev1.PodSecurityContext{
					RunAsUser: new(int64(500)),
				},
				Volumes: []corev1.Volume{
					{Name: "operator-vol", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}},
				},
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
							{Weight: 2, Preference: corev1.NodeSelectorTerm{}},
						},
					},
				},
			}

			baseCopy := base.DeepCopy()
			patched, err := ApplyPodTemplateOverrides(base, patch)

			Expect(err).ToNot(HaveOccurred())
			Expect(base).To(Equal(baseCopy), "caller's PodSpec should be untouched")
			Expect(patched.SecurityContext).To(Equal(patch.SecurityContext))
		})
	})

	Describe("InitContainers", func() {
		It("should add user init containers", func() {
			podSpec, err := ApplyPodTemplateOverrides(
				&corev1.PodSpec{
					Containers: []corev1.Container{{Name: "server"}},
				},
				&v1.PodTemplateSpec{
					InitContainers: []corev1.Container{
						{Name: "user-init", Image: "busybox"},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(podSpec.InitContainers).To(HaveLen(1))
			Expect(podSpec.InitContainers[0].Name).To(Equal("user-init"))
			Expect(podSpec.InitContainers[0].Image).To(Equal("busybox"))
		})

		It("should preserve existing init containers", func() {
			podSpec, err := ApplyPodTemplateOverrides(
				&corev1.PodSpec{
					InitContainers: []corev1.Container{
						{Name: "operator-init", Image: "busybox"},
					},
				},
				&v1.PodTemplateSpec{
					InitContainers: []corev1.Container{
						{Name: "user-init", Image: "busybox"},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(podSpec.InitContainers).To(ContainElements(
				corev1.Container{Name: "operator-init", Image: "busybox"},
				corev1.Container{Name: "user-init", Image: "busybox"},
			))
		})

		It("should merge containers with the same name", func() {
			podSpec, err := ApplyPodTemplateOverrides(
				&corev1.PodSpec{
					InitContainers: []corev1.Container{
						{Name: "operator-init", Image: "busybox"},
					},
				},
				&v1.PodTemplateSpec{
					InitContainers: []corev1.Container{
						{Name: "operator-init", Image: "another"},
					},
				},
			)

			Expect(err).ToNot(HaveOccurred())
			Expect(podSpec.InitContainers).To(HaveLen(1))
			Expect(podSpec.InitContainers[0].Name).To(Equal("operator-init"))
			Expect(podSpec.InitContainers[0].Image).To(Equal("another"))
		})
	})
})
