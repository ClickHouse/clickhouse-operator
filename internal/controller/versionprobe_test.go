package controller

import (
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
	It("should add pod labels and annotations without affecting Job-level metadata", func() {
		job := baseJob()
		override := &v1.VersionProbeOverride{
			PodAnnotations: map[string]string{
				"sidecar.istio.io/inject": "false",
			},
			PodLabels: map[string]string{
				"probe-label": "probe-value",
			},
		}

		applyVersionProbeOverrides(&job, override)

		By("verifying containers are preserved")
		Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(job.Spec.Template.Spec.Containers[0].Name).To(Equal("version-probe"))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("clickhouse/clickhouse-server:latest"))

		By("verifying base resources are preserved")
		Expect(job.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("100m"))
		Expect(job.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("128Mi"))

		By("verifying override annotations are applied to Pod only")
		Expect(job.Spec.Template.Annotations).To(HaveKeyWithValue("sidecar.istio.io/inject", "false"))
		Expect(job.Annotations).NotTo(HaveKey("sidecar.istio.io/inject"))

		By("verifying override labels are applied to Pod only")
		Expect(job.Spec.Template.Labels).To(HaveKeyWithValue("probe-label", "probe-value"))
		Expect(job.Labels).NotTo(HaveKey("probe-label"))

		By("verifying existing cluster labels/annotations are preserved")
		Expect(job.Labels).To(HaveKeyWithValue("app", "test-cluster"))
		Expect(job.Annotations).To(HaveKeyWithValue("cluster-annotation", "cluster-value"))
		Expect(job.Spec.Template.Labels).To(HaveKeyWithValue("app", "test-cluster"))
		Expect(job.Spec.Template.Annotations).To(HaveKeyWithValue("cluster-annotation", "cluster-value"))
	})

	It("should apply TTLSecondsAfterFinished override", func() {
		job := baseJob()
		override := &v1.VersionProbeOverride{
			TTLSecondsAfterFinished: new(int32(300)),
		}

		applyVersionProbeOverrides(&job, override)

		By("verifying ttlSecondsAfterFinished is set")
		Expect(job.Spec.TTLSecondsAfterFinished).NotTo(BeNil())
		Expect(*job.Spec.TTLSecondsAfterFinished).To(Equal(int32(300)))

		By("verifying containers are preserved")
		Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(job.Spec.Template.Spec.Containers[0].Name).To(Equal("version-probe"))

		By("verifying backoffLimit is preserved")
		Expect(job.Spec.BackoffLimit).NotTo(BeNil())
		Expect(*job.Spec.BackoffLimit).To(Equal(int32(0)))
	})

	It("should merge container resource overrides without wiping other container fields", func() {
		job := baseJob()
		override := &v1.VersionProbeOverride{
			Resources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"),
				},
			},
		}

		applyVersionProbeOverrides(&job, override)

		By("verifying CPU limit is overridden")

		container := job.Spec.Template.Spec.Containers[0]
		Expect(container.Resources.Limits.Cpu().String()).To(Equal("500m"))

		By("verifying memory limit is preserved from base")
		Expect(container.Resources.Limits.Memory().String()).To(Equal("128Mi"))

		By("verifying container command is preserved")
		Expect(container.Command).To(Equal([]string{"sh", "-c", "clickhouse-server --version"}))

		By("verifying container image is preserved")
		Expect(container.Image).To(Equal("clickhouse/clickhouse-server:latest"))
	})
})
