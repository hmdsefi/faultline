// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// finish keeps the first result; removeDial removes one record and ignores a missing one.
func TestDialRecordFinish(t *testing.T) {
	dr := &dialRecord{done: make(chan struct{})}
	ep := &Conn{}
	dr.finish(ep, nil)
	dr.finish(nil, errAbandoned)
	if !dr.finished || dr.conn != ep || dr.err != nil || !closed(dr.done) {
		t.Fatalf("finish twice: conn %v, err %v", dr.conn, dr.err)
	}
	h := &Host{}
	d1, d2, d3 := &dialRecord{}, &dialRecord{}, &dialRecord{}
	h.dials = []*dialRecord{d1, d2, d3}
	h.removeDial(d2)
	h.removeDial(d2)
	if len(h.dials) != 2 || h.dials[0] != d1 || h.dials[1] != d3 {
		t.Fatalf("dials %v", h.dials)
	}
}

// The context errors mirror package net's.
func TestCtxErrors(t *testing.T) {
	te := ctxError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded))
	var ne net.Error
	tmp, ok := te.(interface{ Temporary() bool }) // as package net's error; net.Error's method is deprecated
	if te.Error() != "i/o timeout" || !errors.As(te, &ne) || !ne.Timeout() || !ok || !tmp.Temporary() ||
		!errors.Is(te, context.DeadlineExceeded) || !errors.Is(te, os.ErrDeadlineExceeded) || errors.Is(te, context.Canceled) {
		t.Errorf("deadline error %v", te)
	}
	ce := ctxError(context.Canceled)
	if ce.Error() != "operation was canceled" || !errors.Is(ce, context.Canceled) || errors.Is(ce, context.DeadlineExceeded) || errors.As(ce, &ne) {
		t.Errorf("cancel error %v", ce)
	}
}

