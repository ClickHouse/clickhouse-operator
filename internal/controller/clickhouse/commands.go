package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/ClickHouse/clickhouse-go/v2"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
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
	OR replica_id >= ?`
)

type DatabaseDescriptor struct {
	Name         string `ch:"name"`
	EngineFull   string `ch:"engine_full"`
	UUID         string `ch:"uuid"`
	IsReplicated bool   `ch:"is_replicated"`
}

type Commander struct {
	log     util.Logger
	cluster *v1.ClickHouseCluster
	auth    clickhouse.Auth

	conns sync.Map
}

func NewCommander(log util.Logger, cluster *v1.ClickHouseCluster, secret *corev1.Secret) *Commander {
	return &Commander{
		log:     log.Named("commander"),
		conns:   sync.Map{},
		cluster: cluster,
		auth: clickhouse.Auth{
			Username: OperatorManagementUsername,
			Password: string(secret.Data[SecretKeyManagementPassword]),
		},
	}
}

func (cmd *Commander) Close() {
	cmd.conns.Range(func(id, conn interface{}) bool {
		if err := conn.(clickhouse.Conn).Close(); err != nil {
			cmd.log.Warn("error closing connection", "error", err, "replica_id", id)
		}

		return true
	})

	cmd.conns.Clear()
}

func (cmd *Commander) Ping(ctx context.Context, id v1.ReplicaID) error {
	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %v: %w", id, err)
	}

	return conn.Ping(ctx)
}

func (cmd *Commander) Databases(ctx context.Context, id v1.ReplicaID) (map[string]DatabaseDescriptor, error) {
	conn, err := cmd.getConn(id)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection for replica %v: %w", id, err)
	}

	rows, err := conn.Query(ctx, listDatabasesQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query databases on replica %v: %w", id, err)
	}
	defer func() {
		_ = rows.Close()
	}()

	databases := map[string]DatabaseDescriptor{}
	for rows.Next() {
		var db DatabaseDescriptor
		if err := rows.ScanStruct(&db); err != nil {
			return nil, fmt.Errorf("failed to scan database row on replica %v: %w", id, err)
		}
		databases[db.Name] = db
	}

	return databases, nil
}

func (cmd *Commander) CreateDatabases(ctx context.Context, id v1.ReplicaID, databases map[string]DatabaseDescriptor) error {
	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %v: %w", id, err)
	}

	for name, desc := range databases {
		query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` UUID '%s' ENGINE = %s", name, desc.UUID, desc.EngineFull)
		if err = conn.Exec(ctx, query); err != nil {
			return fmt.Errorf("failed to create database %s on replica %v: %w", name, id, err)
		}

		if desc.IsReplicated {
			if err = conn.Exec(ctx, fmt.Sprintf("SYSTEM SYNC DATABASE REPLICA `%s`", name)); err != nil {
				return fmt.Errorf("failed to sync replica for database %s on replica %v: %w", name, id, err)
			}
		}
	}

	return nil
}

// EnsureDefaultDatabaseEngine ensures that the default database engine is set to the Selected one.
func (cmd *Commander) EnsureDefaultDatabaseEngine(ctx context.Context, log util.Logger, cluster *v1.ClickHouseCluster, id v1.ReplicaID) error {
	log = log.With("replica_id", id)

	conn, err := cmd.getConn(id)
	if err != nil {
		return fmt.Errorf("failed to get connection for replica %v: %w", id, err)
	}

	var engine string
	rows := conn.QueryRow(ctx, "SELECT engine FROM system.databases WHERE name='default' ")
	if err = rows.Scan(&engine); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to scan default database engine for replica %v: %w", id, err)
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
			return err
		}

		if count > 0 {
			log.Warn("database `default` has tables, but its engine is not Replicated, data loss is possible")
		}

		log.Debug("dropping default database")
		if err := conn.Exec(ctx, "DROP DATABASE default SYNC"); err != nil {
			return fmt.Errorf("failed to drop default database on replica %v: %w", id, err)
		}
	}

	log.Debug("creating replicated default database")
	defaultDatabaseUUID := uuid.NewSHA1(uuid.Nil, []byte(cluster.SpecificName())).String()
	return conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS `default` UUID ? ENGINE=Replicated('/clickhouse/databases/default', '{shard}', '{replica}')", defaultDatabaseUUID)
}

func (cmd *Commander) SyncShard(ctx context.Context, log util.Logger, shardID int32) error {
	replicasToSync := make([]v1.ReplicaID, 0, cmd.cluster.Replicas())
	for i := int32(0); i < cmd.cluster.Replicas(); i++ {
		replicasToSync = append(replicasToSync, v1.ReplicaID{
			ShardID: shardID,
			Index:   i,
		})
	}

	results := util.ExecuteParallel(replicasToSync, func(id v1.ReplicaID) (v1.ReplicaID, struct{}, error) {
		errs := cmd.SyncReplica(ctx, log.With("replica_id", id), id)
		if len(errs) > 0 {
			return id, struct{}{}, fmt.Errorf("sync replica %v: %w", id, errors.Join(errs...))
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

func (cmd *Commander) SyncReplica(ctx context.Context, log util.Logger, id v1.ReplicaID) (errs []error) {
	databases, err := cmd.Databases(ctx, id)
	if err != nil {
		errs = append(errs, fmt.Errorf("get databases for replica %v: %w", id, err))
		return errs
	}

	conn, err := cmd.getConn(id)
	if err != nil {
		errs = append(errs, fmt.Errorf("get connection for replica %v: %w", id, err))
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
func (cmd *Commander) CleanupDatabaseReplicas(ctx context.Context, log util.Logger, notInSync map[v1.ReplicaID]struct{}) (bool, error) {
	var anyID v1.ReplicaID
	for id := range cmd.cluster.ReplicaIDs() {
		anyID = id
	}
	log = log.With("replica_id", anyID)
	conn, err := cmd.getConn(anyID)
	if err != nil {
		return false, fmt.Errorf("failed to get connection for replica %v: %w", anyID, err)
	}

	rows, err := conn.Query(ctx, listStaleDatabaseReplicasQuery, cmd.cluster.Shards(), cmd.cluster.Replicas())
	if err != nil {
		return false, fmt.Errorf("failed to query stale database replicas %v: %w", anyID, err)
	}
	defer func() {
		_ = rows.Close()
	}()

	total := 0
	succeed := 0
	for rows.Next() {
		var database string
		var toDrop v1.ReplicaID
		var isActive bool
		var hostname string
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

	return total == succeed, nil
}

func (cmd *Commander) getConn(id v1.ReplicaID) (clickhouse.Conn, error) {
	if conn, ok := cmd.conns.Load(id); ok {
		return conn.(clickhouse.Conn), nil
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", cmd.cluster.HostnameById(id), PortManagement)},
		Auth: cmd.auth,
		Debugf: func(format string, args ...interface{}) {
			cmd.log.Debug(fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		cmd.log.Error(err, "failed to open ClickHouse connection", "replica_id", id)
		return nil, err
	}

	cmd.conns.Store(id, conn)
	return conn, nil
}
