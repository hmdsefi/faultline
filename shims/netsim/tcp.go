// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"fmt"
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
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
	first    kernel.Time //nolint:unused // retransmission state, not wired up yet
	last     kernel.Time //nolint:unused // retransmission state, not wired up yet
	attempts int         //nolint:unused // retransmission state, not wired up yet
	acked    bool        //nolint:unused // retransmission state, not wired up yet
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
//
//nolint:unused // byte-stream state, not wired up yet
type direction struct {
	c   *connection
	dir int
	// sender
	queued       []byte
	inflight     int
	nextSeq      uint64
	finPending   bool
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
