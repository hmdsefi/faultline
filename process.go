// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"sync"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// Errors that end a process. Each one is the context.Cause of the process's context, and
// Process.Do returns it without running fn once the process is no longer alive.
var (
	// ErrCrashed means the process's node crashed.
	ErrCrashed = errors.New("faultline: process crashed")
	// ErrExited means the process's main function returned or panicked.
	ErrExited = errors.New("faultline: process exited")
	// ErrRunEnded means the run ended. Processes then get Options.Drain of virtual time to exit.
	ErrRunEnded = errors.New("faultline: run ended")
)

// ProcessError is the error a run fails with when a process's main function returns a non-nil
// error. An error returned after the process's node crashed, or after the run ended, does not
// fail the run.
type ProcessError struct {
	Node string // node name
	Inc  uint32 // incarnation of the process (kernel.Node.Incarnation)
	Err  error  // the error main returned
}

// Error returns "process <Node> (incarnation <Inc>) returned an error: <Err>".
func (e *ProcessError) Error() string {
	return fmt.Sprintf("process %s (incarnation %d) returned an error: %s", e.Node, e.Inc, e.Err.Error())
}

// Unwrap returns Err.
func (e *ProcessError) Unwrap() error { return e.Err }

// ProcessFunc is the main function of a process. It runs once per incarnation, on its own
// goroutine, and should return when ctx is done. Returning nil ends the process and leaves its
// node up. Returning an error or panicking fails the run, unless the incarnation already ended.
// t.FailNow and t.Fatal stop the run, as they do in the function passed to Run. Calling
// runtime.Goexit without failing the test is the same as returning nil.
type ProcessFunc func(ctx context.Context, p *Process) error

// Process is one incarnation of a process node. A new Process is created at every boot.
type Process struct {
	Node *kernel.Node // only ID and Name may be called from process goroutines; use Rand, not Node.Rand

	d      *driver
	inc    uint32
	rnd    *rand.Rand
	ctx    context.Context
	cancel context.CancelCauseFunc

	// Guarded by the world lock.
	status      incStatus
	endAt       kernel.Time // crash or exit time
	ghostLogged bool        // GOR-052: logged a ghost panic once
	g0          kernel.Time // GOR-055 clock state
	m0          time.Duration
	ppm         int32
}

type incStatus uint8

const (
	incRunning incStatus = iota + 1
	incCrashed
	incExited
)

// nodeProc is the driver's per-node table (slices indexed by node ID, GOR §11).
type nodeProc struct {
	main    ProcessFunc   // nil: not a process node
	procs   []*Process    // one per incarnation, ascending
	pending []*request    // scheduled requests of the current incarnation, ascending req
	waitCh  chan struct{} //nolint:unused // WaitUnpaused waiters (GOR-041)
}

// lockedSource makes a stream safe for concurrent use (GOR-046 step 4).
type lockedSource struct {
	mu  sync.Mutex
	src rand.Source
}

func (s *lockedSource) Uint64() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src.Uint64()
}

// Rand returns the stream "node/<name>/<incarnation>", wrapped so that it is safe for concurrent
// use. Draws from more than one goroutine can come in a different order when the seed replays.
// Node.Rand is the same stream without the lock, so draw from Rand everywhere, checks and kernel
// callbacks included.
func (p *Process) Rand() *rand.Rand { return p.rnd }

// Context returns the context passed to main.
func (p *Process) Context() context.Context { return p.ctx }

// Incarnation returns the incarnation this Process belongs to.
func (p *Process) Incarnation() uint32 { return p.inc }

// AddProcess adds a process node tagged "server" and "process". It equals
// w.Sim.AddNode(name, boot, append(opts, kernel.WithTags("server", "process"))...), where boot
// starts main on its own goroutine, with a new Process, at every boot of the node. AddProcess
// panics unless Options.Mode is ModeGoroutine.
func (w *World) AddProcess(name string, main ProcessFunc, opts ...kernel.NodeOption) *kernel.Node {
	return w.addProcess("AddProcess", "server", name, main, opts)
}

// AddClientProcess adds a process node tagged "client" and "process". Apart from the tags, it
// works like AddProcess.
func (w *World) AddClientProcess(name string, main ProcessFunc, opts ...kernel.NodeOption) *kernel.Node {
	return w.addProcess("AddClientProcess", "client", name, main, opts)
}

