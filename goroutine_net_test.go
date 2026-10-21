// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
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

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["gor-14"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second, StallTimeout: 2 * time.Second},
			func(w *World) {
				w.AddProcess("p1", func(ctx context.Context, p *Process) error {
					c, err := p.Net.Dial("tcp", "p2:80")
					if err != nil {
						return err
					}
					var mu sync.Mutex
					locked := make(chan struct{})
					p.Go(func() { // A: holds mu across a read that never gets data
						mu.Lock()
						close(locked)
						_, _ = c.Read(make([]byte, 1))
						mu.Unlock()
					})
					<-locked
					p.Go(func() { mu.Lock(); mu.Unlock() }) //nolint:staticcheck // SA2001: B only blocks on the lock A holds
					<-ctx.Done()
					return nil
				})
				w.AddProcess("p2", func(ctx context.Context, p *Process) error {
					ln, err := p.Net.Listen("tcp", ":80")
					if err != nil {
						return err
					}
					if _, err := ln.Accept(); err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				})
			})
	}
	gorScenarios["netsim-exit-error"] = netExitScenario(func(*World) error { return errors.New("boom") }, false)
	gorScenarios["netsim-exit-panic-paused"] = netExitScenario(func(*World) error { panic("boom") }, true)
	gorScenarios["netsim-exit-fatal"] = netExitScenario(func(w *World) error { w.T().Fatal("fatal in p1"); return nil }, false)
}

// netExitScenario returns a scenario whose process p1 holds a listener with a blocked Accept, a UDP
// socket and a connection to p2, writes to the connection at 1 s and then ends main with end. With
// pause, p1 is paused from 0.5 s on.
func netExitScenario(end func(w *World) error, pause bool) func(t *testing.T) {
	return func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 3 * time.Second, CheckDeterminism: true}, func(w *World) {
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				ln, err := p.Net.Listen("tcp", ":80")
				if err != nil {
					return err
				}
				if _, err := p.Net.ListenPacket("udp", ":9"); err != nil {
					return err
				}
				c, err := p.Net.Dial("tcp", "p2:80")
				if err != nil {
					return err
				}
				p.Go(func() {
					_, err := ln.Accept()
					fmt.Printf("accept closed: %t\n", errors.Is(err, net.ErrClosed))
				})
				time.Sleep(time.Second - time.Since(epoch))
				if _, err := c.Write([]byte("x")); err != nil {
					return err
				}
				return end(w)
			})
			w.AddProcess("p2", func(ctx context.Context, p *Process) error {
				ln, err := p.Net.Listen("tcp", ":80")
				if err != nil {
					return err
				}
				if _, err := ln.Accept(); err != nil {
					return err
				}
				<-ctx.Done()
				return nil
			})
			if pause {
				w.Plan(fault.Script(fault.Event{At: vt(500 * time.Millisecond), Kind: fault.KindPause, Node: "p1"}))
			}
		})
	}
}

