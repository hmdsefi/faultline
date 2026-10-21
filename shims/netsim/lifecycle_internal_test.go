// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// texts returns "n<node> <text>" of the records of the given kinds emitted at or after t0.
func texts(s *kernel.Sim, t0 kernel.Time, kinds ...string) []string {
	var out []string
	for _, r := range s.Records() {
		for _, k := range kinds {
			if r.At >= t0 && r.Kind == k {
				out = append(out, fmt.Sprintf("n%d %s", r.Node, r.Text))
			}
		}
	}
	return out
}

var lifecycleKinds = []string{"net.conn.listen", "net.conn.segment", "net.conn.close", "net.conn.reset", "net.conn.rst",
	"net.conn.unlisten", "net.udp.bind", "net.udp.close", "net.send"}

// Conn.Close marks the endpoint closed, wakes it and posts net.close; closeRun sends the queued
// bytes and a fin and keeps the endpoint until its direction is acknowledged, resets it when
// received bytes are unread, removes it at once when CloseWrite's fin was acknowledged already, and
// does nothing for an endpoint that is not live (NSM-045).
func TestConnClose(t *testing.T) {
	t.Run("fin", func(t *testing.T) {
		p := streamPair(t, Config{})
		s, d, st, cli := p.s, p.d, p.st, p.cli
		w := cli.c.dirs[c2s]
		w.queued = []byte("tail")
		old := cli.notify
		if err := cli.Close(); err != nil || !cli.closed || !closed(old) || w.finPending {
			t.Fatalf("Close: %v, closed %v, woken %v, finPending %v", err, cli.closed, closed(old), w.finPending)
		}
		var oe *net.OpError
		if err := cli.Close(); !errors.As(err, &oe) || oe.Op != "close" || oe.Net != "tcp" || !errors.Is(err, net.ErrClosed) ||
			oe.Source.String() != "10.0.0.1:49152" || oe.Addr.String() != "10.0.0.2:80" {
			t.Fatalf("second Close: %v", err)
		}
		sameLines(t, "posts", postLines(d, 0), []string{"n1/1 net.close tcp:c1/cli"})
		d.posts[0].cancel()
		if len(texts(s, 0, lifecycleKinds...)) != 0 {
			t.Fatal("the cancel function sent or recorded something")
		}
		t0 := s.Now()
		at(s, d.posts[0].run)
		sameLines(t, "records", texts(s, t0, lifecycleKinds...), []string{
			"n1 c1 c2s data seq=1 len=4 attempt=1: sent",
			"n1 c1 c2s fin seq=2 len=0 attempt=1: sent",
			"n1 close c1: fin",
		})
		if !st.live(cli) || !cli.h.holds(cli) {
			t.Fatal("the endpoint went before its direction was acknowledged")
		}
		for len(w.unacked) > 0 {
			st.ack(w, w.unacked[0])
		}
		if st.live(cli) || cli.h.holds(cli) {
			t.Fatal("the endpoint stayed after its fin was acknowledged")
		}
	})
	t.Run("unread data", func(t *testing.T) {
		p := streamPair(t, Config{})
		s, d, st, srv := p.s, p.d, p.st, p.srv
		srv.c.dirs[c2s].buf = []byte("abc")
		srv.c.dirs[s2c].queued = []byte("lost")
		_ = srv.Close()
		t0 := s.Now()
		at(s, d.posts[0].run)
		sameLines(t, "records", texts(s, t0, lifecycleKinds...), []string{
			"n2 c1 s2c rst seq=0 len=0 attempt=1: sent",
			"n2 reset c1: abort",
			"n2 close c1: rst",
		})
		if st.live(srv) || srv.h.holds(srv) || !errors.Is(srv.errno, syscall.ECONNRESET) || len(srv.c.dirs[s2c].queued) != 0 {
			t.Fatalf("after the rst: live %v, errno %v", st.live(srv), srv.errno)
		}
	})
	t.Run("unread out-of-order data", func(t *testing.T) {
		p := streamPair(t, Config{})
		s, d, srv := p.s, p.d, p.srv
		srv.c.dirs[c2s].oooBytes = 3
		_ = srv.Close()
		t0 := s.Now()
		at(s, d.posts[0].run)
		sameLines(t, "records", texts(s, t0, "net.conn.close"), []string{"n2 close c1: rst"})
	})
	t.Run("after an acknowledged CloseWrite", func(t *testing.T) {
		p := streamPair(t, Config{})
		s, d, st, cli := p.s, p.d, p.st, p.cli
		w := cli.c.dirs[c2s]
		_ = cli.CloseWrite()
		at(s, d.posts[0].run)
		st.ack(w, w.unacked[0])
		if !st.live(cli) {
			t.Fatal("CloseWrite's acknowledged fin removed the endpoint")
		}
		_ = cli.Close()
		t0 := s.Now()
		at(s, d.posts[1].run)
		sameLines(t, "records", texts(s, t0, lifecycleKinds...), []string{"n1 close c1: fin"})
		if st.live(cli) || cli.h.holds(cli) {
			t.Fatal("a closed endpoint with nothing left to acknowledge stayed")
		}
	})
	t.Run("not live", func(t *testing.T) {
		p := streamPair(t, Config{})
		s, d, cli := p.s, p.d, p.cli
		cli.errno = syscall.ECONNRESET
		if err := cli.Close(); err != nil {
			t.Fatalf("Close of a reset endpoint: %v", err)
		}
		t0 := s.Now()
		at(s, d.posts[0].run)
		if got := texts(s, t0, lifecycleKinds...); len(got) != 0 {
			t.Fatalf("records %q", got)
		}
	})
}

