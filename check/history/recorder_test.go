package history_test

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/hmdsefi/faultline/check/history"
)

func TestStatusString(t *testing.T) {
	for s, want := range map[history.Status]string{history.Pending: "pending", history.OK: "ok", history.Fail: "fail",
		history.Info: "info", 9: "status(9)"} {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// AT-HIS-01, HIS-004, HIS-006, HIS-007, HIS-012
func TestRecording(t *testing.T) {
	h := at01(t)
	if h.r.Len() != 3 {
		t.Fatalf("Len() = %d", h.r.Len())
	}
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	want := []history.Op{
		{ID: 1, Process: "c1", F: "write", Status: history.OK, Call: ms(1), Return: ms(3), CallIndex: 1, ReturnIndex: 3,
			Input: raw(`{"key":"x","value":1}`), Output: raw(`null`)},
		{ID: 2, Process: "c2", F: "read", Status: history.OK, Call: ms(2), Return: ms(3), CallIndex: 2, ReturnIndex: 4,
			Input: raw(`{"key":"x"}`), Output: raw(`1`)},
		{ID: 3, Process: "c1", F: "read", Status: history.Pending, Call: ms(3), Return: 0, CallIndex: 5, ReturnIndex: 0,
			Input: raw(`{"key":"x"}`), Output: raw(`null`)},
	}
	if got := h.r.Ops(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Ops() = %+v\nwant   %+v", got, want)
	}
	ops := h.r.Ops()
	ops[0].F = "changed"
	if h.r.Ops()[0].F != "write" {
		t.Fatalf("Ops() is not a copy")
	}
	mustPanic(t, "history: NewRecorder: nil *kernel.Sim", func() { history.NewRecorder(nil) })
}

// AT-HIS-04, HIS-003
func TestInvokeMisuse(t *testing.T) {
	h := newH()
	kx := map[string]string{"key": "x"}
	h.r.Invoke("c1", "write", map[string]any{"key": "x", "value": 1})
	cases := []struct {
		call func()
		want string
	}{
		{func() { h.r.Invoke("", "read", kx) }, "history: Invoke: process must not be empty"},
		{func() { h.r.Invoke("c2", "", kx) }, "history: Invoke: f must not be empty"},
		{func() { h.r.Invoke("\xff", "read", kx) }, "history: Invoke: process is not valid UTF-8"},
		{func() { h.r.Invoke("c2", "\xff", kx) }, "history: Invoke: f is not valid UTF-8"},
		{func() { h.r.Invoke("c1", "read", kx) }, `history: Invoke: process "c1" has pending op 1 (write)`},
		{func() { h.r.Invoke("c2", "read", make(chan int)) },
			`history: Invoke: process "c2" f "read": input is not JSON-encodable: json: unsupported type: chan int`},
		{func() { h.r.Invoke("c2", "read", math.NaN()) },
			`history: Invoke: process "c2" f "read": input is not JSON-encodable: json: unsupported value: NaN`},
		// HIS-003 order: two or three checks fail and the first one wins. Together these rows cover
		// every pair of checks that can fail at once (a process that fails a name check never has a pending op).
		{func() { h.r.Invoke("", "", make(chan int)) }, "history: Invoke: process must not be empty"},
		{func() { h.r.Invoke("", "\xff", make(chan int)) }, "history: Invoke: process must not be empty"},
		{func() { h.r.Invoke("\xff", "", make(chan int)) }, "history: Invoke: process is not valid UTF-8"},
		{func() { h.r.Invoke("\xff", "\xff", make(chan int)) }, "history: Invoke: process is not valid UTF-8"},
		{func() { h.r.Invoke("c1", "", make(chan int)) }, "history: Invoke: f must not be empty"},
		{func() { h.r.Invoke("c1", "\xff", make(chan int)) }, "history: Invoke: f is not valid UTF-8"},
		{func() { h.r.Invoke("c1", "read", make(chan int)) }, `history: Invoke: process "c1" has pending op 1 (write)`},
	}
	recs := len(h.s.Records())
	for _, c := range cases {
		mustPanic(t, c.want, c.call)
	}
	if h.r.Len() != 1 || len(h.s.Records()) != recs {
		t.Fatalf("a panicking Invoke changed the recorder (Len %d)", h.r.Len())
	}
	if id := h.r.Invoke("c2", "read", kx); id != 2 {
		t.Fatalf("next Invoke = %d, want 2", id)
	}
	if ops := h.r.Ops(); ops[1].CallIndex != 2 {
		t.Fatalf("a panicking Invoke consumed an event index: %d", ops[1].CallIndex)
	}
}

// AT-HIS-05, HIS-005
func TestCompleteMisuse(t *testing.T) {
	h := newH()
	h.r.Invoke("c1", "write", map[string]any{"key": "x", "value": 1})
	h.r.Complete(1, history.OK, nil)
	h.r.Invoke("c2", "read", map[string]string{"key": "x"})
	h.T(1) // so that a Return set by a panicking Complete is not 0
	cases := []struct {
		call func()
		want string
	}{
		{func() { h.r.Complete(0, history.OK, nil) }, "history: Complete: unknown op id 0 (have 1..2)"},
		{func() { h.r.Complete(3, history.OK, nil) }, "history: Complete: unknown op id 3 (have 1..2)"},
		{func() { h.r.Complete(1, history.Fail, nil) }, "history: Complete: op 1 (c1 write) already completed with status ok"},
		{func() { h.r.Complete(2, history.Pending, nil) }, "history: Complete: op 2 (c2 read): invalid status pending"},
		{func() { h.r.Complete(2, history.Status(9), nil) }, "history: Complete: op 2 (c2 read): invalid status status(9)"},
		{func() { h.r.Complete(2, history.OK, func() {}) },
			"history: Complete: op 2 (c2 read): output is not JSON-encodable: json: unsupported type: func()"},
		// HIS-005 order: two or three checks fail and the first one wins. Together these rows cover
		// every pair of checks that can fail at once (an unknown id has no op to be completed already).
		{func() { h.r.Complete(0, history.Pending, func() {}) }, "history: Complete: unknown op id 0 (have 1..2)"},
		{func() { h.r.Complete(1, history.Pending, func() {}) },
			"history: Complete: op 1 (c1 write) already completed with status ok"},
		{func() { h.r.Complete(2, history.Pending, func() {}) }, "history: Complete: op 2 (c2 read): invalid status pending"},
	}
	before, recs := h.r.Ops(), len(h.s.Records())
	for _, c := range cases {
		mustPanic(t, c.want, c.call)
	}
	if !reflect.DeepEqual(h.r.Ops(), before) || len(h.s.Records()) != recs {
		t.Fatalf("a panicking Complete changed the recorder: %+v, %d records, want %d", h.r.Ops(), len(h.s.Records()), recs)
	}
	if op := h.r.Ops()[1]; op.Status != history.Pending || op.ReturnIndex != 0 {
		t.Fatalf("op 2 = %+v, want pending", op)
	}
	// op 2 is still pending, so c2 cannot invoke yet (HIS-005, HIS-012 item 5).
	mustPanic(t, `history: Invoke: process "c2" has pending op 2 (read)`, func() { h.r.Invoke("c2", "read", nil) })
	h.r.Complete(2, history.Info, "timeout")
	if op := h.r.Ops()[1]; op.ReturnIndex != 4 || string(op.Output.(json.RawMessage)) != `"timeout"` {
		t.Fatalf("op 2 = %+v", op)
	}
	h.r.Invoke("c2", "read", nil) // HIS-008: a process may invoke again after Info
	mustPanic(t, "history: Complete: unknown op id 1 (have none)", func() { newH().r.Complete(1, history.OK, nil) })
}

// AT-HIS-06
func TestSnapshot(t *testing.T) {
	h := newH()
	m := map[string]string{"key": "x"}
	h.r.Invoke("c1", "read", m)
	m["key"] = "y"
	out := []int{1}
	h.r.Complete(1, history.OK, out)
	out[0] = 9
	op := h.r.Ops()[0]
	if string(op.Input.(json.RawMessage)) != `{"key":"x"}` || string(op.Output.(json.RawMessage)) != `[1]` {
		t.Fatalf("Input %s Output %s", op.Input, op.Output)
	}
}

// AT-HIS-08, HIS §8
func TestTraceRecords(t *testing.T) {
	h := at01(t)
	inv := h.records("history.invoke")[0]
	if inv.Node != h.c1.ID() || inv.Inc != h.c1.Incarnation() || inv.Text != `c1 invoke write {"key":"x","value":1}` ||
		attrString(inv) != `id=1, process=c1, f=write, input={"key":"x","value":1}` {
		t.Fatalf("history.invoke %+v", inv)
	}
	cmp := h.records("history.complete")[0]
	if cmp.Node != h.c1.ID() || cmp.Text != "c1 ok write null" || attrString(cmp) != "id=1, process=c1, f=write, status=ok, output=null" {
		t.Fatalf("history.complete %+v", cmp)
	}
	h.r.Invoke("client-9", "read", nil)
	h.r.Complete(4, history.Fail, "no leader")
	for _, kind := range []string{"history.invoke", "history.complete"} {
		rs := h.records(kind)
		if last := rs[len(rs)-1]; last.Node != 0 || last.Inc != 0 {
			t.Fatalf("%s of an unknown process on node %d inc %d", kind, last.Node, last.Inc)
		}
	}
	if last := h.records("history.complete"); last[len(last)-1].Text != `client-9 fail read "no leader"` {
		t.Fatalf("Text %q", last[len(last)-1].Text)
	}
	// Inc is the node's incarnation when the record is emitted: op 3 completes after a restart.
	h.c1.Crash()
	h.c1.Restart()
	h.r.Complete(3, history.Info, nil)
	if rs := h.records("history.complete"); rs[len(rs)-1].Inc != h.c1.Incarnation() || h.c1.Incarnation() != 2 {
		t.Fatalf("history.complete after a restart has inc %d, node inc %d", rs[len(rs)-1].Inc, h.c1.Incarnation())
	}
	// HIS-002: json.Marshal escapes <, > and & in the stored bytes, which the records carry.
	h.r.Invoke("c2", "write", "<&>")
	h.r.Complete(5, history.OK, "<&>")
	if op := h.r.Ops()[4]; string(op.Input.(json.RawMessage)) != `"\u003c\u0026\u003e"` ||
		string(op.Output.(json.RawMessage)) != `"\u003c\u0026\u003e"` {
		t.Fatalf("Input %s Output %s", op.Input, op.Output)
	}
	// Cause is KRN's default: the record emitted just before (KRN-090).
	for _, kind := range []string{"history.invoke", "history.complete"} {
		for _, r := range h.records(kind) {
			if r.Cause != r.Seq-1 {
				t.Fatalf("%s seq %d has cause %d, want %d", kind, r.Seq, r.Cause, r.Seq-1)
			}
		}
	}
}

// AT-HIS-09, HIS-011
func TestRealTimeOrder(t *testing.T) {
	ops := at01(t).r.Ops()
	op1, op2, op3 := ops[0], ops[1], ops[2]
	if !op1.Precedes(op3) || op1.Precedes(op2) || !op2.Precedes(op3) || op1.Precedes(op1) {
		t.Fatalf("Precedes on AT-HIS-01")
	}
	for _, x := range ops {
		if op3.Precedes(x) {
			t.Fatalf("a pending op precedes op %d", x.ID)
		}
	}
	h := newH()
	h.r.Invoke("c1", "write", 1)
	h.r.Complete(1, history.Fail, nil)
	h.r.Invoke("c2", "write", 2)
	h.r.Complete(2, history.Info, nil)
	h.r.Invoke("c1", "read", nil)
	all := h.r.Ops()
	if all[0].Precedes(all[2]) || all[1].Precedes(all[2]) {
		t.Fatalf("Fail or Info ops precede a later op")
	}
	// HIS-011: op 1 completes OK in the instant op 2 is invoked, after op 2, so it does not precede op 2.
	h = newH()
	h.T(1)
	h.r.Invoke("c1", "write", 1)
	h.T(2)
	h.r.Invoke("c2", "read", nil)
	h.r.Complete(1, history.OK, nil)
	if a, b := h.r.Ops()[0], h.r.Ops()[1]; a.Return != b.Call || a.Precedes(b) {
		t.Fatalf("op 1 returned at %v (index %d), op 2 called at %v (index %d), Precedes %v",
			a.Return, a.ReturnIndex, b.Call, b.CallIndex, a.Precedes(b))
	}
}
