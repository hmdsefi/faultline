// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/cryptotest"
	"time"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// unitRunner returns a runner for internal attempt tests, built as Run builds it but with
// options resolved without environment.
func unitRunner(tb testing.TB, opts Options, body func(*World)) *runner {
	tb.Helper()
	opts.NoCryptoSeed = true
	p, err := resolve(resolveInput{opts: opts, testName: "TestUnit", lookup: fakeEnv(nil), exploreBase: func() uint64 { return 1 }})
	if err != nil {
		tb.Fatal(err)
	}
	t, _ := tb.(*testing.T) // nil in benchmarks
	r := &runner{t: t, body: body, plan: p, build: readBuild(), results: resultsPath(fakeEnv(nil))}
	r.cwd, _ = os.Getwd()
	return r
}

func full() kernel.TraceConfig { return kernel.TraceConfig{Level: kernel.TraceFull} }

// unitTicker adds server n1 that ticks every 10 ms and calls onTick with the tick count.
func unitTicker(w *World, onTick func(n *kernel.Node, c int)) *int {
	count := new(int)
	w.AddServer("n1", func(n *kernel.Node) {
		var tick func()
		tick = func() {
			*count++
			if onTick != nil {
				onTick(n, *count)
			}
			n.After(10*time.Millisecond, "tick", tick)
		}
		n.After(10*time.Millisecond, "tick", tick)
	})
	return count
}

func kinds(records []kernel.Record, prefix string) []string {
	var out []string
	for _, r := range records {
		if strings.HasPrefix(r.Kind, prefix) {
			out = append(out, r.Kind+":"+r.Text)
		}
	}
	return out
}

// recordsOf returns the records of one kind, in trace order.
func recordsOf(records []kernel.Record, kind string) []kernel.Record {
	var out []kernel.Record
	for _, r := range records {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// attrsOf builds the attribute list key1=value1, key2=value2, ...
func attrsOf(kv ...string) []kernel.Attr {
	var out []kernel.Attr
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, kernel.Attr{Key: kv[i], Value: kv[i+1]})
	}
	return out
}

// expectMisuse calls fn and checks that it panics with the misuse message want.
func expectMisuse(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		v := recover()
		if m, ok := v.(misuse); !ok || m.msg != want {
			t.Errorf("panic %v, want misuse %q", v, want)
		}
	}()
	fn()
}

var (
	errC3  = errors.New("c=3")
	errBad = errors.New("bad")
	errF   = errors.New("F")
)

// recordingPlanner records the PlanContext of every Start.
type recordingPlanner struct{ ctxs []*fault.PlanContext }

func (p *recordingPlanner) Name() string                 { return "p" }
func (p *recordingPlanner) Start(ctx *fault.PlanContext) { p.ctxs = append(p.ctxs, ctx) }

// panicPlanner panics with value in Start.
type panicPlanner struct {
	name  string
	value any
}

func (p panicPlanner) Name() string             { return p.name }
func (p panicPlanner) Start(*fault.PlanContext) { panic(p.value) }

// funcPlanner runs fn in Start.
type funcPlanner struct {
	name string
	fn   func(*fault.PlanContext)
}

func (p funcPlanner) Name() string                 { return p.name }
func (p funcPlanner) Start(ctx *fault.PlanContext) { p.fn(ctx) }

// namePanicPlanner panics in Name.
type namePanicPlanner struct{}

func (namePanicPlanner) Name() string             { panic("no name") }
func (namePanicPlanner) Start(*fault.PlanContext) {}

// API-030, API-032, API-045, API-074, API-093, §8 run.phase records. The post-body drive also
// runs when body already ran to End() (API-045: Now() <= End()).
func TestAttemptPhases(t *testing.T) {
	for _, bodyRuns := range []bool{false, true} {
		r := unitRunner(t, Options{Seeds: 1, Duration: 50 * time.Millisecond}, func(w *World) {
			unitTicker(w, nil)
			w.Final("ok", func() error { return nil })
			if bodyRuns {
				w.RunFor(50 * time.Millisecond)
			}
		})
		res := r.attempt(t, 7, full(), nil, true)
		if res.setupErr != "" || res.fail != nil {
			t.Fatalf("bodyRuns=%v: attempt: %+v", bodyRuns, res)
		}
		if got := kinds(res.records, "run."); !slices.Equal(got, []string{"run.phase:setup", "run.phase:run", "run.phase:final", "run.phase:end"}) {
			t.Fatalf("bodyRuns=%v: run records %v", bodyRuns, got)
		}
		setup := recordsOf(res.records, "run.phase")[0]
		want := attrsOf("phase", "setup", "seed", "0x0000000000000007", "duration_ns", "50000000", "max_events", "10000000", "mode", "event")
		if setup.Node != 0 || !slices.Equal(setup.Attrs, want) {
			t.Fatalf("bodyRuns=%v: setup record %+v", bodyRuns, setup)
		}
		end := res.records[len(res.records)-1]
		if !slices.Equal(end.Attrs, attrsOf("phase", "end", "stop", "deadline", "events", "6")) {
			t.Fatalf("bodyRuns=%v: end record %+v", bodyRuns, end)
		}
		if res.now != kernel.Time(50*time.Millisecond) || res.executed != 6 || res.stop != "deadline" || res.hash != kernel.HashRecords(res.records) || res.lastSeq != end.Seq {
			t.Fatalf("bodyRuns=%v: result %+v", bodyRuns, res)
		}
		if res.schedule.End != kernel.Time(50*time.Millisecond) || res.schedule.Recovery != kernel.Time(50*time.Millisecond) || res.hasRecovery {
			t.Fatalf("bodyRuns=%v: schedule %+v, hasRecovery %v", bodyRuns, res.schedule, res.hasRecovery)
		}
		if len(res.nodes) != 1 || res.nodes[0].Name != "n1" || !slices.Equal(res.nodes[0].Tags, []string{"server"}) {
			t.Fatalf("bodyRuns=%v: nodes %+v", bodyRuns, res.nodes)
		}
		if res.history != nil { // no operations: no history.jsonl (API-074)
			t.Fatalf("bodyRuns=%v: history %q", bodyRuns, res.history)
		}
		// The attempt's trace config reaches the kernel, and the same seed gives the same hash at
		// every trace level (API §6).
		if again := r.attempt(t, 7, kernel.TraceConfig{}, nil, false); again.hash != res.hash || again.records != nil {
			t.Fatalf("bodyRuns=%v: TraceHash: hash %x != %x or %d records kept", bodyRuns, again.hash, res.hash, len(again.records))
		}
		ring := r.attempt(t, 7, kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 3}, nil, false)
		if ring.hash != res.hash || len(ring.records) != 3 || ring.records[2].Seq != end.Seq {
			t.Fatalf("bodyRuns=%v: Buffer 3: hash %x, %d records", bodyRuns, ring.hash, len(ring.records))
		}
	}
}

