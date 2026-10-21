// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package netsim provides net.Conn, net.Listener and net.PacketConn implementations over the
// simulated network, for processes in faultline goroutine mode.
package netsim

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
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
		case *Datagram:
			st.deliverDatagram(id, p)
		default:
			st.emitDrop(id, st.s.Node(from).Name(), st.s.Node(id).Name(), "0", "foreign")
		}
	}
}

// Release closes everything h owns, as for a process exit. It runs on the driver goroutine with
// the lock held.
func (st *Stack) Release(h *Host) {
	for _, port := range slices.Clone(h.uports) {
		st.closeUDP(h.udps[port])
		st.emitUDPClose(h.id, port, "exit")
	}
	h.gone = true
	for _, port := range h.lports { // a blocked Accept sees the host gone and returns net.ErrClosed
		h.listeners[port].wake()
	}
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
		for _, port := range slices.Clone(h.uports) {
			st.closeUDP(h.udps[port])
		}
		for _, port := range h.lports {
			h.listeners[port].wake()
		}
	}
}

// onCrash is the crash hook (NSM-060); it runs after the driver's (GOR-051).
func (st *Stack) onCrash(n *kernel.Node) {
	h := st.cur(n.ID())
	if h == nil {
		return
	}
	h.gone = true
	for _, port := range h.lports {
		h.listeners[port].wake()
	}
	for _, port := range slices.Clone(h.uports) { // step 3
		st.closeUDP(h.udps[port])
		st.emitUDPClose(h.id, port, "crash")
	}
}
