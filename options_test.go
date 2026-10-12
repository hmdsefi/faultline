// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// optionsDiff returns "<field>: got <value>, want <value>" for each field of got that differs from
// want, joined by "; ", so a failing row names the fields instead of printing two whole Options.
func optionsDiff(got, want Options) string {
	var diffs []string
	g, w := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := range g.NumField() {
		if gf, wf := g.Field(i).Interface(), w.Field(i).Interface(); !reflect.DeepEqual(gf, wf) {
			diffs = append(diffs, fmt.Sprintf("%s: got %+v, want %+v", g.Type().Field(i).Name, gf, wf))
		}
	}
	return strings.Join(diffs, "; ")
}

// API-002
func TestApplyDefaults(t *testing.T) {
	want := Options{Duration: DefaultDuration, MaxEvents: DefaultMaxEvents, Net: simnet.DefaultConfig()}
	if d := optionsDiff(applyDefaults(Options{}), want); d != "" {
		t.Errorf("applyDefaults(Options{}): %s", d)
	}
	// Only zero fields change: set values, negative and largest ones included, reach validation
	// (API-001), and a Net with any field set is kept whole.
	for _, c := range []struct {
		name string
		in   Options
	}{
		{"small values, Net with Latency", Options{Duration: time.Second, MaxEvents: 7, Net: simnet.Config{Default: simnet.Link{Latency: 1}}}},
		{"1 ns, Net with Jitter", Options{Duration: 1, MaxEvents: 1, Net: simnet.Config{Default: simnet.Link{Jitter: 1}}}},
		{"largest values, Net with a slow link", Options{Duration: math.MaxInt64, MaxEvents: math.MaxUint64, Net: simnet.Config{Default: simnet.Link{Latency: 2 * time.Second}}}},
		{"every field set", Options{
			Seeds: 3, BaseSeed: 9, Duration: -1, MaxEvents: 1, Mode: ModeGoroutine,
			Net:              simnet.Config{NoDeliveryCheck: true},
			Disk:             simdisk.Config{SectorSize: 512},
			Trace:            kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 4},
			CheckDeterminism: true, KeepGoing: true, NoCryptoSeed: true, AllowLimit: true,
			Procs: 2, Drain: 1, StallTimeout: 1, FailOnLeak: true, Swarm: true,
		}},
	} {
		if d := optionsDiff(applyDefaults(c.in), c.in); d != "" {
			t.Errorf("%s: applyDefaults changed %s", c.name, d)
		}
	}
}

// API §4: the defaults and limits are part of the API.
func TestDefaultConstants(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"DefaultSeeds", DefaultSeeds, 20},
		{"DefaultDuration", DefaultDuration, 60 * time.Second},
		{"DefaultMaxEvents", DefaultMaxEvents, 10_000_000},
		{"ShortSeeds", ShortSeeds, 5},
		{"MaxSeeds", MaxSeeds, 1_000_000},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// Mode.String (API §4).
func TestModeString(t *testing.T) {
	for _, c := range []struct {
		m    Mode
		want string
	}{
		{ModeEvent, "event"},
		{ModeGoroutine, "goroutine"},
		{Mode(5), "Mode(5)"},
		{Mode(255), "Mode(255)"},
	} {
		if got := c.m.String(); got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", uint8(c.m), got, c.want)
		}
	}
}

