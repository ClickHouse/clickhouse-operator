package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	listDatabasesQuery = `SELECT name, engine_full, uuid, engine = 'Replicated' AS is_replicated
FROM system.databases 
WHERE 
	engine NOT IN ('Atomic', 'Lazy', 'SQLite', 'Ordinary')
SETTINGS
	format_display_secrets_in_show_and_select=1`
	listStaleDatabaseReplicasQuery = `SELECT
	database,
	toInt32(database_shard_name) AS shard_id,
	toInt32(database_replica_name) AS replica_id,
	sum(is_active)::Bool AS is_active,
	any(hostname) AS hostname
FROM (
	SELECT 
		name as database,
		database_shard_name,
		database_replica_name,
		is_active,
		hostname() AS hostname
	FROM clusterAllReplicas(default, system.clusters)
	WHERE database_replica_name != ''
)
GROUP BY 
	database, shard_id, replica_id
HAVING
	shard_id >= ?
	OR replica_id >= ?
SETTINGS
	skip_unavailable_shards=1`
	createDefaultDatabaseQuery = `CREATE DATABASE IF NOT EXISTS default UUID ? 
		ENGINE=Replicated('/clickhouse/databases/default', '{shard}', '{replica}')`
)

type databaseDescriptor struct {
	Name         string `ch:"name"`
	EngineFull   string `ch:"engine_full"`
	UUID         string `ch:"uuid"`
	IsReplicated bool   `ch:"is_replicated"`
}

type commander struct {
	log     controllerutil.Logger
	cluster *v1.ClickHouseCluster
	auth    clickhouse.Auth

	lock  sync.RWMutex
	conns map[v1.ClickHouseReplicaID]clickhouse.Conn
}

func newCommander(log controllerutil.Logger, cluster *v1.ClickHouseCluster, secret *corev1.Secret) *commander {
	return &commander{
		log:     log.Named("commander"),
		conns:   map[v1.ClickHouseReplicaID]clickhouse.Conn{},
		cluster: cluster,
		auth: clickhouse.Auth{
			Username: OperatorManagementUsername,
			Password: string(secret.Data[SecretKeyManagementPassword]),
		},
	}
}

func (cmd *commander) Close() {
	cmd.lock.Lock()
	defer cmd.lock.Unlock()

	for id, conn := range cmd.conns {
		if err := conn.Close(); err != nil {
			cmd.log.Warn("error closing connection", "error", err, "replica_id", id)
		}
	}

	cmd.conns = map[v1.ClickHouseReplicaID]clickhouse.Conn{}
}

func (cmd *commander) Ping(ctx context.Context, id v1.ClickHouseReplicaID) error {
	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping replica %s: %w", id, err)
	}

	return nil
}

func (cmd *commander) Databases(ctx context.Context, id v1.ClickHouseReplicaID) (map[string]databaseDescriptor, error) {
	conn, err := cmd.getConn(id)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	rows, err := conn.Query(ctx, listDatabasesQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query databases on replica %s: %w", id, err)
	}

	defer func() {
		_ = rows.Close()
	}()

	databases := map[string]databaseDescriptor{}
	for rows.Next() {
		var db databaseDescriptor
		if err := rows.ScanStruct(&db); err != nil {
			return nil, fmt.Errorf("failed to scan database row on replica %s: %w", id, err)
		}

		databases[db.Name] = db
	}

	return databases, nil
}

