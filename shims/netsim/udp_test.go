// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/netsim"
)

func TestATNSM23_UDPDialAndReply(t *testing.T) {
	var srcs result[[]string]
	var replies result[int]
	var big result[error]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			var seen []string
			buf := make([]byte, 100)
			for range 3 {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return err
				}
				seen = append(seen, from.String())
				if _, err := pc.WriteTo(buf[:n], from); err != nil {
					return err
				}
			}
			srcs.put(seen)
			_, err = pc.WriteTo(make([]byte, 65508), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 49152})
			big.put(err)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			c, err := p.Net.Dial("udp", "a:9")
			if err != nil {
				return err
			}
			got := 0
			buf := make([]byte, 100)
			for i := range 3 {
				if _, err := c.Write([]byte{byte(i)}); err != nil {
					return err
				}
				if _, err := c.Read(buf); err != nil {
					return err
				}
				got++
			}
			replies.put(got)
			<-ctx.Done()
			return nil
		})
	})
	for _, s := range srcs.get(t) {
		if s != "10.0.0.2:49152" {
			t.Fatalf("a saw source %s, want 10.0.0.2:49152", s)
		}
	}
	if replies.get(t) != 3 {
		t.Fatal("b did not read 3 replies")
	}
	if err := big.get(t); !errors.Is(err, syscall.EMSGSIZE) {
		t.Fatalf("WriteTo of 65508 bytes = %v, want EMSGSIZE", err)
	}
}

func TestATNSM24_UDPLoss(t *testing.T) {
	var wrote result[timed]
	var read result[error]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 2 * time.Second,
		Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond, DropPPM: simnet.MaxPPM}}},
		func(w *faultline.World) {
			w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
				pc, err := p.Net.ListenPacket("udp", ":9")
				if err != nil {
					return err
				}
				_ = pc.SetReadDeadline(time.Now().Add(time.Second))
				_, _, err = pc.ReadFrom(make([]byte, 10))
				read.put(err)
				<-ctx.Done()
				return nil
			})
			w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
				pc, err := p.Net.ListenPacket("udp", ":0")
				if err != nil {
					return err
				}
				n, err := pc.WriteTo([]byte("hello"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 9})
				wrote.put(timed{n: n, err: err})
				<-ctx.Done()
				return nil
			})
			rec.capture(w)
		})
	if w := wrote.get(t); w.n != 5 || w.err != nil {
		t.Fatalf("WriteTo = %d, %v; want 5, nil", w.n, w.err)
	}
	if err := read.get(t); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a read %v, want a deadline error (nothing arrived)", err)
	}
	found := false
	for _, r := range rec.kind("net.drop") {
		found = found || attr(r, "reason") == "loss"
	}
	if !found {
		t.Fatal("no net.drop reason=loss record")
	}
}

func TestATNSM25_UDPOverflow(t *testing.T) {
	var rec records
	world(t, netsim.Config{UDPRecvBuffer: 65507}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			if _, err := p.Net.ListenPacket("udp", ":9"); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			c, err := p.Net.Dial("udp", "a:9")
			if err != nil {
				return err
			}
			for range 2 {
				if _, err := c.Write(make([]byte, 40_000)); err != nil {
					return err
				}
			}
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if n := len(rec.kind("net.udp.recv")); n != 1 {
		t.Fatalf("%d datagrams received, want 1", n)
	}
	drops := rec.kind("net.udp.drop")
	if len(drops) != 1 || attr(drops[0], "reason") != "overflow" || drops[0].Text != "udp drop b:49152 -> a:9: overflow" ||
		attr(drops[0], "len") != "40000" {
		t.Fatalf("net.udp.drop %+v", drops)
	}
}

func TestATNSM34_EventNodeDatagrams(t *testing.T) {
	var rec records
	var got result[string]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddServer("e", func(n *kernel.Node) {
			send := func() {
				w.Net.Send(n, 2, &netsim.Datagram{
					Src:  netip.AddrPortFrom(netsim.NodeAddr(1), 7),
					Dst:  netip.AddrPortFrom(netsim.NodeAddr(2), 9),
					Data: []byte("hi"),
				})
			}
			n.After(10*time.Millisecond, "first", send)
			n.After(100*time.Millisecond, "second", send)
		})
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(50 * time.Millisecond)
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			buf := make([]byte, 10)
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return err
			}
			got.put(string(buf[:n]))
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	drops := rec.kind("net.udp.drop")
	if len(drops) != 1 || attr(drops[0], "reason") != "no-socket" || drops[0].Text != "udp drop e:7 -> a:9: no-socket" {
		t.Fatalf("net.udp.drop %+v", drops)
	}
	if got.get(t) != "hi" || len(rec.kind("net.udp.recv")) != 1 {
		t.Fatal("second datagram not delivered")
	}
}

