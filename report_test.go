// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// API-065, API-067
func TestHeadlines(t *testing.T) {
	at := kernel.Time(41207000000)
	lim := &artifact.Limit{Name: "MaxEvents", Value: 1000}
	det := &determinism{seed: 0x5e1f9a2c4b7d3e80, hashes: []uint64{1, 2, 3}}
	cases := []struct {
		f         failure
		headline  string
		signature string
	}{
		{failure{kind: "invariant", check: "acked writes survive", context: "invariant", at: at, node: "n3", event: 48211, hasEvent: true},
			`invariant "acked writes survive" violated at t=41.207000000s on n3 (event 48211)`, "invariant:acked writes survive"},
		{failure{kind: "final", check: "a", context: "final", at: at}, `final check "a" failed after the run at t=41.207000000s`, "final:a"},
		{failure{kind: "panic", check: "x.f", context: "panic-callback", at: at, event: 3, hasEvent: true}, `panic at t=41.207000000s (event 3)`, "panic:x.f"},
		{failure{kind: "panic", check: "x.f", context: "panic-invariant", name: "i", at: at, node: "n1", event: 3, hasEvent: true}, `panic in invariant "i" at t=41.207000000s on n1 (event 3)`, "panic:x.f"},
		{failure{kind: "panic", check: "x.f", context: "panic-final", name: "f", at: at}, `panic in final check "f" after the run at t=41.207000000s`, "panic:x.f"},
		{failure{kind: "panic", check: "x.f", context: "panic-body", at: at}, `panic in body at t=41.207000000s`, "panic:x.f"},
		{failure{kind: "fail", context: "fail-loop", at: at, node: "n2", event: 9, hasEvent: true}, `simulation failed at t=41.207000000s on n2 (event 9)`, "fail:"},
		{failure{kind: "fail", context: "fail-body", at: 0}, `simulation failed in body at t=0.000000000s`, "fail:"},
		{failure{kind: "fail", context: "fail-test", at: at}, `test marked failed during the run (t.Error, t.Errorf or t.Fail) at t=41.207000000s`, "fail:"},
		{failure{kind: "limit", check: "max-events", context: "limit", at: 0, event: 1000, hasEvent: true, limit: lim}, `run exceeded MaxEvents (1000) at t=0.000000000s (event 1000)`, "limit:max-events"},
		{failure{kind: "determinism", context: "determinism-artifact", determinism: det},
			`determinism failure: re-running seed 0x5e1f9a2c4b7d3e80 gave trace hash 0x0000000000000002; the first run gave 0x0000000000000001`, "determinism:"},
		{failure{kind: "determinism", context: "determinism-check", determinism: det},
			`determinism failure: two runs of seed 0x5e1f9a2c4b7d3e80 gave trace hashes 0x0000000000000001 and 0x0000000000000002`, "determinism:"},
	}
	for _, c := range cases {
		if got := c.f.headline(); got != c.headline {
			t.Errorf("headline = %q\nwant %q", got, c.headline)
		}
		if got := c.f.signature(); got != c.signature {
			t.Errorf("signature = %q, want %q", got, c.signature)
		}
	}
}

