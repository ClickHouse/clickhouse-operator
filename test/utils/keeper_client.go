package utils

import (
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"net"
	"slices"
	"time"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller/keeper"
	"github.com/go-zookeeper/zk"
	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive,staticcheck
	"k8s.io/client-go/rest"
)

const (
	keeperTestDataKey = "/%d_test_data_%d"
	keeperTestDataVal = "test data value %d"
)

type zkLogger struct{}

func (l zkLogger) Printf(s string, args ...any) {
	GinkgoWriter.Printf(s+"\n", args...)
}

type KeeperClient struct {
	cluster *ForwardedCluster
	client  *zk.Conn
}

func NewKeeperClient(ctx context.Context, config *rest.Config, cr *v1.KeeperCluster) (*KeeperClient, error) {
	var port uint16 = keeper.PortNative
	if cr.Spec.Settings.TLS.Enabled {
		port = keeper.PortNativeSecure
	}

	cluster, err := NewForwardedCluster(ctx, config, cr.Namespace, cr.SpecificName(), port)
	if err != nil {
		return nil, fmt.Errorf("forwarding zk nodes failed: %w", err)
	}

	var dialer zk.Dialer = func(network, address string, timeout time.Duration) (net.Conn, error) {
		if !cr.Spec.Settings.TLS.Required {
			return net.DialTimeout(network, address, timeout)
		}

		timeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		//nolint:gosec // Test certs are self-signed, so we skip verification.
		dial := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
		return dial.DialContext(timeCtx, network, address)
	}

	keeperAddrs := slices.Collect(maps.Values(cluster.PodToAddr))
	conn, _, err := zk.Connect(keeperAddrs, 5*time.Second, zk.WithLogger(zkLogger{}), zk.WithDialer(dialer))
	if err != nil {
		cluster.Close()
		return nil, fmt.Errorf("connecting to zk %v failed: %w", cr.NamespacedName(), err)
	}

	return &KeeperClient{
		cluster: cluster,
		client:  conn,
	}, nil
}

func (c *KeeperClient) Close() {
	c.client.Close()
	c.cluster.Close()
}

func (c *KeeperClient) CheckWrite(order int) error {
	for i := range 10 {
		path := fmt.Sprintf(keeperTestDataKey, order, i)
		if _, err := c.client.Create(path, []byte(fmt.Sprintf(keeperTestDataVal, i)), 0, nil); err != nil {
			return fmt.Errorf("creating test data failed: %w", err)
		}
		if _, err := c.client.Sync(path); err != nil {
			return fmt.Errorf("sync test data failed: %w", err)
		}
	}

	return nil
}

func (c *KeeperClient) CheckRead(order int) error {
	for i := range 10 {
		data, _, err := c.client.Get(fmt.Sprintf(keeperTestDataKey, order, i))
		if err != nil {
			return fmt.Errorf("check test data failed: %w", err)
		}

		if string(data) != fmt.Sprintf(keeperTestDataVal, i) {
			return fmt.Errorf("check test data failed: expected %q, got %q", fmt.Sprintf(keeperTestDataVal, i), string(data))
		}
	}

	return nil
}