func TestUDPMisuseAndRecords(t *testing.T) {
	type errs struct{ inUse, notAvail, network, unconnected, closed, closedTwice error }
	var got result[errs]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			var e errs
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			_, e.inUse = p.Net.ListenPacket("udp", ":9")
			_, e.notAvail = p.Net.ListenPacket("udp", "b:9")
			_, e.network = p.Net.ListenPacket("udp6", ":9")
			u, ok := pc.(*netsim.UDPConn)
			if !ok {
				return fmt.Errorf("ListenPacket returned %T", pc)
			}
			_, e.unconnected = u.Write([]byte("x"))
			e.closed = pc.Close()
			e.closedTwice = pc.Close()
			got.put(e)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", wait)
		rec.capture(w)
	})
	e := got.get(t)
	var une net.UnknownNetworkError
	if !errors.Is(e.inUse, syscall.EADDRINUSE) || !errors.Is(e.notAvail, syscall.EADDRNOTAVAIL) ||
		!errors.As(e.network, &une) || !errors.Is(e.unconnected, syscall.EDESTADDRREQ) ||
		e.closed != nil || !errors.Is(e.closedTwice, net.ErrClosed) {
		t.Fatalf("errors %+v", e)
	}
	binds, closes := rec.kind("net.udp.bind"), rec.kind("net.udp.close")
	if len(binds) != 1 || binds[0].Text != "udp bind :9" || attr(binds[0], "port") != "9" ||
		len(closes) != 1 || closes[0].Text != "udp close :9: close" || attr(closes[0], "reason") != "close" {
		t.Fatalf("bind %+v close %+v", binds, closes)
	}
}

