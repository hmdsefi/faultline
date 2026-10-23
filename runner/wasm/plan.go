// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package wasm

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// PlanPrefix starts every plan line: "faultline-exact-plan\t<test>\t0x%016x\n".
const PlanPrefix = "faultline-exact-plan\t"

// PlanEntry is one plan line.
type PlanEntry struct {
	Test string
	Seed uint64
}

// ParsePlanLine parses a plan line without its newline.
func ParsePlanLine(line string) (PlanEntry, error) {
	bad := fmt.Errorf("bad plan line %q", line)
	rest, ok := strings.CutPrefix(line, PlanPrefix)
	if !ok {
		return PlanEntry{}, bad
	}
	test, seed, ok := strings.Cut(rest, "\t")
	hex, isHex := strings.CutPrefix(seed, "0x")
	if !ok || test == "" || !isHex || len(hex) != 16 {
		return PlanEntry{}, bad
	}
	v, err := strconv.ParseUint(hex, 16, 64)
	if err != nil {
		return PlanEntry{}, bad
	}
	return PlanEntry{Test: test, Seed: v}, nil
}

// PlanFilter forwards everything except plan lines to W and collects the plan entries in
// order. A plan line may follow output that has no newline; that output is forwarded. Partial
// lines are held until their newline or Flush.
type PlanFilter struct {
	W       io.Writer
	Entries []PlanEntry
	Err     error // the first malformed plan line
	buf     []byte
}

// Write handles every complete line in p and holds the rest. It always reports len(p) written.
func (f *PlanFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			break
		}
		f.line(f.buf[:i+1])
		f.buf = f.buf[i+1:]
	}
	return len(p), nil
}

// Flush forwards a held partial line.
func (f *PlanFilter) Flush() {
	if len(f.buf) > 0 {
		f.line(f.buf)
		f.buf = nil
	}
}

func (f *PlanFilter) line(l []byte) {
	i := bytes.Index(l, []byte(PlanPrefix))
	if i < 0 {
		_, _ = f.W.Write(l)
		return
	}
	if i > 0 { // output without a newline before the plan line
		_, _ = f.W.Write(l[:i])
		l = l[i:]
	}
	e, err := ParsePlanLine(strings.TrimSuffix(string(l), "\n"))
	if err != nil {
		if f.Err == nil {
			f.Err = err
		}
		return
	}
	f.Entries = append(f.Entries, e)
}

// RunPattern is the -test.run pattern that selects exactly test.
func RunPattern(test string) string {
	parts := strings.Split(test, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// RunArgs returns args without any -test.run or -test.count flag, with -test.run=<RunPattern(test)>
// and -test.count=1 appended, so that a run instance runs its seed once.
func RunArgs(args []string, test string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-test.run" || a == "--test.run" || a == "-test.count" || a == "--test.count":
			i++ // its value is the next argument
			continue
		case strings.HasPrefix(a, "-test.run=") || strings.HasPrefix(a, "--test.run="),
			strings.HasPrefix(a, "-test.count=") || strings.HasPrefix(a, "--test.count="):
			continue
		}
		out = append(out, a)
	}
	return append(out, "-test.run="+RunPattern(test), "-test.count=1")
}
