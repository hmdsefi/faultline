package etcdraft

import (
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
	d := Config{Duration: time.Minute}.withDefaults()
	if d.SnapshotTimeout != 2*time.Second || d.Membership.Spares != 0 || d.LogLevel != LogInfo || d.Nodes != 3 || d.Workload.OpTimeout != 5*time.Second {
		t.Fatalf("withDefaults of a zero config = %+v", d)
	}
	m := Config{Membership: Membership{Enabled: true}}.withDefaults()
	if m.Membership.Spares != 2 || m.Membership.Every != 5*time.Second {
		t.Fatalf("membership defaults = %+v", m.Membership)
	}
}

// configEqual compares the exported fields of two configs.
func configEqual(a, b Config) bool {
	a.selfTestHook, b.selfTestHook = nil, nil
	return a.Nodes == b.Nodes && a.Clients == b.Clients && a.TickInterval == b.TickInterval &&
		a.ElectionTick == b.ElectionTick && a.HeartbeatTick == b.HeartbeatTick && a.PreVote == b.PreVote &&
		a.CheckQuorum == b.CheckQuorum && a.StepDownOnRemoval == b.StepDownOnRemoval &&
		a.DisableProposalForwarding == b.DisableProposalForwarding && a.MaxSizePerMsg == b.MaxSizePerMsg &&
		a.MaxInflightMsgs == b.MaxInflightMsgs && a.MaxUncommittedEntriesSize == b.MaxUncommittedEntriesSize &&
		a.SyncLatencyMin == b.SyncLatencyMin && a.SyncLatencyMax == b.SyncLatencyMax &&
		a.SyncOnlyMustSync == b.SyncOnlyMustSync && a.ReportUnreachable == b.ReportUnreachable &&
		a.SnapshotTimeout == b.SnapshotTimeout && a.CrashRestartDelay == b.CrashRestartDelay &&
		a.SnapshotEvery == b.SnapshotEvery && a.CompactKeep == b.CompactKeep && a.Membership == b.Membership &&
		a.Workload == b.Workload && a.Duration == b.Duration && a.Quiet == b.Quiet &&
		a.ProgressWithin == b.ProgressWithin && a.Drain == b.Drain && a.Faults == b.Faults &&
		a.LogLevel == b.LogLevel && a.Bugs == b.Bugs && a.noConfSnapshot == b.noConfSnapshot && a.earlySend == b.earlySend
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
