// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/shims/netsim"
)

// listenAndWait is b's main in several tests: listen on address, accept nothing, wait.
func listenAndWait(address string) faultline.ProcessFunc { //nolint:unparam // every test so far listens on :80
	return func(ctx context.Context, p *faultline.Process) error {
		if _, err := p.Net.Listen("tcp", address); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
}

func TestATNSM03_EventNodeRefuses(t *testing.T) {
	var dial result[timed]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			_, err := p.Net.Dial("tcp", "b:80")
			dial.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.AddServer("b", func(*kernel.Node) {})
	})
	if d := dial.get(t); d.at != 0 || !errors.Is(d.err, syscall.ECONNREFUSED) {
		t.Fatalf("dial at %v: %v; want ECONNREFUSED at 0", d.at, d.err)
	}
}

func TestATNSM04_AddressErrors(t *testing.T) {
	var errs result[[3]error]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			var e [3]error
			_, e[0] = p.Net.Dial("tcp", "zz:80")
			_, e[1] = p.Net.Dial("tcp", "10.0.0.99:80")
			_, e[2] = p.Net.Dial("tcp", "b:http")
			errs.put(e)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", wait)
	})
	e := errs.get(t)
	var dns *net.DNSError
	var ae *net.AddrError
	if !errors.As(e[0], &dns) || !dns.IsNotFound {
		t.Fatalf("zz:80: %v", e[0])
	}
	if !errors.Is(e[1], syscall.EHOSTUNREACH) {
		t.Fatalf("10.0.0.99:80: %v", e[1])
	}
	if !errors.As(e[2], &ae) || ae.Err != "unknown port" {
		t.Fatalf("b:http: %v", e[2])
	}
}

func TestATNSM06_DialTimesOut(t *testing.T) {
	var dial result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 40 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			_, err := p.Net.Dial("tcp", "b:80")
			dial.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", listenAndWait(":80"))
		w.Plan(fault.Script(fault.Event{At: 0, Kind: fault.KindIsolate, Node: "b"}))
		rec.capture(w)
	})
	var at []time.Duration
	for _, r := range rec.kind("net.conn.segment") {
		if attr(r, "kind") == "syn" && attr(r, "outcome") == "held" {
			at = append(at, time.Duration(r.At))
		}
	}
	want := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second, 25 * time.Second}
	if len(at) != len(want) {
		t.Fatalf("held syn attempts at %v, want %v", at, want)
	}
	for i := range want {
		if at[i] != want[i] {
			t.Fatalf("held syn attempts at %v, want %v", at, want)
		}
	}
	if d := dial.get(t); d.at != 35*time.Second || !errors.Is(d.err, syscall.ETIMEDOUT) {
		t.Fatalf("dial at %v: %v; want ETIMEDOUT at 35s", d.at, d.err)
	}
	if to := rec.kind("net.conn.timeout"); len(to) != 1 || attr(to[0], "waited_ns") != "35000000000" || to[0].Text != "timeout c1 c2s seq=0" {
		t.Fatalf("net.conn.timeout %+v", to)
	}
}

// A process node is never refused as an event-style node, even when its boot runs after a t=0
// dial request: the seeded tie-break orders the two events, and 16 seeds give both orders.
func TestDialProcessBeforeItBoots(t *testing.T) {
	world(t, netsim.Config{}, faultline.Options{Seeds: 16, Duration: 2 * time.Second}, func(w *faultline.World) {
		var dial result[timed]
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			c, cancel := context.WithTimeout(ctx, time.Second)
			_, err := p.Net.DialContext(c, "tcp", "b:80")
			cancel()
			dial.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", listenAndWait(":80"))
		w.Plan(fault.Script(fault.Event{At: 0, Kind: fault.KindIsolate, Node: "b"}))
		w.Final("the dial waits for b", func() error {
			for _, r := range w.Sim.Records() {
				if r.Kind == "net.conn.refused" {
					return fmt.Errorf("%s at %v", r.Text, time.Duration(r.At))
				}
			}
			dial.mu.Lock()
			d, ok := dial.v, dial.set
			dial.mu.Unlock()
			if !ok || d.at != time.Second || !errors.Is(d.err, context.DeadlineExceeded) {
				return fmt.Errorf("dial at %v: %v; want the context deadline at 1s", d.at, d.err)
			}
			return nil
		})
	})
}

