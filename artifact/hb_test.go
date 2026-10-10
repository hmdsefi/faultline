// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
)

// hbFixtureCap5 is the hb.mmd example of ART §5.8, byte for byte.
const hbFixtureCap5 = `flowchart TD
    %% faultline causal slice v1: root=12 records=5 cap=5 truncated=true
    n0["7 0.000000000s n2#35;1<br>kernel.boot: boot"]
    n1["9 0.001000000s n1#35;1<br>net.send: send #35;1 n1 -#gt; n2: #quot;ping#quot;"]
    n2["10 0.003000000s n2#35;1<br>kernel.event: net.deliver"]
    n3["11 0.003000000s n2#35;1<br>net.deliver: deliver #35;1.1 n1 -#gt; n2"]
    n4["12 0.003000000s global<br>check.violation: invariant #quot;no pong#quot; violated"]
    n0 --> n2
    n1 --> n2
    n2 --> n3
    n3 --> n4
    classDef violation fill:#fdd,stroke:#c00,stroke-width:2px
    classDef fault fill:#ffe9b3,stroke:#b07800
    classDef net fill:#e3efff,stroke:#3367d6
    class n1,n3 net
    class n4 violation
`

// AT-ART-07
func TestWriteHBFixture(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHB(&buf, fixtureTrace(), 12, 5); err != nil {
		t.Fatal(err)
	}
	if buf.String() != hbFixtureCap5 {
		t.Fatalf("hb.mmd mismatch:\n%s", buf.String())
	}
	buf.Reset()
	if err := WriteHB(&buf, fixtureTrace(), 0, 200); err != nil {
		t.Fatal(err)
	}
	want := "flowchart TD\n    %% faultline causal slice v1: root=0 records=0 cap=200 truncated=false\n    n0[\"no failure record\"]\n"
	if buf.String() != want {
		t.Fatalf("no-root hb.mmd = %q", buf.String())
	}
}

// chainTrace is the trace of AT-ART-08: 400 records on n1#1, each caused by the one before.
func chainTrace() *Trace {
	tr := &Trace{Header: TraceHeader{Nodes: []Node{{ID: 1, Name: "n1", Tags: []string{"server"}}}}}
	for seq := uint64(1); seq <= 400; seq++ {
		tr.Records = append(tr.Records, kernel.Record{
			Seq: seq, At: kernel.Time(seq * 1000), Node: 1, Inc: 1, Cause: seq - 1,
			Kind: "chain." + strings.Repeat("k", 60), Text: strings.Repeat("#", 200),
		})
	}
	return tr
}

// AT-ART-08
func TestWriteHBHalvesCap(t *testing.T) {
	tr := chainTrace()
	s200, _, err := renderHB(tr, CausalSlice(tr.Records, 400, 200))
	if err != nil {
		t.Fatal(err)
	}
	if len(s200) != 57924 {
		t.Fatalf("cap-200 rendering is %d bytes, want 57924", len(s200))
	}
	var buf bytes.Buffer
	if err := WriteHB(&buf, tr, 400, 200); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 28924 {
		t.Fatalf("output is %d bytes, want 28924", buf.Len())
	}
	if !strings.Contains(buf.String(), "%% faultline causal slice v1: root=400 records=100 cap=100 truncated=true\n") {
		t.Fatalf("comment line wrong:\n%s", buf.String()[:200])
	}
}

// hbTrace has a record for each label, class and edge rule of ART-055 that the fixture leaves out:
// a global record before a global root of kind net.* (no program-order edge, class violation), a
// fault, a net record on node 7, which is not in the node table, whose cause is a later record (no
// edge), a cause that is also the program-order predecessor (one edge), a cause and a different
// program-order predecessor (two edges), gograph's escapes, texts of 40 and 41 runes, and invalid
// UTF-8 in a node name, a kind and a text (where it must be replaced before the text is cut).
func hbTrace() *Trace {
	e40 := strings.Repeat("é", 40)
	return &Trace{
		Header: TraceHeader{Nodes: []Node{{ID: 1, Name: "n\xff\xfe1"}}},
		Records: []kernel.Record{
			{Seq: 1, Kind: "run.phase", Text: "body"},
			{Seq: 2, Node: 1, Inc: 1, Kind: "fault.crash", Text: "crash", Cause: 1},
			{Seq: 3, Node: 7, Inc: 2, Kind: "net.recv", Text: e40, Cause: 6},
			{Seq: 4, Node: 1, Inc: 1, Kind: "app.b", Text: "a\nb|<c>\"#", Cause: 2},
			{Seq: 5, Node: 1, Inc: 1, Kind: "app.\xff\xfek", Text: strings.Repeat("a", 38) + "\xff\xfebc", Cause: 3},
			{Seq: 6, Kind: "net.send", Text: e40 + "é", Cause: 5},
		},
	}
}

