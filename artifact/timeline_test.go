package artifact

import (
	"bytes"
	"encoding/json"
	"html"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

// AT-ART-10 (a)
func TestTimelineDataFixture(t *testing.T) {
	rep := fixtureReport()
	tr := fixtureTrace()
	sched := []byte("{\n  \"faultline_schedule\": 1,\n  \"events\": []\n}\n") // fault.Schedule{Version: 1}.Write (FLT-028)
	tl, err := TimelineData(&rep, tr, sched, CausalSlice(tr.Records, 12, 200), 50_000)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Trace != fixtureTraceText {
		t.Fatalf("trace text mismatch:\n%s", tl.Trace)
	}
	if tl.Schedule != string(sched) || tl.Window != nil || tl.Version != 1 || tl.Title != "faultline TestToy/seed=0x0000000000000001" {
		t.Fatalf("timeline = %+v", tl)
	}
	if tl.Total != 12 || tl.Dropped != 0 || tl.Report != &rep {
		t.Fatalf("total/dropped/report wrong: %+v", tl)
	}
	if tl.Slice == nil || !slices.Equal(tl.Slice.Seqs, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}) || tl.Slice.Root != 12 || tl.Slice.Cap != 200 || tl.Slice.Truncated {
		t.Fatalf("slice = %+v", tl.Slice)
	}
	if _, err := TimelineData(&rep, tr, nil, Slice{}, 999); err == nil || err.Error() != "artifact: maxRecords 999 is below 1000" {
		t.Fatalf("maxRecords 999: %v", err)
	}
	none, err := TimelineData(&rep, tr, nil, Slice{}, 1000)
	if err != nil || none.Slice != nil || none.Schedule != "" {
		t.Fatalf("no slice/schedule: %+v, %v", none, err)
	}
}

// window60k builds the synthetic trace of AT-ART-10 (b).
func window60k() *Trace {
	tr := &Trace{Header: TraceHeader{Nodes: []Node{{ID: 1, Name: "n1", Tags: []string{"server"}}}}}
	for seq := uint64(1); seq <= 59_999; seq++ {
		r := kernel.Record{Seq: seq, At: kernel.Time(seq * 1000), Node: 1, Inc: 1, Kind: "kernel.event", Cause: seq - 1, Text: "e"}
		if seq == 100 {
			r.Kind = "fault.crash"
			r.Attrs = attrs("id", "1", "effect", "applied")
		}
		tr.Records = append(tr.Records, r)
	}
	tr.Records = append(tr.Records, kernel.Record{Seq: 60_000, At: kernel.Time(60_000 * 1000), Kind: "check.violation", Cause: 59_999})
	return tr
}

// AT-ART-10 (b)
func TestTimelineDataWindow(t *testing.T) {
	rep := fixtureReport()
	tr := window60k()
	s := CausalSlice(tr.Records, 60_000, 200)
	if s.Seqs[len(s.Seqs)-1] != 59_801 {
		t.Fatalf("slice ends at %d", s.Seqs[len(s.Seqs)-1])
	}
	tl, err := TimelineData(&rep, tr, nil, s, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(tl.Trace, "\n"), "\n")
	if len(lines) != 50_001 {
		t.Fatalf("trace has %d lines", len(lines))
	}
	if !strings.Contains(lines[0], `"records":50000`) {
		t.Fatalf("header = %s", lines[0])
	}
	var prev uint64
	has100 := false
	for _, l := range lines[1:] {
		var r TraceRecord
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		if r.Seq <= prev {
			t.Fatalf("seq %d after %d", r.Seq, prev)
		}
		prev = r.Seq
		has100 = has100 || r.Seq == 100
	}
	if !has100 {
		t.Fatal("seq 100 (fault.crash) not included")
	}
	want := &TimelineWindow{FromSeq: 10_002, FromNS: 10_002_000, Extra: 1}
	if !reflect.DeepEqual(tl.Window, want) {
		t.Fatalf("window = %+v, want %+v", tl.Window, want)
	}
	if tl.Total != 60_000 {
		t.Fatalf("total = %d", tl.Total)
	}
}

