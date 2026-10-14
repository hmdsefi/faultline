// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/fault"
)

// pinnedSeeds are the seeds of the pinned scenario, in the order it lists them; ROW=one lists
// only the failing one, 0x2.
var pinnedSeeds = []uint64{0x3, 0x2, 0x1}

// The pinned scenario lists its seeds with RunSeeds. Options.Seeds and Options.BaseSeed would
// derive other seeds under Run. Seed 0x2 fails at its first tick, unless FIXED=1.
var _ = scenario("pinned", func(t *testing.T) {
	seeds := pinnedSeeds
	if os.Getenv("ROW") == "one" {
		seeds = []uint64{0x2}
	}
	faultline.RunSeeds(t, faultline.Options{Seeds: 7, BaseSeed: 9, Duration: time.Second}, func(w *faultline.World) {
		if os.Getenv("OUT") != "" {
			outLine(t, fmt.Sprintf("0x%016x", w.Seed()))
		}
		tk := addTicker(w)
		w.Invariant("not seed 2", func() error {
			if w.Seed() == 0x2 && tk.count > 0 && os.Getenv("FIXED") != "1" {
				return errors.New("seed 2 ticked")
			}
			return nil
		})
	}, seeds...)
})

// pinnedRun returns the -run pattern and the replay command of the pinned scenario's seed in this
// test binary: the command sets no variable unless vars does (API-114).
func pinnedRun(seed uint64, vars string) (run, command string) {
	run = fmt.Sprintf("^TestScenario$/^seed=0x%016x$", seed)
	return run, vars + "go test -v" + replayFlags() + " -run '" + run + "' " + pkgPath
}