// API-003, API-005 (options), API-091
func TestValidateOptions(t *testing.T) {
	const roadmap = ", and see the roadmap at https://github.com/hmdsefi/faultline/issues/167"
	const (
		seedsNegative   = "faultline: Options.Seeds is -1; want 0 (default 20) or more"
		seedsTooMany    = "faultline: Options.Seeds is 1000001; want at most 1000000"
		durationNs      = "faultline: Options.Duration is -1ns; want 0 (default 1m0s) or more"
		traceLevel      = "faultline: Options.Trace.Level is 7; want kernel.TraceHash or kernel.TraceFull"
		traceBuffer     = "faultline: Options.Trace.Buffer is -1; want 0 (unbounded) or more"
		modeGoroutine   = "faultline: Options.Mode is ModeGoroutine, but goroutine mode is not in this release yet; set it to ModeEvent" + roadmap
		modeUnknown     = "faultline: unknown Options.Mode 9; want ModeEvent (the zero value)"
		procsSet        = "faultline: Options.Procs is set, but goroutine mode is not in this release yet; set it to 0" + roadmap
		drainSet        = "faultline: Options.Drain is set, but goroutine mode is not in this release yet; set it to 0" + roadmap
		stallTimeoutSet = "faultline: Options.StallTimeout is set, but goroutine mode is not in this release yet; set it to 0" + roadmap
		failOnLeakSet   = "faultline: Options.FailOnLeak is set, but goroutine mode is not in this release yet; set it to false" + roadmap
		swarmSet        = "faultline: Options.Swarm is set, but swarm testing is not in this release yet; set it to false" + roadmap
	)
	cases := []struct {
		name string
		o    Options
		want string
	}{
		{"zero value", Options{}, ""},
		{"smallest set values", Options{Seeds: 1, Duration: 1, MaxEvents: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 1}}, ""},
		// No upper bound but MaxSeeds: none for Duration, MaxEvents or Trace.Buffer.
		{"largest values", Options{Seeds: MaxSeeds, Duration: math.MaxInt64, MaxEvents: math.MaxUint64, Trace: kernel.TraceConfig{Level: kernel.TraceFull, Buffer: math.MaxInt}}, ""},
		{"negative Seeds", Options{Seeds: -1}, seedsNegative},
		{"Seeds over MaxSeeds", Options{Seeds: MaxSeeds + 1}, seedsTooMany},
		{"Seeds over MaxSeeds before Duration", Options{Seeds: MaxSeeds + 1, Duration: -1}, seedsTooMany},
		{"negative Duration in seconds", Options{Duration: -time.Second}, "faultline: Options.Duration is -1s; want 0 (default 1m0s) or more"},
		{"negative Duration in ns", Options{Duration: -1}, durationNs},
		{"unknown Trace.Level", Options{Trace: kernel.TraceConfig{Level: 7}}, traceLevel},
		{"negative Trace.Buffer", Options{Trace: kernel.TraceConfig{Buffer: -1}}, traceBuffer},
		{"ModeGoroutine", Options{Mode: ModeGoroutine}, modeGoroutine},
		{"unknown Mode", Options{Mode: 9}, modeUnknown},
		// API-003's order: Seeds, Duration, Trace.Level, Trace.Buffer, then Mode.
		{"order: Seeds first", Options{Seeds: -1, Duration: -1, Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, seedsNegative},
		{"order: Duration before Trace", Options{Duration: -1, Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, durationNs},
		{"order: Trace.Level before Trace.Buffer", Options{Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, traceLevel},
		{"order: Trace.Buffer before an unknown Mode", Options{Trace: kernel.TraceConfig{Buffer: -1}, Mode: 9}, traceBuffer},
		{"order: Trace.Buffer before ModeGoroutine", Options{Trace: kernel.TraceConfig{Buffer: -1}, Mode: ModeGoroutine}, traceBuffer},
		// An unknown Mode and ModeGoroutine come before the later-phase options.
		{"order: unknown Mode before Procs", Options{Mode: 9, Procs: 1}, modeUnknown},
		{"order: ModeGoroutine before Procs", Options{Mode: ModeGoroutine, Procs: 2}, modeGoroutine},
		// API-005: every non-zero value, negative and largest ones included.
		{"Procs", Options{Procs: 2}, procsSet},
		{"negative Procs", Options{Procs: -1}, procsSet},
		{"Drain", Options{Drain: time.Second}, drainSet},
		{"negative Drain", Options{Drain: -1}, drainSet},
		{"largest Drain", Options{Drain: math.MaxInt64}, drainSet},
		{"negative StallTimeout", Options{StallTimeout: -1}, stallTimeoutSet},
		{"StallTimeout", Options{StallTimeout: time.Second}, stallTimeoutSet},
		{"FailOnLeak", Options{FailOnLeak: true}, failOnLeakSet},
		{"Swarm", Options{Swarm: true}, swarmSet},
		// API-005's table order: Procs, Drain, StallTimeout, FailOnLeak, Swarm.
		{"order: Procs first", Options{Procs: 1, Drain: 1, StallTimeout: 1, FailOnLeak: true, Swarm: true}, procsSet},
		{"order: Drain before StallTimeout", Options{Drain: 1, StallTimeout: 1, FailOnLeak: true, Swarm: true}, drainSet},
		{"order: StallTimeout before FailOnLeak", Options{StallTimeout: 1, FailOnLeak: true, Swarm: true}, stallTimeoutSet},
		{"order: FailOnLeak before Swarm", Options{FailOnLeak: true, Swarm: true}, failOnLeakSet},
	}
	for _, c := range cases {
		got := ""
		if err := validateOptions(c.o); err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
