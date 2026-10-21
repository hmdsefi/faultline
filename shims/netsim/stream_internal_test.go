// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// streamPair builds the established connection c1 from a (10.0.0.1:49152) to b (10.0.0.2:80) on a
// postDriver, with full tracing and 1 ms links. The hosts' simnet handlers are removed, so the
// segments a test sends arrive nowhere and only the sending side runs.
func streamPair(t *testing.T, cfg Config) *pair {
	t.Helper()
	return newStreamPair(t, cfg, false)
}

// newStreamPair is streamPair; with loopback set, both endpoints of c1 are on a (10.0.0.1:49152 to
// 10.0.0.1:80).
func newStreamPair(t *testing.T, cfg Config, loopback bool) *pair {
	t.Helper()
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	nw := simnet.New(s, simnet.Config{Default: simnet.Link{Latency: time.Millisecond}})
	d := &postDriver{}
	st := New(s, nw, d, cfg)
	boot := func(n *kernel.Node) { st.Host(n) }
	a := s.AddNode("a", boot)
	b := s.AddNode("b", boot)
	s.RunFor(time.Millisecond)
	ha, hb := st.cur(a.ID()), st.cur(b.ID())
	mute(nw, ha, hb)
	if loopback {
		hb = ha
	}
	c, cli := clientConn(st, ha, hb)
	srv := newConn(c, 1, hb, stEstablished, cli.remote, cli.local)
	c.srv = srv
	hb.addEndpoint(srv)
	return &pair{s: s, d: d, st: st, cli: cli, srv: srv}
}

// pair is what streamPair returns.
type pair struct {
	s        *kernel.Sim
	d        *postDriver
	st       *Stack
	cli, srv *Conn
}

// segments returns "n<node> <text>" of the net.conn.segment records emitted at or after t0.
func segments(s *kernel.Sim, t0 kernel.Time) []string {
	var out []string
	for _, r := range s.Records() {
		if r.At >= t0 && r.Kind == "net.conn.segment" {
			out = append(out, fmt.Sprintf("n%d %s", r.Node, r.Text))
		}
	}
	return out
}

// postLines renders the driver's requests from index i on as "n<node>/<inc> <kind> <key>".
func postLines(d *postDriver, i int) []string {
	var out []string
	for _, p := range d.posts[i:] {
		out = append(out, fmt.Sprintf("n%d/%d %s %s", p.node, p.inc, p.kind, p.key))
	}
	return out
}

// drainLines renders the driver's requests as postLines does, in the order a drain runs one
// window's requests (GOR-031): by node, then key, then posting order.
func drainLines(d *postDriver) []string {
	ps := slices.Clone(d.posts)
	slices.SortStableFunc(ps, func(x, y posted) int {
		return cmp.Or(cmp.Compare(x.node, y.node), cmp.Compare(x.key, y.key))
	})
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("n%d/%d %s %s", p.node, p.inc, p.kind, p.key))
	}
	return out
}

