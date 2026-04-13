package controller

import (
	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

// baseJob builds a minimal operator-generated Job for testing overrides.
func baseJob() batchv1.Job {
	return batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-version-probe",
			Namespace: "default",
			Labels: map[string]string{
				"cluster-label": "cluster-value",
			},
			Annotations: map[string]string{
				"cluster-annotation": "cluster-value",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: new(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"cluster-label": "cluster-value",
					},
					Annotations: map[string]string{
						"cluster-annotation": "cluster-value",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: new(int64(1000)),
					},
					Containers: []corev1.Container{
						{
							Name:    v1.VersionProbeContainerName,
							Image:   "clickhouse/clickhouse-server:latest",
							Command: []string{"sh", "-c", "clickhouse-server --version"},
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot: new(true),
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
}

var _ = Describe("patchResource with jobSchema (version probe overrides)", func() {
	patchJob := func(job *batchv1.Job, override *v1.VersionProbeTemplate) (batchv1.Job, error) {
		return patchResource(job, override, jobSchema)
	}

	It("should apply pod labels and annotations without affecting Job-level metadata", func() {
		job := baseJob()
		override := &v1.VersionProbeTemplate{
			Spec: v1.VersionProbeJobSpec{
				Template: v1.VersionProbePodTemplate{
					Metadata: v1.TemplateMeta{
						Annotations: map[string]string{
							"sidecar.istio.io/inject": "false",
						},
						Labels: map[string]string{
							"probe-label": "probe-value",
						},
					},
				},
			},
		}

		merged, err := patchJob(&job, override)
		Expect(err).NotTo(HaveOccurred())

		By("verifying override annotations are applied to Pod only")
		Expect(merged.Spec.Template.Annotations).To(HaveKeyWithValue("sidecar.istio.io/inject", "false"))
		Expect(merged.Annotations).NotTo(HaveKey("sidecar.istio.io/inject"))

		By("verifying override labels are applied to Pod only")
		Expect(merged.Spec.Template.Labels).To(HaveKeyWithValue("probe-label", "probe-value"))
		Expect(merged.Labels).NotTo(HaveKey("probe-label"))

		By("verifying existing cluster labels/annotations are preserved")
		Expect(merged.Labels).To(HaveKeyWithValue("cluster-label", "cluster-value"))
		Expect(merged.Annotations).To(HaveKeyWithValue("cluster-annotation", "cluster-value"))
		Expect(merged.Spec.Template.Labels).To(HaveKeyWithValue("cluster-label", "cluster-value"))
		Expect(merged.Spec.Template.Annotations).To(HaveKeyWithValue("cluster-annotation", "cluster-value"))
	})

	It("should apply Job-level metadata (labels/annotations)", func() {
		job := baseJob()
		override := &v1.VersionProbeTemplate{
			Metadata: v1.TemplateMeta{
				Labels: map[string]string{
					"custom-job-label": "job-value",
				},
				Annotations: map[string]string{
					"custom-job-annotation": "job-value",
				},
			},
		}

		merged, err := patchJob(&job, override)
		Expect(err).NotTo(HaveOccurred())

		By("verifying Job-level labels/annotations are applied")
		Expect(merged.Labels).To(HaveKeyWithValue("custom-job-label", "job-value"))
		Expect(merged.Annotations).To(HaveKeyWithValue("custom-job-annotation", "job-value"))

		By("verifying Pod template is not affected")
		Expect(merged.Spec.Template.Labels).NotTo(HaveKey("custom-job-label"))
	})

	It("should apply TTLSecondsAfterFinished override", func() {
		job := baseJob()
		override := &v1.VersionProbeTemplate{
			Spec: v1.VersionProbeJobSpec{
				TTLSecondsAfterFinished: new(int32(300)),
			},
		}

		merged, err := patchJob(&job, override)
		Expect(err).NotTo(HaveOccurred())

		Expect(merged.Spec.TTLSecondsAfterFinished).NotTo(BeNil())
		Expect(*merged.Spec.TTLSecondsAfterFinished).To(Equal(int32(300)))
	})

	It("should deep-merge container resources via SMP", func() {
		job := baseJob()
		override := &v1.VersionProbeTemplate{
			Spec: v1.VersionProbeJobSpec{
				Template: v1.VersionProbePodTemplate{
					Spec: v1.VersionProbePodSpec{
						Containers: []v1.VersionProbeContainer{
							{
								Name: v1.VersionProbeContainerName,
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("500m"),
									},
								},
							},
						},
					},
				},
			},
		}

		merged, err := patchJob(&job, override)
		Expect(err).NotTo(HaveOccurred())

		container := merged.Spec.Template.Spec.Containers[0]

		By("verifying CPU limit is overridden")
		Expect(container.Resources.Limits.Cpu().String()).To(Equal("500m"))

		By("verifying memory limit is preserved")
		Expect(container.Resources.Limits.Memory().String()).To(Equal("128Mi"))

		By("verifying container command is preserved")
		Expect(container.Image).To(Equal("clickhouse/clickhouse-server:latest"))
		Expect(container.Command).To(Equal([]string{"sh", "-c", "clickhouse-server --version"}))
	})

	It("should deep-merge securityContext via SMP", func() {
		job := baseJob()
		override := &v1.VersionProbeTemplate{
			Spec: v1.VersionProbeJobSpec{
				Template: v1.VersionProbePodTemplate{
					Spec: v1.VersionProbePodSpec{
						SecurityContext: &corev1.PodSecurityContext{
							RunAsUser: new(int64(500)),
						},
						Containers: []v1.VersionProbeContainer{
							{
								Name: v1.VersionProbeContainerName,
								SecurityContext: &corev1.SecurityContext{
									RunAsUser: new(int64(1000)),
								},
							},
						},
					},
				},
			},
		}

		merged, err := patchJob(&job, override)
		Expect(err).NotTo(HaveOccurred())

		By("verifying user RunAsUser is applied")
		Expect(merged.Spec.Template.Spec.SecurityContext.RunAsUser).NotTo(BeNil())
		Expect(*merged.Spec.Template.Spec.SecurityContext.RunAsUser).To(Equal(int64(500)))

		By("verifying operator FSGroup is preserved via SMP deep-merge")
		Expect(merged.Spec.Template.Spec.SecurityContext.FSGroup).NotTo(BeNil())
		Expect(*merged.Spec.Template.Spec.SecurityContext.FSGroup).To(Equal(int64(1000)))

		container := merged.Spec.Template.Spec.Containers[0]

		By("verifying user RunAsUser is applied")
		Expect(container.SecurityContext.RunAsUser).NotTo(BeNil())
		Expect(*container.SecurityContext.RunAsUser).To(Equal(int64(1000)))

		By("verifying operator RunAsNonRoot is preserved via SMP deep-merge")
		Expect(container.SecurityContext.RunAsNonRoot).NotTo(BeNil())
		Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
	})

	It("should be a no-op when override is empty", func() {
		job := baseJob()
		original := baseJob()
		override := &v1.VersionProbeTemplate{}

		merged, err := patchJob(&job, override)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmp.Diff(merged, original)).To(BeEmpty())
	})

	It("should apply nodeSelector and tolerations overrides", func() {
		job := baseJob()
		override := &v1.VersionProbeTemplate{
			Spec: v1.VersionProbeJobSpec{
				Template: v1.VersionProbePodTemplate{
					Spec: v1.VersionProbePodSpec{
						NodeSelector: map[string]string{"pool": "clickhouse"},
						Tolerations: []corev1.Toleration{
							{Key: "dedicated", Value: "clickhouse", Effect: corev1.TaintEffectNoSchedule},
						},
					},
				},
			},
		}

		merged, err := patchJob(&job, override)
		Expect(err).NotTo(HaveOccurred())

		Expect(merged.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("pool", "clickhouse"))
		Expect(merged.Spec.Template.Spec.Tolerations).To(ContainElement(corev1.Toleration{
			Key: "dedicated", Value: "clickhouse", Effect: corev1.TaintEffectNoSchedule,
		}))
	})
})