// API-071
func TestDeterminismMessage(t *testing.T) {
	a := []kernel.Record{{Seq: 1, Kind: "kernel.start", Text: "start"}, {Seq: 2, At: 5, Node: 1, Inc: 1, Kind: "net.send", Text: "x", Attrs: []kernel.Attr{{Key: "msg", Value: "1"}}}}
	b := []kernel.Record{{Seq: 1, Kind: "kernel.start", Text: "start"}, {Seq: 2, At: 5, Node: 1, Inc: 1, Kind: "net.send", Text: "y"}}
	if d := firstDiff(a, a[:1:1]); d == nil || d.index != 1 || d.a == nil || d.b != nil {
		t.Fatalf("prefix diff %+v", d)
	}
	if firstDiff(a, slices.Clone(a)) != nil {
		t.Fatal("equal lists differ")
	}
	noAttrs := []kernel.Record{{Seq: 1, Attrs: []kernel.Attr{}}}
	if firstDiff(noAttrs, []kernel.Record{{Seq: 1}}) != nil {
		t.Fatal("nil and empty Attrs differ")
	}
	// A change in any one field is a difference, and the smallest index wins.
	base := kernel.Record{Seq: 1, At: 2, Node: 3, Inc: 4, Kind: "k", Cause: 5, Text: "t", Attrs: []kernel.Attr{{Key: "a", Value: "b"}}}
	for i, change := range []func(r *kernel.Record){
		func(r *kernel.Record) { r.Seq = 9 },
		func(r *kernel.Record) { r.At = 9 },
		func(r *kernel.Record) { r.Node = 9 },
		func(r *kernel.Record) { r.Inc = 9 },
		func(r *kernel.Record) { r.Kind = "z" },
		func(r *kernel.Record) { r.Cause = 9 },
		func(r *kernel.Record) { r.Text = "z" },
		func(r *kernel.Record) { r.Attrs = []kernel.Attr{{Key: "a", Value: "c"}} },
	} {
		other := base
		other.Attrs = slices.Clone(base.Attrs)
		change(&other)
		if d := firstDiff([]kernel.Record{base}, []kernel.Record{other}); d == nil || d.index != 0 {
			t.Errorf("field %d: diff %+v", i, d)
		}
	}
	if d := firstDiff([]kernel.Record{{Seq: 1, Text: "a"}, {Seq: 2, Text: "a"}}, []kernel.Record{{Seq: 1, Text: "b"}, {Seq: 2, Text: "b"}}); d == nil || d.index != 0 {
		t.Errorf("smallest index: %+v", d)
	}
	// Lines 3 and 4 of API-071, word for word.
	const causes = "common causes: state kept between runs in the same process (package-level variables, sync.Once, caches), map iteration order, global math/rand, wall-clock time, goroutines\n" +
		"next step: fix the cause; until the seed gives the same run every time, the replay command may not reproduce this failure"
	// Line 1 keeps the first run's message lines that hold a non-space character; line 2 names
	// the record by its Seq and labels each side with its run.
	orig := &failure{kind: "invariant", check: "i", context: "invariant", at: 0, message: "x=1\n \n  y=2\n"}
	f := newDeterminismFailure("artifact_rerun", 0x5e, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}}, orig, a, b, attemptResult{now: 7})
	want := strings.Join([]string{
		`the first run failed: invariant "i" violated at t=0.000000000s`,
		"  x=1",
		"    y=2",
		"first difference at record #2:",
		`  run 2: #2 t=0.000000005s node=1#1 net.send cause=0 "x" msg="1"`,
		`  run 3: #2 t=0.000000005s node=1#1 net.send cause=0 "y"`,
		causes,
	}, "\n")
	if f.message != want {
		t.Fatalf("message:\n%s\nwant:\n%s", f.message, want)
	}
	// at is the artifact attempt's final Now() (API-066); the headline names the seed (API-067).
	if f.at != 7 || f.headline() != "determinism failure: re-running seed 0x000000000000005e gave trace hash 0x0000000000000002; the first run gave 0x0000000000000001" {
		t.Errorf("at %v headline %q", f.at, f.headline())
	}
	// When the two full-trace runs kept the same records, line 2 is chosen from the hashes: it names
	// the run that differed, or says that the difference lies before the kept records.
	for _, c := range []struct {
		context string
		hashes  []uint64
		line    string
	}{
		{"artifact_rerun", []uint64{1, 9, 9}, "the two full-trace runs were identical (trace hash 0x0000000000000009); only the first run differed"},
		{"check_determinism", []uint64{1, 9, 9, 9}, "the two full-trace runs were identical (trace hash 0x0000000000000009); only the first run differed"},
		{"check_determinism", []uint64{9, 2, 9, 9}, "the two full-trace runs were identical (trace hash 0x0000000000000009); only the second run differed"},
		{"check_determinism", []uint64{1, 2, 9, 9}, "the two full-trace runs were identical (trace hash 0x0000000000000009); the first two runs both differed"},
		{"artifact_rerun", []uint64{1, 2, 3}, "the two full-trace runs differ before the last 2 records they kept (trace hashes 0x0000000000000002 and 0x0000000000000003); the kept records are identical"},
	} {
		var attempts []attemptResult
		for _, h := range c.hashes {
			attempts = append(attempts, attemptResult{hash: h})
		}
		if g := newDeterminismFailure(c.context, 1, attempts, nil, a, a, attemptResult{}); g.message != c.line+"\n"+causes {
			t.Errorf("%s %x: message\n%s\nwant line 2\n%s", c.context, c.hashes, g.message, c.line)
		}
	}
	// In the check the full-trace runs are runs 3 and 4. A run whose list ended names its last record.
	h := newDeterminismFailure("check_determinism", 1, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}, {hash: 4}}, nil, a[:1], a, attemptResult{})
	if want := "first difference at record #2:\n  run 3: (trace ended after record #1)\n  run 4: #2 t=0.000000005s node=1#1 net.send cause=0 \"x\" msg=\"1\"\n" + causes; h.message != want {
		t.Fatalf("absent side:\n%s\nwant\n%s", h.message, want)
	}
	// Run 4 ended: run 3 shows its first extra record, run 4 the record it ended after.
	if l := newDeterminismFailure("check_determinism", 1, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}, {hash: 4}}, nil, a, a[:1], attemptResult{}); !strings.HasPrefix(l.message, "first difference at record #2:\n  run 3: #2 t=0.000000005s node=1#1 net.send cause=0 \"x\" msg=\"1\"\n  run 4: (trace ended after record #1)\n") {
		t.Errorf("run 4 ended:\n%s", l.message)
	}
	// The line shows node, incarnation and cause, so a difference only in Cause is visible. Kept
	// windows that start at the same record are compared as they are.
	ca := []kernel.Record{{Seq: 5, At: 10, Node: 2, Inc: 5, Kind: "kernel.event", Cause: 3, Text: "tick"}}
	cb := []kernel.Record{{Seq: 5, At: 10, Node: 2, Inc: 5, Kind: "kernel.event", Cause: 4, Text: "tick"}}
	if c := newDeterminismFailure("check_determinism", 1, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}, {hash: 4}}, nil, ca, cb, attemptResult{}); !strings.HasPrefix(c.message, "first difference at record #5:\n  run 3: #5 t=0.000000010s node=2#5 kernel.event cause=3 \"tick\"\n  run 4: #5 t=0.000000010s node=2#5 kernel.event cause=4 \"tick\"\n") {
		t.Errorf("cause only:\n%s", c.message)
	}
	// A limit re-run whose two runs recorded different numbers of records kept windows that start
	// at different records: index 0 differs, but the runs diverged before the kept records. The
	// report keeps firstDiff's result.
	wa := []kernel.Record{{Seq: 3, Text: "c"}, {Seq: 4, Text: "d"}, {Seq: 5, Text: "e"}}
	wb := []kernel.Record{{Seq: 4, Text: "d"}, {Seq: 5, Text: "e"}, {Seq: 6, Text: "f"}}
	o := newDeterminismFailure("artifact_rerun", 1, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}}, orig, wa, wb, attemptResult{})
	if want := "the two full-trace runs diverged before the records they kept: run 2 recorded 5 records and run 3 recorded 6 (trace hashes 0x0000000000000002 and 0x0000000000000003)\n" + causes; !strings.HasSuffix(o.message, "\n"+want) {
		t.Errorf("windows that do not line up:\n%s\nwant the suffix\n%s", o.message, want)
	}
	if od := o.report().Determinism.Diff; od == nil || od.Index != 0 || od.A == nil || od.A.Seq != 3 || od.B == nil || od.B.Seq != 4 {
		t.Errorf("windows that do not line up: report diff %+v", od)
	}
	if hd := h.report().Determinism; h.headline() != "determinism failure: two runs of seed 0x0000000000000001 gave trace hashes 0x0000000000000001 and 0x0000000000000002" ||
		hd.Context != "check_determinism" || hd.Original != nil || hd.Diff.A != nil || hd.Diff.B == nil || hd.Diff.B.Text != "x" {
		t.Errorf("check_determinism: headline %q report %+v", h.headline(), hd)
	}
	rf := f.report()
	if rf.Kind != "determinism" || rf.Signature != "determinism:" || !slices.Equal(rf.Determinism.Hashes, []string{"0x0000000000000001", "0x0000000000000002", "0x0000000000000003"}) ||
		rf.Determinism.Original == nil || rf.Determinism.Diff.Index != 1 || rf.Determinism.Diff.A.Text != "x" {
		t.Fatalf("report %+v", rf.Determinism)
	}
	if rd := rf.Determinism; rf.AtNS != 7 || rf.At != "0.000000007s" || rd.Context != "artifact_rerun" || rd.Original.Check != "i" || rd.Original.Headline != orig.headline() || rd.Diff.B == nil || rd.Diff.B.Text != "y" {
		t.Errorf("artifact_rerun report: at %d %q, determinism %+v", rf.AtNS, rf.At, rd)
	}
}

