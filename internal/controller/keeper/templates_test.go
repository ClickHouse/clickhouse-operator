package keeper

import (
	"testing"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

type confMap map[any]any

func TestServerRevision(t *testing.T) {
	cr := &v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: v1.KeeperClusterSpec{
			Replicas: ptr.To[int32](1),
		},
	}

	cfgRevision, err := GetConfigurationRevision(cr, nil)
	require.NoError(t, err)
	require.NotEmpty(t, cfgRevision)

	stsRevision, err := GetStatefulSetRevision(cr)
	require.NoError(t, err)
	require.NotEmpty(t, stsRevision)

	t.Run("config revision not changed by replica count", func(t *testing.T) {
		cr := cr.DeepCopy()
		cr.Spec.Replicas = ptr.To[int32](3)
		cfgRevisionUpdated, err := GetConfigurationRevision(cr, nil)
		require.NoError(t, err)
		require.NotEmpty(t, cfgRevision)
		require.Equal(t, cfgRevision, cfgRevisionUpdated, "server config revision shouldn't depend on replica count")

		stsRevisionUpdated, err := GetStatefulSetRevision(cr)
		require.NoError(t, err)
		require.NotEmpty(t, stsRevisionUpdated)
		require.Equal(t, stsRevision, stsRevisionUpdated, "StatefulSet config revision shouldn't depend on replica count")
	})

	t.Run("sts revision not changed by config", func(t *testing.T) {
		cr := cr.DeepCopy()
		cr.Spec.Settings.Logger.Level = "warning"
		cfgRevisionUpdated, err := GetConfigurationRevision(cr, nil)
		require.NoError(t, err)
		require.NotEmpty(t, cfgRevision)
		require.NotEqual(t, cfgRevision, cfgRevisionUpdated, "server config revision shouldn't depend on replica count")

		stsRevisionUpdated, err := GetStatefulSetRevision(cr)
		require.NoError(t, err)
		require.NotEmpty(t, stsRevisionUpdated)
		require.Equal(t, stsRevision, stsRevisionUpdated, "StatefulSet config revision shouldn't depend on replica count")
	})
}

func TestExtraConfig(t *testing.T) {
	cr := &v1.KeeperCluster{}

	baseConfigYAML, err := generateConfigForSingleReplica(cr, nil, "1")
	require.NoError(t, err)
	var baseConfig confMap
	err = yaml.Unmarshal([]byte(baseConfigYAML), &baseConfig)
	require.NoError(t, err)

	t.Run("add new setting", func(t *testing.T) {
		configYAML, err := generateConfigForSingleReplica(cr, map[string]any{
			"keeper_server": confMap{
				"coordination_settings": confMap{
					"quorum_reads": true,
				},
			},
		}, "1")
		require.NoError(t, err)
		var config confMap
		err = yaml.Unmarshal([]byte(configYAML), &config)
		require.NoError(t, err)

		require.False(t, cmp.Equal(baseConfig, config), cmp.Diff(baseConfig, config))
		require.Equal(t, config["keeper_server"].(confMap)["coordination_settings"].(confMap)["quorum_reads"], true)
	})

	t.Run("override existing setting", func(t *testing.T) {
		configYAML, err := generateConfigForSingleReplica(cr, map[string]any{
			"keeper_server": confMap{
				"coordination_settings": confMap{
					"compress_logs": true,
				},
			},
		}, "1")
		require.NoError(t, err)
		var config confMap
		err = yaml.Unmarshal([]byte(configYAML), &config)
		require.NoError(t, err)

		require.False(t, cmp.Equal(baseConfig, config), cmp.Diff(baseConfig, config))
		require.Equal(t, config["keeper_server"].(confMap)["coordination_settings"].(confMap)["compress_logs"], true)
	})
}
