package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

// renderWritten writes a to <tmp>/a and returns that directory.
func renderWritten(t *testing.T, a *Artifact) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "a")
	if err := Write(dir, a); err != nil {
		t.Fatal(err)
	}
	return dir
}

// renderAgain removes the three render files, renders with the default options, checks that every
// file has the bytes Write gave it, and returns Render's paths.
func renderAgain(t *testing.T, dir string) []string {
	t.Helper()
	before := readAll(t, dir)
	for _, name := range []string{FileTimelineText, FileHB, FileTimelineHTML} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := Render(dir, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after := readAll(t, dir)
	for _, name := range slices.Sorted(maps.Keys(before)) {
		if string(before[name]) != string(after[name]) {
			t.Errorf("%s differs after Render", name)
		}
	}
	if len(after) != len(before) {
		t.Errorf("files after Render: %v", slices.Sorted(maps.Keys(after)))
	}
	return paths
}

// renderFails runs Render on dir with opts, wants the error text want (or, ending in ": ", a text
// with that prefix), and checks that no file in dir changed: the same names, bytes and modification
// times (ART-090).
func renderFails(t *testing.T, dir string, opts RenderOptions, want string) {
	t.Helper()
	past := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	before := readAll(t, dir)
	names := slices.Sorted(maps.Keys(before))
	for _, name := range names {
		if err := os.Chtimes(filepath.Join(dir, name), past, past); err != nil {
			t.Fatal(err)
		}
	}
	switch _, err := Render(dir, opts); {
	case err == nil:
		t.Errorf("no error, want %s", want)
	case strings.HasSuffix(want, ": "):
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("error = %v, want the prefix %s", err, want)
		}
	case err.Error() != want:
		t.Errorf("error = %v, want %s", err, want)
	}
	after := readAll(t, dir)
	if !slices.Equal(slices.Sorted(maps.Keys(after)), names) {
		t.Errorf("files after the failed Render: %v", slices.Sorted(maps.Keys(after)))
	}
	for _, name := range names {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !fi.ModTime().Equal(past) || string(after[name]) != string(before[name]) {
			t.Errorf("%s changed in the failed Render", name)
		}
	}
}

// renderChain returns the fixture artifact with a trace of n records, each caused by the one
// before, and the newest as the failure record.
func renderChain(n uint64) *Artifact {
	a := fixtureArtifact()
	a.Trace = &Trace{Header: TraceHeader{Nodes: fixtureNodes()}}
	for seq := uint64(1); seq <= n; seq++ {
		a.Trace.Records = append(a.Trace.Records, kernel.Record{Seq: seq, Node: 1, Inc: 1, Cause: seq - 1, Kind: "k"})
	}
	a.Report.Failure.RecordSeq = n
	return a
}

// renderHostile is the fixture artifact with what Render must regenerate as Write wrote it:
// invalid UTF-8 (two-byte runs, which one U+FFFD replaces only when the cleanup runs), control
// characters and a line separator in a kind, a text cut at 40 runes, an attr, a node name and the
// headline; a node ID the table repeats; a record on a node the table lacks, as the root;
// history.jsonl and two extra files.
func renderHostile() *Artifact {
	a := fixtureArtifact()
	a.Trace.Header.Nodes = append(fixtureNodes(), Node{ID: 1, Name: "dup"}, Node{ID: 3, Name: "n\xff\xfe\x1b"})
	a.Trace.Records = append(a.Trace.Records,
		kernel.Record{Seq: 13, At: 3000000, Node: 3, Inc: 1, Kind: "net.x\xff\xfe\n\x1b", Cause: 12,
			Text: "a\xff\xfe\nb\x00" + string(rune(0x2028)) + strings.Repeat("é", 40), Attrs: attrs("k\xff\xfe", "v\n\x7f")},
		kernel.Record{Seq: 14, At: 3000000, Node: 9, Inc: 2, Kind: "k", Cause: 13},
	)
	a.Report.Failure.Headline += " \xff\xfe\x1b"
	a.Report.Failure.RecordSeq = 14
	a.History = []byte("{\"op\":1}\n")
	a.Extra = map[string][]byte{"a.txt": []byte("A\xff"), "b.json": []byte("{}")}
	return a
}

