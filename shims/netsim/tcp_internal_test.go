// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// tcpStack boots nodes a (10.0.0.1) and b (10.0.0.2) as hosts of a stack with a stub driver.
func tcpStack(t *testing.T) (*kernel.Sim, *stubDriver, *Stack, *kernel.Node, *kernel.Node) {
	t.Helper()
	s := kernel.New(kernel.Config{Seed: 1})
	d := &stubDriver{}
	st := New(s, simnet.New(s, simnet.DefaultConfig()), d, Config{})
	boot := func(n *kernel.Node) { st.Host(n) }
	a := s.AddNode("a", boot)
	b := s.AddNode("b", boot)
	s.RunFor(time.Millisecond)
	return s, d, st, a, b
}

func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Connections are numbered c1, c2, ... in creation order; directions, ends and endpoints map the
// client to side 0 and c2s; segments describe themselves as NSM §8 says.
func TestConnectionsAndSegments(t *testing.T) {
	_, _, st, a, b := tcpStack(t)
	ha, hb := st.cur(a.ID()), st.cur(b.ID())
	dr := &dialRecord{h: ha}
	c1 := st.newConnection(a.ID(), b.ID(), 80, dr)
	c2 := st.newConnection(b.ID(), a.ID(), 81, nil)
	if c1.k != 1 || c1.id != "c1" || c2.k != 2 || c2.id != "c2" || len(st.conns) != 2 || st.conns[0] != c1 || st.conns[1] != c2 {
		t.Fatalf("connections %+v %+v", c1, c2)
	}
	if c1.dial != dr || c2.dial != nil || c1.srvPort != 80 || c1.cliNode != a.ID() || c1.srvNode != b.ID() {
		t.Fatalf("c1 fields %+v", c1)
	}
	for dir := range 2 {
		d := c1.dirs[dir]
		if d.c != c1 || d.dir != dir || d.nextSeq != 1 || d.rcvNext != 1 || d.ooo == nil {
			t.Fatalf("direction %d: %+v", dir, d)
		}
	}
	if s, r := c1.ends(c2s); s != a.ID() || r != b.ID() {
		t.Errorf("ends(c2s) = %d, %d", s, r)
	}
	if s, r := c1.ends(s2c); s != b.ID() || r != a.ID() {
		t.Errorf("ends(s2c) = %d, %d", s, r)
	}
	cli := newConn(c1, 0, ha, stDialing, netip.MustParseAddrPort("10.0.0.1:49152"), netip.MustParseAddrPort("10.0.0.2:80"))
	srv := newConn(c1, 1, hb, stSynRcvd, cli.remote, cli.local)
	c1.cli, c1.srv = cli, srv
	if c1.endpoint(0) != cli || c1.endpoint(1) != srv ||
		c1.dirs[c2s].sender() != cli || c1.dirs[c2s].receiver() != srv ||
		c1.dirs[s2c].sender() != srv || c1.dirs[s2c].receiver() != cli {
		t.Fatal("endpoint, sender or receiver maps to the wrong side")
	}
	for _, tc := range []struct {
		s    *segment
		want string
	}{
		{&segment{c: c1, dir: c2s, kind: kSyn, seq: 1}, "tcp c1 c2s syn seq=1 len=0"},
		{&segment{c: c1, dir: s2c, kind: kSynAck, seq: 1}, "tcp c1 s2c synack seq=1 len=0"},
		{&segment{c: c2, dir: c2s, kind: kData, seq: 7, data: []byte("abc")}, "tcp c2 c2s data seq=7 len=3"},
		{&segment{c: c2, dir: s2c, kind: kFin, seq: 9}, "tcp c2 s2c fin seq=9 len=0"},
		{&segment{c: c1, dir: c2s, kind: kRst, seq: 4}, "tcp c1 c2s rst seq=4 len=0"},
	} {
		if got := kernel.Describe(tc.s); got != tc.want {
			t.Errorf("Describe = %q, want %q", got, tc.want)
		}
	}
}

// Conn reports its ID and addresses, wraps errnos per operation and wakes waiters by closing its
// state channel.
func TestConnBasics(t *testing.T) {
	_, _, st, a, b := tcpStack(t)
	ha := st.cur(a.ID())
	c := st.newConnection(a.ID(), b.ID(), 80, nil)
	ep := newConn(c, 0, ha, stDialing, netip.MustParseAddrPort("127.0.0.1:49152"), netip.MustParseAddrPort("127.0.0.1:80"))
	la, ok1 := ep.LocalAddr().(*net.TCPAddr)
	ra, ok2 := ep.RemoteAddr().(*net.TCPAddr)
	if ep.ID() != "c1" || !ok1 || !ok2 || la.String() != "127.0.0.1:49152" || ra.String() != "127.0.0.1:80" || len(la.IP) != 4 {
		t.Fatalf("ID %s, local %v, remote %v", ep.ID(), ep.LocalAddr(), ep.RemoteAddr())
	}
	var oe *net.OpError
	if err := ep.opErr("read", net.ErrClosed); !errors.As(err, &oe) || oe.Op != "read" || oe.Net != "tcp" ||
		oe.Source.String() != "127.0.0.1:49152" || oe.Addr.String() != "127.0.0.1:80" || !errors.Is(err, net.ErrClosed) {
		t.Errorf("opErr = %v", err)
	}
	var se *os.SyscallError
	ep.errno = syscall.ECONNRESET
	if err := ep.errFor("read"); !errors.As(err, &se) || se.Syscall != "read" || !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("errFor(read) with ECONNRESET = %v", err)
	}
	ep.errno = syscall.ETIMEDOUT
	if err := ep.errFor("write"); !errors.As(err, &se) || se.Syscall != "write" || !errors.Is(err, syscall.ETIMEDOUT) {
		t.Errorf("errFor(write) with ETIMEDOUT = %v", err)
	}
	for _, e := range []error{ErrProcessGone, net.ErrClosed, nil} {
		ep.errno = e
		if err := ep.errFor("read"); err != e { // the error comes back unwrapped
			t.Errorf("errFor with %v = %v", e, err)
		}
	}
	old := ep.notify
	ep.wake()
	if !closed(old) || closed(ep.notify) {
		t.Error("wake did not close the old channel and install an open one")
	}
}

