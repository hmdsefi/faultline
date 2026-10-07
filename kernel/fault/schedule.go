package fault

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// Schedule is a versioned list of events.
type Schedule struct {
	Version int // ScheduleVersion; 0 is treated as ScheduleVersion

	// End is the virtual time at which the run stops; 0 = not set. Recovery is the start of the
	// recovery window; 0 = not set. The injector ignores both; API and MIN use them (MIN §12).
	// JSON "end" and "recovery", written only when non-zero.
	End      kernel.Time
	Recovery kernel.Time

	Events []Event
}

// Validate checks the version, End, Recovery, and every event's structure (FLT-005). Role is
// allowed.
func (s Schedule) Validate() error {
	if s.Version != 0 && s.Version != ScheduleVersion {
		return errors.New("fault: schedule: unsupported version " + strconv.Itoa(s.Version) + " (supported: 1)")
	}
	if s.End < 0 {
		return errors.New("fault: schedule: end must be >= 0 (got " + time.Duration(s.End).String() + ")")
	}
	if s.Recovery < 0 {
		return errors.New("fault: schedule: recovery must be >= 0 (got " + time.Duration(s.Recovery).String() + ")")
	}
	for i, e := range s.Events {
		if p := e.problem(); p != "" {
			return fmt.Errorf("fault: schedule: events[%d]: %s", i, p)
		}
	}
	return nil
}

// Normalize returns a concrete copy of s: version 1, no Role, events stably sorted by At,
// IDs 1..n, Undoes recomputed by the pairing rules, End and Recovery kept (FLT-013). It returns
// Validate's error, or an error naming the first event with a Role.
func (s Schedule) Normalize() (Schedule, error) {
	if err := s.Validate(); err != nil {
		return Schedule{}, err
	}
	for i, e := range s.Events {
		if e.Role != "" {
			return Schedule{}, fmt.Errorf("fault: schedule: events[%d]: %s: role %q is not allowed in a concrete schedule", i, e.Kind, e.Role)
		}
	}
	out := Schedule{Version: ScheduleVersion, End: s.End, Recovery: s.Recovery, Events: cloneEvents(s.Events)}
	sortByAt(out.Events)
	var p pairer
	for i := range out.Events {
		out.Events[i].ID = i + 1
		out.Events[i].Undoes = p.pair(out.Events[i])
	}
	return out, nil
}

// sortByAt sorts events stably by At (FLT-006).
func sortByAt(events []Event) {
	slices.SortStableFunc(events, func(a, b Event) int {
		switch {
		case a.At < b.At:
			return -1
		case a.At > b.At:
			return 1
		}
		return 0
	})
}

// activeFault is one entry of the active set of FLT-012.
type activeFault struct {
	id         int
	kind       Kind
	node, peer string
}

// pairer applies the pairing rules of FLT-012 to a sequence of events. Normalize and the
// injector share it.
type pairer struct {
	active []activeFault // ascending ID
}

// pair returns the IDs of the active faults e ends (nil if none) and updates the active set.
// Call it once per event, in order, with strictly increasing IDs; e.ID must be set.
func (p *pairer) pair(e Event) []int {
	var undoes []int
	kept := p.active[:0] // filter in place
	for _, f := range p.active {
		if ends(e, f) {
			undoes = append(undoes, f.id)
		} else {
			kept = append(kept, f)
		}
	}
	clear(p.active[len(kept):]) // ended entries keep no node names alive
	p.active = kept
	if e.Durable() {
		p.active = append(p.active, activeFault{id: e.ID, kind: e.Kind, node: e.Node, peer: e.Peer})
	}
	return undoes
}

// ends reports whether e ends the active fault f (FLT-012 table).
func ends(e Event, f activeFault) bool {
	sameNode := f.node == e.Node
	sameLink := sameNode && f.peer == e.Peer
	switch e.Kind {
	case KindHeal:
		return f.kind == KindPartition || f.kind == KindIsolate || f.kind == KindCut
	case KindHealLink:
		return f.kind == KindCut && sameLink
	case KindLinkReset:
		return f.kind == KindLink && sameLink
	case KindRestart:
		return f.kind == KindCrash && sameNode
	case KindResume, KindCrash:
		return f.kind == KindPause && sameNode
	case KindSyncFail:
		return f.kind == KindSyncFail && sameNode
	case KindDiskCapacity:
		return f.kind == KindDiskCapacity && sameNode
	}
	return false
}
