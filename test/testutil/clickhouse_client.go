package testutil

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/onsi/ginkgo/v2"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	chcontrol "github.com/ClickHouse/clickhouse-operator/internal/controller/clickhouse"
)

// ClickHouseClient is a ClickHouse client for testing ClickHouse clusters. Forwards ports to ClickHouse pods.
type ClickHouseClient struct {
	cluster *ForwardedCluster
	clients map[v1.ClickHouseReplicaID]clickhouse.Conn
}

// NewClickHouseClient creates a new ClickHouseClient connected to the specified ClickHouseCluster.
func NewClickHouseClient(
	ctx context.Context,
	config *rest.Config,
	cr *v1.ClickHouseCluster,
	auth ...clickhouse.Auth,
) (*ClickHouseClient, error) {
	var port uint16 = chcontrol.PortNative
	if cr.Spec.Settings.TLS.Enabled {
		port = chcontrol.PortNativeSecure
	}

	cluster, err := NewForwardedCluster(ctx, config, cr.Namespace, cr.SpecificName(), port)
	if err != nil {
		return nil, fmt.Errorf("forwarding ch nodes failed: %w", err)
	}

	created := false
	clients := map[v1.ClickHouseReplicaID]clickhouse.Conn{}

	defer func() {
		if !created {
			for _, client := range clients {
				_ = client.Close()
			}

			cluster.Close()
		}
	}()

	opts := clickhouse.Options{
		Auth: clickhouse.Auth{
			Database: "default",
			Username: "default",
		},
		Logger: slog.New(slog.NewTextHandler(ginkgo.GinkgoWriter, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
	}
	if len(auth) > 0 {
		opts.Auth = auth[0]
	}

	if cr.Spec.Settings.TLS.Enabled {
		opts.TLS = &tls.Config{
			//nolint:gosec // Test certs are self-signed, so we skip verification.
			InsecureSkipVerify: true,
		}
	}

	for pod, addr := range cluster.PodToAddr {
		id, err := v1.ClickHouseIDFromLabels(pod.Labels)
		if err != nil {
			return nil, fmt.Errorf("get replica id from pod %s labels: %w", pod.Name, err)
		}

		opts.Addr = []string{addr}

		conn, err := clickhouse.Open(&opts)
		if err != nil {
			return nil, fmt.Errorf("connect node %s: %w", pod.Name, err)
		}

		clients[id] = conn
	}

	created = true

	return &ClickHouseClient{
		cluster: cluster,
		clients: clients,
	}, err
}

// Close closes the ClickHouseClient and releases all resources.
func (c *ClickHouseClient) Close() {
	for _, client := range c.clients {
		_ = client.Close()
	}

	c.cluster.Close()
}

// CreateDatabase creates the test database on the ClickHouse cluster.
func (c *ClickHouseClient) CreateDatabase(ctx context.Context) error {
	q := "CREATE DATABASE IF NOT EXISTS e2e_test ON CLUSTER 'default' " +
		"Engine=Replicated('/data/e2e_test', '{shard}', '{replica}')"
	if err := c.Exec(ctx, q); err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	return nil
}

// CheckWrite writes test data to the ClickHouse cluster.
func (c *ClickHouseClient) CheckWrite(ctx context.Context, order int) error {
	tableName := fmt.Sprintf("e2e_test.e2e_test_%d", order)

	if len(c.clients) == 0 {
		return errors.New("no cluster nodes available")
	}

	q := fmt.Sprintf("CREATE TABLE %s (id Int64, val String) Engine=ReplicatedMergeTree ORDER BY id", tableName)
	if err := c.Exec(ctx, q); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	writtenShards := make(map[int32]struct{})
	for id, client := range c.clients {
		if _, ok := writtenShards[id.ShardID]; ok {
			continue
		}

		err := client.Exec(ctx, fmt.Sprintf("INSERT INTO %s SELECT number, 'test' FROM numbers(10)", tableName))
		if err != nil {
			return fmt.Errorf("insert test data on %v: %w", id, err)
		}

		writtenShards[id.ShardID] = struct{}{}
	}

	return nil
}

// CheckRead reads and verifies test data from the ClickHouse cluster.
func (c *ClickHouseClient) CheckRead(ctx context.Context, order int) error {
	tableName := fmt.Sprintf("e2e_test.e2e_test_%d", order)

	for id, client := range c.clients {
		// Newly created table may not be started yet.
		err := retry.OnError(retry.DefaultBackoff, func(error) bool {
			return true
		}, func() error {
			return client.Exec(ctx, "SYSTEM SYNC REPLICA "+tableName)
		})
		if err != nil {
			return fmt.Errorf("sync replica on %v: %w", id, err)
		}

		err = func() error {
			rows, err := client.Query(ctx, fmt.Sprintf("SELECT id, val FROM %s ORDER BY id", tableName))
			if err != nil {
				return fmt.Errorf("query test data on %v: %w", id, err)
			}

			defer func() {
				_ = rows.Close()
			}()

			for i := range 10 {
				if !rows.Next() {
					return errors.New("expected 10 rows, got less")
				}

				var (
					id  int64
					val string
				)

				if err := rows.Scan(&id, &val); err != nil {
					return fmt.Errorf("scan row %d: %w", i, err)
				}

				if id != int64(i) || val != "test" {
					return fmt.Errorf("unexpected data in row %d: got (%d, %s), want (%d, test)", i, id, val, i)
				}
			}

			if rows.Next() {
				return fmt.Errorf("expected 10 rows on %v, got more", id)
			}

			return nil
		}()
		if err != nil {
			return err
		}
	}

	return nil
}

// QueryRow executes a query on one of the ClickHouse cluster nodes.
// Scans the result into the provided result variable.
func (c *ClickHouseClient) QueryRow(ctx context.Context, query string, result any) error {
	rows, err := c.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query row: %w", err)
	}

	defer func() {
		_ = rows.Close()
	}()

	if !rows.Next() {
		return fmt.Errorf("no rows returned for query: %s", query)
	}

	if err := rows.Scan(result); err != nil {
		return fmt.Errorf("scan row: %w", err)
	}

	return nil
}

// Query executes a query on one of the ClickHouse cluster nodes.
func (c *ClickHouseClient) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	if len(c.clients) == 0 {
		return nil, errors.New("no cluster nodes available")
	}

	for _, client := range c.clients {
		return client.Query(ctx, query, args...) //nolint:wrapcheck
	}

	panic("unreachable")
}

// Exec executes a query on one of the ClickHouse cluster nodes.
func (c *ClickHouseClient) Exec(ctx context.Context, query string, args ...any) error {
	if len(c.clients) == 0 {
		return errors.New("no cluster nodes available")
	}

	for _, client := range c.clients {
		return client.Exec(ctx, query, args...) //nolint:wrapcheck
	}

	panic("unreachable")
}

// CheckDefaultDatabasesReplicated checks that the default database has Replicated engine on all cluster nodes.
func (c *ClickHouseClient) CheckDefaultDatabasesReplicated(ctx context.Context) error {
	for id, client := range c.clients {
		var isReplicated bool

		query := "SELECT engine='Replicated' FROM system.databases WHERE name='default'"
		if err := client.QueryRow(ctx, query).Scan(&isReplicated); err != nil {
			return fmt.Errorf("query default database engine on %v: %w", id, err)
		}

		if !isReplicated {
			return fmt.Errorf("default database is not replicated on %v", id)
		}
	}

	return nil
}
