// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// phase is the attempt phase of API-032.
type phase uint8

const (
	phaseSetup phase = iota
	phaseBody
	phaseRun
	phaseFinal
	phaseDone
)

// misuse is the panic value of World methods called in a way API §7.2 forbids.
type misuse struct{ msg string }

func (m misuse) Error() string { return m.msg }

func panicMisuse(format string, args ...any) {
	panic(misuse{msg: fmt.Sprintf(format, args...)})
}

// abortBody is the panic value World.RunFor uses to stop body when the run fails or hits a
// limit (API-041).
type abortBody struct{}

type namedCheck struct {
	name string
	fn   func() error
}

// plannerReg is one registered planner (API-043, API-044).
type plannerReg struct {
	p       fault.Planner
	name    string      // p.Name(), called once in Plan
	startAt kernel.Time // 0 for planners registered before faults start, else Now() at Plan
	until   kernel.Time
}

// World is one simulated world: one attempt of one seed.
// Its methods must be called from the seed subtest's goroutine only.
type World struct {
	Sim     *kernel.Sim
	Net     *simnet.Network
	Disk    *simdisk.Disks
	Faults  *fault.Injector
	History *history.Recorder

	r                 *runner
	st                *testing.T
	seed              uint64
	primary           bool
	phase             phase
	driving           bool
	aborted           bool // RunFor stopped body (API-041 step 5); every later RunFor stops it again
	invariants        []namedCheck
	finals            []namedCheck
	roles             fault.Roles // lookups only; never iterated
	planners          []plannerReg
	faultsStarted     bool
	sched             *fault.Schedule // replayed schedule, nil when not replaying
	drove             bool
	lastStop          kernel.StopReason
	failedOutsideLoop bool
	violationSeq      uint64 // Seq of the check.violation emitted by afterEvent
}

// T returns the seed subtest. Use it for t.Fatalf on setup errors; prefer Invariant, Final
// or Sim.Fail to report failures of the system under test.
func (w *World) T() testing.TB { return w.st }

// Seed returns the seed of this world.
func (w *World) Seed() uint64 { return w.seed }

// Options returns the effective options of this Run call: defaults applied, environment
// overrides applied, Seeds set to the length of the seed list, BaseSeed set to the resolved
// base (0 when the list came from FAULTLINE_SEED or FAULTLINE_SEED_LIST), Trace set to the
// primary attempt's config. It returns the same value in every attempt.
func (w *World) Options() Options { return w.r.plan.opts }

func (w *World) checkNotEnded(method string) {
	if w.phase == phaseFinal || w.phase == phaseDone {
		panicMisuse("faultline: World.%s called after the run ended; call it from body or from a callback during the run", method)
	}
}

// AddServer adds a node tagged "server". It equals
// w.Sim.AddNode(name, boot, append(opts, kernel.WithTags("server"))...).
func (w *World) AddServer(name string, boot kernel.BootFunc, opts ...kernel.NodeOption) *kernel.Node {
	w.checkNotEnded("AddServer")
	return w.Sim.AddNode(name, boot, append(slices.Clip(opts), kernel.WithTags("server"))...)
}

// AddClient adds a node tagged "client". It equals
// w.Sim.AddNode(name, boot, append(opts, kernel.WithTags("client"))...).
func (w *World) AddClient(name string, boot kernel.BootFunc, opts ...kernel.NodeOption) *kernel.Node {
	w.checkNotEnded("AddClient")
	return w.Sim.AddNode(name, boot, append(slices.Clip(opts), kernel.WithTags("client"))...)
}

func (w *World) tagged(tag string) []*kernel.Node {
	var out []*kernel.Node
	for _, n := range w.Sim.Nodes() {
		if n.HasTag(tag) {
			out = append(out, n)
		}
	}
	return out
}

// Servers returns the nodes tagged "server", in NodeID order.
func (w *World) Servers() []*kernel.Node { return w.tagged("server") }

// Clients returns the nodes tagged "client", in NodeID order.
func (w *World) Clients() []*kernel.Node { return w.tagged("client") }

