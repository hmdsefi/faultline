// Package toys holds tiny event-style systems with exactly specified behavior (DET §5.5). Kernel,
// golden and determinism tests run them; a change to a toy changes golden hashes.
package toys

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// Transport is the message layer a toy uses. *Wire implements it; the golden suite adapts
// *simnet.Network to it.
type Transport interface {
	Handle(n *kernel.Node, h func(from kernel.NodeID, msg any))
	Send(from *kernel.Node, to kernel.NodeID, msg any)
}

// Wire is a minimal kernel-only message layer: uniform latency, no partitions, and drops only at
// destinations that are down or have no handler. The handler of a destination that is up runs inside
// the global toys.deliver event, so a panic in it is reported without a node.
type Wire struct {
	s        *kernel.Sim
	rnd      *rand.Rand
	min, max time.Duration
	handlers []handler // indexed by NodeID
}

// handler is a registered message handler and the incarnation that registered it.
type handler struct {
	fn  func(from kernel.NodeID, msg any)
	inc uint32
}

// NewWire returns a Wire whose latency is kernel.Uniform(s.Rand("toys/wire"), min, max).
func NewWire(s *kernel.Sim, min, max time.Duration) *Wire {
	if min < 0 || min > max {
		panic(fmt.Sprintf("toys: invalid latency [%s, %s]", min, max))
	}
	return &Wire{s: s, rnd: s.Rand("toys/wire"), min: min, max: max}
}

// Handle stores h for n.ID() together with n.Incarnation(), replacing any previous handler of n.
func (w *Wire) Handle(n *kernel.Node, h func(from kernel.NodeID, msg any)) {
	id := int(n.ID())
	for len(w.handlers) <= id {
		w.handlers = append(w.handlers, handler{})
	}
	w.handlers[id] = handler{fn: h, inc: n.Incarnation()}
}

// Send emits toys.send and schedules the delivery of msg to node to after a uniform latency
// (DET-040).
func (w *Wire) Send(from *kernel.Node, to kernel.NodeID, msg any) {
	if from.State() != kernel.NodeUp {
		panic(fmt.Sprintf("toys: Send from node %s in state %s", from.Name(), from.State()))
	}
	dst := w.s.Node(to)
	if dst == nil {
		panic(fmt.Sprintf("toys: Send to unknown node %d", to))
	}
	lat := kernel.Uniform(w.rnd, w.min, w.max)
	desc := kernel.Describe(msg)
	w.s.Emit(kernel.Record{
		Kind: "toys.send",
		Node: from.ID(),
		Text: desc,
		Attrs: []kernel.Attr{
			{Key: "to", Value: dst.Name()},
			{Key: "latency_ns", Value: strconv.FormatInt(int64(lat), 10)},
		},
	})
	w.s.At(w.s.Now().Add(lat), "toys.deliver", func() { w.deliver(from, dst, msg, desc) })
}

// deliver is the toys.deliver event.
func (w *Wire) deliver(from, dst *kernel.Node, msg any, desc string) {
	switch dst.State() {
	case kernel.NodeUp:
		w.deliverNow(from, dst, msg, desc)
	case kernel.NodePaused:
		dst.Post("toys.deliver", func() { w.deliverNow(from, dst, msg, desc) })
	default:
		w.drop(from, dst, desc, "down")
	}
}

// deliverNow hands msg to dst's handler of the current incarnation.
func (w *Wire) deliverNow(from, dst *kernel.Node, msg any, desc string) {
	id := int(dst.ID())
	if id >= len(w.handlers) || w.handlers[id].fn == nil || w.handlers[id].inc != dst.Incarnation() {
		w.drop(from, dst, desc, "no-handler")
		return
	}
	w.s.Emit(kernel.Record{
		Kind:  "toys.recv",
		Node:  dst.ID(),
		Text:  desc,
		Attrs: []kernel.Attr{{Key: "from", Value: from.Name()}},
	})
	w.handlers[id].fn(from.ID(), msg)
}

func (w *Wire) drop(from, dst *kernel.Node, desc, reason string) {
	w.s.Emit(kernel.Record{
		Kind: "toys.drop",
		Node: dst.ID(),
		Text: desc,
		Attrs: []kernel.Attr{
			{Key: "from", Value: from.Name()},
			{Key: "reason", Value: reason},
		},
	})
}
