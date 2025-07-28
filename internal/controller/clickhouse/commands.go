package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ClickHouse/clickhouse-go/v2"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	corev1 "k8s.io/api/core/v1"
)

type DatabaseDescriptor struct {
	Name         string `ch:"name"`
	EngineFull   string `ch:"engine_full"`
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

	rows, err := conn.Query(ctx, `
SELECT name, engine_full, engine = 'Replicated' AS is_replicated
FROM system.databases 
WHERE engine NOT IN ('Atomic', 'Lazy', 'SQLite', 'Ordinary')
SETTINGS format_display_secrets_in_show_and_select=1`)
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
		query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` ENGINE = %s", name, desc.EngineFull)
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

func (cmd *Commander) SyncShard(ctx context.Context, log util.Logger, shardID int32) error {
	replicasToSync := make([]v1.ReplicaID, 0, cmd.cluster.Replicas())
	for i := int32(0); i < cmd.cluster.Replicas(); i++ {
		replicasToSync = append(replicasToSync, v1.ReplicaID{
			ShardID: shardID,
			Index:   i,
		})
	}

	_, err := util.ExecuteParallel(replicasToSync, func(id v1.ReplicaID) (v1.ReplicaID, struct{}, error) {
		errs := cmd.SyncReplica(ctx, log.With("replica_id", id), id)
		if len(errs) > 0 {
			return id, struct{}{}, fmt.Errorf("sync replica %v: %w", id, errors.Join(errs...))
		}
		return id, struct{}{}, nil
	})

	return err
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
