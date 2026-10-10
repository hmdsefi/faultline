// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
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
	// Line 3 of API-071, word for word.
	const causes = "common causes: state kept between runs in the same process (package-level variables, sync.Once, caches), map iteration order, global math/rand, wall-clock time, goroutines"
	orig := &failure{kind: "invariant", check: "i", context: "invariant", at: 0}
	f := newDeterminismFailure("artifact_rerun", 0x5e, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}}, orig, a, b, attemptResult{now: 7})
	want := strings.Join([]string{
		`the first run failed: invariant "i" violated at t=0.000000000s`,
		"first difference at record index 1:",
		`A: #2 t=0.000000005s node=1#1 net.send cause=0 "x" msg="1"`,
		`B: #2 t=0.000000005s node=1#1 net.send cause=0 "y"`,
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
	h := newDeterminismFailure("check_determinism", 1, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}, {hash: 4}}, nil, a[:1], a, attemptResult{})
	if !strings.Contains(h.message, "A: (trace ended after 1 records)\nB: #2 ") {
		t.Fatalf("absent side:\n%s", h.message)
	}
	// B ended: A shows its first extra record, B the count it ended after.
	if l := newDeterminismFailure("check_determinism", 1, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}, {hash: 4}}, nil, a, a[:1], attemptResult{}); !strings.HasPrefix(l.message, "first difference at record index 1:\nA: #2 t=0.000000005s node=1#1 net.send cause=0 \"x\" msg=\"1\"\nB: (trace ended after 1 records)\n") {
		t.Errorf("B ended:\n%s", l.message)
	}
	// The line shows node, incarnation and cause, so a difference only in Cause is visible.
	ca := []kernel.Record{{Seq: 5, At: 10, Node: 2, Inc: 5, Kind: "kernel.event", Cause: 3, Text: "tick"}}
	cb := []kernel.Record{{Seq: 5, At: 10, Node: 2, Inc: 5, Kind: "kernel.event", Cause: 4, Text: "tick"}}
	if c := newDeterminismFailure("check_determinism", 1, []attemptResult{{hash: 1}, {hash: 2}, {hash: 3}, {hash: 4}}, nil, ca, cb, attemptResult{}); !strings.HasPrefix(c.message, "first difference at record index 0:\nA: #5 t=0.000000010s node=2#5 kernel.event cause=3 \"tick\"\nB: #5 t=0.000000010s node=2#5 kernel.event cause=4 \"tick\"\n") {
		t.Errorf("cause only:\n%s", c.message)
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
	// panic("") is a panic too, and an empty or multi-line panic text is quoted, so each warning
	// stays one visible line; the later source's files still count.
	files, warnings = extraFiles(&failure{kind: "final", finals: []finalResult{
		{check: "e", err: panicFiles{""}},
		{check: "m", err: panicFiles{errors.New("a\nb")}},
		{check: "b", err: filesError{map[string][]byte{"b.txt": []byte("B")}}},
	}})
	want := []string{
		`extra artifact files from final check "e" dropped: ArtifactFiles panicked: ""`,
		`extra artifact files from final check "m" dropped: ArtifactFiles panicked: "a\nb"`,
	}
	if len(files) != 1 || files[0].name != "b.txt" || !slices.Equal(warnings, want) {
		t.Errorf("empty and multi-line panics: files %+v warnings %q", files, warnings)
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
	prev := &artifact.Report{OptionsHash: "0x0000000000000000", Versions: artifact.Versions{Faultline: "v0.0.0-test", Go: "go1.26.1", TestBinarySHA256: "aa"}}
	cur := artifact.Versions{Faultline: "(devel)", Go: "go1.27.0", TestBinarySHA256: "bb"}
	want := []string{
		"previous artifact was recorded with faultline v0.0.0-test; this run uses (devel)",
		"previous artifact was recorded with go1.26; this run uses go1.27",
		"the test binary differs from the one that recorded the previous artifact (code, dependencies or build flags changed)",
		"options differ from the previous artifact (options hash 0x0000000000000000, now 0x0000000000000001)",
	}
	if got := compareReports(prev, cur, "0x0000000000000001"); !slices.Equal(got, want) {
		t.Fatalf("warnings %q", got)
	}
	cur.TestBinarySHA256 = ""
	if got := compareReports(prev, cur, "0x0000000000000000"); len(got) != 2 {
		t.Fatalf("warnings %q", got)
	}
	// No warning for what did not change: the same faultline version, the same Go minor, and a
	// binary hash missing on the previous side.
	prev = &artifact.Report{OptionsHash: "0x1", Versions: artifact.Versions{Faultline: "v1", Go: "go1.26.1", TestBinarySHA256: ""}}
	if got := compareReports(prev, artifact.Versions{Faultline: "v1", Go: "go1.26.2", TestBinarySHA256: "bb"}, "0x1"); len(got) != 0 {
		t.Errorf("warnings %q", got)
	}
}
