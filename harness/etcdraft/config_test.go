// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// AT-ETC-01
func TestDefaultConfig(t *testing.T) {
	want := Config{
		Nodes: 3, Clients: 3,
		TickInterval: 100 * time.Millisecond, ElectionTick: 10, HeartbeatTick: 1,
		PreVote: true, CheckQuorum: true, StepDownOnRemoval: true, DisableProposalForwarding: false,
		MaxSizePerMsg: 1 << 20, MaxInflightMsgs: 256, MaxUncommittedEntriesSize: 0,
		SyncLatencyMin: 200 * time.Microsecond, SyncLatencyMax: 2 * time.Millisecond, SyncOnlyMustSync: false,
		ReportUnreachable: true, SnapshotTimeout: 0, CrashRestartDelay: 500 * time.Millisecond,
		SnapshotEvery: 0, CompactKeep: 10,
		Membership: Membership{Enabled: false, Spares: 2, Every: 5 * time.Second},
		Workload: Workload{Keys: 8, ReadPPM: 500_000, ThinkMin: 10 * time.Millisecond, ThinkMax: 90 * time.Millisecond,
			AttemptTimeout: 500 * time.Millisecond, RetryDelay: 20 * time.Millisecond, OpTimeout: 5 * time.Second},
		Duration: 0, Quiet: 10 * time.Second, ProgressWithin: 5 * time.Second, Drain: 2 * time.Second,
		Faults: FaultsDefault, LogLevel: LogInfo, Bugs: 0,
	}
	got := DefaultConfig()
	if got.selfTestHook != nil || got.noConfSnapshot || got.earlySend {
		t.Fatal("DefaultConfig sets a test-only field")
	}
	if !configEqual(got, want) {
		t.Fatalf("DefaultConfig() =\n%+v\nwant\n%+v", got, want)
	}
	// ETC-011: every zero field with a default gets it; booleans, enums other than LogLevel, and
	// the membership fields (membership off) stay zero.
	zero := Config{
		Nodes: 3, Clients: 3,
		TickInterval: 100 * time.Millisecond, ElectionTick: 10, HeartbeatTick: 1,
		MaxSizePerMsg: 1 << 20, MaxInflightMsgs: 256,
		SnapshotTimeout: 2 * time.Second, CrashRestartDelay: 500 * time.Millisecond, CompactKeep: 10,
		Workload: Workload{Keys: 8, AttemptTimeout: 500 * time.Millisecond, RetryDelay: 20 * time.Millisecond, OpTimeout: 5 * time.Second},
		Duration: time.Minute, Quiet: 10 * time.Second, ProgressWithin: 5 * time.Second, Drain: 2 * time.Second,
		LogLevel: LogInfo,
	}
	if d := (Config{Duration: time.Minute}).withDefaults(); !configEqual(d, zero) {
		t.Fatalf("withDefaults of a zero config =\n%+v\nwant\n%+v", d, zero)
	}
	if d := (Config{SnapshotTimeout: 3 * time.Second}).withDefaults(); d.SnapshotTimeout != 3*time.Second {
		t.Fatalf("withDefaults changed a set SnapshotTimeout to %v", d.SnapshotTimeout)
	}
	m := Config{Membership: Membership{Enabled: true}}.withDefaults()
	if m.Membership.Spares != 2 || m.Membership.Every != 5*time.Second {
		t.Fatalf("membership defaults = %+v", m.Membership)
	}
}

// configEqual reports whether a and b are equal in every field but selfTestHook. It clears
// that func field in both copies first, because reflect.DeepEqual treats two funcs as equal
// only when both are nil.
func configEqual(a, b Config) bool {
	a.selfTestHook, b.selfTestHook = nil, nil
	return reflect.DeepEqual(a, b)
}