// API-066, ART-021: report() carries every field of the failure.
func TestFailureReport(t *testing.T) {
	p := &artifact.Panic{Value: "v", In: "callback", Site: "s", Stack: "st"}
	lim := &artifact.Limit{Name: "MaxEvents", Value: 5}
	f := &failure{kind: "limit", check: "max-events", context: "limit", message: "m", at: 7, event: 5, hasEvent: true, nodeID: 2, node: "n2", recordSeq: 11, panic: p, limit: lim,
		finals: []finalResult{{check: "a", message: "A", panic: p}}}
	want := &artifact.Failure{Kind: "limit", Check: "max-events", Signature: "limit:max-events", Headline: f.headline(), Message: "m", AtNS: 7, At: "0.000000007s",
		Node: "n2", NodeID: 2, Event: 5, RecordSeq: 11, Panic: p, Limit: lim, Finals: []artifact.FinalFailure{{Check: "a", Message: "A", Panic: p}}}
	if got := f.report(); !reflect.DeepEqual(got, want) {
		t.Errorf("report\n%+v\nwant\n%+v", got, want)
	}
}

// API-075, API-076
func TestConsoleLines(t *testing.T) {
	var stack []string
	for i := 0; i < 45; i++ {
		stack = append(stack, fmt.Sprintf("frame%d", i))
	}
	f := &failure{kind: "panic", check: "x.f", context: "panic-body", message: "boom", panic: &artifact.Panic{Stack: strings.Join(stack, "\n") + "\n"}}
	lines := consoleLines(f, "CMD", "/a/", []string{"w1"}, false, true)
	if lines[0] != "faultline: panic in body at t=0.000000000s" || lines[1] != "  boom" || lines[2] != "stack:" || lines[3] != "  frame0" || lines[42] != "  frame39" ||
		lines[43] != "  ... 5 more lines (full stack in report.txt)" || lines[44] != "replay:    CMD" || lines[45] != "artifacts: /a/" || lines[46] != "warning: w1" || len(lines) != 47 {
		t.Fatalf("lines %q", lines)
	}
	if l := consoleLines(f, "CMD", "off", nil, false, false); l[43] != "  ... 5 more lines" {
		t.Fatalf("not written: %q", l[43])
	}
	if l := consoleLines(f, "CMD", "/a/", nil, true, true); len(l) != 50 || l[47] != "  frame44" {
		t.Fatalf("full: %q", l)
	}
	if got := joinLines([]string{"a", "b"}, "    "); got != "    a\n    b\n" {
		t.Fatalf("joinLines = %q", got)
	}
	// No stack for other kinds; every message line is indented.
	inv := &failure{kind: "invariant", check: "i", context: "invariant", message: "a\nb"}
	want := []string{`faultline: invariant "i" violated at t=0.000000000s`, "  a", "  b", "replay:    FAULTLINE_SEED=0x0000000000000001 go test -run '^TestA$' .", "artifacts: off"}
	if got := consoleLines(inv, "FAULTLINE_SEED=0x0000000000000001 go test -run '^TestA$' .", "off", nil, false, false); !slices.Equal(got, want) {
		t.Errorf("invariant lines %q", got)
	}
	// A stack of exactly 40 lines is shown whole, with no "more lines" line.
	short := &failure{kind: "panic", context: "panic-body", message: "m", panic: &artifact.Panic{Stack: strings.Join(stack[:40], "\n") + "\n"}}
	if got := consoleLines(short, "CMD", "off", nil, false, true); len(got) != 2+1+40+2 || got[42] != "  frame39" {
		t.Errorf("40-line stack: %q", got)
	}
	// Tabs in stack lines and leading spaces in message lines are kept.
	ws := &failure{kind: "panic", context: "panic-body", message: "diff:\n  -a\n  +b", panic: &artifact.Panic{Stack: "goroutine 1 [running]:\nmain.f()\n\t/x/main.go:3 +0x1d\n"}}
	want = []string{"faultline: panic in body at t=0.000000000s", "  diff:", "    -a", "    +b", "stack:", "  goroutine 1 [running]:", "  main.f()", "  \t/x/main.go:3 +0x1d", "replay:    CMD", "artifacts: off"}
	if got := consoleLines(ws, "CMD", "off", nil, false, false); !slices.Equal(got, want) {
		t.Errorf("whitespace lines %q", got)
	}
}

