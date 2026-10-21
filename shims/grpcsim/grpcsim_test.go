// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package grpcsim_test

import (
	"context"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/grpcsim"
)

func TestTarget(t *testing.T) {
	if got := grpcsim.Target("b:50051"); got != "passthrough:///b:50051" {
		t.Fatalf("Target = %q", got)
	}
}

// countingListener counts the connections it accepts.
type countingListener struct {
	net.Listener
	accepts *atomic.Int64
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return c, err
}

// healthServer returns a process that serves grpc_health_v1 on :50051 and counts the
// connections it accepts in accepts.
func healthServer(accepts *atomic.Int64) func(context.Context, *faultline.Process) error {
	return func(ctx context.Context, p *faultline.Process) error {
		ln, err := grpcsim.Listen(p.Net, ":50051")
		if err != nil {
			return err
		}
		srv := grpc.NewServer()
		healthpb.RegisterHealthServer(srv, health.NewServer())
		p.Go(func() { <-ctx.Done(); srv.Stop() })
		_ = srv.Serve(countingListener{ln, accepts})
		return nil
	}
}

func TestATNSM30_GRPCHealthAndReconnect(t *testing.T) {
	var mu sync.Mutex
	var got []string
	var accepts atomic.Int64
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: 10 * time.Second,
		Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond}}}, func(w *faultline.World) {
		got = nil
		accepts.Store(0)
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			cc, err := grpcsim.NewClient(p.Net, "b:50051")
			if err != nil {
				return err
			}
			defer cc.Close()
			client := healthpb.NewHealthClient(cc)
			check := func() string {
				cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				resp, err := client.Check(cctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
				if err != nil {
					return "error: " + err.Error()
				}
				return resp.GetStatus().String()
			}
			time.Sleep(100 * time.Millisecond)
			first := check()
			time.Sleep(5*time.Second - time.Since(kernel.Epoch))
			second := check()
			mu.Lock()
			got = []string{first, second}
			mu.Unlock()
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", healthServer(&accepts))
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(time.Second), Kind: fault.KindCrash, Node: "b"},
			fault.Event{At: kernel.Time(2 * time.Second), Kind: fault.KindRestart, Node: "b"},
		))
	})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "SERVING" || got[1] != "SERVING" {
		t.Fatalf("checks %q, want SERVING twice (the second after b's restart)", got)
	}
	if n := accepts.Load(); n != 2 {
		t.Fatalf("b accepted %d connections, want 2 (a reconnects after b's restart)", n)
	}
}

// dial is one dial attempt seen by a test dialer: when it returned and whether it connected.
type dial struct {
	At time.Duration
	OK bool
}

// TestNewClientBackoff checks NewClient's backoff and that a dialer in opts replaces WithHost.
// b listens only from 4 s. Each refused dial returns after one round trip (2 ms); gRPC then
// waits 1 s, 1.6 s and 2.56 s (BaseDelay 1 s, Multiplier 1.6, Jitter 0) before it dials again,
// so the fourth dial, from 5.166 s, connects.
func TestNewClientBackoff(t *testing.T) {
	var mu sync.Mutex
	var dials []dial
	var got string
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: 10 * time.Second,
		Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond}}}, func(w *faultline.World) {
		dials, got = nil, ""
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			dialer := grpc.WithContextDialer(func(dctx context.Context, addr string) (net.Conn, error) {
				c, err := p.Net.DialContext(dctx, "tcp", addr)
				mu.Lock()
				dials = append(dials, dial{time.Since(kernel.Epoch), err == nil})
				mu.Unlock()
				return c, err
			})
			cc, err := grpcsim.NewClient(p.Net, "b:50051", dialer)
			if err != nil {
				return err
			}
			defer cc.Close()
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			resp, err := healthpb.NewHealthClient(cc).Check(cctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
			mu.Lock()
			if err != nil {
				got = "error: " + err.Error()
			} else {
				got = resp.GetStatus().String()
			}
			mu.Unlock()
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(4 * time.Second)
			return healthServer(new(atomic.Int64))(ctx, p)
		})
	})
	mu.Lock()
	defer mu.Unlock()
	want := []dial{
		{2 * time.Millisecond, false},
		{1004 * time.Millisecond, false},
		{2606 * time.Millisecond, false},
		{5168 * time.Millisecond, true},
	}
	if !slices.Equal(dials, want) {
		t.Fatalf("dials %v, want %v", dials, want)
	}
	if got != "SERVING" {
		t.Fatalf("check %q, want SERVING", got)
	}
}

// TestNewClientConnectTimeout checks that the dial gets gRPC's connect deadline: b is isolated,
// so a's first dial, from 1 s, ends at 21 s when MinConnectTimeout (20 s) runs out. A dial that
// ignored the deadline would end at 36 s (ConnTimeout 30 s, checked at a SYN retransmission).
func TestNewClientConnectTimeout(t *testing.T) {
	var mu sync.Mutex
	var failedAt time.Duration
	var code codes.Code
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: 40 * time.Second,
		Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond}}}, func(w *faultline.World) {
		failedAt, code = 0, codes.OK
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(time.Second)
			cc, err := grpcsim.NewClient(p.Net, "b:50051")
			if err != nil {
				return err
			}
			defer cc.Close()
			_, err = healthpb.NewHealthClient(cc).Check(ctx, &healthpb.HealthCheckRequest{})
			mu.Lock()
			failedAt, code = time.Since(kernel.Epoch), status.Code(err)
			mu.Unlock()
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", healthServer(new(atomic.Int64)))
		isolate := fault.Script(fault.Event{At: kernel.Time(time.Millisecond), Kind: fault.KindIsolate, Node: "b"})
		w.Plan(isolate)
	})
	mu.Lock()
	defer mu.Unlock()
	if failedAt != 21*time.Second || code != codes.Unavailable {
		t.Fatalf("check failed at %v with %v, want 21s with Unavailable", failedAt, code)
	}
}
