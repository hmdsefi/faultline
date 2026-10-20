// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// epoch is the instant every synctest bubble starts at (GOR §3).
var epoch = kernel.Epoch

// gw runs body in goroutine mode with the GOR §10 fixture: Seeds 1, Duration 10 s, 1 ms links,
// unless opts sets them.
func gw(t *testing.T, opts Options, body func(w *World)) {
	t.Helper()
	opts.Mode = ModeGoroutine
	if opts.Seeds == 0 {
		opts.Seeds = 1
	}
	if opts.Duration == 0 {
		opts.Duration = 10 * time.Second
	}
	if reflect.ValueOf(opts.Net).IsZero() {
		opts.Net = simnet.Config{Default: simnet.Link{Latency: time.Millisecond}}
	}
	Run(t, opts, body)
}

// fullTrace makes the primary attempt retain every record.
func fullTrace(opts Options) Options {
	opts.Trace = kernel.TraceConfig{Level: kernel.TraceFull}
	return opts
}

// recordSink keeps, per seed, the records retained by the last attempt that ran its final checks.
type recordSink struct {
	mu   sync.Mutex
	recs map[uint64][]kernel.Record
}

func newRecordSink() *recordSink { return &recordSink{recs: map[uint64][]kernel.Record{}} }

// capture registers a final check that copies w's retained records (needs TraceFull).
func (s *recordSink) capture(w *World) {
	w.Final("capture records", func() error {
		rs := w.Sim.Records()
		s.mu.Lock()
		s.recs[w.Seed()] = rs
		s.mu.Unlock()
		return nil
	})
}

func (s *recordSink) get(seed uint64) []kernel.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recs[seed]
}

func (s *recordSink) seeds() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []uint64
	for seed := range s.recs { // test file: DET-016 does not lint it; sorted below
		out = append(out, seed)
	}
	slices.Sort(out)
	return out
}

func (s *recordSink) only(t *testing.T) []kernel.Record {
	t.Helper()
	seeds := s.seeds()
	if len(seeds) != 1 {
		t.Fatalf("records captured for %d seeds, want 1", len(seeds))
	}
	return s.get(seeds[0])
}

func attr(r kernel.Record, key string) string {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

func ofKind(rs []kernel.Record, kind string) []kernel.Record {
	var out []kernel.Record
	for _, r := range rs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// indexWhere returns the index of the first record satisfying pred, or -1.
func indexWhere(rs []kernel.Record, pred func(kernel.Record) bool) int {
	for i, r := range rs {
		if pred(r) {
			return i
		}
	}
	return -1
}

// errBox lets tests keep errors of different types in an atomic.Value.
type errBox struct{ err error } //nolint:unused // used by the tests that store a Process.Do error

// gorScenarios holds the child-process scenarios of plans 2.1–2.3, registered from init functions.
var gorScenarios = map[string]func(t *testing.T){}

// TestGoroutineScenario runs the scenario named by FAULTLINE_GOR_SCENARIO. It does nothing in a
// normal test run.
func TestGoroutineScenario(t *testing.T) {
	name := os.Getenv("FAULTLINE_GOR_SCENARIO")
	if name == "" {
		t.Skip("runs only as a child process of runGorScenario")
	}
	f := gorScenarios[name]
	if f == nil {
		t.Fatalf("unknown goroutine-mode scenario %q", name)
	}
	f(t)
}

type childResult struct {
	out       string // combined stdout and stderr
	code      int    // exit code
	artifacts string // FAULTLINE_ARTIFACTS of the child: the fresh directory unless env sets it
}

// runGorScenario runs scenario name in a child test process, the way runScenario
// (scenario_test.go, package faultline_test) does: the parent environment without any FAULTLINE_*
// variable, plus GORACE with atexit_sleep_ms=0 (no 1 s exit delay under -race),
// FAULTLINE_GOR_SCENARIO, FAULTLINE_ARTIFACTS=<fresh dir> and env (the text $ARTIFACTS in env is
// replaced by that directory; later entries win). The child runs up to 4 parallel subtests at
// once (-test.parallel=4) whatever its GOMAXPROCS, so subtests that wait for each other overlap on
// a 1-CPU host too. The child stops itself (-test.timeout) after a fifth of the time left before
// the parent's deadline and is killed at nine tenths of it; without a deadline it gets 10 minutes.
// Under go test -cover it adds its coverage to this binary's.
func runGorScenario(t *testing.T, name string, env ...string) childResult {
	t.Helper()
	root := t.TempDir()
	limit, kill := 10*time.Minute, 11*time.Minute
	if d, ok := t.Deadline(); ok {
		left := time.Until(d)
		limit, kill = left/5, left-left/10
	}
	ctx, cancel := context.WithTimeout(t.Context(), kill)
	defer cancel()
	args := append([]string{"-test.run=^TestGoroutineScenario$", "-test.v", "-test.count=1", "-test.parallel=4",
		"-test.timeout=" + limit.String()}, coverArgs()...)
	cmd := exec.CommandContext(ctx, os.Args[0], args...) //nolint:gosec // runs this test binary again
	cmd.WaitDelay = 5 * time.Second
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FAULTLINE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	cmd.Env = append(cmd.Env, "FAULTLINE_GOR_SCENARIO="+name, "FAULTLINE_ARTIFACTS="+root)
	artifacts := root
	for _, kv := range env {
		kv = strings.ReplaceAll(kv, "$ARTIFACTS", root)
		if dir, ok := strings.CutPrefix(kv, "FAULTLINE_ARTIFACTS="); ok {
			artifacts = dir
		}
		cmd.Env = append(cmd.Env, kv)
	}
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("scenario %s killed after %s: %v\n%s", name, kill, ctx.Err(), b.String())
	}
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatalf("running scenario %s: %v", name, err)
	}
	return childResult{out: b.String(), code: code, artifacts: artifacts}
}

// artifact reads the artifact directory of seed written by the child (API-074).
func (c childResult) artifact(t *testing.T, seed uint64) *artifact.Artifact {
	t.Helper()
	dir := artifact.Dir(c.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", seed)
	a, err := artifact.Read(dir)
	if err != nil {
		t.Fatalf("reading artifact %s: %v\n%s", dir, err, c.out)
	}
	return a
}

// results parses results.jsonl in c.artifacts, which the child writes when env sets
// FAULTLINE_RESULTS to that file, as "FAULTLINE_RESULTS=$ARTIFACTS/results.jsonl" does.
func (c childResult) results(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(c.artifacts, "results.jsonl"))
	if err != nil {
		t.Fatalf("results: %v\n%s", err, c.out)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("results line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("results: %v\n%s", err, c.out)
	}
	return out
}
