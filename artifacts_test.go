package faultline_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/artifact"
)

var _ = scenario("pingpong-full", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) { addPingPong(w) })
})

// AT-API-34; API-073 (one log call, the primary attempt's event count, no pass artifact with
// FAULTLINE_ARTIFACTS=off, write errors through st.Errorf) and API-087 (artifact, artifact_error).
func TestRunPassArtifacts(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "pingpong-full", []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	dir := seedDir(root, seedK(0))
	// One st.Log call: the artifacts line has no file:line prefix of its own.
	logged := regexp.MustCompile(`faultline: seed 0x9a33dad17ee0bb7d passed: (\d+) events, ended at t=1\.000000000s \(deadline\)\n\s+artifacts: ` + regexp.QuoteMeta(dir+string(os.PathSeparator)) + "\n").FindStringSubmatch(o)
	if code != 0 || logged == nil {
		t.Fatalf("exit %d\n%s", code, o)
	}
	rep := readReport(t, dir)
	if rep.Status != "pass" || rep.Failure != nil || len(rep.Warnings) != 0 || logged[1] != strconv.FormatUint(rep.Run.Events, 10) {
		t.Fatalf("report %+v, logged %s events", rep, logged[1])
	}
	text, err := os.ReadFile(filepath.Join(dir, "report.txt"))
	if err != nil || !strings.HasPrefix(string(text), "--- PASS: TestScenario/seed=0x9a33dad17ee0bb7d\n    faultline: seed 0x9a33dad17ee0bb7d passed: ") {
		t.Fatalf("report.txt %q %v", text, err)
	}
	if lines := resultLines(t, results); len(lines) != 1 || lines[0]["status"] != "pass" || lines[0]["artifact"] != dir {
		t.Fatalf("results %v", lines)
	}

	o, code = runScenario(t, "pingpong-full", []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=off"})
	if code != 0 || strings.Contains(o, "artifacts:") || strings.Contains(o, "writing artifacts") {
		t.Fatalf("off: exit %d\n%s", code, o)
	}

	// A root that is a regular file: the write fails, and so does the seed.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	results = filepath.Join(t.TempDir(), "r2.jsonl")
	o, code = runScenario(t, "pingpong-full", []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + file, "FAULTLINE_RESULTS=" + results})
	lines := resultLines(t, results)
	if code != 1 || !strings.Contains(o, "faultline: writing artifacts: ") || len(lines) != 1 || lines[0]["status"] != "pass" || lines[0]["artifact_error"] == nil || lines[0]["artifact"] != nil {
		t.Fatalf("write error: exit %d results %v\n%s", code, lines, o)
	}
}

var _ = scenario("fails-for-0x1", func(t *testing.T) {
	fixed := os.Getenv("FIXED") == "1" // seed 1 passes too, as after a fix
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Invariant("not seed 1", func() error {
			if w.Seed() == 1 && !fixed {
				return errors.New("seed 1")
			}
			return nil
		})
	})
})

// writeOther writes, in dir, a pass artifact of test in package pkg for seed: the artifact of
// another test whose name maps to the same folder (ART-001).
func writeOther(t *testing.T, dir, pkg, test string, seed uint64) {
	t.Helper()
	a := &artifact.Artifact{
		Report: artifact.Report{Package: pkg, Test: test, Seed: fmt.Sprintf("0x%016x", seed), Status: "pass"},
		Trace:  &artifact.Trace{},
	}
	if err := artifact.Write(dir, a); err != nil {
		t.Fatal(err)
	}
}