// The host's endpoint table: endpoints in (connection ID, side) order, the ports Listen and
// ephemeral allocation see as used, sorted listener ports, and live.
func TestEndpointTables(t *testing.T) {
	s, _, st, a, b := tcpStack(t)
	ha, hb := st.cur(a.ID()), st.cur(b.ID())
	c1 := st.newConnection(a.ID(), b.ID(), 80, nil)
	c2 := st.newConnection(b.ID(), a.ID(), 49153, nil)
	srv := newConn(c2, 1, ha, stEstablished, netip.MustParseAddrPort("10.0.0.1:49153"), netip.MustParseAddrPort("10.0.0.2:49152"))
	cli := newConn(c1, 0, ha, stEstablished, netip.MustParseAddrPort("10.0.0.1:49152"), netip.MustParseAddrPort("10.0.0.2:80"))
	ha.addEndpoint(srv)
	ha.addEndpoint(cli)
	if !slices.Equal(ha.endpoints(), []*Conn{cli, srv}) || !ha.holds(cli) || !ha.holds(srv) ||
		ha.holds(newConn(c1, 1, ha, stEstablished, cli.local, cli.remote)) || ha.holds(newConn(c2, 0, ha, stEstablished, srv.local, srv.remote)) {
		t.Fatalf("endpoints %v, keys %v", ha.endpoints(), ha.eks)
	}
	if _, err := ha.Listen("tcp", ":7"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		port          uint16
		tcpUse, lsUse bool
	}{
		{7, true, true},       // listener
		{49152, true, true},   // client endpoint
		{49153, true, false},  // server endpoint: Listen may reuse it
		{80, false, false},    // the peer's port
		{49154, false, false}, // free
	} {
		if ha.tcpInUse(tc.port) != tc.tcpUse || ha.listenInUse(tc.port) != tc.lsUse {
			t.Errorf("port %d: tcpInUse %v, listenInUse %v", tc.port, ha.tcpInUse(tc.port), ha.listenInUse(tc.port))
		}
	}
	if _, err := ha.Listen("tcp", ":49152"); !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("Listen on a client endpoint's port: %v", err)
	}
	if l, err := ha.Listen("tcp", ":0"); err != nil || l.Addr().String() != "10.0.0.1:49154" || st.node(a.ID()).tcpCtr != 3 {
		t.Errorf("Listen(:0) skipping the endpoints' ports: %v, %v, counter %d", l, err, st.node(a.ID()).tcpCtr)
	}
	if l, err := ha.Listen("tcp", ":49153"); err != nil || l.Addr().String() != "10.0.0.1:49153" {
		t.Errorf("Listen on a server endpoint's port: %v", err)
	}
	if hb.tcpInUse(49152) || hb.tcpInUse(7) {
		t.Error("b sees a's ports as used")
	}
	if !slices.Equal(ha.lports, []uint16{7, 49153, 49154}) {
		t.Errorf("listener ports %v, want them ascending", ha.lports)
	}

	if !st.live(cli) || !st.live(srv) || st.live(nil) {
		t.Fatal("live of table endpoints or nil")
	}
	srv.state = stRemoved
	cli.errno = syscall.ECONNRESET
	if st.live(srv) || st.live(cli) {
		t.Error("live of a removed or reset endpoint")
	}
	ep := newConn(c1, 1, hb, stSynRcvd, cli.remote, cli.local)
	hb.addEndpoint(ep)
	at(s, b.Crash)
	at(s, b.Restart)
	if st.live(ep) {
		t.Error("live of an endpoint of an ended incarnation")
	}
}

// The accept queue is FIFO; Accept checks the closed and ghost states, then the deadline, then
// the queue; SetDeadline replaces the deadline-change channel.
func TestListenerQueue(t *testing.T) {
	_, d, st, a, b := tcpStack(t)
	ha := st.cur(a.ID())
	nl, err := ha.Listen("tcp", "localhost:80")
	if err != nil {
		t.Fatal(err)
	}
	l, ok := nl.(*Listener)
	if !ok || l.Addr().String() != "127.0.0.1:80" || ha.listeners[80] != l || len(ha.lports) != 1 || ha.lports[0] != 80 {
		t.Fatalf("listener %v, ports %v", nl, ha.lports)
	}
	c := st.newConnection(b.ID(), a.ID(), 80, nil)
	eps := make([]*Conn, 3)
	for i := range eps {
		eps[i] = newConn(c, 1, ha, stEstablished, netip.AddrPort{}, netip.AddrPort{})
	}
	old := l.notify
	l.enqueue(eps[0])
	if !closed(old) || closed(l.notify) || !eps[0].inAcceptQueue {
		t.Fatal("enqueue did not wake the listener or mark the endpoint")
	}
	l.enqueue(eps[1])
	l.enqueue(eps[2])
	l.dequeue(eps[1])
	l.dequeue(eps[1])
	if len(l.queue) != 2 || l.queue[0] != eps[0] || l.queue[1] != eps[2] || eps[1].inAcceptQueue {
		t.Fatalf("queue after dequeue: %v", l.queue)
	}
	if got, err := l.Accept(); err != nil || got != eps[0] || eps[0].inAcceptQueue {
		t.Fatalf("Accept = %v, %v", got, err)
	}

	oldDL := l.dlCh
	if err := l.SetDeadline(time.Unix(1, 0)); err != nil || !closed(oldDL) || closed(l.dlCh) {
		t.Fatalf("SetDeadline: %v", err)
	}
	var oe *net.OpError
	if _, err := l.Accept(); !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &oe) ||
		oe.Op != "accept" || oe.Net != "tcp" || oe.Addr.String() != "127.0.0.1:80" || len(l.queue) != 1 {
		t.Fatalf("Accept with a passed deadline and a queued connection: %v", err)
	}
	_ = l.SetDeadline(time.Time{})
	if got, err := l.Accept(); err != nil || got != eps[2] {
		t.Fatalf("Accept = %v, %v", got, err)
	}
	func() {
		defer func() {
			want := "netsim: blocking call Listener.Accept on the driver goroutine (body, a check or a kernel callback)"
			if v := recover(); v != want {
				t.Errorf("Accept on an empty queue on the driver: panic %v", v)
			}
		}()
		_, _ = l.Accept()
	}()

	l.enqueue(eps[1])
	l.closed = true
	l.dl = time.Unix(1, 0) // closed wins over a passed deadline, as in package net
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) || !errors.As(err, &oe) || oe.Op != "accept" {
		t.Errorf("Accept on a closed listener: %v", err)
	}
	if err := l.SetDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) || !errors.As(err, &oe) || oe.Op != "set" {
		t.Errorf("SetDeadline on a closed listener: %v", err)
	}

	nl2, err := ha.Listen("tcp", ":81")
	if err != nil {
		t.Fatal(err)
	}
	l2, ok := nl2.(*Listener)
	if !ok {
		t.Fatalf("Listen returned %T", nl2)
	}
	l2.enqueue(eps[0])
	d.dead = a.ID()
	if _, err := ha.Listen("tcp", ":82"); !errors.Is(err, ErrProcessGone) || !errors.As(err, &oe) || oe.Op != "listen" ||
		oe.Net != "tcp" || oe.Addr == nil || oe.Addr.String() != "10.0.0.1:82" {
		t.Errorf("Listen of a gone incarnation: %v", err)
	}
	if _, err := l2.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept of a gone incarnation: %v", err)
	}
	if err := l2.SetDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("SetDeadline of a gone incarnation: %v", err)
	}
}

