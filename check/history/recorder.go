// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
)

// Recorder records the operations of one simulation run.
type Recorder struct {
	s       *kernel.Sim
	ops     []Op
	next    int64            // next event index (HIS-001)
	pending map[string]int64 // process -> pending op ID; lookups only
}

// NewRecorder returns an empty recorder that takes times from s and emits records to s.
// It panics if s is nil.
func NewRecorder(s *kernel.Sim) *Recorder {
	if s == nil {
		panic("history: NewRecorder: nil *kernel.Sim")
	}
	return &Recorder{s: s, next: 1, pending: map[string]int64{}}
}

// Invoke records the invocation of f by process with input at s.Now() and returns the new
// op's ID. It panics on misuse (HIS-003).
func (r *Recorder) Invoke(process, f string, input any) int64 {
	switch {
	case process == "":
		panic("history: Invoke: process must not be empty")
	case !utf8.ValidString(process):
		panic("history: Invoke: process is not valid UTF-8")
	case f == "":
		panic("history: Invoke: f must not be empty")
	case !utf8.ValidString(f):
		panic("history: Invoke: f is not valid UTF-8")
	}
	if id, ok := r.pending[process]; ok {
		panic(fmt.Sprintf("history: Invoke: process %q has pending op %d (%s)", process, id, r.ops[id-1].F))
	}
	in, err := json.Marshal(input)
	if err != nil {
		panic(fmt.Sprintf("history: Invoke: process %q f %q: input is not JSON-encodable: %v", process, f, err))
	}
	in = replaceInvalid(in)
	id := int64(len(r.ops)) + 1
	r.ops = append(r.ops, Op{
		ID: id, Process: process, F: f, Input: json.RawMessage(in), Output: json.RawMessage("null"),
		Call: r.s.Now(), Status: Pending, CallIndex: r.next,
	})
	r.next++
	r.pending[process] = id
	r.emit("history.invoke", process, process+" invoke "+f+" "+string(in),
		kernel.Attr{Key: "id", Value: strconv.FormatInt(id, 10)}, kernel.Attr{Key: "process", Value: process},
		kernel.Attr{Key: "f", Value: f}, kernel.Attr{Key: "input", Value: string(in)})
	return id
}

// Complete records the completion of op id with status and output at s.Now(). status must be
// OK, Fail or Info. It panics on misuse (HIS-005).
func (r *Recorder) Complete(id int64, status Status, output any) {
	n := int64(len(r.ops))
	if id < 1 || id > n {
		if n == 0 {
			panic(fmt.Sprintf("history: Complete: unknown op id %d (have none)", id))
		}
		panic(fmt.Sprintf("history: Complete: unknown op id %d (have 1..%d)", id, n))
	}
	op := &r.ops[id-1]
	if op.Status != Pending {
		panic(fmt.Sprintf("history: Complete: op %d (%s %s) already completed with status %s", id, op.Process, op.F, op.Status))
	}
	if status != OK && status != Fail && status != Info {
		panic(fmt.Sprintf("history: Complete: op %d (%s %s): invalid status %s", id, op.Process, op.F, status))
	}
	out, err := json.Marshal(output)
	if err != nil {
		panic(fmt.Sprintf("history: Complete: op %d (%s %s): output is not JSON-encodable: %v", id, op.Process, op.F, err))
	}
	out = replaceInvalid(out)
	op.Status, op.Output, op.Return, op.ReturnIndex = status, json.RawMessage(out), r.s.Now(), r.next
	r.next++
	delete(r.pending, op.Process)
	r.emit("history.complete", op.Process, op.Process+" "+status.String()+" "+op.F+" "+string(out),
		kernel.Attr{Key: "id", Value: strconv.FormatInt(id, 10)}, kernel.Attr{Key: "process", Value: op.Process},
		kernel.Attr{Key: "f", Value: op.F}, kernel.Attr{Key: "status", Value: status.String()},
		kernel.Attr{Key: "output", Value: string(out)})
}

// escFFFD is the escape that json.Marshal writes on Go 1.26 for a byte that is not valid UTF-8.
const escFFFD = `\ufffd`

// replaceInvalid returns b, the output of one json.Marshal call, with each \ufffd escape and each
// byte that is not part of valid UTF-8 replaced by U+FFFD itself (HIS-002). For a string that is
// not valid UTF-8, json.Marshal writes U+FFFD for each invalid byte: as the escape on Go 1.26 and
// as the character on Go 1.27. It copies the bytes of a json.Marshaler as they are on both. So the
// result is the same on both versions, apart from the exceptions HIS-002 names. b itself is
// returned when it is valid UTF-8 and holds no \ufffd substring; otherwise the result is a copy,
// also when it equals b (an escaped backslash before the text ufffd).
func replaceInvalid(b []byte) []byte {
	if utf8.Valid(b) && !bytes.Contains(b, []byte(escFFFD)) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		switch {
		case bytes.HasPrefix(b[i:], []byte(escFFFD)):
			out, i = utf8.AppendRune(out, utf8.RuneError), i+len(escFFFD)
		case b[i] == '\\' && i+1 < len(b):
			// Any other escape is copied whole, so the second backslash of \\ never starts one.
			out, i = append(out, b[i], b[i+1]), i+2
		case r == utf8.RuneError && n == 1:
			out, i = utf8.AppendRune(out, utf8.RuneError), i+1
		default:
			out, i = append(out, b[i:i+n]...), i+n
		}
	}
	return out
}

// emit emits a record on the node named process, or globally if there is none (HIS §8).
func (r *Recorder) emit(kind, process, text string, attrs ...kernel.Attr) {
	rec := kernel.Record{Kind: kind, Text: text, Attrs: attrs}
	if n := r.s.Lookup(process); n != nil {
		rec.Node = n.ID()
	}
	r.s.Emit(rec)
}

// Ops returns a copy of all operations, in ID order. Pending operations are included.
func (r *Recorder) Ops() []Op {
	out := make([]Op, len(r.ops))
	copy(out, r.ops)
	return out
}

// Len returns the number of operations recorded so far.
func (r *Recorder) Len() int { return len(r.ops) }
