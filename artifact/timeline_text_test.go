package artifact

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

// timelineTextCap5 is the timeline.txt example of ART §5.6, byte for byte.
const timelineTextCap5 = `faultline timeline v1
test:     TestToy/seed=0x0000000000000001
package:  example.com/toy
seed:     0x0000000000000001
status:   fail
failure:  invariant "no pong" violated at t=0.003000000s on n2 (event 4)
records:  12 (dropped 0), trace hash 0x00000000000000aa
nodes:    1=n1 [server], 2=n2 [server]
slice:    5 records, root 12, truncated at cap 5
legend:   ! failure record, * causal slice, <- cause

  SEQ         TIME NODE KIND            TEXT
    1 0.000000000s -    kernel.start    start seed=0x0000000000000001 tie_break=seeded
    2 0.000000000s n1#0 kernel.add_node n1 tags=server offset_ns=0 drift_ppm=0 <-1
    3 0.000000000s n2#0 kernel.add_node n2 tags=server offset_ns=0 drift_ppm=0 <-2
    4 0.000000000s n1#0 kernel.event    boot id=1 <-2
    5 0.000000000s n1#1 kernel.boot     boot <-4
    6 0.000000000s n2#0 kernel.event    boot id=2 <-3
*   7 0.000000000s n2#1 kernel.boot     boot <-6
    8 0.001000000s n1#1 kernel.event    tick id=3 <-5
*   9 0.001000000s n1#1 net.send        send #1 n1 -> n2: "ping" msg=1 from=n1 to=n2 payload="\"ping\"" <-8
*  10 0.003000000s n2#1 kernel.event    net.deliver id=4 <-9
*  11 0.003000000s n2#1 net.deliver     deliver #1.1 n1 -> n2 msg=1 copy=1 from=n1 to=n2 latency_ns=2000000 <-10
!  12 0.003000000s -    check.violation invariant "no pong" violated kind=invariant check="no pong" error="got ping" event=4 <-11
`

// AT-ART-09
func TestWriteTimelineTextFixture(t *testing.T) {
	rep := fixtureReport()
	tr := fixtureTrace()
	var buf bytes.Buffer
	if err := WriteTimelineText(&buf, &rep, tr, CausalSlice(tr.Records, 12, 5)); err != nil {
		t.Fatal(err)
	}
	if buf.String() != timelineTextCap5 {
		t.Fatalf("timeline.txt mismatch:\n%s", buf.String())
	}
}

