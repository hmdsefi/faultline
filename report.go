// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// recordDiff is the first difference of two record lists (API-071).
type recordDiff struct {
	index int
	a, b  *kernel.Record // nil: that list ended
	lenA  int
	lenB  int
}

// recordsEqual compares every field of two records; nil and empty Attrs are equal (API-071).
func recordsEqual(a, b kernel.Record) bool {
	return a.Seq == b.Seq && a.At == b.At && a.Node == b.Node && a.Inc == b.Inc && a.Kind == b.Kind &&
		a.Cause == b.Cause && a.Text == b.Text && slices.Equal(a.Attrs, b.Attrs)
}

// firstDiff returns the first difference of a and b, or nil when they are equal (API-071).
func firstDiff(a, b []kernel.Record) *recordDiff {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if !recordsEqual(a[i], b[i]) {
			return &recordDiff{index: i, a: &a[i], b: &b[i], lenA: len(a), lenB: len(b)}
		}
	}
	if len(a) == len(b) {
		return nil
	}
	d := &recordDiff{index: n, lenA: len(a), lenB: len(b)}
	if len(a) > n {
		d.a = &a[n]
	}
	if len(b) > n {
		d.b = &b[n]
	}
	return d
}

// recordLine formats a record for the determinism message (API-071).
func recordLine(r kernel.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d t=%s node=%d#%d %s cause=%d %q", r.Seq, r.At, r.Node, r.Inc, r.Kind, r.Cause, r.Text)
	for _, a := range r.Attrs {
		fmt.Fprintf(&b, " %s=%q", a.Key, a.Value)
	}
	return b.String()
}

// commonCauses is line 3 of the determinism message (API-071).
const commonCauses = "common causes: state kept between runs in the same process (package-level variables, sync.Once, caches), map iteration order, global math/rand, wall-clock time, goroutines"

// determinismMessage returns the message of a determinism failure (API-071).
func determinismMessage(d *determinism) string {
	var lines []string
	if d.original != nil {
		lines = append(lines, "the first run failed: "+d.original.headline())
	}
	h := d.hashes
	switch {
	case d.diff != nil:
		lines = append(lines, fmt.Sprintf("first difference at record index %d:", d.diff.index))
		side := func(label string, r *kernel.Record, n int) string {
			if r == nil {
				return fmt.Sprintf("%s: (trace ended after %d records)", label, n)
			}
			return label + ": " + recordLine(*r)
		}
		lines = append(lines, side("A", d.diff.a, d.diff.lenA), side("B", d.diff.b, d.diff.lenB))
	case h[len(h)-2] != h[len(h)-1]:
		// Only a limit re-run keeps part of its records (API-070).
		lines = append(lines, fmt.Sprintf("the two full-trace runs differ before the last %d records they kept (trace hashes 0x%016x and 0x%016x); the kept records are identical", d.kept, h[len(h)-2], h[len(h)-1]))
	default:
		odd := "the first two runs both differed"
		switch d.same {
		case h[1]:
			odd = "only the first run differed"
		case h[0]:
			odd = "only the second run differed"
		}
		lines = append(lines, fmt.Sprintf("the two full-trace runs were identical (trace hash 0x%016x); %s", d.same, odd))
	}
	lines = append(lines, commonCauses)
	return strings.Join(lines, "\n")
}

// newDeterminismFailure builds the determinism failure of API-070.
func newDeterminismFailure(context string, seed uint64, attempts []attemptResult, original *failure, diffA, diffB []kernel.Record, art attemptResult) *failure {
	d := &determinism{context: context, seed: seed, original: original, diff: firstDiff(diffA, diffB)}
	for _, a := range attempts {
		d.hashes = append(d.hashes, a.hash)
	}
	d.same = attempts[len(attempts)-1].hash
	if d.diff == nil {
		d.kept = len(diffA)
	}
	f := &failure{kind: "determinism", context: "determinism-artifact", at: art.now, determinism: d}
	if context == "check_determinism" {
		f.context = "determinism-check"
	}
	f.message = determinismMessage(d)
	return f
}

// signature returns kind + ":" + check (API-065).
func (f *failure) signature() string { return f.kind + ":" + f.check }

// report converts f to report.json's failure object (ART-021, API-066).
func (f *failure) report() *artifact.Failure {
	rf := &artifact.Failure{
		Kind: f.kind, Check: f.check, Signature: f.signature(), Headline: f.headline(), Message: f.message,
		AtNS: int64(f.at), At: f.at.String(), Node: f.node, NodeID: int32(f.nodeID), RecordSeq: f.recordSeq,
		Panic: f.panic, Limit: f.limit,
	}
	if f.hasEvent {
		rf.Event = f.event
	}
	for _, fr := range f.finals {
		rf.Finals = append(rf.Finals, artifact.FinalFailure{Check: fr.check, Message: fr.message, Panic: fr.panic})
	}
	if d := f.determinism; d != nil {
		rd := &artifact.Determinism{Context: d.context}
		for _, h := range d.hashes {
			rd.Hashes = append(rd.Hashes, fmt.Sprintf("0x%016x", h))
		}
		if d.original != nil {
			rd.Original = d.original.report()
		}
		if d.diff != nil {
			rd.Diff = &artifact.RecordDiff{Index: d.diff.index}
			if d.diff.a != nil {
				tr := artifact.FromRecord(*d.diff.a)
				rd.Diff.A = &tr
			}
			if d.diff.b != nil {
				tr := artifact.FromRecord(*d.diff.b)
				rd.Diff.B = &tr
			}
		}
		rf.Determinism = rd
	}
	return rf
}