// API-030 step 2, §7.1: unless NoCryptoSeed, crypto/rand is seeded with the first Uint64 of the
// stream "crypto". In a parallel test that is the crypto setup error; a construction panic after
// seeding is still a world construction error.
func TestAttemptCryptoSeed(t *testing.T) {
	t.Run("seeded", func(st *testing.T) {
		var got [16]byte
		r := unitRunner(st, Options{Seeds: 1, Duration: time.Second}, func(w *World) { _, _ = cryptorand.Read(got[:]) })
		r.plan.opts.NoCryptoSeed = false
		if res := r.attempt(st, 1, full(), nil, true); res.setupErr != "" {
			st.Fatal(res.setupErr)
		}
		cryptotest.SetGlobalRandom(st, kernel.New(kernel.Config{Seed: 1}).Rand("crypto").Uint64())
		var want [16]byte
		_, _ = cryptorand.Read(want[:])
		if got != want {
			st.Errorf("crypto bytes %x, want %x", got, want)
		}
	})
	t.Run("construction", func(st *testing.T) {
		r := unitRunner(st, Options{Seeds: 1, Duration: time.Second, Net: simnet.Config{Default: simnet.Link{Latency: -1}}}, func(w *World) {})
		r.plan.opts.NoCryptoSeed = false
		if res := r.attempt(st, 1, full(), nil, true); !strings.HasPrefix(res.setupErr, "faultline: world construction for seed 0x0000000000000001: ") {
			st.Errorf("setupErr %q", res.setupErr)
		}
	})
	t.Run("parallel", func(st *testing.T) {
		st.Parallel()
		r := unitRunner(st, Options{Seeds: 1, Duration: time.Second}, func(w *World) {})
		r.plan.opts.NoCryptoSeed = false
		res := r.attempt(st, 1, full(), nil, true)
		if !strings.HasPrefix(res.setupErr, "faultline: crypto seeding: ") || !strings.HasSuffix(res.setupErr, " (Run cannot be used in a parallel test; remove t.Parallel or set Options.NoCryptoSeed)") {
			st.Errorf("setupErr %q", res.setupErr)
		}
	})
}

// §7.1 seed-level setup errors (API-030, API-042, API-043): each ends the attempt with no failure.
func TestAttemptSetupErrors(t *testing.T) {
	const advanced = "faultline: body advanced the simulation with w.Sim before calling w.RunFor; use w.RunFor so planners start at time 0"
	cases := []struct {
		name   string
		opts   Options
		body   func(w *World)
		want   string
		prefix bool // the message goes on with a stack or a kernel error
	}{
		{"misuse in body", Options{}, func(w *World) { unitTicker(w, nil); w.RunFor(-1) }, "faultline: World.RunFor: negative duration -1ns", false},
		{"panic before the first event", Options{}, func(w *World) { unitTicker(w, nil); panic("boom") },
			"faultline: body panicked during setup (before the first event) for seed 0x0000000000000001: boom\ngoroutine ", true},
		{"Step, then RunFor", Options{}, func(w *World) { unitTicker(w, nil); w.Sim.Step(); w.RunFor(time.Millisecond) }, advanced, false},
		{"Step, then return", Options{}, func(w *World) { unitTicker(w, nil); w.Sim.Step() }, advanced, false},
		{"time advanced, no event", Options{}, func(w *World) { w.Sim.RunUntil(5) }, advanced, false},
		{"planner panics in RunFor", Options{}, func(w *World) { w.Plan(panicPlanner{"q", "nope"}); w.RunFor(time.Millisecond) }, "faultline: planner start: nope", false},
		{"planner panics after body", Options{}, func(w *World) { w.Plan(panicPlanner{"q", "nope"}) }, "faultline: planner start: nope", false},
		{"late planner panics", Options{}, func(w *World) { w.RunFor(time.Millisecond); w.Plan(panicPlanner{"q", "nope"}) }, "faultline: planner start: nope", false},
		{"planner panic text", Options{}, func(w *World) { w.Plan(panicPlanner{"v", new(int)}) }, "faultline: planner start: *int", false},
		{"invalid planner name", Options{}, func(w *World) { w.Plan(panicPlanner{"Bad", "nope"}) }, `faultline: planner start: fault: NewPlanContext: invalid planner name "Bad"`, false},
		{"construction", Options{Net: simnet.Config{Default: simnet.Link{Latency: -1}}}, func(w *World) {},
			"faultline: world construction for seed 0x0000000000000001: simnet: New: invalid Config.Default: ", true},
		{"disk construction", Options{Disk: simdisk.Config{SectorSize: -1}}, func(w *World) {},
			"faultline: world construction for seed 0x0000000000000001: simdisk: New: ", true},
		{"misuse in a planner's Start", Options{}, func(w *World) { w.Plan(funcPlanner{"m", func(*fault.PlanContext) { w.Rand("") }}) },
			"faultline: World.Rand: empty label", false},
	}
	for _, c := range cases {
		c.opts.Seeds, c.opts.Duration = 1, time.Second
		res := unitRunner(t, c.opts, c.body).attempt(t, 1, full(), nil, true)
		ok := res.setupErr == c.want
		if c.prefix {
			ok = strings.HasPrefix(res.setupErr, c.want)
		}
		if !ok || res.fail != nil {
			t.Errorf("%s: setupErr %q, fail %+v", c.name, res.setupErr, res.fail)
		}
	}
	// A panic after time advanced, with no event executed, is a body panic, not a setup error.
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) { w.RunFor(time.Second); panic("late") })
	if res := r.attempt(t, 1, full(), nil, true); res.setupErr != "" || res.fail == nil || res.fail.context != "panic-body" {
		t.Errorf("no events: setupErr %q, fail %+v", res.setupErr, res.fail)
	}
}