// Detach closes everything at once without records or simnet calls and takes over the pending
// requests other than dials; the driver's run and cancel of a taken request do nothing. Release
// then runs the taken requests in (key, posting) order, and closes the listeners, endpoints and UDP
// sockets in ascending order (NSM-061).
func TestDetachAndRelease(t *testing.T) {
	p := streamPair(t, Config{})
	s, d, st, cli := p.s, p.d, p.st, p.cli
	ha := cli.h
	ln, err := ha.Listen("tcp", ":7")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := ln.(*Listener)
	pc, err := ha.ListenPacket("udp", ":9")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := pc.(*UDPConn)
	if _, err := cli.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := u.WriteTo([]byte("dg"), net.UDPAddrFromAddrPort(netip.MustParseAddrPort("10.0.0.2:5"))); err != nil {
		t.Fatal(err)
	}
	dialRan := false
	dr := &dialRecord{h: ha, done: make(chan struct{})}
	ha.dials = append(ha.dials, dr)
	st.post(ha, "net.dial", "dial:10.0.0.2:80", func() { dialRan = true }, func() { ha.removeDial(dr); dr.finish(nil, ErrProcessGone) })
	sameLines(t, "posts", postLines(d, 0), []string{"n1/1 net.listen listen:7", "n1/1 net.udp.bind udp:9", "n1/1 net.pump tcp:c1/cli",
		"n1/1 net.udp.send udp:9", "n1/1 net.dial dial:10.0.0.2:80"})

	n0 := len(s.Records())
	cn, ln0, un := cli.notify, l.notify, u.notify
	st.Detach(ha)
	if len(s.Records()) != n0 {
		t.Fatalf("Detach recorded %v", s.Records()[n0:])
	}
	switch {
	case !ha.gone || ha.alive():
		t.Fatal("host not gone")
	case !l.closed || ha.listeners[7] != nil || len(ha.lports) != 0 || !closed(ln0):
		t.Fatal("listener not closed, unbound and woken")
	case !cli.closed || !cli.exitClose || !closed(cn):
		t.Fatal("endpoint not closed and woken")
	case !u.closed || ha.udps[9] != nil || len(ha.uports) != 0 || !closed(un):
		t.Fatal("UDP socket not closed, unbound and woken")
	case !dr.finished || dr.err != ErrProcessGone || len(ha.dials) != 0:
		t.Fatalf("dial record: finished %v, err %v", dr.finished, dr.err)
	case len(ha.reqs) != 1 || ha.reqs[0].kind != "net.dial":
		t.Fatalf("requests left with the host: %d", len(ha.reqs))
	}
	var held []string
	for _, r := range ha.held {
		held = append(held, r.kind+" "+r.key)
	}
	sameLines(t, "held", held, []string{"net.listen listen:7", "net.pump tcp:c1/cli", "net.udp.bind udp:9", "net.udp.send udp:9"})
	d.posts[2].run()
	d.posts[2].cancel()
	if len(s.Records()) != n0 || !cli.pumpPending {
		t.Fatal("the driver ran or cancelled a request Detach took over")
	}
	if _, err := cli.Write([]byte("y")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ghost Write: %v", err)
	}
	if _, err := u.WriteTo([]byte("y"), u.LocalAddr()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ghost WriteTo: %v", err)
	}
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ghost Accept: %v", err)
	}

	t0 := s.Now()
	at(s, func() { st.Release(ha) })
	sameLines(t, "records", texts(s, t0, lifecycleKinds...), []string{
		"n1 listen :7",
		"n1 c1 c2s data seq=1 len=1 attempt=1: sent",
		"n1 udp bind :9",
		"n1 send #2 a -> b: udp 10.0.0.1:9 -> 10.0.0.2:5 len=2",
		"n1 unlisten :7: exit",
		"n1 c1 c2s fin seq=2 len=0 attempt=1: sent",
		"n1 close c1: fin",
		"n1 udp close :9: exit",
	})
	if cli.pumpPending || cli.exitClose || len(ha.held) != 0 || len(ha.exitLs) != 0 || len(ha.exitUs) != 0 || dialRan {
		t.Fatal("Release left work behind or ran the dial")
	}
	t1 := s.Now()
	at(s, func() { st.Release(ha) })
	if got := texts(s, t1, lifecycleKinds...); len(got) != 0 {
		t.Fatalf("a second Release recorded %q", got)
	}
}