// AT-API-44; API-083 (the AltDir report is the previous report, also for the pass note) and
// API-074 (the pass report.txt names the AltDir).
func TestRunArtifactCollision(t *testing.T) {
	root := t.TempDir()
	const pkg = "github.com/hmdsefi/faultline"
	kept := func(dir string) {
		t.Helper()
		if rep := readReport(t, dir); rep.Test != "Testscenario" {
			t.Fatalf("the other test's artifact was replaced: %+v", rep)
		}
	}

	// A failing seed, twice: the second run compares with the AltDir report, not the other test's.
	// The other test's name differs only in case: on a case-insensitive file system it shares
	// TestScenario's folders.
	dir, alt := seedDir(root, 1), artifact.AltDir(root, pkg, "TestScenario", 1)
	writeOther(t, dir, pkg, "Testscenario", 1)
	env := []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root}
	for run := range 2 {
		o, code := runScenario(t, "fails-for-0x1", env)
		if code != 1 || !strings.Contains(o, "artifacts: "+alt+string(os.PathSeparator)) || strings.Contains(o, "warning:") {
			t.Fatalf("run %d: exit %d\n%s", run+1, code, o)
		}
		kept(dir)
		if rep := readReport(t, alt); rep.Test != "TestScenario" || rep.Status != "fail" {
			t.Fatalf("run %d: report %+v", run+1, rep)
		}
	}
	// Once the AltDir report records another faultline, the fixed seed's pass note names the AltDir
	// and warns.
	editReport(t, alt, "v0.0.0-test", "")
	o, code := runScenario(t, "fails-for-0x1", append(env, "FIXED=1"))
	note := "faultline: seed 0x0000000000000001 passed; the previous artifact at " + alt + " recorded invariant:not seed 1"
	if code != 0 || !regexp.MustCompile(regexp.QuoteMeta(note)+`\n\s+warning: previous artifact was recorded with faultline v0\.0\.0-test; this run uses `).MatchString(o) {
		t.Fatalf("fixed run: exit %d\n%s", code, o)
	}
	kept(dir)

	// A passing seed with full traces still passes, and its report.txt names the AltDir.
	dir, alt = seedDir(root, seedK(0)), artifact.AltDir(root, pkg, "TestScenario", seedK(0))
	writeOther(t, dir, pkg, "Testscenario", seedK(0))
	o, code = runScenario(t, "pingpong-full", []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + root})
	if code != 0 || !strings.Contains(o, "artifacts: "+alt+string(os.PathSeparator)) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	kept(dir)
	if rep := readReport(t, alt); rep.Status != "pass" {
		t.Fatalf("report %+v", rep)
	}
	text, err := os.ReadFile(filepath.Join(alt, "report.txt"))
	if err != nil || !strings.Contains(string(text), "\n    artifacts: "+alt+string(os.PathSeparator)+"\n") {
		t.Fatalf("report.txt %q %v", text, err)
	}
}

// editReport rewrites report.json in dir as if another faultline version or other options had
// recorded it: it sets versions.faultline and options_hash to the values that are not empty.
func editReport(t *testing.T, dir, faultlineVersion, optionsHash string) {
	t.Helper()
	path := filepath.Join(dir, "report.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	vers, ok := m["versions"].(map[string]any)
	if !ok {
		t.Fatalf("versions %v", m["versions"])
	}
	if faultlineVersion != "" {
		vers["faultline"] = faultlineVersion
	}
	if optionsHash != "" {
		m["options_hash"] = optionsHash
	}
	b, _ = json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// AT-API-35
func TestRunReplayWarnings(t *testing.T) {
	root := t.TempDir()
	env := []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root}
	if o, code := runScenario(t, "fails-for-0x1", env); code != 1 {
		t.Fatalf("first run: exit %d\n%s", code, o)
	}
	editReport(t, seedDir(root, 1), "v0.0.0-test", "0x0000000000000000")
	o, code := runScenario(t, "fails-for-0x1", env)
	if code != 1 || !strings.Contains(o, "warning: previous artifact was recorded with faultline v0.0.0-test; this run uses ") ||
		!strings.Contains(o, "warning: options differ from the previous artifact (options hash 0x0000000000000000, now 0x") {
		t.Fatalf("second run: exit %d\n%s", code, o)
	}
	rep := readReport(t, seedDir(root, 1))
	if len(rep.Warnings) != 2 {
		t.Fatalf("warnings %q", rep.Warnings)
	}
}

// API-083: which previous report a run compares with, and the pass note.
func TestRunPreviousReport(t *testing.T) {
	const pkg = "github.com/hmdsefi/faultline"
	// noWarning runs the failing seed 1 and expects no warning: nothing was compared.
	noWarning := func(name string, env []string) {
		t.Helper()
		o, code := runScenario(t, "fails-for-0x1", env)
		if code != 1 || !strings.Contains(o, "--- FAIL: TestScenario/seed=0x0000000000000001") || strings.Contains(o, "warning:") {
			t.Fatalf("%s: exit %d\n%s", name, code, o)
		}
	}

	// Only FAULTLINE_SEED compares; FAULTLINE_SEED_LIST does not.
	root := t.TempDir()
	dir := seedDir(root, 1)
	env := []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root}
	noWarning("first run", env)
	editReport(t, dir, "v0.0.0-test", "")
	noWarning("FAULTLINE_SEED_LIST", []string{"FAULTLINE_SEED_LIST=0x1", "FAULTLINE_ARTIFACTS=" + root})

	// A pass after a failing report logs the note and one warning line per difference; a pass after
	// a passing report (the pass artifact of FAULTLINE_TRACE=full) logs no note.
	editReport(t, dir, "", "0x0000000000000000")
	o, code := runScenario(t, "fails-for-0x1", append(env, "FIXED=1", "FAULTLINE_TRACE=full"))
	note := "faultline: seed 0x0000000000000001 passed; the previous artifact at " + dir + " recorded invariant:not seed 1"
	if code != 0 || !regexp.MustCompile(regexp.QuoteMeta(note)+`\n\s+warning: options differ from the previous artifact \(options hash 0x0000000000000000, now 0x`).MatchString(o) {
		t.Fatalf("pass note: exit %d\n%s", code, o)
	}
	o, code = runScenario(t, "fails-for-0x1", append(env, "FIXED=1"))
	if code != 0 || strings.Contains(o, "passed; the previous artifact") {
		t.Fatalf("after a pass: exit %d\n%s", code, o)
	}

	// A missing Dir report is skipped: the AltDir report counts only after another test's report.
	root = t.TempDir()
	dir, alt := seedDir(root, 1), artifact.AltDir(root, pkg, "TestScenario", 1)
	writeOther(t, dir, pkg, "Testscenario", 1)
	env = []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root}
	noWarning("into the AltDir", env)
	editReport(t, alt, "v0.0.0-test", "")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	noWarning("no Dir report", env)

	// The Dir report of another package or seed is not compared. github.com_hmdsefi/faultline maps
	// to this package's folder, as github.com/a_b and github.com/a/b do.
	for _, other := range []struct {
		pkg  string
		seed uint64
	}{{"github.com_hmdsefi/faultline", 1}, {pkg, 2}} {
		root = t.TempDir()
		writeOther(t, seedDir(root, 1), other.pkg, "TestScenario", other.seed)
		noWarning(fmt.Sprintf("%s seed %d", other.pkg, other.seed), []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root})
	}
}

