// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fault_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// canonical is the FLT-029 example, byte for byte.
const canonical = `{
  "faultline_schedule": 1,
  "events": [
    {"id": 1, "at": "1s", "kind": "partition", "groups": [["n1", "n2"], ["n3", "n4", "n5"]]},
    {"id": 2, "at": "1.5s", "kind": "link", "node": "n1", "peer": "n2", "link": {"latency": "50ms", "jitter": "10ms", "tail_ppm": 10000, "tail": "1s", "drop_ppm": 100000, "dup_ppm": 5000, "fifo": true}},
    {"id": 3, "at": "2s", "kind": "crash", "node": "n3"},
    {"id": 4, "at": "2.25s", "kind": "pause", "node": "n4"},
    {"id": 5, "at": "2.5s", "kind": "clock-jump", "node": "n5", "n": -250000000},
    {"id": 6, "at": "3s", "kind": "clock-drift", "node": "n2", "n": 150},
    {"id": 7, "at": "3s", "kind": "sync-fail", "node": "n1", "n": 2},
    {"id": 8, "at": "3.5s", "kind": "disk-capacity", "node": "n2", "n": 1048576},
    {"id": 9, "at": "4s", "kind": "corrupt", "node": "n1", "path": "/wal/0000000000000001.wal", "off": 4096, "len": 16},
    {"id": 10, "at": "5s", "kind": "heal", "undoes": [1]},
    {"id": 11, "at": "5s", "kind": "isolate", "node": "n5"},
    {"id": 12, "at": "5.5s", "kind": "cut", "node": "n1", "peer": "n3"},
    {"id": 13, "at": "6s", "kind": "heal-link", "node": "n1", "peer": "n3", "undoes": [12]},
    {"id": 14, "at": "6s", "kind": "link-reset", "node": "n1", "peer": "n2", "undoes": [2]},
    {"id": 15, "at": "6.5s", "kind": "resume", "node": "n4", "undoes": [4]},
    {"id": 16, "at": "7s", "kind": "restart", "node": "n3", "undoes": [3]},
    {"id": 17, "at": "7s", "kind": "sync-fail", "node": "n1", "n": 0, "undoes": [7]},
    {"id": 18, "at": "7s", "kind": "disk-capacity", "node": "n2", "n": 0, "undoes": [8]},
    {"id": 19, "at": "8s", "kind": "heal", "undoes": [11]}
  ]
}
`

func read(t *testing.T, text string) fault.Schedule {
	t.Helper()
	s, err := fault.ReadSchedule(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ReadSchedule: %v", err)
	}
	return s
}

