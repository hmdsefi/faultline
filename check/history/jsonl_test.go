package history_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
)

// his021 is the HIS-021 example, byte for byte.
const his021 = `{"faultline_history":1,"ops":3}
{"id":1,"process":"c1","f":"write","status":"ok","call":1000000,"return":3000000,"call_index":1,"return_index":3,"input":{"key":"x","value":1},"output":null}
{"id":2,"process":"c2","f":"read","status":"ok","call":2000000,"return":3000000,"call_index":2,"return_index":4,"input":{"key":"x"},"output":1}
{"id":3,"process":"c1","f":"read","status":"pending","call":3000000,"return":0,"call_index":5,"return_index":0,"input":{"key":"x"},"output":null}
`

func jsonl(t *testing.T, r *history.Recorder) string {
	t.Helper()
	var b bytes.Buffer
	if err := r.WriteJSONL(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// AT-HIS-02
func TestExactFile(t *testing.T) {
	if got := jsonl(t, at01(t).r); got != his021 {
		t.Fatalf("WriteJSONL =\n%s\nwant\n%s", got, his021)
	}
	if got := jsonl(t, newH().r); got != "{\"faultline_history\":1,\"ops\":0}\n" {
		t.Fatalf("empty history = %q", got)
	}
	h := newH()
	h.r.Invoke("<c&>", "f>", "<&>")
	want := `{"faultline_history":1,"ops":1}
{"id":1,"process":"\u003cc\u0026\u003e","f":"f\u003e","status":"pending","call":0,"return":0,"call_index":1,"return_index":0,"input":"\u003c\u0026\u003e","output":null}
`
	if got := jsonl(t, h.r); got != want {
		t.Fatalf("WriteJSONL does not escape like json.Marshal:\n%s", got)
	}
	if ops, err := history.Read(strings.NewReader(want)); err != nil || !reflect.DeepEqual(ops, h.r.Ops()) {
		t.Fatalf("Read of escaped strings = %+v, %v", ops, err)
	}
}

// errFirst and errLater are the errors of errWriter.
var errFirst, errLater = errors.New("first"), errors.New("later")

// errWriter fails every write, with errFirst the first time.
type errWriter struct{ calls int }

func (w *errWriter) Write([]byte) (int, error) {
	if w.calls++; w.calls == 1 {
		return 0, errFirst
	}
	return 0, errLater
}

// countWriter counts the calls of Write.
type countWriter struct{ calls int }

func (w *countWriter) Write(p []byte) (int, error) {
	w.calls++
	return len(p), nil
}

// HIS-020, HIS §7: WriteJSONL writes through a bufio.Writer and returns the first error from w
// unchanged.
func TestWriteError(t *testing.T) {
	h := newH()
	h.r.Invoke("c1", "write", strings.Repeat("x", 10000))
	if err := h.r.WriteJSONL(&errWriter{}); err != errFirst {
		t.Fatalf("WriteJSONL = %v, want %v", err, errFirst)
	}
	w := &countWriter{}
	if err := at01(t).r.WriteJSONL(w); err != nil || w.calls != 1 {
		t.Fatalf("WriteJSONL of AT-HIS-01 made %d writes (%v), want 1 from a bufio.Writer", w.calls, err)
	}
}

// AT-HIS-03 (Read side; the re-encoding is in jsonl_internal_test.go)
func TestRoundTrip(t *testing.T) {
	h := at01(t)
	ops, err := history.Read(strings.NewReader(his021))
	if err != nil || !reflect.DeepEqual(ops, h.r.Ops()) {
		t.Fatalf("Read = %+v, %v", ops, err)
	}
	ops, err = history.Read(strings.NewReader("{\"faultline_history\":1,\"ops\":0}\n"))
	if err != nil || ops == nil || len(ops) != 0 {
		t.Fatalf("Read of the empty history = %#v, %v", ops, err)
	}
	crlf := strings.ReplaceAll(his021, "\n", "\r\n")
	if ops, err := history.Read(strings.NewReader(crlf)); err != nil || !reflect.DeepEqual(ops, h.r.Ops()) {
		t.Fatalf("CRLF file: %v", err)
	}
	noFinal := strings.TrimSuffix(his021, "\n")
	if ops, err := history.Read(strings.NewReader(noFinal)); err != nil || len(ops) != 3 {
		t.Fatalf("file without a final newline: %v", err)
	}
	spaced := strings.Replace(his021, `"input":{"key":"x","value":1}`, `"input": { "key" : "x", "value": 1 }`, 1)
	ops, err = history.Read(strings.NewReader(spaced))
	if err != nil {
		t.Fatalf("Read of spaced values: %v", err)
	}
	if in, ok := ops[0].Input.(json.RawMessage); !ok || string(in) != `{"key":"x","value":1}` {
		t.Fatalf("Read does not compact values: %#v", ops[0].Input)
	}
	mixed := `{"faultline_history":1,"ops":3}
{"id":1,"process":"c1","f":"cas","status":"fail","call":0,"return":1,"call_index":1,"return_index":2,"input":[1,2],"output": { "error" : "conflict" } }
{"id":2,"process":"c\u00e9","f":"write","status":"info","call":2,"return":3,"call_index":3,"return_index":5,"input":null,"output":"timeout"}
{"id":3,"process":"c1","f":"read","status":"ok","call":2,"return":3,"call_index":4,"return_index":6,"input":null,"output":null}
`
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	want := []history.Op{
		{ID: 1, Process: "c1", F: "cas", Input: raw(`[1,2]`), Output: raw(`{"error":"conflict"}`), Call: 0, Return: 1, Status: history.Fail, CallIndex: 1, ReturnIndex: 2},
		{ID: 2, Process: "c\u00e9", F: "write", Input: raw(`null`), Output: raw(`"timeout"`), Call: 2, Return: 3, Status: history.Info, CallIndex: 3, ReturnIndex: 5},
		{ID: 3, Process: "c1", F: "read", Input: raw(`null`), Output: raw(`null`), Call: 2, Return: 3, Status: history.OK, CallIndex: 4, ReturnIndex: 6},
	}
	if ops, err := history.Read(strings.NewReader(mixed)); err != nil || !reflect.DeepEqual(ops, want) {
		t.Fatalf("Read of fail and info ops = %+v, %v", ops, err)
	}
	// HIS-022 sets no line-length limit: a file much larger than bufio's 4,096-byte buffer, with a
	// line longer than 64 KiB.
	big := history.NewRecorder(kernel.New(kernel.Config{Seed: 1}))
	for i := 0; i < 300; i++ {
		big.Complete(big.Invoke(fmt.Sprintf("c%d", i%3), "write", map[string]int{"key": i}), history.OK, i)
	}
	big.Complete(big.Invoke("c1", "write", strings.Repeat("x", 70000)), history.OK, nil)
	big.Invoke("c2", "read", nil)
	if ops, err := history.Read(strings.NewReader(jsonl(t, big))); err != nil || !reflect.DeepEqual(ops, big.Ops()) {
		t.Fatalf("Read of a %d-op file with a 70,000-byte input: %v", big.Len(), err)
	}
}

// AT-HIS-07
func TestKeyOrder(t *testing.T) {
	h := newH()
	m1 := map[string]int{}
	m1["b"], m1["a"], m1["c"] = 1, 2, 3
	m2 := map[string]int{}
	m2["c"], m2["a"], m2["b"] = 3, 2, 1
	h.r.Invoke("c1", "write", m1)
	h.r.Invoke("c2", "write", m2)
	h.r.Complete(1, history.OK, map[int]string{10: "a", 2: "b"})
	ops := h.r.Ops()
	in1, ok := ops[0].Input.(json.RawMessage)
	in2, ok2 := ops[1].Input.(json.RawMessage)
	if !ok || !ok2 || string(in1) != `{"a":2,"b":1,"c":3}` || !bytes.Equal(in1, in2) {
		t.Fatalf("inputs %s %s", ops[0].Input, ops[1].Input)
	}
	if out, ok := ops[0].Output.(json.RawMessage); !ok || string(out) != `{"10":"a","2":"b"}` {
		t.Fatalf("output %s", ops[0].Output)
	}
	script := func() string {
		h := newH()
		for i := 0; i < 1000; i++ {
			h.T(i)
			id := h.r.Invoke(fmt.Sprintf("c%d", i%7), "write", map[string]int{"b": i, "a": 2 * i, "c": 3 * i})
			h.r.Complete(id, history.OK, map[int]string{i: "x", i + 10: "y"})
		}
		return jsonl(t, h.r)
	}
	if a, b := script(), script(); a != b {
		t.Fatalf("the same 1,000-op script wrote different bytes")
	}
}

// AT-HIS-10
func TestReadErrors(t *testing.T) {
	const h1 = `{"faultline_history":1,"ops":1}`
	const ok1 = `{"id":1,"process":"c1","f":"write","status":"ok","call":1000000,"return":3000000,"call_index":1,"return_index":3,"input":{"key":"x","value":1},"output":null}`
	// with returns H1 and OK1 with each pair old, new of pairs replaced once.
	with := func(pairs ...string) string {
		l := ok1
		for i := 0; i+1 < len(pairs); i += 2 {
			l = strings.Replace(l, pairs[i], pairs[i+1], 1)
		}
		return h1 + "\n" + l + "\n"
	}
	// file returns a header for n ops and then lines, each ending in \n.
	file := func(n int, lines ...string) string {
		return fmt.Sprintf(`{"faultline_history":1,"ops":%d}`, n) + "\n" + strings.Join(lines, "\n") + "\n"
	}
	// op returns an op line of process p with f "w", null input and output.
	op := func(id int, p, status string, call, ret, ci, ri int) string {
		return fmt.Sprintf(`{"id":%d,"process":%q,"f":"w","status":%q,"call":%d,"return":%d,"call_index":%d,"return_index":%d,"input":null,"output":null}`,
			id, p, status, call, ret, ci, ri)
	}
	cases := []struct{ in, want string }{
		{"", "history: missing header"},
		{`{"faultline_history":2,"ops":0}` + "\n", "history: line 1: unsupported version 2 (supported: 1)"},
		{`{"ops":0}` + "\n", `history: line 1: missing field "faultline_history"`},
		{`{"faultline_history":1,"ops":0,"x":1}` + "\n", `history: line 1: unknown field "x"`},
		{h1 + "\n", "history: header declares 1 ops, found 0"},
		{with(`"id":1`, `"id":2`), "history: line 2: id 2, want 1"},
		{with(`"status":"ok"`, `"status":"done"`), `history: line 2: unknown status "done"`},
		{with(`"status":"ok"`, `"status":"pending"`), "history: line 2: pending op must have return 0, return_index 0 and output null"},
		{with(`"return":3000000`, `"return":500000`), "history: line 2: return 500000 is before call 1000000"},
		{with(`"return_index":3`, `"return_index":1`), "history: line 2: return_index 1 must be greater than call_index 1"},
		{with(`"output":null}`, `"output":null,"extra":1}`), `history: line 2: unknown field "extra"`},
		{with(`,"output":null`, ``), `history: line 2: missing field "output"`},
		{with(`"process":"c1"`, `"process":null`), `history: line 2: field "process" must not be null`},
		{with(`"return_index":3`, `"return_index":3`), "history: op 1: index 3 out of range 1..2"},
		{h1 + "\n" + ok1 + "\n\n", "history: line 3: empty line"},
		// further HIS-022 rows
		{`{"faultline_history":1,"ops":-1}` + "\n", "history: line 1: ops must be >= 0 (got -1)"},
		{`{"faultline_history":null,"ops":0}` + "\n", `history: line 1: field "faultline_history" must not be null`},
		{with(`"process":"c1"`, `"process":""`), "history: line 2: process must not be empty"},
		{with(`"f":"write"`, `"f":""`), "history: line 2: f must not be empty"},
		{with(`"call":1000000`, `"call":-1`), "history: line 2: call must be >= 0 (got -1)"},
		{with(`"call_index":1`, `"call_index":0`), "history: line 2: call_index must be >= 1 (got 0)"},
		{with(`"id":1`, `"id":"1"`), `history: line 2: field "id": json: cannot unmarshal string into Go value of type int64`},
		{with(`"call":1000000`, `"call":9999999999999999999`), `history: line 2: field "call": json: cannot unmarshal number 9999999999999999999 into Go value of type int64`},
		{with(`"status":"ok"`, `"status":"pending"`, `"return":3000000`, `"return":0`), "history: line 2: pending op must have return 0, return_index 0 and output null"},
		{with(`"status":"ok"`, `"status":"pending"`, `"return":3000000`, `"return":0`, `"return_index":3`, `"return_index":0`, `"output":null`, `"output":1`),
			"history: line 2: pending op must have return 0, return_index 0 and output null"},
		{file(2, op(1, "c1", "ok", 5, 5, 1, 2), op(2, "c2", "ok", 4, 5, 3, 4)), "history: line 3: call 4 is before the previous op's call 5"},
		{file(2, op(1, "c1", "ok", 5, 5, 2, 3), op(2, "c2", "ok", 5, 5, 2, 4)), "history: line 3: call_index 2 is not greater than the previous op's call_index 2"},
		{file(2, op(1, "c1", "ok", 5, 5, 1, 2), op(2, "c2", "ok", 5, 5, 0, 4)), "history: line 3: call_index 0 is not greater than the previous op's call_index 1"},
		{`{"faultline_history":1,"ops":0}` + "\n" + ok1 + "\n", "history: header declares 0 ops, found 1"},
		{file(2, op(1, "c1", "ok", 0, 0, 1, 2), op(2, "c2", "pending", 0, 0, 4, 0)), "history: op 2: index 4 out of range 1..3"},
		{h1 + "\r\n" + ok1 + "\r\n\r\n", "history: line 3: empty line"},
		{`{"faultline_history":1,"ops":0}` + "\n\r", "history: line 2: empty line"},
		// check order: each input breaks two rules and gets the error of the earlier one
		{`{"x":1}` + "\n", `history: line 1: unknown field "x"`},
		{`{"z":1,"b":2,"faultline_history":1,"ops":0}` + "\n", `history: line 1: unknown field "b"`},
		{`{}` + "\n", `history: line 1: missing field "faultline_history"`},
		{`{"faultline_history":null}` + "\n", `history: line 1: missing field "ops"`},
		{`{"faultline_history":null,"ops":"x"}` + "\n", `history: line 1: field "faultline_history" must not be null`},
		{`{"faultline_history":"1","ops":null}` + "\n", `history: line 1: field "faultline_history": json: cannot unmarshal string into Go value of type int`},
		{`{"faultline_history":2,"ops":null}` + "\n", `history: line 1: field "ops" must not be null`},
		{`{"faultline_history":2,"ops":-1}` + "\n", "history: line 1: unsupported version 2 (supported: 1)"},
		{with(`"id":1,`, `"x":1,`), `history: line 2: unknown field "x"`},
		{with(`"output":null}`, `"output":null,"zz":1,"aa":2}`), `history: line 2: unknown field "aa"`},
		{with(`"id":1,`, ``, `"process":"c1",`, ``), `history: line 2: missing field "id"`},
		{with(`"id":1,`, ``, `"process":"c1"`, `"process":null`), `history: line 2: missing field "id"`},
		{with(`"f":"write"`, `"f":null`, `"call_index":1`, `"call_index":null`), `history: line 2: field "f" must not be null`},
		{with(`"id":1`, `"id":"1"`, `"call":1000000`, `"call":null`), `history: line 2: field "call" must not be null`},
		{with(`"process":"c1"`, `"process":1`, `"call":1000000`, `"call":"x"`), `history: line 2: field "process": json: cannot unmarshal number into Go value of type string`},
		{with(`"id":1`, `"id":2`, `"call":1000000`, `"call":"x"`), `history: line 2: field "call": json: cannot unmarshal string into Go value of type int64`},
		{with(`"id":1`, `"id":5`, `"process":"c1"`, `"process":""`), "history: line 2: id 5, want 1"},
		{with(`"process":"c1"`, `"process":""`, `"f":"write"`, `"f":""`), "history: line 2: process must not be empty"},
		{with(`"f":"write"`, `"f":""`, `"status":"ok"`, `"status":"x"`), "history: line 2: f must not be empty"},
		{with(`"status":"ok"`, `"status":"OK"`, `"call":1000000`, `"call":-5`), `history: line 2: unknown status "OK"`},
		{with(`"call":1000000`, `"call":-1`, `"call_index":1`, `"call_index":0`), "history: line 2: call must be >= 0 (got -1)"},
		{file(2, op(1, "c1", "ok", 5, 5, 1, 2), op(2, "c2", "ok", -1, 5, 3, 4)), "history: line 3: call must be >= 0 (got -1)"},
		{file(2, op(1, "c1", "ok", 5, 5, 2, 3), op(2, "c2", "ok", 4, 5, 1, 4)), "history: line 3: call 4 is before the previous op's call 5"},
		{with(`"call_index":1`, `"call_index":0`, `"status":"ok"`, `"status":"pending"`), "history: line 2: call_index must be >= 1 (got 0)"},
		{with(`"return":3000000`, `"return":1`, `"return_index":3`, `"return_index":1`), "history: line 2: return 1 is before call 1000000"},
		{`{"faultline_history":1,"ops":5}` + "\n" + strings.Replace(ok1, `"id":1`, `"id":3`, 1) + "\n", "history: line 2: id 3, want 1"},
		{file(2, op(1, "c1", "ok", 0, 0, 1, 9)), "history: header declares 2 ops, found 1"},
		{file(2, op(1, "c1", "ok", 0, 0, 1, 3), op(2, "c1", "ok", 0, 0, 2, 7)), "history: op 2: index 7 out of range 1..4"},
		{file(2, op(1, "c1", "ok", 0, 0, 1, 3), op(2, "c2", "ok", 0, 0, 3, 9)), "history: index 3 is used by op 1 and op 2"},
		{file(4, op(1, "b", "ok", 0, 0, 1, 5), op(2, "a", "ok", 0, 0, 2, 6), op(3, "b", "ok", 0, 0, 3, 7), op(4, "a", "ok", 0, 0, 4, 8)),
			`history: process "a": op 4 invoked before op 2 completed`},
		{file(4, op(1, "b", "ok", 0, 0, 1, 5), op(2, "aa", "ok", 0, 0, 2, 6), op(3, "b", "ok", 0, 0, 3, 7), op(4, "aa", "ok", 0, 0, 4, 8)),
			`history: process "aa": op 4 invoked before op 2 completed`},
		{file(4, op(1, "a", "ok", 0, 0, 1, 5), op(2, "B", "ok", 0, 0, 2, 6), op(3, "a", "ok", 0, 0, 3, 7), op(4, "B", "ok", 0, 0, 4, 8)),
			`history: process "B": op 4 invoked before op 2 completed`},
		{file(3, op(1, "c", "ok", 0, 0, 1, 2), op(2, "c", "pending", 0, 0, 3, 0), op(3, "c", "ok", 0, 0, 4, 5)),
			`history: process "c": op 3 invoked while op 2 is pending`},
	}
	for _, c := range cases {
		_, err := history.Read(strings.NewReader(c.in))
		if err == nil || err.Error() != c.want {
			t.Errorf("Read(%q)\n got %v\nwant %s", c.in, err, c.want)
		}
	}
	// encoding/json texts that differ between Go versions: the prefix and the wrapped error (HIS §7)
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	prefixed := []struct {
		in, prefix string
		target     any
	}{
		{h1 + "\n" + `{"id":` + "\n", "history: line 2: invalid JSON: ", &syntax},
		{h1 + "\n\r\r\n", "history: line 2: invalid JSON: ", &syntax},
		{"{\n", "history: line 1: invalid JSON: ", &syntax},
		{"[1]\n", "history: line 1: invalid JSON: ", &typ},
		{h1 + "\n[1]\n", "history: line 2: invalid JSON: ", &typ},
		{with(`"input":{"key":"x","value":1}`, `"input":{"key":}`), "history: line 2: invalid JSON: ", &syntax},
		{`{"faultline_history":1,"ops":1.5}` + "\n", `history: line 1: field "ops": `, &typ},
		{with(`"id":1`, `"id":"1"`), `history: line 2: field "id": `, &typ},
	}
	for _, c := range prefixed {
		_, err := history.Read(strings.NewReader(c.in))
		if err == nil || !strings.HasPrefix(err.Error(), c.prefix) || !errors.As(err, c.target) {
			t.Errorf("Read(%q) = %v, want %s and a wrapped %T", c.in, err, c.prefix, c.target)
		}
	}
	boom := errors.New("boom")
	for _, data := range []string{"", h1 + "\n" + ok1[:20]} {
		_, err := history.Read(&errReader{data: []byte(data), err: boom})
		if err == nil || err.Error() != "history: read: boom" || !errors.Is(err, boom) {
			t.Errorf("read error after %q: %v", data, err)
		}
	}
	two := `{"faultline_history":1,"ops":2}
{"id":1,"process":"c1","f":"w","status":"ok","call":0,"return":0,"call_index":1,"return_index":3,"input":null,"output":null}
{"id":2,"process":"c1","f":"w","status":"ok","call":0,"return":0,"call_index":2,"return_index":4,"input":null,"output":null}
`
	if _, err := history.Read(strings.NewReader(two)); err == nil || err.Error() != `history: process "c1": op 2 invoked before op 1 completed` {
		t.Errorf("overlapping ops: %v", err)
	}
	pendingFirst := strings.Replace(two, `"status":"ok","call":0,"return":0,"call_index":1,"return_index":3`,
		`"status":"pending","call":0,"return":0,"call_index":1,"return_index":0`, 1)
	pendingFirst = strings.Replace(pendingFirst, `"return_index":4`, `"return_index":3`, 1)
	if _, err := history.Read(strings.NewReader(pendingFirst)); err == nil || err.Error() != `history: process "c1": op 2 invoked while op 1 is pending` {
		t.Errorf("invoke while pending: %v", err)
	}
	dup := strings.Replace(two, `"return_index":4`, `"return_index":3`, 1)
	dup = strings.Replace(dup, `"process":"c1","f":"w","status":"ok","call":0,"return":0,"call_index":2`, `"process":"c2","f":"w","status":"ok","call":0,"return":0,"call_index":2`, 1)
	if _, err := history.Read(strings.NewReader(dup)); err == nil || err.Error() != "history: index 3 is used by op 1 and op 2" {
		t.Errorf("duplicate index: %v", err)
	}
}

// errReader returns data, then err.
type errReader struct {
	data []byte
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// HIS-022 with 10,000 processes of two ops each, invoked in turn, so the two ops of a process are
// 10,000 IDs apart and process names descend in ID order. In the valid file each op completes
// before the next is invoked. In the faulty file every op is invoked before any completes, so every
// process overlaps, and the error must name the smallest process at its first pair by ID.
func TestManyProcesses(t *testing.T) {
	const n = 10000
	r := history.NewRecorder(kernel.New(kernel.Config{Seed: 1}))
	for i := 0; i < 2*n; i++ {
		r.Complete(r.Invoke(fmt.Sprintf("p%05d", n-1-i%n), "w", nil), history.OK, nil)
	}
	if ops, err := history.Read(strings.NewReader(jsonl(t, r))); err != nil || !reflect.DeepEqual(ops, r.Ops()) {
		t.Fatalf("Read of %d processes: %v", n, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "{\"faultline_history\":1,\"ops\":%d}\n", 2*n)
	for id := 1; id <= 2*n; id++ {
		fmt.Fprintf(&b, `{"id":%d,"process":"p%05d","f":"w","status":"ok","call":0,"return":0,"call_index":%d,"return_index":%d,"input":null,"output":null}`+"\n",
			id, n-1-(id-1)%n, id, 2*n+id)
	}
	want := fmt.Sprintf(`history: process "p00000": op %d invoked before op %d completed`, 2*n, n)
	if _, err := history.Read(strings.NewReader(b.String())); err == nil || err.Error() != want {
		t.Fatalf("Read = %v, want %s", err, want)
	}
}

// clientToy is the AT-HIS-13 toy: clients c1 and c2 invoke on Node.After timers with delays
// from Node.Rand and complete in later callbacks; it runs for 10 s of virtual time.
func clientToy(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, *history.Recorder, kernel.StopReason) {
	s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
	r := history.NewRecorder(s)
	for _, name := range []string{"c1", "c2"} {
		s.AddNode(name, func(n *kernel.Node) {
			var invoke func()
			invoke = func() {
				id := r.Invoke(n.Name(), "write", map[string]int{"value": n.Rand().IntN(100)})
				n.After(kernel.Uniform(n.Rand(), time.Millisecond, 20*time.Millisecond), "complete", func() {
					r.Complete(id, history.OK, nil)
					n.After(kernel.Uniform(n.Rand(), time.Millisecond, 50*time.Millisecond), "invoke", invoke)
				})
			}
			n.After(kernel.Uniform(n.Rand(), time.Millisecond, 50*time.Millisecond), "invoke", invoke)
		})
	}
	return s, r, s.RunUntil(kernel.Time(10 * time.Second))
}
