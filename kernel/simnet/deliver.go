// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"slices"

	"github.com/hmdsefi/faultline/kernel"
)

// deliver runs the delivery pipeline of NET-018 for one copy.
func (nw *Network) deliver(c *copyMsg) {
	nw.register()
	i, j := int(c.from)-1, int(c.to)-1
	if i != j && !nw.cfg.NoDeliveryCheck && !nw.adj[i][j] {
		nw.dropCopy(c, DropPartitionInFlight, c.to, true)
		return
	}
	dst := nw.nodes[j].node
	switch dst.State() {
	case kernel.NodeUp:
		nw.deliverNow(c)
	case kernel.NodePaused:
		nw.park(c, dst)
	default:
		nw.dropCopy(c, DropDown, c.to, true)
	}
}

// park appends c to dst's parked list and posts its delivery for after resume (NET-019).
func (nw *Network) park(c *copyMsg, dst *kernel.Node) {
	ns := &nw.nodes[c.to-1]
	c.parked = true
	ns.parked = append(ns.parked, c)
	nw.count(c.from, c.to, func(st *Stats) { st.Deferred++ })
	from, to := nw.name(c.from), ns.name
	msg, cp := u64(c.id), u64(uint64(c.copyNo))
	nw.emit("net.defer", c.to, "defer #"+msg+"."+cp+" "+from+" -> "+to+": "+to+" paused",
		attr("msg", msg), attr("copy", cp), attr("from", from), attr("to", to))
	dst.Post("net.deliver", func() { nw.unpark(c) })
}

// unpark runs when the posted delivery of a parked copy executes after resume.
func (nw *Network) unpark(c *copyMsg) {
	if !c.parked {
		return // dropped by the crash hook (NET-020)
	}
	ns := &nw.nodes[c.to-1]
	k := slices.Index(ns.parked, c)
	ns.parked = slices.Delete(ns.parked, k, k+1)
	c.parked = false
	nw.deliverNow(c)
}

// deliverNow hands c to the destination's handler, or drops it with reason no-handler (NET-018).
func (nw *Network) deliverNow(c *copyMsg) {
	ns := &nw.nodes[c.to-1]
	if ns.handler == nil || ns.inc != ns.node.Incarnation() {
		nw.dropCopy(c, DropNoHandler, c.to, true)
		return
	}
	from, to := nw.name(c.from), ns.name
	msg, cp := u64(c.id), u64(uint64(c.copyNo))
	nw.emit("net.deliver", c.to, "deliver #"+msg+"."+cp+" "+from+" -> "+to,
		attr("msg", msg), attr("copy", cp), attr("from", from), attr("to", to),
		attr("latency_ns", i64(int64(nw.s.Now().Sub(c.sentAt)))))
	nw.count(c.from, c.to, func(st *Stats) {
		st.InFlight--
		st.Delivered++
	})
	ns.handler(c.from, c.payload)
}