// API-061 rows 1 to 7 and 9 (row 8 is AT-API-26), with API-063 to API-067 and §8's check.violation
// records.
func TestAttemptClassification(t *testing.T) {
	type violation struct {
		text  string
		attrs []kernel.Attr
	}
	cases := []struct {
		name       string
		opts       Options
		body       func(w *World)
		kind       string
		check      string // for kind panic: the panic site, checked by its package prefix
		in         string // artifact.Panic.In, "" for no panic
		node       string // n1 is node 1
		headline   string
		message    string
		err        error // failure.err (API-077) must match it with errors.Is
		violations []violation
	}{
		{name: "invariant", body: func(w *World) {
			c := unitTicker(w, nil)
			w.Invariant("lt3", func() error {
				if *c >= 3 {
					return errC3
				}
				return nil
			})
		}, kind: "invariant", check: "lt3", node: "n1", headline: `invariant "lt3" violated at t=0.030000000s on n1 (event 4)`, message: "c=3", err: errC3,
			violations: []violation{{`invariant "lt3" violated`, attrsOf("kind", "invariant", "check", "lt3", "error", "c=3", "event", "4")}}},
		{name: "invariant panic", body: func(w *World) {
			unitTicker(w, nil)
			w.Invariant("boom", func() error { panic("bang") })
		}, kind: "panic", in: "invariant", node: "n1", headline: `panic in invariant "boom" at t=0.000000000s on n1 (event 1)`, message: "bang",
			violations: []violation{{"panic in invariant", attrsOf("kind", "panic", "in", "invariant", "name", "boom", "error", "bang", "event", "1")}}},
		{name: "callback panic", body: func(w *World) {
			unitTicker(w, func(n *kernel.Node, c int) {
				if c == 2 {
					panic(errors.New("cb"))
				}
			})
		}, kind: "panic", in: "callback", node: "n1", headline: `panic at t=0.020000000s on n1 (event 3)`, message: "cb",
			violations: []violation{{"panic in callback", attrsOf("kind", "panic", "in", "callback", "error", "cb", "event", "3")}}},
		{name: "global event panic", body: func(w *World) {
			w.Sim.After(10*time.Millisecond, "g", func() { panic("global") })
		}, kind: "panic", in: "callback", headline: `panic at t=0.010000000s (event 1)`, message: "global",
			violations: []violation{{"panic in callback", attrsOf("kind", "panic", "in", "callback", "error", "global", "event", "1")}}},
		{name: "fail in body", body: func(w *World) {
			unitTicker(w, nil)
			w.RunFor(20 * time.Millisecond)
			w.Sim.Fail(errBad)
		}, kind: "fail", headline: `simulation failed in body at t=0.020000000s`, message: "bad", err: errBad,
			violations: []violation{{"simulation failed", attrsOf("kind", "fail", "error", "bad")}}},
		{name: "fail before RunFor", body: func(w *World) {
			unitTicker(w, nil)
			w.Sim.Fail(errBad)
			w.RunFor(10 * time.Millisecond)
		}, kind: "fail", headline: `simulation failed in body at t=0.000000000s`, message: "bad", err: errBad,
			violations: []violation{{"simulation failed", attrsOf("kind", "fail", "error", "bad")}}},
		{name: "fail in loop", body: func(w *World) {
			unitTicker(w, func(n *kernel.Node, c int) { n.Sim().Fail(errBad) })
		}, kind: "fail", node: "n1", headline: `simulation failed at t=0.010000000s on n1 (event 2)`, message: "bad", err: errBad,
			violations: []violation{{"simulation failed", attrsOf("kind", "fail", "error", "bad", "event", "2")}}},
		{name: "limit", opts: Options{MaxEvents: 5}, body: func(w *World) { unitTicker(w, nil) },
			kind: "limit", check: "max-events", node: "n1", headline: `run exceeded MaxEvents (5) at t=0.040000000s (event 5)`,
			message:    "likely a livelock or a runaway timer; raise Options.MaxEvents or set Options.AllowLimit",
			violations: []violation{{"MaxEvents exceeded", attrsOf("kind", "limit", "limit", "max-events", "value", "5", "event", "5")}}},
		{name: "final", body: func(w *World) {
			unitTicker(w, nil)
			w.Final("f", func() error { return errF })
			w.Final("g", func() error { panic("G") })
		}, kind: "final", check: "f", headline: `final check "f" failed after the run at t=1.000000000s`, message: "F\nalso failed: \"g\"", err: errF,
			violations: []violation{
				{`final check "f" failed`, attrsOf("kind", "final", "check", "f", "error", "F")},
				{"panic in final", attrsOf("kind", "panic", "in", "final", "name", "g", "error", "G")},
			}},
		{name: "body panic", body: func(w *World) {
			unitTicker(w, nil)
			w.RunFor(time.Second)
			panic("late")
		}, kind: "panic", in: "body", headline: `panic in body at t=1.000000000s`, message: "late",
			violations: []violation{{"panic in body", attrsOf("kind", "panic", "in", "body", "error", "late")}}},
		{name: "body panic at t=0 after the first event", body: func(w *World) {
			unitTicker(w, nil)
			w.RunFor(0) // runs n1's boot at t=0
			panic("zero")
		}, kind: "panic", in: "body", headline: `panic in body at t=0.000000000s`, message: "zero",
			violations: []violation{{"panic in body", attrsOf("kind", "panic", "in", "body", "error", "zero")}}},
		{name: "fail between RunFor calls", body: func(w *World) {
			unitTicker(w, nil)
			w.RunFor(20 * time.Millisecond)
			w.Sim.Fail(errBad)
			w.RunFor(10 * time.Millisecond)
		}, kind: "fail", headline: `simulation failed in body at t=0.020000000s`, message: "bad", err: errBad,
			violations: []violation{{"simulation failed", attrsOf("kind", "fail", "error", "bad")}}},
		{name: "planner panics in a callback", body: func(w *World) {
			unitTicker(w, func(n *kernel.Node, c int) {
				if c == 2 {
					w.Plan(panicPlanner{"q", "nope"}) // starts inside the loop: the kernel recovers it
				}
			})
		}, kind: "panic", in: "callback", node: "n1", headline: `panic at t=0.020000000s on n1 (event 3)`, message: "nope",
			violations: []violation{{"panic in callback", attrsOf("kind", "panic", "in", "callback", "error", "nope", "event", "3")}}},
	}
	for _, c := range cases {
		c.opts.Seeds, c.opts.Duration = 1, time.Second
		r := unitRunner(t, c.opts, c.body)
		res := r.attempt(t, 1, full(), nil, true)
		f := res.fail
		if f == nil {
			t.Errorf("%s: passed (setupErr %q)", c.name, res.setupErr)
			continue
		}
		if f.kind != c.kind || f.headline() != c.headline || f.message != c.message || !errors.Is(f.err, c.err) {
			t.Errorf("%s: kind %q headline %q message %q err %v", c.name, f.kind, f.headline(), f.message, f.err)
		}
		if f.kind == "panic" {
			if !strings.HasPrefix(f.check, "github.com/hmdsefi/faultline.") {
				t.Errorf("%s: panic site %q", c.name, f.check)
			}
		} else if f.check != c.check {
			t.Errorf("%s: check %q, want %q", c.name, f.check, c.check)
		}
		wantID := kernel.NodeID(0)
		if c.node != "" {
			wantID = 1
		}
		if f.node != c.node || f.nodeID != wantID {
			t.Errorf("%s: node %d %q, want %d %q", c.name, f.nodeID, f.node, wantID, c.node)
		}
		if c.in == "" {
			if f.panic != nil {
				t.Errorf("%s: panic %+v", c.name, f.panic)
			}
		} else if p := f.panic; p == nil || p.In != c.in || p.Name != f.name || p.Value != f.message || p.Site != f.check || !strings.HasPrefix(p.Stack, "goroutine ") {
			t.Errorf("%s: panic %+v", c.name, p)
		}
		v := recordsOf(res.records, "check.violation")
		if len(v) != len(c.violations) {
			t.Errorf("%s: check.violation records %+v", c.name, v)
			continue
		}
		for i, want := range c.violations {
			if v[i].Text != want.text || !slices.Equal(v[i].Attrs, want.attrs) || v[i].Node != 0 || v[i].Inc != 0 {
				t.Errorf("%s: check.violation %d: %+v", c.name, i, v[i])
			}
		}
		if f.recordSeq != v[0].Seq {
			t.Errorf("%s: record_seq %d, want %d", c.name, f.recordSeq, v[0].Seq)
		}
	}

	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second, MaxEvents: 5, AllowLimit: true}, func(w *World) {
		unitTicker(w, nil)
		w.Final("never", func() error { return errors.New("ran") })
	})
	if res := r.attempt(t, 1, full(), nil, true); res.fail != nil || !res.limited {
		t.Fatalf("AllowLimit: %+v", res.fail)
	}

	// Without a trace a callback panic still names its node, from kernel.PanicError (API-066).
	r = unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		unitTicker(w, func(n *kernel.Node, c int) {
			if c == 2 {
				panic("cb")
			}
		})
	})
	if f := r.attempt(t, 1, kernel.TraceConfig{}, nil, true).fail; f == nil || f.nodeID != 1 || f.headline() != "panic at t=0.020000000s on n1 (event 3)" {
		t.Errorf("TraceHash: %+v", f)
	}
}

