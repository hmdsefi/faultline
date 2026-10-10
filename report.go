// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// recordDiff is the first difference of two record lists (API-071).
type recordDiff struct {
	index        int
	a, b         *kernel.Record // nil: that list ended
	lastA, lastB uint64         // Seq of the last record of each list (0 for none)
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
			return &recordDiff{index: i, a: &a[i], b: &b[i], lastA: lastSeq(a), lastB: lastSeq(b)}
		}
	}
	if len(a) == len(b) {
		return nil
	}
	d := &recordDiff{index: n, lastA: lastSeq(a), lastB: lastSeq(b)}
	if len(a) > n {
		d.a = &a[n]
	}
	if len(b) > n {
		d.b = &b[n]
	}
	return d
}

// lastSeq returns the Seq of the last record of records, or 0 when there is none.
func lastSeq(records []kernel.Record) uint64 {
	if len(records) == 0 {
		return 0
	}
	return records[len(records)-1].Seq
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

// commonCauses and nextStep end every determinism message (API-071, lines 3 and 4).
const (
	commonCauses = "common causes: state kept between runs in the same process (package-level variables, sync.Once, caches), map iteration order, global math/rand, wall-clock time, goroutines"
	nextStep     = "next step: fix the cause; until the seed gives the same run every time, the replay command may not reproduce this failure"
)

// firstRunLines returns line 1 of a determinism message whose first run failed with f (API-071):
// its headline, then each of its message lines that holds a non-space character, indented.
func firstRunLines(f *failure) []string {
	lines := []string{"the first run failed: " + f.headline()}
	for _, l := range messageLines(f.message) {
		lines = append(lines, "  "+l)
	}
	return lines
}

// messageLines returns the lines of a failure message that hold a non-space character (API-075).
func messageLines(message string) []string {
	var lines []string
	for _, l := range strings.Split(message, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// determinismMessage returns the message of a determinism failure (API-071).
func determinismMessage(d *determinism) string {
	var lines []string
	if d.original != nil {
		lines = append(lines, firstRunLines(d.original)...)
	}
	h := d.hashes
	runA := 2 // the first full-trace run: run 2 after the artifact re-run, run 3 in the check
	if d.context == "check_determinism" {
		runA = 3
	}
	switch {
	case d.recorded != [2]uint64{}:
		lines = append(lines, fmt.Sprintf("the two full-trace runs diverged before the records they kept: run %d recorded %d records and run %d recorded %d (trace hashes 0x%016x and 0x%016x)",
			runA, d.recorded[0], runA+1, d.recorded[1], h[len(h)-2], h[len(h)-1]))
	case d.diff != nil:
		seq := uint64(0)
		if d.diff.a != nil {
			seq = d.diff.a.Seq
		} else if d.diff.b != nil {
			seq = d.diff.b.Seq
		}
		lines = append(lines, fmt.Sprintf("first difference at record #%d:", seq))
		side := func(run int, r *kernel.Record, last uint64) string {
			if r == nil {
				return fmt.Sprintf("  run %d: (trace ended after record #%d)", run, last)
			}
			return fmt.Sprintf("  run %d: %s", run, recordLine(*r))
		}
		lines = append(lines, side(runA, d.diff.a, d.diff.lastA), side(runA+1, d.diff.b, d.diff.lastB))
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
	lines = append(lines, commonCauses, nextStep)
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
	if len(diffA) > 0 && len(diffB) > 0 && diffA[0].Seq != diffB[0].Seq {
		// Kept windows that start at different records: index 0 differs, but the runs diverged
		// before the records they kept (API-071).
		d.recorded = [2]uint64{lastSeq(diffA), lastSeq(diffB)}
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

// consoleLines returns the failure report lines of API-075: failureLines, then the replay and
// artifacts lines and the warnings.
func consoleLines(f *failure, replayCommand, artifactsLine string, warnings []string, full, written bool) []string {
	lines := append(failureLines(f, full, written), "replay:    "+replayCommand, "artifacts: "+artifactsLine)
	for _, w := range warnings {
		lines = append(lines, "warning: "+w)
	}
	return lines
}

// failureLines returns the lines of API-075 that describe the failure: the headline, the message
// lines and the stack. full shows every stack line instead of the stack window (report.txt,
// API-076); written says whether the artifacts were written.
func failureLines(f *failure, full, written bool) []string {
	lines := []string{"faultline: " + f.headline()}
	for _, l := range messageLines(f.message) {
		lines = append(lines, "  "+l)
	}
	if f.panic != nil {
		lines = append(lines, "stack:")
		shown, more := stackLines(f.panic.Stack), 0
		if !full {
			shown, more = stackWindow(shown)
		}
		for _, l := range shown {
			lines = append(lines, "  "+l)
		}
		if more > 0 {
			line := fmt.Sprintf("  ... %d more lines", more)
			if written {
				line += " (full stack in report.txt)"
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// maxStackLines is the size of the console's stack window (API-075).
const maxStackLines = 40

// stackWindow returns the stack lines the console shows and the number of lines after them
// (API-075): the goroutine line, then whole frames from the first frame of user code after the
// panic, at most maxStackLines lines in all. A frame is a function line and the tab-indented lines
// after it.
func stackWindow(lines []string) (shown []string, more int) {
	i := 0
	if len(lines) > 0 && strings.HasPrefix(lines[0], "goroutine ") {
		shown, i = append(shown, lines[0]), 1
	}
	i = firstUserFrame(lines, i)
	for i < len(lines) {
		end := i + 1
		for end < len(lines) && strings.HasPrefix(lines[end], "\t") {
			end++
		}
		if len(shown)+end-i > maxStackLines {
			break
		}
		shown, i = append(shown, lines[i:end]...), end
	}
	return shown, len(lines) - i
}

// firstUserFrame returns the index of the first frame after the last panic( line whose function
// is not the Go runtime's or faultline's, or from when there is none (API-075).
func firstUserFrame(lines []string, from int) int {
	if i, _ := frameAfterPanic(lines, from, faultlineOrRuntimeFrame); i >= 0 {
		return i
	}
	return from
}

// frameAfterPanic returns the index and the function of the first frame after the last line of
// lines[from:] that starts with "panic(" whose function skip does not reject, or -1 when there is
// no such line or frame (API-063, API-075). A frame's function is its line up to the last "(";
// tab-indented and blank lines are not frames.
func frameAfterPanic(lines []string, from int, skip func(fn string) bool) (int, string) {
	last := -1
	for i := from; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "panic(") {
			last = i
		}
	}
	if last < 0 {
		return -1, ""
	}
	for i := last + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "\t") || strings.TrimSpace(l) == "" {
			continue
		}
		fn := l
		if k := strings.LastIndex(l, "("); k >= 0 {
			fn = l[:k]
		}
		if !skip(fn) {
			return i, fn
		}
	}
	return -1, ""
}

// faultlineOrRuntimeFrame reports whether fn, a function name in a stack, belongs to the Go
// runtime or to faultline's own packages: the root package and the packages at or below kernel
// and check. Other libraries count as the user's code.
func faultlineOrRuntimeFrame(fn string) bool {
	if strings.HasPrefix(fn, "runtime.") || strings.HasPrefix(fn, "runtime/") {
		return true
	}
	rest, ok := strings.CutPrefix(fn, "github.com/hmdsefi/faultline")
	if !ok {
		return false
	}
	if strings.HasPrefix(rest, ".") {
		return true
	}
	for _, dir := range []string{"/kernel", "/check"} {
		if r, ok := strings.CutPrefix(rest, dir); ok && (strings.HasPrefix(r, ".") || strings.HasPrefix(r, "/")) {
			return true
		}
	}
	return false
}

// joinLines joins lines, each followed by \n, each prefixed with indent.
func joinLines(lines []string, indent string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(indent + l + "\n")
	}
	return b.String()
}

// compareReports returns the warnings of API-083 for a previous report, given this run's versions,
// options and options hash.
func compareReports(prev *artifact.Report, cur artifact.Versions, opts artifact.RunOptions, optionsHash string) []string {
	var w []string
	if prev.Versions.Faultline != cur.Faultline {
		w = append(w, fmt.Sprintf("previous artifact was recorded with faultline %s; this run uses %s", printable(prev.Versions.Faultline), printable(cur.Faultline)))
	}
	if p, c := goMinor(prev.Versions.Go), goMinor(cur.Go); p != c {
		w = append(w, fmt.Sprintf("previous artifact was recorded with %s; this run uses %s", printable(p), printable(c)))
	}
	if prev.Versions.TestBinarySHA256 != "" && cur.TestBinarySHA256 != "" && prev.Versions.TestBinarySHA256 != cur.TestBinarySHA256 {
		w = append(w, "the test binary differs from the one that recorded the previous artifact (code, dependencies or build flags changed)")
	}
	if prev.OptionsHash != optionsHash {
		hashes := fmt.Sprintf("(options hash %s, now %s)", printable(prev.OptionsHash), printable(optionsHash))
		var changes []string
		if prev.Options.Mode != "" { // Run always writes a mode; without one, every field would look changed
			changes = optionChanges(prev.Options, opts)
		}
		if len(changes) > 0 {
			w = append(w, "options differ from the previous artifact: "+strings.Join(changes, "; ")+" "+hashes)
		} else {
			w = append(w, "options differ from the previous artifact "+hashes)
		}
	}
	return w
}

// optionChanges names the options of API-082's hash whose values differ between a previous
// report's options and this run's, in the hash's order, by the names the user sets (API-083).
func optionChanges(prev, cur artifact.RunOptions) []string {
	var c []string
	if prev.DurationNS != cur.DurationNS {
		c = append(c, fmt.Sprintf("Options.Duration was %s, now %s", time.Duration(prev.DurationNS), time.Duration(cur.DurationNS)))
	}
	if prev.MaxEvents != cur.MaxEvents {
		c = append(c, fmt.Sprintf("Options.MaxEvents was %d, now %d", prev.MaxEvents, cur.MaxEvents))
	}
	if prev.Mode != cur.Mode {
		c = append(c, fmt.Sprintf("Options.Mode was %s, now %s", printable(prev.Mode), printable(cur.Mode)))
	}
	if !sameJSON(prev.Net, cur.Net) {
		c = append(c, "Options.Net changed")
	}
	if !sameJSON(prev.Disk, cur.Disk) {
		c = append(c, "Options.Disk changed")
	}
	if prev.NoCryptoSeed != cur.NoCryptoSeed {
		c = append(c, fmt.Sprintf("Options.NoCryptoSeed was %t, now %t", prev.NoCryptoSeed, cur.NoCryptoSeed))
	}
	if prev.AllowLimit != cur.AllowLimit {
		c = append(c, fmt.Sprintf("Options.AllowLimit was %t, now %t", prev.AllowLimit, cur.AllowLimit))
	}
	if prev.ScheduleHash != cur.ScheduleHash {
		c = append(c, fmt.Sprintf("FAULTLINE_SCHEDULE was %s, now %s", scheduleText(prev.ScheduleHash), scheduleText(cur.ScheduleHash)))
	}
	return c
}

// scheduleText describes the schedule_hash option for the options warning (API-083): "not set",
// or "schedule" and the hash of FAULTLINE_SCHEDULE's file.
func scheduleText(hash string) string {
	if hash == "" {
		return "not set"
	}
	return "schedule " + printable(hash)
}

// sameJSON reports whether a and b hold the same JSON value: report.json stores the options'
// objects indented, and a tool that rewrites it may reorder their keys. Numbers compare by their
// text, so no precision is lost.
func sameJSON(a, b json.RawMessage) bool {
	va, errA := decodeJSON(a)
	vb, errB := decodeJSON(b)
	if errA != nil || errB != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(va, vb)
}

// decodeJSON decodes one JSON value, with numbers as json.Number.
func decodeJSON(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	err := d.Decode(&v)
	return v, err
}

// printable returns s, or s quoted with %q when it is empty or holds a character that is not
// printable, so text read from a previous report.json or a panic text cannot put control
// characters on the console (API-077, API-083).
func printable(s string) string {
	if s == "" || strings.IndexFunc(s, func(r rune) bool { return !strconv.IsPrint(r) }) >= 0 {
		return strconv.Quote(s)
	}
	return s
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
			// printable: one visible line in the warning block, with no control characters
			warnings = append(warnings, fmt.Sprintf("extra artifact files from %s dropped: ArtifactFiles panicked: %s", s.name, printable(text)))
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