// pump segments queued bytes by MSS within the receiver window, which counts in-flight, buffered
// and out-of-order bytes (equal to RecvBuffer still fits), and sets windowFull while the window
// holds bytes back; the fin follows the last data segment once, needs no window, and a sender that
// is not live sends nothing.
func TestPump(t *testing.T) {
	p := streamPair(t, Config{MSS: 100, RecvBuffer: 450})
	s, st, cli := p.s, p.st, p.cli
	d := cli.c.dirs[c2s]
	d.queued = bytes.Repeat([]byte("x"), 520)
	d.buf, d.oooBytes = make([]byte, 50), 100
	d.finPending = true
	t0 := s.Now()
	at(s, func() { st.pump(d) })
	sameLines(t, "window", segments(s, t0), []string{
		"n1 c1 c2s data seq=1 len=100 attempt=1: sent",
		"n1 c1 c2s data seq=2 len=100 attempt=1: sent",
		"n1 c1 c2s data seq=3 len=100 attempt=1: sent",
	})
	if len(d.queued) != 220 || d.inflight != 300 || d.nextSeq != 4 || len(d.unacked) != 3 || d.finSent || !d.checkPending || !d.windowFull {
		t.Fatalf("after the window closed: queued %d, inflight %d, nextSeq %d, unacked %d, windowFull %v", len(d.queued), d.inflight, d.nextSeq, len(d.unacked), d.windowFull)
	}
	if u := d.unacked[1]; !bytes.Equal(u.data, bytes.Repeat([]byte("x"), 100)) || u.kind != kData || u.dir != c2s || u.c != cli.c {
		t.Fatalf("second segment %s", u.Describe())
	}

	d.queued, d.buf, d.oooBytes, d.inflight = []byte("0123456789abcdef"), nil, 0, 0
	d.queued = append(d.queued, bytes.Repeat([]byte("y"), 134)...)
	t1 := s.Now()
	at(s, func() { st.pump(d) })
	at(s, func() { st.pump(d) })
	sameLines(t, "the rest and the fin", segments(s, t1), []string{
		"n1 c1 c2s data seq=4 len=100 attempt=1: sent",
		"n1 c1 c2s data seq=5 len=50 attempt=1: sent",
		"n1 c1 c2s fin seq=6 len=0 attempt=1: sent",
	})
	if !d.finSent || d.nextSeq != 7 || len(d.queued) != 0 || d.inflight != 150 || !bytes.HasPrefix(d.unacked[3].data, []byte("0123456789abcdef")) || d.windowFull {
		t.Fatalf("after the fin: finSent %v, nextSeq %d, inflight %d, windowFull %v", d.finSent, d.nextSeq, d.inflight, d.windowFull)
	}

	// The fin needs no window; without finPending there is none.
	p = streamPair(t, Config{MSS: 100, RecvBuffer: 450})
	s, st, cli = p.s, p.st, p.cli
	d = cli.c.dirs[c2s]
	d.buf = make([]byte, 450)
	t2 := s.Now()
	at(s, func() { st.pump(d) })
	d.finPending = true
	at(s, func() { st.pump(d) })
	sameLines(t, "fin with a full window", segments(s, t2), []string{"n1 c1 c2s fin seq=1 len=0 attempt=1: sent"})

	// A sender that is not live sends nothing.
	for _, kill := range []func(*Stack, *Conn){
		func(_ *Stack, ep *Conn) { ep.errno = syscall.ECONNRESET },
		func(st *Stack, ep *Conn) { st.removeEndpoint(ep) },
	} {
		p := streamPair(t, Config{})
		s, st, cli, srv := p.s, p.st, p.cli, p.srv
		kill(st, srv)
		d := cli.c.dirs[s2c]
		d.queued, d.finPending = []byte("abc"), true
		t3 := s.Now()
		at(s, func() { st.pump(d) })
		if got := segments(s, t3); len(got) != 0 || len(d.queued) != 3 || d.finSent {
			t.Fatalf("pump of a sender that is not live: %q", got)
		}
	}
}

// schedulePump pumps at once for an up sender, posts net.pump to a paused sender's node, which runs
// at the resume, and does nothing for a sender that is not live.
func TestSchedulePump(t *testing.T) {
	p := streamPair(t, Config{})
	s, st, cli, srv := p.s, p.st, p.cli, p.srv
	d := cli.c.dirs[s2c] // b sends
	d.queued = []byte("abc")
	t0 := s.Now()
	at(s, func() { st.schedulePump(d) })
	sameLines(t, "up", segments(s, t0), []string{"n2 c1 s2c data seq=1 len=3 attempt=1: sent"})
	d.unacked = nil // no retransmissions below

	d.queued = []byte("de")
	at(s, srv.h.n.Pause)
	t1 := s.Now()
	at(s, func() { st.schedulePump(d) })
	if got := segments(s, t1); len(got) != 0 {
		t.Fatalf("pump ran on a paused node: %q", got)
	}
	s.RunFor(time.Second)
	resumed := s.Now()
	at(s, srv.h.n.Resume)
	sameLines(t, "after the resume", segments(s, t1), []string{"n2 c1 s2c data seq=2 len=2 attempt=1: sent"})
	var pumps []string // the deferred request is labelled net.pump and runs at the resume
	for _, r := range s.Records() {
		if r.At >= t1 && r.Node == srv.h.id && r.Text == "net.pump" {
			pumps = append(pumps, fmt.Sprintf("%s at %v", r.Kind, time.Duration(r.At-t1)))
		}
	}
	sameLines(t, "net.pump on b", pumps, []string{"kernel.defer at 0s", fmt.Sprintf("kernel.event at %v", time.Duration(resumed-t1))})

	d.queued = []byte("f")
	srv.errno = syscall.ETIMEDOUT
	t2 := s.Now()
	at(s, func() { st.schedulePump(d) })
	if got := segments(s, t2); len(got) != 0 {
		t.Fatalf("pump for a sender that is not live: %q", got)
	}
	srv.errno = nil
	at(s, srv.h.n.Pause)
	srv.errno = syscall.ETIMEDOUT
	at(s, func() { st.schedulePump(d) })
	srv.errno = nil
	at(s, srv.h.n.Resume)
	if got := segments(s, t2); len(got) != 0 {
		t.Fatalf("a paused sender that was not live got a pump: %q", got)
	}
}