// API-056: invariants run after every event in registration order. The first one that fails or
// panics emits check.violation and fails the run with the hashed Error() text; no other invariant
// runs after it.
func TestAttemptInvariantDispatch(t *testing.T) {
	var order []string
	r := unitRunner(t, Options{Seeds: 1, Duration: 50 * time.Millisecond}, func(w *World) {
		unitTicker(w, nil)
		w.Invariant("a", func() error { order = append(order, "a"); return nil })
		w.Invariant("b", func() error { order = append(order, "b"); return nil })
	})
	res := r.attempt(t, 1, full(), nil, true)
	if res.fail != nil || res.executed != 6 || !slices.Equal(order, slices.Repeat([]string{"a", "b"}, 6)) {
		t.Fatalf("order %v, executed %d, fail %+v", order, res.executed, res.fail)
	}
	for _, panics := range []bool{false, true} {
		var calls []string
		r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
			unitTicker(w, nil)
			w.Invariant("x", func() error {
				calls = append(calls, "x")
				if panics {
					panic("X")
				}
				return errors.New("X")
			})
			w.Invariant("y", func() error { calls = append(calls, "y"); return errors.New("Y") })
		})
		res := r.attempt(t, 1, full(), nil, true)
		wantFail := `invariant "x": X`
		if panics {
			wantFail = `panic in invariant "x": X`
		}
		v := recordsOf(res.records, "check.violation")
		fails := recordsOf(res.records, "kernel.fail")
		if !slices.Equal(calls, []string{"x"}) || res.fail == nil || res.fail.message != "X" || len(v) != 1 || len(fails) != 1 || fails[0].Text != wantFail || fails[0].Seq != v[0].Seq+1 {
			t.Errorf("panics=%v: calls %v, fail %+v, check.violation %+v, kernel.fail %+v", panics, calls, res.fail, v, fails)
		}
	}
}

// API-057, API-066 (Spec issues found item 8): every final check runs once, in order; when the
// first failing one panicked the kind is panic, and the others are still listed and named in an
// "also failed:" line.
func TestAttemptFinalPanicFirst(t *testing.T) {
	var calls []string
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		unitTicker(w, nil)
		w.Final("p", func() error { calls = append(calls, "p"); panic("P") })
		w.Final("e", func() error { calls = append(calls, "e"); return errors.New("E") })
		w.Final("ok", func() error { calls = append(calls, "ok"); return nil })
	})
	res := r.attempt(t, 1, full(), nil, true)
	f := res.fail
	if f == nil {
		t.Fatal("passed")
	}
	if f.kind != "panic" || f.context != "panic-final" || f.name != "p" || f.message != "P\nalso failed: \"e\"" ||
		f.headline() != `panic in final check "p" after the run at t=1.000000000s` || !strings.HasPrefix(f.check, "github.com/hmdsefi/faultline.") {
		t.Errorf("failure %q %q %q %q %q %q", f.kind, f.context, f.name, f.message, f.headline(), f.check)
	}
	if p := f.panic; p == nil || p.In != "final" || p.Name != "p" || p.Value != "P" || p.Site != f.check {
		t.Errorf("panic %+v", p)
	}
	if len(f.finals) != 2 || f.finals[0].check != "p" || f.finals[0].panic == nil || f.finals[1].check != "e" || f.finals[1].message != "E" || f.finals[1].err == nil {
		t.Errorf("finals %+v", f.finals)
	}
	if !slices.Equal(calls, []string{"p", "e", "ok"}) {
		t.Errorf("calls %v", calls)
	}
	v := recordsOf(res.records, "check.violation")
	if len(v) != 2 || v[0].Text != "panic in final" || !slices.Equal(v[0].Attrs, attrsOf("kind", "panic", "in", "final", "name", "p", "error", "P")) ||
		v[1].Text != `final check "e" failed` || !slices.Equal(v[1].Attrs, attrsOf("kind", "final", "check", "e", "error", "E")) {
		t.Errorf("check.violation %+v", v)
	}
	if len(v) > 0 && f.recordSeq != v[0].Seq {
		t.Errorf("record_seq %d, want %d", f.recordSeq, v[0].Seq)
	}
	if fin := recordsOf(res.records, "run.phase")[2]; !slices.Equal(fin.Attrs, attrsOf("phase", "final", "checks", "3")) {
		t.Errorf("final record %+v", fin)
	}
}

// API-045, API-057, API-061 row 1: after Sim.Fail or a panic in body, Run neither drives nor runs
// the final checks, and a body panic wins over Sim.Err().
func TestAttemptStopsAfterBodyFailure(t *testing.T) {
	cases := []struct {
		name    string
		body    func(w *World)
		context string
	}{
		{"fail in body", func(w *World) { w.Sim.Fail(errBad) }, "fail-body"},
		{"body panic", func(w *World) { panic("late") }, "panic-body"},
		{"fail, then panic", func(w *World) { w.Sim.Fail(errBad); panic("late") }, "panic-body"},
	}
	for _, c := range cases {
		finalRan := false
		r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
			unitTicker(w, nil)
			w.Final("f", func() error { finalRan = true; return nil })
			w.RunFor(20 * time.Millisecond)
			c.body(w)
		})
		res := r.attempt(t, 1, full(), nil, true)
		if res.fail == nil || res.fail.context != c.context {
			t.Errorf("%s: fail %+v", c.name, res.fail)
		}
		if got := kinds(res.records, "run.phase"); finalRan || !slices.Equal(got, []string{"run.phase:setup", "run.phase:end"}) ||
			res.now != kernel.Time(20*time.Millisecond) || res.executed != 3 {
			t.Errorf("%s: final ran %v, records %v, now %s, executed %d", c.name, finalRan, got, res.now, res.executed)
		}
	}
}

// API-041 step 5: a failure or a limit inside RunFor stops body; Run does not drive again and runs
// no final checks.
func TestAttemptRunForStopsBody(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		build   func(w *World)
		context string
		stop    string
		limited bool
	}{
		{"invariant", Options{}, func(w *World) {
			c := unitTicker(w, nil)
			w.Invariant("lt3", func() error {
				if *c >= 3 {
					return errC3
				}
				return nil
			})
		}, "invariant", "failed", false},
		{"Sim.Fail", Options{}, func(w *World) {
			unitTicker(w, func(n *kernel.Node, c int) { n.Sim().Fail(errBad) })
		}, "fail-loop", "failed", false},
		{"limit", Options{MaxEvents: 5}, func(w *World) { unitTicker(w, nil) }, "limit", "max-events", false},
		{"limit allowed", Options{MaxEvents: 5, AllowLimit: true}, func(w *World) { unitTicker(w, nil) }, "", "max-events", true},
	}
	for _, c := range cases {
		c.opts.Seeds, c.opts.Duration = 1, time.Second
		returned, finalRan := false, false
		r := unitRunner(t, c.opts, func(w *World) {
			c.build(w)
			w.Final("f", func() error { finalRan = true; return nil })
			w.RunFor(time.Second)
			returned = true
		})
		res := r.attempt(t, 1, full(), nil, true)
		context := ""
		if res.fail != nil {
			context = res.fail.context
		}
		phases := kinds(res.records, "run.phase")
		if returned || finalRan || context != c.context || res.stop != c.stop || res.limited != c.limited || !slices.Equal(phases, []string{"run.phase:setup", "run.phase:end"}) {
			t.Errorf("%s: returned %v, final ran %v, context %q, stop %q, limited %v, records %v", c.name, returned, finalRan, context, res.stop, res.limited, phases)
		}
	}
}

