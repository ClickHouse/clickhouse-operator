package keeper

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/clickhouse-operator/internal/util"
)

const (
	FLWCommand = "mntr"

	ModeLeader     = "leader"
	ModeFollower   = "follower"
	ModeStandalone = "standalone"
)

var (
	ClusterModes = []string{ModeLeader, ModeFollower}
)

// ServerStatus holds parsed fields of the "mntr" command response.
type ServerStatus struct {
	ServerState string
	Followers   int
}

type Connections map[string]net.Conn

func (c Connections) Close() {
	for _, connection := range c {
		_ = connection.Close()
	}
}

type dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func getConnections(ctx context.Context, log util.Logger, hostnamesByID map[string]string, tlsRequired bool) (Connections, []error) {
	var d dialer = &net.Dialer{}
	port := PortNative
	if tlsRequired {
		d = &tls.Dialer{
			NetDialer: &net.Dialer{},
			Config: &tls.Config{
				//nolint:gosec // User manged certificate may be outdated or issued for other hostnames.
				InsecureSkipVerify: true,
			},
		}
		port = PortNativeSecure
	}

	result := Connections{}
	var connErrs []error
	for id, host := range hostnamesByID {
		conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
		if err != nil {
			connErrs = append(connErrs, fmt.Errorf("connect to %s: %w", host, err))
			continue
		}
		result[id] = conn
	}

	if len(connErrs) > 0 && len(result) > 0 {
		log.Debug(fmt.Sprintf("open keeper connections, opened: %d, expected: %d", len(result), len(hostnamesByID)))
	}

	return result, connErrs
}

func queryPod(ctx context.Context, log util.Logger, conn net.Conn) (ServerStatus, error) {
	log.Debug(fmt.Sprintf("querying keeper pod: %s", conn.RemoteAddr().String()))
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return ServerStatus{}, fmt.Errorf("set deadline: %w", err)
		}
	}

	n, err := io.WriteString(conn, FLWCommand)
	if err != nil {
		return ServerStatus{}, fmt.Errorf("write command: %w", err)
	}
	if n != len(FLWCommand) {
		return ServerStatus{}, fmt.Errorf("can't write the whole string to socket expected: %d; actual: %d", len(FLWCommand), n)
	}

	reader := bufio.NewReader(conn)
	data, err := io.ReadAll(reader)
	if err != nil {
		return ServerStatus{}, fmt.Errorf("got error while reading from socket: %w", err)
	}

	statMap := map[string]string{}
	for i, stat := range strings.Split(string(data), "\n") {
		if len(stat) == 0 {
			continue
		}
		parts := strings.Split(stat, "\t")
		if len(parts) != 2 {
			return ServerStatus{}, fmt.Errorf("failed to parse response line %d: %q", i, stat)
		}

		statMap[parts[0]] = parts[1]
	}

	result := ServerStatus{
		ServerState: statMap["zk_server_state"],
	}
	if result.ServerState == "" {
		return ServerStatus{}, fmt.Errorf("response missing required field 'Mode': %q", string(data))
	}

	if result.ServerState == ModeLeader {
		if followers, ok := statMap["zk_followers"]; ok {
			result.Followers, err = strconv.Atoi(followers)
			if err != nil {
				return ServerStatus{}, fmt.Errorf("failed to parse field 'zk_followers': %w", err)
			}
		} else {
			log.Warn("'zk_followers' is missing in keeper response")
			return ServerStatus{}, fmt.Errorf("response missing required field 'Followers': %q", string(data))
		}
	}

	return result, nil
}

func queryAllPods(ctx context.Context, log util.Logger, connections Connections) map[string]ServerStatus {
	result, err := util.ExecuteParallel(slices.Collect(maps.Keys(connections)), func(id string) (string, ServerStatus, error) {
		resp, err := queryPod(ctx, log.With("replica_id", id), connections[id])
		return id, resp, err
	})
	log.Debug("all keeper pods have responded")

	if err != nil {
		if errs := util.UnwrapErrors(err); errs != nil {
			for _, err := range errs {
				log.Info("failed to query keeper pod", "error", err, "replica_id", err.(util.ExecutionError).Id)
			}
		} else {
			log.Info("failed to query keeper pods", "error", err)
		}
	}

	return result
}

func getServersStates(ctx context.Context, log util.Logger, hostnamesByID map[string]string, tlsRequired bool) map[string]ServerStatus {
	connections, errs := getConnections(ctx, log, hostnamesByID, tlsRequired)
	for _, err := range errs {
		log.Info("error getting keeper connection", "error", err)
	}

	if len(connections) == 0 {
		return nil
	}

	defer connections.Close()
	return queryAllPods(ctx, log, connections)
}
