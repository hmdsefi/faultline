// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/shims/netsim"
)

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["netsim-invalid"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, NetSim: netsim.Config{MSS: 32}}, func(w *World) {})
	}
	gorScenarios["gor-21"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 20, Duration: time.Second, CheckDeterminism: true, KeepGoing: true},
			func(w *World) {
				w.AddProcess("srv", func(ctx context.Context, p *Process) error {
					if _, err := p.Net.ListenPacket("udp", ":9"); err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				})
				w.AddProcess("cli", func(ctx context.Context, p *Process) error {
					c, err := p.Net.Dial("udp", "srv:9")
					if err != nil {
						return err
					}
					time.Sleep(10 * time.Millisecond)
					for range 1 + rand.IntN(2) { //nolint:gosec // G404: the global draw is the point of AT-GOR-21
						_, _ = c.Write([]byte("x"))
					}
					<-ctx.Done()
					return nil
				})
			})
	}
}

func TestGoroutineNetSimOption(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		res := runGorScenario(t, "netsim-invalid")
		want := "faultline: invalid Options.NetSim: invalid netsim config: MSS 32 out of range [64, 65535]"
		if res.code != 1 || !strings.Contains(res.out, want) {
			t.Fatalf("exit %d\n%s", res.code, res.out)
		}
	})
	t.Run("default", func(t *testing.T) {
		var eff Options
		gw(t, Options{Duration: time.Millisecond}, func(w *World) { eff = w.Options() })
		if eff.NetSim != netsim.DefaultConfig() {
			t.Fatalf("effective NetSim %+v, want DefaultConfig()", eff.NetSim)
		}
	})
	t.Run("event mode", func(t *testing.T) {
		var eff Options
		Run(t, Options{Seeds: 1, Duration: time.Millisecond, NetSim: netsim.Config{MSS: 32}}, func(w *World) { eff = w.Options() })
		if eff.NetSim.MSS != 32 {
			t.Fatal("event mode resolved or validated NetSim")
		}
	})
	t.Run("hash", func(t *testing.T) {
		s := string(hashedOptionsJSON(Options{Mode: ModeGoroutine, Procs: 1, Drain: DefaultDrain, NetSim: netsim.DefaultConfig()}, ""))
		if !strings.Contains(s, `"netsim":{`) {
			t.Fatalf("goroutine-mode hash input lacks netsim: %s", s)
		}
	})
	t.Run("report options", func(t *testing.T) {
		o := Options{Mode: ModeGoroutine, Procs: 1, Drain: DefaultDrain, NetSim: netsim.Config{MSS: 1000}}
		if got, want := string(runOptions(o, &environment{}).NetSim), string(mustJSON(o.NetSim)); got != want {
			t.Fatalf("goroutine-mode report options netsim %s, want %s", got, want)
		}
		o.Mode = ModeEvent
		if ro := runOptions(o, &environment{}); ro.NetSim != nil {
			t.Fatalf("event-mode report options have netsim %s", ro.NetSim)
		}
		if s := string(hashedOptionsJSON(o, "")); strings.Contains(s, "netsim") {
			t.Fatalf("event-mode hash input has netsim: %s", s)
		}
	})
}

func TestATGOR11_UDPReaderRaceFree(t *testing.T) {
	checks := 0
	gw(t, Options{Duration: time.Second}, func(w *World) {
		checks = 0
		count := 0
		var srvID, cliID kernel.NodeID
		srv := w.AddProcess("srv", func(ctx context.Context, p *Process) error {
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			buf := make([]byte, 10)
			for {
				if _, _, err := pc.ReadFrom(buf); err != nil {
					return nil
				}
				count++
			}
		})
		cli := w.AddProcess("cli", func(ctx context.Context, p *Process) error {
			c, err := p.Net.Dial("udp", "srv:9")
			if err != nil {
				return err
			}
			tk := time.NewTicker(10 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-tk.C:
					_, _ = c.Write([]byte("x"))
				}
			}
		})
		srvID, cliID = srv.ID(), cli.ID()
		w.Invariant("reader caught up", func() error {
			checks++
			if d := w.Net.LinkStats(cliID, srvID).Delivered; uint64(count) != d { //nolint:gosec // G115: count is never negative
				return fmt.Errorf("count %d, delivered %d", count, d)
			}
			return nil
		})
	})
	if checks < 50 {
		t.Fatalf("invariant ran %d times", checks)
	}
}

func TestATGOR17_EventNodeUDPInterop(t *testing.T) {
	var mu sync.Mutex
	var fromP1 []*netsim.Datagram
	var read atomic.Value
	gw(t, Options{Duration: time.Second}, func(w *World) {
		mu.Lock()
		fromP1 = nil // the body runs again under FAULTLINE_CHECK_DETERMINISM
		mu.Unlock()
		w.AddServer("e1", func(n *kernel.Node) {
			w.Net.Handle(n, func(from kernel.NodeID, payload any) {
				if d, ok := payload.(*netsim.Datagram); ok {
					mu.Lock()
					fromP1 = append(fromP1, d)
					mu.Unlock()
				}
			})
			n.After(10*time.Millisecond, "send", func() {
				w.Net.Send(n, 2, &netsim.Datagram{
					Src:  netip.AddrPortFrom(netsim.NodeAddr(1), 7),
					Dst:  netip.AddrPortFrom(netsim.NodeAddr(2), 9),
					Data: []byte("hi"),
				})
			})
		})
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			buf := make([]byte, 10)
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return err
			}
			read.Store(string(buf[:n]) + "@" + from.String())
			if _, err := pc.WriteTo([]byte("ho"), from); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
	})
	if got, _ := read.Load().(string); got != "hi@10.0.0.1:7" {
		t.Fatalf("p1 read %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fromP1) != 1 || string(fromP1[0].Data) != "ho" || fromP1[0].Src.String() != "10.0.0.2:9" || fromP1[0].Dst.String() != "10.0.0.1:7" {
		t.Fatalf("e1 received %+v", fromP1)
	}
}

func TestATGOR21_GlobalRandDetected(t *testing.T) {
	res := runGorScenario(t, "gor-21")
	if res.code != 1 || !strings.Contains(res.out, "determinism failure: two runs of seed") || !strings.Contains(res.out, gorLevel3Line) {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}