// AT-API-54; API-110, API-112, API-114: RunSeeds runs exactly its seeds, in the order given, and
// all of them although seed 0x2 fails. Options.Seeds, Options.BaseSeed and -short change nothing.
// The failing seed's report and replay command name its subtest, without FAULTLINE_SEED.
func TestRunSeeds(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "pinned", []string{"OUT=" + out, "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results}, "-test.short")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	want := hexSeeds(pinnedSeeds...)
	if got := ranSeeds(o); !slices.Equal(got, want) {
		t.Errorf("subtests %v, want %v\n%s", got, want, o)
	}
	if got, want := readLines(t, out), hexSeeds(0x3, 0x2, 0x2, 0x1); !slices.Equal(got, want) {
		t.Errorf("body calls %v, want %v (the artifact re-run of 0x2 included)", got, want)
	}
	for _, s := range []string{"faultline: running", "faultline: stopping after", "-short: running", "RunSeeds ignores"} {
		if strings.Contains(o, s) {
			t.Errorf("output holds %q\n%s", s, o)
		}
	}
	lines := resultLines(t, results)
	if len(lines) != 3 {
		t.Fatalf("results %v", lines)
	}
	for i, status := range []string{"pass", "fail", "pass"} {
		checkLine(t, want[i], lines[i], "seed", want[i], "index", float64(i), "status", status, "trace_hash", anyValue{})
	}

	run, command := pinnedRun(0x2, "")
	dir := seedDir(root, 0x2)
	rep := readReport(t, dir)
	for _, c := range []struct {
		field     string
		got, want any
	}{
		{"seed_source", rep.SeedSource, "code"},
		{"seed_index", rep.SeedIndex, 1},
		{"base_seed", rep.BaseSeed, ""},
		{"base_source", rep.BaseSource, ""},
		{"options.seeds", rep.Options.Seeds, 3},
		{"options.keep_going", rep.Options.KeepGoing, true},
		{"replay.command", rep.Replay.Command, command},
		{"replay.run", rep.Replay.Run, run},
		{"replay.env is {}", rep.Replay.Env != nil && len(rep.Replay.Env) == 0, true},
	} {
		if c.got != c.want {
			t.Errorf("report.json %s = %v, want %v", c.field, c.got, c.want)
		}
	}
	mustContainInOrder(t, o, "=== RUN   TestScenario/seed=0x0000000000000002\n", "replay:    "+command+"\n", "artifacts: "+dir+string(os.PathSeparator)+"\n")
	text, err := os.ReadFile(filepath.Join(dir, "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(text, []byte("\n    replay:    "+command+"\n")) {
		t.Errorf("report.txt lacks the replay command %s:\n%s", command, text)
	}

	o, code = runScenario(t, "pinned", []string{"ROW=one"})
	if code != 1 || !slices.Equal(ranSeeds(o), hexSeeds(0x2)) {
		t.Errorf("one seed: exit %d, subtests %v; want 1 and [0x2]\n%s", code, ranSeeds(o), o)
	}
}

// pinnedReplayRe matches the replay command of seed 0x2 in the pinned scenario's output: its
// variables and its -run pattern.
var pinnedReplayRe = regexp.MustCompile(`\n *replay:    (.*)go test -v.* -run '(\^TestScenario\$/\^seed=0x0000000000000002\$)' ` + regexp.QuoteMeta(pkgPath) + `\n`)

// AT-API-57; API-114: the printed replay command of a failing RunSeeds seed runs only that seed,
// and reproduces the failure: the same trace hash. Once the seed passes, the replay reads no
// previous report, so it prints no note about the failing artifact it keeps.
func TestRunSeedsReplay(t *testing.T) {
	root := t.TempDir()
	o, code, lines := runResults(t, "pinned", "FAULTLINE_ARTIFACTS="+root)
	m := pinnedReplayRe.FindStringSubmatch(o)
	if code != 1 || m == nil || len(lines) != 3 {
		t.Fatalf("exit %d, replay command %q, results %v\n%s", code, m, lines, o)
	}
	vars := strings.Fields(m[1])
	if len(vars) != 0 {
		t.Errorf("replay variables %q, want none", vars)
	}
	root2 := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code = runScenario(t, "pinned", append(vars, "FAULTLINE_ARTIFACTS="+root2, "FAULTLINE_RESULTS="+results), "-test.run="+m[2])
	replayed := resultLines(t, results)
	if code != 1 || !slices.Equal(ranSeeds(o), hexSeeds(0x2)) || len(replayed) != 1 {
		t.Fatalf("replay: exit %d, subtests %v, results %v; want 1, [0x2] and one line\n%s", code, ranSeeds(o), replayed, o)
	}
	checkLine(t, "replay", replayed[0], "seed", "0x0000000000000002", "index", 1.0, "status", "fail", "trace_hash", lines[1]["trace_hash"])
	if a, b := readReport(t, seedDir(root, 0x2)).Run.TraceHash, readReport(t, seedDir(root2, 0x2)).Run.TraceHash; a != b {
		t.Errorf("trace hash %s, replay %s", a, b)
	}

	o, code = runScenario(t, "pinned", append(vars, "FIXED=1", "FAULTLINE_ARTIFACTS="+root), "-test.run="+m[2])
	if code != 0 || !slices.Equal(ranSeeds(o), hexSeeds(0x2)) || strings.Contains(o, "kept the failing artifact") || strings.Contains(o, "warning:") {
		t.Errorf("fixed replay: exit %d, subtests %v; want 0, [0x2], no pass note and no warning\n%s", code, ranSeeds(o), o)
	}
	if rep := readReport(t, seedDir(root, 0x2)); rep.Status != "fail" {
		t.Errorf("fixed replay: the failing artifact has status %s", rep.Status)
	}
}

// AT-API-58; API-111, API-112: the seed-selection variables are validated but leave the list
// alone, and one line at the RunSeeds call names the ones that are set. FAULTLINE_SCHEDULE,
// FAULTLINE_TRACE=full and FAULTLINE_CHECK_DETERMINISM apply as in Run.
func TestRunSeedsEnv(t *testing.T) {
	o, code := runScenario(t, "pinned", []string{"ROW=one", "FAULTLINE_SEED=0x9", "FAULTLINE_SEED_LIST=0x7", "FAULTLINE_EXPLORE=1"})
	ignored := regexp.MustCompile(`\srunseeds_test\.go:\d+: faultline: RunSeeds ignores FAULTLINE_SEED, FAULTLINE_EXPLORE, and FAULTLINE_SEED_LIST; it runs the seeds listed in the test\n`)
	if code != 1 || !slices.Equal(ranSeeds(o), hexSeeds(0x2)) || len(ignored.FindAllString(o, -1)) != 1 || strings.Contains(o, "FAULTLINE_EXPLORE: base seed") {
		t.Errorf("seed-selection variables: exit %d, subtests %v; want 1, [0x2] and one line at the RunSeeds call\n%s", code, ranSeeds(o), o)
	}

	o, code = runScenario(t, "pinned", []string{"ROW=one", "FAULTLINE_SEED=0xZZ"})
	if code != 1 || strings.Contains(o, "seed=") || !strings.Contains(o, `faultline: invalid FAULTLINE_SEED value "0xZZ": want a decimal or 0x-prefixed hexadecimal uint64`) {
		t.Errorf("invalid FAULTLINE_SEED: exit %d; want 1, no seed and the setup error\n%s", code, o)
	}

	var buf bytes.Buffer
	if err := (fault.Schedule{Version: 1}).Write(&buf); err != nil {
		t.Fatal(err)
	}
	schedule := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(schedule, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	o, code = runScenario(t, "pinned", []string{"ROW=one", "FAULTLINE_SCHEDULE=" + schedule, "FAULTLINE_ARTIFACTS=" + root})
	_, command := pinnedRun(0x2, "FAULTLINE_SCHEDULE="+schedule+" ")
	rep := readReport(t, seedDir(root, 0x2))
	if code != 1 {
		t.Errorf("FAULTLINE_SCHEDULE: exit %d, want 1\n%s", code, o)
	}
	if line := "faultline: FAULTLINE_SCHEDULE=" + schedule + ": 0 events; planners are disabled\n"; !strings.Contains(o, line) {
		t.Errorf("FAULTLINE_SCHEDULE: the output lacks the line %q\n%s", line, o)
	}
	if !strings.Contains(o, "replay:    "+command+"\n") {
		t.Errorf("FAULTLINE_SCHEDULE: the console lacks the replay command %s\n%s", command, o)
	}
	if rep.Replay.Command != command {
		t.Errorf("FAULTLINE_SCHEDULE: report.json replay.command %s, want %s", rep.Replay.Command, command)
	}
	if len(rep.Replay.Env) != 1 || rep.Replay.Env["FAULTLINE_SCHEDULE"] != schedule {
		t.Errorf("FAULTLINE_SCHEDULE: report.json replay.env %v, want only FAULTLINE_SCHEDULE=%s", rep.Replay.Env, schedule)
	}

	root = t.TempDir()
	o, code = runScenario(t, "pinned", []string{"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=" + root})
	if code != 1 {
		t.Errorf("FAULTLINE_TRACE=full: exit %d\n%s", code, o)
	}
	for _, seed := range []uint64{0x3, 0x1} {
		dir := seedDir(root, seed)
		rep := readReport(t, dir)
		if _, command := pinnedRun(seed, ""); rep.Status != "pass" || rep.Replay.Command != command || !strings.Contains(o, fmt.Sprintf("faultline: seed 0x%016x passed: ", seed)) {
			t.Errorf("FAULTLINE_TRACE=full, seed 0x%x: status %s, replay %s; want pass and %s\n%s", seed, rep.Status, rep.Replay.Command, command, o)
		}
	}

	out := filepath.Join(t.TempDir(), "out")
	o, code = runScenario(t, "pinned", []string{"OUT=" + out, "FAULTLINE_CHECK_DETERMINISM=1"})
	if got, want := readLines(t, out), hexSeeds(0x3, 0x3, 0x2, 0x2, 0x1, 0x1); code != 1 || !slices.Equal(got, want) {
		t.Errorf("FAULTLINE_CHECK_DETERMINISM=1: exit %d, body calls %v; want 1 and %v\n%s", code, got, want, o)
	}
}

var _ = scenario("pinned-none", func(t *testing.T) {
	faultline.RunSeeds(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) { addTicker(w) })
	outLine(t, "after RunSeeds")
})

var _ = scenario("pinned-twice", func(t *testing.T) {
	faultline.RunSeeds(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) { addTicker(w) }, 0x1, 0x2, 0x1)
	outLine(t, "after RunSeeds")
})

var _ = scenario("pinned-nil", func(t *testing.T) {
	faultline.RunSeeds(t, faultline.Options{Duration: time.Second}, nil, 0x1)
	outLine(t, "after RunSeeds")
})

// The pinned-again scenario calls Run, then RunSeeds, on the same t: two calls (API-113).
var _ = scenario("pinned-again", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) { addTicker(w) })
	faultline.RunSeeds(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) { addTicker(w) }, 0x1)
	outLine(t, "after RunSeeds")
})

