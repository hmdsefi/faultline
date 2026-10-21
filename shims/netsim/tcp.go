// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

const (
	c2s = 0 // client to server
	s2c = 1 // server to client
)

var dirNames = [2]string{"c2s", "s2c"}

type segKind uint8

const (
	kSyn segKind = iota + 1
	kSynAck
	kData
	kFin
	kRst
)

var kindNames = [...]string{kSyn: "syn", kSynAck: "synack", kData: "data", kFin: "fin", kRst: "rst"}

// segment is one unit sent on a direction (NSM §3). Retransmissions resend the same pointer; only
// the attempt bookkeeping changes, which simnet never reads.
type segment struct {
	c        *connection
	dir      int
	kind     segKind
	seq      uint64
	data     []byte
	first    kernel.Time
	last     kernel.Time
	attempts int
	acked    bool
}

// Describe is the payload description of net.send_raw (NSM §8).
func (s *segment) Describe() string {
	return fmt.Sprintf("tcp %s %s %s seq=%d len=%d", s.c.id, dirNames[s.dir], kindNames[s.kind], s.seq, len(s.data))
}

// connection is one TCP-like connection c<k> (NSM §3).
type connection struct {
	k       int
	id      string
	cli     *Conn
	srv     *Conn
	dirs    [2]*direction
	cliNode kernel.NodeID
	srvNode kernel.NodeID
	srvPort uint16
	dial    *dialRecord // non-nil while the client is dialing
}

// ends returns the sender and receiver node of direction dir.
func (c *connection) ends(dir int) (kernel.NodeID, kernel.NodeID) {
	if dir == c2s {
		return c.cliNode, c.srvNode
	}
	return c.srvNode, c.cliNode
}

// endpoint returns the client (side 0) or server (side 1) endpoint.
func (c *connection) endpoint(side int) *Conn {
	if side == 0 {
		return c.cli
	}
	return c.srv
}

// direction holds the sender and receiver state of one direction (NSM-035).
type direction struct {
	c   *connection
	dir int
	// sender
	queued       []byte
	inflight     int
	nextSeq      uint64
	finPending   bool //nolint:unused // close state, not wired up yet
	finSent      bool
	unacked      []*segment
	checkPending bool
	// receiver
	rcvNext  uint64
	buf      []byte
	ooo      map[uint64]*segment // lookup only
	oooBytes int
	eof      bool
}

func (d *direction) sender() *Conn   { return d.c.endpoint(d.dir) }
func (d *direction) receiver() *Conn { return d.c.endpoint(1 - d.dir) }

// newConnection allocates connection c<k> (k from 1, creation order) in startDial (a driver event).
// Lock held.
func (st *Stack) newConnection(cliNode, srvNode kernel.NodeID, srvPort uint16, dr *dialRecord) *connection {
	k := len(st.conns) + 1
	c := &connection{k: k, id: "c" + strconv.Itoa(k), cliNode: cliNode, srvNode: srvNode, srvPort: srvPort, dial: dr}
	for dir := range 2 {
		c.dirs[dir] = &direction{c: c, dir: dir, nextSeq: 1, rcvNext: 1, ooo: map[uint64]*segment{}}
	}
	st.conns = append(st.conns, c)
	return c
}

// live reports whether ep exists in its host's table, belongs to its node's current incarnation and
// is neither reset nor timed out (NSM §3). Lock held.
func (st *Stack) live(ep *Conn) bool {
	return ep != nil && ep.state != stRemoved && ep.errno == nil && st.cur(ep.h.id) == ep.h
}

// attempt makes one transmission of s (NSM-031). Driver side; the sender node is up.
func (st *Stack) attempt(s *segment) {
	a, b := s.c.ends(s.dir)
	now := st.s.Now()
	s.attempts++
	s.last = now
	if s.attempts == 1 {
		s.first = now
	}
	outcome, delay, send := "", time.Duration(0), false
	switch {
	case a == b:
		outcome, delay, send = "loopback", st.cfg.Loopback, true
	case !st.nw.Connected(a, b):
		outcome = "held"
	default:
		if dec := st.nw.Decide(a, b); dec.Drop {
			outcome = "lost"
		} else {
			outcome, delay, send = "sent", dec.Delay, true
		}
	}
	st.emitSegment(a, s, outcome, delay) // before SendRaw, so net.send_raw's cause is this record
	if send {
		st.nw.SendRaw(st.s.Node(a), b, s, simnet.RawOptions{Delay: delay, FIFO: a == b})
	}
}