// register implements the checks of API-055 for Invariant and Final; when says when the check
// runs, for the nil-function message (§7.2).
func (w *World) register(method, when string, list *[]namedCheck, name string, check func() error) {
	if name == "" {
		panicMisuse("faultline: World.%s: empty name; the name identifies the check in failure reports", method)
	}
	if check == nil {
		panicMisuse("faultline: World.%s %q: nil function; pass the check to run %s", method, name, when)
	}
	for _, c := range *list {
		if c.name == name {
			panicMisuse("faultline: World.%s: duplicate name %q; give each one a distinct name", method, name)
		}
	}
	w.checkNotEnded(method)
	*list = append(*list, namedCheck{name: name, fn: check})
}

// Invariant registers a check that runs after every executed event. It must be cheap and
// must not change the world. The first non-nil error fails the run (kind "invariant").
func (w *World) Invariant(name string, check func() error) {
	w.register("Invariant", "after every event", &w.invariants, name, check)
}

// Final registers a check that runs once after the run ends normally. All final checks run,
// in registration order; the first failing one is the seed's failure (kind "final").
func (w *World) Final(name string, check func() error) {
	w.register("Final", "when the run ends", &w.finals, name, check)
}

// Role registers a named node selector that planners can target (fault.Rule.Target).
func (w *World) Role(name string, fn func() []kernel.NodeID) {
	if name == "" {
		panicMisuse("faultline: World.Role: empty name; planners target a role by its name")
	}
	if fn == nil {
		panicMisuse("faultline: World.Role %q: nil function; pass a function that returns the role's nodes", name)
	}
	if _, ok := w.roles[name]; ok {
		panicMisuse("faultline: World.Role: duplicate name %q; give each one a distinct name", name)
	}
	w.checkNotEnded("Role")
	w.roles[name] = fn
}

// until returns ctx.Until for planner p (API-044).
func (w *World) until(p fault.Planner) kernel.Time {
	if r, ok := p.(*fault.Random); ok {
		q := r.Quiet
		if q == 0 {
			q = w.r.plan.opts.Duration / 4
		}
		return w.End() - kernel.Time(q)
	}
	return w.End()
}

// Plan registers a fault planner. Planners start when the simulation first advances (the first
// World.RunFor, or after body returns). Under FAULTLINE_SCHEDULE no planner is started (FLT-043).
func (w *World) Plan(p fault.Planner) {
	w.checkNotEnded("Plan")
	r, isRandom := p.(*fault.Random)
	if p == nil || (isRandom && r == nil) {
		panicMisuse("faultline: World.Plan: nil planner; pass a non-nil fault.Planner")
	}
	d := w.r.plan.opts.Duration
	if isRandom && (r.Quiet < 0 || r.Quiet >= d) {
		panicMisuse("faultline: World.Plan: fault.Random.Quiet is %v; want 0 (default Duration/4) or a positive duration shorter than Options.Duration (%v)", r.Quiet, d)
	}
	reg := plannerReg{p: p, name: p.Name(), until: w.until(p)}
	if w.faultsStarted {
		reg.startAt = w.Sim.Now()
	}
	w.planners = append(w.planners, reg)
	if w.faultsStarted && w.sched == nil {
		w.startPlanner(reg)
	}
}

// RecoveryStart returns the start of the recovery window (API-044): the replayed schedule's
// Recovery when non-zero, else the earliest Until of the registered *fault.Random planners, else
// End().
func (w *World) RecoveryStart() kernel.Time {
	t, _ := w.recovery()
	return t
}

// recovery returns RecoveryStart and whether report.run.recovery_ns is present (API-074).
func (w *World) recovery() (kernel.Time, bool) {
	if w.sched != nil && w.sched.Recovery != 0 {
		return w.sched.Recovery, true
	}
	found := false
	var earliest kernel.Time
	for _, reg := range w.planners {
		if _, ok := reg.p.(*fault.Random); !ok {
			continue
		}
		v := max(reg.until, reg.startAt)
		if !found || v < earliest {
			earliest, found = v, true
		}
	}
	if found {
		return earliest, true
	}
	return w.End(), false
}

