// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// builds holds the binaries that this process has checked or built, so that one command uses
// one build of each binary and reads its stamp once.
var builds = struct {
	sync.Mutex
	done map[string]bool
}{done: map[string]bool{}}

// binary returns the path of tc's test binary for c.backend (EXB-060) under <work>/bin, building
// it unless the stamp next to it (<binary>.stamp) equals the stamp of a build now (see build).
// A checkout with uncommitted changes gets a new build in every command. Each binary is checked
// once per command, so a commit made during a measurement does not change its binaries.
func (c *config) binary(tc testCase) (string, error) {
	name := strings.NewReplacer("/", "_", ".", "_").Replace(tc.Module + "_" + tc.Pkg)
	dir := filepath.Join(c.work, "bin", c.backend)
	bin := filepath.Join(dir, name+".test")
	if c.backend == "wasm" {
		bin = filepath.Join(dir, name+".wasm")
	}
	builds.Lock()
	defer builds.Unlock()
	if builds.done[bin] {
		return bin, nil
	}
	cmd, stamp, err := c.build(tc, bin)
	if err != nil {
		return "", err
	}
	old, err := os.ReadFile(bin + ".stamp")
	if _, serr := os.Stat(bin); err != nil || serr != nil || string(old) != stamp || strings.Contains(stamp, uncommitted) {
		_ = os.Remove(bin + ".stamp") // a failed build leaves neither file
		_ = os.Remove(bin)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("building %s %s for %s: %v\n%s", tc.Module, tc.Pkg, c.backend, err, out)
		}
		if err := os.WriteFile(bin+".stamp", []byte(stamp), 0o600); err != nil {
			return "", err
		}
	}
	builds.done[bin] = true
	return bin, nil
}

// build returns the go command that builds tc's binary bin for c.backend (EXB-060's Build column)
// and the build's stamp: the checkout's commit (checkoutCommit), go env GOVERSION and GOFLAGS in
// the module directory, the backend, the module directory and the command, and for toolexec the
// SHA-256 of the wrapper.
func (c *config) build(tc testCase, bin string) (*exec.Cmd, string, error) {
	modDir := filepath.Join(c.root, tc.Module)
	var env, args []string
	wrapper := ""
	switch c.backend {
	case "parta":
		args = []string{"test", "-c", "-o", bin, tc.Pkg}
	case "wasm":
		env, args = []string{"GOOS=wasip1", "GOARCH=wasm"}, []string{"test", "-c", "-o", bin, tc.Pkg}
	case "toolexec":
		tx := c.toolexec
		if tx == "" {
			gopath, err := exec.Command("go", "env", "GOPATH").Output()
			if err != nil {
				return nil, "", err
			}
			tx = filepath.Join(strings.TrimSpace(string(gopath)), "bin", "faultline-toolexec")
		}
		args = []string{"test", "-c", "-toolexec=" + tx, "-o", bin, tc.Pkg}
		wrapper = "toolexec unreadable\n" // the build then fails and says why
		if p, err := exec.LookPath(tx); err == nil {
			if b, err := os.ReadFile(p); err == nil {
				wrapper = fmt.Sprintf("toolexec %x\n", sha256.Sum256(b))
			}
		}
	default:
		return nil, "", fmt.Errorf("unknown backend %q", c.backend)
	}
	out, err := goCmd(modDir, env, "env", "GOVERSION", "GOFLAGS").Output()
	if err != nil {
		return nil, "", fmt.Errorf("go env in %s: %v", modDir, err)
	}
	version, flags, _ := strings.Cut(strings.TrimSuffix(string(out), "\n"), "\n")
	line := strings.Join(append(append(env, "go"), args...), " ")
	stamp := fmt.Sprintf("commit %s\ngo %s\ngoflags %s\nbackend %s\ndir %s\nbuild %s\n%s",
		checkoutCommit(c.root), version, flags, c.backend, modDir, line, wrapper)
	return goCmd(modDir, env, args...), stamp, nil
}

// uncommitted ends the commit of a checkout whose work tree differs from that commit.
const uncommitted = " with uncommitted changes"

// checkoutCommit returns the commit of the git checkout whose top directory is root, followed by
// uncommitted when git status lists a change, or "unknown" when root is not the top of a git
// checkout. Outside a checkout the other lines of a stamp decide alone.
func checkoutCommit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel", "HEAD").Output()
	top, head, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || head == "" {
		return "unknown"
	}
	a, errA := filepath.EvalSymlinks(top)
	b, errB := filepath.EvalSymlinks(root)
	if errA != nil || errB != nil || a != b {
		return "unknown"
	}
	status, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil || len(bytes.TrimSpace(status)) > 0 {
		return head + uncommitted
	}
	return head
}

// baseEnv is EXB-060's fixed environment, plus faultline-wasm's own configuration
// (FAULTLINE_WASM_*), which EXB-081 varies.
func baseEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE", "GOTOOLCHAIN"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "FAULTLINE_WASM_") {
			env = append(env, kv)
		}
	}
	return env
}

// runTimeout is the limit of one run (EXB §11); a variable so that tests can shorten it.
var runTimeout = 10 * time.Minute

// waitDelay bounds how long exec waits for a run's output to close after the run ended or was
// killed, in case a process the run started keeps it open; a variable so that tests can shorten
// it.
var waitDelay = 5 * time.Second