// Read's checks in NSM-040's order, its error forms and partial reads.
func TestReadChecks(t *testing.T) {
	p := streamPair(t, Config{})
	d, cli := p.d, p.cli
	r := cli.c.dirs[s2c]
	past := time.Unix(1, 0)
	read := func(n int) (string, error) {
		b := make([]byte, n)
		k, err := cli.Read(b)
		return string(b[:k]), err
	}
	expect := func(what string, n int, data string, is error) {
		t.Helper()
		got, err := read(n)
		if got != data || !errors.Is(err, is) || (is == nil) != (err == nil) {
			t.Fatalf("%s: %q, %v; want %q, %v", what, got, err, data, is)
		}
		if is == nil || is == io.EOF {
			if err != is {
				t.Fatalf("%s: %v is wrapped", what, err)
			}
			return
		}
		oe := asOpErr(t, what, err)
		if oe.Op != "read" || oe.Net != "tcp" || addrString(oe.Source) != "10.0.0.1:49152" || addrString(oe.Addr) != "10.0.0.2:80" {
			t.Fatalf("%s: %#v", what, oe)
		}
	}

	cli.closed, cli.rdDL, cli.rdShut, cli.errno, r.eof, r.buf = true, past, true, syscall.ECONNRESET, true, []byte("x")
	expect("closed first", 1, "", net.ErrClosed)
	cli.closed = false
	expect("then the deadline", 0, "", os.ErrDeadlineExceeded)
	cli.rdDL = time.Time{}
	expect("then an empty buffer", 0, "", nil)
	expect("then CloseRead", 1, "", io.EOF)
	cli.rdShut = false
	expect("then buffered data", 1, "x", nil)
	expect("then the error state", 1, "", syscall.ECONNRESET)
	var se *os.SyscallError
	if _, err := read(1); !errors.As(err, &se) || se.Syscall != "read" {
		t.Fatalf("ECONNRESET not in a read SyscallError: %v", err)
	}
	cli.errno = syscall.ETIMEDOUT
	expect("a timed-out endpoint", 1, "", syscall.ETIMEDOUT)
	cli.errno = ErrProcessGone
	expect("a gone process", 1, "", ErrProcessGone)
	cli.errno = nil
	expect("then eof", 1, "", io.EOF)
	r.eof = false
	cli.rdDL = time.Now().Add(time.Hour)
	r.buf = []byte("hello")
	expect("a future deadline", 3, "hel", nil)
	expect("the rest", 10, "lo", nil)
	if len(r.buf) != 0 || len(d.posts) != 0 {
		t.Fatalf("buffer %q, posts %v", r.buf, postLines(d, 0))
	}
}