// End returns the virtual time at which the run ends: the replayed schedule's End when non-zero,
// else kernel.Time(Options.Duration).
func (w *World) End() kernel.Time {
	if w.sched != nil && w.sched.End != 0 {
		return w.sched.End
	}
	return kernel.Time(w.r.plan.opts.Duration)
}

// Rand returns the PRNG stream "workload/<label>". label must not be empty.
func (w *World) Rand(label string) *rand.Rand {
	if label == "" {
		panicMisuse(`faultline: World.Rand: empty label; pass a non-empty label such as "load"`)
	}
	return w.Sim.Rand("workload/" + label)
}

// RunFor advances the simulation by d virtual time. It may only be called from body.
// If the run fails or hits a limit, RunFor does not return: body is stopped and Run
// continues with failure handling. Recovering that stop does not resume the run: every
// later RunFor, also a deferred one, stops body again.
func (w *World) RunFor(d time.Duration) {
	if w.driving {
		panicMisuse("faultline: World.RunFor called from inside the simulation (a callback or an invariant); schedule later work with Node.After instead")
	}
	if w.phase != phaseBody {
		panicMisuse("faultline: World.RunFor called after body returned; call it only from body (final checks cannot advance virtual time)")
	}
	if w.aborted {
		panic(abortBody{})
	}
	if d < 0 {
		panicMisuse("faultline: World.RunFor: negative duration %v; want 0 or more", d)
	}
	target := w.Sim.Now().Add(d)
	if target > w.End() {
		panicMisuse("faultline: World.RunFor(%v) at t=%s would run past the end of the run (%s); raise Options.Duration", d, w.Sim.Now(), w.End())
	}
	w.startFaults()
	if w.Sim.Err() != nil {
		w.failedOutsideLoop = true
	}
	switch w.drive(target) {
	case kernel.StopFailed, kernel.StopMaxEvents, kernel.StopMaxTime:
		w.aborted = true
		panic(abortBody{})
	}
}

// Logf records a global kernel.log record (w.Sim.Logf) and, in the primary attempt only, logs
// the message with t.Logf prefixed by the virtual time.
func (w *World) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	w.Sim.Logf("%s", msg)
	if w.primary {
		w.st.Helper()
		w.st.Logf("t=%s %s", w.Sim.Now(), msg)
	}
}

// drive advances virtual time to until (API-046). It is the only place where Run and World
// advance time; goroutine mode replaces its body (API-090).
func (w *World) drive(until kernel.Time) kernel.StopReason {
	w.driving = true
	defer func() { w.driving = false }()
	stop := w.Sim.RunUntil(until)
	w.drove, w.lastStop = true, stop
	return stop
}

// startFaults starts the registered planners once per attempt (API-043).
func (w *World) startFaults() {
	if w.faultsStarted {
		return
	}
	if w.Sim.Executed() > 0 || w.Sim.Now() != 0 {
		panicMisuse("faultline: body advanced the simulation with w.Sim before calling w.RunFor; use w.RunFor so planners start at time 0")
	}
	w.faultsStarted = true
	if w.sched != nil {
		return
	}
	for _, reg := range w.planners {
		w.startPlanner(reg)
	}
}

// startPlanner emits run.plan and starts p (API-043). Outside the kernel loop a panic becomes
// the setup error "faultline: planner start: <panic text>".
func (w *World) startPlanner(reg plannerReg) {
	if !w.driving {
		defer func() {
			if v := recover(); v != nil {
				if m, ok := v.(misuse); ok {
					panic(m)
				}
				panicMisuse("faultline: planner start: %s", panicText(v))
			}
		}()
	}
	ctx := w.Faults.NewPlanContext(reg.p, w.Servers(), w.roles, reg.until)
	w.Sim.Emit(kernel.Record{Kind: "run.plan", Text: "planner " + reg.name, Attrs: []kernel.Attr{
		{Key: "planner", Value: reg.name},
		{Key: "until_ns", Value: strconv.FormatInt(int64(reg.until), 10)},
	}})
	reg.p.Start(ctx)
}
