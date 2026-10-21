// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// dialing starts a dial from ha to hb's port; the client endpoint is dialing and its syn has made
// one attempt at the time of the call.
func dialing(s *kernel.Sim, st *Stack, ha, hb *Host, port uint16) (*connection, *dialRecord) {
	tp := netip.AddrPortFrom(hb.addr, port)
	dr := &dialRecord{h: ha, target: tp, dst: hb.id, key: "dial:" + tp.String(), done: make(chan struct{})}
	ha.dials = append(ha.dials, dr)
	at(s, func() { st.startDial(dr) })
	return dr.c, dr
}

// synOf returns the client's syn, the only unacknowledged segment of a dialing connection.
func synOf(c *connection) *segment { return c.dirs[c2s].unacked[0] }

// serverConn adds an established server endpoint for c to hb's table.
func serverConn(c *connection, hb *Host) *Conn {
	srv := newConn(c, 1, hb, stEstablished, c.cli.remote, c.cli.local)
	c.srv = srv
	hb.addEndpoint(srv)
	return srv
}

// syn arrivals: a new half-open endpoint with the mirrored addresses on a listener bound to any
// address of the port, a synack again for a duplicate while half-open and nothing for one after,
// rst for a port without a listener or a gone host, and no duplicate for an endpoint that was
// removed or belongs to an earlier incarnation.
func TestArriveSyn(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	mute(nw, ha, hb)
	nl, err := hb.Listen("tcp", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := nl.(*Listener)
	c1, _ := dialing(s, st, ha, hb, 80)
	c2, _ := dialing(s, st, ha, hb, 81)
	c3, _ := dialing(s, st, ha, hb, 80)
	t0 := s.Now()
	at(s, func() { st.arrive(hb.id, synOf(c1)) })
	srv := c1.srv
	if srv == nil || srv.side != 1 || srv.h != hb || srv.state != stSynRcvd || srv.lst != l || !hb.holds(srv) || l.synrcvd != 1 ||
		srv.local.String() != "10.0.0.2:80" || srv.remote.String() != "10.0.0.1:49152" || len(l.queue) != 0 {
		t.Fatalf("half-open endpoint %+v, synrcvd %d", srv, l.synrcvd)
	}
	at(s, func() { st.arrive(hb.id, synOf(c1)) })
	srv.state = stEstablished
	at(s, func() { st.arrive(hb.id, synOf(c1)) })
	if c1.srv != srv || l.synrcvd != 1 || len(hb.eks) != 1 {
		t.Fatalf("a duplicate syn changed the server side: synrcvd %d, eks %v", l.synrcvd, hb.eks)
	}
	at(s, func() { st.arrive(hb.id, synOf(c2)) })
	hb.gone = true
	at(s, func() { st.arrive(hb.id, synOf(c3)) })
	hb.gone = false
	if c2.srv != nil || c3.srv != nil || l.synrcvd != 1 {
		t.Fatal("a refused syn created a server endpoint")
	}
	st.removeEndpoint(srv)
	at(s, func() { st.arrive(hb.id, synOf(c1)) })
	srv2 := c1.srv
	srv2.h = &Host{id: hb.id} // as if created by an earlier incarnation of b
	at(s, func() { st.arrive(hb.id, synOf(c1)) })
	if srv2 == srv || c1.srv == srv2 || c1.srv.state != stSynRcvd || l.synrcvd != 3 {
		t.Fatalf("syn after a removed or earlier endpoint: synrcvd %d", l.synrcvd)
	}
	arrive := func(at, conn, result string) string {
		return at + " n2 " + conn + " c2s syn seq=0: " + result + " | conn=" + conn + " dir=c2s kind=syn seq=0 len=0 result=" + result
	}
	reply := func(at, conn, kind string) string {
		return at + " n2 " + conn + " s2c " + kind + " seq=0 len=0 attempt=1: sent | conn=" + conn + " dir=s2c kind=" + kind +
			" seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000"
	}
	sameLines(t, "records", recLines(s, t0, "net.conn.arrive", "net.conn.segment"), []string{
		arrive("0s", "c1", "synrcvd"), reply("0s", "c1", "synack"),
		arrive("1ms", "c1", "duplicate"), reply("1ms", "c1", "synack"),
		arrive("2ms", "c1", "duplicate"),
		arrive("3ms", "c2", "refused"), reply("3ms", "c2", "rst"),
		arrive("4ms", "c3", "refused"), reply("4ms", "c3", "rst"),
		arrive("5ms", "c1", "synrcvd"), reply("5ms", "c1", "synack"),
		arrive("6ms", "c1", "synrcvd"), reply("6ms", "c1", "synack"),
	})
}

// The backlog counts half-open endpoints and the accept queue; a syn that would exceed it gets
// no answer.
func TestArriveSynBacklog(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{Backlog: 2})
	mute(nw, ha, hb)
	nl, err := hb.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := nl.(*Listener)
	c1, _ := dialing(s, st, ha, hb, 80)
	c2, _ := dialing(s, st, ha, hb, 80)
	c3, _ := dialing(s, st, ha, hb, 80)
	t0 := s.Now()
	at(s, func() { st.arrive(hb.id, synOf(c1)) })
	at(s, func() { st.arrive(hb.id, synOf(c2)) })
	at(s, func() { st.arrive(hb.id, synOf(c3)) })
	if c3.srv != nil || l.synrcvd != 2 {
		t.Fatalf("syn over the backlog: synrcvd %d", l.synrcvd)
	}
	c1.srv.state, l.synrcvd = stEstablished, 1
	l.enqueue(c1.srv)
	at(s, func() { st.arrive(hb.id, synOf(c3)) })
	l.dequeue(c1.srv)
	at(s, func() { st.arrive(hb.id, synOf(c3)) })
	if c3.srv == nil || l.synrcvd != 2 {
		t.Fatalf("syn under the backlog: synrcvd %d", l.synrcvd)
	}
	var got []string
	for _, line := range recLines(s, t0, "net.conn.arrive", "net.conn.segment") {
		head, _, _ := strings.Cut(line, " |")
		got = append(got, head)
	}
	sameLines(t, "records", got, []string{
		"0s n2 c1 c2s syn seq=0: synrcvd", "0s n2 c1 s2c synack seq=0 len=0 attempt=1: sent",
		"1ms n2 c2 c2s syn seq=0: synrcvd", "1ms n2 c2 s2c synack seq=0 len=0 attempt=1: sent",
		"2ms n2 c3 c2s syn seq=0: backlog-full",
		"3ms n2 c3 c2s syn seq=0: backlog-full",
		"4ms n2 c3 c2s syn seq=0: synrcvd", "4ms n2 c3 s2c synack seq=0 len=0 attempt=1: sent",
	})
}