// A process that returns an error, panics or calls t.Fatal ends its incarnation on its own goroutine
// (GOR-049, GOR-050, GOR-047's Goexit row). Its netsim host is detached there at once, so blocked
// calls return, and the driver releases it in its next drain, so netsim's records and simnet calls
// stay on the driver (NSM-002, NSM-061); after t.Fatal the run stops before that. On a paused node
// the release waits for the resume: simnet sends nothing from a paused node.
func TestGoroutineNetsimFailureExits(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		res := runGorScenario(t, "netsim-exit-error", "FAULTLINE_SEED=0x1")
		if res.code != 1 || !strings.Contains(res.out, "accept closed: true\n") || strings.Contains(res.out, "determinism failure") ||
			!strings.Contains(res.out, "faultline: simulation failed at t=1.000000000s") || !strings.Contains(res.out, "returned an error: boom") {
			t.Fatalf("exit %d\n%s", res.code, res.out)
		}
		rs := res.artifact(t, 1).Trace.Records
		i := indexWhere(rs, func(r kernel.Record) bool { return r.Kind == "kernel.fail" })
		for _, want := range []string{"c1 c2s data seq=1 len=1 attempt=1: sent", "unlisten :80: exit",
			"c1 c2s fin seq=2 len=0 attempt=1: sent", "close c1: fin", "udp close :9: exit"} {
			j := indexWhere(rs[i+1:], func(r kernel.Record) bool { return r.Node == 1 && r.Text == want })
			if i < 0 || j < 0 || rs[i+1+j].At != vt(time.Second) {
				t.Fatalf("no %q at 1s after kernel.fail (and the records before it)\n%v", want, rs)
			}
			i += 1 + j
		}
	})
	t.Run("panic on a paused node", func(t *testing.T) {
		res := runGorScenario(t, "netsim-exit-panic-paused", "FAULTLINE_SEED=0x1")
		if res.code != 1 || !strings.Contains(res.out, "accept closed: true\n") || strings.Contains(res.out, "simnet:") ||
			!strings.Contains(res.out, "faultline: panic at t=1.000000000s") {
			t.Fatalf("exit %d\n%s", res.code, res.out)
		}
		for _, r := range res.artifact(t, 1).Trace.Records {
			if r.Node == 1 && strings.HasPrefix(r.Kind, "net.conn.") && r.At >= vt(500*time.Millisecond) {
				t.Fatalf("netsim record of the paused node: %+v", r)
			}
		}
	})
	t.Run("fatal", func(t *testing.T) {
		res := runGorScenario(t, "netsim-exit-fatal", "FAULTLINE_SEED=0x1")
		if res.code != 1 || !strings.Contains(res.out, "accept closed: true\n") || !strings.Contains(res.out, "stopped by t.FailNow or t.Fatal at t=1.000000000s") {
			t.Fatalf("exit %d\n%s", res.code, res.out)
		}
	})
}

func TestATGOR05_CrashRestartWithNetwork(t *testing.T) {
	type obs struct {
		inc         uint32
		rand        uint64
		start       kernel.Time
		accept      error
		cause, late error
		lateRan     bool
	}
	run := func(t *testing.T) []obs {
		var mu sync.Mutex
		var out []obs
		gw(t, Options{BaseSeed: 11, Duration: 5 * time.Second}, func(w *World) {
			out = nil
			w.AddProcess("p1", func(ctx context.Context, p *Process) error {
				o := obs{inc: p.Incarnation()}
				ln, err := p.Net.Listen("tcp", ":1")
				if err != nil {
					return err
				}
				accepted := make(chan error, 1)
				p.Go(func() { _, err := ln.Accept(); accepted <- err })
				o.rand = p.Rand().Uint64()
				_ = p.Do("start", func() { o.start = w.Sim.Now() })
				<-ctx.Done()
				o.cause = context.Cause(ctx)
				o.late = p.Do("late", func() { o.lateRan = true })
				o.accept = <-accepted
				mu.Lock()
				out = append(out, o)
				mu.Unlock()
				return nil
			})
			w.Plan(fault.Script(
				fault.Event{At: vt(2 * time.Second), Kind: fault.KindCrash, Node: "p1"},
				fault.Event{At: vt(3 * time.Second), Kind: fault.KindRestart, Node: "p1"},
			))
		})
		mu.Lock()
		defer mu.Unlock()
		return append([]obs(nil), out...)
	}
	var first, second []obs
	t.Run("run1", func(t *testing.T) { first = run(t) })
	t.Run("run2", func(t *testing.T) { second = run(t) })
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("observations %+v / %+v", first, second)
	}
	i1, i2 := first[0], first[1]
	if !errors.Is(i1.accept, net.ErrClosed) || !errors.Is(i1.cause, ErrCrashed) || !errors.Is(i1.late, ErrCrashed) || i1.lateRan {
		t.Fatalf("incarnation 1: %+v", i1)
	}
	if i2.inc != 2 || i2.start != vt(3*time.Second) {
		t.Fatalf("incarnation 2: %+v", i2)
	}
	if i1.rand == i2.rand || i1.rand != second[0].rand || i2.rand != second[1].rand {
		t.Fatalf("rand %d %d / %d %d", i1.rand, i2.rand, second[0].rand, second[1].rand)
	}
}

