package keeper

import (
	"testing"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

type confMap map[any]any

func TestServerRevision(t *testing.T) {
	RegisterFailHandler(NewWithT(t).Fail)
	cr := &v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: v1.KeeperClusterSpec{
			Replicas: ptr.To[int32](1),
		},
	}

	cfgRevision, err := GetConfigurationRevision(cr, nil)
	Expect(err).ToNot(HaveOccurred())
	Expect(cfgRevision).ToNot(BeEmpty())

	stsRevision, err := GetStatefulSetRevision(cr)
	Expect(err).ToNot(HaveOccurred())
	Expect(stsRevision).ToNot(BeEmpty())

	t.Run("config revision not changed by replica count", func(t *testing.T) {
		RegisterFailHandler(NewWithT(t).Fail)
		cr := cr.DeepCopy()
		cr.Spec.Replicas = ptr.To[int32](3)
		cfgRevisionUpdated, err := GetConfigurationRevision(cr, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfgRevision).ToNot(BeEmpty())
		Expect(cfgRevisionUpdated).To(Equal(cfgRevision), "server config revision shouldn't depend on replica count")

		stsRevisionUpdated, err := GetStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(stsRevisionUpdated).ToNot(BeEmpty())
		Expect(stsRevisionUpdated).To(Equal(stsRevision), "StatefulSet config revision shouldn't depend on replica count")
	})

	t.Run("sts revision not changed by config", func(t *testing.T) {
		RegisterFailHandler(NewWithT(t).Fail)
		cr := cr.DeepCopy()
		cr.Spec.Settings.Logger.Level = "warning"
		cfgRevisionUpdated, err := GetConfigurationRevision(cr, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfgRevisionUpdated).ToNot(BeEmpty())
		Expect(cfgRevisionUpdated).ToNot(Equal(cfgRevision), "configuration change should update config revision")

		stsRevisionUpdated, err := GetStatefulSetRevision(cr)
		Expect(err).ToNot(HaveOccurred())
		Expect(stsRevisionUpdated).ToNot(BeEmpty())
		Expect(stsRevisionUpdated).To(Equal(stsRevision), "StatefulSet config revision shouldn't change with config")
	})
}

func TestExtraConfig(t *testing.T) {
	RegisterFailHandler(NewWithT(t).Fail)
	cr := &v1.KeeperCluster{}

	baseConfigYAML, err := generateConfigForSingleReplica(cr, nil, 1)
	Expect(err).NotTo(HaveOccurred())
	var baseConfig confMap
	Expect(yaml.Unmarshal([]byte(baseConfigYAML), &baseConfig)).To(Succeed())

	t.Run("add new setting", func(t *testing.T) {
		RegisterFailHandler(NewWithT(t).Fail)
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
		Expect(config["keeper_server"].(confMap)["coordination_settings"].(confMap)["quorum_reads"]).To(BeTrue())
	})

	t.Run("override existing setting", func(t *testing.T) {
		RegisterFailHandler(NewWithT(t).Fail)
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
		Expect(config["keeper_server"].(confMap)["coordination_settings"].(confMap)["compress_logs"]).To(BeTrue())
	})
}