// API-075: a message line that holds only white space is not printed, so an empty message or one
// that ends in a newline adds no line of spaces to the console.
func TestConsoleMessageLines(t *testing.T) {
	for _, c := range []struct {
		message string
		lines   []string
	}{
		{"", nil},
		{"bad state\n", []string{"  bad state"}},
		{"a\n\n \t\n  b\n\n", []string{"  a", "    b"}},
	} {
		f := &failure{kind: "fail", context: "fail-body", message: c.message}
		want := append(append([]string{"faultline: simulation failed in body at t=0.000000000s"}, c.lines...), "replay:    CMD", "artifacts: off")
		if got := consoleLines(f, "CMD", "off", nil, false, false); !slices.Equal(got, want) {
			t.Errorf("message %q: lines %q, want %q", c.message, got, want)
		}
	}
}

// realStack is a stack as debug.Stack gives it inside a recover: the goroutine line, the frames
// of the recover and the panic, a misuse panic's faultline frames, two runtime frames, then n
// frames of user code (two lines each) and the kernel frames below them.
func realStack(n int) string {
	lines := []string{
		"goroutine 7 [running]:",
		"runtime/debug.Stack()",
		"\t/go/src/runtime/debug/stack.go:26 +0x5e",
		"github.com/hmdsefi/faultline/kernel.(*Sim).guard.func1()",
		"\t/fl/kernel/loop.go:90 +0x60",
		"panic({0x1043a5e40?, 0x1400012c0f0?})",
		"\t/go/src/runtime/panic.go:787 +0x132",
		"github.com/hmdsefi/faultline.panicMisuse({0x1, 0x2}, {0x0, 0x0, 0x0})",
		"\t/fl/world.go:38 +0x70",
		"github.com/hmdsefi/faultline/check/history.(*Recorder).Invoke(...)",
		"\t/fl/check/history/history.go:50",
		"runtime.mapassign_faststr(0x1, 0x2, {0x3, 0x4})",
		"\t/go/src/runtime/map_faststr.go:205 +0x2c",
		"runtime/debug.SetTraceback({0x1, 0x2})",
		"\t/go/src/runtime/debug/garbage.go:20 +0x18",
	}
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("github.com/hmdsefi/faultline_test.f%d(...)", i), fmt.Sprintf("\t/src/kv/store_test.go:%d", i+1))
	}
	lines = append(lines, "github.com/hmdsefi/faultline/kernel.(*Sim).guard(0x1)", "\t/fl/kernel/loop.go:95 +0x88")
	return strings.Join(lines, "\n") + "\n"
}

