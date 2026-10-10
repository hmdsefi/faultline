// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault

import (
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

// lastRecord returns the last kept record of kind in s, or a zero record.
func lastRecord(s *kernel.Sim, kind string) kernel.Record {
	var last kernel.Record
	for _, r := range s.Records() {
		if r.Kind == kind {
			last = r
		}
	}
	return last
}

// FLT-030: apply's panic prefix and the source attribute for every source (Load, Replay and the
// planners call apply from later code), and FLT-043: in replay mode an event from another source
// is validated, then suppressed, while a replayed event applies.
func TestApplySources(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	n1 := s.AddNode("n1", func(*kernel.Node) {})
	s.RunUntil(0)
	in := NewInjector(s, nil, nil)
	panics := func(want string, e Event, source string) {
		t.Helper()
		defer func() {
			t.Helper()
			if got := recover(); got != want {
				t.Errorf("apply(%s, %s) panicked with %v, want %q", e, source, got, want)
			}
		}()
		in.apply(e, source)
	}
	for _, c := range []struct{ source, prefix string }{
		{"inject", "Inject: "}, {"load", "Load: "}, {"replay", "Replay: "}, {"p", "planner p: "},
	} {
		panics("fault: "+c.prefix+`crash: unknown node "zz"`, Event{Kind: KindCrash, Node: "zz"}, c.source)
		in.apply(Event{Kind: KindClockJump, Node: "n1", N: 1}, c.source)
		if r := lastRecord(s, "fault.clock_jump"); len(r.Attrs) < 2 || r.Attrs[1] != attr("source", c.source) {
			t.Errorf("fault.clock_jump attrs %v, want source %q second", r.Attrs, c.source)
		}
	}
	in.replaying = true
	panics("fault: Inject: crash: node is required", Event{Kind: KindCrash}, "inject")
	in.apply(Event{Kind: KindCrash, Node: "n1"}, "inject")
	r := lastRecord(s, "fault.suppressed")
	if n1.State() != kernel.NodeUp || len(in.Applied().Events) != 4 || r.Text != "suppressed (replay): crash n1" ||
		len(r.Attrs) != 2 || r.Attrs[0] != attr("source", "inject") || r.Attrs[1] != attr("detail", "crash n1") {
		t.Fatalf("replay mode, source inject: n1 %v, %d applied, record %+v", n1.State(), len(in.Applied().Events), r)
	}
	// FLT-030 step 1, FLT-043: every source but replay is suppressed, before names and components
	// are resolved (zz is unknown; the injector has no network), and fault.suppressed is global
	for _, source := range []string{"inject", "load", "p"} {
		for _, e := range []Event{{Kind: KindCrash, Node: "zz"}, {Kind: KindHeal}} {
			n := len(s.Records())
			in.apply(e, source)
			r := lastRecord(s, "fault.suppressed")
			if len(s.Records()) != n+1 || r.Node != 0 || r.Inc != 0 || r.Text != "suppressed (replay): "+e.String() ||
				len(r.Attrs) != 2 || r.Attrs[0] != attr("source", source) || r.Attrs[1] != attr("detail", e.String()) {
				t.Fatalf("replay mode, %s from %s: record %+v", e, source, r)
			}
		}
	}
	if len(in.Applied().Events) != 4 {
		t.Fatalf("replay mode: %d applied, want 4", len(in.Applied().Events))
	}
	in.apply(Event{Kind: KindCrash, Node: "n1"}, "replay")
	if n1.State() != kernel.NodeDown || len(in.Applied().Events) != 5 {
		t.Fatalf("replay mode, source replay: n1 %v, %d applied", n1.State(), len(in.Applied().Events))
	}
}
