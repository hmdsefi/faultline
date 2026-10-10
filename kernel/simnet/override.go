// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// SetLink overrides the configuration of the directed link from → to (from == to allowed).
// It applies to messages sent after the call. It panics if l is invalid or an ID is unknown.
func (nw *Network) SetLink(from, to kernel.NodeID, l Link) {
	nw.register()
	i := nw.index("SetLink", from)
	j := nw.index("SetLink", to)
	f, t := nw.nodes[i].name, nw.nodes[j].name
	if err := l.Validate(); err != nil {
		panic("simnet: SetLink " + f + " -> " + t + ": " + err.Error())
	}
	o := l
	nw.linkState(i, j).override = &o
	nw.emit("net.link_config", 0,
		"link "+f+" -> "+t+": latency="+l.Latency.String()+" jitter="+l.Jitter.String()+
			" tail="+u64(uint64(l.TailPPM))+"ppm/"+l.Tail.String()+" drop="+u64(uint64(l.DropPPM))+
			"ppm dup="+u64(uint64(l.DupPPM))+"ppm fifo="+strconv.FormatBool(l.FIFO),
		attr("from", f), attr("to", t),
		attr("latency_ns", i64(int64(l.Latency))), attr("jitter_ns", i64(int64(l.Jitter))),
		attr("tail_ppm", u64(uint64(l.TailPPM))), attr("tail_ns", i64(int64(l.Tail))),
		attr("drop_ppm", u64(uint64(l.DropPPM))), attr("dup_ppm", u64(uint64(l.DupPPM))),
		attr("fifo", strconv.FormatBool(l.FIFO)))
}

// ResetLink removes the override of from → to, so the link uses Config.Default again.
func (nw *Network) ResetLink(from, to kernel.NodeID) {
	nw.register()
	i := nw.index("ResetLink", from)
	j := nw.index("ResetLink", to)
	if ls := nw.links[i][j]; ls != nil {
		ls.override = nil
	}
	f, t := nw.nodes[i].name, nw.nodes[j].name
	nw.emit("net.link_reset", 0, "link "+f+" -> "+t+": reset to default", attr("from", f), attr("to", t))
}

// Link returns the effective configuration of from → to: its override, or Config.Default.
func (nw *Network) Link(from, to kernel.NodeID) Link {
	nw.register()
	return nw.effective(nw.index("Link", from), nw.index("Link", to))
}
