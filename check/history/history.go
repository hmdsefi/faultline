// Package history records client operations of a simulation run: invocations, completions,
// virtual times and their exact order.
//
// A Recorder must be used from the simulation goroutine only; it is not safe for concurrent use.
package history

import (
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// Status is the outcome of an operation.
type Status uint8

const (
	// Pending: invoked, not completed (yet).
	Pending Status = iota
	// OK: completed and took effect; Output is its result.
	OK
	// Fail: completed and definitely did not take effect.
	Fail
	// Info: indeterminate. The operation may have taken effect, or may still take effect at any
	// later time (timeout, lost reply, crashed client).
	Info
)

// String returns "pending", "ok", "fail", "info", or "status(<n>)" for other values.
func (s Status) String() string {
	switch s {
	case Pending:
		return "pending"
	case OK:
		return "ok"
	case Fail:
		return "fail"
	case Info:
		return "info"
	}
	return "status(" + strconv.Itoa(int(s)) + ")"
}

// Op is one operation.
//
// In every Op returned by this package (Recorder.Ops), Input and Output hold a
// json.RawMessage with the compact JSON encoding of the recorded value ("null" for nil and for
// the Output of a pending op). Use DecodeInput and DecodeOutput to get typed values.
type Op struct {
	ID      int64       // 1-based, dense, in invocation order
	Process string      // process name
	F       string      // function name: "read", "write", "cas", "append", "txn", ...
	Input   any         // json.RawMessage
	Output  any         // json.RawMessage
	Call    kernel.Time // virtual time of the invocation
	Return  kernel.Time // virtual time of the completion; 0 while pending
	Status  Status

	CallIndex   int64 // event index of the invocation (>= 1)
	ReturnIndex int64 // event index of the completion; 0 while pending
}

// Precedes reports whether op completed OK before b was invoked (HIS-011).
func (op Op) Precedes(b Op) bool {
	return op.Status == OK && op.ReturnIndex < b.CallIndex
}
