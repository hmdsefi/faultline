// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

var (
	runMu     sync.Mutex
	runActive = map[*testing.T]bool{} // tests inside Run; lookups only (API-004)
	resultsMu sync.Mutex              // serializes FAULTLINE_RESULTS appends (API-087)
	writeMu   sync.Mutex              // serializes artifact writes of parallel subtests (API-074)
)

// seedResult is the outcome of one seed subtest (API-087).
type seedResult struct {
	index       int
	seed        uint64
	start       time.Time
	status      string // pass, fail, skip
	fail        *failure
	events      *uint64 // the primary attempt's; nil until it completed (API-087)
	traceHash   string
	artifactDir string
	artifactErr string
	primary     *failure // the primary attempt's failure, for Goexit in later attempts (API-072)
	attempts    int
	hashes      []uint64 // trace hashes of the attempts that completed, for Goexit in later attempts
	setupStop   bool     // a later attempt stopped with a setup error (API-072)
}

// Run runs body for each seed of the seed list, one seed after another, each seed in its own
// subtest named "seed=0x%016x". Call it from the test goroutine of t, at most once per t.
//
// Run calls body for every run of a seed, so body must build all the state it uses from w. A
// failing seed runs a second time to write its artifacts, and with determinism checking a passing
// seed runs twice. The runs of a seed must do the same thing.
//
// A failing seed fails its subtest. It prints the failure, a replay command and its artifact
// directory, which holds report.json, report.txt, trace.jsonl, timeline.html and the other files
// of package artifact. The first failing seed stops the loop unless Options.KeepGoing is set or
// FAULTLINE_SEED_LIST names the seeds.
//
// These environment variables change a run without code changes:
//   - FAULTLINE_SEED runs one seed. FAULTLINE_SEEDS, FAULTLINE_BASE_SEED and FAULTLINE_EXPLORE
//     choose the seed list.
//   - FAULTLINE_SCHEDULE replays a schedule.json from an artifact.
//   - FAULTLINE_ARTIFACTS sets the artifact root, faultline under os.TempDir() by default, or
//     turns artifacts off with "off".
//   - FAULTLINE_CHECK_DETERMINISM=1 runs every passing seed twice and compares the trace hashes.
//   - FAULTLINE_TRACE=full keeps full traces and writes artifacts for passing seeds too.
//   - FAULTLINE_SEED_LIST runs exactly the listed seeds, and FAULTLINE_RESULTS appends one JSON
//     line per seed to a file.
//
// An invalid value fails t before any seed runs.
func Run(t *testing.T, opts Options, body func(w *World)) {
	t.Helper()
	lookup := onceLookup(os.LookupEnv) // resultsPath and resolve share it: one read per variable (API-010)
	r := &runner{t: t, body: body, build: readBuild(), results: resultsPath(lookup)}
	if body == nil {
		r.parentSetupError("faultline: Run: body is nil")
		return
	}
	runMu.Lock()
	dup := runActive[t]
	runActive[t] = true
	runMu.Unlock()
	if dup {
		r.parentSetupError(fmt.Sprintf("faultline: Run called twice in test %q; wrap each call in t.Run with a distinct name", t.Name()))
		return
	}
	t.Cleanup(func() {
		runMu.Lock()
		delete(runActive, t)
		runMu.Unlock()
	})
	p, err := resolve(resolveInput{opts: opts, testName: t.Name(), short: testing.Short(), lookup: lookup, exploreBase: exploreBase})
	if err != nil {
		r.parentSetupError(err.Error())
		return
	}
	r.plan = p
	if r.cwd, err = os.Getwd(); err != nil {
		t.Logf("faultline: os.Getwd: %v; report.json's replay.dir and replay.package_dir are empty", err)
	}
	for _, l := range p.logs {
		t.Log(l)
	}
	for i, seed := range p.seeds {
		var res *seedResult
		t.Run(fmt.Sprintf("seed=0x%016x", seed), func(st *testing.T) {
			res = &seedResult{index: i, seed: seed, start: time.Now()}
			r.runSeed(st, res)
		})
		if res != nil && res.status == "fail" && !p.keepGoing {
			if left := len(p.seeds) - i - 1; left > 0 {
				t.Logf("faultline: stopping after failing seed 0x%016x; %d of %d seeds not run (set Options.KeepGoing to run all)", seed, left, len(p.seeds))
			}
			return
		}
	}
}