// NSM-046: context and Dialer.Timeout errors.
func TestDialContextErrors(t *testing.T) {
	var deadline, cancelled, dialer, pre, negative result[timed]
	world(t, netsim.Config{}, faultline.Options{Duration: 2 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			c1, cancel1 := context.WithTimeout(ctx, 100*time.Millisecond)
			_, err := p.Net.DialContext(c1, "tcp", "b:80")
			cancel1()
			deadline.put(timed{at: vnow(), err: err})
			c2, cancel2 := context.WithCancel(ctx)
			p.Go(func() { time.Sleep(50 * time.Millisecond); cancel2() })
			_, err = p.Net.DialContext(c2, "tcp", "b:80")
			cancelled.put(timed{at: vnow(), err: err})
			_, err = (&netsim.Dialer{Host: p.Net, Timeout: 200 * time.Millisecond}).Dial("tcp", "b:80")
			dialer.put(timed{at: vnow(), err: err})
			done, cancel3 := context.WithCancel(ctx)
			cancel3()
			_, err = p.Net.DialContext(done, "tcp", "b:80")
			pre.put(timed{at: vnow(), err: err})
			_, err = (&netsim.Dialer{Host: p.Net, Timeout: -time.Second}).DialContext(ctx, "tcp", "b:80")
			negative.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", listenAndWait(":80"))
		w.Plan(fault.Script(fault.Event{At: 0, Kind: fault.KindIsolate, Node: "b"}))
	})
	d := deadline.get(t)
	var ne net.Error
	if d.at != 100*time.Millisecond || !errors.Is(d.err, context.DeadlineExceeded) || !errors.Is(d.err, os.ErrDeadlineExceeded) ||
		!errors.As(d.err, &ne) || !ne.Timeout() || !strings.Contains(d.err.Error(), "i/o timeout") {
		t.Fatalf("deadline: %v at %v", d.err, d.at)
	}
	c := cancelled.get(t)
	if c.at != 150*time.Millisecond || !errors.Is(c.err, context.Canceled) || !strings.Contains(c.err.Error(), "operation was canceled") {
		t.Fatalf("cancel: %v at %v", c.err, c.at)
	}
	if x := dialer.get(t); x.at != 350*time.Millisecond || !errors.Is(x.err, context.DeadlineExceeded) {
		t.Fatalf("Dialer.Timeout: %v at %v", x.err, x.at)
	}
	if x := pre.get(t); x.at != 350*time.Millisecond || !errors.Is(x.err, context.Canceled) {
		t.Fatalf("cancelled before the dial: %v at %v", x.err, x.at)
	}
	if x := negative.get(t); x.at != 350*time.Millisecond || !errors.Is(x.err, os.ErrDeadlineExceeded) || !errors.As(x.err, &ne) || !ne.Timeout() {
		t.Fatalf("negative Dialer.Timeout: %v at %v; want a timeout at once, as package net gives", x.err, x.at)
	}
}