// AT-API-56; API-113: no seeds, a seed listed twice, a nil body, or a second call on the same t is
// a setup error at the RunSeeds call. No seed of RunSeeds starts, the test stops there, and the
// results file gets one setup line.
func TestRunSeedsSetupErrors(t *testing.T) {
	for _, c := range []struct {
		scenario, msg string
		before        int // seeds that Run ran before the RunSeeds call
	}{
		{"pinned-none", "faultline: RunSeeds: no seeds; list at least one seed after body", 0},
		{"pinned-twice", "faultline: RunSeeds: seed 0x0000000000000001 is listed twice; list each seed once", 0},
		{"pinned-nil", "faultline: RunSeeds: body is nil", 0},
		{"pinned-again", `faultline: Run called twice in test "TestScenario"; wrap each call in t.Run with a distinct name`, 1},
	} {
		out := filepath.Join(t.TempDir(), "out")
		o, code, lines := runResults(t, c.scenario, "OUT="+out)
		at := regexp.MustCompile(`\srunseeds_test\.go:\d+: ` + regexp.QuoteMeta(c.msg) + "\n")
		if ran := ranSeeds(o); code != 1 || !at.MatchString(o) || len(ran) != c.before || slices.Contains(ran, "0x0000000000000001") {
			t.Errorf("%s: exit %d, subtests %v; want 1, the error at the RunSeeds call and %d subtests of Run\n%s", c.scenario, code, ran, c.before, o)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("%s: the test went on after the setup error", c.scenario)
		}
		if len(lines) != c.before+1 {
			t.Errorf("%s: results %v", c.scenario, lines)
			continue
		}
		checkLine(t, c.scenario, lines[c.before], "seed", "", "index", -1.0, "status", "fail", "kind", "setup", "message", c.msg)
	}
}

