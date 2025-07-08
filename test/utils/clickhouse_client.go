package utils

import (
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"slices"

	"github.com/ClickHouse/clickhouse-go/v2"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	chcontrol "github.com/clickhouse-operator/internal/controller/clickhouse"
	"github.com/onsi/ginkgo/v2"
	"k8s.io/client-go/rest"
)

type ClickHouseClient struct {
	cluster *ForwardedCluster
	client  clickhouse.Conn
}

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

	chAddrs := slices.Collect(maps.Values(cluster.PodToAddr))
	opts := clickhouse.Options{
		Addr: chAddrs,
		Auth: clickhouse.Auth{
			Database: "default",
			Username: "default",
		},
		Debugf: func(format string, args ...interface{}) {
			ginkgo.GinkgoWriter.Printf(format, args...)
		},
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

	conn, err := clickhouse.Open(&opts)

	return &ClickHouseClient{
		cluster: cluster,
		client:  conn,
	}, err
}

func (c *ClickHouseClient) Close() {
	_ = c.client.Close()
	c.cluster.Close()
}

func (c *ClickHouseClient) CheckWrite(ctx context.Context, order int) error {
	// Do it every time until we'll create it in controller
	err := c.client.Exec(ctx, "CREATE DATABASE IF NOT EXISTS e2e_test ON CLUSTER 'default' "+
		"Engine=Replicated('/data/e2e_test', '{shard}', '{replica}')")
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	tableName := fmt.Sprintf("e2e_test.e2e_test_%d", order)
	err = c.client.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (id Int64, val String) "+
		"Engine=ReplicatedMergeTree() ORDER BY id", tableName))
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	err = c.client.Exec(ctx, fmt.Sprintf("INSERT INTO %s SELECT number, 'test' FROM numbers(10)", tableName))
	if err != nil {
		return fmt.Errorf("insert test data: %w", err)
	}

	return nil
}

func (c *ClickHouseClient) CheckRead(ctx context.Context, order int) error {
	tableName := fmt.Sprintf("e2e_test.e2e_test_%d", order)

	err := c.client.Exec(ctx, fmt.Sprintf("SYSTEM SYNC REPLICA %s", tableName))
	if err != nil {
		return err
	}

	rows, err := c.client.Query(ctx, fmt.Sprintf("SELECT * FROM %s ORDER BY id", tableName))
	if err != nil {
		return fmt.Errorf("query test data: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for i := range 10 {
		if !rows.Next() {
			return fmt.Errorf("expected 10 rows, got less")
		}

		var id int64
		var val string
		if err := rows.Scan(&id, &val); err != nil {
			return fmt.Errorf("scan row %d: %w", i, err)
		}

		if id != int64(i) || val != "test" {
			return fmt.Errorf("unexpected data in row %d: got (%d, %s), want (%d, test)", i, id, val, i)
		}
	}

	return nil
}