// runResult is one execution of a test binary.
type runResult struct {
	code     int
	out      string
	wall     time.Duration
	reason   string // why a run with code -1 ended: "timeout after <limit>" or the signal
	timedOut bool   // the run limit ended the run
}

// head is the first line of a run's log: "exit <code>", and for a run that the limit or a signal
// ended, why.
func (r runResult) head() string {
	if r.reason == "" {
		return fmt.Sprintf("exit %d", r.code)
	}
	return fmt.Sprintf("exit %d (%s)", r.code, r.reason)
}

// exec runs tc's binary in its package directory with baseEnv, tc.Env and env, under runTimeout
// (a timeout is a failed run, code -1). For wasm, faultline-wasm runs in single mode when
// env sets FAULTLINE_SEED and in plan/run mode otherwise (EXB-014). A run that exits but leaves
// a process holding its output open for waitDelay is a tool error.
func (c *config) exec(tc testCase, env []string) (runResult, error) {
	return c.execWithin(tc, env, runTimeout)
}

// execWithin is exec with the run limit limit instead of runTimeout.
func (c *config) execWithin(tc testCase, env []string, limit time.Duration) (runResult, error) {
	bin, err := c.binary(tc)
	if err != nil {
		return runResult{}, err
	}
	args := append([]string{"-test.run", tc.Run, "-test.count=1"}, tc.Args...)
	name := bin
	if c.backend == "wasm" {
		name, args = c.wasmRunner, append([]string{bin}, args...)
	}
	start := time.Now() // the deadline counts from here, so a run that the limit ended took at least limit
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(limit))
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = filepath.Join(c.root, tc.Module, tc.Pkg)
	cmd.Env = append(append(baseEnv(), tc.Env...), env...)
	cmd.WaitDelay = waitDelay
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err = cmd.Run()
	res := runResult{out: b.String(), wall: time.Since(start)}
	if ctx.Err() != nil {
		res.code, res.reason, res.timedOut = -1, fmt.Sprintf("timeout after %v", limit), true // a failed run
		return res, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		res.code = ee.ExitCode()
		if res.code == -1 {
			res.reason = ee.String() // ended by a signal, such as "signal: killed"
		}
	} else if errors.Is(err, exec.ErrWaitDelay) {
		return res, fmt.Errorf("%s: %s exited, but a process it started kept its output open: %w", tc.Name, bin, err)
	} else if err != nil {
		return res, err
	}
	return res, nil
}

// rawPath is the file of seed s, run r of tc in the work directory, with the given extension.
func (c *config) rawPath(tc testCase, s uint64, r int, ext string) string {
	return filepath.Join(c.work, c.backend, c.corpus, strings.ReplaceAll(tc.Name, "/", "_"),
		fmt.Sprintf("0x%016x-run%d.%s", s, r, ext))
}

// ensureRun runs seed s of tc once (run number r) unless -reuse is set and its results file
// exists. A run that produced no results file leaves an empty one, so it is not repeated.
func (c *config) ensureRun(tc testCase, s uint64, r int) error {
	res := c.rawPath(tc, s, r, "jsonl")
	if c.reuse {
		if _, err := os.Stat(res); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(res), 0o755); err != nil {
		return err
	}
	os.Remove(res)
	out := c.rawPath(tc, s, r, "out")
	os.Remove(out)
	rr, err := c.exec(tc, []string{
		fmt.Sprintf("FAULTLINE_SEED=0x%016x", s), "FAULTLINE_RESULTS=" + res, "EXB_OUT=" + out,
		"FAULTLINE_TRACE=full", "FAULTLINE_ARTIFACTS=off",
	})
	if err != nil {
		return err
	}
	if _, err := os.Stat(res); err != nil {
		if err := os.WriteFile(res, nil, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(c.rawPath(tc, s, r, "log"), []byte(rr.head()+"\n"+rr.out), 0o600)
}

// job is one run of one seed of one test.
type job struct {
	tc  testCase
	s   uint64
	run int
}

// ensureAll runs the jobs whose results are missing (all of them without -reuse): it builds
// their binaries first, then runs them with c.parallel workers. Exactness and sensitivity do not
// time runs, so runs may overlap. After a tool error it starts no more runs and returns the first
// error once the runs in progress have ended, so no instance outlives exbmeasure.
func (c *config) ensureAll(all []job) error {
	var jobs []job
	for _, j := range all {
		if c.reuse {
			if _, err := os.Stat(c.rawPath(j.tc, j.s, j.run, "jsonl")); err == nil {
				continue
			}
		}
		if _, err := c.binary(j.tc); err != nil {
			return err
		}
		jobs = append(jobs, j)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return first != nil
	}
	sem := make(chan struct{}, max(c.parallel, 1))
	for _, j := range jobs {
		sem <- struct{}{}
		if failed() {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := c.ensureRun(j.tc, j.s, j.run); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return first
}

// maxResultsLine is the longest results line readResults reads; a variable so that tests can
// shorten it.
var maxResultsLine = 1 << 26

// readResults parses a FAULTLINE_RESULTS file (API-087); a missing file has no lines, and a line
// longer than maxResultsLine is an error.
func readResults(path string) ([]map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, min(1<<20, maxResultsLine)), maxResultsLine)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// readOutcome returns the outcome field of the EXB_OUT line of tc ("" if absent).
func readOutcome(path, test string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) == 3 && f[0] == test {
			return f[2]
		}
	}
	return ""
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
