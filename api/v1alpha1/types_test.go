package v1alpha1

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
				},
				Spec: KeeperClusterSpec{},
			}
		})

		It("should use cluster.local when clusterDomain is empty", func() {
			keeper.Spec.ClusterDomain = ""
			hostname := keeper.HostnameByID(0)
			Expect(hostname).To(Equal("test-keeper-0-0.test-keeper-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should use custom cluster domain when specified", func() {
			keeper.Spec.ClusterDomain = "k8s.example.com"
			hostname := keeper.HostnameByID(0)
			Expect(hostname).To(Equal("test-keeper-0-0.test-keeper-headless.test-ns.svc.k8s.example.com"))
		})

		It("should use explicit cluster.local when specified", func() {
			keeper.Spec.ClusterDomain = testDefaultClusterDomain
			hostname := keeper.HostnameByID(1)
			Expect(hostname).To(Equal("test-keeper-1-0.test-keeper-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should handle complex domain with subdomains", func() {
			keeper.Spec.ClusterDomain = "internal.corp.example.com"
			hostname := keeper.HostnameByID(2)
			Expect(hostname).To(Equal("test-keeper-2-0.test-keeper-headless.test-ns.svc.internal.corp.example.com"))
		})
	})
})

var _ = Describe("ClickHouseCluster", func() {
	Describe("HostnameByID", func() {
		var cluster *ClickHouseCluster

		BeforeEach(func() {
			cluster = &ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
				},
				Spec: ClickHouseClusterSpec{},
			}
		})

		It("should use cluster.local when clusterDomain is empty", func() {
			cluster.Spec.ClusterDomain = ""
			hostname := cluster.HostnameByID(ClickHouseReplicaID{ShardID: 0, Index: 0})
			Expect(hostname).To(Equal("test-clickhouse-0-0-0.test-clickhouse-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should use custom cluster domain when specified", func() {
			cluster.Spec.ClusterDomain = "k8s.example.com"
			hostname := cluster.HostnameByID(ClickHouseReplicaID{ShardID: 0, Index: 0})
			Expect(hostname).To(Equal("test-clickhouse-0-0-0.test-clickhouse-headless.test-ns.svc.k8s.example.com"))
		})

		It("should use explicit cluster.local when specified", func() {
			cluster.Spec.ClusterDomain = testDefaultClusterDomain
			hostname := cluster.HostnameByID(ClickHouseReplicaID{ShardID: 1, Index: 2})
			Expect(hostname).To(Equal("test-clickhouse-1-2-0.test-clickhouse-headless.test-ns.svc." + testDefaultClusterDomain))
		})

		It("should handle complex domain with subdomains", func() {
			cluster.Spec.ClusterDomain = "internal.corp.example.com"
			hostname := cluster.HostnameByID(ClickHouseReplicaID{ShardID: 0, Index: 1})
			Expect(hostname).To(Equal("test-clickhouse-0-1-0.test-clickhouse-headless.test-ns.svc.internal.corp.example.com"))
		})
	})
})