// onceLookup wraps lookup so that each name is looked up at most once. Run passes the result to
// resultsPath and to resolve, so it reads every variable once per call (API-010).
func onceLookup(lookup lookupFunc) lookupFunc {
	type result struct {
		v  string
		ok bool
	}
	read := map[string]result{} // lookups only
	return func(name string) (string, bool) {
		r, done := read[name]
		if !done {
			r.v, r.ok = lookup(name)
			read[name] = r
		}
		return r.v, r.ok
	}
}

// parentSetupError reports a setup error before any seed started (API-001, API-087). It is a
// helper, so the error points at the caller of Run.
func (r *runner) parentSetupError(msg string) {
	r.t.Helper()
	if r.results != "" {
		line := resultLine{Version: 1, Package: r.build.importPath, Test: r.t.Name(), Seed: "", Index: -1, Status: "fail", Kind: "setup", Signature: "setup:", Message: &msg}
		if err := appendResult(r.results, line); err != nil {
			r.t.Errorf("faultline: FAULTLINE_RESULTS: %v", err)
		}
	}
	r.t.Fatalf("%s", msg)
}

// hex16 formats a seed or a trace hash as 0x and 16 lowercase hex digits.
func hex16(v uint64) string { return fmt.Sprintf("0x%016x", v) }

// replay returns report.replay for seed.
func (r *runner) replay(seed uint64) artifact.Replay {
	return buildReplay(r.build, r.cwd, r.t.Name(), seed, r.plan.env.schedulePath)
}

// artifactRoot returns the artifact root, or "" when artifacts are off (API-074).
func (r *runner) artifactRoot() string {
	env := r.plan.env
	if env.artifactsOff {
		return ""
	}
	if env.artifactsRoot != "" {
		return env.artifactsRoot
	}
	return filepath.Join(os.TempDir(), "faultline")
}

// artifactDir returns the artifact directory of seed, or "" when artifacts are off (API-074).
func (r *runner) artifactDir(seed uint64) string {
	root := r.artifactRoot()
	if root == "" {
		return ""
	}
	return artifact.Dir(root, r.build.importPath, r.t.Name(), seed)
}

// writeArtifact writes a to the artifact directory of seed and returns the directory it used
// (API-074). When that directory holds the artifact of another test whose name maps to the same
// folder (ART-001), a goes to artifact.AltDir instead, so both tests keep their artifacts. text
// returns report.txt for a directory. Artifacts must be on. Writes are serialized: two parallel
// subtests whose names share a folder would otherwise race on it.
func (r *runner) writeArtifact(seed uint64, a *artifact.Artifact, text func(dir string) string) (string, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	dir := r.artifactDir(seed)
	a.Text = text(dir)
	err := artifact.Write(dir, a)
	if errors.Is(err, artifact.ErrOtherTest) {
		dir = artifact.AltDir(r.artifactRoot(), r.build.importPath, r.t.Name(), seed)
		a.Text = text(dir)
		err = artifact.Write(dir, a)
	}
	return dir, err
}