// synack arrivals: the client is established, its syn acknowledged, the round trip measured from
// the syn's first attempt, the dial completed and the half-open server endpoint queued for
// Accept; a second synack is a duplicate; a synack for a client that is no longer live or no
// longer dialing is answered with rst on c2s, and so is one for a dial the process abandoned,
// which it fails.
func TestArriveSynAck(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	mute(nw, ha, hb)
	nl, err := hb.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := nl.(*Listener)
	c1, dr := dialing(s, st, ha, hb, 80)
	syn := synOf(c1)
	at(s, func() { st.attempt(syn) }) // a retransmission: the round trip still counts from the first
	at(s, func() { st.arrive(hb.id, syn) })
	cli, srv := c1.cli, c1.srv
	cn, ln := cli.notify, l.notify
	synack := &segment{c: c1, dir: s2c, kind: kSynAck}
	t0 := s.Now()
	at(s, func() { st.arrive(ha.id, synack) })
	if cli.state != stEstablished || !syn.acked || len(c1.dirs[c2s].unacked) != 0 || !closed(cn) {
		t.Fatalf("client after synack: state %d, syn acked %v", cli.state, syn.acked)
	}
	if !dr.finished || dr.conn != cli || dr.err != nil || !closed(dr.done) || c1.dial != nil || len(ha.dials) != 0 {
		t.Fatalf("dial after synack: finished %v, err %v, dials %d", dr.finished, dr.err, len(ha.dials))
	}
	if srv.state != stEstablished || l.synrcvd != 0 || len(l.queue) != 1 || l.queue[0] != srv || !srv.inAcceptQueue || !closed(ln) {
		t.Fatalf("server after synack: state %d, synrcvd %d, queue %d", srv.state, l.synrcvd, len(l.queue))
	}
	at(s, func() { st.arrive(ha.id, synack) })
	cli.errno = syscall.ECONNRESET
	at(s, func() { st.arrive(ha.id, synack) })
	sameLines(t, "records", recLines(s, t0, "net.conn.arrive", "net.conn.segment", "net.conn.established", "net.conn.accept"), []string{
		"0s n1 c1 s2c synack seq=0: established | conn=c1 dir=s2c kind=synack seq=0 len=0 result=established",
		"0s n1 established c1 | conn=c1 rtt_ns=3000000",
		"0s n2 accept queue c1 | conn=c1 port=80",
		"1ms n1 c1 s2c synack seq=0: duplicate | conn=c1 dir=s2c kind=synack seq=0 len=0 result=duplicate",
		"2ms n1 c1 s2c synack seq=0: no-conn | conn=c1 dir=s2c kind=synack seq=0 len=0 result=no-conn",
		"2ms n1 c1 c2s rst seq=0 len=0 attempt=1: sent | conn=c1 dir=c2s kind=rst seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000",
	})

	// The server endpoint of an earlier incarnation, a server endpoint reset meanwhile, or none at
	// all: only the client side completes.
	c2, dr2 := dialing(s, st, ha, hb, 80)
	at(s, func() { st.arrive(hb.id, synOf(c2)) })
	c2.srv.h = &Host{id: hb.id}
	c3, dr3 := dialing(s, st, ha, hb, 80)
	c4, dr4 := dialing(s, st, ha, hb, 80)
	at(s, func() { st.arrive(hb.id, synOf(c4)) })
	st.reset(c4.srv, "rst")
	t1 := s.Now()
	at(s, func() { st.arrive(ha.id, &segment{c: c2, dir: s2c, kind: kSynAck}) })
	at(s, func() { st.arrive(ha.id, &segment{c: c3, dir: s2c, kind: kSynAck}) })
	at(s, func() { st.arrive(ha.id, &segment{c: c4, dir: s2c, kind: kSynAck}) })
	if !dr2.finished || dr2.err != nil || !dr3.finished || dr3.err != nil || !dr4.finished || dr4.err != nil ||
		c2.srv.state != stSynRcvd || c4.srv.state != stRemoved || l.synrcvd != 1 || len(l.queue) != 1 {
		t.Fatalf("synack without a current server endpoint: synrcvd %d, queue %d", l.synrcvd, len(l.queue))
	}
	if got := recLines(s, t1, "net.conn.accept"); len(got) != 0 {
		t.Fatalf("accept records %q", got)
	}

	// A dial that already failed: the client endpoint is gone.
	c5, _ := dialing(s, st, ha, hb, 80)
	st.failDial(c5, errAbandoned, false)
	t2 := s.Now()
	at(s, func() { st.arrive(ha.id, &segment{c: c5, dir: s2c, kind: kSynAck}) })
	sameLines(t, "records", recLines(s, t2, "net.conn.arrive", "net.conn.segment", "net.conn.established"), []string{
		"0s n1 c5 s2c synack seq=0: no-conn | conn=c5 dir=s2c kind=synack seq=0 len=0 result=no-conn",
		"0s n1 c5 c2s rst seq=0 len=0 attempt=1: sent | conn=c5 dir=c2s kind=rst seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000",
	})

	// A dial the process abandoned before its net.dial_abort ran: the synack fails the dial, which
	// removes both endpoints, and is answered with rst; nothing is established or queued.
	c6, dr6 := dialing(s, st, ha, hb, 80)
	at(s, func() { st.arrive(hb.id, synOf(c6)) })
	cli6, srv6 := c6.cli, c6.srv
	dr6.abandoned = true
	t3 := s.Now()
	at(s, func() { st.arrive(ha.id, &segment{c: c6, dir: s2c, kind: kSynAck}) })
	if !dr6.finished || dr6.err != errAbandoned || dr6.conn != nil || c6.dial != nil || len(ha.dials) != 0 ||
		cli6.state != stRemoved || ha.holds(cli6) || srv6.state != stRemoved || hb.holds(srv6) || l.synrcvd != 1 || len(l.queue) != 1 {
		t.Fatalf("abandoned dial after synack: err %v, client %d, server %d, synrcvd %d, queue %d",
			dr6.err, cli6.state, srv6.state, l.synrcvd, len(l.queue))
	}
	sameLines(t, "records", recLines(s, t3, "net.conn.arrive", "net.conn.segment", "net.conn.established", "net.conn.accept", "net.conn.reset",
		"net.conn.refused"), []string{
		"0s n1 c6 s2c synack seq=0: no-conn | conn=c6 dir=s2c kind=synack seq=0 len=0 result=no-conn",
		"0s n2 reset c6: handshake-aborted | conn=c6 reason=handshake-aborted",
		"0s n1 c6 c2s rst seq=0 len=0 attempt=1: sent | conn=c6 dir=c2s kind=rst seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000",
	})
}

