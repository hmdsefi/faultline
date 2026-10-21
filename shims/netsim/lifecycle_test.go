// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/netsim"
)

func blockRead(c net.Conn, out *result[timed]) {
	_, err := c.Read(make([]byte, 10))
	out.put(timed{at: vnow(), err: err})
}

func TestATNSM12_PeerCrashResets(t *testing.T) {
	var read result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 3 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			blockRead(c, &read)
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(fault.Event{At: kernel.Time(time.Second), Kind: fault.KindCrash, Node: "b"}))
		rec.capture(w)
	})
	if r := read.get(t); r.at != 1001*time.Millisecond || !errors.Is(r.err, syscall.ECONNRESET) {
		t.Fatalf("Read: %v at %v; want ECONNRESET at 1.001s", r.err, r.at)
	}
	rst := rec.kind("net.conn.rst")
	if len(rst) != 1 || rst[0].Node != 2 || rst[0].At != kernel.Time(time.Second) || rst[0].Text != "crash rst c1 -> a" ||
		attr(rst[0], "peer") != "a" || attr(rst[0], "delay_ns") != "1000000" {
		t.Fatalf("net.conn.rst %+v", rst)
	}
	found := false
	for _, r := range rec.kind("net.conn.reset") {
		found = found || (r.Node == 1 && attr(r, "reason") == "peer-crashed")
	}
	if !found {
		t.Fatal("no net.conn.reset reason=peer-crashed on a")
	}
}

func TestATNSM13_IsolatedCrashLearnedLater(t *testing.T) {
	var read result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 6 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			p.Go(func() { blockRead(c, &read) })
			time.Sleep(4*time.Second - vnow())
			_, _ = c.Write([]byte("x"))
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(500 * time.Millisecond), Kind: fault.KindIsolate, Node: "b"},
			fault.Event{At: kernel.Time(time.Second), Kind: fault.KindCrash, Node: "b"},
			fault.Event{At: kernel.Time(2 * time.Second), Kind: fault.KindRestart, Node: "b"},
			fault.Event{At: kernel.Time(3 * time.Second), Kind: fault.KindHeal},
		))
		rec.capture(w)
	})
	for _, r := range rec.kind("net.conn.reset") {
		if r.Node == 1 && r.At < kernel.Time(4*time.Second) {
			t.Fatalf("a reset before its write: %+v", r)
		}
	}
	if r := read.get(t); !errors.Is(r.err, syscall.ECONNRESET) || r.at < 4*time.Second {
		t.Fatalf("a's Read: %v at %v; want ECONNRESET after 4s", r.err, r.at)
	}
}

func TestATNSM15_CloseWithUnreadData(t *testing.T) {
	var read result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			_, _ = c.Write([]byte("hello"))
			blockRead(c, &read)
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(100*time.Millisecond - vnow())
			_ = c.Close()
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if r := read.get(t); !errors.Is(r.err, syscall.ECONNRESET) {
		t.Fatalf("a's Read: %v", r.err)
	}
	cl := rec.kind("net.conn.close")
	if len(cl) != 1 || attr(cl[0], "mode") != "rst" || cl[0].Text != "close c1: rst" {
		t.Fatalf("net.conn.close %+v", cl)
	}
}

func TestATNSM16_WriteAfterPeerClosed(t *testing.T) {
	var eof, first, later result[error]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			_, _ = c.Write([]byte("hello"))
			_, err := c.Read(make([]byte, 10))
			eof.put(err)
			_, err = c.Write([]byte("x"))
			first.put(err)
			time.Sleep(10 * time.Millisecond) // the data reaches b, b answers rst (NSM-027)
			_, err = c.Read(make([]byte, 10))
			later.put(err)
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			b := make([]byte, 5)
			if _, err := io.ReadFull(c, b); err != nil {
				return err
			}
			_ = c.Close()
			<-ctx.Done()
			return nil
		})
	})
	if eof.get(t) != io.EOF || first.get(t) != nil || !errors.Is(later.get(t), syscall.ECONNRESET) {
		t.Fatalf("Read %v, first Write %v, later call %v", eof.get(t), first.get(t), later.get(t))
	}
}