func (cmd *commander) CreateDatabases(ctx context.Context, id v1.ClickHouseReplicaID, databases map[string]databaseDescriptor) error {
	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	for name, desc := range databases {
		query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` UUID '%s' ENGINE = %s", name, desc.UUID, desc.EngineFull)
		if err = conn.Exec(ctx, query); err != nil {
			return fmt.Errorf("failed to create database %s on replica %s: %w", name, id, err)
		}

		if desc.IsReplicated {
			if err = conn.Exec(ctx, fmt.Sprintf("SYSTEM SYNC DATABASE REPLICA `%s`", name)); err != nil {
				return fmt.Errorf("failed to sync replica for database %s on replica %s: %w", name, id, err)
			}
		}
	}

	return nil
}

// EnsureDefaultDatabaseEngine ensures that the default database engine is set to the Selected one.
func (cmd *commander) EnsureDefaultDatabaseEngine(ctx context.Context, log controllerutil.Logger, cluster *v1.ClickHouseCluster, id v1.ClickHouseReplicaID) error {
	log = log.With("replica_id", id)

	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %s: %w", id, err)
	}

	var engine string

	rows := conn.QueryRow(ctx, "SELECT engine FROM system.databases WHERE name='default' ")
	if err = rows.Scan(&engine); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to scan default database engine for replica %s: %w", id, err)
		}

		log.Debug("no default database found")
	} else {
		if engine == "Replicated" {
			log.Debug("default database already has the Replicated engine")
			return nil
		}

		var count uint64
		if err = conn.QueryRow(ctx, "SELECT COUNT() FROM system.tables WHERE database='default'").Scan(&count); err != nil {
			log.Error(err, "error checking if database 'default' has tables")
			return fmt.Errorf("check tables in  %s: %w", id, err)
		}

		if count > 0 {
			log.Warn("database `default` has tables, but its engine is not Replicated, data loss is possible")
		}

		log.Debug("dropping default database")

		if err := conn.Exec(ctx, "DROP DATABASE default SYNC"); err != nil {
			return fmt.Errorf("failed to drop default database on replica %s: %w", id, err)
		}
	}

	log.Debug("creating replicated default database")

	defaultDatabaseUUID := uuid.NewSHA1(uuid.Nil, []byte(cluster.SpecificName())).String()
	if err := conn.Exec(ctx, createDefaultDatabaseQuery, defaultDatabaseUUID); err != nil {
		return fmt.Errorf("create default replicated database %s: %w", id, err)
	}

	return nil
}

func (cmd *commander) SyncShard(ctx context.Context, log controllerutil.Logger, shardID int32) error {
	replicasToSync := make([]v1.ClickHouseReplicaID, 0, cmd.cluster.Replicas())
	for id := range cmd.cluster.Replicas() {
		replicasToSync = append(replicasToSync, v1.ClickHouseReplicaID{
			ShardID: shardID,
			Index:   id,
		})
	}

	results := controllerutil.ExecuteParallel(replicasToSync, func(id v1.ClickHouseReplicaID) (v1.ClickHouseReplicaID, struct{}, error) {
		errs := cmd.SyncReplica(ctx, log.With("replica_id", id), id)
		if len(errs) > 0 {
			return id, struct{}{}, fmt.Errorf("sync replica %s: %w", id, errors.Join(errs...))
		}

		return id, struct{}{}, nil
	})

	var errs []error
	for _, res := range results {
		if res.Err != nil {
			errs = append(errs, res.Err)
		}
	}

	return errors.Join(errs...)
}

func (cmd *commander) SyncReplica(ctx context.Context, log controllerutil.Logger, id v1.ClickHouseReplicaID) (errs []error) {
	databases, err := cmd.Databases(ctx, id)
	if err != nil {
		errs = append(errs, fmt.Errorf("get databases for replica %s: %w", id, err))
		return errs
	}

	conn, err := cmd.getConn(id)
	if err != nil {
		errs = append(errs, fmt.Errorf("get connection for replica %s: %w", id, err))
		return errs
	}

	for name, desc := range databases {
		if desc.IsReplicated {
			log.Debug("syncing database replica", "database", name)

			if err = conn.Exec(ctx, fmt.Sprintf("SYSTEM SYNC DATABASE REPLICA `%s`", name)); err != nil {
				errs = append(errs, fmt.Errorf("sync database %s: %w", name, err))
			}
		}
	}

	var replicatedTables []string

	rows, err := conn.Query(ctx, `SELECT database, name FROM system.tables WHERE engine LIKE 'Replicated%'`)
	if err != nil {
		errs = append(errs, fmt.Errorf("query replicated tables: %w", err))
		return errs
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var dbName, tableName string
		if err := rows.Scan(&dbName, &tableName); err != nil {
			errs = append(errs, fmt.Errorf("scan replicated table row: %w", err))
			continue
		}

		replicatedTables = append(replicatedTables, fmt.Sprintf("`%s`.`%s`", dbName, tableName))
	}

	for _, table := range replicatedTables {
		log.Debug("syncing table replica", "table", table)

		if err = conn.Exec(ctx, fmt.Sprintf("SYSTEM SYNC REPLICA %s LIGHTWEIGHT", table)); err != nil {
			errs = append(errs, fmt.Errorf("sync replica %s: %w", table, err))
		}
	}

	return errs
}

// CleanupDatabaseReplicas removes stale replicated database replicas, skipping unsync ones.
func (cmd *commander) CleanupDatabaseReplicas(
	ctx context.Context,
	log controllerutil.Logger,
	notInSync map[v1.ClickHouseReplicaID]struct{},
) error {
	var anyID v1.ClickHouseReplicaID
	for id := range cmd.cluster.ReplicaIDs() {
		anyID = id
		break
	}

	log = log.With("replica_id", anyID)

	conn, err := cmd.getConn(anyID)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %v: %w", anyID, err)
	}

	rows, err := conn.Query(ctx, listStaleDatabaseReplicasQuery, cmd.cluster.Shards(), cmd.cluster.Replicas())
	if err != nil {
		return fmt.Errorf("failed to query stale database replicas %v: %w", anyID, err)
	}

	defer func() {
		_ = rows.Close()
	}()

	total := 0
	succeed := 0

	for rows.Next() {
		var (
			database string
			toDrop   v1.ClickHouseReplicaID
			isActive bool
			hostname string
		)

		total++

		if err = rows.Scan(&database, &toDrop.ShardID, &toDrop.Index, &isActive, &hostname); err != nil {
			log.Info("failed to scan stale database %s replica", "error", err)
			continue
		}

		if _, ok := notInSync[toDrop]; ok {
			log.Debug("skipping stale database replica cleanup that is not in sync", "database", database, "replica_id", toDrop)
			continue
		}

		if isActive {
			log.Debug("stale database replica is still active, skipping", "database", database, "replica_id", toDrop)
			continue
		}

		toExec, err := v1.IDFromHostname(cmd.cluster, hostname)
		if err != nil {
			log.Warn("failed to parse replica ID from hostname", "hostname", hostname, "error", err)
			continue
		}

		execConn, err := cmd.getConn(toExec)
		if err != nil {
			log.Warn("failed to get connection for replica", "replica_id", toExec, "error", err)
			continue
		}

		log.Debug("deleting stale database replica", "database", database, "replica_id", toDrop)

		err = execConn.Exec(ctx, fmt.Sprintf("SYSTEM DROP DATABASE REPLICA '%d|%d' FROM DATABASE `%s`", toDrop.ShardID, toDrop.Index, database))
		if err != nil {
			log.Info("failed to drop stale database replica", "replica_id", toDrop, "error", err)
			continue
		}

		succeed++
	}

	if total != succeed {
		return fmt.Errorf("some stale replicas are not cleaned up: %d/%d", succeed, total)
	}

	return nil
}

func (cmd *commander) getConn(id v1.ClickHouseReplicaID) (clickhouse.Conn, error) {
	cmd.lock.RLock()
	conn, ok := cmd.conns[id]
	cmd.lock.RUnlock()

	if ok {
		return conn, nil
	}

	cmd.lock.Lock()
	defer cmd.lock.Unlock()

	// Check if another goroutine created the connection while we were waiting for the lock
	if conn, ok := cmd.conns[id]; ok {
		return conn, nil
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", cmd.cluster.HostnameByID(id), PortManagement)},
		Auth: cmd.auth,
		Debugf: func(format string, args ...any) {
			cmd.log.Debug(fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		cmd.log.Error(err, "failed to open ClickHouse connection", "replica_id", id)
		return nil, fmt.Errorf("open ClickHouse connection: %w", err)
	}

	cmd.conns[id] = conn

	return conn, nil
}