// A syn copy that arrives after its dial ended or completed is ignored: it creates no half-open
// endpoint (nothing would remove it, and it would hold a backlog slot), replaces no server
// endpoint and gets no answer. A copy that finds a live server endpoint is still a duplicate.
func TestArriveSynLate(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{Backlog: 1})
	mute(nw, ha, hb)
	nl, err := hb.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	l, _ := nl.(*Listener)
	// An established connection whose server endpoint was accepted and then removed.
	c1, _ := dialing(s, st, ha, hb, 80)
	syn1 := synOf(c1)
	at(s, func() { st.arrive(hb.id, syn1) })
	at(s, func() { st.arrive(ha.id, &segment{c: c1, dir: s2c, kind: kSynAck}) })
	srv1 := c1.srv
	l.dequeue(srv1)
	st.removeEndpoint(srv1)
	// A dial that ended with its syn in flight, and a dialing client endpoint of an earlier
	// incarnation of a.
	c2, _ := dialing(s, st, ha, hb, 80)
	syn2 := synOf(c2)
	st.failDial(c2, errAbandoned, false)
	c3, _ := dialing(s, st, ha, hb, 80)
	c3.cli.h = &Host{id: ha.id}
	c4, _ := dialing(s, st, ha, hb, 80)
	t0 := s.Now()
	at(s, func() { st.arrive(hb.id, syn1) })
	at(s, func() { st.arrive(hb.id, syn2) })
	at(s, func() { st.arrive(hb.id, synOf(c3)) })
	if c1.srv != srv1 || c2.srv != nil || c3.srv != nil || l.synrcvd != 0 || len(hb.eks) != 0 {
		t.Fatalf("late syns changed the server side: synrcvd %d, eks %v", l.synrcvd, hb.eks)
	}
	syn4 := synOf(c4)
	at(s, func() { st.arrive(hb.id, syn4) }) // the backlog of 1 is still free
	if c4.srv == nil || c4.srv.state != stSynRcvd || l.synrcvd != 1 {
		t.Fatalf("a new dial after the late syns: synrcvd %d", l.synrcvd)
	}
	x := &segment{c: c1, dir: c2s, kind: kData, seq: 1, data: []byte("x")}
	at(s, func() { st.arrive(hb.id, x) }) // c1's server endpoint is still gone
	at(s, func() { st.arrive(ha.id, &segment{c: c4, dir: s2c, kind: kSynAck}) })
	at(s, func() { st.arrive(hb.id, syn4) }) // both ends live: the duplicate rows come first
	if x.acked || len(c1.dirs[c2s].buf) != 0 {
		t.Fatal("data after a late syn reached a server endpoint")
	}
	ignored := func(at, conn string) string {
		return at + " n2 " + conn + " c2s syn seq=0: ignored | conn=" + conn + " dir=c2s kind=syn seq=0 len=0 result=ignored"
	}
	sameLines(t, "records", recLines(s, t0, "net.conn.arrive", "net.conn.segment"), []string{
		ignored("0s", "c1"), ignored("1ms", "c2"), ignored("2ms", "c3"),
		"3ms n2 c4 c2s syn seq=0: synrcvd | conn=c4 dir=c2s kind=syn seq=0 len=0 result=synrcvd",
		"3ms n2 c4 s2c synack seq=0 len=0 attempt=1: sent | conn=c4 dir=s2c kind=synack seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000",
		"4ms n2 c1 c2s data seq=1: no-conn | conn=c1 dir=c2s kind=data seq=1 len=1 result=no-conn",
		"4ms n2 c1 s2c rst seq=0 len=0 attempt=1: sent | conn=c1 dir=s2c kind=rst seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000",
		"5ms n1 c4 s2c synack seq=0: established | conn=c4 dir=s2c kind=synack seq=0 len=0 result=established",
		"6ms n2 c4 c2s syn seq=0: duplicate | conn=c4 dir=c2s kind=syn seq=0 len=0 result=duplicate",
	})
}

