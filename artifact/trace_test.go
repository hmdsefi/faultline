// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
)

// AT-ART-02
func TestWriteTraceFixture(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTrace(&buf, fixtureTrace()); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != fixtureTraceText {
		t.Fatalf("WriteTrace mismatch:\n got: %q\nwant: %q", got, fixtureTraceText)
	}
}

func TestWriteTraceRecomputesHeader(t *testing.T) {
	tr := fixtureTrace()
	tr.Header.Records = 99
	tr.Header.Dropped = 7
	tr.Header.Version = 2 // recomputed from any value, not only from 0
	var buf bytes.Buffer
	if err := WriteTrace(&buf, tr); err != nil {
		t.Fatal(err)
	}
	if buf.String() != fixtureTraceText {
		t.Fatalf("header fields were not recomputed:\n%s", buf.String())
	}
}

// ART-030: every header field is written, also when empty; nil Nodes and Tags are written as [];
// node, inc, cause, text and attrs are omitted when zero, the other record fields never are;
// dropped is the first record's seq - 1; numbers are written and read back exactly.
func TestWriteTraceEdges(t *testing.T) {
	// §7: a nil pointer is bad input, not a panic.
	if err := WriteTrace(io.Discard, nil); err == nil || err.Error() != "artifact: trace is nil" {
		t.Fatalf("WriteTrace(nil) = %v, want artifact: trace is nil", err)
	}

	var buf bytes.Buffer
	if err := WriteTrace(&buf, &Trace{}); err != nil {
		t.Fatal(err)
	}
	want := `{"faultline_trace":1,"faultline_version":"","go_version":"","package":"","test":"","subtest":"","seed":"","trace_hash":"","records":0,"dropped":0,"nodes":[]}` + "\n"
	if buf.String() != want {
		t.Fatalf("empty trace:\n got: %q\nwant: %q", buf.String(), want)
	}

	tr := &Trace{
		Header: TraceHeader{Nodes: []Node{{ID: 1, Name: "n1"}, {}}},
		Records: []kernel.Record{
			{Seq: 5},
			{Seq: 9, At: 1<<53 + 1, Node: math.MaxInt32, Inc: math.MaxUint32, Kind: "k", Cause: math.MaxUint64, Text: "<a&b>"},
		},
	}
	buf.Reset()
	if err := WriteTrace(&buf, tr); err != nil {
		t.Fatal(err)
	}
	want = `{"faultline_trace":1,"faultline_version":"","go_version":"","package":"","test":"","subtest":"","seed":"","trace_hash":"","records":2,"dropped":4,"nodes":[{"id":1,"name":"n1","tags":[]},{"id":0,"name":"","tags":[]}]}` + "\n" +
		`{"seq":5,"at":0,"t":"0.000000000s","kind":""}` + "\n" +
		`{"seq":9,"at":9007199254740993,"t":"9007199.254740993s","node":2147483647,"inc":4294967295,"kind":"k","cause":18446744073709551615,"text":"<a&b>"}` + "\n"
	if buf.String() != want {
		t.Fatalf("edge trace:\n got: %q\nwant: %q", buf.String(), want)
	}
	got, err := ReadTrace(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Records, tr.Records) {
		t.Fatalf("edge records read back as %+v", got.Records)
	}

	// seq is never omitted, also when 0; a first seq of 0 gives dropped 0 (0 - 1 would wrap).
	if b, err := json.Marshal(FromRecord(kernel.Record{})); err != nil || string(b) != `{"seq":0,"at":0,"t":"0.000000000s","kind":""}` {
		t.Fatalf("zero record = %s, %v", b, err)
	}
	buf.Reset()
	if err := WriteTrace(&buf, &Trace{Records: []kernel.Record{{Kind: "k"}}}); err != nil {
		t.Fatal(err)
	}
	want = `{"faultline_trace":1,"faultline_version":"","go_version":"","package":"","test":"","subtest":"","seed":"","trace_hash":"","records":1,"dropped":0,"nodes":[]}` + "\n" +
		`{"seq":0,"at":0,"t":"0.000000000s","kind":"k"}` + "\n"
	if buf.String() != want {
		t.Fatalf("first seq 0:\n got: %q\nwant: %q", buf.String(), want)
	}

	// Invalid UTF-8: every string written has each run of invalid bytes replaced by one U+FFFD
	// before encoding, so Go 1.26 and 1.27 write the same bytes (encoding/json alone would write
	// one per byte, escaped or not by version). The input is left unchanged.
	badTrace := func() *Trace {
		return &Trace{
			Header: TraceHeader{
				FaultlineVersion: "v\xff\xfe", GoVersion: "go\xff\xfe", Package: "p\xff\xfe", Test: "T\xff\xfe",
				Subtest: "T\xff\xfe/s", Seed: "0x\xff\xfe", TraceHash: "0x\xfe\xff",
				Nodes: []Node{{ID: 1, Name: "n\xff\xfe", Tags: []string{"t\xff\xfe"}}},
			},
			Records: []kernel.Record{{Seq: 1, Kind: "k\xff\xfe", Text: "a\xff\xfeb", Attrs: attrs("k\xff\xfe", "v\xff\xfe")}},
		}
	}
	bad := badTrace()
	buf.Reset()
	if err := WriteTrace(&buf, bad); err != nil {
		t.Fatal(err)
	}
	want = strings.ReplaceAll(`{"faultline_trace":1,"faultline_version":"v~","go_version":"go~","package":"p~","test":"T~","subtest":"T~/s","seed":"0x~","trace_hash":"0x~","records":1,"dropped":0,"nodes":[{"id":1,"name":"n~","tags":["t~"]}]}`+"\n"+
		`{"seq":1,"at":0,"t":"0.000000000s","kind":"k~","text":"a~b","attrs":[["k~","v~"]]}`+"\n", "~", string(utf8.RuneError))
	if buf.String() != want {
		t.Fatalf("invalid UTF-8:\n got: %q\nwant: %q", buf.String(), want)
	}
	if !reflect.DeepEqual(bad, badTrace()) {
		t.Fatalf("WriteTrace changed its input: %+v", bad)
	}
}

