package v1alpha2

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
)

const testDefaultClusterDomain = "cluster.local"

func TestTypes(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Types Suite")
}

var _ = Describe("KeeperCluster", func() {
	Describe("HostnameByID", func() {
		var keeper *KeeperCluster

		BeforeEach(func() {
			keeper = &KeeperCluster{
				Name:      "test",
				Namespace: "test-ns",
				Spec:      KeeperClusterSpec{},
			}
		})

		It("should use cluster.local when clusterDomain is empty", func() {
			keeper.Spec.Network.ClusterDomain = ""
			hostname := keeper.HostnameByID(0)
			Expect(hostname).To(Equal("test-keeper-0-0.test-keeper-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should use custom cluster domain when specified", func() {
			keeper.Spec.Network.ClusterDomain = "k8s.example.com"
			hostname := keeper.HostnameByID(0)
			Expect(hostname).To(Equal("test-keeper-0-0.test-keeper-headless.test-ns.svc.k8s.example.com"))
		})

		It("should use explicit cluster.local when specified", func() {
			keeper.Spec.Network.ClusterDomain = testDefaultClusterDomain
			hostname := keeper.HostnameByID(1)
			Expect(hostname).To(Equal("test-keeper-1-0.test-keeper-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should handle complex domain with subdomains", func() {
			keeper.Spec.Network.ClusterDomain = "internal.corp.example.com"
			hostname := keeper.HostnameByID(2)
			Expect(hostname).To(Equal("test-keeper-2-0.test-keeper-headless.test-ns.svc.internal.corp.example.com"))
		})
	})
})

var _ = Describe("ClickHouseCluster", func() {
	DescribeTable("DatabaseSync.SyncNewReplicas", func(value NewReplicasPolicy, expected bool) {
		settings := DatabaseSyncSettings{NewReplicas: value}
		Expect(settings.SyncNewReplicas()).To(Equal(expected))
	},
		Entry("defaults to sync", NewReplicasPolicy(""), true),
		Entry("is explicitly sync", NewReplicasSync, true),
		Entry("is explicitly skip", NewReplicasSkip, false),
	)

	DescribeTable("DatabaseSync.DropStaleReplicas", func(value StaleReplicasPolicy, expected bool) {
		settings := DatabaseSyncSettings{StaleReplicas: value}
		Expect(settings.DropStaleReplicas()).To(Equal(expected))
	},
		Entry("defaults to drop", StaleReplicasPolicy(""), true),
		Entry("is explicitly drop", StaleReplicasDrop, true),
		Entry("is explicitly ignore", StaleReplicasIgnore, false),
	)

	Describe("KeeperClusterNamespacedName", func() {
		It("should default the keeper namespace to the ClickHouseCluster namespace", func() {
			cluster := &ClickHouseCluster{
				Name:      "test",
				Namespace: "clickhouse-ns",
				Spec: ClickHouseClusterSpec{
					KeeperClusterRef: &KeeperClusterReference{
						Name: "keeper",
					},
				},
			}

			Expect(cluster.KeeperClusterNamespacedName()).To(Equal(types.NamespacedName{
				Namespace: "clickhouse-ns",
				Name:      "keeper",
			}))
		})

		It("should use the explicit keeper namespace when provided", func() {
			cluster := &ClickHouseCluster{
				Name:      "test",
				Namespace: "clickhouse-ns",
				Spec: ClickHouseClusterSpec{
					KeeperClusterRef: &KeeperClusterReference{
						Name:      "keeper",
						Namespace: "keeper-ns",
					},
				},
			}

			Expect(cluster.KeeperClusterNamespacedName()).To(Equal(types.NamespacedName{
				Namespace: "keeper-ns",
				Name:      "keeper",
			}))
		})
	})

	Describe("HostnameByID", func() {
		var cluster *ClickHouseCluster

		BeforeEach(func() {
			cluster = &ClickHouseCluster{
				Name:      "test",
				Namespace: "test-ns",
				Spec:      ClickHouseClusterSpec{},
			}
		})

		It("should use cluster.local when clusterDomain is empty", func() {
			cluster.Spec.Network.ClusterDomain = ""
			hostname := cluster.HostnameByID(ClickHouseReplicaID{Shard: "0", Index: 0})
			Expect(hostname).To(Equal("test-clickhouse-0-0-0.test-clickhouse-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should use custom cluster domain when specified", func() {
			cluster.Spec.Network.ClusterDomain = "k8s.example.com"
			hostname := cluster.HostnameByID(ClickHouseReplicaID{Shard: "0", Index: 0})
			Expect(hostname).To(Equal("test-clickhouse-0-0-0.test-clickhouse-headless.test-ns.svc.k8s.example.com"))
		})

		It("should use explicit cluster.local when specified", func() {
			cluster.Spec.Network.ClusterDomain = testDefaultClusterDomain
			hostname := cluster.HostnameByID(ClickHouseReplicaID{Shard: "1", Index: 2})
			Expect(hostname).To(Equal("test-clickhouse-1-2-0.test-clickhouse-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should handle complex domain with subdomains", func() {
			cluster.Spec.Network.ClusterDomain = "internal.corp.example.com"
			hostname := cluster.HostnameByID(ClickHouseReplicaID{Shard: "0", Index: 1})
			Expect(hostname).To(Equal("test-clickhouse-0-1-0.test-clickhouse-headless.test-ns.svc.internal.corp.example.com"))
		})
	})

	Describe("InternalHostnameByID", func() {
		var cluster *ClickHouseCluster

		BeforeEach(func() {
			cluster = &ClickHouseCluster{
				Name:      "test",
				Namespace: "test-ns",
				Spec:      ClickHouseClusterSpec{},
			}
		})

		It("should use the internal service with the default cluster domain", func() {
			id := ClickHouseReplicaID{Shard: "0", Index: 1}
			hostname := cluster.InternalHostnameByID(id)
			Expect(cluster.InternalServiceNameByReplicaID(id)).To(Equal("test-clickhouse-internal-0-1"))
			Expect(hostname).To(Equal("test-clickhouse-internal-0-1.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should use a custom cluster domain", func() {
			cluster.Spec.Network.ClusterDomain = "k8s.example.com"
			hostname := cluster.InternalHostnameByID(ClickHouseReplicaID{Shard: "1", Index: 2})
			Expect(hostname).To(Equal("test-clickhouse-internal-1-2.test-ns.svc.k8s.example.com"))
		})
	})

	Describe("SpecificResourceName", func() {
		It("should add suffix", func() {
			cluster := ClickHouseCluster{Name: "test"}
			Expect(cluster.SpecificResourceName("suffix")).To(HaveSuffix("-suffix"))
		})

		It("should replace forbidden chars", func() {
			cluster := ClickHouseCluster{Name: "test.name"}
			Expect(cluster.SpecificResourceName("suffix")).To(Not(ContainSubstring(".")))
		})

		It("should truncate length", func() {
			name := "very-very.long.resource-name.that-longer-than-limit"
			suffix := "long-suffix-in-result"
			cluster := ClickHouseCluster{Name: name}
			res := cluster.SpecificResourceName(suffix)
			Expect(res).To(HaveLen(maxResourceNameLen), res)
			Expect(res).To(HaveSuffix(suffix), res)
		})
	})
})