// A host tracks the requests that have neither run nor been cancelled in posting order, whatever
// order the driver finishes the others in: Detach takes over exactly the unfinished ones, Release
// runs each of them once, and the list stays short when only the oldest request is left. The
// host's finished count always equals the finished requests still in its list, so a compaction
// starts the count again and the list is compacted once per halving, not on every drop.
func TestRequestTracking(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	d := &postDriver{}
	st := New(s, simnet.New(s, simnet.DefaultConfig()), d, Config{})
	boot := func(n *kernel.Node) { st.Host(n) }
	a := s.AddNode("a", boot)
	b := s.AddNode("b", boot)
	s.RunFor(time.Millisecond)
	counted := func(phase string, h *Host) {
		t.Helper()
		done := 0
		for _, r := range h.reqs {
			if r.done {
				done++
			}
		}
		if h.finished != done {
			t.Fatalf("%s: finished count %d, %d of %d tracked requests are done", phase, h.finished, done, len(h.reqs))
		}
	}

	ha := st.cur(a.ID())
	const n = 12
	ran := make([]int, n)
	for i := range n {
		st.post(ha, fmt.Sprintf("k%d", i), fmt.Sprintf("udp:%d", i%3), func() { ran[i]++ }, func() { ran[i] += 100 })
	}
	for _, i := range []int{5, 1, 4, 0} {
		d.posts[i].run()
	}
	for _, i := range []int{10, 7} {
		d.posts[i].cancel()
	}
	counted("before Detach", ha)
	st.Detach(ha)
	counted("after Detach", ha)
	var held []string
	for _, r := range ha.held {
		held = append(held, r.kind+" "+r.key)
	}
	sameLines(t, "held", held, []string{"k3 udp:0", "k6 udp:0", "k9 udp:0", "k2 udp:2", "k8 udp:2", "k11 udp:2"})
	if len(ha.reqs) != 0 {
		t.Fatalf("%d requests left with the host", len(ha.reqs))
	}
	d.posts[3].run()
	d.posts[6].cancel()
	st.Release(ha)
	counted("after Release", ha)
	if want := []int{1, 1, 1, 1, 1, 1, 1, 100, 1, 1, 100, 1}; !slices.Equal(ran, want) {
		t.Fatalf("runs and cancels %v, want %v", ran, want)
	}

	hb := st.cur(b.ID())
	first := len(d.posts)
	const m = 1000
	for range m {
		st.post(hb, "net.udp.send", "udp:9", func() {}, func() {})
	}
	for i := first + m - 1; i > first; i-- {
		d.posts[i].run()
		if i == first+m/2-1 { // the 501st finished request is more than half: one compaction
			counted("after the first compaction", hb)
			if len(hb.reqs) != m/2-1 {
				t.Fatalf("%d requests tracked after the first compaction, want %d", len(hb.reqs), m/2-1)
			}
		}
	}
	counted("with only the oldest unfinished", hb)
	if len(hb.reqs) > 3 || hb.reqs[0].done || slices.IndexFunc(hb.reqs[1:], func(r *request) bool { return !r.done }) >= 0 {
		t.Fatalf("%d requests tracked with only the oldest unfinished", len(hb.reqs))
	}
	d.posts[first].run()
	counted("after all finished", hb)
	if len(hb.reqs) != 0 {
		t.Fatalf("%d requests tracked after all finished", len(hb.reqs))
	}
}