// errWriter fails every write with err.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// ART-030: a failed write is returned, whether it happens at the final flush (the fixture), while
// the header is written (a 2 MiB header) or while the records are (30,000 records, more than the
// 1 MiB buffer).
func TestWriteTraceWriteError(t *testing.T) {
	errDisk := errors.New("disk full")
	bigHeader := &Trace{Header: TraceHeader{Test: strings.Repeat("x", 2<<20)}}
	manyRecords := &Trace{Records: make([]kernel.Record, 30_000)}
	for _, tr := range []*Trace{fixtureTrace(), bigHeader, manyRecords} {
		if err := WriteTrace(errWriter{errDisk}, tr); !errors.Is(err, errDisk) {
			t.Errorf("WriteTrace of %d records = %v, want %v", len(tr.Records), err, errDisk)
		}
	}
}

func TestRecordRoundTrip(t *testing.T) {
	for _, r := range fixtureRecords() {
		got := FromRecord(r).Record()
		if !reflect.DeepEqual(got, r) {
			t.Fatalf("round trip of seq %d: got %+v want %+v", r.Seq, got, r)
		}
	}
	if tr := FromRecord(kernel.Record{Seq: 1, At: 1500000000, Kind: "x"}); tr.T != "1.500000000s" || len(tr.Attrs) != 0 {
		t.Fatalf("FromRecord = %+v", tr)
	}
	// Record's Attrs is nil when there are none, also for an empty non-nil slice.
	if r := (TraceRecord{Seq: 1, Kind: "k", Attrs: [][2]string{}}).Record(); r.Attrs != nil {
		t.Fatalf("Record() of empty attrs = %#v, want nil", r.Attrs)
	}
}

