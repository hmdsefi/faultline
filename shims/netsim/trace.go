// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"fmt"
	"net/netip"
	"strconv"
	"time"

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

// emitSegment records net.conn.segment for one attempt of s on the sender node (NSM §8).
func (st *Stack) emitSegment(node kernel.NodeID, s *segment, outcome string, delay time.Duration) {
	st.emit(node, "net.conn.segment",
		fmt.Sprintf("%s %s %s seq=%d len=%d attempt=%d: %s", s.c.id, dirNames[s.dir], kindNames[s.kind], s.seq, len(s.data), s.attempts, outcome),
		kernel.Attr{Key: "conn", Value: s.c.id}, kernel.Attr{Key: "dir", Value: dirNames[s.dir]},
		kernel.Attr{Key: "kind", Value: kindNames[s.kind]}, kernel.Attr{Key: "seq", Value: strconv.FormatUint(s.seq, 10)},
		kernel.Attr{Key: "len", Value: strconv.Itoa(len(s.data))}, kernel.Attr{Key: "attempt", Value: strconv.Itoa(s.attempts)},
		kernel.Attr{Key: "outcome", Value: outcome}, kernel.Attr{Key: "delay_ns", Value: strconv.FormatInt(int64(delay), 10)})
}

// emitArrive records net.conn.arrive for s on the receiving node (NSM §8).
func (st *Stack) emitArrive(node kernel.NodeID, s *segment, result string) {
	st.emit(node, "net.conn.arrive",
		fmt.Sprintf("%s %s %s seq=%d: %s", s.c.id, dirNames[s.dir], kindNames[s.kind], s.seq, result),
		kernel.Attr{Key: "conn", Value: s.c.id}, kernel.Attr{Key: "dir", Value: dirNames[s.dir]},
		kernel.Attr{Key: "kind", Value: kindNames[s.kind]}, kernel.Attr{Key: "seq", Value: strconv.FormatUint(s.seq, 10)},
		kernel.Attr{Key: "len", Value: strconv.Itoa(len(s.data))}, kernel.Attr{Key: "result", Value: result})
}

// emitClose records net.conn.close on the closing node (NSM §8).
func (st *Stack) emitClose(node kernel.NodeID, conn, mode string) {
	st.emit(node, "net.conn.close", "close "+conn+": "+mode,
		kernel.Attr{Key: "conn", Value: conn}, kernel.Attr{Key: "mode", Value: mode})
}