// AT-ART-11
func TestWriteTimelineHTML(t *testing.T) {
	rep := fixtureReport()
	tr := fixtureTrace()
	tr.Records[8].Text = "</script><script>alert(1)</script>"
	s := CausalSlice(tr.Records, 12, 200)
	var a, b bytes.Buffer
	if err := WriteTimelineHTML(&a, &rep, tr, nil, s, DefaultTimelineRecords); err != nil {
		t.Fatal(err)
	}
	if err := WriteTimelineHTML(&b, &rep, tr, nil, s, DefaultTimelineRecords); err != nil {
		t.Fatal(err)
	}
	page := a.String()
	if page != b.String() {
		t.Fatal("two renders differ")
	}
	if !strings.HasPrefix(page, "<!DOCTYPE html>\n") {
		t.Fatal("page does not start with <!DOCTYPE html>")
	}
	csp := `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:">` + "\n"
	if !strings.Contains(page, csp) {
		t.Fatal("CSP meta line missing")
	}
	if n := strings.Count(strings.ToLower(page), "</script"); n != 2 {
		t.Fatalf("page has %d </script occurrences, want 2", n)
	}
	if !strings.Contains(page, "<title>"+html.EscapeString("faultline TestToy/seed=0x0000000000000001")+"</title>") {
		t.Fatal("title missing")
	}
	const open = `<script type="application/json" id="faultline-data">`
	i := strings.Index(page, open)
	j := strings.Index(page[i:], "</script>")
	data := page[i+len(open) : i+j]
	var got, want any
	if err := json.Unmarshal([]byte(data), &got); err != nil {
		t.Fatal(err)
	}
	tl, err := TimelineData(&rep, tr, nil, s, DefaultTimelineRecords)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, _ := json.Marshal(tl)
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("embedded data differs from TimelineData")
	}
	outside := page[:i] + page[i+j:]
	if strings.Contains(outside, "http://") || strings.Contains(outside, "https://") {
		t.Fatal("page references a URL outside the data block")
	}
}

// TestTimelineSize is the ART §9 size target: 50,000 records stay under 16 MB.
func TestTimelineSize(t *testing.T) {
	rep := fixtureReport()
	tr := window60k()
	var buf bytes.Buffer
	if err := WriteTimelineHTML(&buf, &rep, tr, nil, CausalSlice(tr.Records, 60_000, 200), DefaultTimelineRecords); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 16<<20 {
		t.Fatalf("timeline.html is %d bytes", buf.Len())
	}
}

// eventChain returns n records 1..n on node 1 inc 1, each caused by the previous one.
func eventChain(n uint64) *Trace {
	tr := &Trace{Header: TraceHeader{Version: 1, Nodes: []Node{{ID: 1, Name: "n1"}}}}
	for seq := uint64(1); seq <= n; seq++ {
		tr.Records = append(tr.Records, kernel.Record{Seq: seq, At: kernel.Time(seq * 1000), Node: 1, Inc: 1, Kind: "kernel.event", Cause: seq - 1, Text: "e"})
	}
	return tr
}

// includedSeqs reads the embedded trace back with ReadTrace (UI-066) and returns its seqs.
func includedSeqs(t *testing.T, tl *Timeline) []uint64 {
	t.Helper()
	rt, err := ReadTrace(strings.NewReader(tl.Trace))
	if err != nil {
		t.Fatalf("embedded trace does not read back: %v", err)
	}
	var out []uint64
	for _, r := range rt.Records {
		out = append(out, r.Seq)
	}
	return out
}

