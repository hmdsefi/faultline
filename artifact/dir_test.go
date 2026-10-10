// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bytes"
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

// AT-ART-01
func TestSanitize(t *testing.T) {
	cases := []struct {
		fn       func(string) string
		name     string
		in, want string
	}{
		{SanitizePackage, "pkg", "github.com/acme/kv", "github.com_acme_kv"},
		{SanitizePackage, "pkg", "command-line-arguments", "command-line-arguments"},
		{SanitizePackage, "pkg", "example.com/a b/ü", "example.com_a_b___"},
		{SanitizePackage, "pkg", "", "_"},
		{SanitizePackage, "pkg", "..", "_.."},
		{SanitizeTest, "test", "TestKV", "TestKV"},
		{SanitizeTest, "test", "TestKV/raft_3_nodes", "TestKV__raft_3_nodes"},
		{SanitizeTest, "test", "TestX/a:b*c?", "TestX__a_b_c_"},
		{SanitizeTest, "test", "TestX/case#01", "TestX__case#01"},
		{SanitizeTest, "test", "TestLong/" + strings.Repeat("abcdefghij", 11), "TestLong__abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabc-72ac6d5da30ea3f3"},
		// Only "", "." and ".." are reserved.
		{SanitizePackage, "pkg", ".", "_."},
		{SanitizePackage, "pkg", "...", "..."},
		// The cap applies when out passes 100 bytes; "__" makes out longer than the input.
		{SanitizeTest, "test", "T/" + strings.Repeat("a", 97), "T__" + strings.Repeat("a", 97)},
		{SanitizeTest, "test", "T/" + strings.Repeat("a", 98), "T__" + strings.Repeat("a", 80) + "-e2831902091d0efa"},
		// The cut falls inside the "___" of 日 (bytes of out, not runes), and the hash keeps its
		// leading zero.
		{SanitizeTest, "test", "TestCut/" + strings.Repeat("a", 73) + "日本" + strings.Repeat("b", 16), "TestCut__" + strings.Repeat("a", 73) + "_-05abd7da7bab7067"},
	}
	for _, c := range cases {
		if got := c.fn(c.in); got != c.want {
			t.Errorf("Sanitize %s(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if got := Dir("/r", "github.com/acme/kv", "TestKV/a b", 0x5e1f9a2c4b7d3e80); got != filepath.FromSlash("/r/github.com_acme_kv/TestKV__a_b/5e1f9a2c4b7d3e80") {
		t.Errorf("Dir = %q", got)
	}
}

// ART-002: each byte on its own, between two kept bytes so that no other rule applies.
func TestSanitizeBytes(t *testing.T) {
	const kept = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-_+=@,~#"
	for c := 0; c < 256; c++ {
		in := string([]byte{'x', byte(c), 'x'})
		pkg, test := "x_x", "x_x"
		switch {
		case strings.IndexByte(kept, byte(c)) >= 0:
			pkg, test = in, in
		case c == '/':
			test = "x__x"
		}
		if got := SanitizePackage(in); got != pkg {
			t.Errorf("SanitizePackage(%q) = %q, want %q", in, got, pkg)
		}
		if got := SanitizeTest(in); got != test {
			t.Errorf("SanitizeTest(%q) = %q, want %q", in, got, test)
		}
	}
}

// ART-001: filepath.Join cleans the path, and the seed is always 16 hex digits.
func TestDir(t *testing.T) {
	cases := []struct {
		root, pkg, test string
		seed            uint64
		want            string
	}{
		{"/r/", "a/b", "T/c", 1, "/r/a_b/T__c/0000000000000001"},
		{"", "", "", 0, "_/_/0000000000000000"},
	}
	for _, c := range cases {
		if got := Dir(c.root, c.pkg, c.test, c.seed); got != filepath.FromSlash(c.want) {
			t.Errorf("Dir(%q, %q, %q, %#x) = %q, want %q", c.root, c.pkg, c.test, c.seed, got, filepath.FromSlash(c.want))
		}
	}
}

// readAll returns the content of every regular file in dir by name.
func readAll(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	return out
}

func fileNames(files []File) []string {
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names
}

// AT-ART-05
func TestWrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pkg", "TestToy", "0000000000000001")
	a := fixtureArtifact()
	a.Schedule = &fault.Schedule{Version: 1}
	if err := Write(dir, a); err != nil {
		t.Fatal(err)
	}
	first := readAll(t, dir)
	a2 := fixtureArtifact()
	a2.Schedule = &fault.Schedule{Version: 1}
	if err := Write(dir, a2); err != nil {
		t.Fatal(err)
	}
	if second := readAll(t, dir); !reflect.DeepEqual(first, second) {
		t.Fatal("two Write calls produced different files")
	}
	wantFiles := []string{"report.txt", "trace.jsonl", "schedule.json", "timeline.txt", "hb.mmd", "timeline.html"}
	if got := fileNames(a.Report.Files); !slices.Equal(got, wantFiles) {
		t.Fatalf("report.files = %v, want %v", got, wantFiles)
	}
	if !filepath.IsAbs(a.Report.Dir) || a.Report.Dir != dir {
		t.Fatalf("report.dir = %q", a.Report.Dir)
	}
	if string(first["report.txt"]) != a.Text {
		t.Fatal("report.txt is not Artifact.Text")
	}
	rep, err := ReadReport(bytes.NewReader(first["report.json"]))
	if err != nil || rep.Dir != dir || len(rep.Files) != 6 {
		t.Fatalf("report.json: %+v, %v", rep, err)
	}

	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = Write(other, fixtureArtifact())
	if err == nil || err.Error() != "artifact: refusing to replace "+other+": not a faultline artifact directory (no report.json)" {
		t.Fatalf("Write(other) = %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "x.txt")); err != nil {
		t.Fatal("x.txt was removed")
	}

	bad := fixtureArtifact()
	bad.Schedule = &fault.Schedule{Version: 1, Events: []fault.Event{{At: kernel.Time(1)}}} // no kind: fails Validate
	err = Write(dir, bad)
	if err == nil || !strings.HasPrefix(err.Error(), "artifact: write schedule.json: ") {
		t.Fatalf("Write(invalid schedule) = %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".*.tmp-*")); len(left) != 0 {
		t.Fatalf("temporary directories left: %v", left)
	}
	if after := readAll(t, dir); !reflect.DeepEqual(first, after) {
		t.Fatal("failed Write changed the existing artifact")
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".*.tmp-*")); len(left) != 0 {
		t.Fatalf("temporary directories left: %v", left)
	}
}

func TestWriteValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a")
	cases := []struct {
		name   string
		mutate func(a *Artifact)
		want   string
	}{
		{"nil trace", func(a *Artifact) { a.Trace = nil }, "artifact: trace is nil"},
		{"seq 0", func(a *Artifact) { a.Trace.Records[0].Seq = 0 }, "artifact: record with Seq 0"},
		{"order", func(a *Artifact) { a.Trace.Records[3].Seq = 2 }, "artifact: records not in ascending Seq order at index 3"},
		{"equal seqs", func(a *Artifact) { a.Trace.Records[3].Seq = 3 }, "artifact: records not in ascending Seq order at index 3"},
		{"status", func(a *Artifact) { a.Report.Status = "ok" }, `artifact: invalid status "ok"`},
		{"fail without failure", func(a *Artifact) { a.Report.Failure = nil }, "artifact: status fail without failure"},
		{"pass with failure", func(a *Artifact) { a.Report.Status = "pass" }, "artifact: status pass with failure"},
		{"pointer cycle", func(a *Artifact) { a.Report.Failure.Determinism = &Determinism{Original: a.Report.Failure} }, "artifact: report has a pointer cycle"},
		{"cycle checked last", func(a *Artifact) {
			a.Report.Failure.Determinism = &Determinism{Original: a.Report.Failure}
			a.Trace = nil
		}, "artifact: trace is nil"},
	}
	for _, c := range cases {
		a := fixtureArtifact()
		c.mutate(a)
		if err := Write(dir, a); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if err := Write(dir, nil); err == nil || err.Error() != "artifact: artifact is nil" {
		t.Errorf("Write(nil) = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("validation errors touched the file system")
	}
}

// AT-ART-16
func TestRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a")
	a := fixtureArtifact()
	if err := Write(dir, a); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Schedule != nil || got.History != nil || got.Extra != nil || got.Text != a.Text {
		t.Fatalf("Read = %+v", got)
	}
	if !reflect.DeepEqual(got.Trace, a.Trace) {
		t.Fatalf("trace differs:\n got %+v\nwant %+v", got.Trace, a.Trace)
	}
	if got.Report.Dir != a.Report.Dir || !reflect.DeepEqual(got.Report.Files, a.Report.Files) || got.Report.Failure == nil || got.Report.Failure.RecordSeq != 12 {
		t.Fatalf("Read report = %+v", got.Report)
	}
	only := filepath.Join(t.TempDir(), "only")
	if err := os.MkdirAll(only, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(only, FileReport), readAll(t, dir)[FileReport], 0o600); err != nil {
		t.Fatal(err)
	}
	got2, err := Read(only)
	if err != nil || got2.Trace != nil || got2.Text != "" {
		t.Fatalf("Read(report only) = %+v, %v", got2, err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Read(missing); err == nil || !strings.HasPrefix(err.Error(), "artifact: "+missing+": open report.json: ") || strings.Count(err.Error(), missing) != 1 {
		t.Fatalf("Read(missing) = %v", err)
	}
}

// AT-ART-17
func TestWritePassReport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p")
	a := fixtureArtifact()
	a.Report.Status = "pass"
	a.Report.Failure = nil
	if err := Write(dir, a); err != nil {
		t.Fatal(err)
	}
	files := readAll(t, dir)
	wantHB := "flowchart TD\n    %% faultline causal slice v1: root=0 records=0 cap=200 truncated=false\n    n0[\"no failure record\"]\n"
	if string(files["hb.mmd"]) != wantHB {
		t.Fatalf("hb.mmd = %q", files["hb.mmd"])
	}
	txt := string(files["timeline.txt"])
	if !strings.Contains(txt, "failure:  none\n") || !strings.Contains(txt, "slice:    none\n") {
		t.Fatalf("timeline.txt header:\n%s", txt)
	}
	for _, line := range strings.Split(txt, "\n")[12:] {
		if strings.HasPrefix(line, "!") || strings.HasPrefix(line, "*") {
			t.Fatalf("pass timeline has a mark: %q", line)
		}
	}
}

// AT-ART-20
func TestWriteExtraFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	a := fixtureArtifact()
	a.Schedule = &fault.Schedule{Version: 1}
	a.Extra = map[string][]byte{"b.json": []byte("B"), "a.txt": []byte("A"), "0_x-y.txt": []byte("C")}
	if err := Write(dir, a); err != nil {
		t.Fatal(err)
	}
	files := readAll(t, dir)
	if string(files["a.txt"]) != "A" || string(files["b.json"]) != "B" || string(files["0_x-y.txt"]) != "C" {
		t.Fatal("extra files not written verbatim")
	}
	want := []File{
		{Name: "report.txt", Type: "report_text"},
		{Name: "trace.jsonl", Type: "trace", Version: 1},
		{Name: "schedule.json", Type: "schedule", Version: 1},
		{Name: "0_x-y.txt", Type: "extra"},
		{Name: "a.txt", Type: "extra"},
		{Name: "b.json", Type: "extra"},
		{Name: "timeline.txt", Type: "timeline_text", Version: 1},
		{Name: "hb.mmd", Type: "hb", Version: 1},
		{Name: "timeline.html", Type: "timeline_html", Version: 1},
	}
	if !reflect.DeepEqual(a.Report.Files, want) {
		t.Fatalf("files = %+v", a.Report.Files)
	}
	got, err := Read(dir)
	if err != nil || got.Extra != nil {
		t.Fatalf("Read: %v, Extra %v", err, got.Extra)
	}
	cases := []struct{ name, want string }{
		{"Bad", `artifact: invalid extra file name "Bad"`},
		{"x/y", `artifact: invalid extra file name "x/y"`},
		{".hidden", `artifact: invalid extra file name ".hidden"`},
		{"aB", `artifact: invalid extra file name "aB"`},
	}
	for _, name := range []string{FileReport, FileReportText, FileTrace, FileSchedule, FileHistory, FileTimelineText, FileHB, FileTimelineHTML, FileMinimized} {
		cases = append(cases, struct{ name, want string }{name, `artifact: extra file "` + name + `" collides with a standard file`})
	}
	for _, c := range cases {
		target := filepath.Join(root, "bad-"+strings.NewReplacer("/", "_", ".", "_").Replace(c.name))
		b := fixtureArtifact()
		b.Extra = map[string][]byte{c.name: []byte("z"), "ok.txt": []byte("ok")}
		if err := Write(target, b); err == nil || err.Error() != c.want {
			t.Errorf("Extra %q: %v, want %q", c.name, err, c.want)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("Extra %q: %s was created", c.name, target)
		}
	}
	two := fixtureArtifact()
	two.Extra = map[string][]byte{"Bad": nil, ".hidden": nil}
	if err := Write(filepath.Join(root, "two"), two); err == nil || err.Error() != `artifact: invalid extra file name ".hidden"` {
		t.Errorf("two invalid names: %v, want the first in byte order", err)
	}
}

// AT-ART-21
func TestWriteReplacesStallDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "d")
	dir2 := filepath.Join(root, "d2")
	for _, d := range []string{dir, dir2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "stall.txt"), []byte("stall"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir2, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stall.txt")); !os.IsNotExist(err) {
		t.Fatal("stall.txt survived")
	}
	err := Write(dir2, fixtureArtifact())
	if err == nil || !strings.HasPrefix(err.Error(), "artifact: refusing to replace ") {
		t.Fatalf("Write(dir2) = %v", err)
	}
	for _, f := range []string{"stall.txt", "x.txt"} {
		if _, err := os.Stat(filepath.Join(dir2, f)); err != nil {
			t.Fatalf("%s removed", f)
		}
	}
}

