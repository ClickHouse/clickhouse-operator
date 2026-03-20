package testutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/go-zookeeper/zk"
	. "github.com/onsi/ginkgo/v2"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/keeper"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	keeperTestDataKey = "/%d_test_data_%d"
	keeperTestDataVal = "test data value %d"
)

// staticHostProvider is a HostProvider that returns servers as-is without DNS resolution.
type staticHostProvider struct {
	mu      sync.Mutex
	servers []string
	curr    int
	last    int
}

func (hp *staticHostProvider) Init(servers []string) error {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	hp.servers = servers
	hp.curr = -1
	hp.last = -1

	return nil
}

func (hp *staticHostProvider) Len() int {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	return len(hp.servers)
}

func (hp *staticHostProvider) Next() (string, bool) {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	hp.curr = (hp.curr + 1) % len(hp.servers)

	retryStart := hp.curr == hp.last
	if hp.last == -1 {
		hp.last = 0
	}

	return hp.servers[hp.curr], retryStart
}

func (hp *staticHostProvider) Connected() {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	hp.last = hp.curr
}

type zkLogger struct{}

func (l zkLogger) Printf(s string, args ...any) {
	GinkgoWriter.Printf(s+"\n", args...)
}

// KeeperClient is a ZooKeeper client for testing Keeper clusters.
type KeeperClient struct {
	client *zk.Conn
}

// NewKeeperClient creates a new KeeperClient connected to the specified KeeperCluster.
func NewKeeperClient(
	ctx context.Context, dialer controllerutil.DialContextFunc, cr *v1.KeeperCluster,
) (*KeeperClient, error) {
	var port uint16 = keeper.PortNative
	if cr.Spec.Settings.TLS.Enabled {
		port = keeper.PortNativeSecure
	}

	addrs := make([]string, 0, cr.Replicas())
	for id := range cr.Replicas() {
		addrs = append(addrs, net.JoinHostPort(cr.HostnameByID(v1.KeeperReplicaID(id)), strconv.FormatInt(int64(port), 10)))
	}

	zkDialer := func(_ string, address string, timeout time.Duration) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		conn, err := dialer(dialCtx, address)
		if err != nil {
			return nil, fmt.Errorf("dial keeper %s: %w", address, err)
		}

		if cr.Spec.Settings.TLS.Required {
			//nolint:gosec // Test certs are self-signed, so we skip verification.
			return tls.Client(conn, &tls.Config{InsecureSkipVerify: true}), nil
		}

		return conn, nil
	}

	conn, _, err := zk.Connect(addrs, 5*time.Second,
		zk.WithLogger(zkLogger{}),
		zk.WithDialer(zkDialer),
		zk.WithHostProvider(&staticHostProvider{}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to zk %v failed: %w", cr.NamespacedName(), err)
	}

	return &KeeperClient{
		client: conn,
	}, nil
}

// Close closes the KeeperClient and releases all resources.
func (c *KeeperClient) Close() {
	c.client.Close()
}

// CheckWrite writes test data to the Keeper cluster.
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

// CheckRead reads and verifies test data from the Keeper cluster.
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
