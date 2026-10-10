// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"reflect"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Mode selects how code under test runs.
type Mode uint8

const (
	// ModeEvent runs event-style code on the kernel's event loop. Determinism level 1 (exact).
	ModeEvent Mode = iota
	// ModeGoroutine runs processes inside a testing/synctest bubble (Phase 2, spec GOR).
	// In Phase 1, Run rejects it with a setup error.
	ModeGoroutine
)

// String returns "event", "goroutine", or "Mode(N)" for other values.
func (m Mode) String() string {
	switch m {
	case ModeEvent:
		return "event"
	case ModeGoroutine:
		return "goroutine"
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

// Defaults that Run applies to zero-valued Options fields.
const (
	DefaultSeeds     = 20               // Options.Seeds == 0
	DefaultDuration  = 60 * time.Second // Options.Duration == 0
	DefaultMaxEvents = 10_000_000       // Options.MaxEvents == 0
	// ShortSeeds caps the seed count under `go test -short` unless FAULTLINE_SEEDS is set.
	ShortSeeds = 5
	// MaxSeeds is the largest value FAULTLINE_SEEDS and Options.Seeds accept.
	MaxSeeds = 1_000_000
)

// Options configures Run. The zero value is valid: 20 seeds of 60 s virtual time each,
// default network, default disks, hash-only tracing.
type Options struct {
	// Seeds is the number of seeds to run. 0 means DefaultSeeds. Negative or more than
	// MaxSeeds is a setup error.
	// FAULTLINE_SEEDS overrides it; FAULTLINE_SEED and FAULTLINE_SEED_LIST replace the seed list.
	Seeds int

	// BaseSeed is the base from which the seed list is derived. 0 means FNV-1a 64 of t.Name().
	// FAULTLINE_BASE_SEED and FAULTLINE_EXPLORE override it.
	BaseSeed uint64

	// Duration is the virtual length of each run. 0 means DefaultDuration. Negative is a setup error.
	Duration time.Duration

	// MaxEvents stops a run that executes more events (kind "limit" unless AllowLimit).
	// 0 means DefaultMaxEvents. math.MaxUint64 means no practical limit.
	MaxEvents uint64

	// Mode is ModeEvent (default). ModeGoroutine is a Phase 1 setup error.
	Mode Mode

	// Net configures the simulated network. The zero value means simnet.DefaultConfig().
	// (A zero-latency network needs a non-zero field, for example Default.Latency = 1.)
	Net simnet.Config

	// Disk configures simulated volumes. Passed to simdisk.New unchanged; the zero value
	// selects DSK's defaults.
	Disk simdisk.Config

	// Trace configures the primary attempt of every seed. The zero value is TraceHash.
	// FAULTLINE_TRACE=full upgrades it to TraceFull and writes artifacts for passing seeds.
	Trace kernel.TraceConfig

	// CheckDeterminism runs every passing seed a second time and compares trace hashes.
	// FAULTLINE_CHECK_DETERMINISM can enable it but not disable it.
	CheckDeterminism bool

	// KeepGoing runs all seeds even after one fails. Without it, Run stops starting new
	// seeds after the first failing seed. Forced on by FAULTLINE_SEED_LIST.
	KeepGoing bool

	// NoCryptoSeed disables cryptotest.SetGlobalRandom. crypto/rand in code under test is then
	// not deterministic, and in ModeEvent the parent test may be parallel (API-104).
	NoCryptoSeed bool

	// AllowLimit makes hitting MaxEvents (or the kernel's MaxTime) a normal end of the run
	// instead of a failure. Final checks are skipped for such a run.
	AllowLimit bool

	// The fields below belong to later phases. Their behavior and validation are the owning
	// spec's. Until that phase ships, a non-zero value is a setup error (API-005).
	// Options.NetSim (GOR and NSM) is declared in Phase 2 together with package shims/netsim.

	// Procs is GOMAXPROCS during goroutine-mode attempts. 0 means 1. (GOR, Phase 2)
	Procs int

	// Drain is the virtual time processes get after the run ends to exit on their own.
	// 0 means DefaultDrain (60 s). (GOR, Phase 2)
	Drain time.Duration

	// StallTimeout is the wall-clock time without driver progress after which the stall
	// watchdog ends the test binary with a report. 0 means DefaultStallTimeout (60 s);
	// negative disables the watchdog. (GOR, Phase 2)
	StallTimeout time.Duration

	// FailOnLeak makes goroutines still alive after the drain a failure of kind "leak".
	// (GOR, Phase 2)
	FailOnLeak bool

	// Swarm enables per-seed swarm configs. FAULTLINE_SWARM overrides it. (EXP, Phase 3)
	Swarm bool
}

// applyDefaults replaces zero fields by their defaults (API-002). Seeds and BaseSeed are
// resolved together with the environment (API-014, API-015).
func applyDefaults(o Options) Options {
	if o.Duration == 0 {
		o.Duration = DefaultDuration
	}
	if o.MaxEvents == 0 {
		o.MaxEvents = DefaultMaxEvents
	}
	if reflect.ValueOf(o.Net).IsZero() {
		o.Net = simnet.DefaultConfig()
	}
	return o
}

// validateOptions implements API-003 and the option part of API-005, in that order.
func validateOptions(o Options) error {
	switch {
	case o.Seeds < 0:
		return fmt.Errorf("faultline: Options.Seeds is %d; want 0 (default %d) or more", o.Seeds, DefaultSeeds)
	case o.Seeds > MaxSeeds:
		return fmt.Errorf("faultline: Options.Seeds is %d; want at most %d", o.Seeds, MaxSeeds)
	case o.Duration < 0:
		return fmt.Errorf("faultline: Options.Duration is %v; want 0 (default %v) or more", o.Duration, DefaultDuration)
	case o.Trace.Level != kernel.TraceHash && o.Trace.Level != kernel.TraceFull:
		return fmt.Errorf("faultline: Options.Trace.Level is %d; want kernel.TraceHash or kernel.TraceFull", o.Trace.Level)
	case o.Trace.Buffer < 0:
		return fmt.Errorf("faultline: Options.Trace.Buffer is %d; want 0 (unbounded) or more", o.Trace.Buffer)
	case o.Mode == ModeGoroutine:
		return fmt.Errorf("faultline: Options.Mode is ModeGoroutine, but goroutine mode is not in this release yet; set it to ModeEvent, and see the roadmap at %s", roadmapURL)
	case o.Mode != ModeEvent:
		return fmt.Errorf("faultline: unknown Options.Mode %d; want ModeEvent (the zero value)", o.Mode)
	}
	later := []struct {
		name    string
		set     bool
		feature string // the later feature the option belongs to, in the words of the roadmap
		zero    string // the value to use until the feature ships
	}{
		{"Options.Procs", o.Procs != 0, featureGoroutine, "0"},
		{"Options.Drain", o.Drain != 0, featureGoroutine, "0"},
		{"Options.StallTimeout", o.StallTimeout != 0, featureGoroutine, "0"},
		{"Options.FailOnLeak", o.FailOnLeak, featureGoroutine, "false"},
		{"Options.Swarm", o.Swarm, featureSwarm, "false"},
	}
	for _, l := range later {
		if l.set {
			return notInRelease(l.name, l.feature, "set it to "+l.zero)
		}
	}
	return nil
}

// roadmapURL is the public roadmap that the messages for later features point to (API-005).
const roadmapURL = "https://github.com/hmdsefi/faultline/issues/167"

// The later features of API-005's table, as its messages name them.
const (
	featureGoroutine = "goroutine mode"
	featureExact     = "exact replay in goroutine mode"
	featureSwarm     = "swarm testing"
)

// notInRelease is API-005's setup error for an option or variable of a later feature: name is
// set, feature is not in this release, and fix says what to do instead.
func notInRelease(name, feature, fix string) error {
	return fmt.Errorf("faultline: %s is set, but %s is not in this release yet; %s, and see the roadmap at %s", name, feature, fix, roadmapURL)
}