// hbTraceText is WriteHB(hbTrace(), 6, 200), with ~ for U+FFFD.
const hbTraceText = `flowchart TD
    %% faultline causal slice v1: root=6 records=6 cap=200 truncated=false
    n0["1 0.000000000s global<br>run.phase: body"]
    n1["2 0.000000000s n~1#35;1<br>fault.crash: crash"]
    n2["3 0.000000000s node7#35;2<br>net.recv: éééééééééééééééééééééééééééééééééééééééé"]
    n3["4 0.000000000s n~1#35;1<br>app.b: a\nb#124;#lt;c#gt;#quot;#35;"]
    n4["5 0.000000000s n~1#35;1<br>app.~k: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa~b..."]
    n5["6 0.000000000s global<br>net.send: éééééééééééééééééééééééééééééééééééééééé..."]
    n0 --> n1
    n1 --> n3
    n2 --> n4
    n3 --> n4
    n4 --> n5
    classDef violation fill:#fdd,stroke:#c00,stroke-width:2px
    classDef fault fill:#ffe9b3,stroke:#b07800
    classDef net fill:#e3efff,stroke:#3367d6
    class n1 fault
    class n2 net
    class n5 violation
`

// ART-055: labels, classes and edges beyond the fixture. A kind and a text are made valid UTF-8
// before the text is cut at 40 runes, so a trace written by WriteTrace and read back renders the
// same bytes (as Render after Write must, AT-ART-14).
func TestWriteHBRules(t *testing.T) {
	want := strings.ReplaceAll(hbTraceText, "~", string(utf8.RuneError))
	var buf bytes.Buffer
	if err := WriteHB(&buf, hbTrace(), 6, 200); err != nil {
		t.Fatal(err)
	}
	if buf.String() != want {
		t.Fatalf("hb.mmd mismatch:\n%s", buf.String())
	}
	var jsonl bytes.Buffer
	if err := WriteTrace(&jsonl, hbTrace()); err != nil {
		t.Fatal(err)
	}
	back, err := ReadTrace(&jsonl)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := WriteHB(&buf, back, 6, 200); err != nil || buf.String() != want {
		t.Fatalf("after WriteTrace and ReadTrace: %v\n%s", err, buf.String())
	}
	// nodeLabel writes the placeholder it is given for node 0 (timeline.txt passes "-").
	if got := nodeLabel(kernel.Record{}, nil, "-"); got != "-" {
		t.Errorf("nodeLabel of a global record = %q, want -", got)
	}
}

// hbDefs are the three classDef lines of every hb.mmd with a slice.
const hbDefs = `    classDef violation fill:#fdd,stroke:#c00,stroke-width:2px
    classDef fault fill:#ffe9b3,stroke:#b07800
    classDef net fill:#e3efff,stroke:#3367d6
`