// runSeed runs every attempt of one seed (API-020 to API-022, API-070 to API-077, API-101).
func (r *runner) runSeed(st *testing.T, res *seedResult) {
	completed := false
	r.cur = nil
	defer func() {
		if !completed {
			r.handleGoexit(st, res)
		}
		r.writeResult(st, res)
	}()
	seed := res.seed
	prev := r.previousReport(seed)
	res.attempts = 1
	a1 := r.attempt(st, seed, r.plan.primaryTrace, r.plan.env.schedule, true)
	if a1.setupErr != "" {
		res.status = "fail"
		res.fail = &failure{kind: "setup", message: a1.setupErr}
		st.Fatalf("%s", a1.setupErr)
	}
	res.events, res.traceHash, res.hashes = &a1.executed, hex16(a1.hash), []uint64{a1.hash}
	res.primary = a1.fail
	if a1.limited {
		st.Logf("faultline: seed 0x%016x stopped at MaxEvents (%d) at t=%s; Options.AllowLimit is set, so this is not a failure; final checks were skipped", seed, r.plan.opts.MaxEvents, a1.now)
	}

	var outcome *failure
	var art attemptResult
	if a1.fail != nil {
		cfg := kernel.TraceConfig{Level: kernel.TraceFull}
		if a1.fail.kind == "limit" {
			cfg.Buffer = 1_000_000
		}
		a2 := r.rerun(st, res, cfg)
		art = a2
		if a2.hash == a1.hash {
			outcome = a1.fail
			if a2.fail != nil && outcome.node == "" {
				outcome.nodeID, outcome.node = a2.fail.nodeID, a2.fail.node
			}
		} else {
			a3 := r.rerun(st, res, cfg)
			outcome = newDeterminismFailure("artifact_rerun", seed, []attemptResult{a1, a2, a3}, a1.fail, a2.records, a3.records, a2)
		}
	} else if r.plan.checkDeterminism {
		a2 := r.rerun(st, res, r.plan.primaryTrace)
		if a2.hash != a1.hash {
			full := kernel.TraceConfig{Level: kernel.TraceFull}
			a3 := r.rerun(st, res, full)
			a4 := r.rerun(st, res, full)
			outcome = newDeterminismFailure("check_determinism", seed, []attemptResult{a1, a2, a3, a4}, nil, a3.records, a4.records, a3)
			art = a3
		}
	}

	if outcome == nil {
		res.status = "pass"
		if r.plan.passArtifacts {
			r.writePassArtifact(st, res, a1)
		}
		if prev != nil && prev.Status == "fail" && prev.Failure != nil {
			warnings := compareReports(prev, versions(r.build), optionsHash(r.plan.opts, r.plan.env.scheduleHash))
			msg := fmt.Sprintf("faultline: seed 0x%016x passed; the previous artifact at %s recorded %s", seed, prev.Dir, prev.Failure.Signature)
			for _, w := range warnings {
				msg += "\nwarning: " + w
			}
			st.Log(msg)
		}
		completed = true
		return
	}

	res.status, res.fail = "fail", outcome
	var warnings []string
	if prev != nil {
		warnings = compareReports(prev, versions(r.build), optionsHash(r.plan.opts, r.plan.env.scheduleHash))
	}
	warnings = append(warnings, art.warnings...)
	rep := r.replay(seed)
	dir := r.artifactDir(seed)
	artifactsLine := "off"
	written := false
	if dir != "" {
		a := r.buildArtifact(st, res, outcome, art, warnings)
		d, err := r.writeArtifact(seed, a, func(dir string) string {
			return "--- FAIL: " + st.Name() + "\n" + joinLines(consoleLines(outcome, rep.Command, dir+string(os.PathSeparator), warnings, true, true), "    ")
		})
		if err != nil {
			artifactsLine = "not written: " + err.Error()
			res.artifactErr = err.Error()
		} else {
			artifactsLine, written = d+string(os.PathSeparator), true
			res.artifactDir = d
		}
	}
	fmt.Fprint(st.Output(), joinLines(consoleLines(outcome, rep.Command, artifactsLine, warnings, false, written), ""))
	st.Fail()
	completed = true
}

// rerun runs a later attempt of the seed (API-070). A setup error there stops the seed through
// t.Fatalf, and handleGoexit reports it like any other stop in a later attempt (API-072).
func (r *runner) rerun(st *testing.T, res *seedResult, cfg kernel.TraceConfig) attemptResult {
	res.attempts++
	r.cur = nil
	a := r.attempt(st, res.seed, cfg, r.plan.env.schedule, false)
	if a.setupErr != "" {
		res.setupStop = true
		st.Fatalf("%s", a.setupErr)
	}
	res.hashes = append(res.hashes, a.hash)
	return a
}

