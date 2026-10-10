// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"testing/cryptotest"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// runner is the state of one Run call.
type runner struct {
	t       *testing.T
	body    func(w *World)
	plan    *plan
	build   buildInfo
	cwd     string
	results string
	cur     *World // World of the attempt in progress, for Goexit handling (API-101)
}

// failure is the classified failure of one attempt (API-061, API-066).
type failure struct {
	kind        string // invariant, final, panic, fail, limit, determinism, setup
	check       string
	context     string // headline variant (API-067), see headline
	name        string // invariant or final name of a panic in a check
	message     string
	at          kernel.Time
	event       uint64
	hasEvent    bool
	nodeID      kernel.NodeID
	node        string
	recordSeq   uint64
	panic       *artifact.Panic
	limit       *artifact.Limit
	finals      []finalResult
	determinism *determinism
	err         error // error whose ArtifactFiles are collected (API-077); nil for none
}

// finalResult is one failing final check (API-057).
type finalResult struct {
	check   string
	message string
	panic   *artifact.Panic
	err     error
}

// determinism describes a determinism failure (API-070, API-071).
type determinism struct {
	context  string // "artifact_rerun" or "check_determinism"
	seed     uint64
	hashes   []uint64
	original *failure // artifact_rerun only
	diff     *recordDiff
	same     uint64 // trace hash of the last full-trace run
	kept     int    // records each full-trace run kept, when diff is nil
}

// attemptResult is the outcome and data of one attempt (API-093).
type attemptResult struct {
	setupErr    string   // non-empty: a setup error; nothing else is set
	fail        *failure // nil: the attempt passed
	limited     bool     // API-061 row 9: passed because AllowLimit is set
	hash        uint64
	executed    uint64
	now         kernel.Time
	lastSeq     uint64
	stop        string
	records     []kernel.Record
	schedule    fault.Schedule
	history     []byte
	nodes       []artifact.Node
	planners    []string
	recovery    kernel.Time
	hasRecovery bool
	end         kernel.Time
	extra       []extraFile
	warnings    []string
}

// checkFailure is the error an invariant failure passes to Sim.Fail (API-056).
type checkFailure struct {
	name  string
	err   error
	event uint64
}

func (c *checkFailure) Error() string { return "invariant \"" + c.name + "\": " + c.err.Error() }
func (c *checkFailure) Unwrap() error { return c.err }

// panicFailure is the error a panicking invariant passes to Sim.Fail (API-056).
type panicFailure struct {
	in    string
	name  string
	value any
	text  string
	stack string
	event uint64
}

func (p *panicFailure) Error() string {
	return "panic in " + p.in + " \"" + p.name + "\": " + p.text
}

// panicText implements API-064 (KRN-044's rule).
func panicText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	}
	return kernel.Describe(v)
}

// panicSite implements API-063 (MIN-004): the function of the first non-runtime frame after the
// last "panic(" line, or "unknown".
func panicSite(stack string) string {
	lines := strings.Split(stack, "\n")
	last := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "panic(") {
			last = i
		}
	}
	if last < 0 {
		return "unknown"
	}
	for _, l := range lines[last+1:] {
		if strings.HasPrefix(l, "\t") || strings.TrimSpace(l) == "" {
			continue
		}
		fn := l
		if k := strings.LastIndex(l, "("); k >= 0 {
			fn = l[:k]
		}
		if strings.HasPrefix(fn, "runtime.") {
			continue
		}
		return fn
	}
	return "unknown"
}

