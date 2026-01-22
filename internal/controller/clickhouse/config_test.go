package clickhouse

import (
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

var _ = Describe("ConfigGenerator", func() {
	ctx := reconcileContext{
		ReconcileContextBase: controller.ReconcileContextBase[*v1.ClickHouseCluster, v1.ClickHouseReplicaID, replicaState]{
			Cluster: &v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas: ptr.To[int32](3),
					Shards:   ptr.To[int32](2),
					Settings: v1.ClickHouseSettings{
						ExtraConfig: runtime.RawExtension{
							Raw: []byte(`{"test": "value"}`),
						},
						ExtraUsersConfig: runtime.RawExtension{
							Raw: []byte(`{}`),
						},
					},
				},
			},
		},
		keeper: v1.KeeperCluster{
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](3),
			},
		},
	}

	for _, generator := range generators {
		It("should generate config: "+generator.Filename(), func() {
			Expect(generator.Exists(&ctx)).To(BeTrue())
			data, err := generator.Generate(&ctx, v1.ClickHouseReplicaID{})
			Expect(err).ToNot(HaveOccurred())
			obj := map[any]any{}
			Expect(yaml.Unmarshal([]byte(data), &obj)).To(Succeed())
		})
	}
})
