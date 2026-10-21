// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"fmt"
	"net/netip"

	"github.com/hmdsefi/faultline/kernel"
)

// loopback is the address every loopback form resolves to (NSM-015).
var loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})

// NodeAddr returns the IPv4 address of node id: 10.0.0.0 + id. It panics if id < 1 or
// id > 16_777_214.
func NodeAddr(id kernel.NodeID) netip.Addr {
	if id < 1 || id > 16_777_214 {
		panic(fmt.Sprintf("netsim: NodeAddr: node id %d out of range [1, 16777214]", id))
	}
	return netip.AddrFrom4([4]byte{10, byte(id >> 16), byte(id >> 8), byte(id)}) //nolint:gosec // G115: each byte keeps 8 bits of id on purpose
}

// AddrNode returns the node ID of a node address (10.0.0.0/8, low 24 bits >= 1), unmapping
// IPv4-mapped IPv6 first. It does not check that the node exists.
func AddrNode(a netip.Addr) (kernel.NodeID, bool) {
	a = a.Unmap()
	if !a.Is4() {
		return 0, false
	}
	b := a.As4()
	if b[0] != 10 {
		return 0, false
	}
	id := kernel.NodeID(int32(b[1])<<16 | int32(b[2])<<8 | int32(b[3]))
	return id, id >= 1
}

// isLoopback reports whether a is in 127.0.0.0/8 (after unmapping).
func isLoopback(a netip.Addr) bool {
	a = a.Unmap()
	return a.Is4() && a.As4()[0] == 127
}
