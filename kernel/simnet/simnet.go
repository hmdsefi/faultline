// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package simnet simulates a datagram network between kernel nodes: per-link latency, jitter,
// tail latency, loss, duplication and FIFO order, and a gograph topology with partitions.
//
// All methods must be called from the simulation goroutine (inside kernel callbacks or between
// Run calls). Network is not safe for concurrent use.
package simnet

import (
	"fmt"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/gograph"
)

// Config configures a Network.
type Config struct {
	// Default is the configuration of every link without an override.
	Default Link
	// NoDeliveryCheck skips the partition check at delivery time, so only the topology at send
	// time matters.
	NoDeliveryCheck bool
}

// DefaultConfig returns the configuration used by faultline.Run when Options.Net is the zero
// value: Default = Link{Latency: 1ms, Jitter: 4ms}, NoDeliveryCheck = false.
func DefaultConfig() Config {
	return Config{Default: Link{Latency: time.Millisecond, Jitter: 4 * time.Millisecond}}
}

// Handler receives one delivered copy. from is the sender; payload is the value passed to Send,
// by reference.
type Handler func(from kernel.NodeID, payload any)

// Network is the simulated network of one kernel.Sim.
type Network struct {
	s   *kernel.Sim
	cfg Config

	// Registration may reallocate nodes: do not hold &nodes[i] across a handler call or an
	// exported method.
	nodes    []nodeState                      // index: NodeID-1, in registration order
	links    [][]*linkState                   // links[from-1][to-1]; nil until first use
	graph    gograph.Graph[kernel.NodeID]     // source of truth for Topology (NET-022)
	vertices []*gograph.Vertex[kernel.NodeID] // index: NodeID-1
	adj      [][]bool                         // adj[from-1][to-1]: dense mirror of graph edges
	version  uint64                           // TopologyVersion
	lastMsg  uint64                           // last message ID (NET-011)
	stats    Stats                            // network totals
}

// nodeState is the per-node state of the network.
type nodeState struct {
	node    *kernel.Node
	name    string
	handler Handler // nil: no handler
	inc     uint32  // incarnation the handler belongs to (NET-008)
	parked  []*copyMsg
}

// copyMsg is one scheduled delivery of a message (a copy).
type copyMsg struct {
	id      uint64 // message ID
	copyNo  uint8  // 1 or 2
	from    kernel.NodeID
	to      kernel.NodeID
	payload any
	sentAt  kernel.Time
	parked  bool // in the destination's parked list (NET-019)
}

// New creates the network of s and registers its crash hook with s.OnCrash. It panics if s is
// nil or cfg.Default is invalid. Create at most one Network per Sim.
func New(s *kernel.Sim, cfg Config) *Network {
	if s == nil {
		panic("simnet: New: nil *kernel.Sim")
	}
	if err := cfg.Default.Validate(); err != nil {
		panic("simnet: New: invalid Config.Default: " + err.Error())
	}
	nw := &Network{
		s:     s,
		cfg:   cfg,
		graph: gograph.New[kernel.NodeID](gograph.Directed()),
	}
	s.OnCrash(nw.onCrash)
	return nw
}

// Sim returns the simulation this network belongs to.
func (nw *Network) Sim() *kernel.Sim { return nw.s }

// Config returns the configuration passed to New.
func (nw *Network) Config() Config { return nw.cfg }

// register adds every node of the Sim that the network has not registered yet, in ascending ID
// order (NET-002).
func (nw *Network) register() {
	for {
		n := nw.s.Node(kernel.NodeID(len(nw.nodes) + 1)) //nolint:gosec // at most one past the last NodeID; a wrap gives an ID < 1, and Node returns nil
		if n == nil {
			return
		}
		nw.addNode(n)
	}
}

// addNode registers n: a vertex, then edges j -> k and k -> j for every registered j, ascending.
func (nw *Network) addNode(n *kernel.Node) {
	k := len(nw.nodes)
	nw.nodes = append(nw.nodes, nodeState{node: n, name: n.Name()})
	nw.vertices = append(nw.vertices, nw.graph.AddVertexByLabel(n.ID()))
	for i := range nw.adj {
		nw.adj[i] = append(nw.adj[i], false)
		nw.links[i] = append(nw.links[i], nil)
	}
	nw.adj = append(nw.adj, make([]bool, k+1))
	nw.links = append(nw.links, make([]*linkState, k+1))
	for j := 0; j < k; j++ {
		nw.addEdge(j, k)
		nw.addEdge(k, j)
	}
	nw.version++
}

