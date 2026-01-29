package keeper

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	FLWCommand = "mntr"

	ModeLeader     = "leader"
	ModeFollower   = "follower"
	ModeStandalone = "standalone"
)

var (
	clusterModes = []string{ModeLeader, ModeFollower}
)

// serverStatus holds parsed fields of the "mntr" command response.
type serverStatus struct {
	ServerState string
	Followers   int
}

type dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func getConnection(ctx context.Context, hostname string, tlsRequired bool) (net.Conn, error) {
	var d dialer = &net.Dialer{}

	port := PortNative
	if tlsRequired {
		d = &tls.Dialer{
			NetDialer: &net.Dialer{},
			Config: &tls.Config{
				//nolint:gosec // User managed certificate may be outdated or issued for other hostnames.
				InsecureSkipVerify: true,
			},
		}
		port = PortNativeSecure
	}

	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", hostname, port))
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", hostname, err)
	}

	return conn, nil
}

func queryKeeper(ctx context.Context, log controllerutil.Logger, conn net.Conn) (serverStatus, error) {
	log.Debug("querying keeper pod: " + conn.RemoteAddr().String())

	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return serverStatus{}, fmt.Errorf("set deadline: %w", err)
		}
	}

	n, err := io.WriteString(conn, FLWCommand)
	if err != nil {
		return serverStatus{}, fmt.Errorf("write command: %w", err)
	}

	if n != len(FLWCommand) {
		return serverStatus{}, fmt.Errorf("can't write the whole string to socket expected: %d; actual: %d", len(FLWCommand), n)
	}

	reader := bufio.NewReader(conn)

	data, err := io.ReadAll(reader)
	if err != nil {
		return serverStatus{}, fmt.Errorf("got error while reading from socket: %w", err)
	}

	statMap := map[string]string{}
	for i, stat := range strings.Split(string(data), "\n") {
		if len(stat) == 0 {
			continue
		}

		parts := strings.Split(stat, "\t")
		if len(parts) != 2 {
			return serverStatus{}, fmt.Errorf("failed to parse response line %d: %q", i, stat)
		}

		statMap[parts[0]] = parts[1]
	}

	result := serverStatus{
		ServerState: statMap["zk_server_state"],
	}
	if result.ServerState == "" {
		return serverStatus{}, fmt.Errorf("response missing required field 'Mode': %q", string(data))
	}

	if result.ServerState == ModeLeader {
		if followers, ok := statMap["zk_followers"]; ok {
			result.Followers, err = strconv.Atoi(followers)
			if err != nil {
				return serverStatus{}, fmt.Errorf("failed to parse field 'zk_followers': %w", err)
			}
		} else {
			log.Warn("'zk_followers' is missing in keeper response")
			return serverStatus{}, fmt.Errorf("response missing required field 'Followers': %q", string(data))
		}
	}

	return result, nil
}

func getServerStatus(ctx context.Context, log controllerutil.Logger, hostname string, tlsRequired bool) serverStatus {
	conn, err := getConnection(ctx, hostname, tlsRequired)
	if err != nil {
		log.Info("failed to get keeper connection", "error", err)
		return serverStatus{}
	}
	defer func(conn net.Conn) {
		if err := conn.Close(); err != nil {
			log.Warn("failed to close connection", "error", err)
		}
	}(conn)

	status, err := queryKeeper(ctx, log, conn)
	if err != nil {
		log.Info("failed to query keeper pod", "error", err)
		return serverStatus{}
	}

	return status
}