// startDial refuses a dial to an event-style node before allocating anything, ignores a finished
// or abandoned record, fails with EADDRINUSE when no ephemeral port is free, and otherwise
// creates the dialing client endpoint, records the dial and sends the syn.
func TestStartDial(t *testing.T) {
	s, _, st, ha, hb := txStack(t, Config{}, "e")
	mk := func(h *Host, target string, dst kernel.NodeID, loop bool) *dialRecord {
		tp := netip.MustParseAddrPort(target)
		dr := &dialRecord{h: h, target: tp, loop: loop, dst: dst, key: "dial:" + tp.String(), done: make(chan struct{})}
		h.dials = append(h.dials, dr)
		return dr
	}
	t0 := s.Now()
	dr := mk(ha, "10.0.0.3:80", 3, false)
	at(s, func() { st.startDial(dr) })
	var se *os.SyscallError
	if !dr.finished || !errors.As(dr.err, &se) || se.Syscall != "connect" || se.Err != syscall.ECONNREFUSED ||
		len(ha.dials) != 0 || len(st.conns) != 0 || st.node(ha.id).tcpCtr != 0 {
		t.Fatalf("dial to an event-style node: %v, dials %d, conns %d", dr.err, len(ha.dials), len(st.conns))
	}
	sameLines(t, "refused", recLines(s, t0, "net.conn.refused", "net.conn.dial"), []string{"0s n1 refused e:80 | conn="})

	for _, set := range []func(*dialRecord){func(dr *dialRecord) { dr.abandoned = true }, func(dr *dialRecord) { dr.finished = true }} {
		dr := mk(ha, "10.0.0.2:80", hb.id, false)
		set(dr)
		at(s, func() { st.startDial(dr) })
		if len(st.conns) != 0 || dr.c != nil || len(ha.dials) != 0 {
			t.Fatalf("startDial of an abandoned or finished record: conns %d, dials %d", len(st.conns), len(ha.dials))
		}
	}

	if _, err := ha.Listen("tcp", ":0"); err != nil { // takes 49152
		t.Fatal(err)
	}
	t1 := s.Now()
	dr = mk(ha, "10.0.0.2:80", hb.id, false)
	at(s, func() { st.startDial(dr) })
	c := dr.c
	if c == nil || c.id != "c1" || c.dial != dr || c.srvPort != 80 || c.cliNode != ha.id || c.srvNode != hb.id || dr.finished || len(ha.dials) != 1 {
		t.Fatalf("connection %+v", c)
	}
	cli := c.cli
	if cli.state != stDialing || cli.side != 0 || cli.local.String() != "10.0.0.1:49153" || cli.remote.String() != "10.0.0.2:80" || !ha.holds(cli) {
		t.Fatalf("client endpoint: state %d, %s -> %s", cli.state, cli.local, cli.remote)
	}
	d := c.dirs[c2s]
	if len(d.unacked) != 1 || d.unacked[0].kind != kSyn || d.unacked[0].seq != 0 || !d.checkPending {
		t.Fatalf("syn not transmitted reliably: %v", d.unacked)
	}
	dr = mk(ha, "127.0.0.1:81", ha.id, true)
	at(s, func() { st.startDial(dr) })
	if l := dr.c.cli.local.String(); l != "127.0.0.1:49154" {
		t.Fatalf("loopback dial local address %s", l)
	}
	sameLines(t, "records", recLines(s, t1, "net.conn.dial", "net.conn.segment"), []string{
		"0s n1 dial c1 a:49153 -> b:80 | conn=c1 src=a:49153 dst=b:80",
		"0s n1 c1 c2s syn seq=0 len=0 attempt=1: sent | conn=c1 dir=c2s kind=syn seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000",
		"1ms n1 dial c2 127.0.0.1:49154 -> 127.0.0.1:81 | conn=c2 src=127.0.0.1:49154 dst=127.0.0.1:81",
		"1ms n1 c2 c2s syn seq=0 len=0 attempt=1: loopback | conn=c2 dir=c2s kind=syn seq=0 len=0 attempt=1 outcome=loopback delay_ns=10000",
	})

	for p := uint16(49152); ; p++ {
		hb.listeners[p] = &Listener{}
		if p == 65535 {
			break
		}
	}
	t2 := s.Now()
	dr = mk(hb, "10.0.0.1:80", ha.id, false)
	at(s, func() { st.startDial(dr) })
	if !errors.As(dr.err, &se) || se.Syscall != "bind" || se.Err != syscall.EADDRINUSE || len(hb.dials) != 0 ||
		len(st.conns) != 2 || st.node(hb.id).tcpCtr != 16384 || len(recLines(s, t2, "net.conn.dial")) != 0 {
		t.Fatalf("dial without a free port: %v, conns %d, counter %d", dr.err, len(st.conns), st.node(hb.id).tcpCtr)
	}
}

// abortDial fails only a dial that is still dialing with this record.
func TestAbortDial(t *testing.T) {
	s, _, st, ha, hb := txStack(t, Config{})
	start := func() *dialRecord {
		tp := netip.MustParseAddrPort("10.0.0.2:80")
		dr := &dialRecord{h: ha, target: tp, dst: hb.id, key: "dial:" + tp.String(), done: make(chan struct{})}
		ha.dials = append(ha.dials, dr)
		at(s, func() { st.startDial(dr) })
		return dr
	}
	st.abortDial(&dialRecord{h: ha}) // not started

	dr := start()
	dr.c.cli.state = stEstablished
	at(s, func() { st.abortDial(dr) })
	if dr.finished || dr.c.cli.state != stEstablished {
		t.Fatal("abortDial ended an established connection")
	}
	dr2 := start()
	dr2.c.dial = dr
	at(s, func() { st.abortDial(dr2) })
	if dr2.finished || dr2.c.cli.state != stDialing {
		t.Fatal("abortDial ended a connection that another record dials")
	}
	dr3 := start()
	at(s, func() { st.abortDial(dr3) })
	if !dr3.finished || dr3.err != errAbandoned || dr3.c.cli.state != stRemoved || dr3.c.dial != nil || len(ha.dials) != 2 {
		t.Fatalf("abortDial of a dialing connection: err %v, dials %d", dr3.err, len(ha.dials))
	}
}

