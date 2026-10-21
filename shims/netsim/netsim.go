// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package netsim provides net.Conn, net.Listener and net.PacketConn implementations over the
// simulated network, for processes in faultline goroutine mode.
package netsim

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Driver is the subset of the goroutine-mode driver contract that netsim uses.
type Driver interface {
	Lock()
	Unlock()
	Alive(node kernel.NodeID, inc uint32) bool
	Post(node kernel.NodeID, inc uint32, kind, key string, run, cancel func())
	OnDriver() bool
}

// MaxDatagram is the largest UDP payload (65535 - 8 - 20).
const MaxDatagram = 65507

// ErrProcessGone is returned (wrapped in *net.OpError) by Host methods of an incarnation that
// crashed, exited, or whose run ended.
var ErrProcessGone = errors.New("netsim: process is not running")

// Config configures a Stack. Zero fields take the defaults of DefaultConfig.
type Config struct {
	MSS           int           // max data bytes per segment; default 1460; 64..65535
	SendBuffer    int           // per endpoint; default 65536; >= MSS
	RecvBuffer    int           // per endpoint; default 65536; >= MSS
	Backlog       int           // per listener (half-open + not yet accepted); default 128; >= 1
	ConnTimeout   time.Duration // default 30s; > 0
	RTOMin        time.Duration // first data/fin retransmission timeout; default 200ms; > 0
	RTOMax        time.Duration // retransmission timeout cap; default 10s; >= RTOMin and >= SynRTO
	SynRTO        time.Duration // first syn retransmission timeout; default 1s; > 0
	Loopback      time.Duration // one-way delay on a node's own links; default 10µs; 0..simnet.MaxDelay
	UDPRecvBuffer int           // per UDP socket, bytes; default 212992; >= MaxDatagram
}

// DefaultConfig returns the defaults listed on Config.
func DefaultConfig() Config {
	return Config{
		MSS: 1460, SendBuffer: 65536, RecvBuffer: 65536, Backlog: 128,
		ConnTimeout: 30 * time.Second, RTOMin: 200 * time.Millisecond, RTOMax: 10 * time.Second,
		SynRTO: time.Second, Loopback: 10 * time.Microsecond, UDPRecvBuffer: 212992,
	}
}

// withDefaults replaces zero fields by DefaultConfig's values.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MSS == 0 {
		c.MSS = d.MSS
	}
	if c.SendBuffer == 0 {
		c.SendBuffer = d.SendBuffer
	}
	if c.RecvBuffer == 0 {
		c.RecvBuffer = d.RecvBuffer
	}
	if c.Backlog == 0 {
		c.Backlog = d.Backlog
	}
	if c.ConnTimeout == 0 {
		c.ConnTimeout = d.ConnTimeout
	}
	if c.RTOMin == 0 {
		c.RTOMin = d.RTOMin
	}
	if c.RTOMax == 0 {
		c.RTOMax = d.RTOMax
	}
	if c.SynRTO == 0 {
		c.SynRTO = d.SynRTO
	}
	if c.Loopback == 0 {
		c.Loopback = d.Loopback
	}
	if c.UDPRecvBuffer == 0 {
		c.UDPRecvBuffer = d.UDPRecvBuffer
	}
	return c
}

// Validate applies defaults to zero fields and reports the first invalid field, or nil.
func (c Config) Validate() error {
	// The checks run in the order of NSM §7's message list: field order, except that SynRTO comes
	// before RTOMax because the RTOMax message needs both minimums valid.
	c = c.withDefaults()
	switch {
	case c.MSS < 64 || c.MSS > 65535:
		return fmt.Errorf("invalid netsim config: MSS %d out of range [64, 65535]", c.MSS)
	case c.SendBuffer < c.MSS:
		return fmt.Errorf("invalid netsim config: SendBuffer %d is smaller than MSS %d", c.SendBuffer, c.MSS)
	case c.RecvBuffer < c.MSS:
		return fmt.Errorf("invalid netsim config: RecvBuffer %d is smaller than MSS %d", c.RecvBuffer, c.MSS)
	case c.Backlog < 1:
		return fmt.Errorf("invalid netsim config: Backlog %d must be >= 1", c.Backlog)
	case c.ConnTimeout <= 0:
		return fmt.Errorf("invalid netsim config: ConnTimeout %v must be > 0", c.ConnTimeout)
	case c.RTOMin <= 0:
		return fmt.Errorf("invalid netsim config: RTOMin %v must be > 0", c.RTOMin)
	case c.SynRTO <= 0:
		return fmt.Errorf("invalid netsim config: SynRTO %v must be > 0", c.SynRTO)
	case c.RTOMax < c.RTOMin || c.RTOMax < c.SynRTO:
		return fmt.Errorf("invalid netsim config: RTOMax %v must be >= RTOMin and SynRTO", c.RTOMax)
	case c.Loopback < 0 || c.Loopback > simnet.MaxDelay:
		return fmt.Errorf("invalid netsim config: Loopback %v out of range [0s, 24h0m0s]", c.Loopback)
	case c.UDPRecvBuffer < MaxDatagram:
		return fmt.Errorf("invalid netsim config: UDPRecvBuffer %d is smaller than 65507", c.UDPRecvBuffer)
	}
	return nil
}

