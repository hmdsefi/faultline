// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// DropReason says why a copy was not delivered.
type DropReason uint8

const (
	DropPartition         DropReason = iota + 1 // link disconnected at send time
	DropLoss                                    // random loss (Link.DropPPM)
	DropPartitionInFlight                       // link disconnected at delivery time
	DropDown                                    // destination down at delivery time
	DropNoHandler                               // no handler for the destination's current incarnation
)

// String returns "partition", "loss", "partition-in-flight", "down" or "no-handler", and
// "DropReason(<n>)" for any other value.
func (r DropReason) String() string {
	switch r {
	case DropPartition:
		return "partition"
	case DropLoss:
		return "loss"
	case DropPartitionInFlight:
		return "partition-in-flight"
	case DropDown:
		return "down"
	case DropNoHandler:
		return "no-handler"
	}
	return "DropReason(" + strconv.Itoa(int(r)) + ")"
}

// Stats counts messages and copies. For every Stats value returned by this package:
//
//	Sent + Duplicated == Delivered + Dropped() + InFlight
type Stats struct {
	Sent             uint64 // messages (Send and SendRaw calls that passed validation)
	Duplicated       uint64 // duplicate copies scheduled (copy 2)
	Delivered        uint64 // copies handed to a handler
	Deferred         uint64 // copies parked at a paused destination (counted when parked)
	DroppedPartition uint64
	DroppedLoss      uint64
	DroppedInFlight  uint64 // reason partition-in-flight
	DroppedDown      uint64
	DroppedNoHandler uint64
	InFlight         uint64 // copies scheduled or parked and not yet delivered or dropped
}

// Dropped returns the sum of all Dropped* counters.
func (s Stats) Dropped() uint64 {
	return s.DroppedPartition + s.DroppedLoss + s.DroppedInFlight + s.DroppedDown + s.DroppedNoHandler
}

// Stats returns the counters for the whole network.
func (nw *Network) Stats() Stats {
	nw.register()
	return nw.stats
}

// LinkStats returns the counters for the directed link from -> to.
func (nw *Network) LinkStats(from, to kernel.NodeID) Stats {
	nw.register()
	i := nw.index("LinkStats", from)
	j := nw.index("LinkStats", to)
	if ls := nw.links[i][j]; ls != nil {
		return ls.stats
	}
	return Stats{}
}