func TestWriteTimelineTextDroppedAndEscapes(t *testing.T) {
	rep := fixtureReport()
	tr := fixtureTrace()
	tr.Header.Dropped = 3
	tr.Records[0].Text = "a\tb\\c\nd\re"
	tr.Records[0].Attrs = attrs("empty", "")
	var buf bytes.Buffer
	if err := WriteTimelineText(&buf, &rep, tr, Slice{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"records:  12 (dropped 3), trace hash 0x00000000000000aa\n",
		"slice:    none\n",
		"  SEQ         TIME NODE KIND            TEXT\n  ... 3 earlier records were not retained\n",
		`    1 0.000000000s -    kernel.start    a\tb\\c\nd\re empty=""` + "\n",
	} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

// ART-040 line by line: the column widths (minimums, and the longest value in bytes, so a shorter
// multi-byte value is padded by bytes), the report's header fields (not the trace header's), a
// pass report, nodes without tags, a node missing from the table, a slice that is not truncated
// and one written as given, the dropped line, a long text written whole, bare and quoted attr
// values, trailing spaces trimmed, and every string of the report and the trace escaped, control
// characters included.
func TestWriteTimelineTextLayout(t *testing.T) {
	small := `faultline timeline v1
test:     S
package:  p
seed:     0x01
status:   pass
failure:  none
records:  1 (dropped 0), trace hash h
nodes:    none
slice:    none
legend:   ! failure record, * causal slice, <- cause

  SEQ         TIME NODE KIND TEXT
    1 0.000000000s -    k    t
`
	one := `faultline timeline v1
test:
package:  p
seed:     0x01
status:   pass
failure:  none
records:  1 (dropped 0), trace hash h
nodes:    none
slice:    1 records, root 1, truncated at cap 7
legend:   ! failure record, * causal slice, <- cause

  SEQ         TIME NODE KIND TEXT
!   1 0.000000000s -    k
`
	wide := `faultline timeline v1
test:     TestWide/seed=0x00000000000003e8
package:  example.com/wide
seed:     0x00000000000003e8
status:   fail
failure:  invariant "x" violated
records:  3 (dropped 997), trace hash 0x0000000000000bad
nodes:    1=n1 [], 2=dé [a,b]
slice:    3 records, root 1000
legend:   ! failure record, * causal slice, <- cause

   SEQ          TIME NODE     KIND            TEXT
  ... 997 earlier records were not retained
*  998  0.500000000s node7#12 x               ` + strings.Repeat("é", 45) + `
*  999 12.500000000s dé#1    net.send        send bare=a.b_c:d/e@f%g+h,i#j|k-l sp="a b" empty="" <-998
! 1000 12.500000000s -        check.violation boom <-999
`
	escaped := `faultline timeline v1
test:     T\tx
package:  p\\q
seed:     s\tz
status:   x\ty
failure:  a\nb
records:  1 (dropped 1), trace hash h\r
nodes:    1=n\n1 [t\t1]
slice:    none
legend:   ! failure record, * causal slice, <- cause

  SEQ         TIME NODE   KIND   TEXT
  ... 1 earlier records were not retained
    1 0.000000000s n\n1#1 k\n\nx t\r a\tb="v\nw"
`
	// Every rune that strconv.IsPrint rejects is escaped like strconv.QuoteRune: NUL, ESC, DEL,
	// U+009B (CSI), U+2028, U+202E (a bidi override), U+00A0 and U+E0001, also as a string's
	// first rune.
	controls := `faultline timeline v1
test:     \x00T\x1b[2J
package:  p
seed:     s
status:   pass
failure:  none
records:  1 (dropped 0), trace hash h\u2028
nodes:    none
slice:    none
legend:   ! failure record, * causal slice, <- cause

  SEQ         TIME NODE KIND        TEXT
    1 0.000000000s -    k\x7f\u009b \u202eevil\u00a0x\x00\U000e0001 \x1bk\u2028=v
`
	header := TraceHeader{Subtest: "header", Package: "header", Seed: "header", TraceHash: "header", Records: 99}
	wideRecs := []kernel.Record{
		{Seq: 998, At: 500_000_000, Node: 7, Inc: 12, Kind: "x", Text: strings.Repeat("é", 45)},
		{Seq: 999, At: 12_500_000_000, Node: 2, Inc: 1, Kind: "net.send", Cause: 998, Text: "send", Attrs: attrs("bare", "a.b_c:d/e@f%g+h,i#j|k-l", "sp", "a b", "empty", "")},
		{Seq: 1000, At: 12_500_000_000, Kind: "check.violation", Cause: 999, Text: "boom"},
	}
	wideHeader := header
	wideHeader.Dropped = 997
	wideHeader.Nodes = []Node{{ID: 1, Name: "n1"}, {ID: 2, Name: "dé", Tags: []string{"a", "b"}}}
	escHeader := header
	escHeader.Dropped = 1
	escHeader.Nodes = []Node{{ID: 1, Name: "n\n1", Tags: []string{"t\t1"}}}
	cases := []struct {
		name string
		rep  Report
		tr   *Trace
		s    Slice
		want string
	}{
		{"small", Report{Status: "pass", Subtest: "S", Package: "p", Seed: "0x01", Run: RunInfo{TraceHash: "h"}},
			&Trace{Header: header, Records: []kernel.Record{{Seq: 1, Kind: "k", Text: "t"}}}, Slice{}, small},
		// The slice is written as given: its length, root and cap. Trailing spaces are trimmed.
		{"one", Report{Status: "pass", Package: "p", Seed: "0x01", Run: RunInfo{TraceHash: "h"}},
			&Trace{Header: header, Records: []kernel.Record{{Seq: 1, Kind: "k"}}}, Slice{Root: 1, Seqs: []uint64{1}, Cap: 7, Truncated: true}, one},
		{"wide", Report{Status: "fail", Subtest: "TestWide/seed=0x00000000000003e8", Package: "example.com/wide", Seed: "0x00000000000003e8",
			Failure: &Failure{Headline: `invariant "x" violated`, RecordSeq: 1000}, Run: RunInfo{TraceHash: "0x0000000000000bad"}},
			&Trace{Header: wideHeader, Records: wideRecs}, CausalSlice(wideRecs, 1000, 200), wide},
		// "k\n\nx" is 4 bytes but 6 once escaped: the KIND width is measured after escaping.
		{"escaped", Report{Status: "x\ty", Subtest: "T\tx", Package: `p\q`, Seed: "s\tz", Failure: &Failure{Headline: "a\nb"}, Run: RunInfo{TraceHash: "h\r"}},
			&Trace{Header: escHeader, Records: []kernel.Record{{Seq: 1, Node: 1, Inc: 1, Kind: "k\n\nx", Text: "t\r", Attrs: attrs("a\tb", "v\nw")}}}, Slice{}, escaped},
		{"controls", Report{Status: "pass", Subtest: "\x00T\x1b[2J", Package: "p", Seed: "s", Run: RunInfo{TraceHash: "h\u2028"}},
			&Trace{Header: header, Records: []kernel.Record{{Seq: 1, Kind: "k\x7f\u009b", Text: "\u202eevil\u00a0x\x00\U000e0001", Attrs: attrs("\x1bk\u2028", "v")}}}, Slice{}, controls},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := WriteTimelineText(&buf, &c.rep, c.tr, c.s); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if buf.String() != c.want {
			t.Errorf("%s: timeline.txt =\n%s\nwant\n%s", c.name, buf.String(), c.want)
		}
	}
}

// ART-040: an attr value is bare exactly when every byte is in [A-Za-z0-9._:/@%+,#|-].
func TestWriteTimelineTextAttrValues(t *testing.T) {
	const bare = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._:/@%+,#|-"
	rep := fixtureReport()
	for c := 0; c < 128; c++ {
		v := string([]byte{'x', byte(c), 'x'})
		want := strconv.Quote(v)
		if strings.IndexByte(bare, byte(c)) >= 0 {
			want = v
		}
		var buf bytes.Buffer
		tr := &Trace{Records: []kernel.Record{{Seq: 1, Kind: "k", Attrs: attrs("a", v)}}}
		if err := WriteTimelineText(&buf, &rep, tr, Slice{}); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(buf.String(), " a="+want+"\n") {
			t.Errorf("attr value %q: last line %q, want it to end in a=%s", v, buf.String()[strings.LastIndexByte(buf.String()[:buf.Len()-1], '\n')+1:], want)
		}
	}
}

// ART-040: the nodes line writes each node's ID, not its position, the NODE column takes the
// first name of an ID the table repeats, and an attr value is quoted with strconv.Quote, which
// escapes U+00A0 and keeps é.
func TestWriteTimelineTextNodeIDsAndQuotes(t *testing.T) {
	rep := Report{Status: "pass", Subtest: "T", Package: "p", Seed: "0x1", Run: RunInfo{TraceHash: "h"}}
	tr := &Trace{
		Header:  TraceHeader{Nodes: []Node{{ID: 2, Name: "a"}, {ID: 5, Name: "b"}, {ID: 5, Name: "c"}}},
		Records: []kernel.Record{{Seq: 1, Node: 5, Inc: 1, Kind: "k", Attrs: attrs("nb", "a\u00a0b", "e", "é")}},
	}
	var buf bytes.Buffer
	if err := WriteTimelineText(&buf, &rep, tr, Slice{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nodes:    2=a [], 5=b [], 5=c []\n", `    1 0.000000000s b#1  k     nb="a\u00a0b" e="é"` + "\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in\n%s", want, buf.String())
		}
	}
}

// ART-040: every string is made valid UTF-8 like trace.jsonl and report.json (each run of
// invalid bytes becomes one U+FFFD) before it is escaped, and widths are measured after that.
// So the text written from the values Write holds equals the text that Render writes from the
// files (AT-ART-14).
func TestWriteTimelineTextInvalidUTF8(t *testing.T) {
	const want = `faultline timeline v1
test:     T�
package:  p�
seed:     s�
status:   fail�
failure:  h�
records:  1 (dropped 0), trace hash 0x�
nodes:    1=n� [t�]
slice:    none
legend:   ! failure record, * causal slice, <- cause

  SEQ         TIME NODE   KIND    TEXT
    1 0.000000000s n�#1 kind� a�b\t� k�="v�"
`
	rep := fixtureReport()
	rep.Subtest, rep.Package, rep.Seed = "T\xff\xfe", "p\xff\xfe", "s\xff\xfe"
	rep.Failure.Headline, rep.Run.TraceHash = "h\xff\xfe", "0x\xff\xfe"
	rep.Status = "fail\xff\xfe"
	tr := &Trace{
		Header:  TraceHeader{Nodes: []Node{{ID: 1, Name: "n\xff\xfe", Tags: []string{"t\xff\xfe"}}}},
		Records: []kernel.Record{{Seq: 1, Node: 1, Inc: 1, Kind: "kind\xff\xfe", Text: "a\xff\xfeb\t\xff\xfe", Attrs: attrs("k\xff\xfe", "v\xff\xfe")}},
	}
	var buf bytes.Buffer
	if err := WriteTimelineText(&buf, &rep, tr, Slice{}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != want {
		t.Fatalf("timeline.txt =\n%s\nwant\n%s", buf.String(), want)
	}
	var tb, rb bytes.Buffer
	if err := WriteTrace(&tb, tr); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(&rb, &rep); err != nil {
		t.Fatal(err)
	}
	tr2, err := ReadTrace(&tb)
	if err != nil {
		t.Fatal(err)
	}
	rep2, err := ReadReport(&rb)
	if err != nil {
		t.Fatal(err)
	}
	var buf2 bytes.Buffer
	if err := WriteTimelineText(&buf2, rep2, tr2, Slice{}); err != nil {
		t.Fatal(err)
	}
	if buf2.String() != want {
		t.Fatalf("timeline.txt after a round trip =\n%s\nwant\n%s", buf2.String(), want)
	}
}

// §7: nil pointers are reported (the report first) and nothing is written; a write error of w is
// returned.
func TestWriteTimelineTextErrors(t *testing.T) {
	rep := fixtureReport()
	tr := fixtureTrace()
	for _, c := range []struct {
		rep  *Report
		tr   *Trace
		want string
	}{
		{nil, tr, "artifact: report is nil"},
		{&rep, nil, "artifact: trace is nil"},
		{nil, nil, "artifact: report is nil"},
	} {
		var buf bytes.Buffer
		if err := WriteTimelineText(&buf, c.rep, c.tr, Slice{}); err == nil || err.Error() != c.want || buf.Len() != 0 {
			t.Errorf("WriteTimelineText(%v, %v) = %v, wrote %d bytes; want %s", c.rep != nil, c.tr != nil, err, buf.Len(), c.want)
		}
	}
	errDisk := errors.New("disk full")
	if err := WriteTimelineText(errWriter{errDisk}, &rep, tr, Slice{}); !errors.Is(err, errDisk) {
		t.Fatalf("WriteTimelineText to a failing writer = %v, want %v", err, errDisk)
	}
}
