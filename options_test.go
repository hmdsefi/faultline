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
	"github.com/hmdsefi/faultline/shims/netsim"
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
	// GOR-002: ModeGoroutine also defaults Procs, Drain, StallTimeout and NetSim.
	want.Mode, want.Procs, want.Drain, want.StallTimeout = ModeGoroutine, 1, DefaultDrain, DefaultStallTimeout
	want.NetSim = netsim.DefaultConfig()
	if d := optionsDiff(applyDefaults(Options{Mode: ModeGoroutine}), want); d != "" {
		t.Errorf("applyDefaults(Options{Mode: ModeGoroutine}): %s", d)
	}
	// Only zero fields change: set values, negative and largest ones included, reach validation
	// (API-001), and a Net or NetSim with any field set is kept whole.
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
			Procs: 2, Drain: 1, StallTimeout: 1, FailOnLeak: true, NetSim: netsim.Config{MSS: 32}, Swarm: true,
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
		{"DefaultDrain", DefaultDrain, 60 * time.Second},
		{"DefaultStallTimeout", DefaultStallTimeout, 60 * time.Second},
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

// API-003, API-005 (options), GOR-002
func TestValidateOptions(t *testing.T) {
	const roadmap = ", and see the roadmap at https://github.com/hmdsefi/faultline/issues/167"
	const (
		seedsNegative = "faultline: Options.Seeds is -1; want 0 (default 20) or more"
		seedsTooMany  = "faultline: Options.Seeds is 1000001; want at most 1000000"
		durationNs    = "faultline: Options.Duration is -1ns; want 0 (default 1m0s) or more"
		traceLevel    = "faultline: Options.Trace.Level is 7; want kernel.TraceHash or kernel.TraceFull"
		traceBuffer   = "faultline: Options.Trace.Buffer is -1; want 0 (unbounded) or more"
		modeUnknown   = "faultline: unknown Options.Mode 9; want ModeEvent (the zero value) or ModeGoroutine"
		procsNegative = "faultline: Options.Procs is negative: -1"
		drainNegative = "faultline: Options.Drain is negative: -1ns"
		netsimInvalid = "faultline: invalid Options.NetSim: invalid netsim config: MSS 32 out of range [64, 65535]"
		swarmSet      = "faultline: Options.Swarm is set, but swarm testing is not in this release yet; set it to false" + roadmap
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
		{"ModeGoroutine", Options{Mode: ModeGoroutine}, ""},
		{"unknown Mode", Options{Mode: 9}, modeUnknown},
		// API-003's order: Seeds, Duration, Trace.Level, Trace.Buffer, then Mode.
		{"order: Seeds first", Options{Seeds: -1, Duration: -1, Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, seedsNegative},
		{"order: Duration before Trace", Options{Duration: -1, Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, durationNs},
		{"order: Trace.Level before Trace.Buffer", Options{Trace: kernel.TraceConfig{Level: 7, Buffer: -1}, Mode: 9}, traceLevel},
		{"order: Trace.Buffer before an unknown Mode", Options{Trace: kernel.TraceConfig{Buffer: -1}, Mode: 9}, traceBuffer},
		{"order: Trace.Buffer before ModeGoroutine", Options{Trace: kernel.TraceConfig{Buffer: -1}, Mode: ModeGoroutine}, traceBuffer},
		// An unknown Mode comes before the goroutine-mode and later-phase options.
		{"order: unknown Mode before Procs", Options{Mode: 9, Procs: -1}, modeUnknown},
		{"order: ModeGoroutine before Procs", Options{Mode: ModeGoroutine, Procs: 2}, ""},
		// GOR-002: ModeEvent neither resolves nor validates the goroutine-mode fields.
		{"Procs", Options{Procs: 2}, ""},
		{"negative Procs", Options{Procs: -1}, ""},
		{"Drain", Options{Drain: time.Second}, ""},
		{"negative Drain", Options{Drain: -1}, ""},
		{"largest Drain", Options{Drain: math.MaxInt64}, ""},
		{"negative StallTimeout", Options{StallTimeout: -1}, ""},
		{"StallTimeout", Options{StallTimeout: time.Second}, ""},
		{"FailOnLeak", Options{FailOnLeak: true}, ""},
		{"invalid NetSim", Options{NetSim: netsim.Config{MSS: 32}}, ""},
		// GOR-002 in ModeGoroutine: negative Procs, then negative Drain, then an invalid NetSim; any
		// StallTimeout is valid.
		{"goroutine mode: Procs and Drain set", Options{Mode: ModeGoroutine, Procs: 3, Drain: math.MaxInt64, StallTimeout: time.Second, FailOnLeak: true}, ""},
		{"goroutine mode: negative StallTimeout", Options{Mode: ModeGoroutine, StallTimeout: -1}, ""},
		{"goroutine mode: negative Procs", Options{Mode: ModeGoroutine, Procs: -1}, procsNegative},
		{"goroutine mode: negative Drain", Options{Mode: ModeGoroutine, Drain: -1}, drainNegative},
		{"goroutine mode: order: Procs before Drain", Options{Mode: ModeGoroutine, Procs: -1, Drain: -1, Swarm: true}, procsNegative},
		{"goroutine mode: order: Drain before Swarm", Options{Mode: ModeGoroutine, Drain: -1, Swarm: true}, drainNegative},
		{"goroutine mode: invalid NetSim", Options{Mode: ModeGoroutine, NetSim: netsim.Config{MSS: 32}}, netsimInvalid},
		{"goroutine mode: order: Drain before NetSim", Options{Mode: ModeGoroutine, Drain: -1, NetSim: netsim.Config{MSS: 32}}, drainNegative},
		{"goroutine mode: order: NetSim before Swarm", Options{Mode: ModeGoroutine, NetSim: netsim.Config{MSS: 32}, Swarm: true}, netsimInvalid},
		// API-005: Swarm is the last option check.
		{"Swarm", Options{Swarm: true}, swarmSet},
		{"order: event-mode goroutine fields do not hide Swarm", Options{Procs: -1, Drain: -1, StallTimeout: 1, FailOnLeak: true, Swarm: true}, swarmSet},
		{"goroutine mode: valid fields before Swarm", Options{Mode: ModeGoroutine, Procs: 1, Drain: 1, StallTimeout: -1, FailOnLeak: true, Swarm: true}, swarmSet},
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