// Both ends of a loopback connection share one host's table: the table keeps them apart, walks
// them in (connection ID, side) order and counts both ports as used.
func TestLoopbackEndpoints(t *testing.T) {
	_, _, st, a, b := tcpStack(t)
	ha := st.cur(a.ID())
	c1 := st.newConnection(a.ID(), a.ID(), 80, nil)
	c2 := st.newConnection(a.ID(), a.ID(), 81, nil)
	cli1 := newConn(c1, 0, ha, stEstablished, netip.MustParseAddrPort("127.0.0.1:49152"), netip.MustParseAddrPort("127.0.0.1:80"))
	srv1 := newConn(c1, 1, ha, stEstablished, cli1.remote, cli1.local)
	cli2 := newConn(c2, 0, ha, stDialing, netip.MustParseAddrPort("127.0.0.1:49153"), netip.MustParseAddrPort("127.0.0.1:81"))
	for _, ep := range []*Conn{srv1, cli2, cli1} {
		ha.addEndpoint(ep)
	}
	if got := ha.endpoints(); !slices.Equal(got, []*Conn{cli1, srv1, cli2}) {
		t.Fatalf("endpoints %v, want c1 client, c1 server, c2 client", got)
	}
	if !ha.holds(cli1) || !ha.holds(srv1) || !ha.holds(cli2) || ha.holds(newConn(c2, 1, ha, stSynRcvd, cli2.remote, cli2.local)) {
		t.Fatal("holds")
	}
	for _, tc := range []struct {
		port          uint16
		tcpUse, lsUse bool
	}{
		{49152, true, true}, // c1's client
		{80, true, false},   // c1's server: Listen may reuse it
		{49153, true, true}, // c2's client
		{81, false, false},  // nobody listens on c2's target
	} {
		if ha.tcpInUse(tc.port) != tc.tcpUse || ha.listenInUse(tc.port) != tc.lsUse {
			t.Errorf("port %d: tcpInUse %v, listenInUse %v", tc.port, ha.tcpInUse(tc.port), ha.listenInUse(tc.port))
		}
	}
	if _, err := ha.Listen("tcp", ":49152"); !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("Listen on the loopback client's port: %v", err)
	}
	if hb := st.cur(b.ID()); hb.tcpInUse(49152) || hb.tcpInUse(80) || len(hb.endpoints()) != 0 {
		t.Error("b sees a's loopback endpoints")
	}

	walk := ha.endpoints()
	ha.dropEndpoint(newConn(c1, 0, ha, stEstablished, cli1.local, cli1.remote)) // same key, another endpoint
	if !slices.Equal(ha.endpoints(), walk) {
		t.Fatalf("dropping an endpoint that is not in the table changed it: %v", ha.endpoints())
	}
	ha.dropEndpoint(cli1)
	ha.dropEndpoint(cli1)
	if !slices.Equal(walk, []*Conn{cli1, srv1, cli2}) || !slices.Equal(ha.endpoints(), []*Conn{srv1, cli2}) ||
		ha.holds(cli1) || !ha.holds(srv1) || ha.tcpInUse(49152) || !ha.tcpInUse(80) {
		t.Errorf("after dropping c1's client: walk %v, endpoints %v", walk, ha.endpoints())
	}
	if l, err := ha.Listen("tcp", ":49152"); err != nil || l.Addr().String() != "10.0.0.1:49152" {
		t.Errorf("Listen on the freed port: %v", err)
	}
}

// Listen(":0") fails with EADDRINUSE once every ephemeral port is in use, after 16384 tries.
func TestListenEphemeralExhausted(t *testing.T) {
	_, _, st, a, b := tcpStack(t)
	ha := st.cur(a.ID())
	p := uint16(49152)
	for range 16384 {
		ha.listeners[p] = &Listener{}
		p++
	}
	_, err := ha.Listen("tcp", "localhost:0")
	var oe *net.OpError
	var se *os.SyscallError
	if !errors.As(err, &oe) || oe.Op != "listen" || oe.Net != "tcp" || oe.Addr == nil || oe.Addr.String() != "127.0.0.1:0" ||
		!errors.As(err, &se) || se.Syscall != "bind" || !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("Listen(:0) with every ephemeral port in use: %v", err)
	}
	if n := st.node(a.ID()).tcpCtr; n != 16384 {
		t.Errorf("counter %d after exhaustion, want 16384", n)
	}
	if l, err := st.cur(b.ID()).Listen("tcp", ":0"); err != nil || l.Addr().String() != "10.0.0.2:49152" {
		t.Errorf("Listen(:0) on another node: %v", err)
	}
}