// ART-050 and ART-055: program order is per node incarnation, and it is the latest record on that
// incarnation, not the latest slice member; the classes go by the "fault." and "net." prefixes,
// dots included, and the root is "violation" whatever its kind; gograph also escapes & and the
// backquote, and a \r\n in a text is written as the two escapes.
func TestWriteHBProgramOrderAndClasses(t *testing.T) {
	for _, c := range []struct {
		name    string
		records []kernel.Record
		root    uint64
		cap     int
		want    string
	}{
		{"no edge across incarnations", []kernel.Record{
			{Seq: 1, Node: 1, Inc: 1, Kind: "k", Text: "A"},
			{Seq: 2, Node: 2, Inc: 1, Kind: "k", Text: "C", Cause: 1},
			{Seq: 3, Node: 1, Inc: 2, Kind: "k", Text: "B", Cause: 2},
		}, 3, 200, `flowchart TD
    %% faultline causal slice v1: root=3 records=3 cap=200 truncated=false
    n0["1 0.000000000s node1#35;1<br>k: A"]
    n1["2 0.000000000s node2#35;1<br>k: C"]
    n2["3 0.000000000s node1#35;2<br>k: B"]
    n0 --> n1
    n1 --> n2
` + hbDefs + `    class n2 violation
`},
		{"the predecessor is the latest record, here outside the slice", []kernel.Record{
			{Seq: 1, Node: 1, Inc: 1, Kind: "k", Text: "q"},
			{Seq: 2, Node: 1, Inc: 1, Kind: "k", Text: "p"},
			{Seq: 3, Node: 1, Inc: 1, Kind: "k", Text: "b"},
			{Seq: 4, Node: 2, Inc: 1, Kind: "k", Text: "c", Cause: 1},
			{Seq: 5, Node: 1, Inc: 1, Kind: "k", Text: "R", Cause: 4},
		}, 5, 4, `flowchart TD
    %% faultline causal slice v1: root=5 records=4 cap=4 truncated=true
    n0["1 0.000000000s node1#35;1<br>k: q"]
    n1["3 0.000000000s node1#35;1<br>k: b"]
    n2["4 0.000000000s node2#35;1<br>k: c"]
    n3["5 0.000000000s node1#35;1<br>k: R"]
    n0 --> n2
    n1 --> n3
    n2 --> n3
` + hbDefs + `    class n3 violation
`},
		{"prefixes with their dots, a fault root, & and backquote", []kernel.Record{
			{Seq: 1, Kind: "faultline.x", Text: "a&b`c\r\nd"},
			{Seq: 2, Kind: "network.y", Cause: 1, Text: "x"},
			{Seq: 3, Kind: "fault.crash", Cause: 2, Text: "r"},
		}, 3, 200, `flowchart TD
    %% faultline causal slice v1: root=3 records=3 cap=200 truncated=false
    n0["1 0.000000000s global<br>faultline.x: a#amp;b#96;c\r\nd"]
    n1["2 0.000000000s global<br>network.y: x"]
    n2["3 0.000000000s global<br>fault.crash: r"]
    n0 --> n1
    n1 --> n2
` + hbDefs + `    class n2 violation
`},
	} {
		var buf bytes.Buffer
		if err := WriteHB(&buf, &Trace{Records: c.records}, c.root, c.cap); err != nil || buf.String() != c.want {
			t.Errorf("%s: %v\n got:\n%s\nwant:\n%s", c.name, err, buf.String(), c.want)
		}
	}
}

