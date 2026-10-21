// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
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
