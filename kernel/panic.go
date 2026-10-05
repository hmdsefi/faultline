package kernel

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
)

// PanicError is the error a Sim fails with when a guarded call panics or calls runtime.Goexit.
type PanicError struct {
	Value    any     // value passed to panic; nil if Goexit
	Goexit   bool    // the callback called runtime.Goexit (for example t.FailNow)
	Stack    []byte  // runtime/debug.Stack() at recovery; never hashed
	At       Time    // Now() at recovery
	Event    EventID // executing event; 0 for a guarded call outside the loop
	Label    string  // event label, or "crash"/"restart" for a guarded call outside the loop
	Node     NodeID  // node of the event or operation; 0 for global events
	NodeName string  // name of Node; "" if Node is 0
	Inc      uint32  // incarnation of the event, or of the node at the panic outside the loop
	Observer bool    // the panic happened in an OnEvent observer
	Seed     uint64
}

// Error formats the panic as specified in KRN-047.
func (e *PanicError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "kernel: panic at %s in event %d %q", e.At, e.Event, e.Label)
	if e.Node != 0 {
		fmt.Fprintf(&b, " on node %q (inc %d)", e.NodeName, e.Inc)
	}
	if e.Observer {
		b.WriteString(" (observer)")
	}
	fmt.Fprintf(&b, " (seed 0x%016x): %s", e.Seed, panicText(e.Value, e.Goexit))
	return b.String()
}

// Unwrap returns Value if it is an error, otherwise nil.
func (e *PanicError) Unwrap() error {
	if err, ok := e.Value.(error); ok {
		return err
	}
	return nil
}

// panicText is the text of a recovered value (KRN-044).
func panicText(v any, goexit bool) string {
	if goexit {
		return "runtime.Goexit called in simulation callback"
	}
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	}
	return Describe(v)
}

// guardCtx describes the code that the current guarded call runs (KRN-043). event is 0 for a
// guarded call outside the loop; node is nil for global events.
type guardCtx struct {
	event    EventID
	label    string
	node     *Node
	inc      uint32 // the incarnation in the event's kernel.event record
	observer bool
}

// guard runs fn as a guarded call (KRN-042): it recovers a panic and detects runtime.Goexit.
// After a Goexit the unwinding continues; the deferred functions of the callers still run.
func (s *Sim) guard(fn func()) {
	done := false
	defer func() {
		if !done {
			s.recordPanic(recover())
		}
	}()
	fn()
	done = true
}

// recordPanic fails the Sim with a *PanicError for the recovered value v (nil for Goexit) and
// emits kernel.panic, unless the Sim has already failed (KRN-042, KRN-043).
func (s *Sim) recordPanic(v any) {
	if s.err != nil {
		return
	}
	c := s.ctx
	pe := &PanicError{
		Value:    v,
		Goexit:   v == nil,
		Stack:    debug.Stack(),
		At:       s.now,
		Event:    c.event,
		Label:    c.label,
		Observer: c.observer,
		Seed:     s.cfg.Seed,
	}
	if c.node != nil {
		pe.Node, pe.NodeName, pe.Inc = c.node.id, c.node.name, c.inc
		if c.event == 0 {
			pe.Inc = c.node.inc // outside the loop: the incarnation at the time of the panic
		}
	}
	s.err = pe
	s.emit(Record{
		Kind: "kernel.panic",
		Node: pe.Node,
		Inc:  pe.Inc,
		Text: panicText(v, pe.Goexit),
		Attrs: []Attr{
			{Key: "event_id", Value: strconv.FormatUint(uint64(c.event), 10)},
			{Key: "label", Value: c.label},
			{Key: "observer", Value: strconv.FormatBool(c.observer)},
		},
	})
}
