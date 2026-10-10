// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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

// AT-API-34; API-073 (two lines on st.Output, the primary attempt's event count, no pass artifact
// with FAULTLINE_ARTIFACTS=off, write errors through st.Errorf) and API-087 (artifact,
// artifact_error).
func TestRunPassArtifacts(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "pingpong-full", []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	dir := seedDir(root, seedK(0))
	// st.Output: testing indents each line by four spaces and adds no file:line prefix.
	logged := regexp.MustCompile(`\n    faultline: seed 0x9a33dad17ee0bb7d passed: (\d+) events, ended at t=1\.000000000s \(deadline\)\n    artifacts: ` + regexp.QuoteMeta(dir+string(os.PathSeparator)) + "\n").FindStringSubmatch(o)
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
	if code != 0 || !regexp.MustCompile(`\n    `+regexp.QuoteMeta(keptNote(alt, "invariant:not seed 1"))+`\n    warning: previous artifact was recorded with faultline v0\.0\.0-test; this run uses `).MatchString(o) {
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
	editReportJSON(t, dir, func(m map[string]any) {
		vers := object(t, m, "versions")
		if faultlineVersion != "" {
			vers["faultline"] = faultlineVersion
		}
		if optionsHash != "" {
			m["options_hash"] = optionsHash
		}
	})
}

// object returns the JSON object under key in m.
func object(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	o, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("report.json %s is %v, not an object", key, m[key])
	}
	return o
}

// editReportJSON rewrites report.json in dir with edit applied to its JSON object.
func editReportJSON(t *testing.T, dir string, edit func(m map[string]any)) {
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
	edit(m)
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

	// API-083: the options warning names the option that changed, and text from the old
	// report.json that holds a control character is quoted.
	editReportJSON(t, seedDir(root, 1), func(m map[string]any) {
		object(t, m, "options")["max_events"] = 5
		m["options_hash"] = "0x0000000000000000"
		object(t, m, "versions")["faultline"] = "v1\x1b[2J"
	})
	o, code = runScenario(t, "fails-for-0x1", env)
	if code != 1 || !strings.Contains(o, "\n    warning: previous artifact was recorded with faultline \"v1\\x1b[2J\"; this run uses ") ||
		!strings.Contains(o, "\n    warning: options differ from the previous artifact: Options.MaxEvents was 5, now 10000000 (options hash 0x0000000000000000, now 0x") || strings.Contains(o, "\x1b") {
		t.Fatalf("third run: exit %d\n%s", code, o)
	}
}

// seedOutput matches the whole output of seed 1 of a verbose run: the lines, each indented by
// testing alone (st.Output adds no file:line prefix), between its === RUN line and the next marker.
// Each line is a regular expression.
func seedOutput(lines ...string) *regexp.Regexp {
	return regexp.MustCompile(`\n=== RUN   TestScenario/seed=0x0000000000000001\n    ` + strings.Join(lines, `\n    `) + `\n(?:---|===) `)
}

// keptNote is the pass note of a seed that passed over a failing artifact in dir (API-083).
func keptNote(dir, signature string) string {
	return "faultline: seed 0x0000000000000001 passed; kept the failing artifact at " + dir + string(os.PathSeparator) + ", which recorded " + signature
}

// AT-API-47; API-073, API-083: a passing replay with FAULTLINE_TRACE=full keeps the failing
// artifact. The seed's whole output is the pass note, one line that names the directory the
// report was read from, which is not the one recorded in it after the artifact root moved.
func TestRunReplayKeepsFailingArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if o, code := runScenario(t, "fails-for-0x1", []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root}); code != 1 {
		t.Fatalf("failing run: exit %d\n%s", code, o)
	}
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	dir := seedDir(moved, 1)
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "fails-for-0x1", []string{"FAULTLINE_SEED=0x1", "FIXED=1", "FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + moved, "FAULTLINE_RESULTS=" + results})
	if code != 0 || !seedOutput(regexp.QuoteMeta(keptNote(dir, "invariant:not seed 1"))).MatchString(o) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if rep := readReport(t, dir); rep.Status != "fail" || rep.Failure == nil || rep.Failure.Signature != "invariant:not seed 1" {
		t.Fatalf("the failing artifact was replaced: %+v", rep)
	}
	if lines := resultLines(t, results); len(lines) != 1 || lines[0]["status"] != "pass" || lines[0]["artifact"] != nil {
		t.Fatalf("results %v", lines)
	}
}

