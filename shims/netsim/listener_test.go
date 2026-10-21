// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/shims/netsim"
)

func TestATNSM21_ListenErrors(t *testing.T) {
	var errs result[[3]error]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", wait)
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			var e [3]error
			if _, err := p.Net.Listen("tcp", ":80"); err != nil {
				return err
			}
			_, e[0] = p.Net.Listen("tcp", ":80")
			_, e[1] = p.Net.Listen("tcp", "a:81")
			_, e[2] = p.Net.Listen("tcp6", ":1")
			errs.put(e)
			<-ctx.Done()
			return nil
		})
	})
	e := errs.get(t)
	var une net.UnknownNetworkError
	if !errors.Is(e[0], syscall.EADDRINUSE) || !errors.Is(e[1], syscall.EADDRNOTAVAIL) || !errors.As(e[2], &une) {
		t.Fatalf("errors %v", e)
	}
}

// Listen's reported addresses, ephemeral ports, port spaces, error details, net.listen requests
// and net.conn.listen records.
func TestListenAddrsAndRecords(t *testing.T) {
	type out struct {
		addrs                                      []string
		ipLens                                     []int
		inUse, badPrt, foreign, foreignIP, unknown error
	}
	var got result[out]
	var rec records
	world(t, netsim.Config{Backlog: 7}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", wait)
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			var o out
			if _, err := p.Net.ListenPacket("udp", ":85"); err != nil {
				return err
			}
			for _, addr := range []string{":80", "b:81", "localhost:82", "127.0.0.9:83", "10.0.0.2:84", "[::]:85",
				"0.0.0.0:0", ":0", ":49154", ":0"} {
				l, err := p.Net.Listen("tcp4", addr)
				if err != nil {
					return err
				}
				o.addrs = append(o.addrs, l.Addr().String())
				ta, ok := l.Addr().(*net.TCPAddr)
				if !ok {
					return fmt.Errorf("Addr returned %T", l.Addr())
				}
				o.ipLens = append(o.ipLens, len(ta.IP))
			}
			_, o.inUse = p.Net.Listen("tcp", "localhost:80")
			_, o.badPrt = p.Net.Listen("tcp", ":65536")
			_, o.foreign = p.Net.Listen("tcp", "a:81")
			_, o.foreignIP = p.Net.Listen("tcp", "192.0.2.1:86")
			_, o.unknown = p.Net.Listen("tcp", "nosuch:87")
			got.put(o)
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	o := got.get(t)
	want := []string{"10.0.0.2:80", "10.0.0.2:81", "127.0.0.1:82", "127.0.0.1:83", "10.0.0.2:84", "10.0.0.2:85",
		"10.0.0.2:49152", "10.0.0.2:49153", "10.0.0.2:49154", "10.0.0.2:49155"}
	if !slices.Equal(o.addrs, want) || slices.ContainsFunc(o.ipLens, func(n int) bool { return n != 4 }) {
		t.Errorf("addresses %v (IP lengths %v), want %v with 4-byte IPs", o.addrs, o.ipLens, want)
	}
	// bindErr checks a bind error: Op listen, Net tcp, Addr as given ("" for none), Syscall bind.
	bindErr := func(err error, addr string, errno syscall.Errno) bool {
		var oe *net.OpError
		var se *os.SyscallError
		gotAddr := ""
		if errors.As(err, &oe) && oe.Addr != nil {
			gotAddr = oe.Addr.String()
		}
		return oe != nil && oe.Op == "listen" && oe.Net == "tcp" && gotAddr == addr &&
			errors.As(err, &se) && se.Syscall == "bind" && errors.Is(err, errno)
	}
	if !bindErr(o.inUse, "127.0.0.1:80", syscall.EADDRINUSE) {
		t.Errorf("port in use: %v", o.inUse)
	}
	if !bindErr(o.foreign, "10.0.0.1:81", syscall.EADDRNOTAVAIL) || !bindErr(o.foreignIP, "192.0.2.1:86", syscall.EADDRNOTAVAIL) ||
		!bindErr(o.unknown, "", syscall.EADDRNOTAVAIL) {
		t.Errorf("foreign bind addresses: %v; %v; %v", o.foreign, o.foreignIP, o.unknown)
	}
	var ae *net.AddrError
	if !errors.As(o.badPrt, &ae) || ae.Err != "unknown port" || ae.Addr != "tcp/65536" {
		t.Errorf("bad port: %v", o.badPrt)
	}
	ls := rec.kind("net.conn.listen")
	var ports []string
	for _, r := range ls {
		p := attr(r, "port")
		ports = append(ports, p)
		if r.Node != 2 || r.Inc != 1 || r.Text != "listen :"+p ||
			!slices.Equal(r.Attrs, []kernel.Attr{{Key: "port", Value: p}, {Key: "backlog", Value: "7"}}) {
			t.Errorf("record %+v", r)
		}
	}
	// The requests of one window run in key order, not program order, so compare sorted.
	slices.Sort(ports)
	wantPorts := []string{"49152", "49153", "49154", "49155", "80", "81", "82", "83", "84", "85"}
	if !slices.Equal(ports, wantPorts) {
		t.Errorf("listen records for ports %v, want %v", ports, wantPorts)
	}
	var keys []string
	for _, r := range rec.kind("driver.request") {
		if r.Node == 2 && attr(r, "kind") == "net.listen" {
			keys = append(keys, attr(r, "key"))
		}
	}
	slices.Sort(keys)
	var wantKeys []string
	for _, p := range wantPorts {
		wantKeys = append(wantKeys, "listen:"+p)
	}
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("net.listen request keys %v, want %v", keys, wantKeys)
	}
}