// ART-011: Version, the absolute cleaned Dir of a relative dir, and the header counts, which the
// render files use too.
func TestWriteSetsFields(t *testing.T) {
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	a := fixtureArtifact()
	a.Report.Version = 0
	a.Trace.Header.Version, a.Trace.Header.Records, a.Trace.Header.Dropped = 0, 0, 7
	if err := Write(filepath.Join("rel", "x", "..", "a"), a); err != nil {
		t.Fatal(err)
	}
	if a.Report.Dir != filepath.Join(wd, "rel", "a") {
		t.Fatalf("report.dir = %q", a.Report.Dir)
	}
	h := a.Trace.Header
	if a.Report.Version != ReportVersion || h.Version != TraceVersion || h.Records != 12 || h.Dropped != 0 {
		t.Fatalf("version %d, header %d/%d/%d", a.Report.Version, h.Version, h.Records, h.Dropped)
	}
	var want bytes.Buffer
	if err := WriteTimelineText(&want, &a.Report, a.Trace, CausalSlice(a.Trace.Records, 12, DefaultSliceCap)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(a.Report.Dir, FileTimelineText)); err != nil || !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("timeline.txt was not rendered from the header Write set (%v):\n%s", err, got)
	}
}

// ART-012, ART-014, ART-035: the render files use the default options, the failure record as root
// and the schedule bytes; the directory holds the index plus report.json; Read parses schedule.json.
func TestWriteRenderFiles(t *testing.T) {
	for _, n := range []int{12, 1001} {
		dir := filepath.Join(t.TempDir(), "a")
		a := fixtureArtifact()
		if n > 12 { // above MinTimelineRecords, so DefaultTimelineRecords matters
			a.Trace.Records = nil
			for i := 1; i <= n; i++ {
				a.Trace.Records = append(a.Trace.Records, kernel.Record{Seq: uint64(i), At: kernel.Time(i), Node: 1, Inc: 1, Kind: "kernel.event", Cause: uint64(i - 1), Text: "e"})
			}
			a.Report.Failure.RecordSeq = uint64(n)
		}
		a.Schedule = &fault.Schedule{Version: 1}
		if err := Write(dir, a); err != nil {
			t.Fatal(err)
		}
		files := readAll(t, dir)
		var sched bytes.Buffer
		if err := a.Schedule.Write(&sched); err != nil {
			t.Fatal(err)
		}
		root := a.Report.Failure.RecordSeq
		s := CausalSlice(a.Trace.Records, root, DefaultSliceCap)
		var txt, hb, html bytes.Buffer
		if err := WriteTimelineText(&txt, &a.Report, a.Trace, s); err != nil {
			t.Fatal(err)
		}
		if err := WriteHB(&hb, a.Trace, root, DefaultSliceCap); err != nil {
			t.Fatal(err)
		}
		if err := WriteTimelineHTML(&html, &a.Report, a.Trace, sched.Bytes(), s, DefaultTimelineRecords); err != nil {
			t.Fatal(err)
		}
		for _, f := range []struct {
			name string
			want []byte
		}{{FileSchedule, sched.Bytes()}, {FileTimelineText, txt.Bytes()}, {FileHB, hb.Bytes()}, {FileTimelineHTML, html.Bytes()}} {
			if !bytes.Equal(files[f.name], f.want) {
				t.Errorf("%d records: %s differs", n, f.name)
			}
		}
		want := append(fileNames(a.Report.Files), FileReport)
		slices.Sort(want)
		if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, want) {
			t.Errorf("%d records: files %v, want %v", n, got, want)
		}
		got, err := Read(dir)
		if err != nil || got.Schedule == nil {
			t.Fatalf("Read = %+v, %v", got, err)
		}
		var again bytes.Buffer
		if err := got.Schedule.Write(&again); err != nil || !bytes.Equal(again.Bytes(), sched.Bytes()) {
			t.Errorf("Read schedule = %+v, %v", got.Schedule, err)
		}
	}
}