// API-075, API-076: the console stack window starts at the goroutine line and the first frame of
// user code after the panic, holds whole frames only, and counts the lines after it; report.txt
// (full) has every line.
func TestStackWindow(t *testing.T) {
	stackOf := func(lines []string) []string {
		var out []string
		for i := 0; i < len(lines) && lines[i] != "replay:    CMD"; i++ {
			if i >= 2 {
				out = append(out, lines[i])
			}
		}
		return out
	}
	user := func(from, to int) []string {
		var out []string
		for i := from; i < to; i++ {
			out = append(out, fmt.Sprintf("  github.com/hmdsefi/faultline_test.f%d(...)", i), fmt.Sprintf("  \t/src/kv/store_test.go:%d", i+1))
		}
		return out
	}
	kernelFrame := []string{"  github.com/hmdsefi/faultline/kernel.(*Sim).guard(0x1)", "  \t/fl/kernel/loop.go:95 +0x88"}
	// A deferred function that recovers and panics again puts a second panic( line above the
	// first one; the window starts at the original site, after the last panic( line.
	const firstPanic = "panic({0x1043a5e40?, 0x1400012c0f0?})\n\t/go/src/runtime/panic.go:787 +0x132\n"
	repanic := strings.Replace(realStack(2), firstPanic, "panic({0x1043a5e40?, 0x1400012c0f0?}) [recovered, repanicked]\n\t/go/src/runtime/panic.go:787 +0x132\n"+
		"github.com/hmdsefi/faultline_test.wrap.func1()\n\t/src/kv/store_test.go:40 +0x2c\n"+firstPanic, 1)
	cases := []struct {
		name    string
		stack   string
		written bool
		want    []string
	}{
		// 1 + 3*2 lines fit: nothing after the window, no truncation line.
		{"short", realStack(2), true, slices.Concat([]string{"stack:", "  goroutine 7 [running]:"}, user(0, 2), kernelFrame)},
		// 1 + 19*2 = 39 lines; the 20th frame would make 41, so the cut falls between frames.
		{"long", realStack(25), true, slices.Concat([]string{"stack:", "  goroutine 7 [running]:"}, user(0, 19), []string{"  ... 14 more lines (full stack in report.txt)"})},
		{"long, not written", realStack(25), false, slices.Concat([]string{"stack:", "  goroutine 7 [running]:"}, user(0, 19), []string{"  ... 14 more lines"})},
		{"re-panic", repanic, true, slices.Concat([]string{"stack:", "  goroutine 7 [running]:"}, user(0, 2), kernelFrame)},
	}
	if repanic == realStack(2) {
		t.Fatal("re-panic stack not built")
	}
	for _, c := range cases {
		f := &failure{kind: "panic", context: "panic-callback", message: "m", panic: &artifact.Panic{Stack: c.stack}}
		got := stackOf(consoleLines(f, "CMD", "off", nil, false, c.written))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
		var full []string
		for _, l := range stackLines(c.stack) {
			full = append(full, "  "+l)
		}
		if got := stackOf(consoleLines(f, "CMD", "off", nil, true, true)); !slices.Equal(got, append([]string{"stack:"}, full...)) {
			t.Errorf("%s, full: %q", c.name, got)
		}
	}
	// Without a user frame after the panic, the window starts right after the goroutine line.
	inner := "goroutine 1 [running]:\nruntime/debug.Stack()\n\t/x/stack.go:26\npanic({0x1, 0x2})\n\t/x/panic.go:787\ngithub.com/hmdsefi/faultline/kernel.f()\n\t/fl/kernel/f.go:1\n"
	f := &failure{kind: "panic", context: "panic-callback", message: "m", panic: &artifact.Panic{Stack: inner}}
	want := []string{"stack:"}
	for _, l := range stackLines(inner) {
		want = append(want, "  "+l)
	}
	if got := stackOf(consoleLines(f, "CMD", "off", nil, false, true)); !slices.Equal(got, want) {
		t.Errorf("no user frame: %q", got)
	}
	// A stack without a goroutine line or a panic line keeps its first 40 lines (TestConsoleLines).
}