// A connected socket filters other sources, an unknown node address has no route, and loopback
// datagrams go through send_raw with the Loopback delay. Ephemeral ports skip a bound port.
func TestUDPFilteredNoRouteAndLoopback(t *testing.T) {
	var aGot, bGot result[[]string]
	var bRead result[error]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			pc9, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			pc10, err := p.Net.ListenPacket("udp", "a:10")
			if err != nil {
				return err
			}
			time.Sleep(10 * time.Millisecond)
			for _, to := range []string{"10.0.0.2:49152", "10.0.0.99:9", "127.0.0.1:9"} {
				if _, err := pc10.WriteTo([]byte("x"), net.UDPAddrFromAddrPort(netip.MustParseAddrPort(to))); err != nil {
					return err
				}
			}
			_, from, err := pc9.ReadFrom(make([]byte, 10))
			if err != nil {
				return err
			}
			lo, err := p.Net.ListenPacket("udp", "localhost:11")
			if err != nil {
				return err
			}
			lc, err := p.Net.Dial("udp", "localhost:9")
			if err != nil {
				return err
			}
			if _, err := lc.Write([]byte("y")); err != nil {
				return err
			}
			_, from2, err := pc9.ReadFrom(make([]byte, 10))
			if err != nil {
				return err
			}
			aGot.put([]string{from.String(), lo.LocalAddr().String(), pc10.LocalAddr().String(),
				lc.LocalAddr().String(), lc.RemoteAddr().String(), from2.String()})
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			pre, err := p.Net.ListenPacket("udp", ":49153")
			if err != nil {
				return err
			}
			c, err := p.Net.Dial("udp", "a:9")
			if err != nil {
				return err
			}
			e, err := p.Net.ListenPacket("udp", ":0")
			if err != nil {
				return err
			}
			u, ok := pre.(*netsim.UDPConn)
			if !ok {
				return fmt.Errorf("ListenPacket returned %T", pre)
			}
			bGot.put([]string{c.LocalAddr().String(), c.RemoteAddr().String(), e.LocalAddr().String(), fmt.Sprint(u.RemoteAddr())})
			_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			_, err = c.Read(make([]byte, 10))
			bRead.put(err)
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if got, want := aGot.get(t), []string{"10.0.0.1:10", "127.0.0.1:11", "10.0.0.1:10", "127.0.0.1:49152", "127.0.0.1:9",
		"127.0.0.1:49152"}; !slices.Equal(got, want) {
		t.Errorf("a: loopback source, loopback bind, named bind, loopback dial local, remote, source = %v, want %v", got, want)
	}
	if got, want := bGot.get(t), []string{"10.0.0.2:49152", "10.0.0.1:9", "10.0.0.2:49154", "<nil>"}; !slices.Equal(got, want) {
		t.Errorf("b: dial local, dial remote, ephemeral bind, listen remote = %v, want %v", got, want)
	}
	if err := bRead.get(t); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("b read %v, want a deadline error (the datagram from a:10 is filtered)", err)
	}
	var texts []string
	for _, r := range rec.kind("net.udp.drop") {
		texts = append(texts, r.Text+" @"+strconv.Itoa(int(r.Node)))
	}
	if want := []string{"udp drop a:10 -> 10.0.0.99:9: no-route @1", "udp drop a:10 -> b:49152: filtered @2"}; !slices.Equal(texts, want) {
		t.Errorf("net.udp.drop records %v, want %v", texts, want)
	}
	raws := rec.kind("net.send_raw")
	if len(raws) != 2 || attr(raws[0], "from") != "a" || attr(raws[0], "to") != "a" ||
		attr(raws[0], "delay_ns") != "10000" || attr(raws[0], "fifo") != "true" ||
		attr(raws[0], "payload") != "udp 10.0.0.1:10 -> 127.0.0.1:9 len=1" {
		t.Errorf("net.send_raw records %+v", raws)
	}
	recvs := rec.kind("net.udp.recv")
	if len(recvs) != 2 || recvs[0].Text != "udp recv a:10 -> 127.0.0.1:9 len=1" || recvs[0].Node != 1 ||
		recvs[1].Text != "udp recv 127.0.0.1:49152 -> 127.0.0.1:9 len=1" ||
		attrs(recvs[0]) != "src=a:10 dst=127.0.0.1:9 len=1" {
		t.Errorf("net.udp.recv records %+v", recvs)
	}
	if drops := rec.kind("net.udp.drop"); len(drops) == 0 || attrs(drops[0]) != "src=a:10 dst=10.0.0.99:9 len=1 reason=no-route" {
		t.Errorf("net.udp.drop attributes %+v", drops)
	}
}

// attrs prints r's attributes in order as "key=value key=value".
func attrs(r kernel.Record) string {
	var kv []string
	for _, a := range r.Attrs {
		kv = append(kv, a.Key+"="+a.Value)
	}
	return strings.Join(kv, " ")
}