// A Read that takes bytes posts net.window on the reader's node while the peer's last pump stopped
// at the receiver window and no window request is pending; the request pumps the peer's direction.
// Bytes queued after the peer's last pump post nothing: their own net.pump request is pending.
func TestReadWindowRequest(t *testing.T) {
	p := streamPair(t, Config{MSS: 100, RecvBuffer: 200})
	s, d, cli, srv := p.s, p.d, p.cli, p.srv
	r := cli.c.dirs[s2c]
	r.buf = []byte("abcdefg")
	b := make([]byte, 1)
	_, _ = cli.Read(b) // nothing queued
	r.queued = bytes.Repeat([]byte("q"), 300)
	_, _ = cli.Read(b) // queued after the last pump
	if len(d.posts) != 0 {
		t.Fatalf("posts for bytes queued after the last pump: %v", postLines(d, 0))
	}
	r.windowFull = true
	_, _ = cli.Read(b)
	_, _ = cli.Read(b)
	sameLines(t, "posts", postLines(d, 0), []string{"n1/1 net.window tcp:c1/cli"})
	if !cli.windowPending || srv.windowPending {
		t.Fatal("windowPending not set on the reader")
	}
	d.posts[0].cancel()
	if cli.windowPending {
		t.Fatal("cancel left windowPending set")
	}
	_, _ = cli.Read(b)
	sameLines(t, "posts after the cancel", postLines(d, 1), []string{"n1/1 net.window tcp:c1/cli"})
	t0 := s.Now()
	at(s, d.posts[1].run)
	if cli.windowPending {
		t.Fatal("run left windowPending set")
	}
	// 2 bytes are still unread: the first segment fits (2+100), the second does not (100+2+100).
	sameLines(t, "pumped", segments(s, t0), []string{"n2 c1 s2c data seq=1 len=100 attempt=1: sent"})

	// The request goes through schedulePump: a paused peer pumps at its resume.
	r.inflight, r.unacked = 0, nil
	_, _ = cli.Read(b)
	at(s, srv.h.n.Pause)
	t1 := s.Now()
	at(s, d.posts[2].run)
	if got := segments(s, t1); len(got) != 0 {
		t.Fatalf("a paused peer pumped: %q", got)
	}
	at(s, srv.h.n.Resume)
	sameLines(t, "pumped at the resume", segments(s, t1), []string{"n2 c1 s2c data seq=2 len=100 attempt=1: sent"})
}

// One arrival can wake a Read and the peer's blocked Write together, and they take the lock in
// either order. Both orders must give the same requests in the same drain order (GOR-031), so the
// Write's bytes must not decide whether the Read posts net.window, and on a loopback connection,
// whose endpoints are on one node, the two endpoints' requests must not share a key.
func TestReadWindowRequestWakeOrder(t *testing.T) {
	for _, tc := range []struct {
		loopback bool
		full     bool
		want     []string
	}{
		{false, false, []string{"n2/1 net.pump tcp:c1/srv"}},
		{false, true, []string{"n1/1 net.window tcp:c1/cli", "n2/1 net.pump tcp:c1/srv"}},
		{true, false, []string{"n1/1 net.pump tcp:c1/srv"}},
		{true, true, []string{"n1/1 net.window tcp:c1/cli", "n1/1 net.pump tcp:c1/srv"}},
	} {
		loopback, full, want := tc.loopback, tc.full, tc.want
		for _, writeFirst := range []bool{false, true} {
			p := newStreamPair(t, Config{}, loopback)
			d, cli, srv := p.d, p.cli, p.srv
			r := cli.c.dirs[s2c]
			r.buf = []byte("data") // the arrival delivered these bytes and acknowledged the segment
			if full {
				r.queued, r.windowFull = []byte("left"), true
			}
			read := func() { _, _ = cli.Read(make([]byte, 4)) }
			write := func() { _, _ = srv.Write([]byte("more")) }
			if writeFirst {
				write()
				read()
			} else {
				read()
				write()
			}
			what := fmt.Sprintf("loopback %v, window full %v, Write first %v", loopback, full, writeFirst)
			sameLines(t, what, drainLines(d), want)
		}
	}
}

