// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package grpcsim connects gRPC clients and servers to netsim.
package grpcsim

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/hmdsefi/faultline/shims/netsim"
)

// Target returns "passthrough:///" + addr, so gRPC passes addr to the dialer without DNS.
func Target(addr string) string { return "passthrough:///" + addr }

// WithHost returns grpc.WithContextDialer with a dialer that calls h.DialContext(ctx, "tcp", addr).
func WithHost(h *netsim.Host) grpc.DialOption {
	return grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		return h.DialContext(ctx, "tcp", addr)
	})
}

// NewClient is grpc.NewClient(Target(addr), WithHost(h), insecure transport credentials,
// connect parameters with jitter 0, opts...). Options in opts override these.
func NewClient(h *netsim.Host, addr string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	base := []grpc.DialOption{
		WithHost(h),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: time.Second, Multiplier: 1.6, Jitter: 0, MaxDelay: 120 * time.Second},
			MinConnectTimeout: 20 * time.Second,
		}),
	}
	return grpc.NewClient(Target(addr), append(base, opts...)...)
}

// Listen is h.Listen("tcp", address). Serve it with (*grpc.Server).Serve.
func Listen(h *netsim.Host, address string) (net.Listener, error) { return h.Listen("tcp", address) }