// filesErr is an error that adds artifact files (API-077).
type filesErr map[string]string

func (e filesErr) Error() string { return "files" }
func (e filesErr) ArtifactFiles() map[string][]byte {
	m := map[string][]byte{}
	for k, v := range e {
		m[k] = []byte(v)
	}
	return m
}

var _ = scenario("extra-files", func(t *testing.T) {
	row := os.Getenv("ROW")
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		switch row {
		case "a":
			w.Invariant("files", func() error { return filesErr{"a.json": "A"} })
		case "b":
			w.Final("f1", func() error { return filesErr{"x.json": "1", "y.txt": "Y"} })
			w.Final("f2", func() error { return filesErr{"x.json": "2"} })
		case "c":
			w.Final("bad", func() error { return filesErr{"Bad/Name": "B"} })
		}
	})
})

// AT-API-42
func TestRunExtraFiles(t *testing.T) {
	read := func(dir, name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	root := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "extra-files", []string{"ROW=a", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	dir := seedDir(root, 1)
	if code != 1 || read(dir, "a.json") != "A" {
		t.Fatalf("(a) exit %d\n%s", code, o)
	}
	rep := readReport(t, dir)
	if !slices.ContainsFunc(rep.Files, func(f artifact.File) bool { return f.Name == "a.json" && f.Type == "extra" }) {
		t.Fatalf("(a) files %+v", rep.Files)
	}

	root = t.TempDir()
	o, code = runScenario(t, "extra-files", []string{"ROW=b", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	dir = seedDir(root, 1)
	warning := `extra artifact file "x.json" from final check "f2" dropped: the name is already used`
	if code != 1 || read(dir, "x.json") != "1" || read(dir, "y.txt") != "Y" || !strings.Contains(o, "warning: "+warning) {
		t.Fatalf("(b) exit %d\n%s", code, o)
	}
	if rep := readReport(t, dir); !slices.Contains(rep.Warnings, warning) {
		t.Fatalf("(b) warnings %q", rep.Warnings)
	}

	root = t.TempDir()
	results = filepath.Join(t.TempDir(), "r3.jsonl")
	o, code = runScenario(t, "extra-files", []string{"ROW=c", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	lines := resultLines(t, results)
	if code != 1 || !strings.Contains(o, `artifacts: not written: artifact: invalid extra file name "Bad/Name"`) || len(lines) != 1 || lines[0]["artifact_error"] == nil || lines[0]["artifact"] != nil {
		t.Fatalf("(c) exit %d results %v\n%s", code, lines, o)
	}
}