var _ = scenario("pinned-parallel-noseed", func(t *testing.T) {
	t.Parallel()
	faultline.RunSeeds(t, faultline.Options{Duration: time.Second, NoCryptoSeed: true}, func(w *faultline.World) { addTicker(w) }, 0x1, 0x2)
})

var _ = scenario("pinned-parallel", func(t *testing.T) {
	t.Parallel()
	faultline.RunSeeds(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) { addTicker(w) }, 0x1, 0x2)
})

// AT-API-59; API-113, API-115: a parallel test can call RunSeeds with Options.NoCryptoSeed.
// Without it, seeding crypto/rand fails every listed seed with API-022's setup error, which names
// RunSeeds.
func TestRunSeedsParallel(t *testing.T) {
	o, code := runScenario(t, "pinned-parallel-noseed", nil)
	if code != 0 || !slices.Equal(ranSeeds(o), hexSeeds(0x1, 0x2)) {
		t.Errorf("NoCryptoSeed: exit %d, subtests %v; want 0 and [0x1 0x2]\n%s", code, ranSeeds(o), o)
	}
	o, code = runScenario(t, "pinned-parallel", nil)
	if code != 1 || !slices.Equal(ranSeeds(o), hexSeeds(0x1, 0x2)) || strings.Count(o, "faultline: crypto seeding:") != 2 ||
		strings.Count(o, "(RunSeeds cannot be used in a parallel test; remove t.Parallel or set Options.NoCryptoSeed)\n") != 2 {
		t.Errorf("crypto seeding: exit %d, subtests %v; want 1, [0x1 0x2] and two setup errors\n%s", code, ranSeeds(o), o)
	}
}