// API-041 step 5, API-042: once RunFor stopped body, body code that still runs cannot change the
// failure. After a recover, a deferred RunFor (also one past the end), or a deferred panic or
// misuse, the loop's failure stays as it was, with its event and node.
func TestAttemptStopIsSticky(t *testing.T) {
	failAt3 := func(w *World) {
		unitTicker(w, func(n *kernel.Node, c int) {
			if c == 3 {
				n.Sim().Fail(errBad)
			}
		})
	}
	lt3 := func(w *World) {
		c := unitTicker(w, nil)
		w.Invariant("lt3", func() error {
			if *c >= 3 {
				return errC3
			}
			return nil
		})
	}
	recovering := func(w *World) { // as a helper that swallows panics would
		defer func() { _ = recover() }()
		w.RunFor(500 * time.Millisecond)
	}
	const failed = "simulation failed at t=0.030000000s on n1 (event 4)"
	const violated = `invariant "lt3" violated at t=0.030000000s on n1 (event 4)`
	cases := []struct {
		name     string
		opts     Options
		body     func(w *World)
		headline string
		stop     string
	}{
		{"recover, then return", Options{}, func(w *World) { failAt3(w); recovering(w) }, failed, "failed"},
		{"recover, then RunFor", Options{}, func(w *World) { failAt3(w); recovering(w); w.RunFor(100 * time.Millisecond) }, failed, "failed"},
		{"deferred RunFor", Options{}, func(w *World) { failAt3(w); defer w.RunFor(100 * time.Millisecond); w.RunFor(500 * time.Millisecond) }, failed, "failed"},
		{"deferred RunFor past the end", Options{}, func(w *World) { failAt3(w); defer w.RunFor(2 * time.Second); w.RunFor(500 * time.Millisecond) }, failed, "failed"},
		{"deferred panic", Options{}, func(w *World) { lt3(w); defer func() { panic("cleanup") }(); w.RunFor(500 * time.Millisecond) }, violated, "failed"},
		{"deferred misuse", Options{}, func(w *World) { lt3(w); defer func() { w.Invariant("", nil) }(); w.RunFor(500 * time.Millisecond) }, violated, "failed"},
		{"limit, recovered", Options{MaxEvents: 5}, func(w *World) { unitTicker(w, nil); recovering(w) },
			"run exceeded MaxEvents (5) at t=0.040000000s (event 5)", "max-events"},
	}
	for _, c := range cases {
		c.opts.Seeds, c.opts.Duration = 1, time.Second
		res := unitRunner(t, c.opts, c.body).attempt(t, 1, full(), nil, true)
		headline := ""
		if res.fail != nil {
			headline = res.fail.headline()
		}
		phases := kinds(res.records, "run.phase")
		if res.setupErr != "" || headline != c.headline || res.stop != c.stop || !slices.Equal(phases, []string{"run.phase:setup", "run.phase:end"}) {
			t.Errorf("%s: setupErr %q, headline %q, stop %q, records %v", c.name, res.setupErr, headline, res.stop, phases)
		}
	}
}

// API-043, API-044, §8 run.plan: planners start in registration order when faults start, with the
// servers, the roles and until(p); a planner registered later starts inside Plan. RecoveryStart is
// the earliest max(until, start time) over the *fault.Random planners.
func TestAttemptPlanners(t *testing.T) {
	p := &recordingPlanner{}
	rnd := &fault.Random{}                               // until 750 ms
	late := &fault.Random{Quiet: 950 * time.Millisecond} // until 50 ms, started at 100 ms
	var inBody, inFinal kernel.Time
	var w0 *World
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		w0 = w
		unitTicker(w, nil)
		w.AddClient("c", func(*kernel.Node) {})
		w.Role("leader", func() []kernel.NodeID { return []kernel.NodeID{1} })
		w.Plan(p)
		w.Plan(rnd)
		inBody = w.RecoveryStart()
		w.RunFor(100 * time.Millisecond)
		w.Plan(late)
		w.Final("f", func() error { inFinal = w.RecoveryStart(); return nil })
	})
	res := r.attempt(t, 1, full(), nil, true)
	if res.setupErr != "" || res.fail != nil {
		t.Fatalf("attempt %+v", res)
	}
	if len(p.ctxs) != 1 || p.ctxs[0].Until != kernel.Time(time.Second) || p.ctxs[0].Roles["leader"] == nil ||
		len(p.ctxs[0].Servers) != 1 || p.ctxs[0].Servers[0].Name() != "n1" || p.ctxs[0].Rand != w0.Sim.Rand("fault/p") {
		t.Errorf("planner contexts %+v", p.ctxs)
	}
	if rnd.RecoverAt() != kernel.Time(750*time.Millisecond) || late.RecoverAt() != kernel.Time(100*time.Millisecond) {
		t.Errorf("RecoverAt %s %s", rnd.RecoverAt(), late.RecoverAt())
	}
	plans := recordsOf(res.records, "run.plan")
	want := []struct {
		text  string
		at    kernel.Time
		attrs []kernel.Attr
	}{
		{"planner p", 0, attrsOf("planner", "p", "until_ns", "1000000000")},
		{"planner random", 0, attrsOf("planner", "random", "until_ns", "750000000")},
		{"planner random", kernel.Time(100 * time.Millisecond), attrsOf("planner", "random", "until_ns", "50000000")},
	}
	if len(plans) != len(want) {
		t.Fatalf("run.plan %+v", plans)
	}
	for i, w := range want {
		if plans[i].Text != w.text || plans[i].At != w.at || !slices.Equal(plans[i].Attrs, w.attrs) || plans[i].Node != 0 {
			t.Errorf("run.plan %d: %+v", i, plans[i])
		}
	}
	if inBody != kernel.Time(750*time.Millisecond) || inFinal != kernel.Time(100*time.Millisecond) || res.recovery != inFinal || !res.hasRecovery || res.schedule.Recovery != inFinal {
		t.Errorf("RecoveryStart in body %s, in final %s, result %s %v", inBody, inFinal, res.recovery, res.hasRecovery)
	}
	if !slices.Equal(res.planners, []string{"p", "random", "random"}) {
		t.Errorf("planners %v", res.planners)
	}
	if len(res.nodes) != 2 || res.nodes[1].ID != 2 || res.nodes[1].Name != "c" || !slices.Equal(res.nodes[1].Tags, []string{"client"}) {
		t.Errorf("nodes %+v", res.nodes)
	}
}

// API-044, API-103: Plan calls the planner's Name once. A panic there is a panic of body
// (API-042), also when replaying, where no planner starts.
func TestAttemptPlannerNamePanics(t *testing.T) {
	for _, sched := range []*fault.Schedule{nil, {Version: 1}} {
		replay := sched != nil
		r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) { w.Plan(namePanicPlanner{}) })
		if res := r.attempt(t, 1, full(), sched, true); !strings.HasPrefix(res.setupErr, "faultline: body panicked during setup (before the first event) for seed 0x0000000000000001: no name\ngoroutine ") {
			t.Errorf("replay=%v, before the first event: setupErr %q", replay, res.setupErr)
		}
		r = unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
			unitTicker(w, nil)
			w.RunFor(10 * time.Millisecond)
			w.Plan(namePanicPlanner{})
		})
		if f := r.attempt(t, 1, full(), sched, true).fail; f == nil || f.context != "panic-body" || f.message != "no name" || f.check != "github.com/hmdsefi/faultline.namePanicPlanner.Name" {
			t.Errorf("replay=%v, after the first event: %+v", replay, f)
		}
	}
}