// Write's checks in NSM-042's order and its error forms; a Write that fits returns without the
// driver, copies b and posts one net.pump on the writer's node while none is pending.
func TestWriteChecks(t *testing.T) {
	p := streamPair(t, Config{MSS: 100, SendBuffer: 1000})
	s, d, cli, srv := p.s, p.d, p.cli, p.srv
	w := cli.c.dirs[c2s]
	expect := func(what string, b []byte, is error, syscallName string) {
		t.Helper()
		n, err := cli.Write(b)
		if n != 0 || !errors.Is(err, is) {
			t.Fatalf("%s: %d, %v; want %v", what, n, err, is)
		}
		oe := asOpErr(t, what, err)
		if oe.Op != "write" || oe.Net != "tcp" || addrString(oe.Source) != "10.0.0.1:49152" || addrString(oe.Addr) != "10.0.0.2:80" {
			t.Fatalf("%s: %#v", what, oe)
		}
		var se *os.SyscallError
		if syscallName != "" && (!errors.As(err, &se) || se.Syscall != syscallName) {
			t.Fatalf("%s: %v not in a %s SyscallError", what, err, syscallName)
		}
	}
	cli.closed, cli.wrDL, cli.errno, cli.wrShut = true, time.Unix(1, 0), syscall.ECONNRESET, true
	expect("closed first", []byte("a"), net.ErrClosed, "")
	cli.closed = false
	expect("then the deadline", nil, os.ErrDeadlineExceeded, "")
	cli.wrDL = time.Now().Add(time.Hour)
	expect("then the error state", nil, syscall.ECONNRESET, "write")
	cli.errno = ErrProcessGone
	expect("a gone process", nil, ErrProcessGone, "")
	cli.errno = nil
	expect("then CloseWrite", nil, syscall.EPIPE, "write")
	cli.wrShut = false
	if len(w.queued) != 0 || len(d.posts) != 0 {
		t.Fatalf("a failed Write queued %d bytes, posts %v", len(w.queued), postLines(d, 0))
	}

	if n, err := cli.Write(nil); n != 0 || err != nil || len(d.posts) != 0 {
		t.Fatalf("empty Write: %d, %v, posts %v", n, err, postLines(d, 0))
	}
	w.inflight, w.queued = 600, bytes.Repeat([]byte("q"), 300)
	b := []byte("0123456789")
	b = append(b, bytes.Repeat([]byte("z"), 90)...)
	if n, err := cli.Write(b); n != 100 || err != nil {
		t.Fatalf("a Write that fills the send buffer exactly: %d, %v", n, err)
	}
	b[0] = 'X'
	if len(w.queued) != 400 || !bytes.HasPrefix(w.queued[300:], []byte("0123456789")) {
		t.Fatalf("queued %d bytes: %q", len(w.queued), w.queued[300:310])
	}
	w.inflight = 500
	if n, err := cli.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("second Write: %d, %v", n, err)
	}
	sameLines(t, "posts", postLines(d, 0), []string{"n1/1 net.pump tcp:c1/cli"})
	if !cli.pumpPending || srv.pumpPending {
		t.Fatal("pumpPending not set on the writer")
	}
	d.posts[0].cancel()
	if cli.pumpPending {
		t.Fatal("cancel left pumpPending set")
	}
	_, _ = cli.Write([]byte("d"))
	w.inflight = 0
	t0 := s.Now()
	at(s, d.posts[1].run)
	if cli.pumpPending || len(w.queued) != 0 || w.inflight != 404 {
		t.Fatalf("after the pump: pending %v, queued %d, inflight %d", cli.pumpPending, len(w.queued), w.inflight)
	}
	if got := segments(s, t0); len(got) != 5 || got[0] != "n1 c1 c2s data seq=1 len=100 attempt=1: sent" {
		t.Fatalf("pumped %q", got)
	}

	// The server side writes on s2c, into an empty send queue, which also gets a copy of b.
	bye := []byte("bye")
	if n, err := srv.Write(bye); n != 3 || err != nil {
		t.Fatalf("server Write: %d, %v", n, err)
	}
	bye[0] = 'X'
	if q := cli.c.dirs[s2c].queued; !bytes.Equal(q, []byte("bye")) {
		t.Fatalf("server Write queued %q", q)
	}
	sameLines(t, "server posts", postLines(d, 2), []string{"n2/1 net.pump tcp:c1/srv"})
}

// Calls on one Conn from several goroutines at once serialize on the driver lock (NSM-044); the
// race detector fails the test if one of them changes the endpoint without it. The peer's pump
// stopped at the window, so CloseRead and Read post window requests too.
func TestConcurrentConnCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cli := streamPair(t, Config{}).cli
		cli.c.dirs[s2c].queued, cli.c.dirs[s2c].windowFull = []byte("q"), true
		var wg sync.WaitGroup
		for _, f := range []func(){
			func() { _ = cli.CloseWrite() },
			func() { _ = cli.CloseRead() },
			func() { _, _ = cli.Write([]byte("x")) },
			func() { _, _ = cli.Read(make([]byte, 1)) },
			func() { _ = cli.SetDeadline(time.Time{}) },
		} {
			wg.Go(f)
		}
		wg.Wait()
	})
}