func (w *World) addProcess(method, role, name string, main ProcessFunc, opts []kernel.NodeOption) *kernel.Node {
	if w.drv == nil {
		panicMisuse("faultline: World.%s requires Options.Mode == faultline.ModeGoroutine", method)
	}
	w.mustDriver(method)
	if main == nil {
		panicMisuse("faultline: World.%s %q: nil ProcessFunc", method, name)
	}
	w.checkNotEnded(method) // API §7.2's phase message (GOR-003)
	o := append(append([]kernel.NodeOption(nil), opts...), kernel.WithTags(role, "process"))
	n := w.Sim.AddNode(name, w.drv.processBoot(main), o...)
	w.drv.node(n.ID()).main = main
	return n
}

// Process returns the Process of node id's current incarnation. It returns nil if id is not a
// process node, its process exited or crashed, the run ended, or Options.Mode is ModeEvent.
func (w *World) Process(id kernel.NodeID) *Process {
	w.mustDriver("Process")
	if w.drv == nil {
		return nil
	}
	defer w.drv.acquire()() // checks run without the lock
	n := w.Sim.Node(id)
	if n == nil || !w.drv.Alive(id, n.Incarnation()) {
		return nil
	}
	return w.drv.proc(id, n.Incarnation())
}

func (d *driver) node(id kernel.NodeID) *nodeProc {
	for int(id) >= len(d.nodes) {
		d.nodes = append(d.nodes, nil)
	}
	if d.nodes[id] == nil {
		d.nodes[id] = &nodeProc{}
	}
	return d.nodes[id]
}

// proc returns the Process of incarnation inc of node id, or nil.
func (d *driver) proc(id kernel.NodeID, inc uint32) *Process {
	if id < 1 || int(id) >= len(d.nodes) || d.nodes[id] == nil {
		return nil
	}
	ps := d.nodes[id].procs
	for i := len(ps) - 1; i >= 0 && ps[i].inc >= inc; i-- {
		if ps[i].inc == inc {
			return ps[i]
		}
	}
	return nil
}

// processBoot returns the boot function of a process node (GOR-046).
func (d *driver) processBoot(main ProcessFunc) kernel.BootFunc {
	return func(n *kernel.Node) {
		id, inc := n.ID(), n.Incarnation()
		p := &Process{Node: n, d: d, inc: inc, status: incRunning} // step 1
		np := d.node(id)
		np.procs = append(np.procs, p)
		// Steps 2 (the netsim host) and 3 (the fsx file system) go here, with the shims.
		// Step 4: the node's seeded stream, safe for concurrent use.
		p.rnd = rand.New(&lockedSource{src: n.Rand()}) //nolint:gosec // seeded on purpose: a deterministic simulator needs reproducible streams
		// Step 5: the clock state of GOR-055.
		p.g0, p.m0, p.ppm = d.w.Sim.Now(), 0, n.Drift()
		// Steps 6 and 7: p is complete once it has its context.
		base := pprof.WithLabels(context.Background(), pprof.Labels(
			"faultline_run", d.ga.run,
			"faultline_node", strconv.Itoa(int(id)),
			"faultline_inc", strconv.FormatUint(uint64(inc), 10)))
		p.ctx, p.cancel = context.WithCancelCause(base)
		// Step 8.
		d.w.Sim.Emit(kernel.Record{Node: id, Inc: inc, Kind: "driver.process.start", Text: "process start"})
		go d.runProcess(p, main) // step 9
	}
}

// runProcess runs one incarnation's main on its goroutine (GOR-047).
func (d *driver) runProcess(p *Process, main ProcessFunc) {
	pprof.SetGoroutineLabels(p.ctx)
	var (
		err      error
		text     string // err.Error()
		returned bool   // false after runtime.Goexit
		panicked bool
		value    any
		stack    []byte
	)
	defer func() { // deferred, so that it also runs when main calls runtime.Goexit
		d.Lock()
		defer d.Unlock()
		alive := d.Alive(p.Node.ID(), p.inc)
		switch {
		case panicked && alive:
			d.processPanic(p, value, stack, "process")
		case panicked:
			d.ghostPanic(p, value)
		case !alive:
		case !returned:
			d.processGoexit(p)
		case err != nil:
			d.processError(p, err, text)
		default:
			d.Post(p.Node.ID(), p.inc, "process.exit", "process.exit", func() { d.exitOK(p) }, func() {})
		}
	}()
	func() {
		defer func() {
			if v := recover(); v != nil {
				panicked, value, stack = true, v, debug.Stack()
			}
		}()
		err = main(p.ctx, p)
		if err != nil {
			text = err.Error() // an Error method that panics is a panic of main
		}
		returned = true
	}()
}