// buildArtifact fills the artifact of a seed from its artifact attempt (API-074).
func (r *runner) buildArtifact(st *testing.T, res *seedResult, f *failure, art attemptResult, warnings []string) *artifact.Artifact {
	p := r.plan
	vers := versions(r.build)
	seedText := hex16(res.seed)
	status := "pass"
	var rf *artifact.Failure
	if f != nil {
		status, rf = "fail", f.report()
	}
	rep := artifact.Report{
		Status: status, Package: r.build.importPath, Test: r.t.Name(), Subtest: st.Name(),
		Seed: seedText, SeedSource: p.seedSource, SeedIndex: res.index, Failure: rf, Warnings: warnings,
		Replay: r.replay(res.seed), Versions: vers, Options: runOptions(p.opts, p.env),
		OptionsHash: optionsHash(p.opts, p.env.scheduleHash), Nodes: art.nodes,
		Run: artifact.RunInfo{
			TraceHash: hex16(art.hash), Events: art.executed, Records: art.lastSeq,
			EndNS: int64(art.now), End: art.now.String(), Stop: art.stop, Planners: art.planners, Attempts: res.attempts,
		},
	}
	if p.seedSource == "derived" {
		rep.BaseSeed, rep.BaseSource = hex16(p.base), p.baseSource
	}
	if len(art.records) > 0 {
		rep.Run.Dropped = art.records[0].Seq - 1
	}
	if art.hasRecovery {
		rep.Run.RecoveryNS, rep.Run.Recovery = int64(art.recovery), art.recovery.String()
	}
	sched := art.schedule
	a := &artifact.Artifact{
		Report: rep,
		Trace: &artifact.Trace{
			Header: artifact.TraceHeader{
				FaultlineVersion: vers.Faultline, GoVersion: vers.Go, Package: r.build.importPath, Test: r.t.Name(),
				Subtest: st.Name(), Seed: seedText, TraceHash: hex16(art.hash), Nodes: art.nodes,
			},
			Records: art.records,
		},
		Schedule: &sched,
		History:  art.history,
	}
	if len(art.extra) > 0 {
		a.Extra = map[string][]byte{}
		for _, e := range art.extra {
			a.Extra[e.name] = e.data
		}
	}
	return a
}

// writePassArtifact writes the artifact of a passing seed under FAULTLINE_TRACE=full (API-073).
func (r *runner) writePassArtifact(st *testing.T, res *seedResult, a1 attemptResult) {
	if r.artifactDir(res.seed) == "" {
		return
	}
	lines := func(dir string) []string {
		return []string{
			fmt.Sprintf("faultline: seed 0x%016x passed: %d events, ended at t=%s (%s)", res.seed, a1.executed, a1.now, a1.stop),
			fmt.Sprintf("artifacts: %s%c", dir, os.PathSeparator),
		}
	}
	dir, err := r.writeArtifact(res.seed, r.buildArtifact(st, res, nil, a1, nil), func(dir string) string {
		return "--- PASS: " + st.Name() + "\n" + joinLines(lines(dir), "    ")
	})
	if err != nil {
		res.artifactErr = err.Error()
		st.Errorf("faultline: writing artifacts: %v", err)
		return
	}
	res.artifactDir = dir
	st.Log(strings.Join(lines(dir), "\n"))
}

// previousReport reads the report of a previous run of the same seed (API-083): only when the
// seed list came from FAULTLINE_SEED and artifacts are on. When the seed's directory holds the
// report of another test whose name maps to the same folder, it reads the one in artifact.AltDir,
// where writeArtifact put this test's.
func (r *runner) previousReport(seed uint64) *artifact.Report {
	if r.plan.seedSource != "env" {
		return nil
	}
	root := r.artifactRoot()
	if root == "" {
		return nil
	}
	pkg, test := r.build.importPath, r.t.Name()
	for _, dir := range []string{artifact.Dir(root, pkg, test, seed), artifact.AltDir(root, pkg, test, seed)} {
		rep := readReportFile(dir)
		if rep == nil {
			return nil
		}
		if rep.Package == pkg && rep.Test == test && rep.Seed == fmt.Sprintf("0x%016x", seed) {
			return rep
		}
	}
	return nil
}