func TestATGOR06_ExitedListenerRefuses(t *testing.T) {
	sink := newRecordSink()
	var state kernel.NodeState
	var dialErr atomic.Value
	gw(t, fullTrace(Options{Duration: 3 * time.Second}), func(w *World) {
		n1 := w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			if _, err := p.Net.Listen("tcp", ":80"); err != nil {
				return err
			}
			time.Sleep(time.Second)
			return nil
		})
		w.AddProcess("p2", func(ctx context.Context, p *Process) error {
			time.Sleep(2 * time.Second)
			_, err := p.Net.Dial("tcp", "p1:80")
			dialErr.Store(errBox{err})
			<-ctx.Done()
			return nil
		})
		sink.capture(w)
		w.Final("state", func() error { state = n1.State(); return nil })
	})
	exits := ofKind(sink.only(t), "driver.process.exit")
	if len(exits) != 1 || attr(exits[0], "status") != "ok" || exits[0].At != vt(time.Second) {
		t.Fatalf("driver.process.exit %+v", exits)
	}
	box, _ := dialErr.Load().(errBox)
	if state != kernel.NodeUp || !errors.Is(box.err, syscall.ECONNREFUSED) {
		t.Fatalf("state %v, dial %v", state, box.err)
	}
}

func TestATGOR14_StallWithNetsimRead(t *testing.T) {
	start := time.Now()
	res := runGorScenario(t, "gor-14", "FAULTLINE_SEED=0x1")
	if time.Since(start) > 30*time.Second || res.code == 0 {
		t.Fatalf("exit %d after %v\n%s", res.code, time.Since(start), res.out)
	}
	// "[sync.Mutex.Lock, synctest bubble " is B's goroutine header; the report's own hint line
	// also contains "[sync.Mutex.Lock".
	for _, want := range []string{"goroutine mode stalled", "[sync.Mutex.Lock, synctest bubble ", "replay:   FAULTLINE_SEED=0x0000000000000001"} {
		if !strings.Contains(res.out, want) {
			t.Fatalf("output lacks %q\n%s", want, res.out)
		}
	}
	dir := artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1)
	if _, err := os.Stat(filepath.Join(dir, "stall.txt")); err != nil {
		t.Fatalf("stall.txt: %v", err)
	}
}