// DialTimeout, Dialer.DialContext, tcp4, a loopback dial, a refused loopback dial and the remaining
// address errors; the dial and segment records name endpoints as NSM §8 renders them.
func TestDialVariantsAndRecords(t *testing.T) {
	var res result[[7]timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			var e [7]timed
			_, err := p.Net.DialTimeout("tcp4", "b:80", 10*time.Millisecond)
			e[0] = timed{at: vnow(), err: err}
			c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			_, err = (&netsim.Dialer{Host: p.Net}).DialContext(c, "tcp", "10.0.0.2:80")
			cancel()
			e[1] = timed{at: vnow(), err: err}
			if _, err := p.Net.Listen("tcp", ":81"); err != nil {
				return err
			}
			lc, err := p.Net.DialTimeout("tcp", "localhost:81", 5*time.Millisecond)
			e[2] = timed{at: vnow(), err: err}
			if err == nil {
				e[2].s = lc.LocalAddr().String() + " " + lc.RemoteAddr().String()
			}
			_, err = p.Net.Dial("tcp", "b:0")
			e[3] = timed{err: err}
			_, err = p.Net.Dial("tcp6", "b:80")
			e[4] = timed{err: err}
			_, err = p.Net.Dial("tcp", "192.168.1.1:80")
			e[5] = timed{err: err}
			_, err = p.Net.Dial("tcp", "localhost:82")
			e[6] = timed{at: vnow(), err: err}
			res.put(e)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", listenAndWait(":80"))
		w.Plan(fault.Script(fault.Event{At: 0, Kind: fault.KindIsolate, Node: "b"}))
		rec.capture(w)
	})
	e := res.get(t)
	var oe *net.OpError
	var ne net.Error
	op := func(err error, network, addr string) bool {
		return errors.As(err, &oe) && oe.Op == "dial" && oe.Net == network && (addr == "" && oe.Addr == nil || oe.Addr != nil && oe.Addr.String() == addr)
	}
	if e[0].at != 10*time.Millisecond || !errors.As(e[0].err, &ne) || !ne.Timeout() || !op(e[0].err, "tcp4", "10.0.0.2:80") {
		t.Fatalf("DialTimeout: %v at %v", e[0].err, e[0].at)
	}
	if e[1].at != 30*time.Millisecond || !errors.Is(e[1].err, context.DeadlineExceeded) || !op(e[1].err, "tcp", "10.0.0.2:80") {
		t.Fatalf("Dialer.DialContext: %v at %v", e[1].err, e[1].at)
	}
	if e[2].at != 30*time.Millisecond+20*time.Microsecond || e[2].err != nil || e[2].s != "127.0.0.1:49154 127.0.0.1:81" {
		t.Fatalf("loopback DialTimeout: %v at %v, addresses %q", e[2].err, e[2].at, e[2].s)
	}
	var ae *net.AddrError
	if !errors.As(e[3].err, &ae) || ae.Err != "invalid port" || ae.Addr != "b:0" || !op(e[3].err, "tcp", "10.0.0.2:0") {
		t.Fatalf("port 0: %v", e[3].err)
	}
	var une net.UnknownNetworkError
	if !errors.As(e[4].err, &une) || string(une) != "tcp6" || !op(e[4].err, "tcp6", "") {
		t.Fatalf("tcp6: %v", e[4].err)
	}
	var se *os.SyscallError
	if !errors.As(e[5].err, &se) || se.Syscall != "connect" || !errors.Is(e[5].err, syscall.EHOSTUNREACH) || !op(e[5].err, "tcp", "192.168.1.1:80") {
		t.Fatalf("192.168.1.1:80: %v", e[5].err)
	}
	if e[6].at != 30*time.Millisecond+40*time.Microsecond || !errors.Is(e[6].err, syscall.ECONNREFUSED) || !op(e[6].err, "tcp", "127.0.0.1:82") {
		t.Fatalf("refused loopback dial: %v at %v", e[6].err, e[6].at)
	}
	var dials []string
	for _, r := range rec.kind("net.conn.dial") {
		dials = append(dials, r.Text)
		if r.Node != 1 || attr(r, "conn")+" "+attr(r, "src")+" -> "+attr(r, "dst") != strings.TrimPrefix(r.Text, "dial ") {
			t.Fatalf("net.conn.dial %+v", r)
		}
	}
	if want := []string{"dial c1 a:49152 -> b:80", "dial c2 a:49153 -> b:80", "dial c3 127.0.0.1:49154 -> 127.0.0.1:81", "dial c4 127.0.0.1:49155 -> 127.0.0.1:82"}; strings.Join(dials, ";") != strings.Join(want, ";") {
		t.Fatalf("dials %q, want %q", dials, want)
	}
	for _, r := range rec.kind("net.conn.segment") {
		if (attr(r, "conn") == "c3" || attr(r, "conn") == "c4") && (attr(r, "outcome") != "loopback" || attr(r, "delay_ns") != "10000" || r.Node != 1) {
			t.Fatalf("loopback syn %+v", r)
		}
	}
}