// readReportFile reads report.json in dir, or returns nil when it is missing or unreadable. Only a
// regular file is opened, so a link is not followed and a FIFO does not block the run.
func readReportFile(dir string) *artifact.Report {
	path := filepath.Join(dir, artifact.FileReport)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	rep, err := artifact.ReadReport(f)
	if err != nil {
		return nil
	}
	return rep
}

// goexitMessage is the message of a seed that its primary attempt stopped through Goexit (API-101).
const goexitMessage = "stopped by t.FailNow, t.Fatal or t.SkipNow"

// handleGoexit classifies a seed whose subtest goroutine exited through runtime.Goexit. The
// primary attempt decides the seed's outcome: a Goexit there goes through API-101's cases. A
// Goexit in a later attempt never changes the outcome (API-072). The seed keeps the primary
// attempt's failure, or the determinism failure that a diagnostic attempt was looking into. The
// one exception is the check attempt after a passing primary attempt: the first run did not stop,
// so a stop there is a determinism failure. No Goexit writes artifacts, and events and trace_hash
// stay as the primary attempt set them (API-087).
func (r *runner) handleGoexit(st *testing.T, res *seedResult) {
	s := r.howStopped(st, res)
	if res.attempts == 1 {
		r.primaryGoexit(st, res, s)
		return
	}
	st.Fail() // also after a skip: the seed fails (API-072)
	res.status = "fail"
	var name string
	switch {
	case res.attempts == 2 && res.primary != nil:
		name, res.fail = "artifact re-run", res.primary
	case res.attempts == 2:
		name = "determinism check"
		res.fail = stoppedDeterminism(res, name, s)
	default:
		name = "diagnostic re-run"
		res.fail = stoppedDeterminism(res, name, s)
	}
	lines := consoleLines(res.fail, "", "", nil, false, false)
	lines = lines[:len(lines)-2] // the failure without its replay and artifacts lines (API-075)
	lines = append(lines,
		fmt.Sprintf("faultline: the %s of seed %s %s; no artifacts were written", name, hex16(res.seed), s.text()),
		"  the first run did not stop, so the test depends on something outside the seed; the failure above is the seed's outcome",
		"replay:    "+r.replay(res.seed).Command)
	fmt.Fprint(st.Output(), joinLines(lines, ""))
}

// primaryGoexit applies API-101's cases to a Goexit in the primary attempt.
func (r *runner) primaryGoexit(st *testing.T, res *seedResult, s goexitStop) {
	switch {
	case res.fail != nil: // the setup error that runSeed reported
	case st.Skipped() && !st.Failed():
		res.status = "skip"
	case s.early:
		res.status, res.fail = "fail", &failure{kind: "setup", message: goexitMessage}
	default:
		res.status, res.fail = "fail", &failure{kind: "fail", message: goexitMessage, at: s.at}
		fmt.Fprintf(st.Output(), "faultline: seed %s %s; no artifacts were written\n  report failures with an Invariant, a Final check or w.Sim.Fail(err) to get a replayable report\nreplay:    %s\n",
			hex16(res.seed), s.text(), r.replay(res.seed).Command)
	}
}

// goexitStop is how and when an attempt stopped through runtime.Goexit (API-072, API-101).
type goexitStop struct {
	how   string      // t.FailNow or t.Fatal, t.SkipNow or t.Skip, or a setup error
	early bool        // before the attempt's first event: no World, or w.Sim.Executed() == 0
	at    kernel.Time // PanicError.At for a Goexit in a callback, else w.Sim.Now(); 0 without a World
	event uint64      // w.Sim.Executed()
}

