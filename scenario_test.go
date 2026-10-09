package faultline_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// scenarios are the child-process scenarios of API §10, by name.
var scenarios = map[string]func(t *testing.T){}

// scenario registers fn under name; use it in package-level var declarations. A name used twice
// panics when the test binary starts, so a scenario never silently replaces another.
func scenario(name string, fn func(t *testing.T)) bool { //nolint:unparam // var _ = scenario(...) registers a scenario at package level
	if _, dup := scenarios[name]; dup {
		panic("scenario " + name + " registered twice")
	}
	scenarios[name] = fn
	return true
}

// TestScenario runs the scenario named by FAULTLINE_TEST_SCENARIO and does nothing otherwise.
func TestScenario(t *testing.T) {
	name := os.Getenv("FAULTLINE_TEST_SCENARIO")
	if name == "" {
		return
	}
	fn, ok := scenarios[name]
	if !ok {
		t.Fatalf("unknown scenario %q", name)
	}
	fn(t)
}

// scenarioVars are the variables scenarios read besides FAULTLINE_*; children never inherit them.
var scenarioVars = []string{"OUT", "KEEP", "QUIET", "LATE", "ROW", "FIXED", "GOEXIT"}

// scenarioTimeout bounds a scenario child when the parent test has no deadline (-timeout 0).
const scenarioTimeout = 10 * time.Minute

// runScenario runs scenario name in a child process (API §10): the parent environment minus
// every FAULTLINE_* variable and scenarioVars, plus FAULTLINE_TEST_SCENARIO,
// FAULTLINE_ARTIFACTS=<t.TempDir()>, plus env (later entries win). It returns the combined output
// and the exit code. A child built with -race exits without the race runtime's 1 s exit delay.
//
// A hung child fails the calling test and does not outlive it: the child stops itself with its
// own stack dump (-test.timeout) after a fifth of the time left before the parent's deadline, and
// is killed if it still runs at nine tenths of it. Without a deadline it gets scenarioTimeout.
func runScenario(t *testing.T, name string, env []string, args ...string) (string, int) { //nolint:unparam // AT-API-39 (Task 20c) passes args
	t.Helper()
	limit, kill := scenarioTimeout, scenarioTimeout+time.Minute
	if d, ok := t.Deadline(); ok {
		left := time.Until(d)
		limit, kill = left/5, left-left/10
	}
	ctx, cancel := context.WithTimeout(t.Context(), kill)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestScenario$", "-test.v", "-test.timeout=" + limit.String()}, args...)...) //nolint:gosec // runs this test binary again
	cmd.WaitDelay = 5 * time.Second
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "FAULTLINE_") && !slices.Contains(scenarioVars, k) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	cmd.Env = append(cmd.Env, "FAULTLINE_TEST_SCENARIO="+name, "FAULTLINE_ARTIFACTS="+t.TempDir())
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("scenario %s killed after %s: %v\n%s", name, kill, ctx.Err(), out)
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// seedK returns seed k of TestScenario's derived list (API-015 golden table).
func seedK(k int) uint64 { return faultline.DeriveSeed(faultline.NameBase("TestScenario"), k) }