// addEdge adds the edge i -> j (indices) to the graph and the mirror.
func (nw *Network) addEdge(i, j int) {
	if _, err := nw.graph.AddEdge(nw.vertices[i], nw.vertices[j]); err != nil {
		panic("simnet: internal error: add edge: " + err.Error())
	}
	nw.adj[i][j] = true
}

// removeEdge removes the edge i -> j (indices) from the graph and the mirror.
func (nw *Network) removeEdge(i, j int) {
	nw.graph.RemoveEdges(nw.graph.GetEdge(nw.vertices[i], nw.vertices[j]))
	nw.adj[i][j] = false
}

// index validates id (NET-003) and returns its index.
func (nw *Network) index(method string, id kernel.NodeID) int {
	if id < 1 || int(id) > len(nw.nodes) {
		panic(fmt.Sprintf("simnet: %s: unknown node id %d", method, id))
	}
	return int(id) - 1
}

// nodeIndex validates n (NET-003) and returns its index.
func (nw *Network) nodeIndex(method string, n *kernel.Node) int {
	if n == nil {
		panic(fmt.Sprintf("simnet: %s: nil *kernel.Node", method))
	}
	if n.Sim() != nw.s {
		panic(fmt.Sprintf("simnet: %s: node %s belongs to a different Sim", method, n.Name()))
	}
	return int(n.ID()) - 1
}

// stateWord names a node state in panic messages (NET §7).
func stateWord(st kernel.NodeState) string {
	switch st {
	case kernel.NodeDown:
		return "down"
	case kernel.NodePaused:
		return "paused"
	}
	return "not up"
}

// name returns the registered name of id.
func (nw *Network) name(id kernel.NodeID) string { return nw.nodes[id-1].name }

// emit emits one record. node 0 means a global record; otherwise Emit fills Inc with the node's
// current incarnation (KRN-090).
func (nw *Network) emit(kind string, node kernel.NodeID, text string, attrs ...kernel.Attr) {
	nw.s.Emit(kernel.Record{Node: node, Kind: kind, Text: text, Attrs: attrs})
}

func attr(key, value string) kernel.Attr { return kernel.Attr{Key: key, Value: value} }

func u64(v uint64) string { return strconv.FormatUint(v, 10) }

func i64(v int64) string { return strconv.FormatInt(v, 10) }

// count applies f to the network totals and to the counters of link from -> to (NET-032).
func (nw *Network) count(from, to kernel.NodeID, f func(st *Stats)) {
	f(&nw.stats)
	f(&nw.linkState(int(from)-1, int(to)-1).stats)
}

// dropCopy emits net.drop for c at node and updates the counters. inFlight says whether c was
// counted in InFlight (delivery-time and crash drops) or not (send-time drops).
func (nw *Network) dropCopy(c *copyMsg, reason DropReason, node kernel.NodeID, inFlight bool) {
	from, to := nw.name(c.from), nw.name(c.to)
	msg, cp := u64(c.id), u64(uint64(c.copyNo))
	nw.emit("net.drop", node,
		"drop #"+msg+"."+cp+" "+from+" -> "+to+": "+reason.String(),
		attr("msg", msg), attr("copy", cp), attr("from", from), attr("to", to),
		attr("reason", reason.String()))
	nw.count(c.from, c.to, func(st *Stats) {
		if inFlight {
			st.InFlight--
		}
		switch reason {
		case DropPartition:
			st.DroppedPartition++
		case DropLoss:
			st.DroppedLoss++
		case DropPartitionInFlight:
			st.DroppedInFlight++
		case DropDown:
			st.DroppedDown++
		case DropNoHandler:
			st.DroppedNoHandler++
		}
	})
}

// onCrash is the crash hook registered by New (NET-020).
func (nw *Network) onCrash(n *kernel.Node) {
	nw.register()
	ns := &nw.nodes[n.ID()-1]
	ns.handler = nil
	ns.inc = 0
	parked := ns.parked
	ns.parked = nil
	for _, c := range parked {
		c.parked = false
		nw.dropCopy(c, DropDown, n.ID(), true)
	}
}
