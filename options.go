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
	"github.com/hmdsefi/faultline/shims/netsim"
)

// Mode selects how code under test runs.
type Mode uint8

const (
	// ModeEvent runs code written as kernel callbacks (boot functions, timers, network handlers)
	// on the kernel's event loop. A run is exactly reproducible from its seed.
	ModeEvent Mode = iota
	// ModeGoroutine runs goroutine code as processes inside a testing/synctest bubble, one bubble
	// per run. A run is reproducible from its seed only on a best-effort basis, and report.json
	// says so with determinism_level "best-effort".
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

// Defaults for the goroutine-mode fields of Options.
const (
	DefaultDrain        = 60 * time.Second // Options.Drain == 0
	DefaultStallTimeout = 60 * time.Second // Options.StallTimeout == 0
)

// Options configures Run and RunSeeds. The zero value is valid: 20 seeds of 60 s virtual time
// each, default network, default disks, hash-only tracing.
type Options struct {
	// Seeds is the number of seeds to run. 0 means DefaultSeeds. Negative or more than
	// MaxSeeds is a setup error.
	// FAULTLINE_SEEDS overrides it; FAULTLINE_SEED and FAULTLINE_SEED_LIST replace the seed list.
	// RunSeeds ignores it.
	Seeds int

	// BaseSeed is the base from which the seed list is derived. 0 means FNV-1a 64 of t.Name().
	// FAULTLINE_BASE_SEED and FAULTLINE_EXPLORE override it. RunSeeds ignores it.
	BaseSeed uint64

	// Duration is the virtual length of each run. 0 means DefaultDuration. Negative is a setup error.
	Duration time.Duration

	// MaxEvents stops a run that executes more events (kind "limit" unless AllowLimit).
	// 0 means DefaultMaxEvents. math.MaxUint64 means no practical limit.
	MaxEvents uint64

	// Mode selects how code under test runs: ModeEvent, the zero value, or ModeGoroutine.
	// Procs, Drain, StallTimeout, FailOnLeak and NetSim apply to ModeGoroutine only; ModeEvent
	// ignores them.
	Mode Mode

	// Net configures the simulated network. The zero value means simnet.DefaultConfig(), so a
	// network with no latency at all cannot be configured here. The closest is a config with one
	// non-zero field, for example Default.Latency = 1, which gives every message 1 ns of latency.
	Net simnet.Config

	// Disk configures the simulated volumes and is passed to simdisk.New unchanged. The zero
	// value behaves like simdisk.DefaultConfig().
	Disk simdisk.Config

	// Trace configures the first run of every seed. The zero value is TraceHash.
	// FAULTLINE_TRACE=full upgrades it to TraceFull and writes artifacts for passing seeds.
	Trace kernel.TraceConfig

	// CheckDeterminism runs every passing seed a second time and compares trace hashes.
	// FAULTLINE_CHECK_DETERMINISM can enable it but not disable it.
	CheckDeterminism bool

	// KeepGoing runs all seeds even after one fails. Without it, Run stops starting new
	// seeds after the first failing seed. Forced on by FAULTLINE_SEED_LIST and by RunSeeds.
	KeepGoing bool

	// NoCryptoSeed stops Run from seeding crypto/rand with cryptotest.SetGlobalRandom. crypto/rand
	// in code under test is then not deterministic. In exchange, a ModeEvent test may call
	// t.Parallel, which cryptotest.SetGlobalRandom forbids. A ModeGoroutine test must not call
	// t.Parallel, with or without NoCryptoSeed (see Run).
	NoCryptoSeed bool

	// AllowLimit makes hitting MaxEvents (or the kernel's MaxTime) a normal end of the run
	// instead of a failure. Final checks are skipped for such a run.
	AllowLimit bool

	// Procs is GOMAXPROCS during each goroutine-mode run. 0 means 1. Negative is a setup error.
	Procs int

	// Drain is the virtual time processes get after the run ends (contexts cancelled, shims
	// closed) to exit on their own. 0 means DefaultDrain. Negative is a setup error.
	Drain time.Duration

	// StallTimeout is the wall-clock time without driver progress after which the stall
	// watchdog reports and terminates the test binary. 0 means DefaultStallTimeout.
	// Negative disables the watchdog.
	StallTimeout time.Duration

	// FailOnLeak makes goroutines still alive after the drain a failure of kind "leak".
	// Without it, Run logs the leak report and the seed's outcome does not change.
	FailOnLeak bool

	// NetSim configures shims/netsim. The zero value means netsim.DefaultConfig().
	NetSim netsim.Config

	// Swarm is reserved for swarm testing, where each seed also picks which fault kinds are on.
	// Run rejects true with a setup error.
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
	if o.Mode == ModeGoroutine { // GOR-002
		if o.Procs == 0 {
			o.Procs = 1
		}
		if o.Drain == 0 {
			o.Drain = DefaultDrain
		}
		if o.StallTimeout == 0 {
			o.StallTimeout = DefaultStallTimeout
		}
		if o.NetSim == (netsim.Config{}) {
			o.NetSim = netsim.DefaultConfig()
		}
	}
	return o
}

// validateOptions implements API-003, GOR-002's checks and the option part of API-005, in that
// order. ModeEvent ignores the goroutine-mode fields (GOR-002).
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
	case o.Mode != ModeEvent && o.Mode != ModeGoroutine:
		return fmt.Errorf("faultline: unknown Options.Mode %d; want ModeEvent (the zero value) or ModeGoroutine", o.Mode)
	}
	if o.Mode == ModeGoroutine {
		if o.Procs < 0 {
			return fmt.Errorf("faultline: Options.Procs is negative: %d", o.Procs)
		}
		if o.Drain < 0 {
			return fmt.Errorf("faultline: Options.Drain is negative: %v", o.Drain)
		}
		if err := o.NetSim.Validate(); err != nil {
			return fmt.Errorf("faultline: invalid Options.NetSim: %v", err)
		}
	}
	later := []struct {
		name    string
		set     bool
		feature string // the later feature the option belongs to, in the words of the roadmap
		zero    string // the value to use until the feature ships
	}{
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
	featureExact = "exact replay in goroutine mode"
	featureSwarm = "swarm testing"
)

// notInRelease is API-005's setup error for an option or variable of a later feature: name is
// set, feature is not in this release, and fix says what to do instead.
func notInRelease(name, feature, fix string) error {
	return fmt.Errorf("faultline: %s is set, but %s is not in this release yet; %s, and see the roadmap at %s", name, feature, fix, roadmapURL)
}