// API-030 step 6, API-044, FLT-043, §8: a replayed schedule sets End and RecoveryStart, no planner
// starts (not even one registered after faults started), the setup record names the schedule, and
// a schedule that Replay rejects is a setup error.
func TestAttemptReplay(t *testing.T) {
	ms := func(n int) kernel.Time { return kernel.Time(time.Duration(n) * time.Millisecond) }
	cases := []struct {
		name        string
		sched       fault.Schedule
		body        func(w *World)
		end         kernel.Time
		recovery    kernel.Time
		hasRecovery bool
		planners    []string
	}{
		{"End and Recovery", fault.Schedule{End: ms(30), Recovery: ms(20)}, func(w *World) { w.Plan(&fault.Random{}) }, ms(30), ms(20), true, []string{"random"}},
		// Recovery 0: the Random planner's until (End() - Quiet) counts, although it never starts.
		{"Recovery 0", fault.Schedule{End: ms(400)}, func(w *World) { w.Plan(&fault.Random{Quiet: 100 * time.Millisecond}) }, ms(400), ms(300), true, []string{"random"}},
		{"no Random", fault.Schedule{End: ms(400)}, func(w *World) { w.Plan(&recordingPlanner{}) }, ms(400), ms(400), false, []string{"p"}},
		{"late planner", fault.Schedule{End: ms(400)}, func(w *World) { w.RunFor(10 * time.Millisecond); w.Plan(&recordingPlanner{}) }, ms(400), ms(400), false, []string{"p"}},
		// End 0 keeps End() = Duration.
		{"End 0", fault.Schedule{Recovery: ms(20)}, func(w *World) {}, ms(1000), ms(20), true, nil},
	}
	for _, c := range cases {
		r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
			unitTicker(w, nil)
			c.body(w)
		})
		r.plan.env.scheduleHash = "0x00000000000000ab"
		res := r.attempt(t, 1, full(), &c.sched, true)
		if res.setupErr != "" || res.fail != nil {
			t.Errorf("%s: attempt %+v", c.name, res)
			continue
		}
		if res.end != c.end || res.now != c.end || res.recovery != c.recovery || res.hasRecovery != c.hasRecovery || res.schedule.End != c.end || res.schedule.Recovery != c.recovery {
			t.Errorf("%s: end %s now %s recovery %s (%v) schedule %+v", c.name, res.end, res.now, res.recovery, res.hasRecovery, res.schedule)
		}
		if got := kinds(res.records, "run.plan"); len(got) != 0 || !slices.Equal(res.planners, c.planners) {
			t.Errorf("%s: run.plan records %v, planners %v", c.name, got, res.planners)
		}
		if setup := recordsOf(res.records, "run.phase")[0]; len(setup.Attrs) != 6 || setup.Attrs[5] != (kernel.Attr{Key: "schedule", Value: "0x00000000000000ab"}) {
			t.Errorf("%s: setup attrs %+v", c.name, setup.Attrs)
		}
	}
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {})
	bad := r.attempt(t, 1, full(), &fault.Schedule{Version: 9}, true)
	if want := "faultline: FAULTLINE_SCHEDULE replay: fault: Replay: schedule: unsupported version 9 (supported: 1)"; bad.setupErr != want {
		t.Fatalf("setupErr %q, want %q", bad.setupErr, want)
	}
}

// API-063 with the stacks of AT-MIN-01b and AT-MIN-01c; API-064.
func TestPanicSite(t *testing.T) {
	stack := "goroutine 7 [running]:\nruntime/debug.Stack()\n\t/go/src/runtime/debug/stack.go:26 +0x5e\n" +
		"github.com/hmdsefi/faultline/kernel.(*Sim).guard.func1()\n\t/x/kernel/sim.go:120 +0x45\n" +
		"panic({0x10d2ac0?, 0x1215f50?})\n\t/go/src/runtime/panic.go:787 +0x132\n" +
		"runtime.panicmem(...)\n\t/go/src/runtime/panic.go:262\nruntime.sigpanic()\n\t/go/src/runtime/signal_unix.go:917 +0x2c5\n" +
		"github.com/acme/kv.(*Node).apply(0x0, {0x1, 0x2})\n\t/x/kv/node.go:88 +0x1d\ngithub.com/acme/kv.(*Node).step(...)\n"
	if got := panicSite(stack); got != "github.com/acme/kv.(*Node).apply" {
		t.Errorf("panicSite = %q", got)
	}
	if got := panicSite("goroutine 1 [running]:\nmain.f()"); got != "unknown" {
		t.Errorf("no panic line: %q", got)
	}
	two := "panic(1)\na.first()\n\t/a.go:1\npanic(2)\nruntime.gopanic(...)\nb.(*T).second[...](0x1)\n\t/b.go:2\n"
	if got := panicSite(two); got != "b.(*T).second[...]" {
		t.Errorf("nested panics: %q", got)
	}
	// Blank lines are skipped: the empty last line of a stack ending in "\n" is no frame.
	if got := panicSite("panic(1)\nruntime.gopanic(...)\n\t/x.go:1\n"); got != "unknown" {
		t.Errorf("only runtime frames: %q", got)
	}
	if got := panicSite("panic(1)\n\nmain.f()\n"); got != "main.f" {
		t.Errorf("blank line, then a frame: %q", got)
	}
	if panicText("s") != "s" || panicText(errors.New("e")) != "e" || panicText(42) != "42" || panicText(new(int)) != "*int" {
		t.Error("panicText")
	}
}

// panicInBody, panicInInvariant, panicInFinal and (*panicTicker).tick panic in named functions, so
// their panic sites are exact. go:noinline keeps each one a frame of its own.
//
//go:noinline
func panicInBody(w *World) { w.RunFor(time.Second); panic("late") }

//go:noinline
func panicInInvariant() error { panic("inv") }

//go:noinline
func panicInFinal() error { panic("fin") }

type panicTicker struct{}

//go:noinline
func (*panicTicker) tick() { panic("cb") }

// API-063, API-065: the panic site, and so the check, is exactly the user's function, wherever the
// panic is recovered.
func TestAttemptPanicSites(t *testing.T) {
	cases := []struct {
		name string
		body func(w *World)
		site string
	}{
		{"body", func(w *World) { unitTicker(w, nil); panicInBody(w) }, "github.com/hmdsefi/faultline.panicInBody"},
		{"invariant", func(w *World) { unitTicker(w, nil); w.Invariant("i", panicInInvariant) }, "github.com/hmdsefi/faultline.panicInInvariant"},
		{"final", func(w *World) { unitTicker(w, nil); w.Final("f", panicInFinal) }, "github.com/hmdsefi/faultline.panicInFinal"},
		{"callback", func(w *World) {
			p := &panicTicker{}
			w.AddServer("n1", func(n *kernel.Node) { n.After(10*time.Millisecond, "tick", p.tick) })
		}, "github.com/hmdsefi/faultline.(*panicTicker).tick"},
	}
	for _, c := range cases {
		f := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, c.body).attempt(t, 1, full(), nil, true).fail
		if f == nil || f.check != c.site || f.panic == nil || f.panic.Site != c.site {
			t.Errorf("%s: %+v", c.name, f)
		}
	}
}