// MaxDatagram bytes fit in a write and in a UDPRecvBuffer of MaxDatagram, and reading frees the
// buffer space.
func TestUDPBufferBoundary(t *testing.T) {
	var reads result[[]int]
	var rec records
	world(t, netsim.Config{UDPRecvBuffer: netsim.MaxDatagram}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			var ns []int
			buf := make([]byte, netsim.MaxDatagram+1)
			for range 2 {
				n, _, err := pc.ReadFrom(buf)
				if err != nil {
					return err
				}
				ns = append(ns, n)
			}
			reads.put(ns)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			c, err := p.Net.Dial("udp", "a:9")
			if err != nil {
				return err
			}
			for range 2 {
				if _, err := c.Write(make([]byte, netsim.MaxDatagram)); err != nil {
					return err
				}
				time.Sleep(100 * time.Millisecond)
			}
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if got := reads.get(t); !slices.Equal(got, []int{netsim.MaxDatagram, netsim.MaxDatagram}) {
		t.Fatalf("a read %v", got)
	}
	if n := len(rec.kind("net.udp.drop")); n != 0 {
		t.Fatalf("%d datagrams dropped", n)
	}
}

// Sockets close in ascending port order on a crash (reason crash) and an exit (reason exit), and
// at the end of the run without a record; blocked readers get net.ErrClosed when their socket
// closes.
func TestUDPCrashExitAndRunEnd(t *testing.T) {
	var crashRead, exitRead result[timed]
	var endRead result[error]
	var rec records
	bind := func(p *faultline.Process, ports ...string) (net.PacketConn, error) {
		var first net.PacketConn
		for _, port := range ports {
			pc, err := p.Net.ListenPacket("udp", ":"+port)
			if err != nil {
				return nil, err
			}
			if first == nil {
				first = pc
			}
		}
		return first, nil
	}
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			pc, err := bind(p, "10", "9")
			if err != nil {
				return err
			}
			p.Go(func() { _, _, err := pc.ReadFrom(make([]byte, 1)); crashRead.put(timed{at: vnow(), err: err}) })
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			pc, err := bind(p, "7", "5")
			if err != nil {
				return err
			}
			p.Go(func() { _, _, err := pc.ReadFrom(make([]byte, 1)); exitRead.put(timed{at: vnow(), err: err}) })
			time.Sleep(50 * time.Millisecond)
			return nil
		})
		w.AddProcess("c", func(ctx context.Context, p *faultline.Process) error {
			pc, err := bind(p, "3")
			if err != nil {
				return err
			}
			p.Go(func() { _, _, err := pc.ReadFrom(make([]byte, 1)); endRead.put(err) })
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(fault.Event{At: kernel.Time(100 * time.Millisecond), Kind: fault.KindCrash, Node: "a"}))
		rec.capture(w)
	})
	var closes []string
	for _, r := range rec.kind("net.udp.close") {
		closes = append(closes, r.Text+" @"+strconv.Itoa(int(r.Node))+" port="+attr(r, "port"))
	}
	want := []string{"udp close :5: exit @2 port=5", "udp close :7: exit @2 port=7",
		"udp close :9: crash @1 port=9", "udp close :10: crash @1 port=10"}
	if !slices.Equal(closes, want) {
		t.Errorf("net.udp.close records\n%v\nwant\n%v", closes, want)
	}
	if r := crashRead.get(t); !errors.Is(r.err, net.ErrClosed) || r.at != 100*time.Millisecond {
		t.Errorf("reader of the crashed process: %v at %v, want net.ErrClosed at 100ms", r.err, r.at)
	}
	if r := exitRead.get(t); !errors.Is(r.err, net.ErrClosed) || r.at != 50*time.Millisecond {
		t.Errorf("reader of the exited process: %v at %v, want net.ErrClosed at 50ms", r.err, r.at)
	}
	if err := endRead.get(t); !errors.Is(err, net.ErrClosed) {
		t.Errorf("reader blocked at the end of the run: %v, want net.ErrClosed", err)
	}
}