// A blocked Write takes send space as acknowledgments free it and returns the bytes accepted so
// far when CloseWrite or a passed deadline ends it; a blocked Read returns at CloseRead, data or
// an error state.
func TestBlockedStreamCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := streamPair(t, Config{MSS: 100, SendBuffer: 1000})
		d, cli := p.d, p.cli
		w := cli.c.dirs[c2s]
		type res struct {
			n   int
			err error
			at  time.Duration
		}
		start := time.Now()
		run := func(f func() (int, error)) <-chan res {
			ch := make(chan res, 1)
			go func() {
				n, err := f()
				ch <- res{n, err, time.Since(start)}
			}()
			synctest.Wait()
			return ch
		}
		blocked := func(ch <-chan res) {
			t.Helper()
			select {
			case r := <-ch:
				t.Fatalf("returned %d, %v", r.n, r.err)
			default:
			}
		}

		w.inflight = 1000
		ch := run(func() (int, error) { return cli.Write([]byte("abcde")) })
		blocked(ch)
		if len(d.posts) != 0 {
			t.Fatalf("a Write with no send space posted %v", postLines(d, 0))
		}
		d.Lock()
		w.inflight = 998
		cli.wake()
		d.Unlock()
		synctest.Wait()
		blocked(ch)
		if !bytes.Equal(w.queued, []byte("ab")) || len(d.posts) != 1 {
			t.Fatalf("after 2 bytes of space: queued %q, posts %v", w.queued, postLines(d, 0))
		}
		if err := cli.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		if r := <-ch; r.n != 2 || !errors.Is(r.err, syscall.EPIPE) {
			t.Fatalf("Write ended by CloseWrite: %d, %v", r.n, r.err)
		}

		p = streamPair(t, Config{MSS: 100, SendBuffer: 1000})
		d, cli = p.d, p.cli
		w = cli.c.dirs[c2s]
		w.queued = make([]byte, 1000)
		ch = run(func() (int, error) { return cli.Write([]byte("abc")) })
		d.Lock()
		w.queued = w.queued[:997]
		cli.wake()
		d.Unlock()
		if r := <-ch; r.n != 3 || r.err != nil {
			t.Fatalf("Write completed by freed space: %d, %v", r.n, r.err)
		}
		w.queued = make([]byte, 1000)
		t0 := time.Since(start)
		ch = run(func() (int, error) { return cli.Write([]byte("abc")) })
		if err := cli.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if r := <-ch; r.n != 0 || !errors.Is(r.err, os.ErrDeadlineExceeded) || r.at != t0 {
			t.Fatalf("Write ended by a passed deadline: %d, %v at %v", r.n, r.err, r.at-t0)
		}

		p = streamPair(t, Config{})
		d, cli = p.d, p.cli
		r := cli.c.dirs[s2c]
		read := func() (int, error) { return cli.Read(make([]byte, 10)) }
		ch = run(read)
		d.Lock()
		r.buf = []byte("hi")
		cli.wake()
		d.Unlock()
		if x := <-ch; x.n != 2 || x.err != nil {
			t.Fatalf("Read woken by data: %d, %v", x.n, x.err)
		}
		ch = run(read)
		d.Lock()
		cli.errno = syscall.ECONNRESET
		cli.wake()
		d.Unlock()
		if x := <-ch; !errors.Is(x.err, syscall.ECONNRESET) {
			t.Fatalf("Read woken by a reset: %v", x.err)
		}
		cli.errno = nil
		ch = run(read)
		if err := cli.CloseRead(); err != nil {
			t.Fatal(err)
		}
		if x := <-ch; x.n != 0 || x.err != io.EOF {
			t.Fatalf("Read woken by CloseRead: %d, %v", x.n, x.err)
		}
	})
}