// Stack is the netsim state of one world.
type Stack struct {
	s     *kernel.Sim
	nw    *simnet.Network
	d     Driver
	cfg   Config        // defaults applied
	nodes []*nodeNet    // indexed by kernel.NodeID
	conns []*connection // conns[k-1] is c<k>
}

// nodeNet is per-node state that outlives incarnations.
type nodeNet struct {
	host   *Host  // the current incarnation's host; nil before the first boot as a process
	ever   bool   // the node has had a netsim host (NSM-022)
	tcpCtr uint64 // NSM-016 ephemeral-port counters
	udpCtr uint64
}

// New creates the Stack of s and nw and registers its crash hook with s.OnCrash. It panics if
// s, nw or d is nil or if cfg is invalid.
func New(s *kernel.Sim, nw *simnet.Network, d Driver, cfg Config) *Stack {
	switch {
	case s == nil:
		panic("netsim: New: nil *kernel.Sim")
	case nw == nil:
		panic("netsim: New: nil *simnet.Network")
	case d == nil:
		panic("netsim: New: nil Driver")
	}
	if err := cfg.Validate(); err != nil {
		panic("netsim: New: invalid Config: " + err.Error())
	}
	st := &Stack{s: s, nw: nw, d: d, cfg: cfg.withDefaults()}
	s.OnCrash(st.onCrash)
	return st
}

func (st *Stack) node(id kernel.NodeID) *nodeNet {
	for int(id) >= len(st.nodes) {
		st.nodes = append(st.nodes, nil)
	}
	if st.nodes[id] == nil {
		st.nodes[id] = &nodeNet{}
	}
	return st.nodes[id]
}

// cur returns node id's current host, or nil.
func (st *Stack) cur(id kernel.NodeID) *Host {
	if int(id) >= len(st.nodes) || st.nodes[id] == nil {
		return nil
	}
	return st.nodes[id].host
}

// Host is the network interface of one process incarnation. Its methods are safe for concurrent
// use from process goroutines and must not be called from kernel callbacks.
type Host struct {
	st   *Stack
	n    *kernel.Node
	id   kernel.NodeID
	inc  uint32
	addr netip.Addr
	gone bool // released, crashed or shut down

	listeners map[uint16]*Listener // bound TCP listeners by port; lookups only (NSM-011)
	lports    []uint16             // the ports of listeners, ascending
	eps       map[int]*Conn        // TCP endpoints by epKey (connection number and side); lookups only
	eks       []int                // the keys of eps, ascending
	udps      map[uint16]*UDPConn  // bound UDP sockets by port; lookups only (NSM-011)
	uports    []uint16             // the ports of udps, ascending
	dials     []*dialRecord        // pending TCP dials, in posting order
	reqs      []*request           // posted requests in posting order; finished ones leave lazily (dropRequest)
	finished  int                  // the requests in reqs that ran or were cancelled

	// Set by Detach for Release.
	held   []*request  // the requests Detach took over, in (key, posting) order
	exitLs []*Listener // the listeners Detach unbound, ascending port
	exitUs []uint16    // the ports of the UDP sockets Detach closed, ascending
}

// request is a request that netsim posted for a host; done marks one that ran or was cancelled. A
// process exit carries out the ones that are not done (NSM-061).
type request struct {
	kind, key string
	run       func()
	taken     bool // Detach took it over: Release runs it, and the driver's run and cancel do nothing
	done      bool // it ran or was cancelled; it may still be in h.reqs
}

// post posts a request of h's incarnation (GOR-030) and tracks it in h.reqs until it runs or is
// cancelled. Lock held.
func (st *Stack) post(h *Host, kind, key string, run, cancel func()) {
	r := &request{kind: kind, key: key, run: run}
	h.reqs = append(h.reqs, r)
	st.d.Post(h.id, h.inc, kind, key, func() {
		if !r.taken {
			h.dropRequest(r)
			run()
		}
	}, func() {
		if !r.taken {
			h.dropRequest(r)
			cancel()
		}
	})
}

