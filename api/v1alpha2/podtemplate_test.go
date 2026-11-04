package v1alpha2

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var _ = Describe("PodTemplateSpec", func() {
	It("should accept a partial pod spec without containers", func() {
		raw := `{"metadata":{"labels":{"team":"db"}},"spec":{"nodeSelector":{"pool":"ch"},"priorityClassName":"high"}}`

		var tmpl PodTemplateSpec
		Expect(json.Unmarshal([]byte(raw), &tmpl)).To(Succeed())
		Expect(tmpl.Metadata.Labels).To(HaveKeyWithValue("team", "db"))
		Expect(tmpl.Spec.NodeSelector).To(HaveKeyWithValue("pool", "ch"))
		Expect(tmpl.Spec.PriorityClassName).To(Equal("high"))
		Expect(tmpl.Spec.Containers).To(BeEmpty())
	})

	It("should inline the template fields of a named template", func() {
		raw := `{"name":"highmem","metadata":{"labels":{"team":"db"}},"spec":{"priorityClassName":"high"}}`

		var tmpl NamedPodTemplate
		Expect(json.Unmarshal([]byte(raw), &tmpl)).To(Succeed())
		Expect(tmpl.Name).To(Equal("highmem"))
		Expect(tmpl.Metadata.Labels).To(HaveKeyWithValue("team", "db"))
		Expect(tmpl.Spec.PriorityClassName).To(Equal("high"))
	})
})

var _ = Describe("SchedulingSpec", func() {
	It("should unmarshal the cluster scheduling settings", func() {
		raw := `{"zoneKey":"topology.kubernetes.io/zone","minDomains":3,"nodeHostnameKey":"kubernetes.io/hostname"}`

		var scheduling SchedulingSpec
		Expect(json.Unmarshal([]byte(raw), &scheduling)).To(Succeed())
		Expect(*scheduling.ZoneKey).To(Equal("topology.kubernetes.io/zone"))
		Expect(*scheduling.MinDomains).To(BeEquivalentTo(3))
		Expect(*scheduling.NodeHostnameKey).To(Equal("kubernetes.io/hostname"))
	})
})

var _ = Describe("PodTemplate", func() {
	templates := []NamedPodTemplate{
		{Name: "default", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{PriorityClassName: "normal"}}},
		{Name: "highmem", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{PriorityClassName: "high"}}},
	}

	It("should return the referenced ClickHouseCluster template", func() {
		spec := ClickHouseClusterSpec{PodTemplates: templates, PodTemplateName: "highmem"}
		Expect(spec.PodTemplate()).To(BeIdenticalTo(&spec.PodTemplates[1].PodTemplateSpec))
	})

	It("should return nil when no template is referenced", func() {
		spec := ClickHouseClusterSpec{PodTemplates: templates}
		Expect(spec.PodTemplate()).To(BeNil())
	})

	It("should return nil when the referenced template does not exist", func() {
		spec := ClickHouseClusterSpec{PodTemplates: templates, PodTemplateName: "missing"}
		Expect(spec.PodTemplate()).To(BeNil())
	})
})

var _ = Describe("VersionProbeTemplate", func() {
	It("should accept a native pod spec without containers", func() {
		raw := `{"metadata":{"labels":{"job":"probe"}},"spec":{"ttlSecondsAfterFinished":60,"backoffLimit":2,` +
			`"template":{"spec":{"priorityClassName":"low","affinity":{"nodeAffinity":{}}}}}}`

		var tmpl VersionProbeTemplate
		Expect(json.Unmarshal([]byte(raw), &tmpl)).To(Succeed())
		Expect(tmpl.Metadata.Labels).To(HaveKeyWithValue("job", "probe"))
		Expect(*tmpl.Spec.TTLSecondsAfterFinished).To(BeEquivalentTo(60))
		Expect(*tmpl.Spec.BackoffLimit).To(BeEquivalentTo(2))
		Expect(tmpl.Spec.Template.Spec.PriorityClassName).To(Equal("low"))
		Expect(tmpl.Spec.Template.Spec.Affinity).NotTo(BeNil())
		Expect(tmpl.Spec.Template.Spec.Containers).To(BeEmpty())
	})

	It("should keep the batchv1.Job JSON shape for strategic merge patch", func() {
		tmpl := VersionProbeTemplate{Spec: VersionProbeJobSpec{Template: VersionProbePodTemplate{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: VersionProbeContainerName}}},
		}}}

		data, err := json.Marshal(tmpl)
		Expect(err).NotTo(HaveOccurred())

		var job batchv1.Job
		Expect(json.Unmarshal(data, &job)).To(Succeed())
		Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(job.Spec.Template.Spec.Containers[0].Name).To(Equal(VersionProbeContainerName))
	})
})