// txStack boots hosts a (10.0.0.1) and b (10.0.0.2), then the event-style nodes named in extra,
// with full tracing, 1 ms links without jitter and a stub driver.
func txStack(t *testing.T, cfg Config, extra ...string) (*kernel.Sim, *simnet.Network, *Stack, *Host, *Host) {
	t.Helper()
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	nw := simnet.New(s, simnet.Config{Default: simnet.Link{Latency: time.Millisecond}})
	st := New(s, nw, &stubDriver{}, cfg)
	boot := func(n *kernel.Node) { st.Host(n) }
	a := s.AddNode("a", boot)
	b := s.AddNode("b", boot)
	for _, name := range extra {
		s.AddNode(name, func(*kernel.Node) {})
	}
	s.RunFor(time.Millisecond)
	return s, nw, st, st.cur(a.ID()), st.cur(b.ID())
}

// clientConn makes a connection from ha to hb:80 whose established client endpoint is in ha's
// table at port 49152.
func clientConn(st *Stack, ha, hb *Host) (*connection, *Conn) {
	c := st.newConnection(ha.id, hb.id, 80, nil)
	cli := newConn(c, 0, ha, stEstablished, netip.AddrPortFrom(ha.addr, 49152), netip.AddrPortFrom(hb.addr, 80))
	c.cli = cli
	ha.addEndpoint(cli)
	return c, cli
}

// recLines renders the records of the given kinds emitted at or after t0 as
// "<at - t0> n<node> <text> | k=v ...", and every net.send_raw record among them as
// "send_raw delay_ns=<delay> fifo=<fifo>".
func recLines(s *kernel.Sim, t0 kernel.Time, kinds ...string) []string {
	var out []string
	for _, r := range s.Records() {
		if r.At < t0 || !slices.Contains(kinds, r.Kind) {
			continue
		}
		if r.Kind == "net.send_raw" {
			out = append(out, "send_raw delay_ns="+attrValue(r, "delay_ns")+" fifo="+attrValue(r, "fifo"))
			continue
		}
		kv := make([]string, len(r.Attrs))
		for i, a := range r.Attrs {
			kv[i] = a.Key + "=" + a.Value
		}
		out = append(out, fmt.Sprintf("%v n%d %s | %s", r.At.Sub(t0), r.Node, r.Text, strings.Join(kv, " ")))
	}
	return out
}

func attrValue(r kernel.Record, key string) string {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

func sameLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// Every outcome of an attempt, with its record and its bookkeeping; a sent or loopback attempt's
// net.send_raw record follows its segment record.
func TestAttemptOutcomes(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{Loopback: 7 * time.Microsecond})
	c, _ := clientConn(st, ha, hb)
	t0 := s.Now()
	data := &segment{c: c, dir: c2s, kind: kData, seq: 3, data: []byte("abc")}
	at(s, func() { st.attempt(data) })
	if data.attempts != 1 || data.first != t0 || data.last != t0 {
		t.Fatalf("after one attempt: attempts %d, first %v, last %v", data.attempts, data.first, data.last)
	}
	t1 := s.Now()
	at(s, func() { st.attempt(data) })
	if data.attempts != 2 || data.first != t0 || data.last != t1 {
		t.Fatalf("after two attempts: attempts %d, first %v, last %v", data.attempts, data.first, data.last)
	}
	at(s, func() { st.sendUnreliable(c, s2c, kSynAck) })
	at(s, func() { nw.Isolate(hb.id); st.attempt(data) })
	at(s, func() {
		nw.Heal()
		nw.SetLink(ha.id, hb.id, simnet.Link{Latency: time.Millisecond, DropPPM: simnet.MaxPPM})
		st.attempt(data)
	})
	loop := st.newConnection(ha.id, ha.id, 80, nil)
	at(s, func() { st.sendUnreliable(loop, c2s, kRst) })
	if data.attempts != 4 {
		t.Fatalf("attempts %d, want 4", data.attempts)
	}
	sameLines(t, "segment records", recLines(s, t0, "net.conn.segment", "net.send_raw"), []string{
		"0s n1 c1 c2s data seq=3 len=3 attempt=1: sent | conn=c1 dir=c2s kind=data seq=3 len=3 attempt=1 outcome=sent delay_ns=1000000",
		"send_raw delay_ns=1000000 fifo=false",
		"1ms n1 c1 c2s data seq=3 len=3 attempt=2: sent | conn=c1 dir=c2s kind=data seq=3 len=3 attempt=2 outcome=sent delay_ns=1000000",
		"send_raw delay_ns=1000000 fifo=false",
		"2ms n2 c1 s2c synack seq=0 len=0 attempt=1: sent | conn=c1 dir=s2c kind=synack seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000",
		"send_raw delay_ns=1000000 fifo=false",
		"3ms n1 c1 c2s data seq=3 len=3 attempt=3: held | conn=c1 dir=c2s kind=data seq=3 len=3 attempt=3 outcome=held delay_ns=0",
		"4ms n1 c1 c2s data seq=3 len=3 attempt=4: lost | conn=c1 dir=c2s kind=data seq=3 len=3 attempt=4 outcome=lost delay_ns=0",
		"5ms n1 c2 c2s rst seq=0 len=0 attempt=1: loopback | conn=c2 dir=c2s kind=rst seq=0 len=0 attempt=1 outcome=loopback delay_ns=7000",
		"send_raw delay_ns=7000 fifo=true",
	})
	if c.dirs[c2s].unacked != nil || c.dirs[s2c].unacked != nil || c.dirs[c2s].checkPending {
		t.Fatal("an attempt or an unreliable send touched the unacknowledged list")
	}
}