func TestATNSM20_ListenerClose(t *testing.T) {
	var blocked result[error]
	var reads [2]result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 2 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			for i := range 2 {
				c, err := p.Net.Dial("tcp", "b:80")
				if err != nil {
					return err
				}
				p.Go(func() { blockRead(c, &reads[i]) })
			}
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			l1, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			l2, err := p.Net.Listen("tcp", ":81")
			if err != nil {
				return err
			}
			p.Go(func() {
				_, err := l2.Accept()
				blocked.put(err)
			})
			time.Sleep(time.Second - vnow())
			_ = l1.Close()
			_ = l2.Close()
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if !errors.Is(blocked.get(t), net.ErrClosed) {
		t.Fatalf("blocked Accept: %v", blocked.get(t))
	}
	for i := range reads {
		if r := reads[i].get(t); !errors.Is(r.err, syscall.ECONNRESET) {
			t.Fatalf("client %d Read: %v", i, r.err)
		}
	}
	ok := false
	for _, r := range rec.kind("net.conn.unlisten") {
		ok = ok || (attr(r, "port") == "80" && attr(r, "reason") == "close" && attr(r, "reset") == "2" && r.Text == "unlisten :80: close")
	}
	if !ok {
		t.Fatalf("no net.conn.unlisten port=80 reason=close reset=2: %+v", rec.kind("net.conn.unlisten"))
	}
}

// AT-NSM-36: a listener re-opened on its port while a connection it accepted is still open
// (NSM-020: server endpoints do not block Listen, as with SO_REUSEADDR).
func TestATNSM36_RelistenWithOpenConn(t *testing.T) {
	var relisten, second result[error]
	var got result[string]
	world(t, netsim.Config{}, faultline.Options{Duration: 2 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			c, err := p.Net.Dial("tcp", "b:80")
			if err != nil {
				return err
			}
			time.Sleep(time.Second - vnow())
			if _, err := c.Write([]byte("x")); err != nil {
				return err
			}
			_, err = p.Net.Dial("tcp", "b:80")
			second.put(err)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			l1, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			c, err := l1.Accept()
			if err != nil {
				return err
			}
			_ = l1.Close()
			l2, err := p.Net.Listen("tcp", ":80")
			relisten.put(err)
			if err != nil {
				<-ctx.Done()
				return nil
			}
			p.Go(func() { _, _ = l2.Accept() })
			b := make([]byte, 1)
			if _, err := io.ReadFull(c, b); err == nil {
				got.put(string(b))
			}
			<-ctx.Done()
			return nil
		})
	})
	if err := relisten.get(t); err != nil {
		t.Fatalf("second Listen on :80: %v", err)
	}
	if s := got.get(t); s != "x" {
		t.Fatalf("old conn read %q, want x", s)
	}
	if err := second.get(t); err != nil {
		t.Fatalf("dial to the re-opened listener: %v", err)
	}
}