// sendUnreliable makes the single attempt of a synack or rst (NSM-030).
func (st *Stack) sendUnreliable(c *connection, dir int, kind segKind) {
	st.attempt(&segment{c: c, dir: dir, kind: kind})
}

// transmit is attempt + unacked + arm for a reliable segment (NSM-032).
func (st *Stack) transmit(s *segment) {
	st.attempt(s)
	d := s.c.dirs[s.dir]
	d.unacked = append(d.unacked, s)
	st.arm(d)
}

// rto is min(base << min(attempts-1, 30), RTOMax), computed without overflow (NSM-032).
func (st *Stack) rto(s *segment) time.Duration {
	base := st.cfg.RTOMin
	if s.kind == kSyn {
		base = st.cfg.SynRTO
	}
	sh := min(s.attempts-1, 30)
	if base > st.cfg.RTOMax>>sh {
		return st.cfg.RTOMax
	}
	return min(base<<sh, st.cfg.RTOMax)
}

func (st *Stack) deadline(s *segment) kernel.Time { return s.last.Add(st.rto(s)) }

// arm schedules the direction's retransmission check (NSM-032). Sim.At panics for a time in the
// past, and after an acknowledgment the new head's deadline may have passed, so such a deadline is
// checked now.
func (st *Stack) arm(d *direction) {
	if d.checkPending || len(d.unacked) == 0 {
		return
	}
	d.checkPending = true
	at := max(st.deadline(d.unacked[0]), st.s.Now())
	st.s.At(at, "net.rto", func() { st.check(d) })
}

// check is the retransmission check (NSM-032), a global kernel event.
func (st *Stack) check(d *direction) {
	snd := d.sender()
	if !st.live(snd) {
		return
	}
	if n := st.s.Node(snd.h.id); n.State() == kernel.NodePaused {
		n.Post("net.rto", func() { st.check(d) })
		return
	}
	d.checkPending = false
	for len(d.unacked) > 0 && d.unacked[0].acked {
		d.unacked = d.unacked[1:]
	}
	if len(d.unacked) == 0 {
		return
	}
	s := d.unacked[0]
	now := st.s.Now()
	if now < st.deadline(s) {
		st.arm(d)
		return
	}
	if now.Sub(s.first) >= st.cfg.ConnTimeout {
		st.timeout(d, s)
		return
	}
	st.attempt(s)
	st.arm(d)
}

// timeout times the sender endpoint out (NSM-033).
func (st *Stack) timeout(d *direction, s *segment) {
	c := d.c
	snd, _ := c.ends(d.dir)
	st.emit(snd, "net.conn.timeout", fmt.Sprintf("timeout %s %s seq=%d", c.id, dirNames[d.dir], s.seq),
		kernel.Attr{Key: "conn", Value: c.id}, kernel.Attr{Key: "dir", Value: dirNames[d.dir]},
		kernel.Attr{Key: "seq", Value: strconv.FormatUint(s.seq, 10)},
		kernel.Attr{Key: "waited_ns", Value: strconv.FormatInt(int64(st.s.Now().Sub(s.first)), 10)})
	if s.kind == kSyn {
		st.failDial(c, &os.SyscallError{Syscall: "connect", Err: syscall.ETIMEDOUT}, false)
		return
	}
	ep := d.sender()
	ep.errno = syscall.ETIMEDOUT
	st.discard(ep)
	ep.wake()
	st.removeEndpoint(ep)
}

// ack acknowledges s (NSM-037's first step). A closed sender endpoint is removed once its fin was
// sent and its direction has no unacknowledged segment left (NSM-045).
func (st *Stack) ack(d *direction, s *segment) {
	if s.acked {
		return
	}
	s.acked = true
	if i := slices.Index(d.unacked, s); i >= 0 { // not listed after discard, which zeroed inflight
		d.unacked = slices.Delete(d.unacked, i, i+1)
		if s.kind == kData {
			d.inflight -= len(s.data)
		}
	}
	snd := d.sender()
	if snd == nil {
		return
	}
	snd.wake() // send space freed
	if snd.closed && d.finSent && len(d.unacked) == 0 {
		st.removeEndpoint(snd)
	}
}