// API-030, §6: a fixed scenario with network traffic, a disk append, a pause, a crash with parked
// messages and seeded tie-breaks has a pinned trace hash. It keeps the construction order (simnet
// before simdisk), the tie-break and every record faultline emits fixed.
func TestAttemptTraceHash(t *testing.T) {
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		var b *kernel.Node
		w.AddServer("a", func(n *kernel.Node) {
			var tick func()
			tick = func() { w.Net.Send(n, b.ID(), "ping"); n.After(5*time.Millisecond, "tick", tick) }
			n.After(5*time.Millisecond, "tick", tick)
		})
		b = w.AddServer("b", func(n *kernel.Node) {
			w.Net.Handle(n, func(kernel.NodeID, any) {})
			if f, err := w.Disk.Volume(n).Create("log"); err == nil {
				_, _ = f.Append([]byte("x"))
			}
		})
		w.AddClient("c", func(*kernel.Node) {})
		w.RunFor(20 * time.Millisecond)
		b.Pause()
		w.RunFor(30 * time.Millisecond)
		b.Crash()
		w.RunFor(10 * time.Millisecond)
	})
	res := r.attempt(t, 1, full(), nil, true)
	if res.setupErr != "" || res.fail != nil {
		t.Fatalf("attempt %q %+v", res.setupErr, res.fail)
	}
	const want = 0x342fdf4042cc4da5
	if res.hash != want {
		t.Errorf("trace hash 0x%016x, want 0x%016x", res.hash, uint64(want))
	}
}

// API-074, API-093: with recorded operations the attempt result carries the history as JSONL
// (without any, it is nil: TestAttemptPhases).
func TestAttemptHistory(t *testing.T) {
	var want bytes.Buffer
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		id := w.History.Invoke("c1", "put", 1)
		w.History.Complete(id, history.OK, nil)
		w.Final("written", func() error { return w.History.WriteJSONL(&want) })
	})
	res := r.attempt(t, 1, full(), nil, true)
	if res.fail != nil || !bytes.Contains(res.history, []byte(`"put"`)) || !bytes.Equal(res.history, want.Bytes()) {
		t.Errorf("history %q, want %q (fail %+v)", res.history, want.Bytes(), res.fail)
	}
}

// API-041, API-044, API-055, API-059, §7.2: misuse in body panics with these messages; the valid
// calls in between succeed.
func TestWorldMisuseInBody(t *testing.T) {
	ok := func() error { return nil }
	sel := func() []kernel.NodeID { return nil }
	const quiet = "; want 0 (default Duration/4) or a positive duration shorter than Options.Duration (1s)"
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		expectMisuse(t, "faultline: World.Invariant: empty name", func() { w.Invariant("", ok) })
		expectMisuse(t, "faultline: World.Final: empty name", func() { w.Final("", ok) })
		expectMisuse(t, "faultline: World.Role: empty name", func() { w.Role("", sel) })
		expectMisuse(t, `faultline: World.Invariant "a": nil function`, func() { w.Invariant("a", nil) })
		expectMisuse(t, `faultline: World.Final "a": nil function`, func() { w.Final("a", nil) })
		expectMisuse(t, `faultline: World.Role "a": nil function`, func() { w.Role("a", nil) })
		w.Invariant("a", ok)
		w.Final("a", ok) // invariant and final names are separate namespaces
		w.Role("a", sel)
		expectMisuse(t, `faultline: World.Invariant: duplicate name "a"`, func() { w.Invariant("a", ok) })
		expectMisuse(t, `faultline: World.Final: duplicate name "a"`, func() { w.Final("a", ok) })
		expectMisuse(t, `faultline: World.Role: duplicate name "a"`, func() { w.Role("a", sel) })
		expectMisuse(t, "faultline: World.Plan: nil planner", func() { w.Plan(nil) })
		expectMisuse(t, "faultline: World.Plan: fault.Random.Quiet is 1s"+quiet, func() { w.Plan(&fault.Random{Quiet: time.Second}) })
		expectMisuse(t, "faultline: World.Plan: fault.Random.Quiet is -1ns"+quiet, func() { w.Plan(&fault.Random{Quiet: -1}) })
		w.Plan(&fault.Random{Quiet: time.Second - 1})
		expectMisuse(t, "faultline: World.Rand: empty label", func() { w.Rand("") })
		if w.Rand("load") != w.Sim.Rand("workload/load") {
			t.Error("Rand stream")
		}
		expectMisuse(t, "faultline: World.RunFor: negative duration -1ns", func() { w.RunFor(-1) })
		expectMisuse(t, "faultline: World.RunFor(2s) at t=0.000000000s would run past the end of the run (1.000000000s); raise Options.Duration", func() { w.RunFor(2 * time.Second) })
		w.RunFor(time.Second)
		expectMisuse(t, "faultline: World.RunFor(1ns) at t=1.000000000s would run past the end of the run (1.000000000s); raise Options.Duration", func() { w.RunFor(1) })
	})
	if res := r.attempt(t, 1, full(), nil, true); res.setupErr != "" || res.fail != nil {
		t.Fatalf("attempt %+v", res)
	}
}

// §7.2, API-057: a final check cannot register anything or call RunFor. Final checks run outside
// the loop in phase final, so RunFor gets "called after body returned".
func TestWorldMisuseInFinal(t *testing.T) {
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		ok := func() error { return nil }
		w.Final("inv", func() error { w.Invariant("late", ok); return nil })
		w.Final("fin", func() error { w.Final("late", ok); return nil })
		w.Final("role", func() error { w.Role("late", func() []kernel.NodeID { return nil }); return nil })
		w.Final("plan", func() error { w.Plan(&fault.Random{}); return nil })
		w.Final("srv", func() error { w.AddServer("s", func(*kernel.Node) {}); return nil })
		w.Final("cli", func() error { w.AddClient("c", func(*kernel.Node) {}); return nil })
		w.Final("run", func() error { w.RunFor(0); return nil })
	})
	res := r.attempt(t, 1, full(), nil, true)
	if res.fail == nil {
		t.Fatal("passed")
	}
	want := []string{
		"faultline: World.Invariant called after the run ended",
		"faultline: World.Final called after the run ended",
		"faultline: World.Role called after the run ended",
		"faultline: World.Plan called after the run ended",
		"faultline: World.AddServer called after the run ended",
		"faultline: World.AddClient called after the run ended",
		"faultline: World.RunFor called after body returned",
	}
	var got []string
	for _, f := range res.fail.finals {
		got = append(got, f.message)
	}
	if !slices.Equal(got, want) {
		t.Errorf("final check messages\n%q\nwant\n%q", got, want)
	}
}

// API-041 step 1: RunFor from a callback or an invariant panics with "called from inside the
// simulation", in body's RunFor and in the post-body drive; the check comes before the phase check.
func TestWorldRunForInsideLoop(t *testing.T) {
	const want = "faultline: World.RunFor called from inside the simulation (a callback, invariant or final check)"
	for _, inBody := range []bool{false, true} {
		r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
			unitTicker(w, func(n *kernel.Node, c int) {
				if c == 2 {
					w.RunFor(time.Millisecond)
				}
			})
			if inBody {
				w.RunFor(time.Second)
			}
		})
		res := r.attempt(t, 1, full(), nil, true)
		if res.fail == nil || res.fail.context != "panic-callback" || res.fail.message != want {
			t.Errorf("callback, inBody=%v: %+v", inBody, res.fail)
		}
	}
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		unitTicker(w, nil)
		w.Invariant("i", func() error { w.RunFor(0); return nil })
	})
	res := r.attempt(t, 1, full(), nil, true)
	if res.fail == nil || res.fail.context != "panic-invariant" || res.fail.message != want {
		t.Errorf("invariant: %+v", res.fail)
	}
}