type filesError struct{ files map[string][]byte }

func (e filesError) Error() string                    { return "files" }
func (e filesError) ArtifactFiles() map[string][]byte { return e.files }

// panicFiles is an error whose ArtifactFiles panics with value; nil means "boom".
type panicFiles struct{ value any }

func (panicFiles) Error() string { return "panics" }
func (p panicFiles) ArtifactFiles() map[string][]byte {
	if p.value == nil {
		panic("boom")
	}
	panic(p.value)
}

// API-077
func TestExtraFiles(t *testing.T) {
	f := &failure{kind: "final", finals: []finalResult{
		{check: "f1", err: filesError{map[string][]byte{"y.txt": []byte("Y"), "x.json": []byte("1")}}},
		{check: "p", panic: &artifact.Panic{}},
		{check: "f2", err: fmt.Errorf("wrapped: %w", filesError{map[string][]byte{"x.json": []byte("2")}})},
	}}
	files, warnings := extraFiles(f)
	if len(files) != 2 || files[0].name != "x.json" || string(files[0].data) != "1" || files[1].name != "y.txt" {
		t.Fatalf("files %+v", files)
	}
	if !slices.Equal(warnings, []string{`extra artifact file "x.json" from final check "f2" dropped: final check "f1" already added it`}) {
		t.Fatalf("warnings %q", warnings)
	}
	inv := &failure{kind: "invariant", err: &checkFailure{name: "i", err: filesError{map[string][]byte{"a.json": []byte("A")}}}}
	if files, _ := extraFiles(inv); len(files) != 1 || files[0].name != "a.json" {
		t.Fatalf("checkFailure unwrap: %+v", files)
	}
	if files, _ := extraFiles(&failure{kind: "fail", err: errors.New("plain")}); files != nil {
		t.Fatalf("plain error: %+v", files)
	}
	// ArtifactFiles is user code: a panic drops that source's files with a warning, and the other
	// sources still count.
	files, warnings = extraFiles(&failure{kind: "final", finals: []finalResult{
		{check: "a", err: panicFiles{}},
		{check: "b", err: filesError{map[string][]byte{"b.txt": []byte("B")}}},
	}})
	if len(files) != 1 || files[0].name != "b.txt" || !slices.Equal(warnings, []string{`extra artifact files from final check "a" dropped: ArtifactFiles panicked: boom`}) {
		t.Errorf("panicking final: files %+v warnings %q", files, warnings)
	}
	if files, warnings := extraFiles(&failure{kind: "invariant", err: &checkFailure{name: "i", err: panicFiles{}}}); files != nil ||
		!slices.Equal(warnings, []string{"extra artifact files from the seed's failure error dropped: ArtifactFiles panicked: boom"}) {
		t.Errorf("panicking failure error: files %+v warnings %q", files, warnings)
	}
	// panic("") is a panic too, and a panic text that is empty or holds a character that is not
	// printable is quoted, so each warning stays one visible line and writes no control sequence;
	// the later source's files still count.
	files, warnings = extraFiles(&failure{kind: "final", finals: []finalResult{
		{check: "e", err: panicFiles{""}},
		{check: "m", err: panicFiles{errors.New("a\nb")}},
		{check: "c", err: panicFiles{"a\rb\x1b[2J"}},
		{check: "b", err: filesError{map[string][]byte{"b.txt": []byte("B")}}},
	}})
	want := []string{
		`extra artifact files from final check "e" dropped: ArtifactFiles panicked: ""`,
		`extra artifact files from final check "m" dropped: ArtifactFiles panicked: "a\nb"`,
		`extra artifact files from final check "c" dropped: ArtifactFiles panicked: "a\rb\x1b[2J"`,
	}
	if len(files) != 1 || files[0].name != "b.txt" || !slices.Equal(warnings, want) {
		t.Errorf("empty, multi-line and control-character panics: files %+v warnings %q", files, warnings)
	}
	// The attempt collects them. A final failure's own error is not a second source.
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		unitTicker(w, nil)
		w.Final("f1", func() error { return filesError{map[string][]byte{"x.json": []byte("1")}} })
		w.Final("f2", func() error { return filesError{map[string][]byte{"x.json": []byte("2")}} })
	})
	res := r.attempt(t, 1, full(), nil, true)
	if res.fail == nil || res.fail.kind != "final" || len(res.extra) != 1 || string(res.extra[0].data) != "1" ||
		!slices.Equal(res.warnings, []string{`extra artifact file "x.json" from final check "f2" dropped: final check "f1" already added it`}) {
		t.Errorf("final: fail %+v extra %+v warnings %q", res.fail, res.extra, res.warnings)
	}
	// When the first failing final check panicked (kind panic), the other failing final checks'
	// files are still collected.
	r = unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		unitTicker(w, nil)
		w.Final("p", func() error { panic("P") })
		w.Final("f", func() error { return filesError{map[string][]byte{"x.json": []byte("1")}} })
	})
	res = r.attempt(t, 1, full(), nil, true)
	if res.fail == nil || res.fail.kind != "panic" || len(res.extra) != 1 || res.extra[0].name != "x.json" || string(res.extra[0].data) != "1" || len(res.warnings) != 0 {
		t.Errorf("panic in a final check: fail %+v extra %+v warnings %q", res.fail, res.extra, res.warnings)
	}
}