// howStopped returns how and when the current attempt stopped.
func (r *runner) howStopped(st *testing.T, res *seedResult) goexitStop {
	s := goexitStop{how: "t.FailNow or t.Fatal", early: true}
	switch {
	case res.setupStop:
		s.how = "a setup error"
	case st.Skipped():
		s.how = "t.SkipNow or t.Skip"
	}
	w := r.cur
	if w == nil {
		return s
	}
	s.at, s.event = w.Sim.Now(), w.Sim.Executed()
	s.early = s.event == 0
	var pe *kernel.PanicError
	if errors.As(w.Sim.Err(), &pe) && pe.Goexit {
		s.at = pe.At
	}
	return s
}

// text is "stopped by <how> at t=<at> (event <event>)", or "stopped by <how> before its first event".
func (s goexitStop) text() string {
	if s.early {
		return "stopped by " + s.how + " before its first event"
	}
	return fmt.Sprintf("stopped by %s at t=%s (event %d)", s.how, s.at, s.event)
}

// stoppedDeterminism returns the determinism failure of a seed whose check attempt or diagnostic
// attempt, called name, stopped through Goexit (API-072). It holds the trace hashes of the
// attempts that completed.
func stoppedDeterminism(res *seedResult, name string, s goexitStop) *failure {
	d := &determinism{context: "check_determinism", seed: res.seed, hashes: res.hashes, original: res.primary}
	f := &failure{kind: "determinism", context: "determinism-check", at: s.at, determinism: d}
	stop := "the " + name + " " + s.text()
	switch {
	case res.primary != nil: // a diagnostic attempt after an artifact re-run mismatch
		d.context, f.context = "artifact_rerun", "determinism-artifact"
		f.message = "the first run failed: " + res.primary.headline() + "\n" + stop + ", so the first differing record was not found\n"
	case len(res.hashes) > 1: // a diagnostic attempt after a check mismatch
		f.message = stop + ", so the first differing record was not found\n"
	default: // the check attempt: one trace hash, so the headline is "determinism failure at t=<at>"
		f.context = ""
		f.message = stop + "; the first run passed with trace hash " + hex16(res.hashes[0]) + "\n"
	}
	f.message += commonCauses
	return f
}

// resultLine is one FAULTLINE_RESULTS line (API-087), fields in spec order.
type resultLine struct {
	Version       int     `json:"faultline_result"`
	Package       string  `json:"package"`
	Test          string  `json:"test"`
	Seed          string  `json:"seed"`
	Index         int     `json:"index"`
	Status        string  `json:"status"`
	Kind          string  `json:"kind,omitempty"`
	Check         string  `json:"check,omitempty"`
	Signature     string  `json:"signature,omitempty"`
	Message       *string `json:"message,omitempty"` // set on every fail line, also when empty
	AtNS          *int64  `json:"at_ns,omitempty"`
	At            string  `json:"at,omitempty"`
	Events        *uint64 `json:"events,omitempty"`
	TraceHash     string  `json:"trace_hash,omitempty"`
	Artifact      string  `json:"artifact,omitempty"`
	ArtifactError string  `json:"artifact_error,omitempty"`
	WallNS        int64   `json:"wall_ns"`
}

// appendResult appends one JSON line with a single Write (API-087).
func appendResult(path string, line resultLine) error {
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	resultsMu.Lock()
	defer resultsMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// writeResult appends the seed's results line when FAULTLINE_RESULTS is set (API-087).
func (r *runner) writeResult(st *testing.T, res *seedResult) {
	if r.results == "" {
		return
	}
	line := resultLine{
		Version: 1, Package: r.build.importPath, Test: r.t.Name(), Seed: hex16(res.seed),
		Index: res.index, Status: res.status, Events: res.events, TraceHash: res.traceHash,
		Artifact: res.artifactDir, ArtifactError: res.artifactErr, WallNS: int64(time.Since(res.start)),
	}
	if res.status == "fail" && res.fail != nil {
		f := res.fail
		msg := f.message
		line.Kind, line.Check, line.Signature, line.Message = f.kind, f.check, f.signature(), &msg
		if f.kind != "setup" {
			at := int64(f.at)
			line.AtNS, line.At = &at, f.at.String()
		}
	}
	if err := appendResult(r.results, line); err != nil {
		st.Errorf("faultline: FAULTLINE_RESULTS: %v", err)
	}
}