// The retransmission timeout: base by kind, doubling per attempt with the shift capped at 30, the
// RTOMax cap, no overflow; the deadline counts from the last attempt.
func TestRTO(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	nw := simnet.New(s, simnet.DefaultConfig())
	def := New(s, nw, &stubDriver{}, Config{})
	tiny := New(s, nw, &stubDriver{}, Config{RTOMin: 1, SynRTO: 1, RTOMax: 24 * time.Hour})
	hour := New(s, nw, &stubDriver{}, Config{RTOMin: time.Hour, SynRTO: time.Hour, RTOMax: 24 * time.Hour})
	three := New(s, nw, &stubDriver{}, Config{RTOMin: 1, SynRTO: 1, RTOMax: 3})
	for _, tc := range []struct {
		st       *Stack
		kind     segKind
		attempts int
		want     time.Duration
	}{
		{def, kSyn, 1, time.Second}, {def, kSyn, 2, 2 * time.Second}, {def, kSyn, 4, 8 * time.Second},
		{def, kSyn, 5, 10 * time.Second}, {def, kSyn, 9, 10 * time.Second},
		{def, kData, 1, 200 * time.Millisecond}, {def, kFin, 2, 400 * time.Millisecond},
		{def, kData, 6, 6400 * time.Millisecond}, {def, kData, 7, 10 * time.Second},
		{tiny, kData, 31, 1 << 30}, {tiny, kSyn, 100, 1 << 30}, // the shift stops at 30
		{hour, kData, 2, 2 * time.Hour}, {hour, kData, 6, 24 * time.Hour},
		{hour, kData, 40, 24 * time.Hour}, // hour << 30 overflows int64
		{three, kData, 2, 2},              // base == RTOMax >> 1, and 1 << 1 = 2 is under RTOMax 3
	} {
		if got := tc.st.rto(&segment{kind: tc.kind, attempts: tc.attempts}); got != tc.want {
			t.Errorf("rto(%s, attempt %d) with RTOMin %v = %v, want %v", kindNames[tc.kind], tc.attempts, tc.st.cfg.RTOMin, got, tc.want)
		}
	}
	seg := &segment{kind: kData, attempts: 2, first: 1, last: kernel.Time(5 * time.Second)}
	if got := def.deadline(seg); got != kernel.Time(5400*time.Millisecond) {
		t.Errorf("deadline = %v, want 5.4s", got)
	}
}

// Only an attempt over a connected link draws from a link stream (NSM-031): held and loopback
// attempts between the sent ones leave the sent attempts' outcomes and the self link's stream as
// they are without them.
func TestAttemptDrawsOnlyWhenConnected(t *testing.T) {
	run := func(extra bool) []string {
		s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
		link := simnet.Link{Latency: time.Millisecond, Jitter: time.Millisecond, DropPPM: simnet.MaxPPM / 2}
		nw := simnet.New(s, simnet.Config{Default: link})
		st := New(s, nw, &stubDriver{}, Config{})
		boot := func(n *kernel.Node) { st.Host(n) }
		a, b := s.AddNode("a", boot), s.AddNode("b", boot)
		s.RunFor(time.Millisecond)
		ha, hb := st.cur(a.ID()), st.cur(b.ID())
		nw.Handle(ha.n, nil) // the copies arrive nowhere: only the sending side runs
		nw.Handle(hb.n, nil)
		c, _ := clientConn(st, ha, hb)
		seg := &segment{c: c, dir: c2s, kind: kData, seq: 1, data: []byte("x")}
		loop := st.newConnection(ha.id, ha.id, 80, nil)
		t0 := s.Now()
		for i := range 8 {
			at(s, func() { st.attempt(seg) })
			if extra && i%2 == 0 {
				at(s, func() { nw.Isolate(hb.id); st.attempt(seg); nw.Heal() })
				at(s, func() { st.sendUnreliable(loop, c2s, kRst) })
			}
		}
		var out []string
		for _, r := range s.Records() {
			if o := attrValue(r, "outcome"); r.At >= t0 && r.Kind == "net.conn.segment" && (o == "sent" || o == "lost") {
				out = append(out, o+" "+attrValue(r, "delay_ns"))
			}
		}
		for range 4 {
			dec := nw.Decide(ha.id, ha.id)
			out = append(out, fmt.Sprintf("self drop=%v delay=%v", dec.Drop, dec.Delay))
		}
		return out
	}
	plain := run(false)
	if !slices.Contains(plain, "lost 0") || len(plain) != 12 {
		t.Fatalf("a lossy link gave no lost attempt: %q", plain)
	}
	sameLines(t, "draws with held and loopback attempts in between", run(true), plain)
}