// AT-ART-03
func TestReadTrace(t *testing.T) {
	tr, err := ReadTrace(strings.NewReader(fixtureTraceText))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tr.Records, fixtureRecords()) {
		t.Fatalf("records differ:\n got %+v\nwant %+v", tr.Records, fixtureRecords())
	}
	if !reflect.DeepEqual(tr.Header, fixtureTrace().Header) {
		t.Fatalf("header differs: %+v", tr.Header)
	}

	lines := strings.SplitAfter(fixtureTraceText, "\n")
	lines = lines[:len(lines)-1] // drop the empty element after the final newline
	join := func(ls []string) string { return strings.Join(ls, "") }
	with := func(i int, s string) string {
		c := append([]string(nil), lines...)
		c[i] = s
		return join(c)
	}
	swapped := append([]string(nil), lines...)
	swapped[4], swapped[5] = swapped[5], swapped[4] // seq 4 is line 5, seq 5 is line 6
	emptyMiddle := append(append(append([]string(nil), lines[:3]...), "\n"), lines[3:]...)
	header := func(oldnew ...string) string { return with(0, strings.NewReplacer(oldnew...).Replace(lines[0])) }

	cases := []struct {
		name, text, wantErr string
	}{
		{"newer version", strings.Replace(fixtureTraceText, `"faultline_trace":1`, `"faultline_trace":2`, 1),
			"artifact: trace.jsonl line 1: version 2 is newer than this faultline supports (1); upgrade faultline"},
		{"record count", strings.Replace(fixtureTraceText, `"records":12`, `"records":11`, 1),
			"artifact: trace.jsonl line 1: header says 11 records, found 12"},
		{"bad attrs", with(1, `{"seq":1,"at":0,"t":"0.000000000s","kind":"kernel.start","attrs":[["a"]]}`+"\n"),
			"artifact: trace.jsonl line 2: attrs element 0 is not a [key, value] pair of strings"},
		{"seq order", join(swapped),
			"artifact: trace.jsonl line 6: seq 4 is not greater than the previous seq 5"},
		{"empty line", join(emptyMiddle),
			"artifact: trace.jsonl line 4: empty line"},
		{"not a trace", `{"x":1}` + "\n", "artifact: trace.jsonl line 1: not a faultline trace"},
		{"version 0", `{"faultline_trace":0}` + "\n", "artifact: trace.jsonl line 1: not a faultline trace"},
		{"not an object", "[1]\n", "artifact: trace.jsonl line 1: not a faultline trace"},
		{"record not an object", with(1, "[1]\n"), "artifact: trace.jsonl line 2: not a JSON object"},
		// The edges of ART-031.
		{"empty input", "", "artifact: trace.jsonl line 1: not a faultline trace"},
		{"empty first line", "\n" + join(lines[1:]), "artifact: trace.jsonl line 1: not a faultline trace"},
		{"version not an integer", header(`"faultline_trace":1`, `"faultline_trace":1.0`), "artifact: trace.jsonl line 1: not a faultline trace"},
		// The count is compared after the last line, so the message gives the real total.
		{"fewer records in the header", header(`"records":12`, `"records":10`),
			"artifact: trace.jsonl line 1: header says 10 records, found 12"},
		{"more records in the header", header(`"records":12`, `"records":13`),
			"artifact: trace.jsonl line 1: header says 13 records, found 12"},
		{"equal seq", with(2, strings.Replace(lines[2], `"seq":2`, `"seq":1`, 1)),
			"artifact: trace.jsonl line 3: seq 1 is not greater than the previous seq 1"},
		{"first seq 0", with(1, strings.Replace(lines[1], `"seq":1`, `"seq":0`, 1)),
			"artifact: trace.jsonl line 2: seq 0 is not greater than the previous seq 0"},
		{"blank line", with(3, " \t\r\n"), "artifact: trace.jsonl line 4: empty line"},
		// Only space, tab, \r and \n are JSON whitespace; a line of other white space is not empty.
		{"form feed line", with(3, "\f\n"), "artifact: trace.jsonl line 4: not a JSON object"},
		{"vertical tab line", with(3, "\v\n"), "artifact: trace.jsonl line 4: not a JSON object"},
		{"next line (U+0085) line", with(3, "\u0085\n"), "artifact: trace.jsonl line 4: not a JSON object"},
		{"no-break space line", with(3, "\u00a0\n"), "artifact: trace.jsonl line 4: not a JSON object"},
		{"attr of three strings", with(1, `{"seq":1,"at":0,"t":"0.000000000s","kind":"k","attrs":[["a","b","c"]]}`+"\n"),
			"artifact: trace.jsonl line 2: attrs element 0 is not a [key, value] pair of strings"},
		{"attr value not a string", with(1, `{"seq":1,"at":0,"t":"0.000000000s","kind":"k","attrs":[["k","v"],["a",1]]}`+"\n"),
			"artifact: trace.jsonl line 2: attrs element 1 is not a [key, value] pair of strings"},
		{"attr of three strings at element 1", with(1, `{"seq":1,"at":0,"t":"0.000000000s","kind":"k","attrs":[["k","v"],["a","b","c"]]}`+"\n"),
			"artifact: trace.jsonl line 2: attrs element 1 is not a [key, value] pair of strings"},
		{"attr of three strings before a non-string", with(1, `{"seq":1,"at":0,"t":"0.000000000s","kind":"k","attrs":[["a","b","c"],["x",1]]}`+"\n"),
			"artifact: trace.jsonl line 2: attrs element 0 is not a [key, value] pair of strings"},
		{"attr of one string before a non-string", with(1, `{"seq":1,"at":0,"t":"0.000000000s","kind":"k","attrs":[["a"],["x",1]]}`+"\n"),
			"artifact: trace.jsonl line 2: attrs element 0 is not a [key, value] pair of strings"},
		// Two rules broken at once: the earlier check in ART-031's order wins.
		{"version before count", header(`"faultline_trace":1`, `"faultline_trace":2`, `"records":12`, `"records":11`),
			"artifact: trace.jsonl line 1: version 2 is newer than this faultline supports (1); upgrade faultline"},
		{"version before the header decode", header(`"faultline_trace":1`, `"faultline_trace":2`, `"dropped":0`, `"dropped":"0"`),
			"artifact: trace.jsonl line 1: version 2 is newer than this faultline supports (1); upgrade faultline"},
		{"attrs before seq", with(2, `{"seq":1,"at":0,"t":"0.000000000s","kind":"k","attrs":[["a"]]}`+"\n"),
			"artifact: trace.jsonl line 3: attrs element 0 is not a [key, value] pair of strings"},
		{"seq before count", strings.Replace(join(swapped), `"records":12`, `"records":11`, 1),
			"artifact: trace.jsonl line 6: seq 4 is not greater than the previous seq 5"},
	}
	for _, c := range cases {
		_, err := ReadTrace(strings.NewReader(c.text))
		if err == nil || err.Error() != c.wantErr {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}

	// A line that starts with { but does not decode reports encoding/json's error. Its text differs
	// between Go versions, so the expected error is computed here with the running encoding/json.
	jsonErr := func(line string, v any) string {
		err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), v)
		if err == nil {
			t.Fatalf("%q decodes", line)
		}
		return err.Error()
	}
	headerLine := strings.Replace(lines[0], `"dropped":0`, `"dropped":"0"`, 1)
	decodes := []struct {
		name, text, want string
	}{
		{"header field type", with(0, headerLine), "artifact: trace.jsonl line 1: " + jsonErr(headerLine, &TraceHeader{})},
	}
	for _, c := range []struct {
		name string
		n    int
		line string
	}{
		{"record field type", 3, `{"seq":"2","at":0,"t":"0.000000000s","kind":"k"}` + "\n"},
		{"attrs not an array", 3, `{"seq":2,"at":0,"t":"0.000000000s","kind":"k","attrs":"x"}` + "\n"},
		{"truncated record", 12, "{\n"},
		{"field type before attrs", 3, `{"seq":"2","at":0,"t":"0.000000000s","kind":"k","attrs":[["a"]]}` + "\n"},
	} {
		decodes = append(decodes, struct{ name, text, want string }{c.name, with(c.n-1, c.line),
			fmt.Sprintf("artifact: trace.jsonl line %d: not a JSON object: %s", c.n, jsonErr(c.line, &recordLine{}))})
	}
	for _, c := range decodes {
		_, err := ReadTrace(strings.NewReader(c.text))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}

	// A read error is returned as is.
	errRead := errors.New("read failed")
	for _, r := range []io.Reader{iotest.ErrReader(errRead), io.MultiReader(strings.NewReader(fixtureTraceText[:500]), iotest.ErrReader(errRead))} {
		if got, err := ReadTrace(r); got != nil || err != errRead {
			t.Errorf("read error: got %v, %v, want %v itself", got, err, errRead)
		}
	}

	noFinalNewline := strings.TrimSuffix(fixtureTraceText, "\n")
	tr2, err := ReadTrace(strings.NewReader(noFinalNewline))
	if err != nil {
		t.Fatalf("missing final newline: %v", err)
	}
	if len(tr2.Records) != 12 {
		t.Fatalf("missing final newline: %d records", len(tr2.Records))
	}

	// The header keeps the faultline_trace it checked: encoding/json would let a later key that
	// differs only in case overwrite it.
	tr5, err := ReadTrace(strings.NewReader(header(`"faultline_trace":1`, `"faultline_trace":1,"FAULTLINE_TRACE":5`)))
	if err != nil || !reflect.DeepEqual(tr5.Header, fixtureTrace().Header) {
		t.Fatalf("FAULTLINE_TRACE: %v, %+v", err, tr5)
	}

	// null reads as the zero value, also inside an attrs pair.
	tr6, err := ReadTrace(strings.NewReader(with(1, `{"seq":1,"at":0,"t":"0.000000000s","kind":"kernel.start","attrs":[["seed",null]]}`+"\n")))
	if err != nil || !reflect.DeepEqual(tr6.Records[0].Attrs, []kernel.Attr{{Key: "seed", Value: ""}}) {
		t.Fatalf("null attr value: %v, %+v", err, tr6)
	}

	// One decode target serves every line: nothing of one line reaches the next, also not an attrs
	// value that null leaves alone, and "attrs":[] reads as nil Attrs.
	reuse := `{"faultline_trace":1,"records":4}` + "\n" +
		`{"seq":1,"at":5,"node":1,"inc":2,"kind":"a","cause":7,"text":"t","attrs":[["seed","x"],["k","v"]]}` + "\n" +
		`{"seq":2,"at":0,"kind":"b","attrs":[["seed",null]]}` + "\n" +
		`{"seq":3,"at":0,"kind":"c"}` + "\n" +
		`{"seq":4,"at":0,"kind":"d","attrs":[]}` + "\n"
	tr7, err := ReadTrace(strings.NewReader(reuse))
	want7 := []kernel.Record{
		{Seq: 1, At: 5, Node: 1, Inc: 2, Kind: "a", Cause: 7, Text: "t", Attrs: attrs("seed", "x", "k", "v")},
		{Seq: 2, Kind: "b", Attrs: attrs("seed", "")},
		{Seq: 3, Kind: "c"},
		{Seq: 4, Kind: "d"},
	}
	if err != nil || !reflect.DeepEqual(tr7.Records, want7) {
		t.Fatalf("lines leak into each other: %v, %#v", err, tr7)
	}

	// A repeated attrs key leaves pairs past the final length in the reused array. They are
	// cleared too, so the next line's ["e", null] reads as e="".
	dup := `{"faultline_trace":1,"records":2}` + "\n" +
		`{"seq":1,"at":0,"kind":"a","attrs":[["a","x"],["b","y"]],"attrs":[["c","z"]]}` + "\n" +
		`{"seq":2,"at":0,"kind":"b","attrs":[["k","v"],["e",null]]}` + "\n"
	tr8, err := ReadTrace(strings.NewReader(dup))
	want8 := []kernel.Record{{Seq: 1, Kind: "a", Attrs: attrs("c", "z")}, {Seq: 2, Kind: "b", Attrs: attrs("k", "v", "e", "")}}
	if err != nil || !reflect.DeepEqual(tr8.Records, want8) {
		t.Fatalf("repeated attrs key: %v, %#v", err, tr8)
	}

	// A line leaves at most 16 pairs of 2 strings for the next one to clear, also after a wide
	// record or a repeated attrs key whose first pair is long.
	for _, first := range []string{
		`{"seq":1,"kind":"a","attrs":[` + strings.Repeat(`["k","v"],`, 16) + `["k","v"]]}`,
		`{"seq":1,"kind":"a","attrs":[["a","b","c","d","e"]],"attrs":[["k","v"]]}`,
	} {
		var l recordLine
		for _, line := range []string{first, `{"seq":2,"kind":"b","attrs":[["k","v"]]}`} {
			if _, err := readRecord([]byte(line), &l); err != nil {
				t.Fatalf("%s: %v", line, err)
			}
		}
		if cap(l.Attrs) > 16 || cap(l.Attrs[0]) > 2 {
			t.Errorf("after %s, the next line left room for %d pairs and %d strings in the first", first, cap(l.Attrs), cap(l.Attrs[0]))
		}
	}

	// Records grows toward the header's count: a true count ends in an exact fit, and a header that
	// claims far more than the file holds does not set the allocation.
	if tr, err := ReadTrace(strings.NewReader(fixtureTraceText)); err != nil || cap(tr.Records) != 12 {
		t.Fatalf("fixture: %v, capacity %d, want 12", err, cap(tr.Records))
	}
	for _, c := range []struct {
		capacity     int
		claimed      uint64
		wantCapacity int
	}{
		{0, 1 << 20, 1024}, // a header that claims 1M records before any is read: the usual first step
		{0, 12, 12},        // a true count below the first step
		{1024, 1500, 1500}, // a true count between the capacity and its double
		{1024, 5000, 2048}, // a count beyond the double: one doubling at a time
		{1024, 100, 2048},  // a count the file already exceeds: doubling
		{1024, 1024, 2048}, // the count reached: doubling
	} {
		got := growRecords(make([]kernel.Record, c.capacity), c.claimed)
		if cap(got) != c.wantCapacity || len(got) != c.capacity {
			t.Errorf("growRecords(cap %d, %d) = len %d cap %d, want cap %d", c.capacity, c.claimed, len(got), cap(got), c.wantCapacity)
		}
	}

	// Unknown fields and t are ignored, and JSON whitespace may surround a line's object.
	tolerant := append([]string(nil), lines...)
	tolerant[0] = "\t" + strings.Replace(lines[0], "{", `{"zzz":{"a":[1]},`, 1)
	tolerant[1] = " " + strings.Replace(lines[1], `"t":"0.000000000s"`, `"t":"bogus","zzz":[1]`, 1)
	tolerant[2] = strings.Replace(lines[2], "}\n", "} \r\n", 1)
	tr3, err := ReadTrace(strings.NewReader(join(tolerant)))
	if err != nil || !reflect.DeepEqual(tr3.Records, fixtureRecords()) || !reflect.DeepEqual(tr3.Header, fixtureTrace().Header) {
		t.Fatalf("unknown fields or whitespace: %v, %+v", err, tr3)
	}

	// No line-length limit: a record line of more than 100 KiB is read whole.
	long := fixtureTrace()
	long.Records[0].Text = strings.Repeat("x", 100<<10)
	var buf bytes.Buffer
	if err := WriteTrace(&buf, long); err != nil {
		t.Fatal(err)
	}
	tr4, err := ReadTrace(&buf)
	if err != nil || !reflect.DeepEqual(tr4.Records, long.Records) {
		t.Fatalf("long line: %v", err)
	}
}

