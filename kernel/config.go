// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"fmt"
	"strconv"
)

// TieBreak selects how events with equal times are ordered.
type TieBreak uint8

const (
	TieBreakSeeded TieBreak = iota // tie-break drawn from the "kernel/sched" stream (default)
	TieBreakFIFO                   // tie-break 0: equal times run in scheduling order (debugging)
)

// String returns "seeded", "fifo", or "TieBreak(<n>)" for other values.
func (b TieBreak) String() string {
	switch b {
	case TieBreakSeeded:
		return "seeded"
	case TieBreakFIFO:
		return "fifo"
	}
	return "TieBreak(" + strconv.Itoa(int(b)) + ")"
}

// TraceLevel selects whether records are kept in addition to being hashed.
type TraceLevel uint8

const (
	TraceHash TraceLevel = iota // hash records and discard them (default)
	TraceFull                   // hash records and keep them
)

// String returns "hash", "full", or "TraceLevel(<n>)" for other values.
func (l TraceLevel) String() string {
	switch l {
	case TraceHash:
		return "hash"
	case TraceFull:
		return "full"
	}
	return "TraceLevel(" + strconv.Itoa(int(l)) + ")"
}

// TraceConfig configures record retention. The trace hash never depends on it.
type TraceConfig struct {
	Level  TraceLevel
	Buffer int // TraceFull ring capacity in records; 0 = unbounded; ignored for TraceHash
}

// Config configures a Sim. The zero value is valid: seed 0, no limits, seeded tie-break,
// hash-only trace.
type Config struct {
	Seed      uint64
	MaxEvents uint64 // stop once this many events have run and another is due; 0 = no limit
	MaxTime   Time   // never process an event later than this; 0 = no limit
	TieBreak  TieBreak
	Trace     TraceConfig
}

// validate panics with the KRN §7 message for the first invalid field (KRN-003).
func (cfg Config) validate() {
	if cfg.TieBreak != TieBreakSeeded && cfg.TieBreak != TieBreakFIFO {
		panic(fmt.Sprintf("kernel: invalid Config.TieBreak %d", cfg.TieBreak))
	}
	if cfg.Trace.Level != TraceHash && cfg.Trace.Level != TraceFull {
		panic(fmt.Sprintf("kernel: invalid Config.Trace.Level %d", cfg.Trace.Level))
	}
	if cfg.Trace.Buffer < 0 {
		panic(fmt.Sprintf("kernel: negative Config.Trace.Buffer %d", cfg.Trace.Buffer))
	}
	if cfg.MaxTime < 0 {
		panic(fmt.Sprintf("kernel: negative Config.MaxTime %s", cfg.MaxTime))
	}
}