// dropRequest marks r finished. Finished requests stay in h.reqs until more than half of it is
// finished; one pass then removes them all and keeps posting order. Each request is removed once
// that way, so n requests cost O(n) in any order the driver runs them in. Lock held.
func (h *Host) dropRequest(r *request) {
	if r.done {
		return
	}
	r.done = true
	h.finished++
	if h.finished > len(h.reqs)/2 {
		h.reqs = slices.DeleteFunc(h.reqs, func(q *request) bool { return q.done })
		h.finished = 0
	}
}

// Host creates the Host of n's current incarnation and installs n's simnet handler. It runs on
// the driver goroutine with the lock held.
func (st *Stack) Host(n *kernel.Node) *Host {
	h := &Host{st: st, n: n, id: n.ID(), inc: n.Incarnation(), addr: NodeAddr(n.ID()),
		listeners: map[uint16]*Listener{}, eps: map[int]*Conn{}, udps: map[uint16]*UDPConn{}}
	nn := st.node(n.ID())
	nn.host, nn.ever = h, true
	st.nw.Handle(n, st.handler(n.ID()))
	return h
}

// Name returns the name of h's node.
func (h *Host) Name() string { return h.n.Name() }

// Addr returns the address of h's node: 10.0.0.0 plus the node ID.
func (h *Host) Addr() netip.Addr { return h.addr }

// Incarnation returns the incarnation of the process that h belongs to.
func (h *Host) Incarnation() uint32 { return h.inc }

// alive is NSM-004's ghost check. Lock held.
func (h *Host) alive() bool { return !h.gone && h.st.d.Alive(h.id, h.inc) }

// handler demultiplexes simnet payloads for node id by type (NSM-010).
func (st *Stack) handler(id kernel.NodeID) simnet.Handler {
	return func(from kernel.NodeID, payload any) {
		switch p := payload.(type) {
		case *segment:
			st.arrive(id, p)
		case *Datagram:
			st.deliverDatagram(id, p)
		default:
			st.emitDrop(id, st.s.Node(from).Name(), st.s.Node(id).Name(), "0", "foreign")
		}
	}
}

// Detach is the part of Release that may run on a process goroutine, when a process ends there by
// returning an error, panicking or calling t.FailNow. It marks h gone and closes everything h owns
// at once, as the Close methods do before they post their requests: listeners are unbound, open
// endpoints marked closed, UDP sockets unbound, blocked calls woken, and pending dials fail with
// ErrProcessGone. It also takes over h's pending requests other than dials. Detach emits no records
// and makes no simnet calls; Release, called later on the driver goroutine, does that work. A second
// call finds nothing left to do. Lock held.
func (st *Stack) Detach(h *Host) {
	h.gone = true
	var dials []*request // a dial never starts after the exit; Detach fails its record below
	for _, r := range h.reqs {
		switch {
		case r.done: // finished, not yet removed
		case r.kind == "net.dial":
			dials = append(dials, r)
		default:
			r.taken = true
			h.held = append(h.held, r)
		}
	}
	h.reqs, h.finished = dials, 0
	slices.SortStableFunc(h.held, func(a, b *request) int { return strings.Compare(a.key, b.key) })
	for _, port := range slices.Clone(h.lports) {
		l := h.listeners[port]
		st.unbindListener(l)
		h.exitLs = append(h.exitLs, l)
	}
	for _, ep := range h.endpoints() {
		if !ep.closed {
			ep.closed, ep.exitClose = true, true
			ep.wake()
		}
	}
	for _, dr := range slices.Clone(h.dials) {
		h.removeDial(dr)
		dr.finish(nil, ErrProcessGone)
	}
	for _, port := range slices.Clone(h.uports) {
		st.closeUDP(h.udps[port])
		h.exitUs = append(h.exitUs, port)
	}
}

