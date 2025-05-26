package keeper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

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

func getConnections(ctx context.Context, log util.Logger, hostnamesByID map[string]string) (Connections, []error) {
	result := Connections{}
	var connErrs []error
	for id, host := range hostnamesByID {
		d := net.Dialer{Timeout: time.Second * 10}
		conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, PortNative))
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
	// Contains the hostName along with parsed response from it
	type NamedFourLetterCommandResponse struct {
		id    string
		state ServerStatus
	}

	// Buffered channels will block only if buffer is full
	// In out case it won't block in any situation
	fails := make(chan error, len(connections))
	resultChan := make(chan NamedFourLetterCommandResponse, len(connections))

	var wg sync.WaitGroup
	for id, conn := range connections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := queryPod(ctx, log.With("replica_id", id), conn)
			if err != nil {
				fails <- err
			} else {
				resultChan <- NamedFourLetterCommandResponse{id, resp}
			}
		}()
	}

	result := map[string]ServerStatus{}
	errs := []error{}

	// Wait for all goroutines to return error or result
	wg.Wait()
	log.Debug("all keeper pods have responded")
	close(fails)
	close(resultChan)

	for err := range fails {
		errs = append(errs, err)
	}

	for response := range resultChan {
		result[response.id] = response.state
	}

	for _, err := range errs {
		log.Info("failed to query keeper pod", "error", err)
	}

	return result
}

func getServersStates(ctx context.Context, log util.Logger, hostnamesByID map[string]string) map[string]ServerStatus {
	connections, errs := getConnections(ctx, log, hostnamesByID)
	for _, err := range errs {
		log.Info("error getting keeper connection", "error", err)
	}

	if len(connections) == 0 {
		return nil
	}

	defer connections.Close()
	return queryAllPods(ctx, log, connections)
}