var _ = Describe("WithDefaults", func() {
	userTemplates := func(name string) []NamedPodTemplate {
		return []NamedPodTemplate{{Name: "default", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  name,
				Image: "example.com/custom:1",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
				},
			}},
		}}}}
	}

	It("should default ClickHouseCluster scalars and leave the pod templates untouched", func() {
		spec := ClickHouseClusterSpec{PodTemplates: userTemplates("clickhouse"), PodTemplateName: "default"}
		expected := spec.DeepCopy().PodTemplates

		spec.WithDefaults()

		Expect(*spec.Replicas).To(BeEquivalentTo(DefaultClickHouseReplicaCount))
		Expect(*spec.Shards).To(BeEquivalentTo(DefaultClickHouseShardCount))
		Expect(spec.Settings.Logger.Level).To(Equal(DefaultLogLevel))
		Expect(spec.PodTemplates).To(Equal(expected))
	})

	It("should not inject pod templates into an empty ClickHouseCluster", func() {
		spec := ClickHouseClusterSpec{}
		spec.WithDefaults()
		Expect(spec.PodTemplates).To(BeEmpty())
		Expect(spec.PodTemplateName).To(BeEmpty())
	})

	It("should default KeeperCluster scalars and leave the pod template untouched", func() {
		spec := KeeperClusterSpec{PodTemplate: &userTemplates("keeper")[0].PodTemplateSpec}
		expected := spec.DeepCopy().PodTemplate

		spec.WithDefaults()

		Expect(*spec.Replicas).To(BeEquivalentTo(DefaultKeeperReplicaCount))
		Expect(spec.Settings.Logger.Level).To(Equal(DefaultLogLevel))
		Expect(spec.PodTemplate).To(Equal(expected))
	})

	It("should default data volume access mode", func() {
		spec := KeeperClusterSpec{DataVolumeClaimTemplate: &DataVolumeClaimTemplate{}}
		spec.WithDefaults()
		Expect(spec.DataVolumeClaimTemplate.Spec.AccessModes).To(ConsistOf(DefaultAccessMode))
	})

	It("should default access modes of named data volume claim templates", func() {
		spec := ClickHouseClusterSpec{DataVolumeClaimTemplates: []NamedDataVolumeClaimTemplate{
			{Name: "empty"},
			{Name: "explicit", DataVolumeClaimTemplate: DataVolumeClaimTemplate{Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
			}}},
		}}
		spec.WithDefaults()
		Expect(spec.DataVolumeClaimTemplates[0].Spec.AccessModes).To(ConsistOf(DefaultAccessMode))
		Expect(spec.DataVolumeClaimTemplates[1].Spec.AccessModes).To(ConsistOf(corev1.ReadWriteOncePod))
	})

	It("should default access modes of additional volume claim template sets", func() {
		spec := ClickHouseClusterSpec{AdditionalVolumeClaimTemplateSets: []NamedVolumeClaimTemplateSet{{
			Name: "jbod",
			Templates: []PersistentVolumeClaimTemplate{
				{NamedTemplateMeta: NamedTemplateMeta{Name: "empty"}},
				{NamedTemplateMeta: NamedTemplateMeta{Name: "explicit"}, Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
				}},
			},
		}}}
		spec.WithDefaults()
		templates := spec.AdditionalVolumeClaimTemplateSets[0].Templates
		Expect(templates[0].Spec.AccessModes).To(ConsistOf(DefaultAccessMode))
		Expect(templates[1].Spec.AccessModes).To(ConsistOf(corev1.ReadWriteOncePod))
	})
})