// discard drops ep's buffers and unacknowledged segments.
func (st *Stack) discard(ep *Conn) {
	d, r := ep.c.dirs[ep.side], ep.c.dirs[1-ep.side]
	d.queued, d.unacked, d.inflight = nil, nil, 0
	r.buf, r.oooBytes = nil, 0
	r.ooo = map[uint64]*segment{}
}

// reset resets ep (NSM-041).
func (st *Stack) reset(ep *Conn, reason string) {
	ep.errno = syscall.ECONNRESET
	st.discard(ep)
	if l := ep.lst; l != nil {
		if ep.state == stSynRcvd {
			l.synrcvd--
		}
		if ep.inAcceptQueue {
			l.dequeue(ep)
		}
	}
	st.removeEndpoint(ep)
	ep.wake()
	st.emit(ep.h.id, "net.conn.reset", "reset "+ep.c.id+": "+reason,
		kernel.Attr{Key: "conn", Value: ep.c.id}, kernel.Attr{Key: "reason", Value: reason})
}

// removeEndpoint removes ep from its host's table; a client endpoint's port is freed with it.
func (st *Stack) removeEndpoint(ep *Conn) {
	if ep.state == stRemoved {
		return
	}
	ep.state = stRemoved
	ep.h.dropEndpoint(ep)
}

// failDial ends a dial with err (NSM-025): the client endpoint and the server's half-open endpoint
// are removed; refused adds net.conn.refused.
func (st *Stack) failDial(c *connection, err error, refused bool) {
	dr := c.dial
	c.dial = nil
	if cli := c.cli; cli != nil {
		st.discard(cli)
		st.removeEndpoint(cli)
		cli.wake()
	}
	if refused {
		st.emit(c.cliNode, "net.conn.refused", "refused "+c.id, kernel.Attr{Key: "conn", Value: c.id})
	}
	if srv := c.srv; srv != nil && srv.state == stSynRcvd {
		srv.lst.synrcvd--
		st.removeEndpoint(srv)
		st.emit(c.srvNode, "net.conn.reset", "reset "+c.id+": handshake-aborted",
			kernel.Attr{Key: "conn", Value: c.id}, kernel.Attr{Key: "reason", Value: "handshake-aborted"})
	}
	if dr != nil {
		dr.h.removeDial(dr)
		dr.finish(nil, err)
	}
}

// arrive dispatches a segment arriving at node id (handler, receiving node up).
func (st *Stack) arrive(id kernel.NodeID, s *segment) {
	switch s.kind {
	case kSyn:
		st.arriveSyn(id, s)
	case kSynAck:
		st.arriveSynAck(id, s)
	case kData, kFin:
		st.arriveData(id, s)
	case kRst:
		st.arriveRst(id, s)
	}
}

// arriveSyn is NSM-023.
func (st *Stack) arriveSyn(id kernel.NodeID, s *segment) {
	c, h := s.c, st.cur(id)
	if srv := c.srv; srv != nil && srv.state != stRemoved && srv.h == h {
		st.emitArrive(id, s, "duplicate")
		if srv.state == stSynRcvd {
			st.sendUnreliable(c, s2c, kSynAck)
		}
		return
	}
	if cli := c.cli; !st.live(cli) || cli.state != stDialing { // a late copy: the dial ended or completed
		st.emitArrive(id, s, "ignored")
		return
	}
	var l *Listener
	if h != nil && !h.gone {
		l = h.listeners[c.srvPort]
	}
	if l == nil {
		st.emitArrive(id, s, "refused")
		st.sendUnreliable(c, s2c, kRst)
		return
	}
	if l.synrcvd+len(l.queue) >= st.cfg.Backlog {
		st.emitArrive(id, s, "backlog-full")
		return
	}
	srv := newConn(c, 1, h, stSynRcvd, c.cli.remote, c.cli.local)
	srv.lst = l
	c.srv = srv
	h.addEndpoint(srv)
	l.synrcvd++
	st.emitArrive(id, s, "synrcvd")
	st.sendUnreliable(c, s2c, kSynAck)
}

