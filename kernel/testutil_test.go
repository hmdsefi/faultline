package kernel

import (
	"fmt"
	"strings"
	"testing"
)

// mustPanic calls fn and fails the test unless it panics with exactly the value want.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		v := recover()
		if v == nil {
			t.Fatalf("no panic; want %q", want)
		}
		if got := fmt.Sprint(v); got != want {
			t.Fatalf("panic %q; want %q", got, want)
		}
	}()
	fn()
}

// recLine renders r in the notation of KRN §10: `Seq At Node/Inc Kind cause=C "Text" [k=v ...]`.
func recLine(r Record) string {
	attrs := make([]string, len(r.Attrs))
	for i, a := range r.Attrs {
		attrs[i] = a.Key + "=" + a.Value
	}
	return fmt.Sprintf("%d %s %d/%d %s cause=%d %q [%s]",
		r.Seq, r.At, r.Node, r.Inc, r.Kind, r.Cause, r.Text, strings.Join(attrs, " "))
}

// checkRecords compares the records of s, rendered with recLine, with want: one record per
// non-empty line, leading and trailing spaces ignored.
func checkRecords(t *testing.T, s *Sim, want string) {
	t.Helper()
	var wantLines []string
	for _, l := range strings.Split(want, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			wantLines = append(wantLines, l)
		}
	}
	rs := s.Records()
	for i := 0; i < len(rs) || i < len(wantLines); i++ {
		got, exp := "<none>", "<none>"
		if i < len(rs) {
			got = recLine(rs[i])
		}
		if i < len(wantLines) {
			exp = wantLines[i]
		}
		if got != exp {
			t.Errorf("record %d:\n got  %s\n want %s", i+1, got, exp)
		}
	}
}

// fullSim returns New(Config{Seed: seed, Trace: TraceConfig{Level: TraceFull}}).
func fullSim(seed uint64) *Sim {
	return New(Config{Seed: seed, Trace: TraceConfig{Level: TraceFull}})
}