// Data and fin at a live endpoint: every arrival is acknowledged; segments reach the receive
// buffer in sequence order whatever order they arrive in; copies already delivered or buffered
// are duplicates; data after CloseRead is discarded but still advances the sequence; fin sets
// end of file.
func TestArriveDataInOrder(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	mute(nw, ha, hb)
	c, _ := clientConn(st, ha, hb)
	srv := serverConn(c, hb)
	d := c.dirs[c2s]
	seg := func(seq uint64, kind segKind, data string) *segment {
		return &segment{c: c, dir: c2s, kind: kind, seq: seq, data: []byte(data)}
	}
	s1, s2, s3, fin := seg(1, kData, "ab"), seg(2, kData, "cd"), seg(3, kData, "ef"), seg(4, kFin, "")
	d.unacked, d.inflight = []*segment{s1, s2, s3, fin}, 6
	t0 := s.Now()
	at(s, func() { st.arrive(hb.id, s2) })
	if d.ooo[2] != s2 || d.oooBytes != 2 || d.rcvNext != 1 || len(d.buf) != 0 || !s2.acked || d.inflight != 4 || len(d.unacked) != 3 {
		t.Fatalf("after an early segment: rcvNext %d, oooBytes %d, inflight %d", d.rcvNext, d.oooBytes, d.inflight)
	}
	at(s, func() { st.arrive(hb.id, s2) })
	wake := srv.notify
	at(s, func() { st.arrive(hb.id, s1) })
	if string(d.buf) != "abcd" || d.rcvNext != 3 || len(d.ooo) != 0 || d.oooBytes != 0 || !closed(wake) {
		t.Fatalf("after the missing segment: buf %q, rcvNext %d, oooBytes %d", d.buf, d.rcvNext, d.oooBytes)
	}
	at(s, func() { st.arrive(hb.id, s1) })
	at(s, func() { st.arrive(hb.id, fin) })
	srv.rdShut = true
	at(s, func() { st.arrive(hb.id, s3) })
	if string(d.buf) != "abcd" || d.rcvNext != 5 || !d.eof || len(d.ooo) != 0 || d.oooBytes != 0 || d.inflight != 0 || len(d.unacked) != 0 {
		t.Fatalf("after CloseRead and fin: buf %q, rcvNext %d, eof %v, inflight %d", d.buf, d.rcvNext, d.eof, d.inflight)
	}
	sameLines(t, "records", recLines(s, t0, "net.conn.arrive", "net.conn.segment", "net.conn.reset"), []string{
		"0s n2 c1 c2s data seq=2: buffered | conn=c1 dir=c2s kind=data seq=2 len=2 result=buffered",
		"1ms n2 c1 c2s data seq=2: duplicate | conn=c1 dir=c2s kind=data seq=2 len=2 result=duplicate",
		"2ms n2 c1 c2s data seq=1: in-order | conn=c1 dir=c2s kind=data seq=1 len=2 result=in-order",
		"3ms n2 c1 c2s data seq=1: duplicate | conn=c1 dir=c2s kind=data seq=1 len=2 result=duplicate",
		"4ms n2 c1 c2s fin seq=4: buffered | conn=c1 dir=c2s kind=fin seq=4 len=0 result=buffered",
		"5ms n2 c1 c2s data seq=3: in-order | conn=c1 dir=c2s kind=data seq=3 len=2 result=in-order",
	})
}