// A data segment that is never acknowledged: attempts at 0, 0.2, 0.6, 1.4, 3, 6.2, 12.6 and
// 22.6 s, then the endpoint times out at 32.6 s, loses its buffers, wakes and leaves the table.
func TestDataTimeout(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	c, cli := clientConn(st, ha, hb)
	at(s, func() { nw.Isolate(hb.id) })
	d := c.dirs[c2s]
	d.queued, d.inflight = []byte("more"), 3
	c.dirs[s2c].buf, c.dirs[s2c].oooBytes = []byte("in"), 1
	t0 := s.Now()
	at(s, func() { st.transmit(&segment{c: c, dir: c2s, kind: kData, seq: 1, data: []byte("abc")}) })
	if len(d.unacked) != 1 || !d.checkPending {
		t.Fatalf("after transmit: unacked %d, check pending %v", len(d.unacked), d.checkPending)
	}
	at(s, func() { st.arm(d) }) // a check is pending already
	old := cli.notify
	s.RunFor(40 * time.Second)
	rto := 0
	for _, r := range s.Records() {
		if r.Kind == "kernel.event" && r.Text == "net.rto" {
			rto++
		}
	}
	if rto != 8 {
		t.Fatalf("%d net.rto events, want one per check (8)", rto)
	}
	var want []string
	for i, ms := range []int{0, 200, 600, 1400, 3000, 6200, 12600, 22600} {
		want = append(want, fmt.Sprintf("%v n1 c1 c2s data seq=1 len=3 attempt=%d: held | conn=c1 dir=c2s kind=data seq=1 len=3 attempt=%d outcome=held delay_ns=0",
			time.Duration(ms)*time.Millisecond, i+1, i+1))
	}
	want = append(want, "32.6s n1 timeout c1 c2s seq=1 | conn=c1 dir=c2s seq=1 waited_ns=32600000000")
	sameLines(t, "records", recLines(s, t0, "net.conn.segment", "net.conn.timeout"), want)
	if cli.errno != syscall.ETIMEDOUT || cli.state != stRemoved || ha.holds(cli) || len(ha.eks) != 0 || ha.tcpInUse(49152) {
		t.Fatalf("endpoint after the timeout: errno %v, state %d, eks %v", cli.errno, cli.state, ha.eks)
	}
	if d.unacked != nil || d.queued != nil || d.inflight != 0 || c.dirs[s2c].buf != nil || c.dirs[s2c].oooBytes != 0 || !closed(old) {
		t.Fatal("the timed-out endpoint kept buffers or was not woken")
	}
}

// check defers to the resume of a paused sender and stops for a sender that is no longer live.
func TestCheckPausedOrGone(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	c, _ := clientConn(st, ha, hb)
	at(s, func() { nw.Isolate(hb.id) })
	t0 := s.Now()
	at(s, func() { st.transmit(&segment{c: c, dir: c2s, kind: kSyn}) })
	s.At(t0.Add(500*time.Millisecond), "pause", ha.n.Pause)
	s.At(t0.Add(2500*time.Millisecond), "resume", ha.n.Resume)
	s.RunFor(5 * time.Second)
	got := recLines(s, t0, "net.conn.segment")
	for i, w := range []string{"0s ", "2.5s ", "4.5s "} {
		if len(got) != 3 || !strings.HasPrefix(got[i], w) {
			t.Fatalf("syn attempts with a pause from 0.5 s to 2.5 s: %q", got)
		}
	}
	// The check at 1 s finds a paused, so it posts the deferred check to a under the same label; that
	// one runs at the resume.
	var rto []string
	for _, r := range s.Records() {
		if r.At >= t0 && r.Kind == "kernel.event" && r.Text == "net.rto" {
			rto = append(rto, fmt.Sprintf("%v n%d", r.At.Sub(t0), r.Node))
		}
	}
	sameLines(t, "net.rto events", rto, []string{"1s n0", "2.5s n1", "4.5s n0"})

	c2 := st.newConnection(ha.id, hb.id, 81, nil)
	cli2 := newConn(c2, 0, ha, stEstablished, netip.MustParseAddrPort("10.0.0.1:49153"), netip.MustParseAddrPort("10.0.0.2:81"))
	c2.cli = cli2
	ha.addEndpoint(cli2)
	t1 := s.Now()
	at(s, func() { st.transmit(&segment{c: c2, dir: c2s, kind: kSyn}) })
	st.removeEndpoint(cli2)
	s.RunFor(5 * time.Second)
	for _, l := range recLines(s, t1, "net.conn.segment") {
		if strings.Contains(l, " c2 ") && !strings.HasPrefix(l, "0s ") {
			t.Fatalf("a removed sender retransmitted: %s", l)
		}
	}
}

// arm schedules a deadline that has passed at the current time, and check skips acknowledged
// segments at the head of the list.
func TestArmPastDeadline(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	c, _ := clientConn(st, ha, hb)
	at(s, func() { nw.Isolate(hb.id) })
	s.RunFor(time.Second)
	d := c.dirs[c2s]
	d.unacked = []*segment{{c: c, dir: c2s, kind: kData, seq: 1, attempts: 1, first: s.Now() - 1, last: 0}}
	t0 := s.Now()
	at(s, func() { st.arm(d) }) // deadline 0.2 s, an instant in the past
	sameLines(t, "records", recLines(s, t0, "net.conn.segment"), []string{
		"0s n1 c1 c2s data seq=1 len=0 attempt=2: held | conn=c1 dir=c2s kind=data seq=1 len=0 attempt=2 outcome=held delay_ns=0",
	})

	d.unacked = []*segment{{c: c, dir: c2s, kind: kData, seq: 2, acked: true}}
	d.checkPending = true
	t1 := s.Now()
	at(s, func() { st.check(d) })
	if len(d.unacked) != 0 || d.checkPending || len(recLines(s, t1, "net.conn.segment")) != 0 {
		t.Fatalf("check with an acknowledged head: unacked %d, pending %v", len(d.unacked), d.checkPending)
	}
}

// After the head is acknowledged, the next check finds the new head's deadline in the future and
// re-arms for it instead of retransmitting at once.
func TestCheckRearmsForNewHead(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	c, _ := clientConn(st, ha, hb)
	at(s, func() { nw.Isolate(hb.id) })
	d := c.dirs[c2s]
	d.inflight = 2
	s1 := &segment{c: c, dir: c2s, kind: kData, seq: 1, data: []byte("a")}
	s2 := &segment{c: c, dir: c2s, kind: kData, seq: 2, data: []byte("b")}
	t0 := s.Now()
	at(s, func() { st.transmit(s1) })
	s.RunFor(99 * time.Millisecond)
	at(s, func() { st.transmit(s2) })
	s.RunFor(49 * time.Millisecond)
	at(s, func() { st.ack(d, s1) })
	s.RunFor(time.Second)
	got := recLines(s, t0, "net.conn.segment")
	var short []string
	for _, l := range got {
		f := strings.Fields(l)
		short = append(short, f[0]+" "+f[5]+" "+f[7])
	}
	sameLines(t, "attempts", short, []string{"0s seq=1 attempt=1:", "100ms seq=2 attempt=1:", "300ms seq=2 attempt=2:", "700ms seq=2 attempt=3:"})
}

