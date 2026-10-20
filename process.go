// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"errors"
	"fmt"
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
