package controllerutil

import (
	"context"
	"net"
)

// DialContextFunc is a function that establishes a network connection to the given address.
// It is used to allow dependency injection of custom dialers (e.g., for port-forwarding in tests).
type DialContextFunc func(ctx context.Context, addr string) (net.Conn, error)
