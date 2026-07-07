package clickhouse

import (
	"fmt"
	"strings"
	"text/template"

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
				Replicas:            new(int32(3)),
				Shards:              new(int32(2)),
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
				AdditionalVolumeClaimTemplates: []v1.PersistentVolumeClaimTemplate{
					{NamedTemplateMeta: v1.NamedTemplateMeta{Name: "test1"}},
					{NamedTemplateMeta: v1.NamedTemplateMeta{Name: "test2"}},
				},
				Settings: v1.ClickHouseSettings{
					Encryption: &v1.EncryptionSettings{
						PolicyName: "encrypted",
					},
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
			Expect(generator.Enabled(&ctx)).To(BeTrue())
			data, err := generator.Generate(&ctx, v1.ClickHouseReplicaID{})
			Expect(err).ToNot(HaveOccurred())

			obj := map[any]any{}
			Expect(yaml.Unmarshal([]byte(data), &obj)).To(Succeed())
			Expect(findExposedEnvVars(obj)).To(BeEmpty(), "env vars should be hidden in preprocessed config")
		})
	}

	It("should generate valid disk encryption config", func() {
		var g configGenerator
		for _, generator := range generators {
			if generator.Filename() == "10-storage-jbod.yaml" {
				g = generator
				break
			}
		}

		Expect(g).ToNot(BeNil())

		cfg := struct {
			StorageConfiguration struct {
				Disks map[string]struct {
					Type, Disk, Path, Algorithm string
					KeyHex                      map[string]string `yaml:"key_hex"`
				} `yaml:"disks"`
				Policies map[string]struct {
					Volumes map[string]struct {
						Disk []string
					} `yaml:"volumes"`
				} `yaml:"policies"`
			} `yaml:"storage_configuration"`
		}{}

		Expect(g.Enabled(&ctx)).To(BeTrue())
		data, err := g.Generate(&ctx, v1.ClickHouseReplicaID{})
		Expect(err).ToNot(HaveOccurred())

		Expect(yaml.Unmarshal([]byte(data), &cfg)).To(Succeed())

		var (
			disks          []string
			encryptedDisks []string
		)
		for disk, conf := range cfg.StorageConfiguration.Disks {
			if strings.HasSuffix(disk, EncryptedDiskNameSuffix) {
				Expect(conf.Type).To(Equal("encrypted"))
				Expect(cfg.StorageConfiguration.Disks).To(HaveKey(strings.TrimSuffix(disk, EncryptedDiskNameSuffix)))
				encryptedDisks = append(encryptedDisks, disk)
			} else {
				Expect(cfg.StorageConfiguration.Disks).To(HaveKey(disk + EncryptedDiskNameSuffix))
				disks = append(disks, disk)
			}
		}

		Expect(cfg.StorageConfiguration.Policies[ctx.Cluster.Spec.Settings.Encryption.PolicyName].Volumes["main"].Disk).To(ConsistOf(encryptedDisks))
		Expect(cfg.StorageConfiguration.Policies["default"].Volumes["main"].Disk).To(ConsistOf(disks))
	})

	It("should use internal replica hostnames for remote servers", func() {
		data, err := clusterConfigGenerator(template.Must(template.New("").Parse(clusterConfigTemplateStr)), &ctx, v1.ClickHouseReplicaID{})
		Expect(err).ToNot(HaveOccurred())

		for id := range ctx.Cluster.ReplicaIDs() {
			Expect(data).To(ContainSubstring("host: " + ctx.Cluster.InternalHostnameByID(id)))
		}
	})

	It("should set the internal hostname as the interserver HTTP host", func() {
		id := v1.ClickHouseReplicaID{ShardID: 1, Index: 2}
		data, err := networkConfigGenerator(template.Must(template.New("").Parse(networkConfigTemplateStr)), &ctx, id)
		Expect(err).ToNot(HaveOccurred())
		Expect(data).To(ContainSubstring("interserver_http_host: " + ctx.Cluster.InternalHostnameByID(id)))
	})
})

var _ = Describe("networkConfigGenerator listen_host", func() {
	networkGenerator := func() configGenerator {
		for _, g := range generators {
			if g.Filename() == "00-network.yaml" {
				return g
			}
		}

		return nil
	}

	newReconciler := func(listenHost []string) *clickhouseReconciler {
		return &clickhouseReconciler{
			Cluster: &v1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-namespace",
				},
				Spec: v1.ClickHouseClusterSpec{
					Replicas:            new(int32(1)),
					Shards:              new(int32(1)),
					DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{},
					Settings: v1.ClickHouseSettings{
						Network: v1.NetworkSettings{ListenHost: listenHost},
					},
				},
				Status: v1.ClickHouseClusterStatus{Version: "25.12.1.1"},
			},
			keeper: v1.KeeperCluster{Spec: v1.KeeperClusterSpec{Replicas: new(int32(1))}},
		}
	}

	generateListenHost := func(listenHost []string) []any {
		gen := networkGenerator()
		Expect(gen).ToNot(BeNil())

		data, err := gen.Generate(newReconciler(listenHost), v1.ClickHouseReplicaID{})
		Expect(err).ToNot(HaveOccurred())

		obj := map[string]any{}
		Expect(yaml.Unmarshal([]byte(data), &obj)).To(Succeed())
		Expect(obj).To(HaveKey("listen_host"))

		hosts, ok := obj["listen_host"].([]any)
		Expect(ok).To(BeTrue())

		return hosts
	}

	It("defaults to dual-stack when listenHost is empty", func() {
		Expect(generateListenHost(nil)).To(Equal([]any{"::", "0.0.0.0"}))
	})

	It("honors a custom IPv4-only listenHost override", func() {
		Expect(generateListenHost([]string{"0.0.0.0"})).To(Equal([]any{"0.0.0.0"}))
	})
})

func findExposedEnvVars(value any) []string {
	switch value := value.(type) {
	case map[any]any:
		if env, ok := value["@from_env"]; ok {
			if _, ok := value["@hide_in_preprocessed"]; !ok {
				return []string{fmt.Sprintf("%v", env)}
			}
		} else {
			var exposed []string
			for path, child := range value {
				for _, e := range findExposedEnvVars(child) {
					exposed = append(exposed, fmt.Sprintf("%v.%s", path, e))
				}
			}

			return exposed
		}

	case []any:
		var exposed []string
		for i, child := range value {
			for _, e := range findExposedEnvVars(child) {
				exposed = append(exposed, fmt.Sprintf("%d.%s", i, e))
			}
		}

		return exposed
	}

	return nil
}
