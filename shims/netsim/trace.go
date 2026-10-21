// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import "github.com/hmdsefi/faultline/kernel"

// emit records one netsim record on node (0 = global); Emit fills Inc with the node's current
// incarnation (KRN-090). Driver side only (NSM-002).
func (st *Stack) emit(node kernel.NodeID, kind, text string, attrs ...kernel.Attr) {
	st.s.Emit(kernel.Record{Node: node, Kind: kind, Text: text, Attrs: attrs})
}
