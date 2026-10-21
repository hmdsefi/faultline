// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
	"github.com/hmdsefi/faultline/shims/netsim"
)

// world runs body with NSM §10's fixture: goroutine mode, Seeds 1, Duration 60 s, 1 ms links,
// NetSim cfg, records retained (the fixture's FAULTLINE_TRACE=full).
func world(t *testing.T, cfg netsim.Config, opts faultline.Options, body func(w *faultline.World)) {
	t.Helper()
	opts.Mode = faultline.ModeGoroutine
	if opts.Seeds == 0 {
		opts.Seeds = 1
	}
	if opts.Duration == 0 {
		opts.Duration = 60 * time.Second
	}
	if reflect.ValueOf(opts.Net).IsZero() {
		opts.Net = simnet.Config{Default: simnet.Link{Latency: time.Millisecond}}
	}
	opts.NetSim = cfg
	opts.Trace = kernel.TraceConfig{Level: kernel.TraceFull}
	faultline.Run(t, opts, body)
}

// vnow is the virtual time of the calling process goroutine (processes start at t=0).
func vnow() time.Duration { return time.Since(kernel.Epoch) }

// records collects the records of the last attempt that reached its final checks.
type records struct {
	mu sync.Mutex
	rs []kernel.Record
}

func (r *records) capture(w *faultline.World) {
	w.Final("capture records", func() error {
		rs := w.Sim.Records()
		r.mu.Lock()
		r.rs = rs
		r.mu.Unlock()
		return nil
	})
}

func (r *records) all() []kernel.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rs
}

func (r *records) kind(kind string) []kernel.Record {
	var out []kernel.Record
	for _, x := range r.all() {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func attr(r kernel.Record, key string) string {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// match reports whether r has kind and every key=value pair of kv.
func match(r kernel.Record, kind string, kv ...string) bool {
	if r.Kind != kind {
		return false
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if attr(r, kv[i]) != kv[i+1] {
			return false
		}
	}
	return true
}

// inOrder fails unless every step matches a record, each after the previous one.
func inOrder(t *testing.T, rs []kernel.Record, steps ...func(kernel.Record) bool) {
	t.Helper()
	i := 0
	for n, step := range steps {
		for i < len(rs) && !step(rs[i]) {
			i++
		}
		if i == len(rs) {
			t.Fatalf("step %d of the expected record order not found", n)
		}
		i++
	}
}

// result is a thread-safe slot for a value produced by a process goroutine.
type result[T any] struct {
	mu  sync.Mutex
	v   T
	set bool
}

func (r *result[T]) put(v T) {
	r.mu.Lock()
	r.v, r.set = v, true
	r.mu.Unlock()
}

func (r *result[T]) get(t *testing.T) T {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.set {
		t.Fatal("value was never recorded")
	}
	return r.v
}

// timed is a value with the virtual time it was observed at.
type timed struct {
	at  time.Duration
	n   int
	err error
	s   string
}

// wait returns a process main that blocks until the run ends.
func wait(ctx context.Context, p *faultline.Process) error { <-ctx.Done(); return nil }

// childScenarios are child-process scenarios of this package (AT-NSM-35).
var childScenarios = map[string]func(t *testing.T){}

func TestNetsimScenario(t *testing.T) {
	name := os.Getenv("NETSIM_SCENARIO")
	if name == "" {
		t.Skip("runs only as a child process")
	}
	f := childScenarios[name]
	if f == nil {
		t.Fatalf("unknown netsim scenario %q", name)
	}
	f(t)
}

// runChild runs a scenario in a child test process, as the root package's runGorScenario does:
// the parent environment without any FAULTLINE_* variable, plus GORACE with atexit_sleep_ms=0
// (no 1 s exit delay under -race), NETSIM_SCENARIO, FAULTLINE_ARTIFACTS=<fresh dir> and env.
// The child stops itself (-test.timeout) after a fifth of the time left before the parent's
// deadline and is killed at nine tenths of it; without a deadline it gets 10 minutes. Under
// go test -cover it adds its coverage to this binary's.
func runChild(t *testing.T, name string, env ...string) (string, int) {
	t.Helper()
	limit, kill := 10*time.Minute, 11*time.Minute
	if d, ok := t.Deadline(); ok {
		left := time.Until(d)
		limit, kill = left/5, left-left/10
	}
	ctx, cancel := context.WithTimeout(t.Context(), kill)
	defer cancel()
	args := []string{"-test.run=^TestNetsimScenario$", "-test.v", "-test.count=1", "-test.timeout=" + limit.String()}
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}
	cmd := exec.CommandContext(ctx, os.Args[0], args...) //nolint:gosec // runs this test binary again
	cmd.WaitDelay = 5 * time.Second
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FAULTLINE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	cmd.Env = append(cmd.Env, "NETSIM_SCENARIO="+name, "FAULTLINE_ARTIFACTS="+t.TempDir())
	cmd.Env = append(cmd.Env, env...)
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("scenario %s killed after %s: %v\n%s", name, kill, ctx.Err(), b.String())
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return b.String(), ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.String(), 0
}