func TestATNSM26_ProcessExitReleases(t *testing.T) {
	var peer, dial result[error]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 3 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			if _, err := p.Net.Listen("tcp", ":80"); err != nil {
				return err
			}
			if _, err := p.Net.ListenPacket("udp", ":9"); err != nil {
				return err
			}
			if _, err := p.Net.Dial("tcp", "b:80"); err != nil {
				return err
			}
			time.Sleep(time.Second - vnow())
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			ln, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			c, err := ln.Accept()
			if err != nil {
				return err
			}
			_, err = c.Read(make([]byte, 10))
			peer.put(err)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("c", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(2*time.Second - vnow())
			_, err := p.Net.Dial("tcp", "a:80")
			dial.put(err)
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if peer.get(t) != io.EOF {
		t.Fatalf("peer Read: %v, want io.EOF", peer.get(t))
	}
	if !errors.Is(dial.get(t), syscall.ECONNREFUSED) {
		t.Fatalf("dial to the exited listener: %v", dial.get(t))
	}
	exitUnlisten, exitUDP := false, false
	for _, r := range rec.kind("net.conn.unlisten") {
		exitUnlisten = exitUnlisten || attr(r, "reason") == "exit"
	}
	for _, r := range rec.kind("net.udp.close") {
		exitUDP = exitUDP || attr(r, "reason") == "exit"
	}
	if !exitUnlisten || !exitUDP {
		t.Fatal("missing reason=exit records")
	}
}

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	childScenarios["nsm-27"] = func(t *testing.T) {
		var read, accept, readFrom, dial result[error]
		world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
			connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
				ln, err := p.Net.Listen("tcp", ":81")
				if err != nil {
					return err
				}
				pc, err := p.Net.ListenPacket("udp", ":9")
				if err != nil {
					return err
				}
				p.Go(func() { _, err := c.Read(make([]byte, 1)); read.put(err) })
				p.Go(func() { _, err := ln.Accept(); accept.put(err) })
				p.Go(func() { _, _, err := pc.ReadFrom(make([]byte, 1)); readFrom.put(err) })
				p.Go(func() { _, err := p.Net.Dial("tcp", "z:80"); dial.put(err) })
				<-ctx.Done()
				return nil
			}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
				<-ctx.Done()
				return nil
			})
			w.AddProcess("z", func(ctx context.Context, p *faultline.Process) error { <-ctx.Done(); return nil })
			w.Plan(fault.Script(fault.Event{At: 0, Kind: fault.KindIsolate, Node: "z"}))
		})
		ok := errors.Is(read.get(t), net.ErrClosed) && errors.Is(accept.get(t), net.ErrClosed) &&
			errors.Is(readFrom.get(t), net.ErrClosed) && errors.Is(dial.get(t), netsim.ErrProcessGone)
		t.Logf("nsm27-ok=%v (%v | %v | %v | %v)", ok, read.get(t), accept.get(t), readFrom.get(t), dial.get(t))
	}
}

func TestATNSM27_RunEndUnblocks(t *testing.T) {
	out, code := runChild(t, "nsm-27")
	if code != 0 || !strings.Contains(out, "nsm27-ok=true") || strings.Contains(out, "still blocked") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestATNSM28_PausedNodeIO(t *testing.T) {
	var read result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 3 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			p.Go(func() {
				time.Sleep(1500*time.Millisecond - vnow())
				_, _ = c.Write([]byte("from-a"))
			})
			b := make([]byte, 10)
			n, err := c.Read(b)
			read.put(timed{at: vnow(), n: n, err: err})
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(1500*time.Millisecond - vnow())
			_, _ = c.Write([]byte("from-b"))
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(time.Second), Kind: fault.KindPause, Node: "a"},
			fault.Event{At: kernel.Time(2 * time.Second), Kind: fault.KindResume, Node: "a"},
		))
		rec.capture(w)
	})
	if r := read.get(t); r.err != nil || r.n != 6 || r.at != 2*time.Second {
		t.Fatalf("a read %d bytes at %v (%v); want 6 at 2s", r.n, r.at, r.err)
	}
	for _, r := range rec.kind("net.conn.segment") {
		if r.Node == 1 && attr(r, "kind") == "data" {
			if r.At != kernel.Time(2*time.Second) {
				t.Fatalf("a's data segment first attempted at %v, want 2s", r.At)
			}
			break
		}
	}
	if n := len(rec.kind("net.conn.reset")); n != 0 {
		t.Fatalf("%d resets", n)
	}
}

// A process that writes and then returns nil exits in the same quiescence window: process.exit
// sorts before the udp: key of its send request, and the exit cancels that request. Release
// carries the send out first (NSM-061), as a kernel keeps sending after a process exits.
func TestExitSendsQueuedDatagram(t *testing.T) {
	var got result[string]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			c, err := p.Net.Dial("udp", "b:9")
			if err != nil {
				return err
			}
			time.Sleep(10 * time.Millisecond)
			_, err = c.Write([]byte("bye"))
			return err
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			b := make([]byte, 10)
			if n, _, err := pc.ReadFrom(b); err == nil {
				got.put(string(b[:n]))
			}
			<-ctx.Done()
			return nil
		})
	})
	if s := got.get(t); s != "bye" {
		t.Fatalf("b read %q, want bye", s)
	}
}

