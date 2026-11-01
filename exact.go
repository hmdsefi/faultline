// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/internal/gosched"
)

// exactConsoleLine replaces GOR-077's console line for a seed the toolexec backend ran exactly.
const exactConsoleLine = "  determinism: exact (toolexec backend)"

// gapsFile is the artifact file that lists the coverage gaps of the test binary (XTE-052).
const gapsFile = "toolexec-gaps.txt"

// toolexecActive reports whether this test binary was built with the toolexec backend.
func toolexecActive() bool { return gosched.Active != nil }

// gapLines returns the coverage gaps of the test binary, or nil without the backend.
func gapLines() []string {
	if a := gosched.Active; a != nil {
		return a.Gaps()
	}
	return nil
}

// exactCheck is XTE-110's setup check of FAULTLINE_EXACT=1 for the resolved options o.
func exactCheck(o Options, require, toolexec bool) error {
	if !require || o.Mode != ModeGoroutine {
		return nil
	}
	if !toolexec {
		return errors.New("faultline: FAULTLINE_EXACT=1, but this test binary was built without the toolexec backend; build it with go test -toolexec=faultline-toolexec after running faultline-toolexec prepare")
	}
	if o.Procs != 1 {
		return fmt.Errorf("faultline: FAULTLINE_EXACT=1, but Options.Procs is %d; exact replay needs Procs 1", o.Procs)
	}
	return nil
}

// procsLog is XTE-091's log line, or "".
func procsLog(o Options, toolexec bool) string {
	if !toolexec || o.Mode != ModeGoroutine || o.Procs == 1 {
		return ""
	}
	return fmt.Sprintf("faultline: toolexec backend: Options.Procs is %d; goroutine-mode seeds replay exactly only with Procs 1, so these seeds are best-effort", o.Procs)
}

// exact reports whether the runner's goroutine-mode seeds run exactly (XTE-090).
func (r *runner) exact() bool {
	o := r.plan.opts
	return o.Mode == ModeGoroutine && toolexecActive() && o.Procs == 1
}

// determinism returns report.json's and the results line's determinism_level and
// exact_backend (GOR-078, XTE-090).
func (r *runner) determinism() (level, backend string) {
	if r.exact() {
		return "exact", "toolexec"
	}
	return determinismLevel(r.plan.opts.Mode), ""
}

// exactMessage replaces GOR-077's level-3 line in a determinism message of an exact seed with
// XTE-090's line.
func (r *runner) exactMessage(f *failure) {
	if f == nil || !r.exact() {
		return
	}
	line := "determinism: exact (toolexec backend): a divergence comes from code the rewriter does not control: " +
		strconv.Itoa(len(gapLines())) + " coverage gaps in this test binary, packages outside the rewrite set, sync/atomic, or state shared between runs"
	f.message = strings.Replace(f.message, gorLevel3Line, line, 1)
}

// seedConsoleLines is consoleLines with the determinism and gaps lines of the toolexec backend,
// placed before the warnings (XTE-052, XTE-090).
func (r *runner) seedConsoleLines(f *failure, replayCommand, artifactsLine string, warnings []string, full, written bool) []string {
	goroutine := r.plan.opts.Mode == ModeGoroutine
	lines := consoleLines(f, replayCommand, artifactsLine, warnings, full, written, goroutine && !r.exact())
	if !goroutine || !toolexecActive() {
		return lines
	}
	var extra []string
	if r.exact() {
		extra = append(extra, exactConsoleLine)
	}
	if n := len(gapLines()); n > 0 {
		l := "  coverage gaps: " + strconv.Itoa(n) + " in this test binary"
		if written {
			l += "; " + gapsFile + " in the artifact directory lists them"
		}
		extra = append(extra, l)
	}
	at := len(lines) - len(warnings)
	return slices.Concat(lines[:at], extra, lines[at:])
}

// addGapsFile adds toolexec-gaps.txt to the artifact of a failing goroutine-mode seed (XTE-052).
// An extra file of the same name that a check supplied wins.
func (r *runner) addGapsFile(a *artifact.Artifact) {
	gaps := gapLines()
	if r.plan.opts.Mode != ModeGoroutine || len(gaps) == 0 {
		return
	}
	if a.Extra == nil {
		a.Extra = map[string][]byte{}
	}
	if _, taken := a.Extra[gapsFile]; !taken {
		a.Extra[gapsFile] = []byte(strings.Join(gaps, "\n") + "\n")
	}
}

// toolexecValue is XTE-100's -toolexec value of a replay command, shell-quoted: wrapper, or
// faultline-toolexec when it is empty, in single quotes first when it holds white space (the go
// command splits the value into words).
func toolexecValue(wrapper string) string {
	if wrapper == "" {
		wrapper = "faultline-toolexec"
	}
	if strings.ContainsAny(wrapper, " \t\n") {
		wrapper = "'" + wrapper + "'"
	}
	return shellQuote(wrapper)
}

// replayToolexec returns the -toolexec value for the runner's replay commands, or "".
func (r *runner) replayToolexec() string {
	if r.plan.opts.Mode != ModeGoroutine || !toolexecActive() {
		return ""
	}
	return toolexecValue(gosched.Wrapper)
}
