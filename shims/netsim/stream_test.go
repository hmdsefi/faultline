// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
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

// connPair adds a (dials b:80 at t=0) and b (listens on :80, accepts once); each main gets its
// endpoint of the connection, established at 2 ms with the fixture's 1 ms links.
func connPair(w *faultline.World, aMain, bMain func(ctx context.Context, p *faultline.Process, c net.Conn) error) {
	w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
		c, err := p.Net.Dial("tcp", "b:80")
		if err != nil {
			return err
		}
		return aMain(ctx, p, c)
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
		return bMain(ctx, p, c)
	})
}

// closeWrite calls CloseWrite on c, which must be a *netsim.Conn.
func closeWrite(c net.Conn) error {
	nc, ok := c.(*netsim.Conn)
	if !ok {
		return fmt.Errorf("%T is not a *netsim.Conn", c)
	}
	return nc.CloseWrite()
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

func TestATNSM07_JitterReordering(t *testing.T) {
	var got result[[]byte]
	var rec records
	data := pattern(1 << 20)
	world(t, netsim.Config{}, faultline.Options{Net: simnet.Config{Default: simnet.Link{Latency: time.Millisecond, Jitter: 4 * time.Millisecond}}},
		func(w *faultline.World) {
			connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
				if _, err := c.Write(data); err != nil {
					return err
				}
				if err := closeWrite(c); err != nil {
					return err
				}
				<-ctx.Done()
				return nil
			}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
				b, err := io.ReadAll(c)
				if err != nil {
					return err
				}
				got.put(b)
				<-ctx.Done()
				return nil
			})
			rec.capture(w)
		})
	if !bytes.Equal(got.get(t), data) {
		t.Fatal("b read different bytes")
	}
	buffered := 0
	for _, r := range rec.kind("net.conn.arrive") {
		if attr(r, "result") == "buffered" {
			buffered++
		}
	}
	if buffered == 0 {
		t.Fatal("no buffered (out-of-order) arrival")
	}
}

func TestATNSM08_Backpressure(t *testing.T) {
	var written atomic.Int64
	var at4 result[int64]
	var doneAt result[time.Duration]
	var got result[[]byte]
	data := pattern(1 << 20)
	world(t, netsim.Config{RecvBuffer: 65536, SendBuffer: 65536}, faultline.Options{}, func(w *faultline.World) {
		written.Store(0) // FAULTLINE_CHECK_DETERMINISM runs the body twice
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			p.Go(func() {
				time.Sleep(4*time.Second - vnow())
				at4.put(written.Load())
			})
			for off := 0; off < len(data); off += 16 << 10 {
				n, err := c.Write(data[off : off+16<<10])
				written.Add(int64(n))
				if err != nil {
					return err
				}
			}
			doneAt.put(vnow())
			if err := closeWrite(c); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(5*time.Second - vnow())
			b, err := io.ReadAll(c)
			if err != nil {
				return err
			}
			got.put(b)
			<-ctx.Done()
			return nil
		})
	})
	if n := at4.get(t); n > 131072 {
		t.Fatalf("at 4s a had written %d bytes, want <= 131072", n)
	}
	if d := doneAt.get(t); d < 5*time.Second {
		t.Fatalf("writes finished at %v, before b started reading at 5s", d)
	}
	if !bytes.Equal(got.get(t), data) {
		t.Fatal("bytes differ")
	}
}

