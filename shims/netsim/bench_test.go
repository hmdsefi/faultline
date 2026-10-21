// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/netsim"
)

// perfRun runs body in a TraceHash goroutine-mode world with 100 µs links and returns the wall
// time and the number of heap allocations of the run. A row divides the work its body counted, not
// a fixed count, by that time: with FAULTLINE_CHECK_DETERMINISM=1 the seed runs twice inside the
// one Run, and so does the work.
func perfRun(t *testing.T, body func(w *faultline.World)) (time.Duration, uint64) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 1, Duration: 120 * time.Second,
		MaxEvents: math.MaxUint64, Drain: time.Millisecond,
		Net: simnet.Config{Default: simnet.Link{Latency: 100 * time.Microsecond}}}, body)
	wall := time.Since(start)
	runtime.ReadMemStats(&after)
	return wall, after.Mallocs - before.Mallocs
}

// raceBuild reports whether the test binary was built with -race (Go records the setting only
// when it was given).
func raceBuild() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

func TestNetsimThroughput(t *testing.T) {
	if os.Getenv("FAULTLINE_PERF") != "1" {
		t.Skip("set FAULTLINE_PERF=1 to measure NSM §9 targets (without -race)")
	}
	if raceBuild() {
		t.Skip("NSM §9 targets are measured without -race, and this test binary was built with -race")
	}
	report := func(t *testing.T, name string, rate, target float64, unit string, allocs uint64, ops float64) {
		t.Logf("%s: %.0f %s (target >= %.0f), %.1f allocs/op", name, rate, unit, target, float64(allocs)/ops)
		if rate < target {
			t.Errorf("%s: %.0f %s, want >= %.0f", name, rate, unit, target)
		}
	}
	echoServer := func(_ context.Context, p *faultline.Process) error {
		ln, err := p.Net.Listen("tcp", ":7")
		if err != nil {
			return err
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				return nil
			}
			p.Go(func() { _, _ = io.Copy(c, c); c.Close() })
		}
	}
	t.Run("Echo1K", func(t *testing.T) {
		const ops = 20_000
		var done atomic.Int64
		wall, allocs := perfRun(t, func(w *faultline.World) {
			w.AddProcess("srv", echoServer)
			w.AddProcess("cli", func(ctx context.Context, p *faultline.Process) error {
				c, err := p.Net.Dial("tcp", "srv:7")
				if err != nil {
					return err
				}
				buf := make([]byte, 1024)
				for range ops {
					if _, err := c.Write(buf); err != nil {
						return err
					}
					if _, err := io.ReadFull(c, buf); err != nil {
						return err
					}
					done.Add(1)
				}
				<-ctx.Done()
				return nil
			})
		})
		report(t, "BenchmarkEcho1K", float64(done.Load())/wall.Seconds(), 20_000, "ops/s", allocs, float64(done.Load()))
	})
	t.Run("Stream1M", func(t *testing.T) {
		const streams = 50
		var done atomic.Int64 // streams read to EOF, 1 MiB each
		wall, allocs := perfRun(t, func(w *faultline.World) {
			w.AddProcess("srv", func(ctx context.Context, p *faultline.Process) error {
				ln, err := p.Net.Listen("tcp", ":7")
				if err != nil {
					return err
				}
				for {
					c, err := ln.Accept()
					if err != nil {
						return nil
					}
					_, _ = io.Copy(io.Discard, c)
					c.Close()
				}
			})
			w.AddProcess("cli", func(ctx context.Context, p *faultline.Process) error {
				data := make([]byte, 1<<20)
				for range streams {
					c, err := p.Net.Dial("tcp", "srv:7")
					if err != nil {
						return err
					}
					if _, err := c.Write(data); err != nil {
						return err
					}
					tc, ok := c.(*netsim.Conn)
					if !ok {
						return fmt.Errorf("Dial returned a %T", c)
					}
					if err := tc.CloseWrite(); err != nil {
						return err
					}
					_, _ = io.Copy(io.Discard, c)
					c.Close()
					done.Add(1)
				}
				<-ctx.Done()
				return nil
			})
		})
		report(t, "BenchmarkStream1M", float64(done.Load())/wall.Seconds(), 40, "MiB/s", allocs, float64(done.Load()))
	})
	t.Run("DialClose", func(t *testing.T) {
		const ops = 5_000
		var done atomic.Int64
		wall, allocs := perfRun(t, func(w *faultline.World) {
			w.AddProcess("srv", func(ctx context.Context, p *faultline.Process) error {
				ln, err := p.Net.Listen("tcp", ":7")
				if err != nil {
					return err
				}
				for {
					c, err := ln.Accept()
					if err != nil {
						return nil
					}
					c.Close()
				}
			})
			w.AddProcess("cli", func(ctx context.Context, p *faultline.Process) error {
				for range ops {
					c, err := p.Net.Dial("tcp", "srv:7")
					if err != nil {
						return err
					}
					c.Close()
					done.Add(1)
				}
				<-ctx.Done()
				return nil
			})
		})
		report(t, "BenchmarkDialClose", float64(done.Load())/wall.Seconds(), 5_000, "ops/s", allocs, float64(done.Load()))
	})
	t.Run("UDP", func(t *testing.T) {
		const ops = 30_000
		var done atomic.Int64
		wall, allocs := perfRun(t, func(w *faultline.World) {
			w.AddProcess("srv", func(ctx context.Context, p *faultline.Process) error {
				pc, err := p.Net.ListenPacket("udp", ":9")
				if err != nil {
					return err
				}
				buf := make([]byte, 512)
				for {
					n, from, err := pc.ReadFrom(buf)
					if err != nil {
						return nil
					}
					_, _ = pc.WriteTo(buf[:n], from)
				}
			})
			w.AddProcess("cli", func(ctx context.Context, p *faultline.Process) error {
				c, err := p.Net.Dial("udp", "srv:9")
				if err != nil {
					return err
				}
				buf := make([]byte, 512)
				for range ops {
					if _, err := c.Write(buf); err != nil {
						return err
					}
					if _, err := c.Read(buf); err != nil {
						return err
					}
					done.Add(1)
				}
				<-ctx.Done()
				return nil
			})
		})
		report(t, "BenchmarkUDP", float64(done.Load())/wall.Seconds(), 30_000, "ops/s", allocs, float64(done.Load()))
	})
	t.Run("HTTPGet", func(t *testing.T) {
		const ops = 5_000
		var done atomic.Int64
		wall, allocs := perfRun(t, func(w *faultline.World) {
			w.AddProcess("srv", func(ctx context.Context, p *faultline.Process) error {
				ln, err := p.Net.Listen("tcp", ":80")
				if err != nil {
					return err
				}
				body := make([]byte, 1024)
				srv := &http.Server{ReadHeaderTimeout: 10 * time.Second,
					Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { _, _ = rw.Write(body) })}
				p.Go(func() { <-ctx.Done(); srv.Close() })
				_ = srv.Serve(ln)
				return nil
			})
			w.AddProcess("cli", func(ctx context.Context, p *faultline.Process) error {
				cl := netsim.HTTPClient(p.Net)
				for range ops {
					resp, err := cl.Get("http://srv:80/")
					if err != nil {
						return err
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					done.Add(1)
				}
				cl.CloseIdleConnections()
				<-ctx.Done()
				return nil
			})
		})
		report(t, "BenchmarkHTTPGet", float64(done.Load())/wall.Seconds(), 5_000, "ops/s", allocs, float64(done.Load()))
	})
}