// The TCP counterpart: the bytes of a Write right before the exit reach the peer before the fin.
func TestExitPumpsQueuedBytes(t *testing.T) {
	var got result[timed]
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(10*time.Millisecond - vnow())
			_, err := c.Write([]byte("bye"))
			return err
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			b, err := io.ReadAll(c)
			got.put(timed{s: string(b), err: err})
			<-ctx.Done()
			return nil
		})
	})
	if r := got.get(t); r.s != "bye" || r.err != nil {
		t.Fatalf("b read %q (%v), want bye then io.EOF", r.s, r.err)
	}
}

// With unread data the exit resets the connection (NSM-045). The bytes written right before the
// exit are still sent first: on a FIFO link the peer reads them, then gets ECONNRESET.
func TestExitPumpsQueuedBytesBeforeReset(t *testing.T) {
	var first, second result[timed]
	var rec records
	opts := faultline.Options{Duration: time.Second, Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond, FIFO: true}}}
	world(t, netsim.Config{}, opts, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(20*time.Millisecond - vnow()) // b's "x" arrived and stays unread
			_, err := c.Write([]byte("bye"))
			return err
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			if _, err := c.Write([]byte("x")); err != nil {
				return err
			}
			b := make([]byte, 10)
			n, err := c.Read(b)
			first.put(timed{s: string(b[:n]), err: err})
			_, err = c.Read(b)
			second.put(timed{err: err})
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if r := first.get(t); r.s != "bye" || r.err != nil {
		t.Fatalf("b's first Read %q (%v), want bye", r.s, r.err)
	}
	if r := second.get(t); !errors.Is(r.err, syscall.ECONNRESET) {
		t.Fatalf("b's second Read: %v, want ECONNRESET", r.err)
	}
	inOrder(t, rec.all(),
		func(r kernel.Record) bool {
			return r.Node == 1 && match(r, "net.conn.segment", "dir", "c2s", "kind", "data")
		},
		func(r kernel.Record) bool {
			return r.Node == 1 && match(r, "net.conn.segment", "dir", "c2s", "kind", "rst")
		},
		func(r kernel.Record) bool { return match(r, "net.conn.close", "conn", "c1", "mode", "rst") },
	)
}

// Close calls made right before the exit take effect: their requests sort after process.exit,
// and Release carries them out instead of letting the exit cancel them (NSM-061).
func TestExitCarriesOutPendingCloses(t *testing.T) {
	var got result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			ln, err := p.Net.Listen("tcp", ":81")
			if err != nil {
				return err
			}
			pc, err := p.Net.ListenPacket("udp", ":9")
			if err != nil {
				return err
			}
			time.Sleep(10*time.Millisecond - vnow())
			if _, err := c.Write([]byte("x")); err != nil {
				return err
			}
			_ = c.Close()
			_ = ln.Close()
			_ = pc.Close()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			b, err := io.ReadAll(c)
			got.put(timed{s: string(b), err: err})
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	if r := got.get(t); r.s != "x" || r.err != nil {
		t.Fatalf("b read %q (%v), want x then io.EOF", r.s, r.err)
	}
	var closes []string
	for _, r := range rec.all() {
		if r.Node == 1 && (r.Kind == "net.conn.close" || r.Kind == "net.conn.unlisten" || r.Kind == "net.udp.close") {
			closes = append(closes, r.Text)
		}
	}
	// The unlisten request's key sorts before process.exit, so it runs before the exit.
	if want := []string{"unlisten :81: close", "close c1: fin", "udp close :9: close"}; strings.Join(closes, "|") != strings.Join(want, "|") {
		t.Fatalf("a's close records %q, want %q", closes, want)
	}
}

// A node that crashes while its own dial is pending: the crash fails the dial and removes its
// endpoint, so the retransmission check no longer sends from the down node.
func TestCrashWhileDialing(t *testing.T) {
	var dial result[error]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 3 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			time.Sleep(10 * time.Millisecond)
			_, err := p.Net.Dial("tcp", "b:80")
			dial.put(err)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", wait)
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(5 * time.Millisecond), Kind: fault.KindPause, Node: "b"}, // the syn waits at b
			fault.Event{At: kernel.Time(500 * time.Millisecond), Kind: fault.KindCrash, Node: "a"},
		))
		rec.capture(w)
	})
	if err := dial.get(t); !errors.Is(err, netsim.ErrProcessGone) {
		t.Fatalf("Dial: %v, want ErrProcessGone", err)
	}
	for _, r := range rec.kind("net.conn.segment") {
		if r.At > kernel.Time(500*time.Millisecond) {
			t.Fatalf("segment after the crash: %+v", r)
		}
	}
}