// emit emits a record of package faultline: node 0, cause left to the kernel (API §8).
func (w *World) emit(kind, text string, kv ...string) uint64 {
	attrs := make([]kernel.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		attrs = append(attrs, kernel.Attr{Key: kv[i], Value: kv[i+1]})
	}
	return w.Sim.Emit(kernel.Record{Kind: kind, Text: text, Attrs: attrs})
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

// newWorld builds the World of one attempt in the order of API-030.
func (r *runner) newWorld(st *testing.T, seed uint64, traceCfg kernel.TraceConfig, sched *fault.Schedule, primary bool) (w *World, setupErr string) {
	stage := "construction"
	defer func() {
		if v := recover(); v != nil {
			w = nil
			if stage == "crypto" {
				setupErr = fmt.Sprintf("faultline: crypto seeding: %v (Run cannot be used in a parallel test; remove t.Parallel or set Options.NoCryptoSeed)", v)
			} else {
				setupErr = fmt.Sprintf("faultline: world construction for seed 0x%016x: %v", seed, v)
			}
		}
	}()
	o := r.plan.opts
	sim := kernel.New(kernel.Config{Seed: seed, MaxEvents: o.MaxEvents, MaxTime: 0, TieBreak: kernel.TieBreakSeeded, Trace: traceCfg})
	if !o.NoCryptoSeed {
		c := sim.Rand("crypto").Uint64()
		stage = "crypto"
		cryptotest.SetGlobalRandom(st, c)
		stage = "construction"
	}
	nw := simnet.New(sim, o.Net)
	d := simdisk.New(sim, o.Disk)
	inj := fault.NewInjector(sim, nw, d)
	w = &World{Sim: sim, Net: nw, Disk: d, Faults: inj, r: r, st: st, seed: seed, primary: primary, roles: fault.Roles{}}
	if sched != nil {
		if err := inj.Replay(*sched); err != nil {
			return nil, fmt.Sprintf("faultline: FAULTLINE_SCHEDULE replay: %v", err)
		}
		w.sched = sched
	}
	w.History = history.NewRecorder(sim)
	sim.OnEvent(w.afterEvent)
	kv := []string{"phase", "setup", "seed", fmt.Sprintf("0x%016x", seed), "duration_ns", strconv.FormatInt(int64(o.Duration), 10), "max_events", itoa(o.MaxEvents), "mode", o.Mode.String()}
	if sched != nil {
		kv = append(kv, "schedule", r.plan.env.scheduleHash)
	}
	w.emit("run.phase", "setup", kv...)
	w.phase = phaseBody
	return w, ""
}

// bodyOutcome is how body ended (API-042).
type bodyOutcome struct {
	aborted  bool            // stopped by RunFor
	setupErr string          // misuse or a panic before the first event
	panic    *artifact.Panic // a panic after the first event
}

// callBody runs body with the recover table of API-042.
func (w *World) callBody(body func(*World)) (out bodyOutcome) {
	defer func() {
		v := recover()
		if w.aborted { // body recovered the stop, or a deferred call panicked after it (API-042)
			out = bodyOutcome{aborted: true}
			return
		}
		if v == nil {
			return
		}
		switch x := v.(type) {
		case abortBody:
			out = bodyOutcome{aborted: true}
		case misuse:
			out = bodyOutcome{setupErr: x.msg}
		default:
			stack := string(debug.Stack())
			text := panicText(v)
			if w.Sim.Executed() == 0 && w.Sim.Now() == 0 {
				out = bodyOutcome{setupErr: fmt.Sprintf("faultline: body panicked during setup (before the first event) for seed 0x%016x: %s\n%s", w.seed, text, stack)}
				return
			}
			out = bodyOutcome{panic: &artifact.Panic{Value: text, In: "body", Site: panicSite(stack), Stack: stack}}
		}
	}()
	body(w)
	return bodyOutcome{}
}

// afterEvent is the OnEvent observer that runs the invariants (API-056).
func (w *World) afterEvent() {
	if w.Sim.Err() != nil {
		return
	}
	for _, c := range w.invariants {
		if w.runInvariant(c) {
			return
		}
	}
}

func (w *World) runInvariant(c namedCheck) (failed bool) {
	defer func() {
		if v := recover(); v != nil {
			stack := string(debug.Stack())
			text := panicText(v)
			ev := w.Sim.Executed()
			w.violationSeq = w.emit("check.violation", "panic in invariant", "kind", "panic", "in", "invariant", "name", c.name, "error", text, "event", itoa(ev))
			w.Sim.Fail(&panicFailure{in: "invariant", name: c.name, value: v, text: text, stack: stack, event: ev})
			failed = true
		}
	}()
	if err := c.fn(); err != nil {
		ev := w.Sim.Executed()
		w.violationSeq = w.emit("check.violation", "invariant \""+c.name+"\" violated", "kind", "invariant", "check", c.name, "error", err.Error(), "event", itoa(ev))
		w.Sim.Fail(&checkFailure{name: c.name, err: err, event: ev})
		return true
	}
	return false
}

// runFinals runs every final check in registration order (API-057) and returns the failure of
// the first failing one, or nil.
func (w *World) runFinals() *failure {
	w.phase = phaseFinal
	w.emit("run.phase", "final", "phase", "final", "checks", strconv.Itoa(len(w.finals)))
	var results []finalResult
	var first *failure
	for _, c := range w.finals {
		res, seq, failed := w.runFinal(c)
		if !failed {
			continue
		}
		results = append(results, res)
		if first == nil {
			first = &failure{kind: "final", check: c.name, context: "final", message: res.message, at: w.Sim.Now(), recordSeq: seq, err: res.err}
			if res.panic != nil {
				first.kind, first.check, first.context, first.name, first.panic = "panic", res.panic.Site, "panic-final", c.name, res.panic
			}
		}
	}
	if first == nil {
		return nil
	}
	if len(results) > 1 {
		names := make([]string, 0, len(results)-1)
		for _, res := range results[1:] {
			names = append(names, "\""+res.check+"\"")
		}
		first.message += "\nalso failed: " + strings.Join(names, ", ")
	}
	first.finals = results
	return first
}

func (w *World) runFinal(c namedCheck) (res finalResult, seq uint64, failed bool) {
	defer func() {
		if v := recover(); v != nil {
			stack := string(debug.Stack())
			text := panicText(v)
			seq = w.emit("check.violation", "panic in final", "kind", "panic", "in", "final", "name", c.name, "error", text)
			res = finalResult{check: c.name, message: text, panic: &artifact.Panic{Value: text, In: "final", Name: c.name, Site: panicSite(stack), Stack: stack}}
			failed = true
		}
	}()
	if err := c.fn(); err != nil {
		seq = w.emit("check.violation", "final check \""+c.name+"\" failed", "kind", "final", "check", c.name, "error", err.Error())
		return finalResult{check: c.name, message: err.Error(), err: err}, seq, true
	}
	return finalResult{}, 0, false
}

// classify implements the table of API-061. bodyPanic is the panic recovered from body, if any;
// finalFail is the result of runFinals.
func (w *World) classify(bodyPanic *artifact.Panic, finalFail *failure, failedAtStart bool) (*failure, bool) {
	sim := w.Sim
	err := sim.Err()
	ev := sim.Executed()
	var pe *kernel.PanicError
	cf, isCheck := err.(*checkFailure)
	pf, isPanic := err.(*panicFailure)
	limitStop := w.drove && (w.lastStop == kernel.StopMaxEvents || w.lastStop == kernel.StopMaxTime)
	switch {
	case bodyPanic != nil: // row 1
		seq := w.emit("check.violation", "panic in body", "kind", "panic", "in", "body", "error", bodyPanic.Value)
		return &failure{kind: "panic", check: bodyPanic.Site, context: "panic-body", message: bodyPanic.Value, at: sim.Now(), recordSeq: seq, panic: bodyPanic}, false
	case isCheck: // row 2
		return &failure{kind: "invariant", check: cf.name, context: "invariant", message: cf.err.Error(), at: sim.Now(), event: cf.event, hasEvent: true, recordSeq: w.violationSeq, err: err}, false
	case isPanic: // row 3
		site := panicSite(pf.stack)
		return &failure{kind: "panic", check: site, context: "panic-invariant", name: pf.name, message: pf.text, at: sim.Now(), event: pf.event, hasEvent: true, recordSeq: w.violationSeq,
			panic: &artifact.Panic{Value: pf.text, In: "invariant", Name: pf.name, Site: site, Stack: pf.stack}}, false
	case errors.As(err, &pe): // row 4
		text := panicText(pe.Value)
		stack := string(pe.Stack)
		site := panicSite(stack)
		seq := w.emit("check.violation", "panic in callback", "kind", "panic", "in", "callback", "error", text, "event", itoa(ev))
		return &failure{kind: "panic", check: site, context: "panic-callback", message: text, at: sim.Now(), event: ev, hasEvent: true, nodeID: pe.Node, node: pe.NodeName, recordSeq: seq,
			panic: &artifact.Panic{Value: text, In: "callback", Site: site, Stack: stack}}, false
	case err != nil: // row 5
		f := &failure{kind: "fail", context: "fail-loop", message: err.Error(), at: sim.Now(), err: err}
		kv := []string{"kind", "fail", "error", err.Error()}
		if w.failedOutsideLoop {
			f.context = "fail-body"
		} else {
			f.event, f.hasEvent = ev, true
			kv = append(kv, "event", itoa(ev))
		}
		f.recordSeq = w.emit("check.violation", "simulation failed", kv...)
		return f, false
	case limitStop && !w.r.plan.opts.AllowLimit: // row 6
		lim := &artifact.Limit{Name: "MaxEvents", Value: w.r.plan.opts.MaxEvents}
		check := "max-events"
		if w.lastStop == kernel.StopMaxTime {
			lim, check = &artifact.Limit{Name: "MaxTime", Value: 0}, "max-time"
		}
		seq := w.emit("check.violation", lim.Name+" exceeded", "kind", "limit", "limit", check, "value", itoa(lim.Value), "event", itoa(ev))
		return &failure{kind: "limit", check: check, context: "limit", message: "likely a livelock or a runaway timer; raise Options.MaxEvents or set Options.AllowLimit",
			at: sim.Now(), event: ev, hasEvent: true, recordSeq: seq, limit: lim}, false
	case finalFail != nil: // row 7
		return finalFail, false
	case w.primary && w.st.Failed() && !failedAtStart: // row 8
		return &failure{kind: "fail", context: "fail-test", message: "see the t.Error output above", at: sim.Now()}, false
	}
	return nil, limitStop // row 9 (limited pass) or row 10 (pass)
}

// attempt runs one attempt of seed (API-030 to API-061, API-093). It depends only on its
// arguments and the resolved options. A runtime.Goexit inside it unwinds to runSeed; r.cur holds
// the World for API-101.
func (r *runner) attempt(st *testing.T, seed uint64, traceCfg kernel.TraceConfig, sched *fault.Schedule, primary bool) (res attemptResult) {
	r.runAttempt(st, func(st *testing.T) { res = r.attemptBody(st, seed, traceCfg, sched, primary) })
	return res
}

// runAttempt is the goroutine-mode seam of API-090: in event mode it calls fn directly; GOR
// (Phase 2) runs fn inside a synctest bubble.
func (r *runner) runAttempt(st *testing.T, fn func(st *testing.T)) { fn(st) }

// attemptBody is one attempt in event mode: build, body, drive, check, classify.
func (r *runner) attemptBody(st *testing.T, seed uint64, traceCfg kernel.TraceConfig, sched *fault.Schedule, primary bool) attemptResult {
	failedAtStart := st.Failed()
	w, setupErr := r.newWorld(st, seed, traceCfg, sched, primary)
	if setupErr != "" {
		return attemptResult{setupErr: setupErr}
	}
	// The attempt ends in phase done however it returns, also with a setup error (API-032), so a
	// World that body kept cannot drive the abandoned kernel or register anything.
	defer func() { w.phase = phaseDone }()
	r.cur = w
	out := w.callBody(r.body)
	if out.setupErr != "" {
		return attemptResult{setupErr: out.setupErr}
	}
	w.phase = phaseRun
	if !out.aborted && out.panic == nil {
		if msg := w.startFaultsAfterBody(); msg != "" {
			return attemptResult{setupErr: msg}
		}
		if w.Sim.Err() != nil {
			w.failedOutsideLoop = true
		} else if w.Sim.Now() <= w.End() {
			w.emit("run.phase", "run", "phase", "run")
			w.drive(w.End())
		}
	}
	var finalFail *failure
	if out.panic == nil && w.Sim.Err() == nil && w.drove && (w.lastStop == kernel.StopIdle || w.lastStop == kernel.StopDeadline) {
		finalFail = w.runFinals()
	}
	f, limited := w.classify(out.panic, finalFail, failedAtStart)
	stop := "none"
	if w.drove {
		stop = w.lastStop.String()
	}
	w.emit("run.phase", "end", "phase", "end", "stop", stop, "events", itoa(w.Sim.Executed()))
	w.phase = phaseDone
	return w.result(f, limited, stop)
}

// headline returns failure.headline (API-067).
func (f *failure) headline() string {
	at := f.at.String()
	on := ""
	if f.node != "" {
		on = " on " + f.node
	}
	ev := ""
	if f.hasEvent {
		ev = fmt.Sprintf(" (event %d)", f.event)
	}
	switch f.context {
	case "invariant":
		return fmt.Sprintf("invariant \"%s\" violated at t=%s%s%s", f.check, at, on, ev)
	case "final":
		return fmt.Sprintf("final check \"%s\" failed after the run at t=%s", f.check, at)
	case "panic-callback":
		return fmt.Sprintf("panic at t=%s%s%s", at, on, ev)
	case "panic-invariant":
		return fmt.Sprintf("panic in invariant \"%s\" at t=%s%s%s", f.name, at, on, ev)
	case "panic-final":
		return fmt.Sprintf("panic in final check \"%s\" after the run at t=%s", f.name, at)
	case "panic-body":
		return fmt.Sprintf("panic in body at t=%s", at)
	case "fail-loop":
		return fmt.Sprintf("simulation failed at t=%s%s%s", at, on, ev)
	case "fail-body":
		return fmt.Sprintf("simulation failed in body at t=%s", at)
	case "fail-test":
		return fmt.Sprintf("test marked failed during the run (t.Error, t.Errorf or t.Fail) at t=%s", at)
	case "limit":
		return fmt.Sprintf("run exceeded %s (%d) at t=%s%s", f.limit.Name, f.limit.Value, at, ev)
	case "determinism-artifact":
		d := f.determinism
		return fmt.Sprintf("determinism failure: re-running seed 0x%016x gave trace hash 0x%016x; the first run gave 0x%016x", d.seed, d.hashes[1], d.hashes[0])
	case "determinism-check":
		d := f.determinism
		return fmt.Sprintf("determinism failure: two runs of seed 0x%016x gave trace hashes 0x%016x and 0x%016x", d.seed, d.hashes[0], d.hashes[1])
	}
	return f.kind + " failure at t=" + at
}

// startFaultsAfterBody starts faults after body returned and turns a misuse into a setup error.
func (w *World) startFaultsAfterBody() (setupErr string) {
	defer func() {
		if v := recover(); v != nil {
			m, ok := v.(misuse)
			if !ok {
				panic(v)
			}
			setupErr = m.msg
		}
	}()
	w.startFaults()
	return ""
}

// result collects the attempt's data (API-093).
func (w *World) result(f *failure, limited bool, stop string) attemptResult {
	res := attemptResult{
		fail: f, limited: limited, stop: stop, end: w.End(),
		hash: w.Sim.TraceHash(), executed: w.Sim.Executed(), now: w.Sim.Now(), lastSeq: w.Sim.Cause(),
		records: w.Sim.Records(),
	}
	res.recovery, res.hasRecovery = w.recovery()
	res.schedule = w.Faults.Applied()
	res.schedule.End, res.schedule.Recovery = w.End(), w.RecoveryStart()
	if len(w.History.Ops()) > 0 {
		var b bytes.Buffer
		if err := w.History.WriteJSONL(&b); err == nil {
			res.history = b.Bytes()
		}
	}
	for _, n := range w.Sim.Nodes() {
		res.nodes = append(res.nodes, artifact.Node{ID: int32(n.ID()), Name: n.Name(), Tags: n.Tags()})
	}
	for _, reg := range w.planners {
		res.planners = append(res.planners, reg.name)
	}
	if f != nil && f.hasEvent && f.recordSeq != 0 && f.node == "" && res.records != nil {
		f.nodeID, f.node = nodeBefore(res.records, f.recordSeq, w.Sim)
	}
	res.extra, res.warnings = extraFiles(f)
	return res
}

// nodeBefore returns the node of the latest kernel.event record with Seq < seq (API-066).
func nodeBefore(records []kernel.Record, seq uint64, sim *kernel.Sim) (kernel.NodeID, string) {
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Seq >= seq || r.Kind != "kernel.event" {
			continue
		}
		if r.Node == 0 {
			return 0, ""
		}
		if n := sim.Node(r.Node); n != nil {
			return r.Node, n.Name()
		}
		return 0, ""
	}
	return 0, ""
}
