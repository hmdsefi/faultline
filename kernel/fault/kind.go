// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package fault describes faults as data (events and schedules), applies them to a simulated
// world (Injector), and decides them during a run (planners).
//
// All functions and methods must be called from the simulation goroutine. Nothing in this
// package is safe for concurrent use.
package fault

import "time"

// Kind names a fault event type. The string is the JSON "kind" value.
type Kind string

const (
	KindPartition    Kind = "partition"     // split nodes into groups (Groups)
	KindIsolate      Kind = "isolate"       // remove every link to and from Node
	KindCut          Kind = "cut"           // remove the link Node -> Peer
	KindHeal         Kind = "heal"          // restore every link
	KindHealLink     Kind = "heal-link"     // restore the link Node -> Peer
	KindLink         Kind = "link"          // override the config of link Node -> Peer
	KindLinkReset    Kind = "link-reset"    // restore the config in effect before the first override
	KindCrash        Kind = "crash"         // crash Node
	KindRestart      Kind = "restart"       // restart Node if it is down
	KindPause        Kind = "pause"         // pause Node
	KindResume       Kind = "resume"        // resume Node if it is paused
	KindClockJump    Kind = "clock-jump"    // step Node's clock by N nanoseconds
	KindClockDrift   Kind = "clock-drift"   // set Node's drift to N ppm
	KindSyncFail     Kind = "sync-fail"     // the next N Syncs on Node's volume fail; 0 clears
	KindDiskCapacity Kind = "disk-capacity" // set Node's volume capacity to N bytes; 0 = unlimited
	KindCorrupt      Kind = "corrupt"       // damage up to Len synced bytes of Path at Off on Node's volume
)

// Kinds returns every kind in the declaration order above.
func Kinds() []Kind {
	return []Kind{
		KindPartition, KindIsolate, KindCut, KindHeal, KindHealLink, KindLink, KindLinkReset,
		KindCrash, KindRestart, KindPause, KindResume, KindClockJump, KindClockDrift,
		KindSyncFail, KindDiskCapacity, KindCorrupt,
	}
}

// ScheduleVersion is the schedule format version this package reads and writes.
const ScheduleVersion = 1

// MaxRuleDuration bounds Rule.Every, Rule.MinFor, Rule.MaxFor and clock-jump amounts, so that
// time arithmetic cannot overflow.
const MaxRuleDuration = 10000 * time.Hour

// Field bits, in the order of FLT-003 step 6: node, peer, groups, link, n, path, off, len.
const (
	fNode uint16 = 1 << iota
	fPeer
	fGroups
	fLink
	fN
	fPath
	fOff
	fLen
)

// fieldNames are the JSON keys of the field bits, in bit order.
var fieldNames = [...]string{"node", "peer", "groups", "link", "n", "path", "off", "len"}

// fieldSet returns the field set of k (FLT-002) and whether k is a valid kind (FLT-001).
func fieldSet(k Kind) (uint16, bool) {
	switch k {
	case KindPartition:
		return fGroups, true
	case KindIsolate, KindCrash, KindRestart, KindPause, KindResume:
		return fNode, true
	case KindCut, KindHealLink, KindLinkReset:
		return fNode | fPeer, true
	case KindHeal:
		return 0, true
	case KindLink:
		return fNode | fPeer | fLink, true
	case KindClockJump, KindClockDrift, KindSyncFail, KindDiskCapacity:
		return fNode | fN, true
	case KindCorrupt:
		return fNode | fPath | fOff | fLen, true
	}
	return 0, false
}

// isNet reports whether k needs the network (FLT-031).
func isNet(k Kind) bool {
	switch k {
	case KindPartition, KindIsolate, KindCut, KindHeal, KindHealLink, KindLink, KindLinkReset:
		return true
	}
	return false
}

// isDisk reports whether k needs the disks (FLT-031).
func isDisk(k Kind) bool {
	return k == KindSyncFail || k == KindDiskCapacity || k == KindCorrupt
}