// ART-055 steps 5 and 6: no root, a cap of 1, an odd cap; a nil trace and write errors.
func TestWriteHBEdges(t *testing.T) {
	noRoot := "flowchart TD\n    %%%% faultline causal slice v1: root=0 records=0 cap=%d truncated=false\n    n0[\"no failure record\"]\n"
	for _, c := range []struct {
		root uint64
		cap  int
	}{{99, 200}, {12, 0}, {12, -1}} {
		var buf bytes.Buffer
		if err := WriteHB(&buf, fixtureTrace(), c.root, c.cap); err != nil || buf.String() != fmt.Sprintf(noRoot, c.cap) {
			t.Errorf("WriteHB(root %d, cap %d) = %v, %q", c.root, c.cap, err, buf.String())
		}
	}

	// The cap keeps the root's cause but not its program-order predecessor: no edge from it.
	cut := &Trace{Records: []kernel.Record{
		{Seq: 1, Node: 1, Inc: 1, Kind: "k", Text: "po"},
		{Seq: 2, Node: 2, Inc: 1, Kind: "k", Text: "cause"},
		{Seq: 3, Node: 1, Inc: 1, Kind: "k", Text: "root", Cause: 2},
	}}
	want := `flowchart TD
    %% faultline causal slice v1: root=3 records=2 cap=2 truncated=true
    n0["2 0.000000000s node2#35;1<br>k: cause"]
    n1["3 0.000000000s node1#35;1<br>k: root"]
    n0 --> n1
    classDef violation fill:#fdd,stroke:#c00,stroke-width:2px
    classDef fault fill:#ffe9b3,stroke:#b07800
    classDef net fill:#e3efff,stroke:#3367d6
    class n1 violation
`
	var cutBuf bytes.Buffer
	if err := WriteHB(&cutBuf, cut, 3, 2); err != nil || cutBuf.String() != want {
		t.Errorf("program-order predecessor outside the slice: %v\n%s", err, cutBuf.String())
	}

	// At cap 1 the text is written whatever its size.
	big := &Trace{Records: []kernel.Record{{Seq: 1, Kind: strings.Repeat("k", MermaidMaxBytes)}}}
	var buf bytes.Buffer
	if err := WriteHB(&buf, big, 1, 200); err != nil || buf.Len() <= MermaidMaxBytes ||
		!strings.HasPrefix(buf.String(), "flowchart TD\n    %% faultline causal slice v1: root=1 records=1 cap=1 truncated=false\n") {
		t.Errorf("cap 1: %v, %d bytes", err, buf.Len())
	}

	// A text of exactly MermaidMaxBytes bytes is written; one byte more halves the cap.
	for _, c := range []struct {
		extra int
		want  string
	}{
		{0, "root=2 records=2 cap=2 truncated=false"},
		{1, "root=2 records=1 cap=1 truncated=true"},
	} {
		tr := &Trace{Records: []kernel.Record{{Seq: 1, Kind: "a"}, {Seq: 2, Kind: strings.Repeat("k", 49_646+c.extra), Cause: 1}}}
		buf.Reset()
		if err := WriteHB(&buf, tr, 2, 2); err != nil || !strings.Contains(buf.String(), c.want) || (c.extra == 0 && buf.Len() != MermaidMaxBytes) {
			t.Errorf("%d extra bytes: %v, %d bytes:\n%.200s", c.extra, err, buf.Len(), buf.String())
		}
	}

	// At most MermaidMaxEdges edges are written: 252 records on n1#1, each caused by the one two
	// before, make 2*252-3 = 501 edges, so cap 300 halves to 150; with the last record's cause
	// removed, 500 edges are written at cap 300.
	for _, c := range []struct {
		lastCause bool
		want      string
		edges     int
	}{
		{false, "root=252 records=252 cap=300 truncated=false", 500},
		{true, "root=252 records=150 cap=150 truncated=true", 296},
	} {
		tr := &Trace{}
		for seq := uint64(1); seq <= 252; seq++ {
			r := kernel.Record{Seq: seq, Node: 1, Inc: 1, Kind: "k"}
			if seq > 2 && (seq < 252 || c.lastCause) {
				r.Cause = seq - 2
			}
			tr.Records = append(tr.Records, r)
		}
		buf.Reset()
		if err := WriteHB(&buf, tr, 252, 300); err != nil || !strings.Contains(buf.String(), c.want) || strings.Count(buf.String(), " --> ") != c.edges {
			t.Errorf("edges: %v, %d edges:\n%.200s", err, strings.Count(buf.String(), " --> "), buf.String())
		}
	}

	// A chain of 501 records has exactly 500 edges and is written whole. A slice that cannot fit is
	// halved without being rendered: a 50,000-record chain at cap 1<<30 must not build a graph on
	// each of the passes that keep the whole chain.
	chain := func(n uint64) *Trace {
		tr := &Trace{}
		for seq := uint64(1); seq <= n; seq++ {
			tr.Records = append(tr.Records, kernel.Record{Seq: seq, Node: 1, Inc: 1, Kind: "k", Cause: seq - 1})
		}
		return tr
	}
	buf.Reset()
	if err := WriteHB(&buf, chain(501), 501, 600); err != nil || !strings.Contains(buf.String(), "root=501 records=501 cap=600 truncated=false") || strings.Count(buf.String(), " --> ") != 500 {
		t.Errorf("501-record chain: %v, %d edges", err, strings.Count(buf.String(), " --> "))
	}
	long := chain(50_000)
	allocs := testing.AllocsPerRun(1, func() {
		buf.Reset()
		if err := WriteHB(&buf, long, 50_000, 1<<30); err != nil || !strings.Contains(buf.String(), "root=50000 records=256 cap=256 truncated=true") {
			t.Errorf("50,000-record chain at cap 1<<30: %v\n%.200s", err, buf.String())
		}
	})
	// About 0.8 million allocations come from CausalSlice's passes; rendering each oversized slice
	// would add about 2 million per pass (32 million in all).
	if allocs > 4_000_000 {
		t.Errorf("50,000-record chain at cap 1<<30: %.0f allocations, want at most 4,000,000 (no render of an oversized slice)", allocs)
	}

	// Each skip halves the cap, no more: a 2,000-record chain at cap 1,000 is skipped once, and cap
	// 500 fits (500 members, 499 edges).
	buf.Reset()
	if err := WriteHB(&buf, chain(2_000), 2_000, 1_000); err != nil || !strings.Contains(buf.String(), "root=2000 records=500 cap=500 truncated=true") {
		t.Errorf("2,000-record chain at cap 1,000: %v\n%.200s", err, buf.String())
	}

	// A node ID the table repeats takes the first entry's name, as in the run view.
	buf.Reset()
	twice := &Trace{Header: TraceHeader{Nodes: []Node{{ID: 1, Name: "a"}, {ID: 1, Name: "b"}}}, Records: []kernel.Record{{Seq: 1, Node: 1, Inc: 1, Kind: "k"}}}
	if err := WriteHB(&buf, twice, 1, 200); err != nil || !strings.Contains(buf.String(), `n0["1 0.000000000s a#35;1<br>k: "]`) {
		t.Errorf("repeated node ID: %v\n%s", err, buf.String())
	}

	// An odd cap rounds down when halved: 201 gives the cap-100 text of AT-ART-08.
	var c200, c201 bytes.Buffer
	if err := WriteHB(&c200, chainTrace(), 400, 200); err != nil {
		t.Fatal(err)
	}
	if err := WriteHB(&c201, chainTrace(), 400, 201); err != nil || c201.String() != c200.String() {
		t.Errorf("cap 201: %v, %d bytes, want the %d of cap 200", err, c201.Len(), c200.Len())
	}

	buf.Reset()
	if err := WriteHB(&buf, nil, 1, 200); err == nil || err.Error() != "artifact: trace is nil" || buf.Len() != 0 {
		t.Errorf("nil trace: %v, %q", err, buf.String())
	}
	errDisk := errors.New("disk full")
	for _, root := range []uint64{12, 0} {
		if err := WriteHB(errWriter{errDisk}, fixtureTrace(), root, 5); !errors.Is(err, errDisk) {
			t.Errorf("failed write, root %d: %v", root, err)
		}
	}

	// Records outside ART-051's domain (a repeated seq, seq 0, seqs out of order) give an
	// unspecified result, but never a panic (§7).
	for _, recs := range [][]kernel.Record{
		{{Seq: 1}, {Seq: 2, Cause: 1}, {Seq: 2, Cause: 1}},
		{{Seq: 1, Node: 1, Inc: 1}, {Seq: 1, Node: 2, Inc: 1}, {Seq: 2, Node: 1, Inc: 1}, {Seq: 2, Node: 2, Inc: 1}},
		{{Seq: 0, Node: 1, Inc: 1}, {Seq: 1, Node: 1, Inc: 1}, {Seq: 2, Cause: 1}},
		{{Seq: 3, Node: 1, Inc: 1}, {Seq: 1, Node: 1, Inc: 1, Cause: 3}, {Seq: 2, Node: 1, Inc: 1, Cause: 1}},
	} {
		buf.Reset()
		_ = WriteHB(&buf, &Trace{Records: recs}, 2, 200)
	}
}