// Accept's deadline: a passed deadline fails at once, a new deadline wakes a blocked Accept, and
// the error is a timeout carrying the listener's address.
func TestListenerDeadline(t *testing.T) {
	type out struct {
		past, rearmed, timed error
		addr                 string
		rearmedAt, timedAt   time.Duration
	}
	var got result[out]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			var o out
			nl, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			l, ok := nl.(*netsim.Listener)
			if !ok {
				return fmt.Errorf("Listen returned %T", nl)
			}
			o.addr = l.Addr().String()
			if err := l.SetDeadline(time.Now().Add(-time.Second)); err != nil {
				return err
			}
			_, o.past = l.Accept()
			if err := l.SetDeadline(time.Time{}); err != nil {
				return err
			}
			p.Go(func() {
				time.Sleep(100 * time.Millisecond)
				_ = l.SetDeadline(time.Now().Add(50 * time.Millisecond))
			})
			start := time.Now()
			_, o.rearmed = l.Accept()
			o.rearmedAt = time.Since(start)
			_ = l.SetDeadline(time.Now().Add(200 * time.Millisecond))
			start = time.Now()
			_, o.timed = l.Accept()
			o.timedAt = time.Since(start)
			got.put(o)
			<-ctx.Done()
			return nil
		})
	})
	o := got.get(t)
	var oe *net.OpError
	if !errors.Is(o.past, os.ErrDeadlineExceeded) || !errors.As(o.past, &oe) || !oe.Timeout() ||
		oe.Op != "accept" || oe.Net != "tcp" || oe.Addr == nil || oe.Addr.String() != o.addr || o.addr != "10.0.0.1:80" {
		t.Errorf("passed deadline: %v (listener %s)", o.past, o.addr)
	}
	if !errors.Is(o.rearmed, os.ErrDeadlineExceeded) || o.rearmedAt != 150*time.Millisecond {
		t.Errorf("re-armed Accept returned %v after %v, want a deadline error after 150ms", o.rearmed, o.rearmedAt)
	}
	if !errors.Is(o.timed, os.ErrDeadlineExceeded) || o.timedAt != 200*time.Millisecond {
		t.Errorf("Accept returned %v after %v, want a deadline error after 200ms", o.timed, o.timedAt)
	}
}

// A blocked Accept returns net.ErrClosed when its process crashes, when it exits and when the run
// ends.
func TestAcceptWokenAtCrashExitAndRunEnd(t *testing.T) {
	var crashed, exited, ended result[timed]
	accept := func(p *faultline.Process, out *result[timed]) error {
		l, err := p.Net.Listen("tcp", ":80")
		if err != nil {
			return err
		}
		p.Go(func() {
			start := time.Now()
			_, err := l.Accept()
			out.put(timed{at: time.Since(start), err: err})
		})
		return nil
	}
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			if err := accept(p, &crashed); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			if err := accept(p, &exited); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
			return nil
		})
		w.AddProcess("c", func(ctx context.Context, p *faultline.Process) error {
			if err := accept(p, &ended); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(fault.Event{At: kernel.Time(100 * time.Millisecond), Kind: fault.KindCrash, Node: "a"}))
	})
	for _, tc := range []struct {
		name string
		got  timed
		at   time.Duration
		addr string
	}{
		{"crash", crashed.get(t), 100 * time.Millisecond, "10.0.0.1:80"},
		{"exit", exited.get(t), 50 * time.Millisecond, "10.0.0.2:80"},
		{"run end", ended.get(t), time.Second, "10.0.0.3:80"},
	} {
		var oe *net.OpError
		if !errors.Is(tc.got.err, net.ErrClosed) || !errors.As(tc.got.err, &oe) || oe.Op != "accept" ||
			oe.Addr == nil || oe.Addr.String() != tc.addr || tc.got.at != tc.at {
			t.Errorf("%s: Accept returned %v after %v, want net.ErrClosed from %s after %v", tc.name, tc.got.err, tc.got.at, tc.addr, tc.at)
		}
	}
}

// Listen is safe for concurrent use: two goroutines in each of two processes take ephemeral ports
// at the same time, and each host hands out every port once (run it with -race).
func TestConcurrentListen(t *testing.T) {
	var got [2]result[[]string]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		for i, name := range []string{"a", "b"} {
			w.AddProcess(name, func(ctx context.Context, p *faultline.Process) error {
				type batch struct {
					addrs []string
					err   error
				}
				done := make(chan batch, 2)
				for range 2 {
					p.Go(func() {
						var b batch
						for range 20 {
							l, err := p.Net.Listen("tcp", ":0")
							if err != nil {
								b.err = err
								break
							}
							b.addrs = append(b.addrs, l.Addr().String())
						}
						done <- b
					})
				}
				var addrs []string
				for range 2 {
					b := <-done
					if b.err != nil {
						return b.err
					}
					addrs = append(addrs, b.addrs...)
				}
				got[i].put(addrs)
				<-ctx.Done()
				return nil
			})
		}
	})
	for i, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		addrs := got[i].get(t)
		slices.Sort(addrs)
		var want []string
		for p := 49152; p < 49192; p++ {
			want = append(want, fmt.Sprintf("%s:%d", ip, p))
		}
		if !slices.Equal(addrs, want) {
			t.Errorf("host %s: ports %v, want %v", ip, addrs, want)
		}
	}
}
