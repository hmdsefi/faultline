// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/internal/gosched"
	"github.com/hmdsefi/faultline/kernel"
)

// gapScheduler is fakeScheduler with two coverage gaps.
type gapScheduler struct{ *fakeScheduler }

var twoGaps = []string{"/src/a.go:3: sync.Map.Range", "/src/b.go:9: reflect map iteration"}

func (gapScheduler) Gaps() []string { return twoGaps }

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	// A failing seed of a binary that "was built with the backend": XTE_PROCS sets Options.Procs,
	// XTE_EVENT=1 runs event mode.
	gorScenarios["xte-labels"] = func(t *testing.T) {
		gosched.Active = gapScheduler{&fakeScheduler{}}
		procs, _ := strconv.Atoi(os.Getenv("XTE_PROCS"))
		mode := ModeGoroutine
		if os.Getenv("XTE_EVENT") == "1" {
			mode = ModeEvent
		}
		Run(t, Options{Mode: mode, Seeds: 1, Duration: time.Second, Procs: procs}, func(w *World) {
			w.AddServer("s", func(*kernel.Node) {})
			w.Invariant("fails", func() error { return errors.New("boom") })
		})
	}
	// Every second attempt adds a node: state shared between attempts makes the check diverge.
	gorScenarios["xte-diverge"] = func(t *testing.T) {
		gosched.Active = gapScheduler{&fakeScheduler{}}
		attempts := 0
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: time.Second}, func(w *World) {
			attempts++
			w.AddServer("s", func(*kernel.Node) {})
			if attempts%2 == 0 {
				w.AddServer("extra", func(*kernel.Node) {})
			}
		})
	}
}

// has reports whether out holds line as a whole line, indented or not.
func has(out, line string) bool {
	return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(strings.TrimSpace(line)) + `$`).MatchString(out)
}

// AT-XTE-21
func TestATXTE21_Labels(t *testing.T) {
	const procsLine = "faultline: toolexec backend: Options.Procs is 2; goroutine-mode seeds replay exactly only with Procs 1, so these seeds are best-effort"
	for _, c := range []struct {
		name, level, backend, console string
		env                           []string
		procsLog, toolexec            bool
	}{
		{"exact", "exact", "toolexec", exactConsoleLine, nil, false, true},
		{"procs 2", "best-effort", "", gorConsoleLine, []string{"XTE_PROCS=2"}, true, true},
		{"event mode", "exact", "", "", []string{"XTE_EVENT=1"}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := runGorScenario(t, "xte-labels", append(c.env, "FAULTLINE_SEED=0x1", "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl")...)
			if res.code != 1 {
				t.Fatalf("exit %d\n%s", res.code, res.out)
			}
			lines := res.results(t)
			if len(lines) != 1 {
				t.Fatalf("%d results lines", len(lines))
			}
			backend, _ := lines[0]["exact_backend"].(string)
			if lines[0]["determinism_level"] != c.level || backend != c.backend {
				t.Errorf("results line %v/%q, want %s/%q", lines[0]["determinism_level"], backend, c.level, c.backend)
			}
			rep := res.artifact(t, 1).Report
			if rep.DeterminismLevel != c.level || rep.ExactBackend != c.backend {
				t.Errorf("report %s/%q, want %s/%q", rep.DeterminismLevel, rep.ExactBackend, c.level, c.backend)
			}
			for _, l := range []string{exactConsoleLine, gorConsoleLine} {
				if has(res.out, l) != (l == c.console) {
					t.Errorf("console line %q present: %v, want %v\n%s", l, has(res.out, l), l == c.console, res.out)
				}
			}
			// XTE-091: only the procs 2 row logs, with the resolved Procs; Procs 1 and event mode never do.
			logged := strings.Contains(res.out, "faultline: toolexec backend:")
			if logged != c.procsLog || c.procsLog && !strings.Contains(res.out, procsLine) {
				t.Errorf("XTE-091's log line present: %v, want %v (the procs 2 row wants %q)\n%s", logged, c.procsLog, procsLine, res.out)
			}
			// XTE-052: both goroutine-mode rows get the gaps line and toolexec-gaps.txt, event mode neither.
			goroutine := c.console != ""
			_, err := os.Stat(filepath.Join(artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1), gapsFile))
			if strings.Contains(res.out, "coverage gaps:") != goroutine || (err == nil) != goroutine {
				t.Errorf("gaps line or %s present (stat error %v), want %v\n%s", gapsFile, err, goroutine, res.out)
			}
			if strings.Contains(rep.Replay.Command, "go test -v -toolexec=faultline-toolexec ") != c.toolexec {
				t.Errorf("replay %q: -toolexec present must be %v", rep.Replay.Command, c.toolexec)
			}
		})
	}
}