// ART-014, ART-035: history.jsonl verbatim, its index entry, Read; a version-0 schedule is listed
// as version 1.
func TestWriteHistory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "h")
	a := fixtureArtifact()
	a.Schedule = &fault.Schedule{}
	a.History = []byte("{\"op\":1}\n")
	if err := Write(dir, a); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, FileHistory)); err != nil || !bytes.Equal(b, a.History) {
		t.Fatalf("history.jsonl = %q, %v", b, err)
	}
	if !reflect.DeepEqual(a.Report.Files[2:4], []File{{Name: FileSchedule, Type: "schedule", Version: 1}, {Name: FileHistory, Type: "history"}}) {
		t.Fatalf("files = %+v", a.Report.Files)
	}
	got, err := Read(dir)
	if err != nil || !bytes.Equal(got.History, a.History) {
		t.Fatalf("Read history = %q, %v", got.History, err)
	}
}

// ART-012: a new parent is made like os.Mkdir(p, 0o755); the directory is exactly 0o755 and every
// file exactly 0o644, whatever the umask.
func TestWriteModes(t *testing.T) {
	root := t.TempDir()
	ref := filepath.Join(root, "ref")
	if err := os.Mkdir(ref, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(root, "new", "a"), fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	refInfo, err1 := os.Stat(ref)
	parent, err2 := os.Stat(filepath.Join(root, "new"))
	if err1 != nil || err2 != nil || parent.Mode().Perm() != refInfo.Mode().Perm() {
		t.Fatalf("parent mode %v, want %v (%v, %v)", parent.Mode().Perm(), refInfo.Mode().Perm(), err1, err2)
	}
	old, ok := setUmask(0o077)
	if !ok {
		t.Skip("no umask on this platform")
	}
	defer setUmask(old)
	dir := filepath.Join(root, "strict")
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode: %v, %v", fi.Mode().Perm(), err)
	}
	for name := range readAll(t, dir) {
		if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s mode: %v, %v", name, fi.Mode().Perm(), err)
		}
	}
}

