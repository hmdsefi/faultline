// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Event is one atomic fault action. Node, Peer and Groups hold node names. Each kind uses the
// fields named in the comment of its Kind constant; every other field must be zero. At and Kind
// belong to every event, and Role may replace Node.
type Event struct {
	At     kernel.Time  // JSON "at": Go duration string since Epoch, for example "1.5s"
	Kind   Kind         // JSON "kind"
	Node   string       // JSON "node"
	Peer   string       // JSON "peer"
	Groups [][]string   // JSON "groups"
	Link   *simnet.Link // JSON "link": {"latency": "50ms", "drop_ppm": 100000, ...}, zero fields left out
	N      int64        // JSON "n": clock-jump ns, drift ppm, sync-fail count, capacity bytes
	Path   string       // JSON "path"
	Off    int64        // JSON "off"
	Len    int          // JSON "len"

	// Role, if set, names a role that a planner resolves to Node when the event fires. Only
	// Script resolves roles. Inject, Load, Replay and Normalize reject events with Role set.
	// JSON "role".
	Role string
	// ID is the event's 1-based position in a concrete schedule; 0 = none. Annotation:
	// ignored by Inject, Load and Replay. JSON "id".
	ID int
	// Undoes lists, ascending, the IDs of the active faults this event ends; nil when none. A
	// heal ends every partition, isolation and cut. A heal-link ends the cut of its link, and a
	// link-reset the link overrides of its link. A restart ends the crash of its node, and a
	// resume or crash its pause. A sync-fail or disk-capacity event replaces the previous one on
	// its node. Annotation: ignored by Inject, Load and Replay. JSON "undoes".
	Undoes []int
}

// Validate checks the structure of e: a known kind, valid values in the fields of the kind, and
// zero in the others. Role is allowed. It does not check that nodes exist.
func (e Event) Validate() error {
	if p := e.problem(); p != "" {
		return errors.New("fault: " + p)
	}
	return nil
}

// present returns the bits of the non-zero fields of e (FLT-003 step 6).
func (e Event) present() uint16 {
	var b uint16
	if e.Node != "" {
		b |= fNode
	}
	if e.Peer != "" {
		b |= fPeer
	}
	if len(e.Groups) != 0 {
		b |= fGroups
	}
	if e.Link != nil {
		b |= fLink
	}
	if e.N != 0 {
		b |= fN
	}
	if e.Path != "" {
		b |= fPath
	}
	if e.Off != 0 {
		b |= fOff
	}
	if e.Len != 0 {
		b |= fLen
	}
	return b
}

// problem returns the first problem text of FLT-003 and FLT-004, or "".
func (e Event) problem() string {
	if e.Kind == "" {
		return "missing kind"
	}
	set, ok := fieldSet(e.Kind)
	if !ok {
		return "unknown kind " + strconv.Quote(string(e.Kind))
	}
	k := string(e.Kind)
	if e.At < 0 {
		return k + ": at must be >= 0 (got " + time.Duration(e.At).String() + ")"
	}
	if e.Role != "" && set&fNode == 0 {
		return k + ": role is not allowed"
	}
	if e.Role != "" && e.Node != "" {
		return k + ": node and role are mutually exclusive"
	}
	present := e.present()
	for i, name := range fieldNames {
		if bit := uint16(1) << i; set&bit == 0 && present&bit != 0 {
			return k + ": " + name + " is not allowed"
		}
	}
	if p := e.fieldProblem(set); p != "" {
		return p
	}
	if e.ID < 0 {
		return k + ": id must be >= 0 (got " + strconv.Itoa(e.ID) + ")"
	}
	for i, u := range e.Undoes {
		if u < 1 {
			return fmt.Sprintf("%s: undoes[%d] must be >= 1 (got %d)", k, i, u)
		}
	}
	return ""
}

// fieldProblem runs the field-set checks of FLT-004 in the order node, peer, groups, link, n,
// path, off, len.
func (e Event) fieldProblem(set uint16) string {
	k := string(e.Kind)
	if set&fNode != 0 {
		switch {
		case e.Node == "" && e.Role == "":
			return k + ": node is required"
		case !utf8.ValidString(e.Node):
			return k + ": node is not valid UTF-8"
		case !utf8.ValidString(e.Role):
			return k + ": role is not valid UTF-8"
		}
	}
	if set&fPeer != 0 {
		switch {
		case e.Peer == "":
			return k + ": peer is required"
		case !utf8.ValidString(e.Peer):
			return k + ": peer is not valid UTF-8"
		case e.Peer == e.Node:
			return k + ": node and peer must differ (" + strconv.Quote(e.Node) + ")"
		}
	}
	if set&fGroups != 0 {
		if p := groupsProblem(e.Groups); p != "" {
			return p
		}
	}
	if set&fLink != 0 {
		if e.Link == nil {
			return "link: link is required"
		}
		if err := e.Link.Validate(); err != nil {
			return "link: " + err.Error()
		}
	}
	if set&fN != 0 {
		if p := e.nProblem(); p != "" {
			return p
		}
	}
	if set&fPath != 0 {
		if e.Path == "" {
			return "corrupt: path is required"
		}
		if !utf8.ValidString(e.Path) {
			return "corrupt: path is not valid UTF-8"
		}
	}
	if set&fOff != 0 && e.Off < 0 {
		return "corrupt: off must be >= 0 (got " + strconv.FormatInt(e.Off, 10) + ")"
	}
	if set&fLen != 0 && e.Len <= 0 {
		return "corrupt: len must be > 0 (got " + strconv.Itoa(e.Len) + ")"
	}
	return ""
}