// stackLines returns the stack text split on \n with the trailing empty line dropped (API-075).
func stackLines(stack string) []string {
	return strings.Split(strings.TrimSuffix(stack, "\n"), "\n")
}

// consoleLines returns the failure report lines of API-075. full disables the 40-line stack
// truncation (report.txt, API-076); written says whether the artifacts were written.
func consoleLines(f *failure, replayCommand, artifactsLine string, warnings []string, full, written bool) []string {
	lines := []string{"faultline: " + f.headline()}
	for _, l := range strings.Split(f.message, "\n") {
		lines = append(lines, "  "+l)
	}
	if f.panic != nil {
		lines = append(lines, "stack:")
		st := stackLines(f.panic.Stack)
		shown := st
		if !full && len(st) > 40 {
			shown = st[:40]
		}
		for _, l := range shown {
			lines = append(lines, "  "+l)
		}
		if !full && len(st) > 40 {
			more := fmt.Sprintf("  ... %d more lines", len(st)-40)
			if written {
				more += " (full stack in report.txt)"
			}
			lines = append(lines, more)
		}
	}
	lines = append(lines, "replay:    "+replayCommand, "artifacts: "+artifactsLine)
	for _, w := range warnings {
		lines = append(lines, "warning: "+w)
	}
	return lines
}

// joinLines joins lines, each followed by \n, each prefixed with indent.
func joinLines(lines []string, indent string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(indent + l + "\n")
	}
	return b.String()
}

// compareReports returns the warnings of API-083 for a previous report.
func compareReports(prev *artifact.Report, cur artifact.Versions, optionsHash string) []string {
	var w []string
	if prev.Versions.Faultline != cur.Faultline {
		w = append(w, fmt.Sprintf("previous artifact was recorded with faultline %s; this run uses %s", prev.Versions.Faultline, cur.Faultline))
	}
	if p, c := goMinor(prev.Versions.Go), goMinor(cur.Go); p != c {
		w = append(w, fmt.Sprintf("previous artifact was recorded with %s; this run uses %s", p, c))
	}
	if prev.Versions.TestBinarySHA256 != "" && cur.TestBinarySHA256 != "" && prev.Versions.TestBinarySHA256 != cur.TestBinarySHA256 {
		w = append(w, "the test binary differs from the one that recorded the previous artifact (code, dependencies or build flags changed)")
	}
	if prev.OptionsHash != optionsHash {
		w = append(w, fmt.Sprintf("options differ from the previous artifact (options hash %s, now %s)", prev.OptionsHash, optionsHash))
	}
	return w
}

// extraFile is one file of Artifact.Extra (API-077).
type extraFile struct {
	name string
	data []byte
}

// artifactFiler is the optional interface of check errors that add artifact files (API-077).
type artifactFiler interface {
	ArtifactFiles() map[string][]byte
}

// extraFiles collects Artifact.Extra from the failure's errors (API-077): the failure error for
// API-061 rows 2, 4a and 5, and every failing final check's error for kind final and for kind
// panic when a final check panicked.
func extraFiles(f *failure) ([]extraFile, []string) {
	if f == nil {
		return nil, nil
	}
	type source struct {
		err  error
		name string
	}
	var sources []source
	if f.err != nil && len(f.finals) == 0 {
		sources = append(sources, source{err: f.err, name: "the seed's failure error"})
	}
	for _, fr := range f.finals {
		if fr.err != nil {
			sources = append(sources, source{err: fr.err, name: "final check \"" + fr.check + "\""})
		}
	}
	var files []extraFile
	var warnings []string
	addedBy := map[string]string{} // file name -> the source that added it; lookups only
	for _, s := range sources {
		var af artifactFiler
		if !errors.As(s.err, &af) {
			continue
		}
		m, panicked, text := artifactFilesOf(af)
		if panicked {
			if text == "" || strings.Contains(text, "\n") {
				text = strconv.Quote(text) // one visible line in the warning block
			}
			warnings = append(warnings, fmt.Sprintf("extra artifact files from %s dropped: ArtifactFiles panicked: %s", s.name, text))
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(m)) {
			if first, ok := addedBy[name]; ok {
				warnings = append(warnings, fmt.Sprintf("extra artifact file %q from %s dropped: %s already added it", name, s.name, first))
				continue
			}
			addedBy[name] = s.name
			files = append(files, extraFile{name: name, data: m[name]})
		}
	}
	return files, warnings
}

// artifactFilesOf calls af.ArtifactFiles, which is user code, and recovers its panic instead of
// letting it leave the attempt (API-077). panicked reports the panic, also one with an empty
// value; text is its panic text (API-064).
func artifactFilesOf(af artifactFiler) (files map[string][]byte, panicked bool, text string) {
	defer func() {
		if v := recover(); v != nil {
			files, panicked, text = nil, true, panicText(v)
		}
	}()
	return af.ArtifactFiles(), false, ""
}
