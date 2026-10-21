// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"net/netip"

	"github.com/hmdsefi/faultline/kernel"
)

// dialRecord is one pending TCP dial (NSM-021).
//
//nolint:unused // dial state, not wired up yet
type dialRecord struct {
	h         *Host
	target    netip.AddrPort
	loop      bool
	dst       kernel.NodeID
	key       string
	done      chan struct{}
	conn      *Conn
	err       error
	finished  bool
	abandoned bool
	c         *connection
}
