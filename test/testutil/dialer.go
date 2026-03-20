package testutil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport/spdy"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

const (
	portForwardProtocol = "portforward.k8s.io"
)

// streamConn wraps an httpstream.Stream (SPDY data stream) as a net.Conn.
type streamConn struct {
	httpstream.Stream

	spdyConn httpstream.Connection
	addr     string
}

func (c *streamConn) Close() error {
	defer func() {
		_ = c.spdyConn.Close()
	}()

	if err := c.Stream.Close(); err != nil {
		return fmt.Errorf("close SPDY data stream: %w", err)
	}

	return nil
}

func (c *streamConn) LocalAddr() net.Addr  { return spdyAddr("127.0.0.1:0") }
func (c *streamConn) RemoteAddr() net.Addr { return spdyAddr(c.addr) }

func (c *streamConn) SetDeadline(_ time.Time) error      { return nil }
func (c *streamConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(_ time.Time) error { return nil }

type spdyAddr string

func (a spdyAddr) Network() string { return "spdy" }
func (a spdyAddr) String() string  { return string(a) }

// NewPortForwardDialer returns a DialContextFunc that connects to pods inside a Kubernetes cluster
// by creating SPDY port-forward streams through the API server.
// Pod hostnames are expected in the format: {podName}.{serviceName}.{namespace}.svc.{domain}.
func NewPortForwardDialer(config *rest.Config) controllerutil.DialContextFunc {
	return func(_ context.Context, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("split host port %q: %w", addr, err)
		}

		// Parse pod hostname: {podName}.{serviceName}.{namespace}.svc.{domain}
		parts := strings.SplitN(host, ".", 4)
		if len(parts) < 3 {
			return nil, fmt.Errorf(
				"can't parse pod hostname %q: expected {pod}.{service}.{namespace}[.svc.domain]",
				host,
			)
		}

		podName := parts[0]
		namespace := parts[2]

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("create k8s client: %w", err)
		}

		transport, upgrader, err := spdy.RoundTripperFor(config)
		if err != nil {
			return nil, fmt.Errorf("create SPDY round tripper: %w", err)
		}

		reqURL := clientset.CoreV1().
			RESTClient().
			Post().
			Resource("pods").
			Namespace(namespace).
			Name(podName).
			SubResource("portforward").
			URL()

		spdyDialer := spdy.NewDialer(
			upgrader,
			&http.Client{Transport: transport},
			http.MethodPost,
			reqURL,
		)

		spdyConn, _, err := spdyDialer.Dial(portForwardProtocol)
		if err != nil {
			return nil, fmt.Errorf("SPDY dial to %s/%s: %w", namespace, podName, err)
		}

		requestID := uuid.New().String()

		// Create error stream first (required by the port-forward protocol).
		errorHeaders := http.Header{}
		errorHeaders.Set(corev1.StreamType, corev1.StreamTypeError)
		errorHeaders.Set(corev1.PortHeader, portStr)
		errorHeaders.Set(corev1.PortForwardRequestIDHeader, requestID)

		errorStream, err := spdyConn.CreateStream(errorHeaders)
		if err != nil {
			_ = spdyConn.Close()

			return nil, fmt.Errorf("create error stream for %s/%s: %w", namespace, podName, err)
		}

		// Close the write side — we only read errors from this stream.
		_ = errorStream.Close()

		// Create data stream.
		dataHeaders := http.Header{}
		dataHeaders.Set(corev1.StreamType, corev1.StreamTypeData)
		dataHeaders.Set(corev1.PortHeader, portStr)
		dataHeaders.Set(corev1.PortForwardRequestIDHeader, requestID)

		dataStream, err := spdyConn.CreateStream(dataHeaders)
		if err != nil {
			_ = spdyConn.Close()

			return nil, fmt.Errorf("create data stream for %s/%s: %w", namespace, podName, err)
		}

		return &streamConn{
			Stream:   dataStream,
			spdyConn: spdyConn,
			addr:     addr,
		}, nil
	}
}