// ART-060's selection: everything at n == maxRecords; slice members always, counted once;
// specials newest first up to maxRecords/4, every special kind; the ART-030 header; Dropped from
// the trace header.
func TestTimelineDataSelection(t *testing.T) {
	rep := fixtureReport()
	tl, err := TimelineData(&rep, eventChain(1000), nil, Slice{}, 1000)
	if err != nil || tl.Window != nil {
		t.Fatalf("n == maxRecords: window = %+v, err %v", tl.Window, err)
	}

	tr := eventChain(2000)
	tl, err = TimelineData(&rep, tr, nil, CausalSlice(tr.Records, 300, 200), 1000)
	if err != nil {
		t.Fatal(err)
	}
	got := includedSeqs(t, tl)
	if !slices.Contains(got, 101) || !slices.Contains(got, 300) || len(got) != 1000 {
		t.Fatalf("slice members missing: %d records", len(got))
	}
	if want := (&TimelineWindow{FromSeq: 1201, FromNS: 1201000, Extra: 200}); !reflect.DeepEqual(tl.Window, want) {
		t.Fatalf("window = %+v", tl.Window)
	}
	dup, err := TimelineData(&rep, eventChain(1001), nil, Slice{Root: 1, Seqs: []uint64{1, 1}, Cap: 2}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := includedSeqs(t, dup); len(got) != 1000 {
		t.Fatalf("repeated slice seq: %d records", len(got))
	}

	tr = eventChain(2000)
	for i := 0; i < 300; i++ {
		tr.Records[i].Kind = "fault.x"
	}
	tl, err = TimelineData(&rep, tr, nil, Slice{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	got = includedSeqs(t, tl)
	if !slices.Contains(got, 300) || !slices.Contains(got, 51) || slices.Contains(got, 50) {
		t.Fatal("wrong specials kept")
	}
	if want := (&TimelineWindow{FromSeq: 1251, FromNS: 1251000, Extra: 250}); !reflect.DeepEqual(tl.Window, want) {
		t.Fatalf("window = %+v", tl.Window)
	}

	tr = eventChain(2000)
	kinds := []string{"check.x", "run.phase", "kernel.add_node", "kernel.boot", "kernel.crash", "kernel.pause", "kernel.resume", "kernel.fail", "kernel.panic", "fault.x"}
	for i, k := range kinds {
		tr.Records[10+i].Kind = k
	}
	tl, err = TimelineData(&rep, tr, nil, Slice{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	got = includedSeqs(t, tl)
	for i, k := range kinds {
		if !slices.Contains(got, uint64(11+i)) {
			t.Errorf("%s not kept", k)
		}
	}
	if slices.Contains(got, 10) {
		t.Error("kernel.event 10 kept")
	}

	tl, err = TimelineData(&rep, window60k(), nil, Slice{}, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tl.Trace, `{"faultline_trace":1,`) {
		t.Fatalf("header = %.80s", tl.Trace)
	}
	includedSeqs(t, tl)

	tr = fixtureTrace()
	tr.Header.Dropped = 5
	tl, err = TimelineData(&rep, tr, nil, Slice{}, 1000)
	if err != nil || tl.Dropped != 5 {
		t.Fatalf("dropped = %d, %v", tl.Dropped, err)
	}
}

// ART-060: the slice's Root, Cap and Truncated as given; nil only when Root is 0. The schedule's
// invalid UTF-8 is replaced, as in the other JSON that ART writes (§6).
func TestTimelineDataSliceAndSchedule(t *testing.T) {
	rep := fixtureReport()
	tr := fixtureTrace()
	tl, err := TimelineData(&rep, tr, []byte("{\"x\":\"a\xff\xfeb\"}\n"), CausalSlice(tr.Records, 12, 5), 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := &TimelineSlice{Root: 12, Seqs: []uint64{7, 9, 10, 11, 12}, Cap: 5, Truncated: true}
	if !reflect.DeepEqual(tl.Slice, want) {
		t.Fatalf("slice = %+v, want %+v", tl.Slice, want)
	}
	if tl.Schedule != "{\"x\":\"a\xef\xbf\xbdb\"}\n" { // a, U+FFFD, b
		t.Fatalf("schedule = %q", tl.Schedule)
	}
	h, err := TimelineData(&rep, tr, nil, Slice{Root: 12, Cap: 1}, 1000)
	if err != nil || h.Slice == nil {
		t.Fatalf("slice with root 12 and no seqs = %+v, %v", h.Slice, err)
	}
}

// ART-070: the data block is json.Marshal's bytes with < escaped, and WriteTimelineHTML returns
// TimelineData's error and writes nothing.
func TestWriteTimelineHTMLData(t *testing.T) {
	rep := fixtureReport()
	tr := fixtureTrace()
	tr.Records[8].Text = "a<b>&c"
	var buf bytes.Buffer
	if err := WriteTimelineHTML(&buf, &rep, tr, nil, Slice{}, 1000); err != nil {
		t.Fatal(err)
	}
	tl, err := TimelineData(&rep, tr, nil, Slice{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(tl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `id="faultline-data">`+strings.ReplaceAll(string(want), "<", "\\u003c")+"</script>") {
		t.Fatal("data block is not json.Marshal's bytes")
	}
	var b2 bytes.Buffer
	if err := WriteTimelineHTML(&b2, &rep, tr, nil, Slice{}, 999); err == nil || err.Error() != "artifact: maxRecords 999 is below 1000" || b2.Len() != 0 {
		t.Fatalf("WriteTimelineHTML(999) = %v, wrote %d bytes", err, b2.Len())
	}
}

// §7 and ART-060: nil pointers (the report first), and a causal slice that leaves no room below
// maxRecords, give errors and write nothing.
func TestTimelineDataErrors(t *testing.T) {
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
		if _, err := TimelineData(c.rep, c.tr, nil, Slice{}, 1000); err == nil || err.Error() != c.want {
			t.Errorf("TimelineData(%v, %v) = %v, want %s", c.rep != nil, c.tr != nil, err, c.want)
		}
		var buf bytes.Buffer
		if err := WriteTimelineHTML(&buf, c.rep, c.tr, nil, Slice{}, 1000); err == nil || err.Error() != c.want || buf.Len() != 0 {
			t.Errorf("WriteTimelineHTML(%v, %v) = %v, wrote %d bytes; want %s", c.rep != nil, c.tr != nil, err, buf.Len(), c.want)
		}
	}
	// The nil checks come before the maxRecords check.
	if _, err := TimelineData(nil, fixtureTrace(), nil, Slice{}, 999); err == nil || err.Error() != "artifact: report is nil" {
		t.Errorf("nil report, maxRecords 999: %v", err)
	}
	if _, err := TimelineData(&rep, nil, nil, Slice{}, 999); err == nil || err.Error() != "artifact: trace is nil" {
		t.Errorf("nil trace, maxRecords 999: %v", err)
	}

	long := eventChain(3000)
	for _, c := range []struct {
		tr        *Trace
		root      uint64
		cap       int
		want      string
		fromSeq   uint64
		extraWant int
	}{
		// 1,500 members, the newest record not one of them.
		{long, 2000, 1500, "artifact: causal slice has 1500 records; it must be smaller than maxRecords 1000", 0, 0},
		// Every one of 1,001 records is a member.
		{eventChain(1001), 1001, 2000, "artifact: causal slice has 1001 records; it must be smaller than maxRecords 1000", 0, 0},
		{long, 2000, 1000, "artifact: causal slice has 1000 records; it must be smaller than maxRecords 1000", 0, 0},
		// 999 members leave room for the newest record.
		{long, 2000, 999, "", 3000, 999},
	} {
		tl, err := TimelineData(&rep, c.tr, nil, CausalSlice(c.tr.Records, c.root, c.cap), 1000)
		if c.want != "" {
			if err == nil || err.Error() != c.want {
				t.Errorf("slice cap %d: err = %v, want %s", c.cap, err, c.want)
			}
			continue
		}
		if err != nil || tl.Window == nil || tl.Window.FromSeq != c.fromSeq || tl.Window.Extra != c.extraWant {
			t.Errorf("slice cap %d: window %+v, err %v", c.cap, tl.Window, err)
		}
	}
	// The count is of the slice's seqs found in the trace, each once.
	seqs := []uint64{2000, 2000, 99_999}
	for q := uint64(1001); q < 2000; q++ {
		seqs = append(seqs, q)
	}
	_, err := TimelineData(&rep, long, nil, Slice{Root: 2000, Seqs: seqs, Cap: 2000}, 1000)
	if err == nil || err.Error() != "artifact: causal slice has 1000 records; it must be smaller than maxRecords 1000" {
		t.Errorf("hand-made slice: err = %v", err)
	}
}
