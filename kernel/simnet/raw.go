// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"fmt"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// RawOptions configures Network.SendRaw.
type RawOptions struct {
	// Delay is the delivery delay of the single copy. 0 ≤ Delay ≤ MaxDelay.
	Delay time.Duration
	// FIFO applies the FIFO rule to this copy even if the link is not FIFO.
	FIFO bool
}

// Decide draws the random decisions for one message on from → to from that link's stream,
// exactly as Send does, without sending anything and without emitting a record.
func (nw *Network) Decide(from, to kernel.NodeID) Decision {
	nw.register()
	i := nw.index("Decide", from)
	j := nw.index("Decide", to)
	return decide(nw.effective(i, j), nw.stream(i, j))
}

// SendRaw sends payload as exactly one copy delivered after opt.Delay, through the same
// send-time partition check and delivery pipeline as Send, with no random draws (no loss, no
// duplication, no jitter). It returns the message ID. It panics on the same misuse as Send and
// if opt.Delay is out of range.
func (nw *Network) SendRaw(from *kernel.Node, to kernel.NodeID, payload any, opt RawOptions) uint64 {
	nw.register()
	i, j := nw.sender("SendRaw", from, to)
	if opt.Delay < 0 || opt.Delay > MaxDelay {
		panic(fmt.Sprintf("simnet: SendRaw: delay %s out of range [0s, %s]", opt.Delay, MaxDelay))
	}
	nw.lastMsg++
	id := nw.lastMsg
	f, t := nw.nodes[i].name, nw.nodes[j].name
	msg, desc := u64(id), kernel.Describe(payload)
	nw.emit("net.send_raw", from.ID(), "send_raw #"+msg+" "+f+" -> "+t+": "+desc,
		attr("msg", msg), attr("from", f), attr("to", t), attr("payload", desc),
		attr("delay_ns", i64(int64(opt.Delay))), attr("fifo", strconv.FormatBool(opt.FIFO)))
	nw.count(from.ID(), to, func(st *Stats) { st.Sent++ })
	now := nw.s.Now()
	c := &copyMsg{id: id, copyNo: 1, from: from.ID(), to: to, payload: payload, sentAt: now}
	if i != j && !nw.adj[i][j] {
		nw.dropCopy(c, DropPartition, from.ID(), false)
		return id
	}
	l := nw.effective(i, j)
	nw.schedule(c, nw.fifo(i, j, l.FIFO || opt.FIFO, now.Add(opt.Delay)))
	return id
}