func groupsProblem(groups [][]string) string {
	if len(groups) < 2 {
		return "partition: at least 2 groups are required (got " + strconv.Itoa(len(groups)) + ")"
	}
	for i, g := range groups {
		if len(g) == 0 {
			return fmt.Sprintf("partition: groups[%d] is empty", i)
		}
	}
	for i, g := range groups {
		for j, name := range g {
			if name == "" {
				return fmt.Sprintf("partition: groups[%d][%d]: empty node name", i, j)
			}
		}
	}
	for i, g := range groups {
		for j, name := range g {
			if !utf8.ValidString(name) {
				return fmt.Sprintf("partition: groups[%d][%d] is not valid UTF-8", i, j)
			}
		}
	}
	seen := map[string]bool{} // lookups only
	for _, g := range groups {
		for _, name := range g {
			if seen[name] {
				return "partition: node " + strconv.Quote(name) + " appears more than once"
			}
			seen[name] = true
		}
	}
	return ""
}

func (e Event) nProblem() string {
	n := strconv.FormatInt(e.N, 10)
	switch e.Kind {
	case KindClockJump:
		if e.N == 0 {
			return "clock-jump: n must not be 0"
		}
		if e.N < -int64(MaxRuleDuration) || e.N > int64(MaxRuleDuration) {
			return "clock-jump: n must be in [-36000000000000000, 36000000000000000] (got " + n + ")"
		}
	case KindClockDrift:
		if e.N < int64(kernel.MinDriftPPM) || e.N > int64(kernel.MaxDriftPPM) {
			return "clock-drift: n must be in [-500000, 1000000] (got " + n + ")"
		}
	case KindSyncFail:
		if e.N < 0 || e.N > math.MaxInt32 {
			return "sync-fail: n must be in [0, 2147483647] (got " + n + ")"
		}
	case KindDiskCapacity:
		if e.N < 0 {
			return "disk-capacity: n must be >= 0 (got " + n + ")"
		}
	}
	return ""
}

// Durable reports whether e starts a fault that lasts until another event ends it. The durable
// kinds are partition, isolate, cut, link, crash and pause, and sync-fail and disk-capacity with
// N > 0.
func (e Event) Durable() bool {
	switch e.Kind {
	case KindPartition, KindIsolate, KindCut, KindLink, KindCrash, KindPause:
		return true
	case KindSyncFail, KindDiskCapacity:
		return e.N > 0
	}
	return false
}

// String returns the one-line description of e used in trace records, without At, ID and
// Undoes, for example "crash n3" or "cut n1 -> n3".
func (e Event) String() string {
	node := e.Node
	if e.Role != "" {
		node = "@" + e.Role
	}
	k := string(e.Kind)
	switch e.Kind {
	case KindPartition:
		return "partition " + groupsAttr(e.Groups)
	case KindIsolate, KindCrash, KindRestart, KindPause, KindResume:
		return k + " " + node
	case KindCut, KindHealLink, KindLinkReset:
		return k + " " + node + " -> " + e.Peer
	case KindHeal:
		return "heal"
	case KindLink:
		if e.Link == nil {
			return "link " + node + " -> " + e.Peer + ": <nil>"
		}
		l := e.Link
		return "link " + node + " -> " + e.Peer + ": latency=" + l.Latency.String() + " jitter=" + l.Jitter.String() +
			" tail=" + strconv.FormatUint(uint64(l.TailPPM), 10) + "ppm/" + l.Tail.String() +
			" drop=" + strconv.FormatUint(uint64(l.DropPPM), 10) + "ppm dup=" + strconv.FormatUint(uint64(l.DupPPM), 10) +
			"ppm fifo=" + strconv.FormatBool(l.FIFO)
	case KindClockJump:
		return "clock-jump " + node + " " + time.Duration(e.N).String()
	case KindClockDrift:
		return "clock-drift " + node + " " + strconv.FormatInt(e.N, 10) + "ppm"
	case KindSyncFail, KindDiskCapacity:
		return k + " " + node + " " + strconv.FormatInt(e.N, 10)
	case KindCorrupt:
		return "corrupt " + node + " " + e.Path + " off=" + strconv.FormatInt(e.Off, 10) + " len=" + strconv.Itoa(e.Len)
	}
	return k
}

// groupsAttr joins each group's names with "," and the groups with "|" (FLT §8).
func groupsAttr(groups [][]string) string {
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = strings.Join(g, ",")
	}
	return strings.Join(parts, "|")
}

// clone returns a deep copy of e: Groups, Link and Undoes are copied.
func (e Event) clone() Event {
	if e.Groups != nil {
		g := make([][]string, len(e.Groups))
		for i, x := range e.Groups {
			g[i] = append([]string(nil), x...)
		}
		e.Groups = g
	}
	if e.Link != nil {
		l := *e.Link
		e.Link = &l
	}
	if e.Undoes != nil {
		e.Undoes = append([]int(nil), e.Undoes...)
	}
	return e
}

// cloneEvents deep-copies events; nil stays nil.
func cloneEvents(events []Event) []Event {
	if events == nil {
		return nil
	}
	out := make([]Event, len(events))
	for i, e := range events {
		out[i] = e.clone()
	}
	return out
}

// names returns the node names of e in resolution order: Node, Peer, then Groups (FLT-030).
func (e Event) names() []string {
	var out []string
	if e.Node != "" {
		out = append(out, e.Node)
	}
	if e.Peer != "" {
		out = append(out, e.Peer)
	}
	for _, g := range e.Groups {
		out = append(out, g...)
	}
	return out
}
