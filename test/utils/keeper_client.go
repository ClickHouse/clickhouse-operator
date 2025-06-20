package utils

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller/keeper"
	"github.com/go-zookeeper/zk"
	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive,staticcheck
)

const (
	testDataKey = "/%d_test_data_%d"
	testDataVal = "test data value %d"
)

type zkLogger struct{}

func (l zkLogger) Printf(s string, args ...any) {
	GinkgoWriter.Printf(s+"\n", args...)
}

type KeeperClient struct {
	cancel context.CancelFunc
	cmds   []*exec.Cmd
	client *zk.Conn
}

func NewKeeperClient(ctx context.Context, cr *v1.KeeperCluster) (*KeeperClient, error) {
	ctx, cancel := context.WithCancel(ctx)
	addrs, cmds, err := forwardNodes(ctx, cr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("forwarding zk nodes failed: %w", err)
	}

	var dialer zk.Dialer = func(network, address string, timeout time.Duration) (net.Conn, error) {
		if !cr.Spec.Settings.TLS.Required {
			return net.DialTimeout(network, address, timeout)
		}

		timeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		//nolint:gosec // Test certs are self signed, so we skip verification.
		dial := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
		return dial.DialContext(timeCtx, network, address)
	}

	conn, _, err := zk.Connect(addrs, 5*time.Second, zk.WithLogger(zkLogger{}), zk.WithDialer(dialer))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connecting to zk %v failed: %w", cr.GetNamespacedName(), err)
	}

	return &KeeperClient{
		cancel: cancel,
		cmds:   cmds,
		client: conn,
	}, nil
}

func (c *KeeperClient) Close() {
	c.client.Close()
	c.cancel()
	for _, cmd := range c.cmds {
		if err := cmd.Wait(); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "wait port forward to finish: %s\n", err)
		}
	}
}

func (c *KeeperClient) CheckWrite(order int) error {
	for i := range 10 {
		path := fmt.Sprintf(testDataKey, order, i)
		if _, err := c.client.Create(path, []byte(fmt.Sprintf(testDataVal, i)), 0, nil); err != nil {
			return fmt.Errorf("creating test data failed: %w", err)
		}
		if _, err := c.client.Sync(path); err != nil {
			return fmt.Errorf("sync test data failed: %w", err)
		}
	}

	return nil
}

func (c *KeeperClient) CheckRead(order int) error {
	for i := range 10 {
		data, _, err := c.client.Get(fmt.Sprintf(testDataKey, order, i))
		if err != nil {
			return fmt.Errorf("check test data failed: %w", err)
		}

		if string(data) != fmt.Sprintf(testDataVal, i) {
			return fmt.Errorf("check test data failed: expected %q, got %q", fmt.Sprintf(testDataVal, i), string(data))
		}
	}

	return nil
}

func forwardNodes(ctx context.Context, cr *v1.KeeperCluster) ([]string, []*exec.Cmd, error) {
	keeperAddrs := make([]string, 0, cr.Replicas())
	keeperCmds := make([]*exec.Cmd, 0, len(keeperAddrs))

	keeperPort := keeper.PortNative
	if cr.Spec.Settings.TLS.Required {
		keeperPort = keeper.PortNativeSecure
	}

	for _, id := range cr.Status.Replicas {
		pod := fmt.Sprintf("%s-0", cr.StatefulSetNameByReplicaID(id))
		port, err := GetFreePort()
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get free port: %w", err)
		}

		keeperAddrs = append(keeperAddrs, fmt.Sprintf("127.0.0.1:%d", port))
		cmd := exec.CommandContext(ctx, "kubectl", "port-forward", pod, fmt.Sprintf("%d:%d", port, keeperPort),
			"--namespace", cr.Namespace)
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		keeperCmds = append(keeperCmds, cmd)
		_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", strings.Join(cmd.Args, " "))

		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, nil, fmt.Errorf("fail to create stderr pipe for port forward to keeper pod %q: %w", pod, err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, fmt.Errorf("fail to create stdout pipe for port forward to keeper pod %q: %w", pod, err)
		}

		if err = cmd.Start(); err != nil {
			return nil, nil, fmt.Errorf("fail to create port forward to keeper pod %q: %w", pod, err)
		}

		data, err := bufio.NewReader(stdout).ReadString('\n') // Wait the port forward to report it started
		if err != nil {
			return nil, nil, fmt.Errorf("fail to read to from pod %q port forward stdout: %w", pod, err)
		}

		if !strings.HasPrefix(data, "Forwarding from") {
			stderrData, _ := bufio.NewReader(stderr).ReadString('\n')
			return nil, nil, fmt.Errorf("unexpected output from pod %q port forward: stdout: %q, stderr %q",
				pod, data, stderrData)
		}
	}

	return keeperAddrs, keeperCmds, nil
}