// ART-012 and issue #58: an empty directory is refused; an invalid schedule creates nothing, not
// even a new parent; dir/report.json is checked with os.Lstat.
func TestWriteReplaceRules(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "e")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(empty, fixtureArtifact()); err == nil || err.Error() != "artifact: refusing to replace "+empty+": not a faultline artifact directory (no report.json)" {
		t.Fatalf("empty dir: %v", err)
	}
	bad := fixtureArtifact()
	bad.Schedule = &fault.Schedule{Version: 1, Events: []fault.Event{{At: kernel.Time(1)}}} // no kind: fails Validate
	if err := Write(filepath.Join(root, "new", "d"), bad); err == nil || !strings.HasPrefix(err.Error(), "artifact: write schedule.json: ") {
		t.Fatalf("invalid schedule: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatal("an invalid schedule created the parent")
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Mkdir(dangling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(dangling, FileReport)); err != nil {
		t.Skip(err)
	}
	if err := Write(dangling, fixtureArtifact()); err != nil {
		t.Fatalf("dangling report.json link: %v", err)
	}
}

// ART-012: a write error after the temporary directory exists (a 300-byte extra name passes
// ART-010's pattern, but no file system takes it) leaves the old artifact and no hidden sibling.
func TestWriteErrorKeepsOld(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a")
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, dir)
	a := fixtureArtifact()
	name := strings.Repeat("x", 300)
	a.Extra = map[string][]byte{name: []byte("x")}
	if err := Write(dir, a); err == nil || !strings.HasPrefix(err.Error(), "artifact: write "+name+": ") {
		t.Fatalf("Write(long extra name) = %v", err)
	}
	if after := readAll(t, dir); !reflect.DeepEqual(after, before) {
		t.Fatal("the failed Write changed the old artifact")
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".*")); len(left) != 0 {
		t.Fatalf("hidden siblings left: %v", left)
	}
}

// ART-012: the temporary directory is a sibling of dir, so the final rename never crosses file
// systems; os.TempDir is not used.
func TestWriteTempIsSibling(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(root, "missing"))
	if err := Write(filepath.Join(root, "a"), fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
}

