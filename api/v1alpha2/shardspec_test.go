package v1alpha2

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("ShardSpecs", func() {
	newCluster := func(spec ClickHouseClusterSpec) *ClickHouseCluster {
		return &ClickHouseCluster{Name: "test", Namespace: "ns", Spec: spec}
	}

	Describe("count mode", func() {
		It("should default to a single shard with three replicas", func() {
			cluster := newCluster(ClickHouseClusterSpec{})
			Expect(cluster.ShardNames()).To(Equal([]string{"0"}))
			Expect(cluster.ShardReplicas("0")).To(BeEquivalentTo(DefaultClickHouseReplicaCount))
			Expect(cluster.TotalReplicas()).To(BeEquivalentTo(DefaultClickHouseReplicaCount))
		})

		It("should name shards by index and keep resource names", func() {
			cluster := newCluster(ClickHouseClusterSpec{Shards: new(int32(2)), Replicas: new(int32(3))})
			Expect(cluster.ShardNames()).To(Equal([]string{"0", "1"}))
			Expect(slices.Collect(cluster.ReplicaIDs())).To(Equal([]ClickHouseReplicaID{
				{Shard: "0", Index: 0}, {Shard: "0", Index: 1}, {Shard: "0", Index: 2},
				{Shard: "1", Index: 0}, {Shard: "1", Index: 1}, {Shard: "1", Index: 2},
			}))
			Expect(cluster.TotalReplicas()).To(BeEquivalentTo(6))

			id := ClickHouseReplicaID{Shard: "1", Index: 2}
			Expect(cluster.StatefulSetNameByReplicaID(id)).To(Equal("test-clickhouse-1-2"))
			Expect(cluster.ConfigMapNameByReplicaID(id)).To(Equal("test-clickhouse-1-2"))
			Expect(cluster.InternalServiceNameByReplicaID(id)).To(Equal("test-clickhouse-internal-1-2"))
			Expect(cluster.PodDisruptionBudgetNameByShard("1")).To(Equal("test-clickhouse-1"))
		})
	})

	Describe("detailed mode", func() {
		var cluster *ClickHouseCluster

		BeforeEach(func() {
			cluster = newCluster(ClickHouseClusterSpec{ShardSpecs: []ClickHouseShardSpec{
				{Name: "b", Replicas: new(int32(1))},
				{Name: "10", Replicas: new(int32(2))},
				{Name: "a", ReplicaSpecs: []ClickHouseReplicaSpec{{}, {}, {}}},
				{Name: "2", Replicas: new(int32(1))},
			}})
		})

		It("should order shards numerically first, then lexicographically", func() {
			Expect(cluster.ShardNames()).To(Equal([]string{"2", "10", "a", "b"}))
			Expect(cluster.Shards()).To(BeEquivalentTo(4))
		})

		It("should take replica counts from replicas or replicaSpecs", func() {
			Expect(cluster.ShardReplicas("10")).To(BeEquivalentTo(2))
			Expect(cluster.ShardReplicas("a")).To(BeEquivalentTo(3))
			Expect(cluster.ShardReplicas("missing")).To(BeEquivalentTo(0))
			Expect(cluster.TotalReplicas()).To(BeEquivalentTo(7))
		})

		It("should iterate replicas of every shard in shard order", func() {
			Expect(slices.Collect(cluster.ReplicaIDs())).To(Equal([]ClickHouseReplicaID{
				{Shard: "2", Index: 0},
				{Shard: "10", Index: 0}, {Shard: "10", Index: 1},
				{Shard: "a", Index: 0}, {Shard: "a", Index: 1}, {Shard: "a", Index: 2},
				{Shard: "b", Index: 0},
			}))
		})

		It("should use the shard name in resource names", func() {
			id := ClickHouseReplicaID{Shard: "a", Index: 1}
			Expect(cluster.StatefulSetNameByReplicaID(id)).To(Equal("test-clickhouse-a-1"))
			Expect(cluster.PodDisruptionBudgetNameByShard("a")).To(Equal("test-clickhouse-a"))
		})
	})

	Describe("PodTemplateFor", func() {
		templates := []NamedPodTemplate{
			{Name: "cluster", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{PriorityClassName: "cluster"}}},
			{Name: "shard", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{PriorityClassName: "shard"}}},
			{Name: "replica", PodTemplateSpec: PodTemplateSpec{Spec: corev1.PodSpec{PriorityClassName: "replica"}}},
		}

		cluster := newCluster(ClickHouseClusterSpec{
			PodTemplates:    templates,
			PodTemplateName: "cluster",
			ShardSpecs: []ClickHouseShardSpec{
				{Name: "plain", Replicas: new(int32(1))},
				{Name: "custom", PodTemplateName: "shard", ReplicaSpecs: []ClickHouseReplicaSpec{
					{}, {PodTemplateName: "replica"}, {PodTemplateName: "missing"},
				}},
			},
		})

		DescribeTable("should select the most specific template",
			func(id ClickHouseReplicaID, expectedName, expectedPriority string) {
				Expect(cluster.PodTemplateNameFor(id)).To(Equal(expectedName))

				tmpl := cluster.PodTemplateFor(id)
				if expectedPriority == "" {
					Expect(tmpl).To(BeNil())
					return
				}

				Expect(tmpl).NotTo(BeNil())
				Expect(tmpl.Spec.PriorityClassName).To(Equal(expectedPriority))
			},
			Entry("cluster template", ClickHouseReplicaID{Shard: "plain", Index: 0}, "cluster", "cluster"),
			Entry("shard template", ClickHouseReplicaID{Shard: "custom", Index: 0}, "shard", "shard"),
			Entry("replica template", ClickHouseReplicaID{Shard: "custom", Index: 1}, "replica", "replica"),
			Entry("missing template", ClickHouseReplicaID{Shard: "custom", Index: 2}, "missing", ""),
		)

		It("should return nil when nothing is referenced", func() {
			plain := newCluster(ClickHouseClusterSpec{PodTemplates: templates, Shards: new(int32(1))})
			Expect(plain.PodTemplateFor(ClickHouseReplicaID{Shard: "0", Index: 0})).To(BeNil())
		})
	})

	Describe("DataVolumeClaimTemplateFor", func() {
		storage := func(class string) DataVolumeClaimTemplate {
			return DataVolumeClaimTemplate{Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: new(class)}}
		}

		templates := []NamedDataVolumeClaimTemplate{
			{Name: "cluster", DataVolumeClaimTemplate: storage("cluster")},
			{Name: "shard", DataVolumeClaimTemplate: storage("shard")},
			{Name: "replica", DataVolumeClaimTemplate: storage("replica")},
		}

		cluster := newCluster(ClickHouseClusterSpec{
			DataVolumeClaimTemplates:    templates,
			DataVolumeClaimTemplateName: "cluster",
			ShardSpecs: []ClickHouseShardSpec{
				{Name: "plain", Replicas: new(int32(1))},
				{Name: "custom", DataVolumeClaimTemplateName: "shard", ReplicaSpecs: []ClickHouseReplicaSpec{
					{}, {DataVolumeClaimTemplateName: "replica"}, {DataVolumeClaimTemplateName: "missing"},
				}},
			},
		})

		DescribeTable("should select the most specific template",
			func(id ClickHouseReplicaID, expectedName, expectedClass string) {
				Expect(cluster.DataVolumeClaimTemplateNameFor(id)).To(Equal(expectedName))

				tmpl := cluster.DataVolumeClaimTemplateFor(id)
				if expectedClass == "" {
					Expect(tmpl).To(BeNil())
					return
				}

				Expect(tmpl).NotTo(BeNil())
				Expect(tmpl.Spec.StorageClassName).To(HaveValue(Equal(expectedClass)))
			},
			Entry("cluster template", ClickHouseReplicaID{Shard: "plain", Index: 0}, "cluster", "cluster"),
			Entry("shard template", ClickHouseReplicaID{Shard: "custom", Index: 0}, "shard", "shard"),
			Entry("replica template", ClickHouseReplicaID{Shard: "custom", Index: 1}, "replica", "replica"),
			Entry("missing template", ClickHouseReplicaID{Shard: "custom", Index: 2}, "missing", ""),
		)

		It("should use the cluster template in count mode", func() {
			counted := newCluster(ClickHouseClusterSpec{
				DataVolumeClaimTemplates: templates, DataVolumeClaimTemplateName: "cluster", Shards: new(int32(2)),
			})
			Expect(counted.DataVolumeClaimTemplateFor(ClickHouseReplicaID{Shard: "1", Index: 0}).Spec.StorageClassName).
				To(HaveValue(Equal("cluster")))
		})

		It("should return nil when nothing is referenced", func() {
			plain := newCluster(ClickHouseClusterSpec{DataVolumeClaimTemplates: templates, Shards: new(int32(1))})
			Expect(plain.DataVolumeClaimTemplateFor(ClickHouseReplicaID{Shard: "0", Index: 0})).To(BeNil())
		})
	})

	Describe("AdditionalVolumeClaimTemplatesFor", func() {
		set := func(name string, disks ...string) NamedVolumeClaimTemplateSet {
			s := NamedVolumeClaimTemplateSet{Name: name}
			for _, d := range disks {
				s.Templates = append(s.Templates, PersistentVolumeClaimTemplate{NamedTemplateMeta: NamedTemplateMeta{Name: d}})
			}

			return s
		}

		diskNames := func(templates []PersistentVolumeClaimTemplate) []string {
			names := make([]string, 0, len(templates))
			for _, t := range templates {
				names = append(names, t.Name)
			}

			return names
		}

		sets := []NamedVolumeClaimTemplateSet{
			set("cluster", "c1"),
			set("shard", "s1", "s2"),
			set("replica", "r1", "r2", "r3"),
		}

		cluster := newCluster(ClickHouseClusterSpec{
			AdditionalVolumeClaimTemplateSets:    sets,
			AdditionalVolumeClaimTemplateSetName: "cluster",
			ShardSpecs: []ClickHouseShardSpec{
				{Name: "plain", Replicas: new(int32(1))},
				{Name: "custom", AdditionalVolumeClaimTemplateSetName: "shard", ReplicaSpecs: []ClickHouseReplicaSpec{
					{}, {AdditionalVolumeClaimTemplateSetName: "replica"}, {AdditionalVolumeClaimTemplateSetName: "missing"},
				}},
			},
		})

		DescribeTable("should select the most specific set",
			func(id ClickHouseReplicaID, expectedName string, expectedDisks []string) {
				Expect(cluster.AdditionalVolumeClaimTemplateSetNameFor(id)).To(Equal(expectedName))

				templates := cluster.AdditionalVolumeClaimTemplatesFor(id)
				if expectedDisks == nil {
					Expect(templates).To(BeNil())
					return
				}

				Expect(diskNames(templates)).To(Equal(expectedDisks))
			},
			Entry("cluster set", ClickHouseReplicaID{Shard: "plain", Index: 0}, "cluster", []string{"c1"}),
			Entry("shard set", ClickHouseReplicaID{Shard: "custom", Index: 0}, "shard", []string{"s1", "s2"}),
			Entry("replica set", ClickHouseReplicaID{Shard: "custom", Index: 1}, "replica", []string{"r1", "r2", "r3"}),
			Entry("missing set", ClickHouseReplicaID{Shard: "custom", Index: 2}, "missing", nil),
		)

		It("should return nil when nothing is referenced", func() {
			plain := newCluster(ClickHouseClusterSpec{AdditionalVolumeClaimTemplateSets: sets, Shards: new(int32(1))})
			Expect(plain.AdditionalVolumeClaimTemplatesFor(ClickHouseReplicaID{Shard: "0", Index: 0})).To(BeNil())
		})
	})

	Describe("replica ID labels", func() {
		DescribeTable("should round-trip",
			func(id ClickHouseReplicaID) {
				parsed, err := ClickHouseIDFromLabels(id.Labels())
				Expect(err).NotTo(HaveOccurred())
				Expect(parsed).To(Equal(id))
			},
			Entry("numeric shard", ClickHouseReplicaID{Shard: "3", Index: 1}),
			Entry("named shard", ClickHouseReplicaID{Shard: "tenant-a", Index: 0}),
		)

		It("should fail without the shard label", func() {
			_, err := ClickHouseIDFromLabels(map[string]string{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("WithDefaults", func() {
		It("should leave shards and replicas unset with shardSpecs", func() {
			spec := ClickHouseClusterSpec{ShardSpecs: []ClickHouseShardSpec{{Name: "a", Replicas: new(int32(2))}}}
			spec.WithDefaults()
			Expect(spec.Shards).To(BeNil())
			Expect(spec.Replicas).To(BeNil())
		})

		It("should keep explicit counts", func() {
			spec := ClickHouseClusterSpec{Shards: new(int32(4)), Replicas: new(int32(2))}
			spec.WithDefaults()
			Expect(*spec.Shards).To(BeEquivalentTo(4))
			Expect(*spec.Replicas).To(BeEquivalentTo(2))
		})
	})
})