func TestATGOR18_PausedWriterDeferred(t *testing.T) {
	type arrival struct {
		at   time.Duration
		data string
	}
	var mu sync.Mutex
	var got []arrival
	var sent strings.Builder
	gw(t, Options{Duration: 3 * time.Second}, func(w *World) {
		got = nil
		sent.Reset()
		w.AddProcess("p1", func(ctx context.Context, p *Process) error {
			c, err := p.Net.Dial("tcp", "p2:80")
			if err != nil {
				return err
			}
			tk := time.NewTicker(100 * time.Millisecond)
			defer tk.Stop()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return nil
				case <-tk.C:
					msg := fmt.Sprintf("msg-%06d", i)
					mu.Lock()
					sent.WriteString(msg)
					mu.Unlock()
					if _, err := c.Write([]byte(msg)); err != nil {
						return nil
					}
				}
			}
		})
		w.AddProcess("p2", func(ctx context.Context, p *Process) error {
			ln, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			c, err := ln.Accept()
			if err != nil {
				return err
			}
			b := make([]byte, 4096)
			for {
				n, err := c.Read(b)
				if err != nil {
					return nil
				}
				mu.Lock()
				got = append(got, arrival{at: time.Since(epoch), data: string(b[:n])})
				mu.Unlock()
			}
		})
		w.Plan(fault.Script(
			fault.Event{At: vt(time.Second), Kind: fault.KindPause, Node: "p1"},
			fault.Event{At: vt(2 * time.Second), Kind: fault.KindResume, Node: "p1"},
		))
	})
	mu.Lock()
	defer mu.Unlock()
	var all strings.Builder
	sawResume := false
	for _, a := range got {
		if a.at > 1001*time.Millisecond && a.at < 2*time.Second {
			t.Fatalf("bytes arrived at %v, inside the pause", a.at)
		}
		if a.at == 2001*time.Millisecond {
			sawResume = true
		}
		all.WriteString(a.data)
	}
	if !sawResume || !strings.HasPrefix(sent.String(), all.String()) || len(all.String()) < len(sent.String())-10 {
		t.Fatalf("arrivals %+v; sent %q", got, sent.String())
	}
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["gor-30"] = func(t *testing.T) {
		servers := []string{"s1", "s2", "s3", "s4", "s5"}
		Run(t, Options{Mode: ModeGoroutine, Seeds: 50, Duration: 30 * time.Second, CheckDeterminism: true, KeepGoing: true},
			func(w *World) {
				var echoes atomic.Int64
				w.Final("echoes completed", func() error {
					if echoes.Load() == 0 {
						return errors.New("no echo completed")
					}
					return nil
				})
				for _, name := range servers {
					w.AddProcess(name, func(ctx context.Context, p *Process) error {
						ln, err := p.Net.Listen("tcp", ":7")
						if err != nil {
							return err
						}
						p.Go(func() {
							for {
								c, err := ln.Accept()
								if err != nil {
									return
								}
								p.Go(func() { _, _ = io.Copy(c, c); c.Close() })
							}
						})
						<-ctx.Done()
						return nil
					})
				}
				for _, name := range []string{"c1", "c2", "c3"} {
					w.AddClientProcess(name, func(ctx context.Context, p *Process) error {
						r := p.Rand()
						conns := map[string]net.Conn{}
						buf := make([]byte, 1024)
						tk := time.NewTicker(100 * time.Millisecond)
						defer tk.Stop()
						for {
							select {
							case <-ctx.Done():
								return nil
							case <-tk.C:
							}
							srv, size := servers[r.IntN(len(servers))], 1+r.IntN(1024)
							c := conns[srv]
							if c == nil {
								var err error
								if c, err = (&netsim.Dialer{Host: p.Net, Timeout: time.Second}).Dial("tcp", srv+":7"); err != nil {
									continue
								}
								conns[srv] = c
							}
							_ = c.SetDeadline(time.Now().Add(time.Second))
							if _, err := c.Write(buf[:size]); err != nil {
								c.Close()
								delete(conns, srv)
								continue
							}
							if _, err := io.ReadFull(c, buf[:size]); err != nil {
								c.Close()
								delete(conns, srv)
								continue
							}
							echoes.Add(1)
						}
					})
				}
				w.Plan(&fault.Random{Rules: []fault.Rule{{Kind: fault.KindPartition, Every: 5 * time.Second}}})
			})
	}
}

func TestATGOR30_EchoReplayRate(t *testing.T) {
	if testing.Short() {
		t.Skip("soak")
	}
	res := runGorScenario(t, "gor-30", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")
	lines := res.results(t)
	if len(lines) != 50 {
		t.Fatalf("%d result lines, want 50\n%s", len(lines), res.out)
	}
	det, other := 0, 0
	for _, l := range lines {
		switch {
		case l["status"] == "pass":
		case l["kind"] == "determinism":
			det++
		default:
			other++
		}
	}
	t.Logf("AT-GOR-30 replay rate: %d/50 seeds identical", 50-det)
	if other != 0 || det > 1 {
		t.Fatalf("%d determinism failures (want <= 1) and %d other failures\n%s", det, other, res.out)
	}
}