// ART-012: Write removes what a killed Write of the same dir left: temporary copies, and the
// set-aside old copy of a kill between the two renames (dir then absent). Other names stay.
func TestWriteLeftovers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a")
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	// A kill between the renames: the old copy aside, the complete new one still temporary.
	if err := os.Rename(dir, filepath.Join(root, ".a.tmp-7.old")); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(root, "b"), fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "b"), filepath.Join(root, ".a.tmp-7")); err != nil {
		t.Fatal(err)
	}
	keep := []string{".a.tmp-", ".a.tmp-x", ".a.tmp-1.tmp-2", ".b.tmp-3", "a.tmp-4"}
	for _, name := range append([]string{".a.tmp-123"}, keep...) {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := slices.Sorted(slices.Values(append(keep, "a"))); !slices.Equal(names, want) {
		t.Fatalf("entries %v, want %v", names, want)
	}
}

// ART-012: an old directory that cannot be deleted completely no longer breaks the replace; it is
// set aside, the new artifact takes its place, and the next Write tries the leftover again.
func TestWriteOldNotDeletable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a")
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "minimized")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, FileReport), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "probe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil { // its file cannot be unlinked
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(locked, "probe")); err == nil {
		_ = os.Chmod(locked, 0o755)
		t.Skip("directory permissions are not enforced here (root, Windows or a FAT file system)")
	}
	t.Cleanup(func() { // so that t.TempDir's cleanup can delete it, wherever it is now
		aside, _ := filepath.Glob(filepath.Join(root, ".a.tmp-*.old", "minimized"))
		for _, p := range append(aside, locked) {
			_ = os.Chmod(p, 0o755)
		}
	})
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatalf("Write over an old directory with an undeletable entry: %v", err)
	}
	if _, err := Read(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "minimized")); !os.IsNotExist(err) {
		t.Fatal("the new artifact holds the old minimized/")
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".a.tmp-*.old", "minimized")); len(left) != 1 {
		t.Fatalf("set-aside copies: %v, want the one a later Write removes", left)
	}
}

// ART-012: a dir Write may not replace is refused before anything is written: no leftover of an
// earlier Write is removed and a is unchanged.
func TestWriteRefusesFirst(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(root, ".a.tmp-5")
	if err := os.Mkdir(leftover, 0o755); err != nil {
		t.Fatal(err)
	}
	a := fixtureArtifact()
	if err := Write(dir, a); err == nil || err.Error() != "artifact: refusing to replace "+dir+": not a faultline artifact directory (no report.json)" {
		t.Fatalf("Write(empty dir) = %v", err)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatal("the refused Write removed a leftover")
	}
	if a.Report.Dir != "" || a.Report.Files != nil {
		t.Fatalf("the refused Write changed a: %q %v", a.Report.Dir, a.Report.Files)
	}
}

// ART-012: a symbolic link at dir is refused before anything is written, whatever it points to,
// and is not followed: the link and its target stay as they were.
func TestWriteSymlinkDir(t *testing.T) {
	root := t.TempDir()
	art := filepath.Join(root, "art")
	if err := Write(art, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, art)
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{art, other} {
		link := filepath.Join(root, "link-"+filepath.Base(target))
		if err := os.Symlink(target, link); err != nil {
			t.Skip(err)
		}
		if err := Write(link, fixtureArtifact()); err == nil || err.Error() != "artifact: refusing to replace "+link+": it is a symbolic link" {
			t.Errorf("Write(link to %s) = %v", filepath.Base(target), err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is no longer a symbolic link: %v", link, err)
		}
	}
	if after := readAll(t, art); !reflect.DeepEqual(after, before) {
		t.Error("the linked artifact changed")
	}
	if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
		t.Errorf("the linked directory changed: %v, %v", entries, err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".*")); len(left) != 0 {
		t.Errorf("hidden siblings left: %v", left)
	}
}

// ART-012: an os.Lstat error for dir other than "does not exist" is returned before anything is
// written. A parent without search permission produces one, where permissions are enforced.
func TestWriteLstatError(t *testing.T) {
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	dir := filepath.Join(locked, "a")
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrPermission) {
		t.Skip("directory permissions are not enforced here (root, Windows or a FAT file system)")
	}
	if err := Write(dir, fixtureArtifact()); err == nil || !strings.HasPrefix(err.Error(), "artifact: lstat "+dir+": ") || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Write under a parent without search permission = %v", err)
	}
}

// otherTestErr is the error of ART-012 for a dir that holds the artifact of package
// example.com/toy, test TestA, seed 1.
func otherTestErr(dir string) string {
	return "artifact: refusing to replace " + dir + `: it holds the artifact of another test (package "example.com/toy", test "TestA", seed "0x0000000000000001")`
}