// Both ends of a loopback connection are on one node. When the two ends close at the same instant,
// or the process exits while both have bytes to send, the requests of the client end run before
// those of the server end, whichever goroutine posted first (NSM-002).
func TestLoopbackCloseAndExit(t *testing.T) {
	opts := faultline.Options{Seeds: 4, CheckDeterminism: true, Duration: time.Second}
	at := kernel.Time(100 * time.Millisecond)
	seg := func(dir, kind, n string) func(kernel.Record) bool {
		return func(r kernel.Record) bool {
			return r.At == at && match(r, "net.conn.segment", "dir", dir, "kind", kind, "len", n, "outcome", "loopback")
		}
	}
	finClose := func(r kernel.Record) bool {
		return r.At == at && r.Kind == "net.conn.close" && r.Text == "close c1: fin"
	}
	closes := func(t *testing.T, rec *records) {
		t.Helper()
		var got []string
		for _, r := range rec.kind("net.conn.close") {
			got = append(got, r.Text)
		}
		if strings.Join(got, "|") != "close c1: fin|close c1: fin" {
			t.Fatalf("close records %q, want two fins", got)
		}
	}

	t.Run("both ends close", func(t *testing.T) {
		var cli, srv result[error]
		var rec records
		world(t, netsim.Config{}, opts, func(w *faultline.World) {
			w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
				ln, err := p.Net.Listen("tcp", "127.0.0.1:80")
				if err != nil {
					return err
				}
				p.Go(func() {
					c, err := p.Net.Dial("tcp", "127.0.0.1:80")
					if err == nil {
						err = exchangeAndClose(c, "ping", "pong", 100*time.Millisecond)
					}
					cli.put(err)
				})
				c, err := ln.Accept()
				if err != nil {
					return err
				}
				srv.put(exchangeAndClose(c, "pong", "ping", 100*time.Millisecond))
				<-ctx.Done()
				return nil
			})
			rec.capture(w)
		})
		if err := cli.get(t); err != nil {
			t.Fatalf("client end: %v", err)
		}
		if err := srv.get(t); err != nil {
			t.Fatalf("server end: %v", err)
		}
		closes(t, &rec)
		inOrder(t, rec.all(), seg("c2s", "fin", "0"), finClose, seg("s2c", "fin", "0"), finClose)
	})

	t.Run("exit with the connection open", func(t *testing.T) {
		var rec records
		world(t, netsim.Config{}, opts, func(w *faultline.World) {
			w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
				ln, err := p.Net.Listen("tcp", "127.0.0.1:80")
				if err != nil {
					return err
				}
				p.Go(func() {
					c, err := p.Net.Dial("tcp", "127.0.0.1:80")
					if err != nil {
						return
					}
					time.Sleep(100*time.Millisecond - vnow())
					_, _ = c.Write([]byte("from-cli"))
					_, _ = c.Read(make([]byte, 1)) // returns once the exit closes the endpoint
				})
				c, err := ln.Accept()
				if err != nil {
					return err
				}
				time.Sleep(100*time.Millisecond - vnow())
				_, err = c.Write([]byte("from-srv"))
				return err
			})
			rec.capture(w)
		})
		closes(t, &rec)
		inOrder(t, rec.all(),
			func(r kernel.Record) bool { return r.At == at && r.Kind == "driver.process.exit" },
			seg("c2s", "data", "8"), seg("s2c", "data", "8"), finClose, finClose)
	})
}

// exchangeAndClose writes out, reads want, waits until the virtual time close, and closes c.
func exchangeAndClose(c net.Conn, out, want string, close time.Duration) error {
	if _, err := c.Write([]byte(out)); err != nil {
		return err
	}
	b := make([]byte, len(want))
	if _, err := io.ReadFull(c, b); err != nil {
		return err
	}
	if string(b) != want {
		return errors.New("read " + string(b) + ", want " + want)
	}
	time.Sleep(close - vnow())
	return c.Close()
}
