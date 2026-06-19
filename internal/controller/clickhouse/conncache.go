package clickhouse

import (
	"context"
	"errors"
	"maps"
	"sync"

	"github.com/ClickHouse/clickhouse-go/v2"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

type connCache struct {
	mu      sync.Mutex
	entries map[types.NamespacedName]*connCacheEntry
}

type connCacheEntry struct {
	mu       sync.Mutex
	credHash string
	conns    map[v1.ClickHouseReplicaID]clickhouse.Conn
}

func newConnCache() *connCache {
	return &connCache{entries: map[types.NamespacedName]*connCacheEntry{}}
}

func (c *connCache) Get(key types.NamespacedName, credHash string, log controllerutil.Logger) *connCacheEntry {
	if c == nil { // no pool wired (tests)
		return &connCacheEntry{credHash: credHash, conns: map[v1.ClickHouseReplicaID]clickhouse.Conn{}}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok {
		e = &connCacheEntry{credHash: credHash, conns: map[v1.ClickHouseReplicaID]clickhouse.Conn{}}
		c.entries[key] = e

		return e
	}

	if e.credHash == credHash {
		return e
	}

	e.Close(log)
	e = &connCacheEntry{credHash: credHash, conns: map[v1.ClickHouseReplicaID]clickhouse.Conn{}}
	c.entries[key] = e

	return e
}

func (c *connCache) Evict(key types.NamespacedName, log controllerutil.Logger) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[key]; ok {
		e.Close(log)
		delete(c.entries, key)
	}
}

func (c *connCache) Close(log controllerutil.Logger) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for key, e := range c.entries {
		e.Close(log)
		delete(c.entries, key)
	}
}

func (e *connCacheEntry) Conn(id v1.ClickHouseReplicaID, dial func() (clickhouse.Conn, error)) (clickhouse.Conn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if conn, ok := e.conns[id]; ok {
		return conn, nil
	}

	conn, err := dial()
	if err != nil {
		return nil, err
	}

	e.conns[id] = conn

	return conn, nil
}

func (e *connCacheEntry) AnyConn(ctx context.Context) (v1.ClickHouseReplicaID, clickhouse.Conn, error) {
	conns := func() map[v1.ClickHouseReplicaID]clickhouse.Conn {
		e.mu.Lock()
		defer e.mu.Unlock()

		return maps.Clone(e.conns)
	}()

	for id, conn := range conns {
		if conn.Ping(ctx) == nil {
			return id, conn, nil
		}
	}

	return v1.ClickHouseReplicaID{}, nil, errors.New("no available connections")
}

func (e *connCacheEntry) Close(log controllerutil.Logger) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for id, conn := range e.conns {
		if err := conn.Close(); err != nil {
			log.Warn("error closing pooled connection", "error", err, "replica_id", id)
		}
	}

	clear(e.conns)
}