// Release fails a dial whose net.dial request ran (NSM-025), and a held CloseWrite runs before the
// exit's close of the same endpoint.
func TestReleaseDialingAndCloseWrite(t *testing.T) {
	p := streamPair(t, Config{})
	s, d, st, cli := p.s, p.d, p.st, p.cli
	ha := cli.h
	dr := &dialRecord{h: ha, done: make(chan struct{})}
	ha.dials = append(ha.dials, dr)
	c2 := st.newConnection(ha.id, p.srv.h.id, 81, dr)
	dialing := newConn(c2, 0, ha, stDialing, netip.MustParseAddrPort("10.0.0.1:49153"), netip.MustParseAddrPort("10.0.0.2:81"))
	c2.cli = dialing
	ha.addEndpoint(dialing)
	_ = cli.CloseWrite()
	t0 := s.Now()
	at(s, func() { st.Release(ha) })
	sameLines(t, "records", texts(s, t0, lifecycleKinds...), []string{
		"n1 c1 c2s fin seq=1 len=0 attempt=1: sent",
		"n1 close c1: shutdown",
		"n1 close c1: fin",
	})
	if dialing.state != stRemoved || !dr.finished || !errors.Is(dr.err, ErrProcessGone) || len(d.posts) != 1 {
		t.Fatalf("dialing endpoint state %v, dial %v", dialing.state, dr.err)
	}
}

// Shutdown gives every endpoint without an error net.ErrClosed, closes listeners and sockets, fails
// pending dials and wakes every blocked call, with no record and no simnet call (NSM-062).
func TestShutdown(t *testing.T) {
	p := streamPair(t, Config{})
	s, st, cli, srv := p.s, p.st, p.cli, p.srv
	ha := cli.h
	ln, _ := ha.Listen("tcp", ":7")
	l, _ := ln.(*Listener)
	pc, _ := ha.ListenPacket("udp", ":9")
	u, _ := pc.(*UDPConn)
	dr := &dialRecord{h: ha, done: make(chan struct{})}
	ha.dials = append(ha.dials, dr)
	srv.errno = syscall.ECONNRESET
	cn, sn, ln0, un := cli.notify, srv.notify, l.notify, u.notify
	n0 := len(s.Records())
	st.Shutdown()
	switch {
	case len(s.Records()) != n0:
		t.Fatalf("Shutdown recorded %v", s.Records()[n0:])
	case !errors.Is(cli.errno, net.ErrClosed) || !errors.Is(srv.errno, syscall.ECONNRESET) || !closed(cn) || !closed(sn):
		t.Fatalf("endpoint errors %v, %v", cli.errno, srv.errno)
	case !l.closed || ha.listeners[7] != nil || !closed(ln0) || !u.closed || ha.udps[9] != nil || !closed(un):
		t.Fatal("listener or socket not closed and woken")
	case !dr.finished || dr.err != ErrProcessGone || len(ha.dials) != 0 || !ha.gone || !srv.h.gone:
		t.Fatal("dial not failed or host not gone")
	}
	if _, err := cli.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Read after Shutdown: %v", err)
	}
}