// Data and fin where the receiving endpoint is not live are answered with rst on the reverse
// direction and not acknowledged; data at a closed endpoint resets it (reason abort), a fin there
// is delivered.
func TestArriveDataNoConnOrClosed(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	mute(nw, ha, hb)
	c1, _ := clientConn(st, ha, hb) // no server endpoint
	x := &segment{c: c1, dir: c2s, kind: kData, seq: 1, data: []byte("x")}
	c1.dirs[c2s].unacked, c1.dirs[c2s].inflight = []*segment{x}, 1
	c2, cli2 := clientConn(st, ha, hb)
	cli2.closed = true
	y := &segment{c: c2, dir: s2c, kind: kData, seq: 1, data: []byte("y")}
	c3, cli3 := clientConn(st, ha, hb)
	cli3.closed = true
	c4, cli4 := clientConn(st, ha, hb)
	cli4.errno = syscall.ETIMEDOUT
	t0 := s.Now()
	at(s, func() { st.arrive(hb.id, x) })
	at(s, func() { st.arrive(hb.id, &segment{c: c1, dir: c2s, kind: kFin, seq: 2}) })
	at(s, func() { st.arrive(ha.id, y) })
	at(s, func() { st.arrive(ha.id, &segment{c: c3, dir: s2c, kind: kFin, seq: 1}) })
	at(s, func() { st.arrive(ha.id, &segment{c: c4, dir: s2c, kind: kData, seq: 1, data: []byte("z")}) })
	if x.acked || len(c1.dirs[c2s].unacked) != 1 || c1.dirs[c2s].inflight != 1 || c1.dirs[c2s].rcvNext != 1 {
		t.Fatal("a segment without a live receiver was acknowledged or delivered")
	}
	if !y.acked || cli2.errno != syscall.ECONNRESET || cli2.state != stRemoved || ha.holds(cli2) || len(c2.dirs[s2c].buf) != 0 {
		t.Fatalf("closed endpoint after data: errno %v, state %d", cli2.errno, cli2.state)
	}
	if !c3.dirs[s2c].eof || c3.dirs[s2c].rcvNext != 2 || cli3.errno != nil || !ha.holds(cli3) {
		t.Fatalf("closed endpoint after fin: eof %v, errno %v", c3.dirs[s2c].eof, cli3.errno)
	}
	if c4.dirs[s2c].rcvNext != 1 || len(c4.dirs[s2c].buf) != 0 {
		t.Fatal("data was delivered to an endpoint in an error state")
	}
	rst := func(at, node, conn, dir string) string {
		return at + " " + node + " " + conn + " " + dir + " rst seq=0 len=0 attempt=1: sent | conn=" + conn + " dir=" + dir +
			" kind=rst seq=0 len=0 attempt=1 outcome=sent delay_ns=1000000"
	}
	sameLines(t, "records", recLines(s, t0, "net.conn.arrive", "net.conn.segment", "net.conn.reset"), []string{
		"0s n2 c1 c2s data seq=1: no-conn | conn=c1 dir=c2s kind=data seq=1 len=1 result=no-conn", rst("0s", "n2", "c1", "s2c"),
		"1ms n2 c1 c2s fin seq=2: no-conn | conn=c1 dir=c2s kind=fin seq=2 len=0 result=no-conn", rst("1ms", "n2", "c1", "s2c"),
		"2ms n1 c2 s2c data seq=1: closed | conn=c2 dir=s2c kind=data seq=1 len=1 result=closed", rst("2ms", "n1", "c2", "c2s"),
		"2ms n1 reset c2: abort | conn=c2 reason=abort",
		"3ms n1 c3 s2c fin seq=1: in-order | conn=c3 dir=s2c kind=fin seq=1 len=0 result=in-order",
		"4ms n1 c4 s2c data seq=1: no-conn | conn=c4 dir=s2c kind=data seq=1 len=1 result=no-conn", rst("4ms", "n1", "c4", "c2s"),
	})
}

