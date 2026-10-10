package etcdraft

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scenarios run in a child process (runScenario) because their faultline.Run fails or
// because the parent inspects the child's raw stdout and stderr. Each calls
// faultline.Run (directly or through Test) at most once.
var scenarios = map[string]func(t *testing.T){}

// TestScenario runs one scenario when ETCDRAFT_TEST_SCENARIO names it and does nothing
// otherwise.
func TestScenario(t *testing.T) {
	name := os.Getenv("ETCDRAFT_TEST_SCENARIO")
	if name == "" {
		return
	}
	f, ok := scenarios[name]
	if !ok {
		t.Fatalf("unknown scenario %q", name)
	}
	f(t)
}

// scenarioTimeout bounds a scenario child when the parent has no deadline: `-timeout 0`, or a
// benchmark (*testing.B has no Deadline).
const scenarioTimeout = 10 * time.Minute

// runScenario runs scenario name in a child process of this test binary with the
// parent environment minus every FAULTLINE_* and ETCDRAFT_* variable, plus
// ETCDRAFT_TEST_SCENARIO, FAULTLINE_ARTIFACTS=<temp dir>, and env. It returns the
// combined output and the exit code. A child built with -race exits without the race
// runtime's 1 s exit delay (GORACE atexit_sleep_ms=0).
//
// A hung child fails the calling test and does not outlive it. The child stops itself with
// its own stack dump (-test.timeout) after a fifth of the time left before the parent's
// deadline; args come after it, so a caller's -test.timeout=0 turns that off. Either way the
// child is killed when nine tenths of the time left have passed, as the root module's
// runScenario does. Without a deadline both limits come from scenarioTimeout.
func runScenario(t testing.TB, name string, env []string, args ...string) (string, int) {
	t.Helper()
	limit, kill := scenarioTimeout, scenarioTimeout+time.Minute
	if d, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if deadline, ok := d.Deadline(); ok {
			left := time.Until(deadline)
			limit, kill = left/5, left-left/10
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), kill)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestScenario$", "-test.timeout=" + limit.String()}, args...)...) //nolint:gosec // runs this test binary
	cmd.WaitDelay = 5 * time.Second
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FAULTLINE_") && !strings.HasPrefix(kv, "ETCDRAFT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	cmd.Env = append(cmd.Env, "ETCDRAFT_TEST_SCENARIO="+name, "FAULTLINE_ARTIFACTS="+t.TempDir())
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("scenario %s killed after %s: %v\n%s", name, kill, ctx.Err(), out)
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("running scenario %s: %v", name, err)
	return "", -1
}

// seedResult is the part of a FAULTLINE_RESULTS line (API-087) the tests read.
type seedResult struct {
	Seed      string `json:"seed"`
	Index     int    `json:"index"`
	Status    string `json:"status"`
	Kind      string `json:"kind"`
	Check     string `json:"check"`
	Signature string `json:"signature"`
	Message   string `json:"message"`
	TraceHash string `json:"trace_hash"`
	WallNS    int64  `json:"wall_ns"`
}

// resultsFile returns a fresh FAULTLINE_RESULTS path in a temp directory.
func resultsFile(t testing.TB) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "results.jsonl")
}

// readResults parses a FAULTLINE_RESULTS file. A child that failed before it wrote one (a bad
// scenario name, a panic in init) has no file: the result is nil, so the caller's check prints
// the child's output.
func readResults(t testing.TB, path string) []seedResult {
	t.Helper()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("results: %v", err)
	}
	defer f.Close()
	var out []seedResult
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r seedResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("results line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("results: %v", err)
	}
	return out
}

// hookWriter returns a selfTestHook that appends "<seed hex> <check>" to the file at path. The
// hook runs inside the simulation, where there is no t, so a failed write panics.
func hookWriter(path string) func(seed uint64, check string) {
	return func(seed uint64, check string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path is the parent test's own temp file
		if err == nil {
			_, err = fmt.Fprintf(f, "0x%016x %s\n", seed, check)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			panic(err)
		}
	}
}

// readHook returns the lines hookWriter wrote to path, or nil if the child wrote none.
func readHook(t testing.TB, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("hook output: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// tail returns the last 40 lines of s.
func tail(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}