// A TCP dial from the driver goroutine panics before it posts anything.
func TestDialOnDriver(t *testing.T) {
	s, _, st, ha, _ := txStack(t, Config{})
	defer func() {
		want := "netsim: blocking call Host.DialContext on the driver goroutine (body, a check or a kernel callback)"
		if v := recover(); v != want || len(ha.dials) != 0 || len(st.conns) != 0 || len(recLines(s, 0, "net.conn.dial")) != 0 {
			t.Fatalf("panic %v, dials %d", v, len(ha.dials))
		}
	}()
	_, _ = ha.DialContext(t.Context(), "tcp", "b:80")
}

// postDriver keeps posted requests for the test to run; its callers are never on the driver.
type postDriver struct {
	mu    sync.Mutex
	posts []posted
}

type posted struct {
	kind, key   string
	run, cancel func()
}

func (d *postDriver) Lock()                          { d.mu.Lock() }
func (d *postDriver) Unlock()                        { d.mu.Unlock() }
func (*postDriver) Alive(kernel.NodeID, uint32) bool { return true }
func (*postDriver) OnDriver() bool                   { return false }
func (d *postDriver) Post(_ kernel.NodeID, _ uint32, kind, key string, run, cancel func()) {
	d.posts = append(d.posts, posted{kind: kind, key: key, run: run, cancel: cancel})
}

