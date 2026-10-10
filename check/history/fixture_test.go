// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package history_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
)

// fixture is HIS §10 fixture H.
type fixture struct {
	s      *kernel.Sim
	c1, c2 *kernel.Node
	r      *history.Recorder
}

// newH creates a TraceFull Sim with seed 1, nodes c1 and c2 with empty boots, runs to t=0, and
// returns a new recorder.
func newH() *fixture {
	h := &fixture{s: kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})}
	h.c1 = h.s.AddNode("c1", func(*kernel.Node) {})
	h.c2 = h.s.AddNode("c2", func(*kernel.Node) {})
	h.s.RunUntil(0)
	h.r = history.NewRecorder(h.s)
	return h
}

// T is s.RunUntil(ms milliseconds); calls after it run outside any event at that time.
func (h *fixture) T(ms int) { h.s.RunUntil(kernel.Time(time.Duration(ms) * time.Millisecond)) }

// at01 builds the history of AT-HIS-01 on a fresh fixture H.
func at01(t *testing.T) *fixture {
	t.Helper()
	h := newH()
	h.T(1)
	if id := h.r.Invoke("c1", "write", map[string]any{"value": 1, "key": "x"}); id != 1 {
		t.Fatalf("Invoke = %d, want 1", id)
	}
	h.T(2)
	if id := h.r.Invoke("c2", "read", map[string]string{"key": "x"}); id != 2 {
		t.Fatalf("Invoke = %d, want 2", id)
	}
	h.T(3)
	h.r.Complete(1, history.OK, nil)
	h.r.Complete(2, history.OK, 1)
	if id := h.r.Invoke("c1", "read", map[string]string{"key": "x"}); id != 3 {
		t.Fatalf("Invoke = %d, want 3", id)
	}
	return h
}

// records returns the kept records of kind.
func (h *fixture) records(kind string) []kernel.Record {
	var out []kernel.Record
	for _, r := range h.s.Records() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// attrString renders attributes as "k=v, k=v".
func attrString(r kernel.Record) string {
	parts := make([]string, len(r.Attrs))
	for i, a := range r.Attrs {
		parts[i] = a.Key + "=" + a.Value
	}
	return strings.Join(parts, ", ")
}

// mustPanic runs f and checks that it panics with the string want.
func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		got := recover()
		if s, ok := got.(string); !ok || s != want {
			t.Errorf("panic %v, want %q", got, want)
		}
	}()
	f()
}

// rawString returns v, which must be a json.RawMessage, as a string.
func rawString(t *testing.T, v any) string {
	t.Helper()
	m, ok := v.(json.RawMessage)
	if !ok {
		t.Fatalf("%T is not a json.RawMessage", v)
	}
	return string(m)
}

// ms converts milliseconds to kernel.Time.
func ms(n int) kernel.Time { return kernel.Time(time.Duration(n) * time.Millisecond) }