// API-083
func TestCompareReports(t *testing.T) {
	opts := artifact.RunOptions{DurationNS: int64(time.Minute), MaxEvents: 10, Mode: "event", Net: json.RawMessage(`{"a":1}`), Disk: json.RawMessage(`{}`)}
	prev := &artifact.Report{OptionsHash: "0x0000000000000000", Options: opts, Versions: artifact.Versions{Faultline: "v0.0.0-test", Go: "go1.26.1", TestBinarySHA256: "aa"}}
	cur := artifact.Versions{Faultline: "(devel)", Go: "go1.27.0", TestBinarySHA256: "bb"}
	want := []string{
		"previous artifact was recorded with faultline v0.0.0-test; this run uses (devel)",
		"previous artifact was recorded with go1.26; this run uses go1.27",
		"the test binary differs from the one that recorded the previous artifact (code, dependencies or build flags changed)",
		"options differ from the previous artifact (options hash 0x0000000000000000, now 0x0000000000000001)",
	}
	if got := compareReports(prev, cur, opts, "0x0000000000000001"); !slices.Equal(got, want) {
		t.Fatalf("warnings %q", got)
	}
	cur.TestBinarySHA256 = ""
	if got := compareReports(prev, cur, opts, "0x0000000000000000"); len(got) != 2 {
		t.Fatalf("warnings %q", got)
	}
	// No warning for what did not change: the same faultline version, the same Go minor, and a
	// binary hash missing on the previous side.
	same := &artifact.Report{OptionsHash: "0x1", Versions: artifact.Versions{Faultline: "v1", Go: "go1.26.2", TestBinarySHA256: ""}}
	if got := compareReports(same, artifact.Versions{Faultline: "v1", Go: "go1.26.2", TestBinarySHA256: "bb"}, opts, "0x1"); len(got) != 0 {
		t.Errorf("warnings %q", got)
	}

	// The options warning names every hashed option that changed, in API-082's order. JSON
	// objects compare in compact form: report.json stores them indented.
	now := opts
	now.DurationNS, now.MaxEvents, now.Mode = int64(30*time.Second), 2000, "goroutine"
	now.Net, now.Disk = json.RawMessage(`{"a":1}`), json.RawMessage(`{"b":2}`)
	now.NoCryptoSeed, now.AllowLimit, now.ScheduleHash = true, true, "0x00000000000000ab"
	prev.Options.Net = json.RawMessage("{\n  \"a\": 1\n}")
	got := compareReports(prev, prev.Versions, now, "0x0000000000000001")
	if w := "options differ from the previous artifact: Options.Duration was 1m0s, now 30s; Options.MaxEvents was 10, now 2000; Options.Mode was event, now goroutine; Options.Disk changed; " +
		"Options.NoCryptoSeed was false, now true; Options.AllowLimit was false, now true; FAULTLINE_SCHEDULE was not set, now schedule 0x00000000000000ab (options hash 0x0000000000000000, now 0x0000000000000001)"; !slices.Equal(got, []string{w}) {
		t.Errorf("options warning %q\nwant %q", got, w)
	}
	now = opts
	now.Net = json.RawMessage(`{"a":2}`)
	if got := compareReports(prev, prev.Versions, now, "0x2"); !slices.Equal(got, []string{"options differ from the previous artifact: Options.Net changed (options hash 0x0000000000000000, now 0x2)"}) {
		t.Errorf("net: %q", got)
	}
	// A schedule that was replayed and is not now, or another one.
	sched := &artifact.Report{OptionsHash: "0x0", Options: opts, Versions: prev.Versions}
	sched.Options.ScheduleHash = "0x00000000000000ab"
	for _, c := range []struct{ now, want string }{
		{"", "FAULTLINE_SCHEDULE was schedule 0x00000000000000ab, now not set"},
		{"0x00000000000000cd", "FAULTLINE_SCHEDULE was schedule 0x00000000000000ab, now schedule 0x00000000000000cd"},
	} {
		now = opts
		now.ScheduleHash = c.now
		if got := compareReports(sched, sched.Versions, now, "0x1"); !slices.Equal(got, []string{"options differ from the previous artifact: " + c.want + " (options hash 0x0, now 0x1)"}) {
			t.Errorf("schedule %q: %q", c.now, got)
		}
	}
	// JSON that is missing or does not decode is not the same as this run's.
	for _, net := range []string{"", "{"} {
		bad := &artifact.Report{OptionsHash: "0x0", Options: opts, Versions: prev.Versions}
		bad.Options.Net = json.RawMessage(net)
		if got := compareReports(bad, bad.Versions, opts, "0x1"); !slices.Equal(got, []string{"options differ from the previous artifact: Options.Net changed (options hash 0x0, now 0x1)"}) {
			t.Errorf("net %q: %q", net, got)
		}
	}
	// A report without options (every report Run writes has a mode) lists no changes: they would
	// all be false.
	bare := &artifact.Report{OptionsHash: "0x0", Versions: prev.Versions}
	if got := compareReports(bare, bare.Versions, opts, "0x1"); !slices.Equal(got, []string{"options differ from the previous artifact (options hash 0x0, now 0x1)"}) {
		t.Errorf("no options: %q", got)
	}

	// Text from the previous report.json is quoted when it is empty or holds a character that is
	// not printable, so a report cannot write control sequences to the terminal.
	bad := &artifact.Report{OptionsHash: "0x0\r", Options: opts, Versions: artifact.Versions{Faultline: "v1\x1b[2J", Go: "\u202egoX"}}
	bad.Options.Mode, bad.Options.ScheduleHash = "ev\tent", "0x\a"
	got = compareReports(bad, artifact.Versions{Faultline: "", Go: "go1.27.0"}, opts, "0x1")
	want = []string{
		`previous artifact was recorded with faultline "v1\x1b[2J"; this run uses ""`,
		`previous artifact was recorded with "\u202egoX"; this run uses go1.27`,
		`options differ from the previous artifact: Options.Mode was "ev\tent", now event; FAULTLINE_SCHEDULE was schedule "0x\a", now not set (options hash "0x0\r", now 0x1)`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("escaping:\n%q\nwant\n%q", got, want)
	}
	if got := printable("invariant:tick 5"); got != "invariant:tick 5" {
		t.Errorf("printable kept %q", got)
	}
}