// AT-XTE-22
func TestATXTE22_FaultlineExact(t *testing.T) {
	const (
		noBackend = "faultline: FAULTLINE_EXACT=1, but this test binary was built without the toolexec backend; build it with go test -toolexec=faultline-toolexec after running faultline-toolexec prepare"
		procs2    = "faultline: FAULTLINE_EXACT=1, but Options.Procs is 2; exact replay needs Procs 1"
	)
	g, g2 := Options{Mode: ModeGoroutine}, Options{Mode: ModeGoroutine, Procs: 2}
	for _, c := range []struct {
		name     string
		opts     Options
		val      string
		toolexec bool
		want     string
	}{
		{"no backend", g, "1", false, noBackend},
		{"no backend comes first", g2, "1", false, noBackend},
		{"procs 2", g2, "1", true, procs2},
		{"exact", g, "1", true, ""},
		{"procs 2 unchecked", g2, "0", true, ""},
		{"zero", g2, "0", false, ""},
		{"event mode", Options{}, "1", false, ""},
		{"not a bool", g, "2", true, `faultline: invalid FAULTLINE_EXACT value "2": want 0 or 1`},
	} {
		p, err := resolve(resolveInput{opts: c.opts, testName: "TestScenario", lookup: fakeEnv(map[string]string{"FAULTLINE_EXACT": c.val}),
			exploreBase: func() uint64 { return 7 }, toolexec: c.toolexec})
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: error %q, want %q", c.name, got, c.want)
		}
		// XTE-091 needs the backend and a resolved Procs other than 1.
		if err == nil {
			logged := slices.ContainsFunc(p.logs, func(l string) bool { return strings.HasPrefix(l, "faultline: toolexec backend:") })
			if want := c.toolexec && c.opts.Procs == 2; logged != want {
				t.Errorf("%s: XTE-091's log line present: %v, want %v (logs %q)", c.name, logged, want, p.logs)
			}
		}
	}
}

// AT-XTE-23
func TestATXTE23_GapsVisible(t *testing.T) {
	res := runGorScenario(t, "xte-labels", "FAULTLINE_SEED=0x1")
	re := regexp.MustCompile(`(?m)^\s*artifacts: .*\n\s*determinism: exact \(toolexec backend\)\n\s*coverage gaps: 2 in this test binary; toolexec-gaps\.txt in the artifact directory lists them$`)
	if res.code != 1 || !re.MatchString(res.out) {
		t.Fatalf("exit %d; no determinism and gaps lines right after the artifacts line\n%s", res.code, res.out)
	}
	if text := res.artifact(t, 1).Text; !re.MatchString(text) {
		t.Errorf("report.txt lacks the determinism and gaps lines right after the artifacts line\n%s", text)
	}
	dir := artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1)
	b, err := os.ReadFile(filepath.Join(dir, gapsFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(twoGaps, "\n") + "\n"; string(b) != want {
		t.Errorf("%s = %q, want %q", gapsFile, b, want)
	}
	off := runGorScenario(t, "xte-labels", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=off")
	if !has(off.out, "  coverage gaps: 2 in this test binary") {
		t.Fatalf("artifacts off: no plain gaps line\n%s", off.out)
	}
}

// AT-XTE-24
func TestATXTE24_DivergenceMessage(t *testing.T) {
	res := runGorScenario(t, "xte-diverge", "FAULTLINE_SEED=0x1", "FAULTLINE_CHECK_DETERMINISM=1")
	const line = "determinism: exact (toolexec backend): a divergence comes from code the rewriter does not control: 2 coverage gaps in this test binary, packages outside the rewrite set, sync/atomic, or state shared between runs"
	if res.code != 1 || !strings.Contains(res.out, line) || strings.Contains(res.out, gorLevel3Line) {
		t.Fatalf("exit %d; want XTE-090's message line instead of GOR-077's\n%s", res.code, res.out)
	}
}

// AT-XTE-25
func TestATXTE25_ReplayWithToolexec(t *testing.T) {
	b := buildInfo{importPath: "example.com/kv", modulePath: "example.com/kv"}
	const run = " -run '^TestKV$' example.com/kv"
	for _, c := range []struct {
		wrapper string
		active  bool
		want    string
	}{
		{"/opt/bin/faultline-toolexec", true, "FAULTLINE_SEED=0x0000000000000001 go test -v -toolexec=/opt/bin/faultline-toolexec" + run},
		{"/opt/my tools/faultline-toolexec", true, `FAULTLINE_SEED=0x0000000000000001 go test -v -toolexec=''\''/opt/my tools/faultline-toolexec'\'''` + run},
		{"", true, "FAULTLINE_SEED=0x0000000000000001 go test -v -toolexec=faultline-toolexec" + run},
		{"", false, "FAULTLINE_SEED=0x0000000000000001 go test -v" + run},
	} {
		flag := ""
		if c.active {
			flag = toolexecValue(c.wrapper)
		}
		if got := buildReplay(b, "/src/kv", "TestKV", 1, "", false, false, flag).Command; got != c.want {
			t.Errorf("wrapper %q: %q, want %q", c.wrapper, got, c.want)
		}
	}
}
