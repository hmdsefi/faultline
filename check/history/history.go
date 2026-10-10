// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package history records the client operations of a simulation run: invocations, completions,
// virtual times and their exact order. It writes and reads the history.jsonl format.
//
// A client calls Invoke when it sends a request and Complete when it learns the outcome:
//
//	id := w.History.Invoke("c1", "put", map[string]string{"key": "k1", "value": "v1"})
//	// ... later, when the reply arrives:
//	w.History.Complete(id, history.OK, nil)
//
// A client that times out cannot know whether the request took effect, so it completes the op
// with Info.
//
// Inside faultline.Run, World.History is the recorder of the run. The artifact of a failing seed
// holds it as history.jsonl when it has at least one operation.
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
	// Pending means invoked and not completed.
	Pending Status = iota
	// OK means completed and took effect; Output is its result.
	OK
	// Fail means completed and did not take effect.
	Fail
	// Info means the outcome is unknown: the operation may have taken effect, or may still take
	// effect later. A timeout, a lost reply or a crashed client gives Info.
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
// In every Op returned by this package (Recorder.Ops, Read), Input and Output hold a
// json.RawMessage with the compact JSON encoding of the recorded value ("null" for nil and for
// the Output of a pending op). Invalid UTF-8 in a recorded value is stored as U+FFFD, one per
// invalid byte. Use DecodeInput and DecodeOutput to get typed values.
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

// Precedes reports whether op completed OK before b was invoked. It compares ReturnIndex with
// CallIndex, not times, so within one instant an op that completed before b's invocation still
// precedes b. Fail, Info and Pending ops precede nothing.
func (op Op) Precedes(b Op) bool {
	return op.Status == OK && op.ReturnIndex < b.CallIndex
}
