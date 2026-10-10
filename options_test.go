// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// API-002
func TestApplyDefaults(t *testing.T) {
	if o, want := applyDefaults(Options{}), (Options{Duration: DefaultDuration, MaxEvents: DefaultMaxEvents, Net: simnet.DefaultConfig()}); o != want {
		t.Fatalf("applyDefaults(Options{}) = %+v, want %+v", o, want)
	}
	// Only zero fields change: set values, negative ones included, reach validation (API-001),
	// and a Net with any field set is kept whole.
	for _, in := range []Options{
		{Duration: time.Second, MaxEvents: 7, Net: simnet.Config{Default: simnet.Link{Latency: 1}}},
		{Duration: 1, MaxEvents: 1, Net: simnet.Config{Default: simnet.Link{Jitter: 1}}},
		{
			Seeds: 3, BaseSeed: 9, Duration: -1, MaxEvents: 1, Mode: ModeGoroutine,
			Net:              simnet.Config{NoDeliveryCheck: true},
			Disk:             simdisk.Config{SectorSize: 512},
			Trace:            kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 4},
			CheckDeterminism: true, KeepGoing: true, NoCryptoSeed: true, AllowLimit: true,
			Procs: 2, Drain: 1, StallTimeout: 1, FailOnLeak: true, Swarm: true,
		},
	} {
		if got := applyDefaults(in); got != in {
			t.Errorf("applyDefaults(%+v) = %+v; want it unchanged", in, got)
		}
	}
	if DefaultSeeds != 20 || DefaultDuration != 60*time.Second || DefaultMaxEvents != 10_000_000 || ShortSeeds != 5 || MaxSeeds != 1_000_000 {
		t.Fatal("default constants changed")
	}
}

// API-003, API-005 (options), API-091
func TestValidateOptions(t *testing.T) {
	cases := []struct {
		o    Options
		want string
	}{
		{Options{}, ""},
		{Options{Seeds: 1, Duration: 1, MaxEvents: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 1}}, ""},
		{Options{Seeds: -1}, "faultline: Options.Seeds is -1; want 0 (default 20) or more"},
		{Options{Seeds: MaxSeeds}, ""},
		{Options{Seeds: MaxSeeds + 1}, "faultline: Options.Seeds is 1000001; want at most 1000000"},
		{Options{Seeds: MaxSeeds + 1, Duration: -1}, "faultline: Options.Seeds is 1000001; want at most 1000000"},
		{Options{Duration: -time.Second}, "faultline: Options.Duration is -1s; want 0 (default 1m0s) or more"},
		{Options{Duration: -1}, "faultline: Options.Duration is -1ns; want 0 (default 1m0s) or more"},
		{Options{Trace: kernel.TraceConfig{Level: 7}}, "faultline: Options.Trace.Level is 7; want kernel.TraceHash or kernel.TraceFull"},
		{Options{Trace: kernel.TraceConfig{Buffer: -1}}, "faultline: Options.Trace.Buffer is -1; want 0 (unbounded) or more"},
		{Options{Mode: ModeGoroutine}, "faultline: Options.Mode is ModeGoroutine, which this version of faultline does not support (goroutine mode arrives in Phase 2)"},
		{Options{Mode: 9}, "faultline: unknown Options.Mode 9"},
		// API-003's order: Seeds, Duration, Trace.Level, Trace.Buffer, then Mode.
		{Options{Seeds: -1, Duration: -1, Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, "faultline: Options.Seeds is -1; want 0 (default 20) or more"},
		{Options{Duration: -1, Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, "faultline: Options.Duration is -1ns; want 0 (default 1m0s) or more"},
		{Options{Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, "faultline: Options.Trace.Level is 7; want kernel.TraceHash or kernel.TraceFull"},
		{Options{Trace: kernel.TraceConfig{Buffer: -1}, Mode: 9}, "faultline: Options.Trace.Buffer is -1; want 0 (unbounded) or more"},
		{Options{Trace: kernel.TraceConfig{Buffer: -1}, Mode: ModeGoroutine}, "faultline: Options.Trace.Buffer is -1; want 0 (unbounded) or more"},
		{Options{Mode: 9, Procs: 1}, "faultline: unknown Options.Mode 9"},
		// API-005: every non-zero value, negative ones included.
		{Options{Procs: 2}, "faultline: Options.Procs is not available until Phase 2"},
		{Options{Procs: -1}, "faultline: Options.Procs is not available until Phase 2"},
		{Options{Drain: time.Second}, "faultline: Options.Drain is not available until Phase 2"},
		{Options{Drain: -1}, "faultline: Options.Drain is not available until Phase 2"},
		{Options{StallTimeout: -1}, "faultline: Options.StallTimeout is not available until Phase 2"},
		{Options{StallTimeout: time.Second}, "faultline: Options.StallTimeout is not available until Phase 2"},
		{Options{FailOnLeak: true}, "faultline: Options.FailOnLeak is not available until Phase 2"},
		{Options{Swarm: true}, "faultline: Options.Swarm is not available until Phase 3"},
		// API-005's table order: Procs, Drain, StallTimeout, FailOnLeak, Swarm.
		{Options{Procs: 1, Drain: 1, StallTimeout: 1, FailOnLeak: true, Swarm: true}, "faultline: Options.Procs is not available until Phase 2"},
		{Options{Drain: 1, StallTimeout: 1, FailOnLeak: true, Swarm: true}, "faultline: Options.Drain is not available until Phase 2"},
		{Options{StallTimeout: 1, FailOnLeak: true, Swarm: true}, "faultline: Options.StallTimeout is not available until Phase 2"},
		{Options{FailOnLeak: true, Swarm: true}, "faultline: Options.FailOnLeak is not available until Phase 2"},
		{Options{Procs: 2, Swarm: true}, "faultline: Options.Procs is not available until Phase 2"},
		{Options{Mode: ModeGoroutine, Procs: 2}, "faultline: Options.Mode is ModeGoroutine, which this version of faultline does not support (goroutine mode arrives in Phase 2)"},
	}
	for _, c := range cases {
		err := validateOptions(c.o)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("validateOptions(%+v) = %q, want %q", c.o, got, c.want)
		}
	}
	if ModeEvent.String() != "event" || ModeGoroutine.String() != "goroutine" || Mode(5).String() != "Mode(5)" {
		t.Error("Mode.String")
	}
}