// API-032, API-055 and §7.2: however the attempt ended (passed, failed or a setup error), the
// World is in phase done. A World that body kept cannot register anything or drive the kernel.
func TestWorldMisuseAfterRun(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		body    func(w *World)
		outcome string
	}{
		{"passed", Options{}, func(w *World) {}, "passed"},
		{"failed", Options{MaxEvents: 5}, func(w *World) {}, "failed"},
		{"setup error", Options{}, func(w *World) { w.RunFor(-1) }, "setup error"},
		{"setup error after body", Options{}, func(w *World) { w.Sim.Step() }, "setup error"},
	}
	for _, c := range cases {
		c.opts.Seeds, c.opts.Duration = 1, time.Second
		var saved *World
		r := unitRunner(t, c.opts, func(w *World) {
			saved = w
			unitTicker(w, nil)
			c.body(w)
		})
		res := r.attempt(t, 1, full(), nil, true)
		outcome := "passed"
		if res.setupErr != "" {
			outcome = "setup error"
		} else if res.fail != nil {
			outcome = "failed"
		}
		if outcome != c.outcome || r.cur != saved {
			t.Errorf("%s: outcome %q, r.cur is the attempt's World: %v", c.name, outcome, r.cur == saved)
		}
		executed := saved.Sim.Executed()
		expectMisuse(t, "faultline: World.Invariant called after the run ended", func() { saved.Invariant("x", func() error { return nil }) })
		expectMisuse(t, "faultline: World.AddServer called after the run ended", func() { saved.AddServer("z", func(*kernel.Node) {}) })
		expectMisuse(t, "faultline: World.AddClient called after the run ended", func() { saved.AddClient("y", func(*kernel.Node) {}) })
		expectMisuse(t, "faultline: World.RunFor called after body returned", func() { saved.RunFor(time.Second) })
		if saved.Sim.Executed() != executed {
			t.Errorf("%s: the World ran %d more events after the attempt", c.name, saved.Sim.Executed()-executed)
		}
	}
}

// API-058: Servers and Clients filter by tag in NodeID order; nodes added with w.Sim.AddNode and
// the tag count.
func TestWorldServersClients(t *testing.T) {
	var servers, clients []string
	var c1 *kernel.Node
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		boot := func(*kernel.Node) {}
		w.AddServer("s1", boot)
		c1 = w.AddClient("c1", boot, kernel.WithTags("x"))
		w.Sim.AddNode("s3", boot, kernel.WithTags("server"))
		w.AddServer("s2", boot)
		for _, n := range w.Servers() {
			servers = append(servers, n.Name())
		}
		for _, n := range w.Clients() {
			clients = append(clients, n.Name())
		}
	})
	if res := r.attempt(t, 1, full(), nil, true); res.setupErr != "" || res.fail != nil {
		t.Fatalf("attempt %+v", res)
	}
	if !slices.Equal(servers, []string{"s1", "s3", "s2"}) || !slices.Equal(clients, []string{"c1"}) || !c1.HasTag("x") || !c1.HasTag("client") || c1.HasTag("server") {
		t.Errorf("servers %v, clients %v, c1 tags %v", servers, clients, c1.Tags())
	}
}

// API-060: Logf records a global kernel.log record in every attempt and, in the primary attempt
// only, logs "t=<time> <message>" with the caller's file and line (t.Helper). testing shows that
// location only in its output, so this test reads it from a child process that runs it again.
func TestWorldLogf(t *testing.T) {
	body := func(w *World) {
		w.Logf("hello %d", 7)
		unitTicker(w, func(n *kernel.Node, c int) {
			if c == 2 {
				w.Logf("from a callback")
			}
		})
	}
	opts := Options{Seeds: 1, Duration: 50 * time.Millisecond}
	if os.Getenv("FAULTLINE_TEST_LOGF") == "1" {
		unitRunner(t, opts, body).attempt(t, 1, full(), nil, true)
		return
	}
	res := unitRunner(t, opts, body).attempt(t, 1, full(), nil, false)
	if logs := recordsOf(res.records, "kernel.log"); len(logs) != 2 || logs[0].Text != "hello 7" || logs[1].Text != "from a callback" || logs[0].Node != 0 || logs[1].Node != 0 {
		t.Errorf("kernel.log records %+v", logs)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorldLogf$", "-test.v", "-test.count=1") //nolint:gosec // runs this test binary again
	cmd.Env = append(os.Environ(), "FAULTLINE_TEST_LOGF=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child test: %v\n%s", err, out)
	}
	for _, line := range []string{`attempt_test\.go:\d+: t=0\.000000000s hello 7\n`, `attempt_test\.go:\d+: t=0\.020000000s from a callback\n`} {
		if !regexp.MustCompile(`\s` + line).Match(out) {
			t.Errorf("child output has no line %s:\n%s", line, out)
		}
	}
}

func BenchmarkAttemptEmpty(b *testing.B) {
	r := unitRunner(b, Options{Seeds: 1, Duration: time.Second}, func(w *World) {})
	st := &testing.T{} // attempt needs a *testing.T; NoCryptoSeed keeps it unused
	for b.Loop() {
		if res := r.attempt(st, 1, kernel.TraceConfig{}, nil, false); res.fail != nil {
			b.Fatal(res.fail.message)
		}
	}
}

// BenchmarkInvariantDispatch runs 1,000,000 events with 0 and with 10 no-op invariants; the
// dispatch cost per invariant is (ns/event of invariants=10 - ns/event of invariants=0) / 10.
func BenchmarkInvariantDispatch(b *testing.B) {
	for _, n := range []int{0, 10} {
		b.Run(fmt.Sprintf("invariants=%d", n), func(b *testing.B) {
			r := unitRunner(b, Options{Seeds: 1, Duration: time.Hour, MaxEvents: 1_000_000, AllowLimit: true}, func(w *World) {
				w.AddServer("n1", func(nd *kernel.Node) {
					var f func()
					f = func() { nd.After(time.Microsecond, "e", f) }
					nd.After(time.Microsecond, "e", f)
				})
				for i := 0; i < n; i++ {
					w.Invariant(fmt.Sprintf("inv%d", i), func() error { return nil })
				}
			})
			st := &testing.T{}
			for b.Loop() {
				r.attempt(st, 1, kernel.TraceConfig{}, nil, false)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/1e6, "ns/event")
		})
	}
}

// API-061 row 8, API-066, API-067: t.Fail during the primary attempt is kind fail (test) when no
// other failure was found, but not in a later attempt or when st had already failed. st is a
// zero-value testing.T, as in the benchmarks: Fail sets only its own flag.
func TestAttemptTestMarkedFailed(t *testing.T) {
	r := unitRunner(t, Options{Seeds: 1, Duration: time.Second}, func(w *World) {
		unitTicker(w, func(n *kernel.Node, c int) {
			if c == 3 {
				w.T().Fail()
			}
		})
		w.Final("ok", func() error { return nil })
	})
	res := r.attempt(&testing.T{}, 1, full(), nil, true)
	f := res.fail
	if f == nil || f.kind != "fail" || f.check != "" || f.context != "fail-test" || f.message != "see the t.Error output above" ||
		f.headline() != "test marked failed during the run (t.Error, t.Errorf or t.Fail) at t=1.000000000s" ||
		f.recordSeq != 0 || f.hasEvent || f.node != "" || f.err != nil || len(recordsOf(res.records, "check.violation")) != 0 {
		t.Errorf("primary: %+v", f)
	}
	if res := r.attempt(&testing.T{}, 1, full(), nil, false); res.fail != nil {
		t.Errorf("not the primary attempt: %+v", res.fail)
	}
	failed := &testing.T{}
	failed.Fail()
	if res := r.attempt(failed, 1, full(), nil, true); res.fail != nil {
		t.Errorf("failed before the attempt: %+v", res.fail)
	}
}