// AT-NSM-09 (1 % loss; NSM-032 explains why heavier loss ends in a timeout).
func TestATNSM09_LossAbsorbed(t *testing.T) {
	const dropPPM = 10_000 // AT-NSM-09 as specified
	var got result[[]byte]
	var rec records
	var sent bytes.Buffer
	for i := range 1000 {
		sent.WriteString(fmt.Sprintf("%099d\n", i)[:100])
	}
	world(t, netsim.Config{}, faultline.Options{}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			for i := range 1000 {
				if _, err := c.Write(sent.Bytes()[i*100 : (i+1)*100]); err != nil {
					return err
				}
				time.Sleep(time.Millisecond)
			}
			if err := closeWrite(c); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			b, err := io.ReadAll(c)
			if err != nil {
				return err
			}
			got.put(b)
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(fault.Event{At: kernel.Time(2 * time.Millisecond), Kind: fault.KindLink, Node: "a", Peer: "b",
			Link: &simnet.Link{Latency: time.Millisecond, DropPPM: dropPPM}}))
		rec.capture(w)
	})
	if !bytes.Equal(got.get(t), sent.Bytes()) {
		t.Fatal("b did not receive every byte in order")
	}
	lost := 0
	for _, r := range rec.kind("net.conn.segment") {
		if attr(r, "outcome") == "lost" {
			lost++
		}
	}
	if lost == 0 || len(rec.kind("net.conn.timeout")) != 0 {
		t.Fatalf("lost attempts %d, timeouts %d; want > 0 and 0", lost, len(rec.kind("net.conn.timeout")))
	}
}

func TestATNSM10_PartitionHealedBeforeTimeout(t *testing.T) {
	var read result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{Duration: 20 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(2*time.Second - vnow())
			if _, err := c.Write([]byte("0123456789")); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			b := make([]byte, 100)
			n, err := c.Read(b)
			read.put(timed{at: vnow(), n: n, err: err})
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(time.Second), Kind: fault.KindPartition, Groups: [][]string{{"a"}, {"b"}}},
			fault.Event{At: kernel.Time(6 * time.Second), Kind: fault.KindHeal},
		))
		rec.capture(w)
	})
	if r := read.get(t); r.err != nil || r.n != 10 || r.at != 8201*time.Millisecond {
		t.Fatalf("b read %d bytes at %v (%v); want 10 at 8.201s", r.n, r.at, r.err)
	}
	if n := len(rec.kind("net.conn.reset")); n != 0 {
		t.Fatalf("%d resets, want 0", n)
	}
}

func TestATNSM11_ConnectionTimeout(t *testing.T) {
	var aErr, bErr result[timed]
	var rec records
	world(t, netsim.Config{}, faultline.Options{}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(2*time.Second - vnow())
			if _, err := c.Write([]byte("0123456789")); err != nil {
				return err
			}
			time.Sleep(35*time.Second - vnow())
			_, err := c.Read(make([]byte, 10))
			aErr.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(41*time.Second - vnow())
			if _, err := c.Write([]byte("late")); err != nil {
				return err
			}
			_, err := c.Read(make([]byte, 10))
			bErr.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		})
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(time.Second), Kind: fault.KindPartition, Groups: [][]string{{"a"}, {"b"}}},
			fault.Event{At: kernel.Time(40 * time.Second), Kind: fault.KindHeal},
		))
		rec.capture(w)
	})
	var at []string
	for _, r := range rec.kind("net.conn.segment") {
		if attr(r, "kind") == "data" && attr(r, "dir") == "c2s" {
			at = append(at, r.At.String())
		}
	}
	want := []string{"2.000000000s", "2.200000000s", "2.600000000s", "3.400000000s", "5.000000000s", "8.200000000s", "14.600000000s", "24.600000000s"}
	if fmt.Sprint(at) != fmt.Sprint(want) {
		t.Fatalf("data attempts at %v, want %v", at, want)
	}
	if to := rec.kind("net.conn.timeout"); len(to) != 1 || to[0].At != kernel.Time(34600*time.Millisecond) || to[0].Node != 1 {
		t.Fatalf("net.conn.timeout %+v", to)
	}
	if e := aErr.get(t); !errors.Is(e.err, syscall.ETIMEDOUT) {
		t.Fatalf("a's Read after the timeout: %v", e.err)
	}
	if e := bErr.get(t); !errors.Is(e.err, syscall.ECONNRESET) || e.at > 41002*time.Millisecond {
		t.Fatalf("b's Read: %v at %v; want ECONNRESET by 41.002s", e.err, e.at)
	}
}

