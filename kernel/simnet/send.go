// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"fmt"

	"github.com/hmdsefi/faultline/kernel"
)

// Send sends payload from -> to. Payloads are passed by reference and never copied; send
// immutable values or byte slices that are no longer mutated. It panics if from is nil, belongs
// to another Sim, or is not up, or if to is unknown.
func (nw *Network) Send(from *kernel.Node, to kernel.NodeID, payload any) {
	nw.register()
	i, j := nw.sender("Send", from, to)
	nw.lastMsg++
	id := nw.lastMsg
	f, t := nw.nodes[i].name, nw.nodes[j].name
	msg, desc := u64(id), kernel.Describe(payload)
	nw.emit("net.send", from.ID(), "send #"+msg+" "+f+" -> "+t+": "+desc,
		attr("msg", msg), attr("from", f), attr("to", t), attr("payload", desc))
	nw.count(from.ID(), to, func(st *Stats) { st.Sent++ })
	now := nw.s.Now()
	c1 := &copyMsg{id: id, copyNo: 1, from: from.ID(), to: to, payload: payload, sentAt: now}
	if i != j && !nw.adj[i][j] {
		nw.dropCopy(c1, DropPartition, from.ID(), false)
		return
	}
	l := nw.effective(i, j)
	d := decide(l, nw.stream(i, j))
	if d.Drop {
		nw.dropCopy(c1, DropLoss, from.ID(), false)
		return
	}
	nw.schedule(c1, nw.fifo(i, j, l.FIFO, now.Add(d.Delay)))
	if d.Dup {
		at2 := nw.fifo(i, j, l.FIFO, now.Add(d.DupDelay))
		c2 := &copyMsg{id: id, copyNo: 2, from: from.ID(), to: to, payload: payload, sentAt: now}
		nw.emit("net.dup", from.ID(), "dup #"+msg+" "+f+" -> "+t,
			attr("msg", msg), attr("copy", "2"), attr("from", f), attr("to", t))
		nw.schedule(c2, at2)
		nw.count(c2.from, c2.to, func(st *Stats) { st.Duplicated++ })
	}
}

// sender validates a send (NET-010) and returns the link indices.
func (nw *Network) sender(method string, from *kernel.Node, to kernel.NodeID) (int, int) {
	i := nw.nodeIndex(method, from)
	if st := from.State(); st != kernel.NodeUp {
		panic(fmt.Sprintf("simnet: %s: node %s (id %d) is %s", method, from.Name(), from.ID(), stateWord(st)))
	}
	return i, nw.index(method, to)
}

// fifo applies the FIFO rule of NET-014 on link i -> j and returns the delivery time.
func (nw *Network) fifo(i, j int, enabled bool, at kernel.Time) kernel.Time {
	ls := nw.linkState(i, j)
	if enabled && ls.hasLast && at <= ls.last {
		at = ls.last.Add(1)
	}
	if !ls.hasLast || at > ls.last {
		ls.last, ls.hasLast = at, true
	}
	return at
}

// schedule schedules the delivery event of c at at (NET-015) and counts it in flight.
func (nw *Network) schedule(c *copyMsg, at kernel.Time) {
	nw.s.At(at, "net.deliver", func() { nw.deliver(c) })
	nw.count(c.from, c.to, func(st *Stats) { st.InFlight++ })
}
