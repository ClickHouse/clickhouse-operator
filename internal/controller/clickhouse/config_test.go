package clickhouse

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

var _ = Describe("ConfigGenerator", func() {
	ctx := clickhouseReconciler{
		Cluster: &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas: new(int32(3)),
				Shards:   new(int32(2)),
				Settings: v1.ClickHouseSettings{
					ExtraConfig: runtime.RawExtension{
						Raw: []byte(`{"test": "value"}`),
					},
					ExtraUsersConfig: runtime.RawExtension{
						Raw: []byte(`{}`),
					},
				},
			},
			Status: v1.ClickHouseClusterStatus{
				Version: "25.12.1.1",
			},
		},
		keeper: v1.KeeperCluster{
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(3)),
			},
		},
	}

	for _, generator := range generators {
		It("should generate config: "+generator.Filename(), func() {
			if !generator.Enabled(&ctx) {
				Skip("generator does not apply to this cluster spec")
			}

			data, err := generator.Generate(&ctx, v1.ClickHouseReplicaID{})
			Expect(err).ToNot(HaveOccurred())

			obj := map[any]any{}
			Expect(yaml.Unmarshal([]byte(data), &obj)).To(Succeed())
		})
	}

	It("should generate storage JBOD config when additionalDataVolumeClaimSpecs is set", func() {
		ctxJBOD := clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-jbod",
					Namespace: "test-namespace",
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas:         new(int32(2)),
					Shards:           new(int32(1)),
					KeeperClusterRef: v1.KeeperClusterReference{Name: "keeper"},
					AdditionalDataVolumeClaimSpecs: []v1.AdditionalVolumeClaimSpec{
						{Name: "disk1", Spec: corev1.PersistentVolumeClaimSpec{}},
						{Name: "disk2", MountPath: "/custom/path", Spec: corev1.PersistentVolumeClaimSpec{}},
					},
				},
			},
			keeper: v1.KeeperCluster{Spec: v1.KeeperClusterSpec{Replicas: new(int32(3))}},
		}
		ctxJBOD.Cluster.Spec.WithDefaults()

		configData, err := generateConfigForSingleReplica(&ctxJBOD, v1.ClickHouseReplicaID{})
		Expect(err).ToNot(HaveOccurred())

		storageConfig, ok := configData["etc-clickhouse-server-config-d-10-storage-jbod-yaml"]
		Expect(ok).To(BeTrue())
		Expect(storageConfig).To(ContainSubstring("storage_configuration"))
		Expect(storageConfig).To(ContainSubstring("disk1"))
		Expect(storageConfig).To(ContainSubstring("disk2"))
		Expect(storageConfig).To(ContainSubstring("/var/lib/clickhouse/disks/disk1/"))
		Expect(storageConfig).To(ContainSubstring("/custom/path/"))

		// Verify true JBOD: all disks must be listed inside a single "main" volume
		// as a YAML list (round-robin distribution), not as separate per-disk volumes.
		parsed := map[any]any{}
		Expect(yaml.Unmarshal([]byte(storageConfig), &parsed)).To(Succeed())
		policies := parsed["storage_configuration"].(map[any]any)["policies"].(map[any]any) //nolint:forcetypeassert
		volumes := policies["default"].(map[any]any)["volumes"].(map[any]any)               //nolint:forcetypeassert
		Expect(volumes).To(HaveLen(1), "true JBOD has exactly one volume containing all disks")
		mainVolume := volumes["main"].(map[any]any) //nolint:forcetypeassert
		diskList, ok := mainVolume["disk"].([]any)
		Expect(ok).To(BeTrue(), "disks under main volume must be a list")

		diskNames := make([]string, len(diskList))
		for i, d := range diskList {
			diskNames[i] = d.(string) //nolint:forcetypeassert
		}

		Expect(diskNames).To(ContainElements("default", "disk1", "disk2"))
	})
})