// dialTCP waits for the dial, the context or the Dialer timeout, and maps each outcome.
func TestDialTCPWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := kernel.New(kernel.Config{Seed: 1})
		d := &postDriver{}
		st := New(s, simnet.New(s, simnet.Config{Default: simnet.Link{Latency: time.Millisecond}}), d, Config{})
		boot := func(n *kernel.Node) { st.Host(n) }
		a := s.AddNode("a", boot)
		s.AddNode("b", boot)
		s.RunFor(time.Millisecond)
		h := st.cur(a.ID())
		type res struct {
			c   net.Conn
			err error
			at  time.Duration
		}
		start := time.Now()
		dial := func(ctx context.Context, timeout time.Duration) <-chan res {
			ch := make(chan res, 1)
			go func() {
				c, err := h.dial(ctx, "tcp", "b:80", timeout)
				ch <- res{c, err, time.Since(start)}
			}()
			synctest.Wait()
			return ch
		}
		// fail releases the driver lock before it fails: a dialer blocked on the lock would keep the
		// bubble from ending, and the test would hang instead of failing.
		fail := func(format string, args ...any) {
			t.Helper()
			d.Unlock()
			t.Fatalf(format, args...)
		}
		var oe *net.OpError
		wrapped := func(err error) bool {
			return errors.As(err, &oe) && oe.Op == "dial" && oe.Net == "tcp" && oe.Addr.String() == "10.0.0.2:80"
		}

		// The dial completed before the process saw the cancel: it gets the connection.
		ctx, cancel := context.WithCancel(t.Context())
		ch := dial(ctx, 0)
		d.Lock()
		if len(d.posts) != 1 || d.posts[0].kind != "net.dial" || d.posts[0].key != "dial:10.0.0.2:80" || len(h.dials) != 1 {
			fail("posts %+v, dials %d", d.posts, len(h.dials))
		}
		d.posts[0].run()
		dr := h.dials[0]
		cancel()
		h.removeDial(dr)
		dr.finish(dr.c.cli, nil)
		d.Unlock()
		if r := <-ch; r.err != nil || r.c != dr.c.cli || len(d.posts) != 1 {
			t.Fatalf("dial completed, then cancelled: %v, %v", r.c, r.err)
		}

		// Cancelled while dialing: abandoned, and net.dial_abort ends the dial.
		ctx, cancel = context.WithCancel(t.Context())
		ch = dial(ctx, 0)
		d.Lock()
		d.posts[1].run()
		dr = h.dials[0]
		d.Unlock()
		cancel()
		if r := <-ch; !errors.Is(r.err, context.Canceled) || !wrapped(r.err) || !dr.abandoned || dr.finished {
			t.Fatalf("cancelled dial: %v", r.err)
		}
		d.Lock()
		if len(d.posts) != 3 || d.posts[2].kind != "net.dial_abort" || d.posts[2].key != "dial:10.0.0.2:80" {
			fail("posts %+v", d.posts)
		}
		d.posts[2].cancel()
		if dr.finished {
			fail("the abort's cancel function ended the dial")
		}
		d.posts[2].run()
		if !dr.finished || dr.err != errAbandoned || dr.c.cli.state != stRemoved || len(h.dials) != 0 {
			fail("after net.dial_abort: err %v, dials %d", dr.err, len(h.dials))
		}
		d.Unlock()

		// The Dialer timeout passes before net.dial runs: the record is dropped when it runs.
		t0 := time.Since(start)
		ch = dial(t.Context(), 50*time.Millisecond)
		r := <-ch
		var ne net.Error
		if r.at-t0 != 50*time.Millisecond || !errors.Is(r.err, os.ErrDeadlineExceeded) || !errors.As(r.err, &ne) || !ne.Timeout() || !wrapped(r.err) {
			t.Fatalf("Dialer timeout: %v after %v", r.err, r.at-t0)
		}
		d.Lock()
		conns := len(st.conns)
		d.posts[3].run()
		d.posts[4].run()
		if len(st.conns) != conns || len(h.dials) != 0 {
			fail("an abandoned dial started: conns %d, dials %d", len(st.conns), len(h.dials))
		}
		d.Unlock()

		// The dial fails first: its error comes back wrapped.
		ch = dial(t.Context(), time.Hour)
		d.Lock()
		d.posts[5].run()
		st.failDial(h.dials[0].c, &os.SyscallError{Syscall: "connect", Err: syscall.ETIMEDOUT}, false)
		d.Unlock()
		if r := <-ch; !errors.Is(r.err, syscall.ETIMEDOUT) || !wrapped(r.err) {
			t.Fatalf("failed dial: %v", r.err)
		}

		// net.dial cancelled (the incarnation ended before it ran): ErrProcessGone.
		ch = dial(t.Context(), 0)
		d.Lock()
		d.posts[6].cancel()
		d.Unlock()
		if r := <-ch; !errors.Is(r.err, ErrProcessGone) || !wrapped(r.err) || len(h.dials) != 0 {
			t.Fatalf("cancelled net.dial: %v", r.err)
		}

		// The dial fails while the cancel is seen: the context error, and net.dial_abort is posted
		// anyway because the dial did not complete successfully.
		ctx, cancel = context.WithCancel(t.Context())
		ch = dial(ctx, 0)
		d.Lock()
		d.posts[7].run()
		dr = h.dials[0]
		cancel()
		st.failDial(dr.c, &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}, true)
		d.Unlock()
		if r := <-ch; !errors.Is(r.err, context.Canceled) || !wrapped(r.err) || len(d.posts) != 9 || d.posts[8].kind != "net.dial_abort" {
			t.Fatalf("dial failed while cancelled: %v, posts %d", r.err, len(d.posts))
		}
		d.Lock()
		d.posts[8].run()
		d.Unlock()
		if dr.err == nil || errors.Is(dr.err, errAbandoned) {
			t.Fatalf("the abort changed a finished dial: %v", dr.err)
		}

		// A gone host fails at once, after a done context is checked.
		h.gone = true
		if _, err := h.DialContext(t.Context(), "tcp", "b:80"); !errors.Is(err, ErrProcessGone) || !wrapped(err) {
			t.Fatalf("dial of a gone host: %v", err)
		}
		ctx, cancel = context.WithCancel(t.Context())
		cancel()
		if _, err := h.DialContext(ctx, "tcp", "b:80"); !errors.Is(err, context.Canceled) || !wrapped(err) || len(d.posts) != 9 {
			t.Fatalf("dial of a gone host with a done context: %v", err)
		}
	})
}