// Release closes everything h owns, as for a process exit. It first carries out the requests that
// h's process posted and that have not run, except dials, in the order the driver runs one key's
// requests: bytes and datagrams written right before the exit are still sent, and a Close called
// right before it takes effect. Then it closes h's listeners, endpoints and UDP sockets. It runs on
// the driver goroutine with the lock held, while h's node is up.
func (st *Stack) Release(h *Host) {
	st.Detach(h) // nothing left to do if the driver detached h on a process goroutine
	held := h.held
	h.held = nil
	for _, r := range held {
		r.run()
	}
	for _, l := range h.exitLs {
		st.unlistenRun(l, "exit", true)
	}
	for _, ep := range h.endpoints() {
		switch {
		case ep.state == stRemoved: // removed earlier in this walk
		case ep.side == 0 && ep.state == stDialing:
			st.failDial(ep.c, ErrProcessGone, false)
		case ep.exitClose:
			ep.exitClose = false
			st.closeRun(ep) // as Conn.Close, run at once
		}
	}
	for _, port := range h.exitUs {
		st.emitUDPClose(h.id, port, "exit")
	}
	h.exitLs, h.exitUs = nil, nil
}

// Shutdown ends all network activity at the end of the run. It runs on the driver goroutine with
// the lock held, emits no records and makes no simnet calls.
func (st *Stack) Shutdown() {
	for _, nn := range st.nodes {
		if nn == nil || nn.host == nil {
			continue
		}
		h := nn.host
		h.gone = true
		for _, ep := range h.endpoints() {
			if ep.errno == nil {
				ep.errno = net.ErrClosed
			}
			ep.wake()
		}
		for _, port := range slices.Clone(h.lports) {
			st.unbindListener(h.listeners[port])
		}
		for _, port := range slices.Clone(h.uports) {
			st.closeUDP(h.udps[port])
		}
		for _, dr := range slices.Clone(h.dials) {
			h.removeDial(dr)
			dr.finish(nil, ErrProcessGone)
		}
	}
}

// onCrash is the crash hook (NSM-060). It runs after the driver's hook (GOR-051), lock held.
func (st *Stack) onCrash(n *kernel.Node) {
	id := n.ID()
	h := st.cur(id)
	if h == nil {
		return
	}
	// 1. endpoints, ascending (connection ID, side)
	for _, ep := range h.endpoints() {
		switch {
		case ep.state == stRemoved: // removed earlier in this walk
		case ep.side == 0 && ep.state == stDialing:
			st.failDial(ep.c, ErrProcessGone, false) // a pending dial of the node (NSM-025)
		case ep.state == stSynRcvd:
			// half-open: removed without a record and without notifying the dialing peer, which must
			// never be reset (NSM-041); it retransmits syn and gets rst after a restart
			ep.lst.synrcvd--
			st.removeEndpoint(ep)
		case ep.inAcceptQueue && ep.lst.h.listeners[ep.lst.port] == ep.lst:
			// queued on a bound listener: step 2 resets it without notifying the peer
		default:
			st.notifyPeer(id, ep)
			st.reset(ep, "crash")
		}
	}
	for _, dr := range slices.Clone(h.dials) { // dials whose net.dial request has not run
		h.removeDial(dr)
		dr.finish(nil, ErrProcessGone)
	}
	// 2. listeners, ascending port
	for _, port := range slices.Clone(h.lports) {
		l := h.listeners[port]
		st.unbindListener(l)
		st.unlistenRun(l, "crash", false)
	}
	// 3. UDP sockets, ascending port
	for _, port := range slices.Clone(h.uports) {
		st.closeUDP(h.udps[port])
		st.emitUDPClose(id, port, "crash")
	}
	h.gone = true
}

// notifyPeer schedules the crash notification of ep's peer on another, connected node (NSM-060).
func (st *Stack) notifyPeer(crashed kernel.NodeID, ep *Conn) {
	peer := ep.c.endpoint(1 - ep.side)
	if peer == nil || peer.h.id == crashed || !st.nw.Connected(crashed, peer.h.id) {
		return
	}
	lat := st.nw.Link(crashed, peer.h.id).Latency
	st.s.At(st.s.Now().Add(lat), "net.rst", func() { st.peerCrashed(peer) })
	name := peer.h.n.Name()
	st.emit(crashed, "net.conn.rst", "crash rst "+ep.c.id+" -> "+name,
		kernel.Attr{Key: "conn", Value: ep.c.id}, kernel.Attr{Key: "peer", Value: name},
		kernel.Attr{Key: "delay_ns", Value: strconv.FormatInt(int64(lat), 10)})
}

// peerCrashed resets p if it is still live; on a paused node it is deferred to resume.
func (st *Stack) peerCrashed(p *Conn) {
	if !st.live(p) {
		return
	}
	if n := st.s.Node(p.h.id); n.State() == kernel.NodePaused {
		n.Post("net.rst", func() { st.peerCrashed(p) })
		return
	}
	st.reset(p, "peer-crashed")
}
