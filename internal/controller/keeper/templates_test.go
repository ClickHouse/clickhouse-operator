package keeper

import (
	"testing"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestServerRevision(t *testing.T) {
	cr := &v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: v1.KeeperClusterSpec{
			Replicas: ptr.To[int32](1),
		},
	}

	cfgRevision, err := GetConfigurationRevision(cr)
	require.NoError(t, err)
	require.NotEmpty(t, cfgRevision)

	stsRevision, err := GetStatefulSetRevision(cr)
	require.NoError(t, err)
	require.NotEmpty(t, stsRevision)

	t.Run("config revision not changed by replica count", func(t *testing.T) {
		cr := cr.DeepCopy()
		cr.Spec.Replicas = ptr.To[int32](3)
		cfgRevisionUpdated, err := GetConfigurationRevision(cr)
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
		cr.Spec.LoggerConfig.LoggerLevel = "warning"
		cfgRevisionUpdated, err := GetConfigurationRevision(cr)
		require.NoError(t, err)
		require.NotEmpty(t, cfgRevision)
		require.NotEqual(t, cfgRevision, cfgRevisionUpdated, "server config revision shouldn't depend on replica count")

		stsRevisionUpdated, err := GetStatefulSetRevision(cr)
		require.NoError(t, err)
		require.NotEmpty(t, stsRevisionUpdated)
		require.Equal(t, stsRevision, stsRevisionUpdated, "StatefulSet config revision shouldn't depend on replica count")
	})
}