// rst arrivals: a dialing client fails its dial as refused, a live endpoint on either side is
// reset (reason rst), anything else ignores it; an rst is never answered.
func TestArriveRst(t *testing.T) {
	s, nw, st, ha, hb := txStack(t, Config{})
	mute(nw, ha, hb)
	c1, dr := dialing(s, st, ha, hb, 80)
	c2, cli2 := clientConn(st, ha, hb)
	srv2 := serverConn(c2, hb)
	t0 := s.Now()
	at(s, func() { st.arrive(ha.id, &segment{c: c1, dir: s2c, kind: kRst}) })
	var se *os.SyscallError
	if !dr.finished || !errors.As(dr.err, &se) || se.Syscall != "connect" || se.Err != syscall.ECONNREFUSED || dr.conn != nil ||
		c1.cli.state != stRemoved || len(ha.dials) != 0 {
		t.Fatalf("dial after rst: %v", dr.err)
	}
	at(s, func() { st.arrive(ha.id, &segment{c: c1, dir: s2c, kind: kRst}) })
	at(s, func() { st.arrive(ha.id, &segment{c: c2, dir: s2c, kind: kRst}) })
	at(s, func() { st.arrive(hb.id, &segment{c: c2, dir: c2s, kind: kRst}) })
	if cli2.errno != syscall.ECONNRESET || cli2.state != stRemoved || srv2.errno != syscall.ECONNRESET || srv2.state != stRemoved {
		t.Fatalf("endpoints after rst: %v %v", cli2.errno, srv2.errno)
	}
	sameLines(t, "records", recLines(s, t0, "net.conn.arrive", "net.conn.segment", "net.conn.refused", "net.conn.reset"), []string{
		"0s n1 c1 s2c rst seq=0: refused | conn=c1 dir=s2c kind=rst seq=0 len=0 result=refused",
		"0s n1 refused c1 | conn=c1",
		"1ms n1 c1 s2c rst seq=0: ignored | conn=c1 dir=s2c kind=rst seq=0 len=0 result=ignored",
		"2ms n1 c2 s2c rst seq=0: reset | conn=c2 dir=s2c kind=rst seq=0 len=0 result=reset",
		"2ms n1 reset c2: rst | conn=c2 reason=rst",
		"3ms n2 c2 c2s rst seq=0: reset | conn=c2 dir=c2s kind=rst seq=0 len=0 result=reset",
		"3ms n2 reset c2: rst | conn=c2 reason=rst",
	})
}

// A process node that has not booted yet when the dial starts is not event-style: the dial goes
// to the network, and here the node's rst refuses it, since nothing listens on the port.
func TestStartDialBeforeTargetBoots(t *testing.T) {
	s, _, st, ha, _ := txStack(t, Config{})
	p := s.AddNode("p", func(n *kernel.Node) { st.Host(n) }, kernel.WithTags("server", "process"))
	tp := netip.AddrPortFrom(NodeAddr(p.ID()), 80)
	dr := &dialRecord{h: ha, target: tp, dst: p.ID(), key: "dial:" + tp.String(), done: make(chan struct{})}
	ha.dials = append(ha.dials, dr)
	t0 := s.Now()
	st.startDial(dr) // p's boot is due now but has not run
	if dr.finished || dr.c == nil || st.node(p.ID()).ever {
		t.Fatalf("dial to a process node that has not booted: finished %v, err %v", dr.finished, dr.err)
	}
	s.RunFor(2 * time.Second)
	sameLines(t, "records", recLines(s, t0, "net.conn.refused"), []string{"2ms n1 refused c1 | conn=c1"})
	if !errors.Is(dr.err, syscall.ECONNREFUSED) {
		t.Fatalf("dial: %v", dr.err)
	}
}