func TestATNSM14_HalfClose(t *testing.T) {
	var bRead result[string]
	var aRead result[string]
	var aWrite result[error]
	world(t, netsim.Config{}, faultline.Options{Duration: 5 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			if _, err := c.Write([]byte("hello")); err != nil {
				return err
			}
			if err := closeWrite(c); err != nil {
				return err
			}
			_, err := c.Write([]byte("more"))
			aWrite.put(err)
			b := make([]byte, 10)
			n, err := c.Read(b)
			if err != nil {
				return err
			}
			aRead.put(string(b[:n]))
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			b, err := io.ReadAll(c)
			if err != nil {
				return err
			}
			bRead.put(string(b))
			if _, err := c.Write([]byte("bye")); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
	})
	if bRead.get(t) != "hello" || aRead.get(t) != "bye" || !errors.Is(aWrite.get(t), syscall.EPIPE) {
		t.Fatalf("b read %q, a read %q, a's write after CloseWrite %v", bRead.get(t), aRead.get(t), aWrite.get(t))
	}
}

// A receiver that called CloseRead never reads, so the arrivals it discards must reopen the
// window: a's queued bytes beyond one window and its fin still go out.
func TestCloseReadKeepsTheSenderGoing(t *testing.T) {
	const size = 128 << 10
	var rec records
	world(t, netsim.Config{SendBuffer: size, RecvBuffer: 64 << 10}, faultline.Options{Duration: 20 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(100*time.Millisecond - vnow())
			if _, err := c.Write(pattern(size)); err != nil {
				return err
			}
			if err := closeWrite(c); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			nc, ok := c.(*netsim.Conn)
			if !ok {
				return fmt.Errorf("%T is not a *netsim.Conn", c)
			}
			if err := nc.CloseRead(); err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
		rec.capture(w)
	})
	data, fins := 0, 0
	for _, r := range rec.kind("net.conn.segment") {
		if attr(r, "dir") != "c2s" || attr(r, "attempt") != "1" {
			continue
		}
		switch attr(r, "kind") {
		case "data":
			n, err := strconv.Atoi(attr(r, "len"))
			if err != nil {
				t.Fatal(err)
			}
			data += n
		case "fin":
			fins++
		}
	}
	if data != size || fins != 1 {
		t.Fatalf("a sent %d of %d bytes and %d fins; want all bytes and one fin", data, size, fins)
	}
}

func TestATNSM17_ReadDeadlines(t *testing.T) {
	var first, second, third result[timed]
	world(t, netsim.Config{}, faultline.Options{Duration: 2 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			p.Go(func() {
				time.Sleep(50*time.Millisecond - vnow())
				_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			})
			b := make([]byte, 10)
			_, err := c.Read(b)
			first.put(timed{at: vnow(), err: err})
			_ = c.SetReadDeadline(time.Time{})
			n, err := c.Read(b)
			second.put(timed{at: vnow(), n: n, err: err})
			time.Sleep(400*time.Millisecond - vnow())
			_ = c.SetReadDeadline(time.Now().Add(-time.Millisecond))
			_, err = c.Read(b)
			third.put(timed{at: vnow(), err: err})
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			time.Sleep(200*time.Millisecond - vnow())
			_, _ = c.Write([]byte("abc"))
			time.Sleep(300*time.Millisecond - vnow())
			_, _ = c.Write([]byte("buffered"))
			<-ctx.Done()
			return nil
		})
	})
	var ne net.Error
	if f := first.get(t); f.at != 150*time.Millisecond || !errors.Is(f.err, os.ErrDeadlineExceeded) || !errors.As(f.err, &ne) || !ne.Timeout() {
		t.Fatalf("first Read: %v at %v", f.err, f.at)
	}
	if s := second.get(t); s.err != nil || s.n != 3 || s.at != 201*time.Millisecond {
		t.Fatalf("second Read: %d bytes at %v (%v)", s.n, s.at, s.err)
	}
	if x := third.get(t); x.at != 400*time.Millisecond || !errors.Is(x.err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read with a past deadline and buffered data: %v at %v", x.err, x.at)
	}
}

