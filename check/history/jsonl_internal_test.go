package history

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

// AT-HIS-03: writing the ops returned by Read with the HIS-020 encoding reproduces the file.
func TestReadThenWrite(t *testing.T) {
	in := `{"faultline_history":1,"ops":3}
{"id":1,"process":"c1","f":"write","status":"ok","call":1000000,"return":3000000,"call_index":1,"return_index":3,"input":{"key":"x","value":1},"output":null}
{"id":2,"process":"c2","f":"read","status":"ok","call":2000000,"return":3000000,"call_index":2,"return_index":4,"input":{"key":"x"},"output":1}
{"id":3,"process":"c1","f":"read","status":"pending","call":3000000,"return":0,"call_index":5,"return_index":0,"input":{"key":"x"},"output":null}
`
	ops, err := Read(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := writeOps(&b, ops); err != nil || b.String() != in {
		t.Fatalf("re-encoded file differs: %v\n%s", err, b.String())
	}
	// HIS-022: the same holds for every file WriteJSONL writes, here with every status, escaped
	// strings, a float and an integer-keyed map.
	r := NewRecorder(kernel.New(kernel.Config{Seed: 1}))
	r.Invoke("<c&>", "f<", map[string]any{"z": []int{1, 2}, "a": " <\u2028"})
	r.Invoke("c1", "w", nil)
	r.Complete(2, Fail, "err")
	r.Complete(1, Info, map[int]string{10: "a", 2: "b"})
	r.Invoke("c1", "w", 1.5)
	r.Invoke("c9", "r", nil)
	r.Complete(4, OK, []any{nil, true})
	var w bytes.Buffer
	if err := r.WriteJSONL(&w); err != nil {
		t.Fatal(err)
	}
	ops, err = Read(bytes.NewReader(w.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := writeOps(&b, ops); err != nil || b.String() != w.String() {
		t.Fatalf("re-encoded file differs: %v\n%s\nwant\n%s", err, b.String(), w.String())
	}
}

// writtenLines returns the op lines that writeOps writes for ops of every status, with escapes,
// non-ASCII text, 18-digit and negative integers, and nested values. The first three are in the
// layout that decodeCanonical takes; the fourth has an escaped process name.
func writtenLines(tb testing.TB) []string {
	tb.Helper()
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	var b bytes.Buffer
	err := writeOps(&b, []Op{
		{ID: 1, Process: "c1", F: "write", Input: raw(`{"key":"x","value":1}`), Output: raw(`null`), Call: 1000000, Return: 3000000, Status: OK, CallIndex: 1, ReturnIndex: 3},
		{ID: 22, Process: "é ü", F: "cas", Input: raw(`[1,"\"",-2.5e3,true,{"":null},"\"","a \"b\" \\ c"]`), Output: raw(`{"a":[{}],"b":-0}`), Call: 999999999999999999, Return: -5, Status: Fail, CallIndex: 12},
		{ID: 3, Process: "p", F: "r", Input: raw(`"x"`), Output: raw(`false`), Status: Info, ReturnIndex: -7},
		{ID: 4, Process: "<c>", F: "f", Input: raw(`0`), Output: raw(`"\u00e9"`), Status: Pending},
	})
	if err != nil {
		tb.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")[1:]
}

// Read decodes an op line with decodeCanonical when it can and with decodeLine otherwise:
// decodeCanonical must take the lines that WriteJSONL writes, and must return what decodeLine
// returns for every line it takes. The lines below are written lines and every line one byte away
// from them.
func TestDecodeCanonical(t *testing.T) {
	lines := writtenLines(t)
	for _, l := range lines[:3] {
		if _, ok := decodeCanonical([]byte(l)); !ok {
			t.Errorf("decodeCanonical does not take the written line %s", l)
		}
	}
	taken := 0
	for _, l := range lines {
		var near []string
		for i := 0; i <= len(l); i++ {
			if i < len(l) {
				near = append(near, l[:i]+l[i+1:], l[:i]+l[i:i+1]+l[i:])
			}
			for _, c := range []string{" ", "\t", "\r", "\n", `"`, `\`, ",", "}", ".", "e", "0", "9", "-", "\xff"} {
				near = append(near, l[:i]+c+l[i:])
				if i < len(l) {
					near = append(near, l[:i]+c+l[i+1:])
				}
			}
		}
		for _, m := range near {
			fast, ok := decodeCanonical([]byte(m))
			if !ok {
				continue
			}
			taken++
			if strict, err := decodeLine([]byte(m)); err != nil || !reflect.DeepEqual(fast, strict) {
				t.Errorf("%s\ndecodeCanonical = %+v\n    decodeLine = %+v, %v", m, fast, strict, err)
			}
		}
	}
	if taken < 100 {
		t.Errorf("decodeCanonical took only %d lines", taken)
	}
	l, _ := decodeCanonical([]byte(lines[0]))
	_ = append(l.Input, `,"output":true`...)
	if string(l.Output) != "null" {
		t.Errorf("appending to Input changed Output to %s", l.Output)
	}
}

// FuzzDecodeCanonical: for any bytes that decodeCanonical takes, decodeLine succeeds and returns the
// same fields. go test runs the seeds, the lines that writeOps writes.
func FuzzDecodeCanonical(f *testing.F) {
	for _, l := range writtenLines(f) {
		f.Add([]byte(l))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fast, ok := decodeCanonical(b)
		if !ok {
			return
		}
		if strict, err := decodeLine(b); err != nil || !reflect.DeepEqual(fast, strict) {
			t.Fatalf("%q\ndecodeCanonical = %+v\n    decodeLine = %+v, %v", b, fast, strict, err)
		}
	})
}