// AT-ART-22 (a) to (d): the artifact of another package, test or seed is refused before anything
// is written: the old directory, a leftover of an earlier Write and a stay as they were. The same
// package, test and seed replace it, whatever else the old report says, compared after normReport.
func TestWriteOtherTest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "0000000000000001")
	first := fixtureArtifact()
	first.Report.Test = "TestA"
	if err := Write(dir, first); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, dir)
	if err := os.Mkdir(filepath.Join(root, ".0000000000000001.tmp-5"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		mutate func(r *Report)
	}{
		{"test", func(r *Report) { r.Test = "Testa" }},
		{"package", func(r *Report) { r.Test, r.Package = "TestA", "example.com/toy2" }},
		{"seed", func(r *Report) { r.Test, r.Seed = "TestA", "0x0000000000000002" }},
		{"package case", func(r *Report) { r.Test, r.Package = "TestA", "Example.com/toy" }},
	} {
		a, want := fixtureArtifact(), fixtureArtifact()
		c.mutate(&a.Report)
		c.mutate(&want.Report)
		err := Write(dir, a)
		if err == nil || err.Error() != otherTestErr(dir) || !errors.Is(err, ErrOtherTest) {
			t.Errorf("another %s: %v", c.name, err)
		}
		if !reflect.DeepEqual(a, want) {
			t.Errorf("another %s: the refused Write changed a", c.name)
		}
	}
	if after := readAll(t, dir); !reflect.DeepEqual(after, before) {
		t.Error("a refused Write changed the old artifact")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{".0000000000000001.tmp-5", "0000000000000001"}; !slices.Equal(names, want) {
		t.Errorf("entries %v, want %v", names, want)
	}

	same := fixtureArtifact()
	same.Report.Test, same.Report.Status, same.Report.Failure = "TestA", "pass", nil
	same.Report.Subtest, same.Report.OptionsHash = "TestA/other", "0x0000000000000002"
	if err := Write(dir, same); err != nil {
		t.Fatalf("Write(same package, test and seed) = %v", err)
	}
	if got, err := Read(dir); err != nil || got.Report.Status != "pass" {
		t.Fatalf("Read = %+v, %v", got, err)
	}
	bad := filepath.Join(root, "bad")
	for range 2 {
		a := fixtureArtifact()
		a.Report.Test = "Test\xff"
		if err := Write(bad, a); err != nil {
			t.Fatalf("Write(invalid UTF-8 test name) = %v", err)
		}
	}
}

// AT-ART-22 (e), ART-012: a report.json that cannot be read (empty or cut short after a power loss,
// not a faultline report, a newer version, no read permission) or that is not a regular file (a
// link is not followed, a FIFO is not opened) names no test, so Write replaces its directory. Each
// row first shows that the intact report is refused. A FIFO that Write opened would block it until
// go test's -timeout stops the test binary with the stack of every goroutine.
func TestWriteUnreadableReport(t *testing.T) {
	root := t.TempDir()
	other := fixtureArtifact()
	other.Report.Test = "TestOther"
	linked := filepath.Join(root, "linked")
	if err := Write(linked, other); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"empty", "cut short", "not a faultline report", "newer version", "unreadable", "link", "fifo"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, strings.ReplaceAll(name, " ", "-"))
			if err := Write(dir, other); err != nil {
				t.Fatal(err)
			}
			if err := Write(dir, fixtureArtifact()); !errors.Is(err, ErrOtherTest) {
				t.Fatalf("intact report: %v", err)
			}
			p, b := filepath.Join(dir, FileReport), readAll(t, dir)[FileReport]
			var err error
			switch name {
			case "empty":
				err = os.WriteFile(p, nil, 0o600)
			case "cut short":
				err = os.WriteFile(p, b[:len(b)/2], 0o600)
			case "not a faultline report":
				err = os.WriteFile(p, bytes.Replace(b, []byte(`"faultline_report": 1`), []byte(`"faultline_report": 0`), 1), 0o600)
			case "newer version":
				err = os.WriteFile(p, bytes.Replace(b, []byte(`"faultline_report": 1`), []byte(`"faultline_report": 2`), 1), 0o600)
			case "unreadable":
				if err = os.Chmod(p, 0); err == nil {
					if _, rerr := os.ReadFile(p); rerr == nil {
						t.Skip("file permissions are not enforced here (root, Windows or a FAT file system)")
					}
				}
			case "link":
				if err = os.Remove(p); err == nil {
					if lerr := os.Symlink(filepath.Join(linked, FileReport), p); lerr != nil {
						t.Skip(lerr)
					}
				}
			case "fifo":
				if err = os.Remove(p); err == nil {
					if ok, ferr := mkfifo(p); !ok || ferr != nil {
						t.Skipf("no FIFO here: %v", ferr)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Write(dir, fixtureArtifact()); err != nil {
				t.Fatal(err)
			}
			if got, err := Read(dir); err != nil || got.Report.Test != "TestToy" {
				t.Fatalf("Read = %+v, %v", got, err)
			}
		})
	}
}

// AT-ART-22: on a case-insensitive file system (the default on macOS and Windows), names that
// differ only in case are one folder, and the second Write is refused.
func TestWriteCaseOnlyNames(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "X")); err != nil {
		t.Skip("the file system is case-sensitive")
	}
	a, b := fixtureArtifact(), fixtureArtifact()
	a.Report.Test, b.Report.Test = "TestA", "Testa"
	dirA, dirB := Dir(root, "example.com/toy", "TestA", 1), Dir(root, "example.com/toy", "Testa", 1)
	if err := Write(dirA, a); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, dirA)
	if err := Write(dirB, b); err == nil || err.Error() != otherTestErr(dirB) || !errors.Is(err, ErrOtherTest) {
		t.Fatalf("Write(%s) = %v", dirB, err)
	}
	if after := readAll(t, dirA); !reflect.DeepEqual(after, before) {
		t.Fatal("the refused Write changed the first artifact")
	}
}