func write(t *testing.T, s fault.Schedule) string {
	t.Helper()
	var b bytes.Buffer
	if err := s.Write(&b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return b.String()
}

// doc returns a version 1 document with the given events array.
func doc(events string) string { return `{"faultline_schedule": 1, "events": ` + events + `}` }

// AT-FLT-01
func TestCanonicalExample(t *testing.T) {
	s := read(t, canonical)
	if len(s.Events) != 19 {
		t.Fatalf("%d events, want 19", len(s.Events))
	}
	wantLink := simnet.Link{Latency: 50 * time.Millisecond, Jitter: 10 * time.Millisecond, TailPPM: 10000,
		Tail: time.Second, DropPPM: 100000, DupPPM: 5000, FIFO: true}
	if s.Events[1].Link == nil || *s.Events[1].Link != wantLink {
		t.Errorf("Events[1].Link = %+v", s.Events[1].Link)
	}
	if s.Events[4].N != -250000000 {
		t.Errorf("Events[4].N = %d", s.Events[4].N)
	}
	if !reflect.DeepEqual(s.Events[9].Undoes, []int{1}) || s.Events[2].Undoes != nil {
		t.Errorf("Undoes: %v, %v", s.Events[9].Undoes, s.Events[2].Undoes)
	}
	if !reflect.DeepEqual(s.Events[0].Groups, [][]string{{"n1", "n2"}, {"n3", "n4", "n5"}}) {
		t.Errorf("Events[0].Groups = %v", s.Events[0].Groups)
	}
	if got := write(t, s); got != canonical {
		t.Fatalf("Write differs from the canonical bytes:\n%s", got)
	}
	ns, err := s.Normalize()
	if err != nil || !reflect.DeepEqual(ns, s) {
		t.Fatalf("Normalize changed the canonical schedule: %v", err)
	}
	stripped := s
	stripped.Events = make([]fault.Event, len(s.Events))
	for i, e := range s.Events {
		e.ID, e.Undoes = 0, nil
		stripped.Events[i] = e
	}
	restored, err := stripped.Normalize()
	if err != nil || !reflect.DeepEqual(restored, s) {
		t.Fatalf("Normalize did not restore IDs and Undoes: %v", err)
	}
}

// AT-FLT-02
func TestSortingAndStability(t *testing.T) {
	s := read(t, `{"faultline_schedule": 1, "events": [{"at":"2s","kind":"heal"}, {"at":"1s","kind":"crash","node":"n1"}, {"at":"1s","kind":"crash","node":"n2"}, {"at":"0s","kind":"heal"}]}`)
	want := []fault.Event{
		{At: 0, Kind: "heal"},
		{At: kernel.Time(time.Second), Kind: "crash", Node: "n1"},
		{At: kernel.Time(time.Second), Kind: "crash", Node: "n2"},
		{At: kernel.Time(2 * time.Second), Kind: "heal"},
	}
	if !reflect.DeepEqual(s.Events, want) {
		t.Fatalf("events %+v", s.Events)
	}
	wantText := `{
  "faultline_schedule": 1,
  "events": [
    {"at": "0s", "kind": "heal"},
    {"at": "1s", "kind": "crash", "node": "n1"},
    {"at": "1s", "kind": "crash", "node": "n2"},
    {"at": "2s", "kind": "heal"}
  ]
}
`
	if got := write(t, s); got != wantText {
		t.Fatalf("Write:\n%s", got)
	}
}

// AT-FLT-03, FLT-024, FLT-025, FLT-026
func TestStrictParsing(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"faultline_schedule": 1, "events": [], "x": 1}`, `fault: schedule: unknown field "x"`},
		{`{"events": []}`, `fault: schedule: missing field "faultline_schedule"`},
		{`{"faultline_schedule": 2, "events": []}`, `fault: schedule: unsupported version 2 (supported: 1)`},
		{`{"faultline_schedule": 1}`, `fault: schedule: missing field "events"`},
		{`{"faultline_schedule": 1, "events": null}`, `fault: schedule: field "events" must not be null`},
		{`{"faultline_schedule": 1, "end": "-1s", "events": []}`, `fault: schedule: end must be >= 0 (got -1s)`},
		{`{"faultline_schedule": 1, "recovery": 5, "events": []}`, `fault: schedule: field "recovery": json: cannot unmarshal number into Go value of type string`},
		{doc(`[{"at": "1s", "kind": "crash", "node": "n1", "Node": "n2"}]`), `fault: schedule: events[0]: unknown field "Node"`},
		{doc(`[{"at": "1s", "node": "n1"}]`), `fault: schedule: events[0]: missing field "kind"`},
		{doc(`[{"at": "1s", "kind": "explode"}]`), `fault: schedule: events[0]: unknown kind "explode"`},
		{doc(`[{"at": "1.5", "kind": "crash", "node": "n1"}]`), `fault: schedule: events[0]: field "at": time: missing unit in duration "1.5"`},
		{doc(`[{"at": "-1s", "kind": "crash", "node": "n1"}]`), `fault: schedule: events[0]: crash: at must be >= 0 (got -1s)`},
		{doc(`[{"at": "1s", "kind": "crash"}]`), `fault: schedule: events[0]: crash: node is required`},
		{doc(`[{"at": "1s", "kind": "crash", "node": null}]`), `fault: schedule: events[0]: field "node" must not be null`},
		{doc(`[{"at": "1s", "kind": "crash", "node": "n1", "peer": "n2"}]`), `fault: schedule: events[0]: crash: peer is not allowed`},
		{doc(`[{"at": "1s", "kind": "sync-fail", "node": "n1"}]`), `fault: schedule: events[0]: sync-fail: n is required`},
		{doc(`[{"at": "1s", "kind": "clock-jump", "node": "n1", "n": 1.5}]`), `fault: schedule: events[0]: field "n": json: cannot unmarshal number 1.5 into Go value of type int64`},
		{doc(`[{"at": "1s", "kind": "clock-jump", "node": "n1", "n": 0}]`), `fault: schedule: events[0]: clock-jump: n must not be 0`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"loss": 5}}]`), `fault: schedule: events[0]: field "link": unknown field "loss"`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"drop_ppm": 2000000}}]`), `fault: schedule: events[0]: link: invalid link: DropPPM 2000000 exceeds 1000000`},
		{doc(`[{"at": "1s", "kind": "partition", "groups": [["n1"], ["n1", "n2"]]}]`), `fault: schedule: events[0]: partition: node "n1" appears more than once`},
		{doc(`[{"at": "1s", "kind": "cut", "node": "n1", "peer": "n1"}]`), `fault: schedule: events[0]: cut: node and peer must differ ("n1")`},
		{doc(`[{"at": "1s", "kind": "crash", "node": "n1", "role": "leader"}]`), `fault: schedule: events[0]: crash: node and role are mutually exclusive`},
		// further FLT-024 to FLT-026 rows
		{`{"faultline_schedule": null, "events": []}`, `fault: schedule: field "faultline_schedule" must not be null`},
		{`{"faultline_schedule": 1.0, "events": []}`, `fault: schedule: field "faultline_schedule": json: cannot unmarshal number 1.0 into Go value of type int`},
		{`{"faultline_schedule": 1, "end": null, "events": []}`, `fault: schedule: field "end" must not be null`},
		{doc(`[{"kind": "heal"}]`), `fault: schedule: events[0]: missing field "at"`},
		{doc(`[{"at": "1s", "kind": ""}]`), `fault: schedule: events[0]: missing kind`},
		{doc(`[{"at": "1s", "kind": "heal", "node": "n1"}]`), `fault: schedule: events[0]: heal: node is not allowed`},
		{doc(`[{"at": "1s", "kind": "heal", "role": "x"}]`), `fault: schedule: events[0]: heal: role is not allowed`},
		{doc(`[{"at": "1s", "kind": "cut", "node": "n1"}]`), `fault: schedule: events[0]: cut: peer is required`},
		{doc(`[{"at": "1s", "kind": "corrupt", "node": "n1", "path": "/f", "len": 1}]`), `fault: schedule: events[0]: corrupt: off is required`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"latency": null}}]`), `fault: schedule: events[0]: field "link": field "latency" must not be null`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"latency": 5}}]`), `fault: schedule: events[0]: field "link": field "latency": json: cannot unmarshal number into Go value of type string`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"drop_ppm": -1}}]`), `fault: schedule: events[0]: field "link": drop_ppm out of range (got -1)`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"tail_ppm": 4294967296}}]`), `fault: schedule: events[0]: field "link": tail_ppm out of range (got 4294967296)`},
		{doc(`[{"at": "1s", "kind": "heal", "undoes": [0]}]`), `fault: schedule: events[0]: heal: undoes[0] must be >= 1 (got 0)`},
		{doc(`[{"at": "1s", "kind": "heal"}, {"at": "1s", "kind": "crash"}]`), `fault: schedule: events[1]: crash: node is required`},
		{`{"faultline_schedule": 0, "events": []}`, `fault: schedule: unsupported version 0 (supported: 1)`},
		{`{"faultline_schedule": 1, "end": "-1ns", "events": []}`, `fault: schedule: end must be >= 0 (got -1ns)`},
		// the smallest unknown key, in byte order, at each level
		{`{"faultline_schedule": 1, "events": [], "b": 1, "a": 2}`, `fault: schedule: unknown field "a"`},
		{`{"faultline_schedule": 1, "events": [], "a": 1, "B": 2}`, `fault: schedule: unknown field "B"`},
		{doc(`[{"at": "1s", "kind": "heal", "b": 1, "a": 2}]`), `fault: schedule: events[0]: unknown field "a"`},
		{doc(`[{"at": "1s", "kind": "heal", "a": 1, "B": 2}]`), `fault: schedule: events[0]: unknown field "B"`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"b": 1, "a": 2}}]`), `fault: schedule: events[0]: field "link": unknown field "a"`},
		// FLT-026 step 6: rules on present and absent keys, which Event.Validate cannot see
		{doc(`[{"at": "1s", "kind": "crash", "node": "", "role": "r"}]`), `fault: schedule: events[0]: crash: node and role are mutually exclusive`},
		{doc(`[{"at": "1s", "kind": "heal", "node": "n1", "role": "r"}]`), `fault: schedule: events[0]: heal: node and role are mutually exclusive`},
		{doc(`[{"at": "1s", "kind": "crash", "node": "n1", "peer": ""}]`), `fault: schedule: events[0]: crash: peer is not allowed`},
		{doc(`[{"at": "1s", "kind": "sync-fail", "node": "n1", "peer": "n2"}]`), `fault: schedule: events[0]: sync-fail: peer is not allowed`},
		{doc(`[{"at": "1s", "kind": "heal", "peer": "x", "node": "y"}]`), `fault: schedule: events[0]: heal: node is not allowed`},
		{doc(`[{"at": "1s", "kind": "corrupt", "node": "n1"}]`), `fault: schedule: events[0]: corrupt: path is required`},
		// check order: each input breaks two rules, and the earlier one is reported
		{`{"x": 1}`, `fault: schedule: unknown field "x"`},
		{`{"faultline_schedule": 2, "end": "-1s"}`, `fault: schedule: unsupported version 2 (supported: 1)`},
		{`{"faultline_schedule": 1, "end": "-1s", "recovery": null}`, `fault: schedule: end must be >= 0 (got -1s)`},
		{`{"faultline_schedule": 1, "end": null, "recovery": "-1s"}`, `fault: schedule: field "end" must not be null`},
		{`{"faultline_schedule": 1, "recovery": "-1s"}`, `fault: schedule: recovery must be >= 0 (got -1s)`},
		{doc(`[{"x": 1, "at": null}]`), `fault: schedule: events[0]: unknown field "x"`},
		{doc(`[{"node": null}]`), `fault: schedule: events[0]: field "node" must not be null`},
		{doc(`[{"peer": null, "at": null}]`), `fault: schedule: events[0]: field "at" must not be null`},
		{doc(`[{}]`), `fault: schedule: events[0]: missing field "at"`},
		{doc(`[{"at": "1.5"}]`), `fault: schedule: events[0]: missing field "kind"`},
		{doc(`[{"at": "1.5", "kind": "explode"}]`), `fault: schedule: events[0]: field "at": time: missing unit in duration "1.5"`},
		{doc(`[{"len": "x", "id": "y", "at": "1s", "kind": "heal"}]`), `fault: schedule: events[0]: field "id": json: cannot unmarshal string into Go value of type int`},
		{doc(`[{"at": "-1s", "kind": "sync-fail", "node": "n1"}]`), `fault: schedule: events[0]: sync-fail: n is required`},
		{doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"jitter": null, "latency": 5}}]`), `fault: schedule: events[0]: field "link": field "latency": json: cannot unmarshal number into Go value of type string`},
	}
	for _, c := range cases {
		_, err := fault.ReadSchedule(strings.NewReader(c.in))
		if err == nil || err.Error() != c.want {
			t.Errorf("ReadSchedule(%s)\n got %v\nwant %s", c.in, err, c.want)
		}
	}
	prefixes := []struct{ in, prefix string }{
		{`{"faultline_schedule": 1, "events": []} x`, "fault: schedule: invalid JSON: "},
		{`[]`, "fault: schedule: invalid JSON: "},
		// the type name in encoding/json's message differs between Go releases
		{`{"faultline_schedule": 1, "events": {}}`, `fault: schedule: field "events": json: cannot unmarshal object into Go value of type `},
	}
	for _, c := range prefixes {
		_, err := fault.ReadSchedule(strings.NewReader(c.in))
		if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
			t.Errorf("ReadSchedule(%s) = %v, want prefix %q", c.in, err, c.prefix)
		}
	}
}

// FLT §7: errors.As reaches the encoding/json error inside every wrapping.
func TestParsingErrorChain(t *testing.T) {
	var syntax *json.SyntaxError
	if _, err := fault.ReadSchedule(strings.NewReader(`{"faultline_schedule": 1, "events": []} x`)); !errors.As(err, &syntax) {
		t.Errorf("trailing data: %v does not wrap a *json.SyntaxError", err)
	}
	for _, in := range []string{
		`[]`,
		doc(`[{"at": "1s", "kind": "clock-jump", "node": "n1", "n": 1.5}]`),
		doc(`[{"at": "1s", "kind": "link", "node": "n1", "peer": "n2", "link": {"dup_ppm": 1.0}}]`),
	} {
		var typ *json.UnmarshalTypeError
		if _, err := fault.ReadSchedule(strings.NewReader(in)); !errors.As(err, &typ) {
			t.Errorf("ReadSchedule(%s) = %v, which does not wrap a *json.UnmarshalTypeError", in, err)
		}
	}
}

// failingReader returns the bytes of data, then err.
type failingReader struct {
	data string
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// countingWriter counts Write calls and fails each one with err if err is set.
type countingWriter struct {
	bytes.Buffer
	calls int
	err   error
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.err != nil {
		return 0, w.err
	}
	return w.Buffer.Write(p)
}

// FLT-024 and FLT-028: the reader's and the writer's errors are returned, and Write calls w.Write
// once.
func TestReadWriteErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, data := range []string{"", `{"faultline_schedule": 1, "events": []}`} {
		_, err := fault.ReadSchedule(&failingReader{data: data, err: boom})
		if err == nil || err.Error() != "fault: schedule: read: boom" || !errors.Is(err, boom) {
			t.Errorf("ReadSchedule after %q, then an error: %v", data, err)
		}
	}
	s := read(t, canonical)
	w := &countingWriter{}
	if err := s.Write(w); err != nil || w.calls != 1 || w.String() != canonical {
		t.Errorf("Write: %v, %d calls", err, w.calls)
	}
	w = &countingWriter{err: boom}
	if err := s.Write(w); err != boom || w.calls != 1 {
		t.Errorf("Write to a failing writer: %v, %d calls", err, w.calls)
	}
}

// FLT-022, FLT-025, FLT-026, FLT-027
func TestParsingDetails(t *testing.T) {
	s := read(t, `{"faultline_schedule": 1, "events": [
		{"id": 3, "at": "250us", "kind": "heal", "undoes": [9]},
		{"at": "1s500ms", "kind": "crash", "role": "leader"},
		{"at": "250μs", "kind": "link", "node": "n1", "peer": "n2", "link": {}},
		{"at": "1500ms", "kind": "restart", "node": "n1", "undoes": []}]}`)
	if s.Events[0].ID != 3 || !reflect.DeepEqual(s.Events[0].Undoes, []int{9}) || s.Events[0].At != kernel.Time(250*time.Microsecond) {
		t.Errorf("annotations not returned as written: %+v", s.Events[0])
	}
	if s.Events[1].At != kernel.Time(250*time.Microsecond) || *s.Events[1].Link != (simnet.Link{}) {
		t.Errorf("U+03BC duration or empty link: %+v", s.Events[1])
	}
	if s.Events[2].Role != "leader" || s.Events[2].At != kernel.Time(1500*time.Millisecond) {
		t.Errorf("role event: %+v", s.Events[2])
	}
	if s.Events[3].Undoes != nil {
		t.Errorf("an empty undoes array must become nil")
	}
	got := write(t, s)
	for _, want := range []string{
		`{"id": 3, "at": "250µs", "kind": "heal", "undoes": [9]}`,
		`{"at": "250µs", "kind": "link", "node": "n1", "peer": "n2", "link": {}}`,
		`{"at": "1.5s", "kind": "crash", "role": "leader"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Write output lacks %s:\n%s", want, got)
		}
	}
	// FLT-024 step 1: of duplicate keys, the last one wins
	dup := read(t, `{"faultline_schedule": 2, "faultline_schedule": 1, "end": "1m", "recovery": "45s", "events": [{"at": "1s", "kind": "crash", "node": "a", "node": "b"}]}`)
	if dup.End != kernel.Time(time.Minute) || dup.Recovery != kernel.Time(45*time.Second) || dup.Events[0].Node != "b" {
		t.Errorf("duplicate keys, End and Recovery: %+v", dup)
	}
	esc := fault.Schedule{Events: []fault.Event{{Kind: "corrupt", Node: "n<1>", Path: "/a&b", Len: 1}}}
	if out := write(t, esc); !strings.Contains(out, `{"at": "0s", "kind": "corrupt", "node": "n\u003c1\u003e", "path": "/a\u0026b", "off": 0, "len": 1}`) {
		t.Errorf("escaping or zero fields of the field set:\n%s", out)
	}
}

