package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// baseJob builds a minimal operator-generated Job for testing overrides.
func baseJob() batchv1.Job {
	return batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-version-probe",
			Namespace: "default",
			Labels: map[string]string{
				"app":  "test-cluster",
				"role": "version-probe",
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
						"app":  "test-cluster",
						"role": "version-probe",
					},
					Annotations: map[string]string{
						"cluster-annotation": "cluster-value",
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "version-probe",
							Image:   "clickhouse/clickhouse-server:latest",
							Command: []string{"sh", "-c", "clickhouse-server --version"},
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

var _ = Describe("applyVersionProbeOverrides", func() {
	It("should preserve containers and base spec when override has only metadata", func() {
		job := baseJob()
		override := &batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"sidecar.istio.io/inject": "false",
				},
				Labels: map[string]string{
					"probe-label": "probe-value",
				},
			},
		}

		result, err := applyVersionProbeOverrides(job, override)
		Expect(err).NotTo(HaveOccurred())

		By("verifying containers are preserved")
		Expect(result.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(result.Spec.Template.Spec.Containers[0].Name).To(Equal("version-probe"))
		Expect(result.Spec.Template.Spec.Containers[0].Image).To(Equal("clickhouse/clickhouse-server:latest"))

		By("verifying base resources are preserved")
		Expect(result.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("100m"))
		Expect(result.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("128Mi"))

		By("verifying override annotations are applied")
		Expect(result.Annotations).To(HaveKeyWithValue("sidecar.istio.io/inject", "false"))
		Expect(result.Spec.Template.Annotations).To(HaveKeyWithValue("sidecar.istio.io/inject", "false"))

		By("verifying override labels are applied")
		Expect(result.Labels).To(HaveKeyWithValue("probe-label", "probe-value"))
		Expect(result.Spec.Template.Labels).To(HaveKeyWithValue("probe-label", "probe-value"))

		By("verifying existing cluster labels/annotations are preserved")
		Expect(result.Labels).To(HaveKeyWithValue("app", "test-cluster"))
		Expect(result.Annotations).To(HaveKeyWithValue("cluster-annotation", "cluster-value"))
	})

	It("should apply spec-level overrides via SMP (ttlSecondsAfterFinished)", func() {
		job := baseJob()
		override := &batchv1.JobTemplateSpec{
			Spec: batchv1.JobSpec{
				TTLSecondsAfterFinished: new(int32(300)),
			},
		}

		result, err := applyVersionProbeOverrides(job, override)
		Expect(err).NotTo(HaveOccurred())

		By("verifying ttlSecondsAfterFinished is set")
		Expect(result.Spec.TTLSecondsAfterFinished).NotTo(BeNil())
		Expect(*result.Spec.TTLSecondsAfterFinished).To(Equal(int32(300)))

		By("verifying containers are preserved through SMP")
		Expect(result.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(result.Spec.Template.Spec.Containers[0].Name).To(Equal("version-probe"))

		By("verifying backoffLimit is preserved")
		Expect(result.Spec.BackoffLimit).NotTo(BeNil())
		Expect(*result.Spec.BackoffLimit).To(Equal(int32(0)))
	})

	It("should merge container resource overrides without wiping other container fields", func() {
		job := baseJob()
		override := &batchv1.JobTemplateSpec{
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "version-probe",
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

		result, err := applyVersionProbeOverrides(job, override)
		Expect(err).NotTo(HaveOccurred())

		By("verifying CPU limit is overridden")

		container := result.Spec.Template.Spec.Containers[0]
		Expect(container.Resources.Limits.Cpu().String()).To(Equal("500m"))

		By("verifying memory limit is preserved from base")
		Expect(container.Resources.Limits.Memory().String()).To(Equal("128Mi"))

		By("verifying container command is preserved")
		Expect(container.Command).To(Equal([]string{"sh", "-c", "clickhouse-server --version"}))

		By("verifying container image is preserved")
		Expect(container.Image).To(Equal("clickhouse/clickhouse-server:latest"))
	})

	It("should add tolerations via SMP without affecting existing pod fields", func() {
		job := baseJob()
		override := &batchv1.JobTemplateSpec{
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Tolerations: []corev1.Toleration{
							{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "probe", Effect: corev1.TaintEffectNoSchedule},
						},
					},
				},
			},
		}

		result, err := applyVersionProbeOverrides(job, override)
		Expect(err).NotTo(HaveOccurred())

		By("verifying toleration is added")
		Expect(result.Spec.Template.Spec.Tolerations).To(ContainElement(corev1.Toleration{
			Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "probe", Effect: corev1.TaintEffectNoSchedule,
		}))

		By("verifying containers are still intact")
		Expect(result.Spec.Template.Spec.Containers).To(HaveLen(1))

		By("verifying restart policy is preserved")
		Expect(result.Spec.Template.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
	})
})