// ART-012: the second check repeats the first, report check included, only when dir or its
// report.json is another file now: another inode, or for report.json another size or modification
// time. The functions are called directly: in Write, no caller code runs between the two checks.
func TestWriteSecondCheck(t *testing.T) {
	root := t.TempDir()
	rep := fixtureReport()
	rep.Test = "TestA"
	retest := func(b []byte, test string) []byte {
		return bytes.Replace(b, []byte(`"test": "TestA"`), []byte(`"test": "`+test+`"`), 1)
	}
	cases := []struct {
		name   string
		change func(dir, p string, b []byte, mtime time.Time) error
		want   string // the error after "<dir>: "; "": the first check's result stands
	}{
		{"same files, other content", func(_, p string, b []byte, mtime time.Time) error {
			if err := os.WriteFile(p, retest(b, "TestB"), 0o600); err != nil {
				return err
			}
			return os.Chtimes(p, mtime, mtime)
		}, ""},
		{"report.json larger", func(_, p string, b []byte, mtime time.Time) error {
			if err := os.WriteFile(p, retest(b, "TestOther"), 0o600); err != nil {
				return err
			}
			return os.Chtimes(p, mtime, mtime)
		}, "it holds the artifact of another test"},
		{"report.json smaller", func(_, p string, b []byte, mtime time.Time) error {
			if err := os.WriteFile(p, retest(b, "T"), 0o600); err != nil {
				return err
			}
			return os.Chtimes(p, mtime, mtime)
		}, "it holds the artifact of another test"},
		{"report.json modification time later", func(_, p string, b []byte, mtime time.Time) error {
			if err := os.WriteFile(p, retest(b, "TestB"), 0o600); err != nil {
				return err
			}
			return os.Chtimes(p, mtime, mtime.Add(time.Second))
		}, "it holds the artifact of another test"},
		{"report.json modification time earlier", func(_, p string, b []byte, mtime time.Time) error {
			if err := os.WriteFile(p, retest(b, "TestB"), 0o600); err != nil {
				return err
			}
			return os.Chtimes(p, mtime, mtime.Add(-time.Second))
		}, "it holds the artifact of another test"},
		{"report.json replaced", func(_, p string, b []byte, mtime time.Time) error {
			if err := os.WriteFile(p+".new", retest(b, "TestB"), 0o600); err != nil {
				return err
			}
			if err := os.Chtimes(p+".new", mtime, mtime); err != nil {
				return err
			}
			return os.Rename(p+".new", p)
		}, "it holds the artifact of another test"},
		{"dir replaced", func(dir, _ string, _ []byte, _ time.Time) error {
			other := fixtureArtifact()
			other.Report.Test = "TestB"
			if err := Write(dir+"-other", other); err != nil {
				return err
			}
			if err := os.Rename(dir, dir+"-aside"); err != nil {
				return err
			}
			return os.Rename(dir+"-other", dir)
		}, "it holds the artifact of another test"},
		{"dir now a link to itself", func(dir, _ string, _ []byte, _ time.Time) error {
			if err := os.Rename(dir, dir+"-aside"); err != nil {
				return err
			}
			return os.Symlink(dir+"-aside", dir)
		}, "it is a symbolic link"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := fixtureArtifact()
			a.Report = rep
			dir := filepath.Join(root, strings.ReplaceAll(c.name, " ", "-"))
			if err := Write(dir, a); err != nil {
				t.Fatal(err)
			}
			seen, err := checkTarget(dir, &rep)
			if err != nil || seen.dir == nil || seen.report == nil {
				t.Fatalf("checkTarget = %+v, %v", seen, err)
			}
			p := filepath.Join(dir, FileReport)
			if err := c.change(dir, p, readAll(t, dir)[FileReport], seen.report.ModTime()); err != nil {
				if c.want == "it is a symbolic link" {
					t.Skip(err)
				}
				t.Fatal(err)
			}
			got, err := recheckTarget(dir, &rep, seen)
			if c.want == "" && (err != nil || got.dir == nil) {
				t.Fatalf("recheckTarget = %+v, %v, want the first check's result", got, err)
			}
			if c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), "artifact: refusing to replace "+dir+": "+c.want)) {
				t.Fatalf("recheckTarget = %v, want %q", err, c.want)
			}
		})
	}

	// dir absent in the first check: absent again, still nothing to replace, at no more cost than
	// the first check (one os.Lstat); a directory or another test's artifact that appeared is
	// checked in full.
	dir := filepath.Join(root, "new")
	seen, err := checkTarget(dir, &rep)
	if err != nil || seen.dir != nil {
		t.Fatalf("checkTarget(absent) = %+v, %v", seen, err)
	}
	if got, err := recheckTarget(dir, &rep, seen); err != nil || got.dir != nil {
		t.Fatalf("recheckTarget(still absent) = %+v, %v", got, err)
	}
	first := testing.AllocsPerRun(10, func() { _, _ = checkTarget(dir, &rep) })
	if second := testing.AllocsPerRun(10, func() { _, _ = recheckTarget(dir, &rep, seen) }); second > first {
		t.Errorf("second check of a new dir: %v allocations, first check %v", second, first)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := recheckTarget(dir, &rep, seen); err == nil || err.Error() != "artifact: refusing to replace "+dir+": not a faultline artifact directory (no report.json)" {
		t.Fatalf("recheckTarget(appeared) = %v", err)
	}
	other := fixtureArtifact()
	other.Report.Test = "TestB"
	dir = filepath.Join(root, "new-other")
	if err := Write(dir, other); err != nil {
		t.Fatal(err)
	}
	if _, err := recheckTarget(dir, &rep, seen); !errors.Is(err, ErrOtherTest) {
		t.Fatalf("recheckTarget(another test's artifact appeared) = %v", err)
	}

	// A dir holding only stall.txt has no report.json to compare, so it is checked in full again:
	// an entry that appeared during the writes is refused.
	stall := filepath.Join(root, "stall")
	if err := os.Mkdir(stall, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stall, "stall.txt"), []byte("stall"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen, err = checkTarget(stall, &rep)
	if err != nil || seen.dir == nil || seen.report != nil {
		t.Fatalf("checkTarget(stall.txt only) = %+v, %v", seen, err)
	}
	if got, err := recheckTarget(stall, &rep, seen); err != nil || got.dir == nil {
		t.Fatalf("recheckTarget(stall.txt only) = %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(stall, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recheckTarget(stall, &rep, seen); err == nil || err.Error() != "artifact: refusing to replace "+stall+": not a faultline artifact directory (no report.json)" {
		t.Fatalf("recheckTarget(stall.txt and x.txt) = %v", err)
	}
}

// ART-003: Dir plus the FNV-1a 64 hash of the package and test names, so two names that share a
// Dir get different AltDirs.
func TestAltDir(t *testing.T) {
	if got, want := AltDir("/r", "github.com/acme/kv", "TestKV/a b", 0x5e1f9a2c4b7d3e80), filepath.FromSlash("/r/github.com_acme_kv/TestKV__a_b/5e1f9a2c4b7d3e80-1d148d2edfb479eb"); got != want {
		t.Errorf("AltDir = %q, want %q", got, want)
	}
	if got, want := AltDir("/r", "p", "TestKV", 1), filepath.FromSlash("/r/p/TestKV/0000000000000001-0286916e368eed7c"); got != want {
		t.Errorf("AltDir = %q, want %q (16 hex digits, a leading zero kept)", got, want)
	}
	for _, pair := range [][2][2]string{
		{{"a/b", "TestX"}, {"a_b", "TestX"}},
		{{"p", "TestX/a:b"}, {"p", "TestX/a*b"}},
		{{"p", "TestKV/Raft"}, {"p", "TestKV/raft"}}, // one Dir on a case-insensitive file system
	} {
		x, y := pair[0], pair[1]
		if !strings.EqualFold(Dir("/r", x[0], x[1], 1), Dir("/r", y[0], y[1], 1)) {
			t.Fatalf("%q and %q do not share a Dir", x, y)
		}
		if AltDir("/r", x[0], x[1], 1) == AltDir("/r", y[0], y[1], 1) {
			t.Errorf("%q and %q share an AltDir", x, y)
		}
	}
}

// ART-085: an invalid report, and errors in present files, are returned as artifact: <dir>: <reason>,
// naming the file once and wrapping the cause. A file that cannot be read, such as a directory in
// its place, gives the operation and the *fs.PathError's inner error.
func TestReadErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	orig := readAll(t, dir)
	for _, c := range []struct {
		file    string
		content []byte // nil: a directory in place of the file
		want    string // the start of the reason; for a directory, set below to the whole reason
	}{
		{FileReport, []byte("{}"), "report.json: not a faultline report"},
		{FileTrace, []byte("{\"faultline_trace\":2}\n"), "trace.jsonl line 1: version 2 is newer"},
		{FileSchedule, []byte("nope"), "schedule.json: fault: schedule: "},
		{FileSchedule, []byte{}, "schedule.json: fault: schedule: "}, // empty, so present and invalid
		{FileReport, nil, ""},
		{FileTrace, nil, ""},
		{FileSchedule, nil, ""},
		{FileHistory, nil, ""},
		{FileReportText, nil, ""},
	} {
		p := filepath.Join(dir, c.file)
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		var cause error
		if c.content != nil {
			err := os.WriteFile(p, c.content, 0o600)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
			// What reading a directory gives here: on Unix, "read" and "is a directory".
			var pe *fs.PathError
			if _, err := os.ReadFile(p); !errors.As(err, &pe) {
				t.Fatalf("reading a directory: %v", err)
			}
			c.want, cause = pe.Op+" "+c.file+": "+pe.Err.Error(), pe.Err
		}
		switch _, err := Read(dir); {
		case err == nil || !strings.HasPrefix(err.Error(), "artifact: "+dir+": "+c.want):
			t.Errorf("%s: %v, want the reason %s", c.file, err, c.want)
		case strings.Count(err.Error(), dir) != 1:
			t.Errorf("%s: the path is repeated: %v", c.file, err)
		case errors.Unwrap(err) == nil:
			t.Errorf("%s: %v wraps no error", c.file, err)
		case cause != nil && (err.Error() != "artifact: "+dir+": "+c.want || !errors.Is(err, cause)):
			t.Errorf("%s: %v, want the reason %s wrapping %v", c.file, err, c.want, cause)
		}
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if b, ok := orig[c.file]; ok {
			if err := os.WriteFile(p, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func BenchmarkWrite1M(b *testing.B) {
	a := fixtureArtifact()
	a.Trace.Records = make([]kernel.Record, 1_000_000)
	for i := range a.Trace.Records {
		a.Trace.Records[i] = kernel.Record{Seq: uint64(i + 1), At: kernel.Time(i * 1000), Node: 1, Inc: 1, Kind: "net.send", Cause: uint64(i), Text: "send", Attrs: attrs("msg", "1", "from", "n1", "to", "n2")}
	}
	a.Report.Failure.RecordSeq = 1_000_000
	dir := filepath.Join(b.TempDir(), "a")
	b.ResetTimer()
	for b.Loop() {
		if err := Write(dir, a); err != nil {
			b.Fatal(err)
		}
	}
}

// ART-085: a trace.jsonl that cannot be opened gives "open trace.jsonl: <inner error>", wrapping
// the cause. A symbolic link to itself cannot be opened, also by root.
func TestReadTraceOpenError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	if err := Write(dir, fixtureArtifact()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, FileTrace)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(FileTrace, p); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	f, err := os.Open(p)
	if err == nil {
		f.Close()
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) || errors.Is(err, fs.ErrNotExist) {
		t.Skipf("opening a link to itself: %v", err)
	}
	want := "artifact: " + dir + ": open trace.jsonl: " + pe.Err.Error()
	if _, err := Read(dir); err == nil || err.Error() != want || !errors.Is(err, pe.Err) {
		t.Errorf("Read = %v, want %s", err, want)
	}
}