func BenchmarkWriteTrace(b *testing.B) {
	tr := &Trace{Records: make([]kernel.Record, 1_000_000)}
	for i := range tr.Records {
		tr.Records[i] = kernel.Record{Seq: uint64(i + 1), At: kernel.Time(i * 1000), Node: 1, Inc: 1, Kind: "net.send", Cause: uint64(i), Text: "send #1 n1 -> n2", Attrs: attrs("msg", "1", "from", "n1", "to", "n2")}
	}
	b.ResetTimer()
	for b.Loop() {
		if err := WriteTrace(io.Discard, tr); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(tr.Records))*float64(b.N)/b.Elapsed().Seconds(), "records/s")
}

// readTraceInput returns the trace.jsonl bytes BenchmarkReadTrace reads and their record count.
// The source Trace is not returned, so it is garbage by the time the timed loop runs: kept alive,
// its 1M records would make every collection in the loop slower than in real use.
func readTraceInput(b *testing.B) ([]byte, int) {
	tr := &Trace{Records: make([]kernel.Record, 1_000_000)}
	for i := range tr.Records {
		tr.Records[i] = kernel.Record{Seq: uint64(i + 1), At: kernel.Time(i * 1000), Node: 1, Inc: 1, Kind: "net.send", Cause: uint64(i), Text: "send #1 n1 -> n2", Attrs: attrs("msg", "1", "from", "n1", "to", "n2")}
	}
	var buf bytes.Buffer
	if err := WriteTrace(&buf, tr); err != nil {
		b.Fatal(err)
	}
	return buf.Bytes(), len(tr.Records)
}

func BenchmarkReadTrace(b *testing.B) {
	data, n := readTraceInput(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := ReadTrace(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "records/s")
}