// AT-ART-14, also for the hostile artifact: Render after Write gives the same bytes, with mode
// 0o644, and the absolute paths in order. No other file changes: their modification times are set
// to a fixed past time first, so even a rewrite with the same bytes shows on a file system with
// coarse timestamps.
func TestRender(t *testing.T) {
	scheduled := fixtureArtifact()
	scheduled.Schedule = &fault.Schedule{Version: 1}
	past := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	renders := []string{FileTimelineText, FileHB, FileTimelineHTML}
	for _, c := range []struct {
		name string
		a    *Artifact
	}{{"fixture", scheduled}, {"hostile", renderHostile()}} {
		dir := renderWritten(t, c.a)
		names := slices.Sorted(maps.Keys(readAll(t, dir)))
		for _, name := range names {
			if err := os.Chtimes(filepath.Join(dir, name), past, past); err != nil {
				t.Fatal(err)
			}
		}
		paths := renderAgain(t, dir)
		wantPaths := []string{filepath.Join(dir, FileTimelineText), filepath.Join(dir, FileHB), filepath.Join(dir, FileTimelineHTML)}
		if !reflect.DeepEqual(paths, wantPaths) {
			t.Errorf("%s: paths = %v", c.name, paths)
		}
		for _, p := range wantPaths {
			fi, err := os.Stat(p)
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if fi.Mode().Perm() != 0o644 {
				t.Errorf("%s: %s: mode %v", c.name, p, fi.Mode().Perm())
			}
		}
		for _, name := range names {
			if slices.Contains(renders, name) {
				continue
			}
			if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || !fi.ModTime().Equal(past) {
				t.Errorf("%s: %s was modified", c.name, name)
			}
		}
	}

	// With cap 5: AT-ART-07's hb.mmd, the cap-5 timeline.txt, and the same slice in timeline.html.
	dir := renderWritten(t, scheduled)
	if _, err := Render(dir, RenderOptions{SliceCap: 5}); err != nil {
		t.Fatal(err)
	}
	hb, err := os.ReadFile(filepath.Join(dir, FileHB))
	if err != nil || string(hb) != hbFixtureCap5 {
		t.Fatalf("hb.mmd with cap 5:\n%s", hb)
	}
	txt, _ := os.ReadFile(filepath.Join(dir, FileTimelineText))
	if string(txt) != timelineTextCap5 {
		t.Fatalf("timeline.txt with cap 5:\n%s", txt)
	}
	if page, _ := os.ReadFile(filepath.Join(dir, FileTimelineHTML)); !strings.Contains(string(page), `"cap":5,`) {
		t.Fatal("timeline.html does not hold the cap-5 slice")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// ART-090: a pass report (root 0) without schedule.json renders as Write wrote it.
func TestRenderPassNoSchedule(t *testing.T) {
	a := fixtureArtifact()
	a.Report.Status, a.Report.Failure = "pass", nil
	dir := renderWritten(t, a)
	renderAgain(t, dir)
	if hb, _ := os.ReadFile(filepath.Join(dir, FileHB)); !strings.HasPrefix(string(hb), "flowchart TD\n    %% faultline causal slice v1: root=0 records=0 cap=200 ") {
		t.Errorf("hb.mmd of a pass report:\n%s", hb)
	}
}

// ART-090: a relative directory gives absolute paths.
func TestRenderRelativeDir(t *testing.T) {
	dir := renderWritten(t, fixtureArtifact())
	t.Chdir(filepath.Dir(dir))
	paths, err := Render("a", RenderOptions{})
	want := []string{filepath.Join(dir, FileTimelineText), filepath.Join(dir, FileHB), filepath.Join(dir, FileTimelineHTML)}
	if err != nil || !reflect.DeepEqual(paths, want) {
		t.Errorf("Render(\"a\") = %v, %v", paths, err)
	}
}

// ART-090: TimelineRecords and its default reach timeline.html. 1,500 records fit the default, as
// Write writes them, and need a window at MinTimelineRecords.
func TestRenderTimelineRecords(t *testing.T) {
	dir := renderWritten(t, renderChain(1500))
	renderAgain(t, dir)
	if _, err := Render(dir, RenderOptions{TimelineRecords: MinTimelineRecords}); err != nil {
		t.Fatal(err)
	}
	if page, _ := os.ReadFile(filepath.Join(dir, FileTimelineHTML)); !strings.Contains(string(page), `"window":{`) {
		t.Error("TimelineRecords did not reach timeline.html")
	}
}

// ART-090 and §7: the option checks, on a real artifact so that a missing report.json cannot answer
// for them, with their whole text; MinTimelineRecords itself is accepted. A directory without
// report.json is not an artifact; the other input errors name their file once and wrap the cause.
func TestRenderErrors(t *testing.T) {
	dir := renderWritten(t, fixtureArtifact())
	for _, c := range []struct {
		opts   RenderOptions
		reason string
	}{
		{RenderOptions{SliceCap: -1}, "slice cap -1 is negative"},
		{RenderOptions{TimelineRecords: MinTimelineRecords - 1}, "timeline records 999 is below 1000"},
		{RenderOptions{TimelineRecords: -1}, "timeline records -1 is below 1000"},
	} {
		if _, err := Render(dir, c.opts); err == nil || err.Error() != "artifact: render "+dir+": "+c.reason {
			t.Errorf("%+v: %v", c.opts, err)
		}
	}
	if _, err := Render(dir, RenderOptions{TimelineRecords: MinTimelineRecords}); err != nil {
		t.Errorf("TimelineRecords %d: %v", MinTimelineRecords, err)
	}

	empty := t.TempDir()
	if _, err := Render(empty, RenderOptions{}); err == nil || err.Error() != "artifact: render "+empty+": not a faultline artifact directory (no report.json); pass the directory of one seed, which holds report.json and trace.jsonl" {
		t.Errorf("missing report: %v", err)
	}
	file := filepath.Join(empty, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(file, RenderOptions{}); err == nil || err.Error() != "artifact: render "+file+": open report.json: not a directory" {
		t.Errorf("file as dir: %v", err)
	}
	for _, c := range []struct {
		name string
		edit func(dir string) error
		want string // the whole reason
		is   error  // errors.Is holds for it, when not nil
	}{
		{"no trace", func(d string) error { return os.Remove(filepath.Join(d, FileTrace)) }, "open trace.jsonl: no such file or directory", fs.ErrNotExist},
		{"bad report", func(d string) error { return os.WriteFile(filepath.Join(d, FileReport), []byte("{}"), 0o600) }, "report.json: not a faultline report", nil},
		{"bad trace", func(d string) error { return os.WriteFile(filepath.Join(d, FileTrace), []byte("{}\n"), 0o600) }, "trace.jsonl line 1: not a faultline trace", nil},
		{"unreadable schedule", func(d string) error { return os.Mkdir(filepath.Join(d, FileSchedule), 0o755) }, "read schedule.json: is a directory", nil},
		{"bad schedule", func(d string) error { return os.WriteFile(filepath.Join(d, FileSchedule), []byte("{}"), 0o600) }, `schedule.json: fault: schedule: missing field "faultline_schedule"`, nil},
	} {
		dir := renderWritten(t, fixtureArtifact())
		if err := c.edit(dir); err != nil {
			t.Fatal(err)
		}
		_, err := Render(dir, RenderOptions{})
		if err == nil || err.Error() != "artifact: render "+dir+": "+c.want || (c.is != nil && !errors.Is(err, c.is)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// ART-090: schedule.json is parsed and written again as Write writes it. A valid file in another
// layout reaches timeline.html as Write wrote it; a symlink to a file that is not a schedule is an
// error that changes no file, so the linked content never reaches the page.
func TestRenderSchedule(t *testing.T) {
	a := fixtureArtifact()
	a.Schedule = &fault.Schedule{Version: 1}
	dir := renderWritten(t, a)
	path := filepath.Join(dir, FileSchedule)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil || compact.String() == string(b) {
		t.Fatalf("compact schedule: %v", err)
	}
	if err := os.WriteFile(path, compact.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	renderAgain(t, dir)

	dir = renderWritten(t, a)
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET-TOKEN-abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, FileSchedule)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, path); err != nil {
		t.Fatal(err)
	}
	renderFails(t, dir, RenderOptions{}, "artifact: render "+dir+": schedule.json: fault: schedule: invalid JSON: ")
}

// ART-090 with §5.9: the slice error, found only while timeline.html is written, changes no file.
func TestRenderSliceError(t *testing.T) {
	dir := renderWritten(t, renderChain(1001))
	renderFails(t, dir, RenderOptions{SliceCap: 1000, TimelineRecords: MinTimelineRecords},
		"artifact: render "+dir+": write timeline.html: artifact: causal slice has 1000 records; it must be smaller than maxRecords 1000")
}

// ART-090: Render removes the temporary files an interrupted Render left, os.CreateTemp's names for
// its three prefixes only (the prefix, then digits), and only once the inputs are read: a Render
// that fails on schedule.json removes nothing.
func TestRenderRemovesStaleTemps(t *testing.T) {
	dir := renderWritten(t, fixtureArtifact())
	stale := []string{".timeline.txt.tmp-1", ".hb.mmd.tmp-2", ".timeline.html.tmp-3"}
	keep := []string{".report.json.tmp-4", "timeline.txt.tmp-5", ".timeline.txt.tmp", ".hb.mmd-tmp-6", ".timeline.txt.tmp-owned-by-someone", ".hb.mmd.tmp-7x", ".timeline.html.tmp-"}
	for _, name := range slices.Concat(stale, keep) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, FileSchedule)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderFails(t, dir, RenderOptions{}, "artifact: render "+dir+`: schedule.json: fault: schedule: missing field "faultline_schedule"`)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(dir, RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range stale {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was not removed: %v", name, err)
		}
	}
	for _, name := range keep {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != name {
			t.Errorf("%s changed: %v", name, err)
		}
	}
}

// ART-090: the temporary files go in dir itself, not in os.TempDir or the parent: with TMPDIR
// missing and the parent read-only, Render still succeeds.
func TestRenderTempInDir(t *testing.T) {
	dir := renderWritten(t, fixtureArtifact())
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	parent := filepath.Dir(dir)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if _, err := Render(dir, RenderOptions{}); err != nil {
		t.Fatal(err)
	}
}

// ART-090: a directory in the place of a target fails Render before the first rename, so no file
// changes and no temporary file is left.
func TestRenderRenameFails(t *testing.T) {
	dir := renderWritten(t, fixtureArtifact())
	if err := os.WriteFile(filepath.Join(dir, FileTimelineText), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, FileHB)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, FileHB, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(dir, RenderOptions{}); err == nil || err.Error() != "artifact: render "+dir+": hb.mmd is a directory" {
		t.Fatalf("Render over a directory = %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, FileTimelineText)); err != nil || string(b) != "old" {
		t.Errorf("timeline.txt changed: %q, %v", b, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