// arriveSynAck is NSM-024.
func (st *Stack) arriveSynAck(id kernel.NodeID, s *segment) {
	c := s.c
	cli := c.cli
	switch {
	case st.live(cli) && cli.state == stEstablished:
		st.emitArrive(id, s, "duplicate")
		return
	case !st.live(cli) || cli.state != stDialing:
		st.emitArrive(id, s, "no-conn")
		st.sendUnreliable(c, c2s, kRst)
		return
	case c.dial != nil && c.dial.abandoned: // its net.dial_abort has not run yet
		st.emitArrive(id, s, "no-conn")
		st.failDial(c, errAbandoned, false)
		st.sendUnreliable(c, c2s, kRst)
		return
	}
	cli.state = stEstablished
	d := c.dirs[c2s]
	var syn *segment
	if len(d.unacked) > 0 {
		syn = d.unacked[0] // a dialing client has sent nothing but the syn
	}
	st.emitArrive(id, s, "established")
	rtt := time.Duration(0)
	if syn != nil {
		rtt = st.s.Now().Sub(syn.first)
		st.ack(d, syn)
	}
	st.emit(c.cliNode, "net.conn.established", "established "+c.id,
		kernel.Attr{Key: "conn", Value: c.id}, kernel.Attr{Key: "rtt_ns", Value: strconv.FormatInt(int64(rtt), 10)})
	if dr := c.dial; dr != nil {
		c.dial = nil
		dr.h.removeDial(dr)
		dr.finish(cli, nil)
	}
	if srv := c.srv; srv != nil && srv.state == stSynRcvd && st.cur(c.srvNode) == srv.h {
		srv.state = stEstablished
		srv.lst.synrcvd--
		srv.lst.enqueue(srv)
		st.emit(c.srvNode, "net.conn.accept", "accept queue "+c.id,
			kernel.Attr{Key: "conn", Value: c.id}, kernel.Attr{Key: "port", Value: strconv.Itoa(int(c.srvPort))})
	}
}

// arriveData is NSM-027 (no live endpoint) and NSM-037 (live endpoint).
func (st *Stack) arriveData(id kernel.NodeID, s *segment) {
	c := s.c
	ep := c.endpoint(1 - s.dir)
	if !st.live(ep) {
		st.emitArrive(id, s, "no-conn")
		st.sendUnreliable(c, 1-s.dir, kRst)
		return
	}
	d := c.dirs[s.dir]
	st.ack(d, s)
	if s.seq < d.rcvNext || d.ooo[s.seq] != nil {
		st.emitArrive(id, s, "duplicate")
		return
	}
	if ep.closed && s.kind == kData {
		st.emitArrive(id, s, "closed")
		st.sendUnreliable(c, 1-s.dir, kRst)
		st.reset(ep, "abort")
		return
	}
	if s.seq != d.rcvNext {
		d.ooo[s.seq] = s
		d.oooBytes += len(s.data)
		st.emitArrive(id, s, "buffered")
		return
	}
	st.deliver(ep, d, s)
	for {
		n := d.ooo[d.rcvNext]
		if n == nil {
			break
		}
		delete(d.ooo, n.seq)
		d.oooBytes -= len(n.data)
		st.deliver(ep, d, n)
	}
	ep.wake()
	st.emitArrive(id, s, "in-order")
}

// deliver hands one in-order segment to the receiving endpoint (NSM-037).
func (st *Stack) deliver(ep *Conn, d *direction, s *segment) {
	d.rcvNext++
	if s.kind == kFin {
		d.eof = true
		return
	}
	if !ep.rdShut {
		d.buf = append(d.buf, s.data...)
	}
}

// arriveRst is NSM-039.
func (st *Stack) arriveRst(id kernel.NodeID, s *segment) {
	ep := s.c.endpoint(1 - s.dir)
	switch {
	case st.live(ep) && ep.side == 0 && ep.state == stDialing:
		st.emitArrive(id, s, "refused")
		st.failDial(s.c, &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}, true)
	case st.live(ep):
		st.emitArrive(id, s, "reset")
		st.reset(ep, "rst")
	default:
		st.emitArrive(id, s, "ignored")
	}
}