// ART-055: the backslash and every rune that strconv.IsPrint rejects (NUL, ESC, DEL, U+009B,
// U+2028, the bidi override U+202E) are written the way strconv.QuoteRune writes them, in a node
// name, a kind and a text; a kind whose only such rune is the backslash too. The text is cut at 40
// runes before it is escaped, so an escape is never split, and the trace read back from
// trace.jsonl renders the same bytes.
func TestWriteHBControlRunes(t *testing.T) {
	bad := "\x00\x1b\x7f" + string(rune(0x9b)) + string(rune(0x2028)) + string(rune(0x202e)) + `\`
	quoted := `\x00\x1b\x7f`
	for _, r := range []rune{0x9b, 0x2028, 0x202e, '\\'} {
		q := strconv.QuoteRune(r)
		quoted += q[1 : len(q)-1]
	}
	tr := &Trace{
		Header: TraceHeader{Nodes: []Node{{ID: 1, Name: "n" + bad}}},
		Records: []kernel.Record{
			{Seq: 1, Node: 1, Inc: 1, Kind: "k" + bad, Text: "t" + bad},
			{Seq: 2, Node: 1, Inc: 1, Kind: `k\`, Text: strings.Repeat("a", 39) + "\x00bc", Cause: 1},
		},
	}
	want := "flowchart TD\n    %% faultline causal slice v1: root=2 records=2 cap=200 truncated=false\n" +
		`    n0["1 0.000000000s n` + quoted + `#35;1<br>k` + quoted + `: t` + quoted + `"]` + "\n" +
		`    n1["2 0.000000000s n` + quoted + `#35;1<br>k\\: ` + strings.Repeat("a", 39) + `\x00..."]` + "\n" +
		"    n0 --> n1\n" + hbDefs + "    class n1 violation\n"
	var buf bytes.Buffer
	if err := WriteHB(&buf, tr, 2, 200); err != nil || buf.String() != want {
		t.Fatalf("control runes: %v\n got: %q\nwant: %q", err, buf.String(), want)
	}
	var jsonl bytes.Buffer
	if err := WriteTrace(&jsonl, tr); err != nil {
		t.Fatal(err)
	}
	back, err := ReadTrace(&jsonl)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := WriteHB(&buf, back, 2, 200); err != nil || buf.String() != want {
		t.Fatalf("control runes after WriteTrace and ReadTrace: %v\n%q", err, buf.String())
	}
}
