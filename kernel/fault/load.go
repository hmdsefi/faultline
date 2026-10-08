package fault

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Load schedules every event of s to be applied at its At (FLT-040). Node names are looked up
// when an event fires. It panics if s is invalid or has a Role, or, outside replay mode, has an
// event before Now. In replay mode it schedules nothing and records fault.suppressed.
func (in *Injector) Load(s Schedule) {
	ns, err := s.Normalize()
	if err != nil {
		panic("fault: Load: " + strings.TrimPrefix(err.Error(), "fault: "))
	}
	if in.replaying {
		in.suppressed("load", "load "+strconv.Itoa(len(s.Events))+" events")
		return
	}
	if err := in.notBeforeNow("Load", s); err != nil {
		panic(err.Error())
	}
	in.schedule(ns.Events, "fault/load", "load")
}

// Replay loads s and enters replay mode: from then on s is the only source of faults
// (FLT-042, FLT-043). It returns an error, and changes nothing, if the injector is already
// replaying, s is invalid or has a Role, or s has an event before Now.
func (in *Injector) Replay(s Schedule) error {
	if in.replaying {
		return errors.New("fault: Replay: already replaying")
	}
	ns, err := s.Normalize()
	if err != nil {
		return errors.New("fault: Replay: " + strings.TrimPrefix(err.Error(), "fault: "))
	}
	if err := in.notBeforeNow("Replay", s); err != nil {
		return err
	}
	in.schedule(ns.Events, "fault/replay", "replay")
	in.replaying = true
	return nil
}

// Replaying reports whether Replay succeeded on this injector.
func (in *Injector) Replaying() bool { return in.replaying }

// notBeforeNow returns the error for the first event of s (slice order) with At < Now.
func (in *Injector) notBeforeNow(method string, s Schedule) error {
	now := in.s.Now()
	for i, e := range s.Events {
		if e.At < now {
			return fmt.Errorf("fault: %s: schedule: events[%d]: at %s is before now (%s)", method, i, time.Duration(e.At), time.Duration(now))
		}
	}
	return nil
}

// schedule calls Sim.AtFront once per distinct At, ascending; each kernel event applies the
// events of that instant in schedule order with source (FLT-040 step 4). events are sorted.
func (in *Injector) schedule(events []Event, label, source string) {
	for start := 0; start < len(events); {
		end := start + 1
		for end < len(events) && events[end].At == events[start].At {
			end++
		}
		group := events[start:end]
		in.s.AtFront(group[0].At, label, func() {
			for _, e := range group {
				in.apply(e, source)
			}
		})
		start = end
	}
}