// ack removes the segment wherever it is in the list, frees data bytes once, wakes the sender and
// removes a closed sender whose fin was sent once nothing is left unacknowledged.
func TestAck(t *testing.T) {
	_, _, st, ha, hb := txStack(t, Config{})
	c, cli := clientConn(st, ha, hb)
	d := c.dirs[c2s]
	s1 := &segment{c: c, dir: c2s, kind: kData, seq: 1, data: []byte("abc")}
	s2 := &segment{c: c, dir: c2s, kind: kData, seq: 2, data: []byte("de")}
	fin := &segment{c: c, dir: c2s, kind: kFin, seq: 3}
	d.unacked, d.inflight = []*segment{s1, s2, fin}, 5
	old := cli.notify
	st.ack(d, s2)
	if !s2.acked || d.inflight != 3 || len(d.unacked) != 2 || d.unacked[0] != s1 || d.unacked[1] != fin || !closed(old) {
		t.Fatalf("after ack of the middle segment: inflight %d, unacked %v", d.inflight, d.unacked)
	}
	st.ack(d, s2)
	st.ack(d, fin)
	if d.inflight != 3 || len(d.unacked) != 1 {
		t.Fatalf("after a repeated ack and a fin ack: inflight %d, unacked %d", d.inflight, len(d.unacked))
	}
	cli.closed, d.finSent = true, true
	st.ack(d, s1)
	if d.inflight != 0 || cli.state != stRemoved || ha.holds(cli) {
		t.Fatalf("closed sender with its fin acknowledged: inflight %d, state %d", d.inflight, cli.state)
	}

	for _, tc := range []struct {
		name            string
		closed, finSent bool
		more            bool
	}{
		{"open", false, true, false},
		{"fin not sent", true, false, false},
		{"segments left", true, true, true},
	} {
		c, cli := clientConn(st, ha, hb)
		d := c.dirs[c2s]
		s1 := &segment{c: c, dir: c2s, kind: kFin, seq: 1}
		d.unacked = []*segment{s1}
		if tc.more {
			d.unacked = append(d.unacked, &segment{c: c, dir: c2s, kind: kData, seq: 2})
		}
		cli.closed, d.finSent = tc.closed, tc.finSent
		st.ack(d, s1)
		if cli.state == stRemoved || !ha.holds(cli) {
			t.Errorf("%s: sender removed", tc.name)
		}
	}

	c3 := st.newConnection(ha.id, hb.id, 80, nil) // no server endpoint
	d3 := c3.dirs[s2c]
	seg := &segment{c: c3, dir: s2c, kind: kData, seq: 1, data: []byte("x")}
	d3.unacked, d3.inflight = []*segment{seg}, 1
	st.ack(d3, seg)
	if !seg.acked || d3.inflight != 0 || len(d3.unacked) != 0 {
		t.Fatal("ack without a sender endpoint")
	}

	// A copy acknowledged after a timeout or reset discarded the list leaves inflight at 0.
	c4, cli4 := clientConn(st, ha, hb)
	d4 := c4.dirs[c2s]
	s4 := &segment{c: c4, dir: c2s, kind: kData, seq: 1, data: []byte("abc")}
	d4.unacked, d4.inflight = []*segment{s4}, 3
	st.discard(cli4)
	st.ack(d4, s4)
	if !s4.acked || d4.inflight != 0 {
		t.Fatalf("ack after discard: inflight %d", d4.inflight)
	}
}

// reset sets ECONNRESET, discards both directions' buffers, settles the listener's counts, frees
// the endpoint's place and port, wakes it and records the reason.
func TestReset(t *testing.T) {
	s, _, st, ha, hb := txStack(t, Config{})
	nl, err := hb.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := nl.(*Listener)
	c := st.newConnection(ha.id, hb.id, 80, nil)
	srv := newConn(c, 1, hb, stSynRcvd, netip.MustParseAddrPort("10.0.0.2:80"), netip.MustParseAddrPort("10.0.0.1:49152"))
	srv.lst, l.synrcvd = l, 2
	hb.addEndpoint(srv)
	seg := &segment{c: c, dir: s2c, kind: kSynAck}
	out, in := c.dirs[s2c], c.dirs[c2s]
	out.queued, out.unacked, out.inflight = []byte("q"), []*segment{seg}, 1
	in.buf, in.ooo[5], in.oooBytes = []byte("b"), seg, 1
	old := srv.notify
	t0 := s.Now()
	at(s, func() { st.reset(srv, "rst") })
	if srv.errno != syscall.ECONNRESET || srv.state != stRemoved || hb.holds(srv) || len(hb.eks) != 0 || l.synrcvd != 1 || !closed(old) {
		t.Fatalf("half-open endpoint after reset: errno %v, state %d, synrcvd %d", srv.errno, srv.state, l.synrcvd)
	}
	if out.queued != nil || out.unacked != nil || out.inflight != 0 || in.buf != nil || in.oooBytes != 0 || len(in.ooo) != 0 || in.ooo == nil {
		t.Fatal("reset kept buffers")
	}
	sameLines(t, "records", recLines(s, t0, "net.conn.reset"), []string{"0s n2 reset c1: rst | conn=c1 reason=rst"})

	c2 := st.newConnection(ha.id, hb.id, 80, nil)
	q := newConn(c2, 1, hb, stEstablished, netip.MustParseAddrPort("10.0.0.2:80"), netip.MustParseAddrPort("10.0.0.1:49153"))
	q.lst = l
	hb.addEndpoint(q)
	l.enqueue(q)
	at(s, func() { st.reset(q, "abort") })
	if len(l.queue) != 0 || q.inAcceptQueue || l.synrcvd != 1 {
		t.Fatalf("queued endpoint after reset: queue %d, synrcvd %d", len(l.queue), l.synrcvd)
	}

	_, cli := clientConn(st, ha, hb)
	at(s, func() { st.reset(cli, "peer-crashed") })
	if ha.tcpInUse(49152) {
		t.Fatal("a reset client endpoint kept its port")
	}
}

