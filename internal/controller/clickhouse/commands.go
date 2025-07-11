package clickhouse

import (
	"context"
	"fmt"
	"sync"

	"github.com/ClickHouse/clickhouse-go/v2"
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	corev1 "k8s.io/api/core/v1"
)

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