// CloseWrite marks the side shut at once and posts net.shutdown, whose run queues the fin behind
// the queued bytes and records net.conn.close mode=shutdown; a second call does nothing.
func TestCloseWrite(t *testing.T) {
	p := streamPair(t, Config{})
	s, d, cli := p.s, p.d, p.cli
	w := cli.c.dirs[c2s]
	w.queued = []byte("tail")
	old := cli.notify
	if err := cli.CloseWrite(); err != nil || !cli.wrShut || !closed(old) || w.finPending {
		t.Fatalf("CloseWrite: %v, wrShut %v, woken %v, finPending %v", err, cli.wrShut, closed(old), w.finPending)
	}
	if err := cli.CloseWrite(); err != nil {
		t.Fatalf("second CloseWrite: %v", err)
	}
	sameLines(t, "posts", postLines(d, 0), []string{"n1/1 net.shutdown tcp:c1/cli"})
	d.posts[0].cancel()
	if w.finPending {
		t.Fatal("the cancel function set finPending")
	}
	t0 := s.Now()
	at(s, d.posts[0].run)
	got := recLines(s, t0, "net.conn.segment", "net.conn.close")
	want := []string{
		"0s n1 c1 c2s data seq=1 len=4 attempt=1: sent | conn=c1 dir=c2s kind=data seq=1 len=4 attempt=1 outcome=sent delay_ns=1000000",
		"0s n1 c1 c2s fin seq=2 len=0 attempt=1: sent | conn=c1 dir=c2s kind=fin seq=2 len=0 attempt=1 outcome=sent delay_ns=1000000",
		"0s n1 close c1: shutdown | conn=c1 mode=shutdown",
	}
	sameLines(t, "records", got, want)
	if !w.finPending || !w.finSent {
		t.Fatal("finPending or finSent not set")
	}
	n, err := cli.Write([]byte("more"))
	var se *os.SyscallError
	if n != 0 || !errors.Is(err, syscall.EPIPE) || !errors.As(err, &se) || se.Syscall != "write" {
		t.Fatalf("Write after CloseWrite: %d, %v", n, err)
	}
	cli.closed = true
	err = cli.CloseWrite()
	oe := asOpErr(t, "CloseWrite after Close", err)
	if !errors.Is(err, net.ErrClosed) || oe.Op != "close" || addrString(oe.Source) != "10.0.0.1:49152" || addrString(oe.Addr) != "10.0.0.2:80" || len(d.posts) != 1 {
		t.Fatalf("CloseWrite after Close: %#v", oe)
	}
}

// CloseRead discards the unread bytes but keeps the out-of-order buffer, wakes readers, posts a
// window request while the peer's last pump stopped at the window, and Read then returns io.EOF.
func TestCloseRead(t *testing.T) {
	p := streamPair(t, Config{})
	d, cli, srv := p.d, p.cli, p.srv
	r := cli.c.dirs[s2c]
	r.buf = []byte("unread")
	ooo := &segment{c: cli.c, dir: s2c, kind: kData, seq: 5, data: []byte("later")}
	r.ooo[5], r.oooBytes = ooo, 5
	old := cli.notify
	if err := cli.CloseRead(); err != nil || !cli.rdShut || r.buf != nil || !closed(old) || len(d.posts) != 0 {
		t.Fatalf("CloseRead: %v, rdShut %v, buf %q, woken %v, posts %v", err, cli.rdShut, r.buf, closed(old), postLines(d, 0))
	}
	if r.ooo[5] != ooo || r.oooBytes != 5 {
		t.Fatal("CloseRead dropped the out-of-order buffer")
	}
	if n, err := cli.Read(make([]byte, 4)); n != 0 || err != io.EOF {
		t.Fatalf("Read after CloseRead: %d, %v", n, err)
	}
	if n, err := srv.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("the peer's Write after CloseRead: %d, %v", n, err)
	}

	p = streamPair(t, Config{})
	d, cli = p.d, p.cli
	cli.c.dirs[s2c].queued, cli.c.dirs[s2c].windowFull = []byte("q"), true
	if err := cli.CloseRead(); err != nil {
		t.Fatal(err)
	}
	sameLines(t, "posts with a full peer window", postLines(d, 0), []string{"n1/1 net.window tcp:c1/cli"})
	cli.closed = true
	err := cli.CloseRead()
	oe := asOpErr(t, "CloseRead after Close", err)
	if !errors.Is(err, net.ErrClosed) || oe.Op != "close" || addrString(oe.Source) != "10.0.0.1:49152" || addrString(oe.Addr) != "10.0.0.2:80" {
		t.Fatalf("CloseRead after Close: %#v", oe)
	}
}

