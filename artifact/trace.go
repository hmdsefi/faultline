// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
)

// Trace is the content of trace.jsonl.
type Trace struct {
	Header  TraceHeader
	Records []kernel.Record // ascending Seq
}

// TraceHeader is the first line of trace.jsonl.
type TraceHeader struct {
	Version          int    `json:"faultline_trace"`   // TraceVersion
	FaultlineVersion string `json:"faultline_version"` // report.versions.faultline
	GoVersion        string `json:"go_version"`        // runtime.Version()
	Package          string `json:"package"`
	Test             string `json:"test"`
	Subtest          string `json:"subtest"`
	Seed             string `json:"seed"`       // 0x%016x
	TraceHash        string `json:"trace_hash"` // 0x%016x, hash of the whole run
	Records          uint64 `json:"records"`    // number of record lines that follow
	Dropped          uint64 `json:"dropped"`    // records emitted before the first retained one
	Nodes            []Node `json:"nodes"`
}

// Node is one entry of the node table.
type Node struct {
	ID   int32    `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"` // never null; [] when none
}

// TraceRecord is the JSON form of a kernel.Record (one trace.jsonl line, one timeline record).
type TraceRecord struct {
	Seq   uint64      `json:"seq"`
	At    int64       `json:"at"`             // virtual nanoseconds since kernel.Epoch
	T     string      `json:"t"`              // kernel.Time(At).String(); ignored when reading
	Node  int32       `json:"node,omitempty"` // 0 = global
	Inc   uint32      `json:"inc,omitempty"`
	Kind  string      `json:"kind"`
	Cause uint64      `json:"cause,omitempty"`
	Text  string      `json:"text,omitempty"`
	Attrs [][2]string `json:"attrs,omitempty"` // [key, value] pairs in record order
}

// FromRecord converts r; T is set from r.At, and Kind, Text and the attrs are made valid UTF-8
// (each run of invalid bytes becomes one U+FFFD).
func FromRecord(r kernel.Record) TraceRecord {
	t := TraceRecord{
		Seq:   r.Seq,
		At:    int64(r.At),
		T:     r.At.String(),
		Node:  int32(r.Node),
		Inc:   r.Inc,
		Kind:  validUTF8(r.Kind),
		Cause: r.Cause,
		Text:  validUTF8(r.Text),
	}
	if len(r.Attrs) > 0 {
		t.Attrs = make([][2]string, len(r.Attrs))
		for i, a := range r.Attrs {
			t.Attrs[i] = [2]string{validUTF8(a.Key), validUTF8(a.Value)}
		}
	}
	return t
}

// Record converts back. Attrs is nil when there are none.
func (t TraceRecord) Record() kernel.Record {
	r := kernel.Record{
		Seq:   t.Seq,
		At:    kernel.Time(t.At),
		Node:  kernel.NodeID(t.Node),
		Inc:   t.Inc,
		Kind:  t.Kind,
		Cause: t.Cause,
		Text:  t.Text,
	}
	if len(t.Attrs) > 0 {
		r.Attrs = make([]kernel.Attr, len(t.Attrs))
		for i, a := range t.Attrs {
			r.Attrs[i] = kernel.Attr{Key: a[0], Value: a[1]}
		}
	}
	return r
}

// validUTF8 returns s with each run of invalid UTF-8 bytes replaced by U+FFFD (ART-030).
// encoding/json replaces them too, but Go 1.26 writes the escape and Go 1.27 the character
// itself; a valid string is written the same way by both. report.json and the timeline data use
// it too.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, string(utf8.RuneError))
}

// normNodes returns a copy of nodes with valid UTF-8 names and tags, and nil Tags written as []
// (ART-011, ART-030).
func normNodes(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	for i, n := range nodes {
		tags := make([]string, len(n.Tags)) // never nil
		for j, tag := range n.Tags {
			tags[j] = validUTF8(tag)
		}
		out[i] = Node{ID: n.ID, Name: validUTF8(n.Name), Tags: tags}
	}
	return out
}