// Deadlines: a passed one fails reads and writes at once, a new one wakes a blocked reader, and a
// closed socket reports net.ErrClosed.
func TestUDPDeadlines(t *testing.T) {
	type out struct {
		pastWrite, pastRead, rearmed, setClosed, readClosed, writeClosed error
		rearmedAt                                                        time.Duration
		read                                                             string
	}
	var got result[out]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			var o out
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			to := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 9}
			_ = pc.SetWriteDeadline(time.Now())
			_, o.pastWrite = pc.WriteTo([]byte("x"), to)
			_ = pc.SetDeadline(time.Time{})
			msg := []byte("xy")
			if _, err := pc.WriteTo(msg, to); err != nil {
				return err
			}
			msg[0] = 'z' // WriteTo copied msg: the datagram still says "xy"
			buf := make([]byte, 2)
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return err
			}
			o.read = string(buf[:n])
			_ = pc.SetReadDeadline(time.Now().Add(-time.Second))
			_, _, o.pastRead = pc.ReadFrom(make([]byte, 1))
			_ = pc.SetReadDeadline(time.Time{})
			p.Go(func() {
				time.Sleep(100 * time.Millisecond)
				_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			})
			start := time.Now()
			_, _, o.rearmed = pc.ReadFrom(make([]byte, 1))
			o.rearmedAt = time.Since(start)
			if err := pc.Close(); err != nil {
				return err
			}
			o.setClosed = pc.SetDeadline(time.Time{})
			_, _, o.readClosed = pc.ReadFrom(make([]byte, 1))
			_, o.writeClosed = pc.WriteTo([]byte("x"), to)
			got.put(o)
			<-ctx.Done()
			return nil
		})
	})
	o := got.get(t)
	if o.read != "xy" {
		t.Errorf("read %q, want the bytes WriteTo was given, \"xy\"", o.read)
	}
	var ne net.Error
	if !errors.Is(o.pastWrite, os.ErrDeadlineExceeded) || !errors.Is(o.pastRead, os.ErrDeadlineExceeded) ||
		!errors.As(o.pastRead, &ne) || !ne.Timeout() {
		t.Errorf("passed deadlines: write %v, read %v", o.pastWrite, o.pastRead)
	}
	if !errors.Is(o.rearmed, os.ErrDeadlineExceeded) || o.rearmedAt != 150*time.Millisecond {
		t.Errorf("re-armed read returned %v after %v, want a deadline error after 150ms", o.rearmed, o.rearmedAt)
	}
	var oe *net.OpError
	if !errors.Is(o.setClosed, net.ErrClosed) || !errors.As(o.setClosed, &oe) || oe.Op != "set" ||
		!errors.Is(o.readClosed, net.ErrClosed) || !errors.Is(o.writeClosed, net.ErrClosed) {
		t.Errorf("closed socket: set %v, read %v, write %v", o.setClosed, o.readClosed, o.writeClosed)
	}
}

func TestUDPDialErrors(t *testing.T) {
	var got result[[]error]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			var errs []error
			for _, a := range [][2]string{{"udp", "10.0.0.99:9"}, {"udp", "nosuch:9"}, {"udp4", "a:x"}, {"ip", "a:9"}} {
				_, err := p.Net.Dial(a[0], a[1])
				errs = append(errs, err)
			}
			_, err := p.Net.ListenPacket("udp", "a:x")
			errs = append(errs, err)
			got.put(errs)
			<-ctx.Done()
			return nil
		})
	})
	e := got.get(t)
	var dns *net.DNSError
	var ae1, ae2 *net.AddrError
	var une net.UnknownNetworkError
	if !errors.Is(e[0], syscall.EHOSTUNREACH) || !errors.As(e[1], &dns) || !dns.IsNotFound ||
		!errors.As(e[2], &ae1) || ae1.Addr != "udp4/x" || !errors.As(e[3], &une) ||
		!errors.As(e[4], &ae2) || ae2.Err != "unknown port" {
		t.Fatalf("errors %v", e)
	}
}

// WriteTo keeps the lock through its checks: under -race, a Close on another goroutine of the
// process does not race with it. The writes fail before posting a request, so the trace does not
// depend on how the two goroutines interleave.
func TestUDPWriteToAndCloseRaceFree(t *testing.T) {
	var closed result[error]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			bad := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: -1}
			var started atomic.Bool
			p.Go(func() {
				for i := range 1000 {
					_, _ = pc.WriteTo([]byte("x"), bad)
					if i == 0 {
						started.Store(true) // orders only the first write before the Close
					}
				}
			})
			p.Go(func() {
				for !started.Load() {
					runtime.Gosched()
				}
				closed.put(pc.Close())
			})
			<-ctx.Done()
			return nil
		})
	})
	if err := closed.get(t); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