// The crash hook (NSM-060): step 1 walks the endpoints in (connection ID, side) order and leaves a
// queued endpoint of a bound listener to step 2, removes a half-open one silently, notifies the
// connected peer of any other endpoint and resets it, and fails the node's own dial; step 2 resets
// the queued endpoints without rst or notification; step 3 closes the UDP sockets. The peer resets
// one link latency later.
func TestCrashHook(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	nw := simnet.New(s, simnet.Config{Default: simnet.Link{Latency: time.Millisecond}})
	st := New(s, nw, &postDriver{}, Config{})
	boot := func(n *kernel.Node) { st.Host(n) }
	a := s.AddNode("a", boot)
	b := s.AddNode("b", boot)
	s.RunFor(time.Millisecond)
	ha, hb := st.cur(a.ID()), st.cur(b.ID())
	mute(nw, ha, hb)
	ln, _ := hb.Listen("tcp", ":80")
	l, _ := ln.(*Listener)
	if _, err := hb.ListenPacket("udp", ":9"); err != nil {
		t.Fatal(err)
	}
	pair := func(port uint16, cliState, srvState epState) (*Conn, *Conn) {
		c := st.newConnection(ha.id, hb.id, 80, nil)
		cl := newConn(c, 0, ha, cliState, netip.AddrPortFrom(ha.addr, port), netip.AddrPortFrom(hb.addr, 80))
		sv := newConn(c, 1, hb, srvState, cl.remote, cl.local)
		sv.lst = l
		c.cli, c.srv = cl, sv
		ha.addEndpoint(cl)
		hb.addEndpoint(sv)
		return cl, sv
	}
	queuedCli, queued := pair(49152, stEstablished, stEstablished) // c1: queued for Accept
	l.enqueue(queued)
	halfCli, half := pair(49153, stDialing, stSynRcvd) // c2: half-open
	l.synrcvd++
	otherCli, other := pair(49154, stEstablished, stEstablished) // c3: accepted
	dr := &dialRecord{h: hb, done: make(chan struct{})}          // c4: b's own dial
	c4 := st.newConnection(hb.id, ha.id, 99, dr)
	c4.cli = newConn(c4, 0, hb, stDialing, netip.MustParseAddrPort("10.0.0.2:49152"), netip.MustParseAddrPort("10.0.0.1:99"))
	hb.addEndpoint(c4.cli)
	queuedDial := &dialRecord{h: hb, done: make(chan struct{})} // its net.dial request has not run
	hb.dials = append(hb.dials, dr, queuedDial)

	t0 := s.Now()
	at(s, b.Crash)
	sameLines(t, "records", texts(s, t0, lifecycleKinds...), []string{
		"n2 crash rst c3 -> a",
		"n2 reset c3: crash",
		"n2 reset c1: crash",
		"n2 unlisten :80: crash",
		"n2 udp close :9: crash",
		"n1 reset c3: peer-crashed",
	})
	for _, r := range s.Records() {
		if r.Kind == "net.conn.rst" && r.At != t0 || r.Kind == "net.conn.reset" && r.Node == 1 && r.At != t0.Add(time.Millisecond) {
			t.Fatalf("record at the wrong time: %+v", r)
		}
	}
	switch {
	case half.state != stRemoved || l.synrcvd != 0 || len(l.queue) != 0 || !l.closed:
		t.Fatal("listener state after the crash")
	case !st.live(queuedCli) || !st.live(halfCli) || st.live(otherCli) || st.live(queued) || st.live(other):
		t.Fatal("live after the crash: only the clients of the queued and half-open endpoints stay")
	case c4.cli.state != stRemoved || !dr.finished || !errors.Is(dr.err, ErrProcessGone) || len(hb.dials) != 0 || !hb.gone ||
		!queuedDial.finished || queuedDial.err != ErrProcessGone:
		t.Fatal("b's own dial not failed")
	}
}

