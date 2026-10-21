// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"net/netip"
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// emit records one netsim record on node (0 = global); Emit fills Inc with the node's current
// incarnation (KRN-090). Driver side only (NSM-002).
func (st *Stack) emit(node kernel.NodeID, kind, text string, attrs ...kernel.Attr) {
	st.s.Emit(kernel.Record{Node: node, Kind: kind, Text: text, Attrs: attrs})
}

// emitUDPDrop records net.udp.drop on node for a datagram from src to dst (NSM §8).
func (st *Stack) emitUDPDrop(node kernel.NodeID, src, dst netip.AddrPort, n int, reason string) {
	st.emitDrop(node, st.render(src), st.render(dst), strconv.Itoa(n), reason)
}

// emitDrop records net.udp.drop with rendered src, dst and len values (NSM §8).
func (st *Stack) emitDrop(node kernel.NodeID, src, dst, n, reason string) {
	st.emit(node, "net.udp.drop", "udp drop "+src+" -> "+dst+": "+reason,
		kernel.Attr{Key: "src", Value: src}, kernel.Attr{Key: "dst", Value: dst},
		kernel.Attr{Key: "len", Value: n}, kernel.Attr{Key: "reason", Value: reason})
}

// emitUDPClose records net.udp.close on node (NSM §8).
func (st *Stack) emitUDPClose(node kernel.NodeID, port uint16, reason string) {
	p := strconv.Itoa(int(port))
	st.emit(node, "net.udp.close", "udp close :"+p+": "+reason,
		kernel.Attr{Key: "port", Value: p}, kernel.Attr{Key: "reason", Value: reason})
}