// API-073, API-083: a report that has status fail but no failure object (damaged, or edited by
// hand) still keeps its artifact under FAULTLINE_TRACE=full. The pass note then has neither the
// signature nor warnings, and a replay without FAULTLINE_TRACE=full prints no note.
func TestRunKeptWithoutFailure(t *testing.T) {
	root := t.TempDir()
	dir := seedDir(root, 1)
	env := []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root}
	if o, code := runScenario(t, "fails-for-0x1", env); code != 1 {
		t.Fatalf("failing run: exit %d\n%s", code, o)
	}
	path := filepath.Join(dir, "report.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "failure")
	object(t, m, "versions")["faultline"] = "v0.0.0-test" // a difference whose warning the note leaves out
	if b, err = json.MarshalIndent(m, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	env = append(env, "FIXED=1")
	note := "faultline: seed 0x0000000000000001 passed; kept the failing artifact at " + dir + string(os.PathSeparator)
	if o, code := runScenario(t, "fails-for-0x1", append(env, "FAULTLINE_TRACE=full")); code != 0 || !seedOutput(regexp.QuoteMeta(note)).MatchString(o) {
		t.Fatalf("with full traces: exit %d\n%s", code, o)
	}
	if o, code := runScenario(t, "fails-for-0x1", env); code != 0 || strings.Contains(o, "faultline: seed ") {
		t.Fatalf("without full traces: exit %d\n%s", code, o)
	}
	if rep := readReport(t, dir); rep.Status != "fail" {
		t.Fatalf("the failing artifact was replaced: %+v", rep)
	}
}

// AT-API-52; API-073: under FAULTLINE_TRACE=full a pass artifact replaces only a pass artifact, in
// a plain run and in a replay. A failing report, or a report.json this version cannot read, keeps
// its directory unchanged, and the seed's whole output is one line that says why.
func TestRunPassKeepsArtifacts(t *testing.T) {
	// escKeyReport is a hand-made report.json whose decoding error names a map key that holds an
	// ESC byte. Only Go 1.27 and later put map keys in that error: on Go 1.26 the reason holds no
	// control character and the line prints it as it is.
	const escKeyReport = `{"faultline_report": 1, "replay": {"env": {"\u001b[2J": 1}}}`
	const pkg = "github.com/hmdsefi/faultline"
	seed := fmt.Sprintf("0x%016x", seedK(0)) // pingpong-full's one seed
	writeReport := func(content string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			t.Helper()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// ownStatus is a report of this test and seed whose status is the JSON value status.
	ownStatus := func(status string) func(t *testing.T, dir string) {
		return writeReport(`{"faultline_report": 1, "package": "` + pkg + `", "test": "TestScenario", "seed": "` + seed + `", "status": ` + status + `}`)
	}
	cases := []struct {
		name   string
		setup  func(t *testing.T, dir string)
		why    string // "" for a failing report
		replay bool
	}{
		{"failing report", func(t *testing.T, dir string) {
			t.Helper()
			a := &artifact.Artifact{
				Report: artifact.Report{Package: pkg, Test: "TestScenario", Seed: seed, Status: "fail", Failure: &artifact.Failure{Kind: "invariant", Check: "x", Signature: "invariant:x"}},
				Trace:  &artifact.Trace{},
			}
			if err := artifact.Write(dir, a); err != nil {
				t.Fatal(err)
			}
		}, "", false},
		{"newer report version", writeReport(`{"faultline_report": 99}`), "version 99 is newer than this faultline supports (1); upgrade faultline", false},
		{"newer report version, replay", writeReport(`{"faultline_report": 99}`), "version 99 is newer than this faultline supports (1); upgrade faultline", true},
		{"not a report", writeReport("{not json"), "not a faultline report", false},
		{"damaged report", writeReport(`{"faultline_report": 1, "status": 5}`), "json: cannot unmarshal number into Go struct field Report.status of type string", true},
		{"unknown status", ownStatus(`"ok\u001b"`), `status "ok\x1b" is neither pass nor fail`, false},
		{"empty status", ownStatus(`""`), `status "" is neither pass nor fail`, false},
		{"status in capitals", ownStatus(`"PASS"`), `status "PASS" is neither pass nor fail`, false},
		{"control character in the reason", writeReport(escKeyReport), readReason(t, escKeyReport), false},
		{"control character in the reason, replay", writeReport(escKeyReport), readReason(t, escKeyReport), true},
		{"not a regular file", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(dir, "report.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file", false},
	}
	for _, c := range cases {
		root := t.TempDir()
		dir := seedDir(root, seedK(0))
		c.setup(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := dirFiles(t, dir)
		env := []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + root}
		if c.replay {
			env = append(env, "FAULTLINE_SEED="+seed)
		}
		o, code, lines := runResults(t, "pingpong-full", env...)
		note := "faultline: seed " + seed + " passed; kept the failing artifact at " + dir + string(os.PathSeparator) + ", which recorded invariant:x"
		if c.why != "" {
			note = "faultline: seed " + seed + " passed; kept the artifact at " + dir + string(os.PathSeparator) + ", whose report.json could not be read: " + c.why
		}
		out := regexp.MustCompile(`\n=== RUN   TestScenario/seed=` + seed + `\n    ` + regexp.QuoteMeta(note) + `\n(?:---|===) `)
		if code != 0 || !out.MatchString(o) || len(lines) != 1 || lines[0]["status"] != "pass" || lines[0]["artifact"] != nil || strings.Contains(o, "\x1b") {
			t.Errorf("%s: exit %d, results %v\n%s", c.name, code, lines, o)
			continue
		}
		if after := dirFiles(t, dir); !maps.Equal(after, before) {
			t.Errorf("%s: the directory changed:\n%q\nwant\n%q", c.name, after, before)
		}
	}
}

// readReason is the reason that the kept line of API-073 gives for a report.json that
// artifact.ReadReport rejects: its error without the prefix, in quotes when it holds a character
// that is not printable (API-083).
func readReason(t *testing.T, report string) string {
	t.Helper()
	_, err := artifact.ReadReport(strings.NewReader(report))
	if err == nil {
		t.Fatalf("ReadReport accepted %q", report)
	}
	reason := strings.TrimPrefix(err.Error(), "artifact: report.json: ")
	if strings.IndexFunc(reason, func(r rune) bool { return !strconv.IsPrint(r) }) >= 0 {
		return strconv.Quote(reason)
	}
	return reason
}

// dirFiles returns the regular files under dir by their slash-separated path relative to dir,
// with their content.
func dirFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	fsys := os.DirFS(dir)
	files := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		files[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
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

	// A pass after a failing report logs the note and one warning line per difference, and nothing
	// else: without FAULTLINE_TRACE=full there is no pass log. A pass after a passing report (the
	// pass artifact of FAULTLINE_TRACE=full) logs no note.
	editReport(t, dir, "", "0x0000000000000000")
	o, code := runScenario(t, "fails-for-0x1", append(env, "FIXED=1"))
	warning := `warning: options differ from the previous artifact \(options hash 0x0000000000000000, now 0x[0-9a-f]{16}\)`
	if code != 0 || !seedOutput(regexp.QuoteMeta(keptNote(dir, "invariant:not seed 1")), warning).MatchString(o) {
		t.Fatalf("pass note: exit %d\n%s", code, o)
	}
	passRoot := t.TempDir()
	passDir := seedDir(passRoot, 1)
	passed := []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + passRoot, "FIXED=1"}
	if o, code := runScenario(t, "fails-for-0x1", append(passed, "FAULTLINE_TRACE=full")); code != 0 || !strings.Contains(o, "\n    artifacts: "+passDir+string(os.PathSeparator)+"\n") {
		t.Fatalf("pass artifact: exit %d\n%s", code, o)
	}
	o, code = runScenario(t, "fails-for-0x1", passed)
	if code != 0 || strings.Contains(o, "kept the failing artifact") {
		t.Fatalf("after a pass: exit %d\n%s", code, o)
	}
	// A pass artifact is not a failing artifact: another full-trace run replaces it.
	editReport(t, passDir, "v0.0.0-test", "")
	o, code = runScenario(t, "fails-for-0x1", append(passed, "FAULTLINE_TRACE=full"))
	if code != 0 || strings.Contains(o, "kept the failing artifact") || !strings.Contains(o, "\n    artifacts: "+passDir+string(os.PathSeparator)+"\n") {
		t.Fatalf("second pass artifact: exit %d\n%s", code, o)
	}
	if rep := readReport(t, passDir); rep.Status != "pass" || rep.Versions.Faultline == "v0.0.0-test" {
		t.Fatalf("the pass artifact was not replaced: %+v", rep)
	}

	// The note quotes a signature that holds a control character. Without the failing report's
	// failure object, a plain replay prints no note (API-083).
	root = t.TempDir()
	dir = seedDir(root, 1)
	env = []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root}
	noWarning("first run", env)
	editReportJSON(t, dir, func(m map[string]any) { object(t, m, "failure")["signature"] = "invariant:x\x1b[2J" })
	o, code = runScenario(t, "fails-for-0x1", append(env, "FIXED=1"))
	if note := "\n    " + keptNote(dir, `"invariant:x\x1b[2J"`) + "\n"; code != 0 || !strings.Contains(o, note) {
		t.Fatalf("quoted signature: exit %d\n%s", code, o)
	}
	editReportJSON(t, dir, func(m map[string]any) { delete(m, "failure") })
	o, code = runScenario(t, "fails-for-0x1", append(env, "FIXED=1"))
	if code != 0 || strings.Contains(o, "passed; kept the failing artifact") {
		t.Fatalf("no failure object: exit %d\n%s", code, o)
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
	warning := `extra artifact file "x.json" from final check "f2" dropped: final check "f1" already added it`
	if code != 1 || read(dir, "x.json") != "1" || read(dir, "y.txt") != "Y" || !strings.Contains(o, "warning: "+warning) {
		t.Fatalf("(b) exit %d\n%s", code, o)
	}
	if rep := readReport(t, dir); !slices.Contains(rep.Warnings, warning) {
		t.Fatalf("(b) warnings %q", rep.Warnings)
	}
	// (d) No file is written with artifacts off, so no file is dropped either.
	o, code = runScenario(t, "extra-files", []string{"ROW=b", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=off"})
	if code != 1 || !strings.Contains(o, "artifacts: off\n") || strings.Contains(o, "warning:") {
		t.Fatalf("(d) exit %d\n%s", code, o)
	}

	root = t.TempDir()
	results = filepath.Join(t.TempDir(), "r3.jsonl")
	o, code = runScenario(t, "extra-files", []string{"ROW=c", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	lines := resultLines(t, results)
	if code != 1 || !strings.Contains(o, `artifacts: not written: artifact: invalid extra file name "Bad/Name"`) || len(lines) != 1 || lines[0]["artifact_error"] == nil || lines[0]["artifact"] != nil {
		t.Fatalf("(c) exit %d results %v\n%s", code, lines, o)
	}
}
