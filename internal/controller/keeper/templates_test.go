package keeper

import (
	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

type confMap map[any]any

var _ = Describe("ServerRevision", func() {
	var (
		baseCR          *v1.KeeperCluster
		baseCfgRevision string
		baseStsRevision string
	)

	BeforeEach(func() {
		var err error
		baseCR = &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
			},
		}

		baseCfgRevision, err = getConfigurationRevision(baseCR, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(baseCfgRevision).ToNot(BeEmpty())

		baseStsRevision, err = getStatefulSetRevision(baseCR)
		Expect(err).ToNot(HaveOccurred())
		Expect(baseStsRevision).ToNot(BeEmpty())
	})

	It("should not change config revision if only replica count changes", func() {
		cr := baseCR.DeepCopy()
		cr.Spec.Replicas = ptr.To[int32](3)
		cfgRevisionUpdated, err := getConfigurationRevision(cr, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(baseCfgRevision).ToNot(BeEmpty())
		Expect(cfgRevisionUpdated).To(Equal(baseCfgRevision), "server config revision shouldn't depend on replica count")

		stsRevisionUpdated, err := getStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(stsRevisionUpdated).ToNot(BeEmpty())
		Expect(stsRevisionUpdated).To(Equal(baseStsRevision), "StatefulSet config revision shouldn't depend on replica count")
	})

	It("should not change sts revision if only config changes", func() {
		cr := baseCR.DeepCopy()
		cr.Spec.Settings.Logger.Level = "warning"
		cfgRevisionUpdated, err := getConfigurationRevision(cr, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfgRevisionUpdated).ToNot(BeEmpty())
		Expect(cfgRevisionUpdated).ToNot(Equal(baseCfgRevision), "configuration change should update config revision")

		stsRevisionUpdated, err := getStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(stsRevisionUpdated).ToNot(BeEmpty())
		Expect(stsRevisionUpdated).To(Equal(baseStsRevision), "StatefulSet config revision shouldn't change with config")
	})
})

var _ = Describe("ExtraConfig", func() {
	var (
		cr             *v1.KeeperCluster
		baseConfigYAML string
		baseConfig     confMap
	)

	BeforeEach(func() {
		cr = &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
			},
		}

		var err error
		baseConfigYAML, err = generateConfigForSingleReplica(cr, nil, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(yaml.Unmarshal([]byte(baseConfigYAML), &baseConfig)).To(Succeed())
	})

	It("should reflect config changes in generated config", func() {
		configYAML, err := generateConfigForSingleReplica(cr, map[string]any{
			"keeper_server": confMap{
				"coordination_settings": confMap{
					"quorum_reads": true,
				},
			},
		}, 1)
		Expect(err).NotTo(HaveOccurred())

		var config confMap
		Expect(yaml.Unmarshal([]byte(configYAML), &config)).To(Succeed())
		Expect(config).ToNot(Equal(baseConfig), cmp.Diff(config, baseConfig))
		//nolint:forcetypeassert
		Expect(config["keeper_server"].(confMap)["coordination_settings"].(confMap)["quorum_reads"]).To(BeTrue())
	})

	It("should override existing setting by extra config", func() {
		configYAML, err := generateConfigForSingleReplica(cr, map[string]any{
			"keeper_server": confMap{
				"coordination_settings": confMap{
					"compress_logs": true,
				},
			},
		}, 1)
		Expect(err).NotTo(HaveOccurred())

		var config confMap
		err = yaml.Unmarshal([]byte(configYAML), &config)
		Expect(err).NotTo(HaveOccurred())

		Expect(config).ToNot(Equal(baseConfig), cmp.Diff(config, baseConfig))
		//nolint:forcetypeassert
		Expect(config["keeper_server"].(confMap)["coordination_settings"].(confMap)["compress_logs"]).To(BeTrue())
	})
})
