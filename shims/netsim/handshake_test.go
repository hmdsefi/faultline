// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"errors"
	"net"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/netsim"
)

func vt(d time.Duration) kernel.Time { return kernel.Time(d) }

func TestATNSM01_Handshake(t *testing.T) {
	var dial, acc result[timed]
	var cl, cr, sl, sr result[string]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(10 * time.Millisecond)
			c, err := p.Net.Dial("tcp", "b:80")
			dial.put(timed{at: vnow(), err: err})
			if err != nil {
				return err
			}
			cl.put(c.LocalAddr().String())
			cr.put(c.RemoteAddr().String())
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			ln, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			c, err := ln.Accept()
			acc.put(timed{at: vnow(), err: err})
			if err != nil {
				return err
			}
			sl.put(c.LocalAddr().String())
			sr.put(c.RemoteAddr().String())
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if d := dial.get(t); d.err != nil || d.at != 12*time.Millisecond {
		t.Fatalf("dial at %v err %v, want 12ms", d.at, d.err)
	}
	if a := acc.get(t); a.err != nil || a.at != 12*time.Millisecond {
		t.Fatalf("accept at %v err %v, want 12ms", a.at, a.err)
	}
	if cl.get(t) != "10.0.0.1:49152" || cr.get(t) != "10.0.0.2:80" || sl.get(t) != "10.0.0.2:80" || sr.get(t) != "10.0.0.1:49152" {
		t.Fatalf("addresses %s %s / %s %s", cl.get(t), cr.get(t), sl.get(t), sr.get(t))
	}
	inOrder(t, rec.all(),
		func(r kernel.Record) bool {
			return match(r, "net.conn.dial", "conn", "c1", "src", "a:49152", "dst", "b:80")
		},
		func(r kernel.Record) bool {
			return match(r, "net.conn.segment", "kind", "syn", "outcome", "sent", "delay_ns", "1000000")
		},
		func(r kernel.Record) bool { return match(r, "net.conn.arrive", "kind", "syn", "result", "synrcvd") },
		func(r kernel.Record) bool {
			return match(r, "net.conn.arrive", "kind", "synack", "result", "established")
		},
		func(r kernel.Record) bool { return match(r, "net.conn.accept", "conn", "c1", "port", "80") },
	)
	if est := rec.kind("net.conn.established"); len(est) != 1 || est[0].Text != "established c1" || attr(est[0], "rtt_ns") != "2000000" {
		t.Fatalf("net.conn.established %+v", est)
	}
	if l := rec.kind("net.conn.listen"); len(l) != 1 || l[0].Text != "listen :80" || attr(l[0], "backlog") != "128" {
		t.Fatalf("net.conn.listen %+v", l)
	}
}

func TestATNSM02_Refused(t *testing.T) {
	var dial result[timed]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			_, err := p.Net.Dial("tcp", "b:80")
			dial.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", wait)
	})
	d := dial.get(t)
	var oe *net.OpError
	if d.at != 2*time.Millisecond || !errors.Is(d.err, syscall.ECONNREFUSED) || !errors.As(d.err, &oe) || oe.Op != "dial" {
		t.Fatalf("dial at %v: %v; want ECONNREFUSED at 2ms", d.at, d.err)
	}
}

func TestATNSM05_LostSyns(t *testing.T) {
	var dial result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 5 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			_, err := p.Net.Dial("tcp", "b:80")
			dial.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", listenAndWait(":80"))
		w.Plan(fault.Script(
			fault.Event{At: 0, Kind: fault.KindLink, Node: "a", Peer: "b",
				Link: &simnet.Link{Latency: time.Millisecond, DropPPM: simnet.MaxPPM}},
			fault.Event{At: vt(2500 * time.Millisecond), Kind: fault.KindLinkReset, Node: "a", Peer: "b"},
		))
		rec.capture(w)
	})
	var syns []string
	for _, r := range rec.kind("net.conn.segment") {
		if attr(r, "kind") == "syn" {
			syns = append(syns, r.At.String()+" "+attr(r, "outcome"))
		}
	}
	want := []string{"0.000000000s lost", "1.000000000s lost", "3.000000000s sent"}
	if len(syns) != 3 || syns[0] != want[0] || syns[1] != want[1] || syns[2] != want[2] {
		t.Fatalf("syn attempts %q, want %q", syns, want)
	}
	if d := dial.get(t); d.err != nil || d.at != 3002*time.Millisecond {
		t.Fatalf("dial at %v: %v; want success at 3.002s", d.at, d.err)
	}
}

