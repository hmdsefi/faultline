// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault

import (
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

// FLT §8: fault.target without a role and fault.skip, with the "rule" field that Random uses.
func TestTargetAndSkipRecords(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	n1, n2 := s.AddNode("n1", func(*kernel.Node) {}), s.AddNode("n2", func(*kernel.Node) {})
	s.RunUntil(0)
	ctx := &PlanContext{Sim: s}
	emitTarget(ctx, "random", "rule", 2, KindCrash, "", []*kernel.Node{n1, n2}, n2)
	emitSkip(ctx, "random", "rule", 3, KindPause, "no-target")
	var got []string
	for _, r := range s.Records() {
		if r.Kind != "fault.target" && r.Kind != "fault.skip" {
			continue
		}
		line := r.Kind + " " + r.Text + ":"
		for _, a := range r.Attrs {
			line += " " + a.Key + "=" + a.Value
		}
		if r.Node != 0 {
			line += " (node set)"
		}
		got = append(got, line)
	}
	want := []string{
		"fault.target target random rules[2] crash -> n2: planner=random rule=2 candidates=n1,n2 chosen=n2",
		"fault.skip skip random rules[3] pause: no-target: planner=random rule=3 kind=pause reason=no-target",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("records\n got %q\nwant %q", got, want)
	}
}