// droppedOf returns Records[0].Seq - 1, or 0 when there are no records or the first Seq is 0
// (ART-011, ART-030); a first Seq of 0 is invalid, and 0 - 1 would wrap round.
func droppedOf(records []kernel.Record) uint64 {
	if len(records) == 0 || records[0].Seq == 0 {
		return 0
	}
	return records[0].Seq - 1
}

// headerFor returns the header line that WriteTrace writes for t (ART-030): the version, the
// counts and the node table recomputed, and the strings made valid UTF-8.
func headerFor(t *Trace) TraceHeader {
	h := t.Header
	h.Version = TraceVersion
	for _, p := range []*string{&h.FaultlineVersion, &h.GoVersion, &h.Package, &h.Test, &h.Subtest, &h.Seed, &h.TraceHash} {
		*p = validUTF8(*p)
	}
	h.Records = uint64(len(t.Records))
	h.Dropped = droppedOf(t.Records)
	h.Nodes = normNodes(h.Nodes)
	return h
}

// WriteTrace writes t as trace.jsonl: the header line, then one JSON line per record. It does
// not change t.
func WriteTrace(w io.Writer, t *Trace) error {
	if t == nil {
		return errors.New("artifact: trace is nil")
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(headerFor(t)); err != nil {
		return err
	}
	for _, r := range t.Records {
		if err := enc.Encode(FromRecord(r)); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// recordLine is the decoding form of one record line.
type recordLine struct {
	Seq   uint64     `json:"seq"`
	At    int64      `json:"at"`
	Node  int32      `json:"node"`
	Inc   uint32     `json:"inc"`
	Kind  string     `json:"kind"`
	Cause uint64     `json:"cause"`
	Text  string     `json:"text"`
	Attrs [][]string `json:"attrs"`
}

// rawRecordLine is recordLine with the attrs left raw: it decodes a line that recordLine rejects
// only because of an attrs element, so that element can be named.
type rawRecordLine struct {
	recordLine
	Attrs []json.RawMessage `json:"attrs"`
}

// ReadTrace reads and validates trace.jsonl. It rejects a trace with a newer version than this
// package writes.
func ReadTrace(r io.Reader) (*Trace, error) {
	br := bufio.NewReader(r)
	lineErr := func(n int, format string, args ...any) error {
		return fmt.Errorf("artifact: trace.jsonl line %d: %s", n, fmt.Sprintf(format, args...))
	}
	var tr Trace
	n := 0
	var prev uint64
	var l recordLine // reused from line to line, so its attrs arrays keep their capacity
	for {
		line, err := br.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(line) == 0 && errors.Is(err, io.EOF) {
			break
		}
		n++
		line = bytes.TrimSuffix(line, []byte("\n"))
		if n == 1 {
			if err := readHeader(line, &tr.Header); err != nil {
				return nil, lineErr(1, "%s", err)
			}
		} else {
			rec, err := readRecord(line, &l)
			if err != nil {
				return nil, lineErr(n, "%s", err)
			}
			if rec.Seq <= prev {
				return nil, lineErr(n, "seq %d is not greater than the previous seq %d", rec.Seq, prev)
			}
			prev = rec.Seq
			if len(tr.Records) == cap(tr.Records) {
				tr.Records = growRecords(tr.Records, tr.Header.Records)
			}
			tr.Records = append(tr.Records, rec)
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if n == 0 {
		return nil, lineErr(1, "not a faultline trace")
	}
	if uint64(len(tr.Records)) != tr.Header.Records {
		return nil, lineErr(1, "header says %d records, found %d", tr.Header.Records, len(tr.Records))
	}
	return &tr, nil
}

// growRecords returns records with room for more: twice the capacity (at least 1024), or the
// header's count when that lies between the two. A true count ends in an exact fit, and a header
// that claims more records than the file holds costs at most twice the records really read.
func growRecords(records []kernel.Record, claimed uint64) []kernel.Record {
	n := max(2*cap(records), 1024)
	if claimed > uint64(cap(records)) && claimed < uint64(n) {
		n = int(claimed) //nolint:gosec // claimed is below n, an int
	}
	out := make([]kernel.Record, len(records), n)
	copy(out, records)
	return out
}

// skipJSONSpace returns b after its leading JSON whitespace (space, tab, \r and \n). Unlike
// bytes.TrimSpace it keeps \v, \f, U+0085 and U+00A0, which JSON does not allow (ART-031). The
// callers only ask whether anything is left and what its first byte is.
func skipJSONSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	return b
}

func readHeader(line []byte, h *TraceHeader) error {
	var obj map[string]json.RawMessage
	if len(skipJSONSpace(line)) == 0 || skipJSONSpace(line)[0] != '{' || json.Unmarshal(line, &obj) != nil {
		return errors.New("not a faultline trace")
	}
	raw, ok := obj["faultline_trace"]
	var v int64
	if !ok || json.Unmarshal(raw, &v) != nil || v < 1 {
		return errors.New("not a faultline trace")
	}
	if v > TraceVersion {
		return fmt.Errorf("version %d is newer than this faultline supports (%d); upgrade faultline", v, TraceVersion)
	}
	if err := json.Unmarshal(line, h); err != nil {
		return err
	}
	// encoding/json matches keys case-insensitively, so a "FAULTLINE_TRACE" key could overwrite
	// the version just checked; the header keeps the checked one.
	h.Version = int(v)
	return nil
}

// readRecord decodes one record line into l, which it reuses, and returns the record or the
// reason the line is rejected, without the line prefix (ART-031).
func readRecord(line []byte, l *recordLine) (kernel.Record, error) {
	trimmed := skipJSONSpace(line)
	if len(trimmed) == 0 {
		return kernel.Record{}, errors.New("empty line")
	}
	if trimmed[0] != '{' {
		return kernel.Record{}, errors.New("not a JSON object")
	}
	// Every field is reset, and the strings the last line left in the reused attrs arrays are
	// cleared: encoding/json leaves an element alone on null, and ["k", null] reads as k="".
	// At most 16 pairs of 2 strings are kept, so one wide record does not make every later line
	// clear its width.
	attrs := l.Attrs[:cap(l.Attrs)]
	if len(attrs) > 16 {
		attrs = nil
	}
	for i, kv := range attrs {
		if cap(kv) > 2 {
			attrs[i] = nil // only a repeated "attrs" key leaves a longer one
			continue
		}
		clear(kv[:cap(kv)])
	}
	*l = recordLine{Attrs: attrs[:0]}
	if err := json.Unmarshal(line, l); err != nil {
		return kernel.Record{}, recordError(line, err)
	}
	r := kernel.Record{
		Seq:   l.Seq,
		At:    kernel.Time(l.At),
		Node:  kernel.NodeID(l.Node),
		Inc:   l.Inc,
		Kind:  l.Kind,
		Cause: l.Cause,
		Text:  l.Text,
	}
	if len(l.Attrs) > 0 {
		r.Attrs = make([]kernel.Attr, len(l.Attrs))
		for i, kv := range l.Attrs {
			if len(kv) != 2 {
				return kernel.Record{}, fmt.Errorf("attrs element %d is not a [key, value] pair of strings", i)
			}
			r.Attrs[i] = kernel.Attr{Key: kv[0], Value: kv[1]}
		}
	}
	return r, nil
}

// recordError says why a record line that starts with { did not decode: the first attrs element
// that is not a pair of strings, as the fast path would have named it, or else err.
func recordError(line []byte, err error) error {
	var raw rawRecordLine
	if json.Unmarshal(line, &raw) == nil {
		for i, a := range raw.Attrs {
			var kv []string
			if json.Unmarshal(a, &kv) != nil || len(kv) != 2 {
				return fmt.Errorf("attrs element %d is not a [key, value] pair of strings", i)
			}
		}
	}
	return fmt.Errorf("not a JSON object: %v", err)
}