func TestATNSM18_WriteDeadline(t *testing.T) {
	var res result[timed]
	world(t, netsim.Config{SendBuffer: 64 << 10}, faultline.Options{Duration: 3 * time.Second}, func(w *faultline.World) {
		connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			_ = c.SetWriteDeadline(kernel.Epoch.Add(time.Second))
			n, err := c.Write(pattern(1 << 20))
			res.put(timed{at: vnow(), n: n, err: err})
			<-ctx.Done()
			return nil
		}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
			<-ctx.Done()
			return nil
		})
	})
	r := res.get(t)
	if r.at != time.Second || r.n <= 0 || r.n >= 1<<20 || !errors.Is(r.err, os.ErrDeadlineExceeded) {
		t.Fatalf("Write returned %d, %v at %v", r.n, r.err, r.at)
	}
}

// A blocked Write and a Read that one arrival wakes run in either order, and the trace must not
// depend on it (GOR-031). Each in-order arrival acknowledges a segment of a, which wakes a's
// Write, and hands its bytes to b, which wakes b's Read. The determinism check runs each seed twice.
// With the default 64 KiB RecvBuffer the window never fills; with RecvBuffer below SendBuffer it
// fills on every arrival, so b's Read posts net.window while a's Write posts net.pump.
func TestStreamWakeOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  netsim.Config
	}{
		{"window open", netsim.Config{SendBuffer: 8 << 10}},
		{"window full", netsim.Config{SendBuffer: 16 << 10, RecvBuffer: 4 << 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got result[[]byte]
			data := pattern(256 << 10)
			world(t, tc.cfg, faultline.Options{Seeds: 4, CheckDeterminism: true}, func(w *faultline.World) {
				connPair(w, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
					if _, err := c.Write(data); err != nil {
						return err
					}
					if err := closeWrite(c); err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				}, func(ctx context.Context, p *faultline.Process, c net.Conn) error {
					b, err := io.ReadAll(c)
					if err != nil {
						return err
					}
					got.put(b)
					<-ctx.Done()
					return nil
				})
			})
			if !bytes.Equal(got.get(t), data) {
				t.Fatal("b read different bytes")
			}
		})
	}
}

// One process streams to itself over loopback, so both endpoints are on one node. An arrival
// wakes the writer's Write and the reader's Read together; their net.pump and net.window
// requests must carry different keys, or GOR-031 orders the tie by which goroutine posted first.
func TestLoopbackStreamWakeOrder(t *testing.T) {
	var got result[[]byte]
	data := pattern(256 << 10)
	world(t, netsim.Config{SendBuffer: 16 << 10, RecvBuffer: 4 << 10}, faultline.Options{Seeds: 4, CheckDeterminism: true}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			ln, err := p.Net.Listen("tcp", "127.0.0.1:80")
			if err != nil {
				return err
			}
			errc := make(chan error, 1)
			p.Go(func() {
				c, err := p.Net.Dial("tcp", "127.0.0.1:80")
				if err != nil {
					errc <- err
					return
				}
				if _, err := c.Write(data); err != nil {
					errc <- err
					return
				}
				errc <- closeWrite(c)
			})
			c, err := ln.Accept()
			if err != nil {
				return err
			}
			b, err := io.ReadAll(c)
			if err != nil {
				return err
			}
			got.put(b)
			if err := <-errc; err != nil {
				return err
			}
			<-ctx.Done()
			return nil
		})
	})
	if !bytes.Equal(got.get(t), data) {
		t.Fatal("the server end read different bytes")
	}
}