// AT-FLT-04
func TestWriteValidatesFirst(t *testing.T) {
	var buf bytes.Buffer
	err := fault.Schedule{Events: []fault.Event{{Kind: fault.KindCrash}}}.Write(&buf)
	if err == nil || err.Error() != "fault: schedule: events[0]: crash: node is required" || buf.Len() != 0 {
		t.Fatalf("Write of an invalid schedule: %v, %d bytes", err, buf.Len())
	}
	err = fault.Schedule{Version: 3}.Write(&buf)
	if err == nil || err.Error() != "fault: schedule: unsupported version 3 (supported: 1)" {
		t.Fatalf("Write with version 3: %v", err)
	}
	if got := write(t, fault.Schedule{}); got != "{\n  \"faultline_schedule\": 1,\n  \"events\": []\n}\n" {
		t.Fatalf("Write(Schedule{}) = %q", got)
	}
	s := fault.Schedule{End: kernel.Time(time.Minute), Recovery: kernel.Time(45 * time.Second)}
	got := write(t, s)
	if got != "{\n  \"faultline_schedule\": 1,\n  \"end\": \"1m0s\",\n  \"recovery\": \"45s\",\n  \"events\": []\n}\n" {
		t.Fatalf("Write with End and Recovery = %q", got)
	}
	back := read(t, got)
	if back.End != s.End || back.Recovery != s.Recovery {
		t.Fatalf("read back End %v Recovery %v", back.End, back.Recovery)
	}
	micro := fault.Schedule{Events: []fault.Event{{At: kernel.Time(1500 * time.Nanosecond), Kind: "heal"}}}
	got = write(t, micro)
	if !strings.Contains(got, `"at": "1.5µs"`) || read(t, got).Events[0].At != micro.Events[0].At {
		t.Fatalf("sub-millisecond at: %s", got)
	}
	// FLT-028, FLT-006: events in stable At order, without reordering the caller's slice; an
	// empty Undoes is not written (FLT-021).
	unsorted := fault.Schedule{Events: []fault.Event{
		{At: 2, Kind: "heal", Undoes: []int{}},
		{At: 1, Kind: "crash", Node: "n2"},
		{At: 1, Kind: "crash", Node: "n1"},
	}}
	before := append([]fault.Event(nil), unsorted.Events...)
	want := "{\n  \"faultline_schedule\": 1,\n  \"events\": [\n" +
		"    {\"at\": \"1ns\", \"kind\": \"crash\", \"node\": \"n2\"},\n" +
		"    {\"at\": \"1ns\", \"kind\": \"crash\", \"node\": \"n1\"},\n" +
		"    {\"at\": \"2ns\", \"kind\": \"heal\"}\n" +
		"  ]\n}\n"
	if got := write(t, unsorted); got != want {
		t.Fatalf("Write of unsorted events:\n%s", got)
	}
	if !reflect.DeepEqual(unsorted.Events, before) {
		t.Fatalf("Write reordered the caller's events: %#v", unsorted.Events)
	}
}