// outLine appends line to the file named by the OUT environment variable.
func outLine(t *testing.T, line string) {
	f, err := os.OpenFile(os.Getenv("OUT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // OUT is a file the parent test chose
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(errors.Join(err, f.Close()))
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// readLines returns the lines of path (no trailing empty line).
func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// mustContainInOrder fails unless out contains every part, in order.
func mustContainInOrder(t *testing.T, out string, parts ...string) {
	t.Helper()
	rest := out
	for _, p := range parts {
		i := strings.Index(rest, p)
		if i < 0 {
			t.Fatalf("output lacks %q (in order):\n%s", p, out)
		}
		rest = rest[i+len(p):]
	}
}

// seedDir returns the artifact directory of seed under root for TestScenario.
func seedDir(root string, seed uint64) string {
	return artifact.Dir(root, "github.com/hmdsefi/faultline", "TestScenario", seed)
}

// readReport reads report.json in dir.
func readReport(t *testing.T, dir string) *artifact.Report {
	t.Helper()
	a, err := artifact.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &a.Report
}

// readTrace reads trace.jsonl in dir.
func readTrace(t *testing.T, dir string) []kernel.Record {
	t.Helper()
	a, err := artifact.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return a.Trace.Records
}

// resultLines decodes a FAULTLINE_RESULTS file: each line is one JSON object and nothing else.
func resultLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readLines(t, path) {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("results line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

// ---- toy systems (API §10) ----

// ticker is a server n1 whose boot schedules a 10 ms tick that increments count and re-arms.
type ticker struct {
	count   int
	panicAt int                         // the tick that assigns to a nil map; 0 = never
	onTick  func(n *kernel.Node, c int) // called after count is incremented
}

func addTicker(w *faultline.World) *ticker {
	tk := &ticker{}
	w.AddServer("n1", tk.boot)
	return tk
}

func (tk *ticker) boot(n *kernel.Node) {
	n.After(10*time.Millisecond, "tick", func() { tk.tick(n) })
}

func (tk *ticker) tick(n *kernel.Node) {
	tk.count++
	if tk.count == tk.panicAt {
		var m map[string]int
		m["x"] = 1 //nolint:staticcheck // the nil-map write is the panic under test
	}
	if tk.onTick != nil {
		tk.onTick(n, tk.count)
	}
	n.After(10*time.Millisecond, "tick", func() { tk.tick(n) })
}

// pingpong: a sends "ping" to b every 100 ms; b replies "pong"; both count deliveries.
type pingpong struct{ pings, pongs int }

func addPingPong(w *faultline.World) *pingpong {
	pp := &pingpong{}
	var b *kernel.Node
	w.AddServer("a", func(n *kernel.Node) {
		w.Net.Handle(n, func(from kernel.NodeID, payload any) { pp.pongs++ })
		var ping func()
		ping = func() {
			w.Net.Send(n, b.ID(), []byte("ping"))
			n.After(100*time.Millisecond, "ping", ping)
		}
		n.After(100*time.Millisecond, "ping", ping)
	})
	b = w.AddServer("b", func(n *kernel.Node) {
		w.Net.Handle(n, func(from kernel.NodeID, payload any) {
			pp.pings++
			w.Net.Send(n, from, []byte("pong"))
		})
	})
	return pp
}

// addSpin adds server n1 whose boot posts an event that re-posts itself at the same time.
func addSpin(w *faultline.World) {
	w.AddServer("n1", func(n *kernel.Node) {
		var f func()
		f = func() { n.Post("spin", f) }
		n.Post("spin", f)
	})
}

var _ = scenario("harness", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 2, Duration: time.Second, MaxEvents: 1000}, func(w *faultline.World) {
		outLine(t, fmt.Sprintf("0x%016x", w.Seed()))
		if w.Seed() == seedK(0) {
			pp := addPingPong(w)
			w.Final("pingpong", func() error {
				if pp.pings == 0 || pp.pongs == 0 {
					return fmt.Errorf("pings %d, pongs %d", pp.pings, pp.pongs)
				}
				return nil
			})
		} else {
			addSpin(w)
		}
	})
})

// TestScenarioHarness checks the harness (API §10) on one passing seed (pingpong, whose final
// check needs pings and pongs) and one failing seed (spin): the child's output, OUT, the artifact
// of the failing seed and the results file.
func TestScenarioHarness(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "harness", []string{"OUT=" + out, "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	s0, s1 := fmt.Sprintf("0x%016x", seedK(0)), fmt.Sprintf("0x%016x", seedK(1))
	dir := seedDir(root, seedK(1))
	mustContainInOrder(t, o, "=== RUN   TestScenario/seed="+s0+"\n", "=== RUN   TestScenario/seed="+s1+"\n",
		"faultline: run exceeded MaxEvents (1000) at t=0.000000000s (event 1000)\n", "artifacts: "+dir+string(os.PathSeparator)+"\n")
	// The primary attempt of each seed, then the artifact re-run of seed 1.
	if got := strings.Join(readLines(t, out), " "); got != s0+" "+s1+" "+s1 {
		t.Fatalf("body calls %s", got)
	}
	rep, trace := readReport(t, dir), readTrace(t, dir)
	if rep.Failure == nil || rep.Failure.Signature != "limit:max-events" || rep.Run.Attempts != 2 || len(trace) == 0 || trace[len(trace)-1].Text != "end" {
		t.Fatalf("report %+v %+v, %d records", rep.Failure, rep.Run, len(trace))
	}
	lines := resultLines(t, results)
	if len(lines) != 2 || lines[0]["seed"] != s0 || lines[0]["status"] != "pass" || lines[1]["seed"] != s1 || lines[1]["kind"] != "limit" || lines[1]["artifact"] != dir {
		t.Fatalf("results %v", lines)
	}
}
