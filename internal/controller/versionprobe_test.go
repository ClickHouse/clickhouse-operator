package controller

import (
	"context"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
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

// setupProbeTest creates a fake Controller, owner CR, and ResourceManager
// for testing VersionProbe() with a fake Kubernetes client.
func setupProbeTest() (ResourceManager, controllerutil.Logger) {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(v1.AddToScheme(scheme)).To(Succeed())

	builder := fake.NewClientBuilder().WithScheme(scheme)

	fakeClient := builder.Build()
	recorder := events.NewFakeRecorder(32)
	log := controllerutil.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	owner := &v1.ClickHouseCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test",
			UID:       "test-uid",
		},
	}

	cc := &fakeController{client: fakeClient, scheme: scheme, recorder: recorder}

	return NewResourceManager(cc, owner), log
}

// fakeController implements the Controller interface for unit tests.
type fakeController struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder events.EventRecorder
}

func (f *fakeController) GetClient() client.Client          { return f.client }
func (f *fakeController) GetScheme() *runtime.Scheme        { return f.scheme }
func (f *fakeController) GetRecorder() events.EventRecorder { return f.recorder }

// probeCfg returns a VersionProbeConfig with the given image and cache values.
func probeCfg(image, cachedVersion, cachedRevision string) VersionProbeConfig {
	return VersionProbeConfig{
		Binary: "clickhouse-server",
		ContainerTemplate: v1.ContainerTemplateSpec{
			Image: v1.ContainerImage{Repository: image, Tag: "latest"},
		},
		CachedVersion:  cachedVersion,
		CachedRevision: cachedRevision,
	}
}

var _ = Describe("VersionProbe caching", func() {
	It("should return cached version on cache hit without creating a Job", func(ctx context.Context) {
		rm, log := setupProbeTest()

		cfg := probeCfg("clickhouse/clickhouse-server", "", "")

		By("running the first probe to get the revision")

		revision, err := imageRevision(cfg)
		Expect(err).NotTo(HaveOccurred())

		By("setting up cache fields as if a previous probe succeeded")

		cfg.CachedVersion = "25.3.1.1"
		cfg.CachedRevision = revision

		result, err := rm.VersionProbe(ctx, log, cfg)
		Expect(err).NotTo(HaveOccurred())

		By("verifying it returned the cached version without creating a Job")
		Expect(result.Version).To(Equal("25.3.1.1"))
		Expect(result.Revision).To(Equal(revision))
		Expect(result.Pending).To(BeFalse())
		Expect(result.Completed()).To(BeTrue())

		By("verifying no Jobs were created")

		var jobs batchv1.JobList
		Expect(rm.ctrl.GetClient().List(ctx, &jobs, client.InNamespace("default"))).To(Succeed())
		Expect(jobs.Items).To(BeEmpty())
	})

	It("should miss cache when CachedVersion is empty even if revision matches", func(ctx context.Context) {
		rm, log := setupProbeTest()

		cfg := probeCfg("clickhouse/clickhouse-server", "", "")
		revision, err := imageRevision(cfg)
		Expect(err).NotTo(HaveOccurred())

		By("setting revision but leaving version empty (fresh CR)")

		cfg.CachedRevision = revision
		cfg.CachedVersion = ""

		result, err := rm.VersionProbe(ctx, log, cfg)
		Expect(err).NotTo(HaveOccurred())

		By("verifying probe was created (cache miss)")
		Expect(result.Pending).To(BeTrue())
		Expect(result.Version).To(BeEmpty())

		By("verifying a Job was created")

		var jobs batchv1.JobList
		Expect(rm.ctrl.GetClient().List(ctx, &jobs, client.InNamespace("default"))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
	})

	It("should miss cache when image changes", func(ctx context.Context) {
		rm, log := setupProbeTest()

		By("computing revision for the original image")

		originalCfg := probeCfg("clickhouse/clickhouse-server", "", "")
		originalRevision, err := imageRevision(originalCfg)
		Expect(err).NotTo(HaveOccurred())

		By("creating config with a different image but the old revision cached")

		cfg := probeCfg("custom-registry/clickhouse-server", "25.3.1.1", originalRevision)

		result, err := rm.VersionProbe(ctx, log, cfg)
		Expect(err).NotTo(HaveOccurred())

		By("verifying a new probe Job was created (cache miss due to image change)")
		Expect(result.Pending).To(BeTrue())

		var jobs batchv1.JobList
		Expect(rm.ctrl.GetClient().List(ctx, &jobs, client.InNamespace("default"))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
	})
})