// allKinds returns a 10,000-event schedule using every kind, for the benchmarks.
func allKinds(tb testing.TB) fault.Schedule {
	tb.Helper()
	l := &simnet.Link{Latency: 50 * time.Millisecond, DropPPM: 100000}
	proto := []fault.Event{
		{Kind: "partition", Groups: [][]string{{"n1", "n2"}, {"n3", "n4", "n5"}}},
		{Kind: "isolate", Node: "n1"}, {Kind: "cut", Node: "n1", Peer: "n2"}, {Kind: "heal"},
		{Kind: "heal-link", Node: "n1", Peer: "n2"}, {Kind: "link", Node: "n1", Peer: "n2", Link: l},
		{Kind: "link-reset", Node: "n1", Peer: "n2"}, {Kind: "crash", Node: "n3"}, {Kind: "restart", Node: "n3"},
		{Kind: "pause", Node: "n4"}, {Kind: "resume", Node: "n4"}, {Kind: "clock-jump", Node: "n5", N: -250000000},
		{Kind: "clock-drift", Node: "n2", N: 150}, {Kind: "sync-fail", Node: "n1", N: 2},
		{Kind: "disk-capacity", Node: "n2", N: 1048576}, {Kind: "corrupt", Node: "n1", Path: "/wal/1", Off: 4096, Len: 16},
	}
	s := fault.Schedule{}
	for i := 0; i < 10000; i++ {
		e := proto[i%len(proto)]
		e.At = kernel.Time(time.Duration(i) * time.Millisecond)
		s.Events = append(s.Events, e)
	}
	ns, err := s.Normalize()
	if err != nil {
		tb.Fatal(err)
	}
	return ns
}