func TestATNSM19_Backlog(t *testing.T) {
	var mu result[[]time.Duration]
	var rec records
	world(t, netsim.Config{Backlog: 2}, faultline.Options{Duration: 5 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			done := make(chan time.Duration, 3)
			for range 3 {
				p.Go(func() {
					if _, err := p.Net.Dial("tcp", "b:80"); err == nil {
						done <- vnow()
					} else {
						done <- -1
					}
				})
			}
			var at []time.Duration
			for range 3 {
				at = append(at, <-done)
			}
			sort.Slice(at, func(i, j int) bool { return at[i] < at[j] })
			mu.put(at)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			ln, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			time.Sleep(2500 * time.Millisecond)
			if _, err := ln.Accept(); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	at := mu.get(t)
	if at[0] != 2*time.Millisecond || at[1] != 2*time.Millisecond || at[2] != 3002*time.Millisecond {
		t.Fatalf("dials completed at %v, want [2ms 2ms 3.002s]", at)
	}
	full := 0
	for _, r := range rec.kind("net.conn.arrive") {
		if attr(r, "result") == "backlog-full" {
			full++
		}
	}
	if full == 0 {
		t.Fatal("no backlog-full arrival")
	}
}

func TestATNSM22_Loopback(t *testing.T) {
	var self result[timed]
	var addrs result[[2]string]
	var remote result[error]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(time.Millisecond)
			_, err := p.Net.Dial("tcp", "b:80")
			remote.put(err)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			if _, err := p.Net.Listen("tcp", "127.0.0.1:80"); err != nil {
				return err
			}
			c, err := p.Net.Dial("tcp", "localhost:80")
			self.put(timed{at: vnow(), err: err})
			if err != nil {
				return err
			}
			addrs.put([2]string{c.LocalAddr().String(), c.RemoteAddr().String()})
			<-ctx.Done()
			return nil
		})
	})
	if s := self.get(t); s.err != nil || s.at != 20*time.Microsecond {
		t.Fatalf("loopback dial at %v: %v; want 20µs", s.at, s.err)
	}
	if a := addrs.get(t); a != [2]string{"127.0.0.1:49152", "127.0.0.1:80"} {
		t.Fatalf("addresses %v", a)
	}
	if err := remote.get(t); err != nil {
		t.Fatalf("dial b:80 from a: %v (port-only matching, NSM-017)", err)
	}
}

// A dial abandoned while its syn is in flight leaves no half-open endpoint at b when the syn
// arrives, even though b's answer would be lost: with Backlog 1 the next dial still connects in
// one round trip.
func TestLateSynAfterAbandonedDial(t *testing.T) {
	var first, second result[timed]
	world(t, netsim.Config{Backlog: 1}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(10 * time.Millisecond)
			_, err := p.Net.DialTimeout("tcp", "b:80", 500*time.Microsecond)
			first.put(timed{at: vnow(), err: err})
			time.Sleep(200 * time.Millisecond)
			_, err = p.Net.Dial("tcp", "b:80")
			second.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", listenAndWait(":80"))
		// The syn leaves at 10 ms and arrives at 11 ms; every packet sent from 10.6 ms to 100 ms
		// is lost.
		w.Plan(fault.Script(
			fault.Event{At: vt(10600 * time.Microsecond), Kind: fault.KindLink, Node: "a", Peer: "b",
				Link: &simnet.Link{Latency: time.Millisecond, DropPPM: simnet.MaxPPM}},
			fault.Event{At: vt(100 * time.Millisecond), Kind: fault.KindLinkReset, Node: "a", Peer: "b"},
		))
	})
	var ne net.Error
	if f := first.get(t); f.at != 10500*time.Microsecond || !errors.As(f.err, &ne) || !ne.Timeout() {
		t.Fatalf("first dial: %v at %v", f.err, f.at)
	}
	if s := second.get(t); s.err != nil || s.at != 212500*time.Microsecond {
		t.Fatalf("second dial: %v at %v, want a connection at 212.5ms", s.err, s.at)
	}
}
