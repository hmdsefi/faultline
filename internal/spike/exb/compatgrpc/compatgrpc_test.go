// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package compatgrpc is C6's gRPC test (EXB-055): informational, it does not gate compatibility.
package compatgrpc

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/shims/grpcsim"
)

// TestMain clears FAULTLINE_CHECK_DETERMINISM, which CI's nested job sets: Part A replays the
// corpus only at level 3, and exbmeasure measures that (EXB-061), so a divergence here is a
// measurement, not a test failure. Measured runs never set the variable (EXB-060).
func TestMain(m *testing.M) {
	os.Unsetenv("FAULTLINE_CHECK_DETERMINISM")
	os.Exit(m.Run())
}

func TestCompatGRPC(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 3, Duration: 5 * time.Second}, func(w *faultline.World) {
		var got atomic.Value
		w.AddProcess("srv", func(ctx context.Context, p *faultline.Process) error {
			ln, err := grpcsim.Listen(p.Net, ":50051")
			if err != nil {
				return err
			}
			s := grpc.NewServer()
			healthpb.RegisterHealthServer(s, health.NewServer())
			p.Go(func() {
				<-ctx.Done()
				s.Stop()
			})
			_ = s.Serve(ln)
			return nil
		})
		w.AddProcess("cli", func(ctx context.Context, p *faultline.Process) error {
			cc, err := grpcsim.NewClient(p.Net, "srv:50051")
			if err != nil {
				return err
			}
			defer cc.Close()
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			resp, err := healthpb.NewHealthClient(cc).Check(cctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
			if err != nil {
				got.Store("error: " + err.Error())
			} else {
				got.Store(resp.GetStatus().String())
			}
			<-ctx.Done()
			return nil
		})
		w.Final("health", func() error {
			if s, _ := got.Load().(string); s != "SERVING" {
				return fmt.Errorf("health check: %q", s)
			}
			return nil
		})
	})
}