// AT-ETC-02: each row of ETC-012, alone and together with an error from a later row.
func TestValidateRows(t *testing.T) {
	type row struct {
		msg string
		bad func(c *Config)
	}
	rows := []row{
		{"Nodes must be 3 or 5, got 4", func(c *Config) { c.Nodes = 4 }},
		{"Clients must be in 1..16, got 17", func(c *Config) { c.Clients = 17 }},
		{"Workload.OpTimeout must not be negative", func(c *Config) { c.Workload.OpTimeout = -1 }},
		{"ElectionTick (2) must be greater than HeartbeatTick (2) >= 1", func(c *Config) { c.ElectionTick, c.HeartbeatTick = 2, 2 }},
		{"MaxInflightMsgs must be at least 1", func(c *Config) { c.MaxInflightMsgs = -1 }},
		{"SyncLatencyMin must not exceed SyncLatencyMax", func(c *Config) { c.SyncLatencyMin = 3 * time.Millisecond }},
		{"Membership.Spares must be in 1..4, got 5", func(c *Config) { c.Membership.Enabled, c.Membership.Spares = true, 5 }},
		{"Workload.Keys must be in 1..64, got 65", func(c *Config) { c.Workload.Keys = 65 }},
		{"Workload.ReadPPM must not exceed 1000000", func(c *Config) { c.Workload.ReadPPM = 1_000_001 }},
		{"Workload.ThinkMin must not exceed Workload.ThinkMax", func(c *Config) { c.Workload.ThinkMin = time.Second }},
		{"Workload.OpTimeout must exceed Workload.AttemptTimeout", func(c *Config) { c.Workload.OpTimeout = 500 * time.Millisecond }},
		{"Duration must be set (it must equal faultline.Options.Duration)", func(c *Config) { c.Duration = 0 }},
		{"Quiet must be at least ProgressWithin + Drain", func(c *Config) { c.Quiet = 6 * time.Second }},
		{"Duration must exceed Quiet", func(c *Config) { c.Duration = 10 * time.Second }},
		{"Faults has an undefined value 5", func(c *Config) { c.Faults = 5 }},
		{"LogLevel has an undefined value 6", func(c *Config) { c.LogLevel = 6 }},
		{"Bugs has an undefined value 32", func(c *Config) { c.Bugs = 32 }},
	}
	base := DefaultConfig()
	base.Duration = time.Minute
	if err := base.withDefaults().validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for i, r := range rows {
		c := base
		r.bad(&c)
		err := c.withDefaults().validate()
		want := "etcdraft: invalid config: " + r.msg
		if err == nil || err.Error() != want {
			t.Errorf("row %d alone: err = %v, want %q", i, err, want)
		}
		if i+1 < len(rows) {
			rows[i+1].bad(&c)
			if err := c.withDefaults().validate(); err == nil || err.Error() != want {
				t.Errorf("row %d with row %d: err = %v, want %q", i, i+1, err, want)
			}
		}
	}
	neg := base
	neg.TickInterval, neg.Drain = -1, -1
	if err := neg.withDefaults().validate(); err == nil || !strings.HasSuffix(err.Error(), "TickInterval must not be negative") {
		t.Errorf("two negative durations: err = %v, want the first in declaration order", err)
	}
	hb := base
	hb.HeartbeatTick = -1
	if err := hb.withDefaults().validate(); err == nil || err.Error() != "etcdraft: invalid config: ElectionTick (10) must be greater than HeartbeatTick (-1) >= 1" {
		t.Errorf("HeartbeatTick -1: err = %v", err)
	}
}

// ETC-012: each duration is checked for a negative value, in declaration order: each one alone,
// and together with the next.
func TestValidateNegativeDurations(t *testing.T) {
	durations := []struct {
		name string
		set  func(c *Config)
	}{
		{"TickInterval", func(c *Config) { c.TickInterval = -1 }},
		{"SyncLatencyMin", func(c *Config) { c.SyncLatencyMin = -1 }},
		{"SyncLatencyMax", func(c *Config) { c.SyncLatencyMax = -1 }},
		{"SnapshotTimeout", func(c *Config) { c.SnapshotTimeout = -1 }},
		{"CrashRestartDelay", func(c *Config) { c.CrashRestartDelay = -1 }},
		{"Membership.Every", func(c *Config) { c.Membership.Every = -1 }},
		{"Workload.ThinkMin", func(c *Config) { c.Workload.ThinkMin = -1 }},
		{"Workload.ThinkMax", func(c *Config) { c.Workload.ThinkMax = -1 }},
		{"Workload.AttemptTimeout", func(c *Config) { c.Workload.AttemptTimeout = -1 }},
		{"Workload.RetryDelay", func(c *Config) { c.Workload.RetryDelay = -1 }},
		{"Workload.OpTimeout", func(c *Config) { c.Workload.OpTimeout = -1 }},
		{"Duration", func(c *Config) { c.Duration = -1 }},
		{"Quiet", func(c *Config) { c.Quiet = -1 }},
		{"ProgressWithin", func(c *Config) { c.ProgressWithin = -1 }},
		{"Drain", func(c *Config) { c.Drain = -1 }},
	}
	base := DefaultConfig()
	base.Duration = time.Minute
	for i, d := range durations {
		want := "etcdraft: invalid config: " + d.name + " must not be negative"
		c := base
		d.set(&c)
		if err := c.withDefaults().validate(); err == nil || err.Error() != want {
			t.Errorf("%s alone: err = %v, want %q", d.name, err, want)
		}
		if i+1 < len(durations) {
			durations[i+1].set(&c)
			if err := c.withDefaults().validate(); err == nil || err.Error() != want {
				t.Errorf("%s with %s: err = %v, want %q", d.name, durations[i+1].name, err, want)
			}
		}
	}
}