// FLT §9: a document in the layout that Write writes is read, and written, without per-key
// encoding/json work: at most 3 allocations per event to read and 1 to write.
func TestScheduleAllocs(t *testing.T) {
	s := allKinds(t)
	var buf bytes.Buffer
	if err := s.Write(&buf); err != nil {
		t.Fatal(err)
	}
	readAllocs := testing.AllocsPerRun(3, func() {
		if _, err := fault.ReadSchedule(bytes.NewReader(buf.Bytes())); err != nil {
			t.Fatal(err)
		}
	})
	writeAllocs := testing.AllocsPerRun(3, func() {
		var b bytes.Buffer
		if err := s.Write(&b); err != nil {
			t.Fatal(err)
		}
	})
	if n := float64(len(s.Events)); readAllocs > 3*n || writeAllocs > n {
		t.Fatalf("%.0f allocations to read and %.0f to write %.0f events, want at most 3 and 1 per event", readAllocs, writeAllocs, n)
	}
}

func BenchmarkReadSchedule10k(b *testing.B) {
	var buf bytes.Buffer
	if err := allKinds(b).Write(&buf); err != nil {
		b.Fatal(err)
	}
	data := buf.Bytes()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fault.ReadSchedule(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteSchedule10k(b *testing.B) {
	s := allKinds(b)
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := s.Write(&buf); err != nil {
			b.Fatal(err)
		}
	}
}

// FLT-028, FLT-004: Write refuses every string that is not valid UTF-8 and writes nothing, so a
// written schedule has the same bytes on Go 1.26 and Go 1.27, whose json.Marshal differ only for
// such strings (ART-030). Strings that take json.Marshal's escaping are written, and read back,
// the same on both.
func TestWriteInvalidUTF8(t *testing.T) {
	for _, c := range []struct {
		e    fault.Event
		want string
	}{
		{fault.Event{Kind: "crash", Node: "n\xff"}, "crash: node is not valid UTF-8"},
		{fault.Event{Kind: "isolate", Role: "r\xff"}, "isolate: role is not valid UTF-8"},
		{fault.Event{Kind: "cut", Node: "n1", Peer: "\xed\xa0\x80"}, "cut: peer is not valid UTF-8"},
		{fault.Event{Kind: "partition", Groups: [][]string{{"n1"}, {"n2", "\xfe"}}}, "partition: groups[1][1] is not valid UTF-8"},
		{fault.Event{Kind: "corrupt", Node: "n1", Path: "/w\xff", Len: 1}, "corrupt: path is not valid UTF-8"},
	} {
		var buf bytes.Buffer
		err := fault.Schedule{Events: []fault.Event{c.e}}.Write(&buf)
		if err == nil || err.Error() != "fault: schedule: events[0]: "+c.want || buf.Len() != 0 {
			t.Errorf("Write of %+v: %v, %d bytes", c.e, err, buf.Len())
		}
	}
	// é, U+2028, <, &, >, U+FFFD itself, a quote, a backslash, U+0001 and DEL.
	s := fault.Schedule{Events: []fault.Event{{At: 1, Kind: "partition", Groups: [][]string{{"é\u2028<&>"}, {"\uFFFD\"\\\x01\x7f"}}}}}
	want := "{\n  \"faultline_schedule\": 1,\n  \"events\": [\n" +
		"    {\"at\": \"1ns\", \"kind\": \"partition\", \"groups\": [[\"é\\u2028\\u003c\\u0026\\u003e\"], [\"\uFFFD\\\"\\\\\\u0001\x7f\"]]}\n" +
		"  ]\n}\n"
	if got := write(t, s); got != want {
		t.Fatalf("Write =\n%q\nwant\n%q", got, want)
	}
	if back := read(t, want); !reflect.DeepEqual(back.Events, s.Events) {
		t.Fatalf("read back %+v", back.Events)
	}
}