// removeEndpoint deletes the table entry only while it belongs to the endpoint.
func TestRemoveEndpoint(t *testing.T) {
	s, _, st, ha, _ := txStack(t, Config{})
	c := st.newConnection(ha.id, ha.id, 80, nil) // loopback: both endpoints on one host
	e1 := newConn(c, 0, ha, stDialing, netip.MustParseAddrPort("127.0.0.1:49152"), netip.MustParseAddrPort("127.0.0.1:80"))
	e2 := newConn(c, 1, ha, stSynRcvd, e1.remote, e1.local)
	ha.addEndpoint(e1)
	ha.addEndpoint(e2)
	at(s, func() { st.removeEndpoint(e1) })
	if e1.state != stRemoved || ha.holds(e1) || !ha.holds(e2) || len(ha.eks) != 1 {
		t.Fatal("removing an endpoint deleted another endpoint's entry")
	}
	st.removeEndpoint(e2)
	st.removeEndpoint(e2)
	if ha.holds(e2) || len(ha.eks) != 0 {
		t.Fatalf("eks %v", ha.eks)
	}
}

// failDial removes the client and the half-open server endpoint, records refused and the
// server's reset, and finishes the dial record.
func TestFailDial(t *testing.T) {
	s, _, st, ha, hb := txStack(t, Config{})
	nl, err := hb.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := nl.(*Listener)
	other := &dialRecord{h: ha, done: make(chan struct{})}
	dr := &dialRecord{h: ha, done: make(chan struct{})}
	ha.dials = []*dialRecord{other, dr}
	c := st.newConnection(ha.id, hb.id, 80, dr)
	dr.c = c
	cli := newConn(c, 0, ha, stDialing, netip.MustParseAddrPort("10.0.0.1:49152"), netip.MustParseAddrPort("10.0.0.2:80"))
	srv := newConn(c, 1, hb, stSynRcvd, cli.remote, cli.local)
	c.cli, c.srv, srv.lst, l.synrcvd = cli, srv, l, 1
	ha.addEndpoint(cli)
	hb.addEndpoint(srv)
	c.dirs[c2s].unacked = []*segment{{c: c, dir: c2s, kind: kSyn}}
	old := cli.notify
	refused := &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}
	t0 := s.Now()
	at(s, func() { st.failDial(c, refused, true) })
	if c.dial != nil || cli.state != stRemoved || ha.holds(cli) || c.dirs[c2s].unacked != nil || !closed(old) {
		t.Fatal("the client endpoint survived the failed dial")
	}
	if srv.state != stRemoved || hb.holds(srv) || l.synrcvd != 0 {
		t.Fatalf("half-open server endpoint: state %d, synrcvd %d", srv.state, l.synrcvd)
	}
	if !dr.finished || dr.err != refused || dr.conn != nil || !closed(dr.done) || len(ha.dials) != 1 || ha.dials[0] != other {
		t.Fatalf("dial record: finished %v, err %v, dials %d", dr.finished, dr.err, len(ha.dials))
	}
	sameLines(t, "records", recLines(s, t0, "net.conn.refused", "net.conn.reset"), []string{
		"0s n1 refused c1 | conn=c1",
		"0s n2 reset c1: handshake-aborted | conn=c1 reason=handshake-aborted",
	})

	dr2 := &dialRecord{h: ha, done: make(chan struct{})}
	c2 := st.newConnection(ha.id, hb.id, 80, dr2)
	srv2 := newConn(c2, 1, hb, stEstablished, netip.MustParseAddrPort("10.0.0.2:80"), netip.MustParseAddrPort("10.0.0.1:49153"))
	c2.srv, srv2.lst = srv2, l
	hb.addEndpoint(srv2)
	t1 := s.Now()
	at(s, func() { st.failDial(c2, errAbandoned, false) })
	if !dr2.finished || dr2.err != errAbandoned || srv2.state != stEstablished || !hb.holds(srv2) || l.synrcvd != 0 {
		t.Fatal("failDial touched an established server endpoint or missed the dial record")
	}
	if got := recLines(s, t1, "net.conn.refused", "net.conn.reset"); len(got) != 0 {
		t.Fatalf("records %q", got)
	}
	st.failDial(st.newConnection(ha.id, hb.id, 80, nil), errAbandoned, false) // nothing to end
}

// A check exactly ConnTimeout after the first attempt times the syn out; the dial fails with
// ETIMEDOUT.
func TestTimeoutBoundary(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{ConnTimeout: 3 * time.Second})
	at(s, func() { nw.Isolate(hb.id) })
	tp := netip.MustParseAddrPort("10.0.0.2:80")
	dr := &dialRecord{h: ha, target: tp, dst: hb.id, key: "dial:" + tp.String(), done: make(chan struct{})}
	ha.dials = []*dialRecord{dr}
	t0 := s.Now()
	at(s, func() { st.startDial(dr) })
	s.RunFor(10 * time.Second)
	got := recLines(s, t0, "net.conn.segment", "net.conn.timeout")
	if len(got) != 3 || got[2] != "3s n1 timeout c1 c2s seq=0 | conn=c1 dir=c2s seq=0 waited_ns=3000000000" {
		t.Fatalf("records %q", got)
	}
	var se *os.SyscallError
	if !dr.finished || !errors.As(dr.err, &se) || se.Syscall != "connect" || se.Err != syscall.ETIMEDOUT || len(ha.dials) != 0 || dr.c.cli.state != stRemoved {
		t.Fatalf("dial after the timeout: %v", dr.err)
	}
}