// ETC-012: the valid edges. Every equal bound passes, every bug switch is defined, and Spares is
// not checked while membership is off.
func TestValidateEdges(t *testing.T) {
	base := DefaultConfig()
	base.Duration = time.Minute
	for _, c := range []struct {
		name string
		set  func(c *Config)
	}{
		{"SyncLatencyMin == SyncLatencyMax", func(c *Config) { c.SyncLatencyMin = c.SyncLatencyMax }},
		{"ReadPPM 1000000", func(c *Config) { c.Workload.ReadPPM = 1_000_000 }},
		{"ThinkMin == ThinkMax", func(c *Config) { c.Workload.ThinkMin = c.Workload.ThinkMax }},
		{"Quiet == ProgressWithin + Drain", func(c *Config) { c.Quiet = c.ProgressWithin + c.Drain }},
		{"every bug switch", func(c *Config) {
			c.Bugs = BugSendBeforePersist | BugSkipSync | BugApplyBeforeCommit | BugForgetHardState | BugSnapshotOffByOne
		}},
		{"Spares 9 with membership off", func(c *Config) { c.Membership = Membership{Spares: 9} }},
		{"zero config", func(c *Config) { *c = Config{Duration: time.Minute} }},
	} {
		cfg := base
		c.set(&cfg)
		if err := cfg.withDefaults().validate(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// AT-ETC-03
func TestValidateOptions(t *testing.T) {
	cfg := DefaultConfig()
	if _, _, err := validateOptions(cfg, faultline.Options{NoCryptoSeed: true}); err == nil ||
		err.Error() != "etcdraft: Options.NoCryptoSeed must be false: etcd/raft draws election timeouts from crypto/rand" {
		t.Errorf("NoCryptoSeed: err = %v", err)
	}
	if _, _, err := validateOptions(cfg, faultline.Options{Mode: faultline.ModeGoroutine}); err == nil ||
		err.Error() != "etcdraft: Options.Mode must be ModeEvent" {
		t.Errorf("ModeGoroutine: err = %v", err)
	}
	cfg.Duration = 30 * time.Second
	if _, _, err := validateOptions(cfg, faultline.Options{Duration: 20 * time.Second}); err == nil ||
		err.Error() != "etcdraft: Config.Duration (30s) differs from Options.Duration (20s)" {
		t.Errorf("mismatch: err = %v", err)
	}
	c, o, err := validateOptions(DefaultConfig(), faultline.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Duration != 60*time.Second || c.Duration != 60*time.Second || o.Net != NetConfig() || o.MaxEvents != 50_000_000 {
		t.Errorf("filled = cfg.Duration %v, opts %+v", c.Duration, o)
	}
	keep := simnet.Config{Default: simnet.Link{Latency: 7 * time.Millisecond}}
	disk := simdisk.Config{Crash: simdisk.CrashTorn}
	if _, o, _ := validateOptions(DefaultConfig(), faultline.Options{Net: keep, MaxEvents: 9, Disk: disk}); o.Net != keep || o.MaxEvents != 9 || o.Disk != disk {
		t.Errorf("non-zero Net/MaxEvents or Disk changed (ETC-142): %+v", o)
	}
	want := simnet.Config{Default: simnet.Link{Latency: time.Millisecond, Jitter: 2 * time.Millisecond, TailPPM: 10_000,
		Tail: 50 * time.Millisecond, DropPPM: 1_000, DupPPM: 1_000, FIFO: false}}
	if NetConfig() != want {
		t.Errorf("NetConfig() = %+v", NetConfig())
	}
}

// ETC-191
func TestSelfTestConfig(t *testing.T) {
	c := SelfTestConfig(BugSkipSync)
	d := DefaultConfig()
	d.Faults = FaultsSelfTest
	d.SyncLatencyMin, d.SyncLatencyMax = 5*time.Millisecond, 50*time.Millisecond
	d.Workload.ThinkMin, d.Workload.ThinkMax = 0, 20*time.Millisecond
	d.SnapshotEvery, d.CompactKeep = 50, 10
	d.Quiet, d.ProgressWithin, d.Drain = 6*time.Second, 4*time.Second, time.Second
	d.Bugs = BugSkipSync
	if !configEqual(c, d) {
		t.Fatalf("SelfTestConfig = %+v", c)
	}
	if got := (BugSendBeforePersist | BugSnapshotOffByOne).names(); got != "SendBeforePersist,SnapshotOffByOne" {
		t.Fatalf("names = %q", got)
	}
}