// An in-order arrival pumps the sender only when the receiver called CloseRead: then its bytes
// are discarded, the window opens and no Read will open it. The pump goes through schedulePump,
// so a paused sender's runs at the resume (NSM-034, NSM-037, NSM-048).
func TestArrivalAtReadShutEndpointPumps(t *testing.T) {
	p := streamPair(t, Config{MSS: 100, RecvBuffer: 200})
	s, st, cli, srv := p.s, p.st, p.cli, p.srv
	w := cli.c.dirs[c2s]
	w.queued = bytes.Repeat([]byte("x"), 600)
	t0 := s.Now()
	at(s, func() { st.pump(w) })
	s1, s2 := w.unacked[0], w.unacked[1]
	at(s, func() { st.arrive(srv.h.id, s1) })
	w.buf = nil // as a Read whose window request has not run yet
	at(s, func() { st.arrive(srv.h.id, s2) })
	sameLines(t, "a reading receiver", segments(s, t0), []string{
		"n1 c1 c2s data seq=1 len=100 attempt=1: sent",
		"n1 c1 c2s data seq=2 len=100 attempt=1: sent",
	})

	srv.rdShut, w.buf = true, nil
	at(s, func() { st.pump(w) })
	s3, s4 := w.unacked[0], w.unacked[1]
	t1 := s.Now()
	at(s, func() { st.arrive(srv.h.id, s3) })
	sameLines(t, "a read-shut receiver", segments(s, t1), []string{"n1 c1 c2s data seq=5 len=100 attempt=1: sent"})
	if len(w.buf) != 0 || w.inflight != 200 || len(w.queued) != 100 {
		t.Fatalf("after the arrival: buf %d, inflight %d, queued %d", len(w.buf), w.inflight, len(w.queued))
	}

	at(s, cli.h.n.Pause)
	t2 := s.Now()
	at(s, func() { st.arrive(srv.h.id, s4) })
	if got := segments(s, t2); len(got) != 0 {
		t.Fatalf("a paused sender transmitted: %q", got)
	}
	at(s, cli.h.n.Resume)
	sameLines(t, "after the resume", segments(s, t2), []string{"n1 c1 c2s data seq=6 len=100 attempt=1: sent"})
}

// The deadline setters set their deadlines only and replace the deadline-change channel; on a
// closed conn they fail with net.ErrClosed, Op set and the local address only, as package net.
func TestConnDeadlines(t *testing.T) {
	cli := streamPair(t, Config{}).cli
	t1, t2, t3 := time.Unix(10, 0), time.Unix(20, 0), time.Unix(30, 0)
	for _, tc := range []struct {
		name   string
		set    func(time.Time) error
		at     time.Time
		rd, wr time.Time
	}{
		{"SetDeadline", cli.SetDeadline, t1, t1, t1},
		{"SetReadDeadline", cli.SetReadDeadline, t2, t2, t1},
		{"SetWriteDeadline", cli.SetWriteDeadline, t3, t2, t3},
		{"SetDeadline zero", cli.SetDeadline, time.Time{}, time.Time{}, time.Time{}},
	} {
		old := cli.dlCh
		if err := tc.set(tc.at); err != nil || !cli.rdDL.Equal(tc.rd) || !cli.wrDL.Equal(tc.wr) || !closed(old) || closed(cli.dlCh) {
			t.Fatalf("%s: %v, read %v, write %v", tc.name, err, cli.rdDL, cli.wrDL)
		}
	}
	cli.closed = true
	for _, set := range []func(time.Time) error{cli.SetDeadline, cli.SetReadDeadline, cli.SetWriteDeadline} {
		old := cli.dlCh
		err := set(t1)
		oe := asOpErr(t, "set on a closed conn", err)
		if !errors.Is(err, net.ErrClosed) || oe.Op != "set" || oe.Net != "tcp" || oe.Source != nil || addrString(oe.Addr) != "10.0.0.1:49152" {
			t.Fatalf("set on a closed conn: %#v", oe)
		}
		if closed(old) || !cli.rdDL.IsZero() || !cli.wrDL.IsZero() {
			t.Fatal("a failed set changed the deadlines")
		}
	}
}
