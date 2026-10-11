// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// Sim is one simulated world. It is not safe for concurrent use.
type Sim struct {
	cfg      Config
	now      Time
	executed uint64
	err      error
	streams  map[string]*rand.Rand // lookups only (KRN-115)
	sched    *rand.Rand            // the "kernel/sched" stream
	tr       trace

	nextID  uint64             // last EventID handed out
	nextSeq uint64             // last queue seq handed out
	q       queue              // pending entries
	pending map[EventID]*entry // pending entries by ID, for Cancel (lookups only)
	free    []*entry           // recycled entries
	running bool               // inside Step, Run, RunUntil, RunFor or a guarded call

	observers []func() // OnEvent, in registration order
	ctx       guardCtx // what the current guarded call runs

	nodes      []*Node          // by ID - 1
	byName     map[string]*Node // lookups only
	bootHooks  []func(*Node)    // OnBoot, in registration order
	crashHooks []func(*Node)    // OnCrash, in registration order
}

// New validates cfg, creates a Sim at time 0 and emits the kernel.start record.
// It panics on an unknown TieBreak or Trace.Level, or a negative Trace.Buffer or MaxTime.
func New(cfg Config) *Sim {
	cfg.validate()
	s := &Sim{
		cfg:     cfg,
		streams: map[string]*rand.Rand{},
		tr:      newTrace(cfg.Trace),
		pending: map[EventID]*entry{},
		byName:  map[string]*Node{},
	}
	s.sched = s.stream("kernel/sched")
	s.emit(Record{
		Kind: "kernel.start",
		Text: "start",
		Attrs: []Attr{
			{Key: "seed", Value: fmt.Sprintf("0x%016x", cfg.Seed)},
			{Key: "tie_break", Value: cfg.TieBreak.String()},
		},
	})
	return s
}

// Config returns the configuration the Sim was created with.
func (s *Sim) Config() Config { return s.cfg }

// Seed returns cfg.Seed.
func (s *Sim) Seed() uint64 { return s.cfg.Seed }

// Now returns the current virtual time.
func (s *Sim) Now() Time { return s.now }

// Rand returns the stream for label, creating it on first use. The same label always returns the
// same *rand.Rand within one Sim. Labels starting with "kernel/" or "node/" are reserved.
func (s *Sim) Rand(label string) *rand.Rand {
	if label == "" {
		panic("kernel: empty stream label")
	}
	if strings.HasPrefix(label, "kernel/") || strings.HasPrefix(label, "node/") {
		panic(fmt.Sprintf("kernel: stream label %q is reserved", label))
	}
	return s.stream(label)
}

// stream returns the stream for label without the reservation check (KRN-011, KRN-022).
func (s *Sim) stream(label string) *rand.Rand {
	if r, ok := s.streams[label]; ok {
		return r
	}
	r := newStream(s.cfg.Seed, label)
	s.streams[label] = r
	return r
}

// Logf emits a kernel.log record with Node 0 and Text fmt.Sprintf(format, args...).
func (s *Sim) Logf(format string, args ...any) {
	s.emit(Record{Kind: "kernel.log", Text: fmt.Sprintf(format, args...)})
}

// Fail records err as the run's failure. The first call wins; later calls are ignored.
// The loop stops after the current callback returns. It panics if err is nil.
func (s *Sim) Fail(err error) {
	if err == nil {
		panic("kernel: Fail called with a nil error")
	}
	if s.err != nil {
		return
	}
	s.err = err
	s.emit(Record{Kind: "kernel.fail", Text: err.Error()})
}

// Err returns the first error passed to Fail, a *PanicError, or nil.
func (s *Sim) Err() error { return s.err }

// Executed returns the number of events executed so far (callbacks run, including panicking ones).
func (s *Sim) Executed() uint64 { return s.executed }

// OnEvent registers an observer that runs after every executed event, in registration order.
func (s *Sim) OnEvent(fn func()) {
	if fn == nil {
		panic("kernel: nil function passed to OnEvent")
	}
	s.observers = append(s.observers, fn)
}

// OnBoot registers a subsystem hook that runs at every boot of every node, before the node's
// BootFunc, in registration order.
func (s *Sim) OnBoot(fn func(n *Node)) {
	if fn == nil {
		panic("kernel: nil function passed to OnBoot")
	}
	s.bootHooks = append(s.bootHooks, fn)
}

// OnCrash registers a subsystem hook that runs at every crash of every node, after the node is
// down, in registration order.
func (s *Sim) OnCrash(fn func(n *Node)) {
	if fn == nil {
		panic("kernel: nil function passed to OnCrash")
	}
	s.crashHooks = append(s.crashHooks, fn)
}