// exitOK is the process.exit request (GOR-048).
func (d *driver) exitOK(p *Process) {
	d.w.Sim.Emit(kernel.Record{Node: p.Node.ID(), Inc: p.inc, Kind: "driver.process.exit", Text: "process exit: ok",
		Attrs: []kernel.Attr{{Key: "status", Value: "ok"}, {Key: "error", Value: ""}}})
	d.endIncarnation(p, incExited, ErrExited, "exited")
}

// processGoexit handles a live process whose main called runtime.Goexit (GOR-047). After t.FailNow
// or t.Fatal, which mark the test failed, it ends the incarnation and makes the driver stop the run
// at once; otherwise the Goexit is an exit, as a return of nil is.
func (d *driver) processGoexit(p *Process) {
	if !d.ga.bt.Failed() {
		d.Post(p.Node.ID(), p.inc, "process.exit", "process.exit", func() { d.exitOK(p) }, func() {})
		return
	}
	d.SyncClock()
	d.endIncarnation(p, incExited, ErrExited, "exited")
	d.failNow = true
	d.signal()
}

// processError handles a live process returning an error; text is err.Error() (GOR-049).
func (d *driver) processError(p *Process, err error, text string) {
	d.SyncClock()
	d.w.Sim.Emit(kernel.Record{Node: p.Node.ID(), Inc: p.inc, Kind: "driver.process.exit", Text: "process exit: error",
		Attrs: []kernel.Attr{{Key: "status", Value: "error"}, {Key: "error", Value: text}}})
	d.w.Sim.Fail(&ProcessError{Node: p.Node.Name(), Inc: p.inc, Err: err})
	d.endIncarnation(p, incExited, ErrExited, "exited")
	d.signal() // wake the driver at the failure instant (GOR-024)
}

// processPanic handles a panic of a live incarnation in main (label "process") or in a Process.Go
// goroutine (label "process.Go") (GOR-050).
func (d *driver) processPanic(p *Process, v any, stack []byte, label string) {
	d.SyncClock()
	id, text := p.Node.ID(), panicText(v)
	if label == "process" {
		d.w.Sim.Emit(kernel.Record{Node: id, Inc: p.inc, Kind: "driver.process.exit", Text: "process exit: panic",
			Attrs: []kernel.Attr{{Key: "status", Value: "panic"}, {Key: "error", Value: text}}})
	} else {
		d.w.Sim.Emit(kernel.Record{Node: id, Inc: p.inc, Kind: "driver.process.panic", Text: "process goroutine panic",
			Attrs: []kernel.Attr{{Key: "error", Value: text}}})
	}
	d.w.Sim.Fail(&kernel.PanicError{Value: v, Stack: stack, At: d.w.Sim.Now(), Event: 0, Label: label,
		Node: id, NodeName: p.Node.Name(), Inc: p.inc, Seed: d.w.Seed()})
	if label == "process" {
		d.endIncarnation(p, incExited, ErrExited, "exited")
	}
	d.signal()
}

// ghostPanic logs a panic of an incarnation that is no longer alive, once per incarnation (GOR-052).
func (d *driver) ghostPanic(p *Process, v any) {
	if p.ghostLogged {
		return
	}
	p.ghostLogged = true
	d.ga.bt.Logf("faultline: ghost of %s (incarnation %d) panicked after it stopped: %s", p.Node.Name(), p.inc, panicText(v))
}

// endIncarnation performs GOR-048's steps after its record: the status, the end time, the
// context and the scheduled requests.
func (d *driver) endIncarnation(p *Process, status incStatus, cause error, reason string) {
	p.status = status
	p.endAt = d.w.Sim.Now()
	p.cancel(cause)
	d.cancelScheduled(p.Node.ID(), reason)
}