// Listener.Close frees the port at once, wakes Accept and posts net.unlisten; its run resets the
// queued endpoints with an rst, in FIFO order, removes the half-open ones without a record and
// records net.conn.unlisten with the number reset (NSM-049).
func TestListenerCloseRun(t *testing.T) {
	p := streamPair(t, Config{})
	s, d, st, hb := p.s, p.d, p.st, p.srv.h
	ln, _ := hb.Listen("tcp", ":80")
	l, _ := ln.(*Listener)
	srv := p.srv
	srv.lst = l
	l.enqueue(srv) // c1 is queued
	c2 := st.newConnection(p.cli.h.id, hb.id, 80, nil)
	half := newConn(c2, 1, hb, stSynRcvd, netip.MustParseAddrPort("10.0.0.2:80"), netip.MustParseAddrPort("10.0.0.1:49153"))
	half.lst = l
	c2.srv = half
	hb.addEndpoint(half)
	l.synrcvd++
	old := l.notify
	if err := l.Close(); err != nil || !l.closed || hb.listeners[80] != nil || len(hb.lports) != 0 || !closed(old) {
		t.Fatalf("Close: %v", err)
	}
	var oe *net.OpError
	if err := l.Close(); !errors.As(err, &oe) || oe.Op != "close" || oe.Addr.String() != "10.0.0.2:80" || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close: %v", err)
	}
	sameLines(t, "posts", postLines(d, 1), []string{"n2/1 net.unlisten listen:80"})
	t0 := s.Now()
	at(s, d.posts[1].run)
	sameLines(t, "records", texts(s, t0, lifecycleKinds...), []string{
		"n2 c1 s2c rst seq=0 len=0 attempt=1: sent",
		"n2 reset c1: listener-closed",
		"n2 unlisten :80: close",
	})
	if half.state != stRemoved || l.synrcvd != 0 || len(l.queue) != 0 || srv.state != stRemoved {
		t.Fatal("queued or half-open endpoint left")
	}
	for _, r := range s.Records() {
		if r.Kind == "net.conn.unlisten" && (attrValue(r, "port") != "80" || attrValue(r, "reason") != "close" || attrValue(r, "reset") != "1") {
			t.Fatalf("net.conn.unlisten %+v", r)
		}
	}
}

// A crash notification for an endpoint on a paused node waits for the node's resume (NSM-060).
func TestPeerCrashedPaused(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	nw := simnet.New(s, simnet.Config{Default: simnet.Link{Latency: time.Millisecond}})
	st := New(s, nw, &postDriver{}, Config{})
	boot := func(n *kernel.Node) { st.Host(n) }
	a := s.AddNode("a", boot)
	b := s.AddNode("b", boot)
	s.RunFor(time.Millisecond)
	ha, hb := st.cur(a.ID()), st.cur(b.ID())
	mute(nw, ha, hb)
	_, cli := clientConn(st, ha, hb)
	at(s, a.Pause)
	at(s, func() { st.peerCrashed(cli) })
	if !st.live(cli) {
		t.Fatal("the paused node's endpoint was reset")
	}
	t0 := s.Now()
	at(s, a.Resume)
	sameLines(t, "records", texts(s, t0, "net.conn.reset"), []string{"n1 reset c1: peer-crashed"})
	at(s, func() { st.peerCrashed(cli) }) // no longer live: nothing
	if n := len(texts(s, 0, "net.conn.reset")); n != 1 {
		t.Fatalf("%d resets", n)
	}
}
