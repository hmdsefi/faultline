package etcdraft

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Config configures a simulated etcd/raft cluster. Start from DefaultConfig.
// A zero numeric or duration field takes the default named in its comment;
// booleans and enums are used as given.
type Config struct {
	// Nodes is the number of initial voters n1..nN (raft IDs 1..N). 3 or 5. Default 3.
	Nodes int
	// Clients is the number of workload clients c1..cK. 1..16. Default 3.
	Clients int

	// TickInterval is the local-clock time between RawNode.Tick calls. Default 100ms.
	TickInterval time.Duration
	// ElectionTick is raft.Config.ElectionTick. Default 10.
	ElectionTick int
	// HeartbeatTick is raft.Config.HeartbeatTick. Default 1.
	HeartbeatTick int
	// PreVote is raft.Config.PreVote.
	PreVote bool
	// CheckQuorum is raft.Config.CheckQuorum.
	CheckQuorum bool
	// StepDownOnRemoval is raft.Config.StepDownOnRemoval.
	StepDownOnRemoval bool
	// DisableProposalForwarding is raft.Config.DisableProposalForwarding.
	DisableProposalForwarding bool
	// MaxSizePerMsg is raft.Config.MaxSizePerMsg. Default 1 MiB.
	MaxSizePerMsg uint64
	// MaxInflightMsgs is raft.Config.MaxInflightMsgs. Default 256.
	MaxInflightMsgs int
	// MaxUncommittedEntriesSize is raft.Config.MaxUncommittedEntriesSize; 0 means no limit.
	MaxUncommittedEntriesSize uint64

	// SyncLatencyMin and SyncLatencyMax bound the local-clock duration of a WAL sync,
	// drawn uniformly per synced Ready. If both are zero, a Ready is persisted and
	// finished in a single event.
	SyncLatencyMin time.Duration
	SyncLatencyMax time.Duration
	// SyncOnlyMustSync skips File.Sync for a Ready with MustSync == false and no
	// snapshot, as etcd's server does. When false, every Ready that writes WAL
	// records is synced.
	SyncOnlyMustSync bool
	// ReportUnreachable makes a server call RawNode.ReportUnreachable for each peer it
	// sent a message to while the link was cut or the peer was down.
	ReportUnreachable bool
	// SnapshotTimeout is how long a sender waits for a snapshot receipt before calling
	// ReportSnapshot(SnapshotFailure). Default 2 × ElectionTick × TickInterval.
	SnapshotTimeout time.Duration
	// CrashRestartDelay is how long after a server crashes itself on a WAL error the
	// harness restarts it. Default 500ms.
	CrashRestartDelay time.Duration

	// SnapshotEvery enables stage 1b: a server creates a local snapshot when its applied
	// index is at least SnapshotEvery past its latest snapshot, then compacts
	// MemoryStorage. 0 disables periodic snapshots.
	SnapshotEvery uint64
	// CompactKeep is how many entries below a new local snapshot's index stay in
	// MemoryStorage. Default 10.
	CompactKeep uint64

	// Membership enables and configures stage 1c.
	Membership Membership
	// Workload configures the clients.
	Workload Workload

	// Duration is the virtual run length and must equal faultline.Options.Duration.
	// Test sets it; Setup requires it.
	Duration time.Duration
	// Quiet is the length of the recovery window at the end of the run. Default 10s.
	Quiet time.Duration
	// ProgressWithin bounds the time from recovery start to the probe write's
	// acknowledgment. Default 5s.
	ProgressWithin time.Duration
	// Drain is the final period in which clients start no new operations. Default 2s.
	Drain time.Duration

	// Faults selects the fault preset that Test plans.
	Faults FaultPreset
	// LogLevel is the most verbose raft log level routed into the trace.
	LogLevel LogLevel
	// Bugs enables harness bug switches. For harness self-tests only.
	Bugs Bug

	// Set by in-package tests only (ETC-196).
	selfTestHook   func(seed uint64, check string)
	noConfSnapshot bool // disables ETC-174
	earlySend      bool // sends before persisting without BugSendBeforePersist, so ETC-114 rule 4 is evaluated
}

// Membership configures stage 1c.
type Membership struct {
	// Enabled adds spare servers and the admin client.
	Enabled bool
	// Spares is the number of spare servers. 1..4. Default 2.
	Spares int
	// Every is the mean gap between admin rounds; each gap is uniform in
	// [Every/2, 3*Every/2]. Default 5s.
	Every time.Duration
}

// Workload configures the closed-loop clients.
type Workload struct {
	// Keys is the key space size; keys are "k0".."k<Keys-1>". 1..64. Default 8.
	Keys int
	// ReadPPM is the probability, in parts per million, that a new operation is a read.
	ReadPPM uint32
	// ThinkMin and ThinkMax bound the pause before each new operation. Both zero: none.
	ThinkMin time.Duration
	ThinkMax time.Duration
	// AttemptTimeout is how long a client waits for a reply before retrying on another
	// server. Default 500ms.
	AttemptTimeout time.Duration
	// RetryDelay is the pause before retrying after a Dropped reply. Default 20ms.
	RetryDelay time.Duration
	// OpTimeout is how long an operation may take before it completes as Info. Default 5s.
	OpTimeout time.Duration
}

// FaultPreset selects a fault planner for Test (see Faults).
type FaultPreset uint8

const (
	FaultsDefault  FaultPreset = iota // partitions, isolations, crashes, pauses, sync failures, clock drift
	FaultsNone                        // no faults
	FaultsNetwork                     // partitions and isolations only
	FaultsCrash                       // crashes and sync failures only
	FaultsSelfTest                    // aggressive preset for bug-switch self-tests
)

// LogLevel is a raft log verbosity threshold.
type LogLevel uint8

const (
	LogDefault LogLevel = iota // same as LogInfo
	LogNone                    // only Panic and Fatal (which always panic)
	LogError
	LogWarning
	LogInfo
	LogDebug
)

// Bug is a bitmask of harness bug switches. Each switch deliberately breaks the
// harness's use of the etcd/raft contract, to prove that the checks catch it.
type Bug uint32

const (
	BugSendBeforePersist Bug = 1 << iota // send a Ready's messages before writing its WAL records
	BugSkipSync                          // never call File.Sync on the Ready and snapshot paths
	BugApplyBeforeCommit                 // apply normal entries from rd.Entries before they commit
	BugForgetHardState                   // never write HardState records
	BugSnapshotOffByOne                  // label local snapshots with applied-1 instead of applied
)

const allBugs = BugSendBeforePersist | BugSkipSync | BugApplyBeforeCommit | BugForgetHardState | BugSnapshotOffByOne

var bugNames = []struct {
	b    Bug
	name string
}{
	{BugSendBeforePersist, "SendBeforePersist"},
	{BugSkipSync, "SkipSync"},
	{BugApplyBeforeCommit, "ApplyBeforeCommit"},
	{BugForgetHardState, "ForgetHardState"},
	{BugSnapshotOffByOne, "SnapshotOffByOne"},
}

// names returns the switch names of b in declaration order joined with ",".
func (b Bug) names() string {
	var out []string
	for _, bn := range bugNames {
		if b&bn.b != 0 {
			out = append(out, bn.name)
		}
	}
	return strings.Join(out, ",")
}

// DefaultConfig returns the recommended stage 1a configuration (table in ETC-010).
func DefaultConfig() Config {
	return Config{
		Nodes:                     3,
		Clients:                   3,
		TickInterval:              100 * time.Millisecond,
		ElectionTick:              10,
		HeartbeatTick:             1,
		PreVote:                   true,
		CheckQuorum:               true,
		StepDownOnRemoval:         true,
		DisableProposalForwarding: false,
		MaxSizePerMsg:             1 << 20,
		MaxInflightMsgs:           256,
		MaxUncommittedEntriesSize: 0,
		SyncLatencyMin:            200 * time.Microsecond,
		SyncLatencyMax:            2 * time.Millisecond,
		SyncOnlyMustSync:          false,
		ReportUnreachable:         true,
		SnapshotTimeout:           0,
		CrashRestartDelay:         500 * time.Millisecond,
		SnapshotEvery:             0,
		CompactKeep:               10,
		Membership:                Membership{Enabled: false, Spares: 2, Every: 5 * time.Second},
		Workload: Workload{
			Keys:           8,
			ReadPPM:        500_000,
			ThinkMin:       10 * time.Millisecond,
			ThinkMax:       90 * time.Millisecond,
			AttemptTimeout: 500 * time.Millisecond,
			RetryDelay:     20 * time.Millisecond,
			OpTimeout:      5 * time.Second,
		},
		Duration:       0,
		Quiet:          10 * time.Second,
		ProgressWithin: 5 * time.Second,
		Drain:          2 * time.Second,
		Faults:         FaultsDefault,
		LogLevel:       LogInfo,
		Bugs:           0,
	}
}

// SelfTestConfig returns the configuration used by the bug-switch self-tests with
// Bugs set to b (table in ETC-191).
func SelfTestConfig(b Bug) Config {
	c := DefaultConfig()
	c.Faults = FaultsSelfTest
	c.SyncLatencyMin, c.SyncLatencyMax = 5*time.Millisecond, 50*time.Millisecond
	c.Workload.ThinkMin, c.Workload.ThinkMax = 0, 20*time.Millisecond
	c.SnapshotEvery, c.CompactKeep = 50, 10
	c.Quiet, c.ProgressWithin, c.Drain = 6*time.Second, 4*time.Second, time.Second
	c.Bugs = b
	return c
}

// NetConfig returns the link configuration Test uses when Options.Net is zero (ETC-022).
func NetConfig() simnet.Config {
	return simnet.Config{Default: simnet.Link{
		Latency: time.Millisecond,
		Jitter:  2 * time.Millisecond,
		TailPPM: 10_000,
		Tail:    50 * time.Millisecond,
		DropPPM: 1_000,
		DupPPM:  1_000,
		FIFO:    false,
	}}
}

// withDefaults applies ETC-011.
func (c Config) withDefaults() Config {
	setInt := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	setDur := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	setInt(&c.Nodes, 3)
	setInt(&c.Clients, 3)
	setDur(&c.TickInterval, 100*time.Millisecond)
	setInt(&c.ElectionTick, 10)
	setInt(&c.HeartbeatTick, 1)
	if c.MaxSizePerMsg == 0 {
		c.MaxSizePerMsg = 1 << 20
	}
	setInt(&c.MaxInflightMsgs, 256)
	setDur(&c.CrashRestartDelay, 500*time.Millisecond)
	if c.CompactKeep == 0 {
		c.CompactKeep = 10
	}
	if c.Membership.Enabled {
		setInt(&c.Membership.Spares, 2)
		setDur(&c.Membership.Every, 5*time.Second)
	}
	setInt(&c.Workload.Keys, 8)
	setDur(&c.Workload.AttemptTimeout, 500*time.Millisecond)
	setDur(&c.Workload.RetryDelay, 20*time.Millisecond)
	setDur(&c.Workload.OpTimeout, 5*time.Second)
	setDur(&c.Quiet, 10*time.Second)
	setDur(&c.ProgressWithin, 5*time.Second)
	setDur(&c.Drain, 2*time.Second)
	if c.SnapshotTimeout == 0 && c.ElectionTick > 0 && c.TickInterval > 0 {
		c.SnapshotTimeout = 2 * time.Duration(c.ElectionTick) * c.TickInterval
	}
	if c.LogLevel == LogDefault {
		c.LogLevel = LogInfo
	}
	return c
}

// validate applies ETC-012 to a config with defaults applied.
func (c Config) validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("etcdraft: invalid config: "+format, args...)
	}
	if c.Nodes != 3 && c.Nodes != 5 {
		return bad("Nodes must be 3 or 5, got %d", c.Nodes)
	}
	if c.Clients < 1 || c.Clients > 16 {
		return bad("Clients must be in 1..16, got %d", c.Clients)
	}
	durations := []struct {
		name string
		v    time.Duration
	}{
		{"TickInterval", c.TickInterval},
		{"SyncLatencyMin", c.SyncLatencyMin},
		{"SyncLatencyMax", c.SyncLatencyMax},
		{"SnapshotTimeout", c.SnapshotTimeout},
		{"CrashRestartDelay", c.CrashRestartDelay},
		{"Membership.Every", c.Membership.Every},
		{"Workload.ThinkMin", c.Workload.ThinkMin},
		{"Workload.ThinkMax", c.Workload.ThinkMax},
		{"Workload.AttemptTimeout", c.Workload.AttemptTimeout},
		{"Workload.RetryDelay", c.Workload.RetryDelay},
		{"Workload.OpTimeout", c.Workload.OpTimeout},
		{"Duration", c.Duration},
		{"Quiet", c.Quiet},
		{"ProgressWithin", c.ProgressWithin},
		{"Drain", c.Drain},
	}
	for _, d := range durations {
		if d.v < 0 {
			return bad("%s must not be negative", d.name)
		}
	}
	if c.HeartbeatTick < 1 || c.ElectionTick <= c.HeartbeatTick {
		return bad("ElectionTick (%d) must be greater than HeartbeatTick (%d) >= 1", c.ElectionTick, c.HeartbeatTick)
	}
	if c.MaxInflightMsgs < 1 {
		return bad("MaxInflightMsgs must be at least 1")
	}
	if c.SyncLatencyMin > c.SyncLatencyMax {
		return bad("SyncLatencyMin must not exceed SyncLatencyMax")
	}
	if c.Membership.Enabled && (c.Membership.Spares < 1 || c.Membership.Spares > 4) {
		return bad("Membership.Spares must be in 1..4, got %d", c.Membership.Spares)
	}
	if c.Workload.Keys < 1 || c.Workload.Keys > 64 {
		return bad("Workload.Keys must be in 1..64, got %d", c.Workload.Keys)
	}
	if c.Workload.ReadPPM > 1_000_000 {
		return bad("Workload.ReadPPM must not exceed 1000000")
	}
	if c.Workload.ThinkMin > c.Workload.ThinkMax {
		return bad("Workload.ThinkMin must not exceed Workload.ThinkMax")
	}
	if c.Workload.OpTimeout <= c.Workload.AttemptTimeout {
		return bad("Workload.OpTimeout must exceed Workload.AttemptTimeout")
	}
	if c.Duration == 0 {
		return bad("Duration must be set (it must equal faultline.Options.Duration)")
	}
	if c.Quiet < c.ProgressWithin+c.Drain {
		return bad("Quiet must be at least ProgressWithin + Drain")
	}
	if c.Duration <= c.Quiet {
		return bad("Duration must exceed Quiet")
	}
	if c.Faults > FaultsSelfTest {
		return bad("Faults has an undefined value %d", c.Faults)
	}
	if c.LogLevel > LogDebug {
		return bad("LogLevel has an undefined value %d", c.LogLevel)
	}
	if c.Bugs&^allBugs != 0 {
		return bad("Bugs has an undefined value %d", c.Bugs)
	}
	return nil
}

// validateOptions applies ETC-022.
func validateOptions(cfg Config, opts faultline.Options) (Config, faultline.Options, error) {
	if opts.NoCryptoSeed {
		return cfg, opts, errors.New("etcdraft: Options.NoCryptoSeed must be false: etcd/raft draws election timeouts from crypto/rand")
	}
	if opts.Mode != faultline.ModeEvent {
		return cfg, opts, errors.New("etcdraft: Options.Mode must be ModeEvent")
	}
	if opts.Duration == 0 {
		opts.Duration = 60 * time.Second
	}
	if cfg.Duration == 0 {
		cfg.Duration = opts.Duration
	}
	if cfg.Duration != opts.Duration {
		return cfg, opts, fmt.Errorf("etcdraft: Config.Duration (%v) differs from Options.Duration (%v)", cfg.Duration, opts.Duration)
	}
	if opts.Net == (simnet.Config{}) {
		opts.Net = NetConfig()
	}
	if opts.MaxEvents == 0 {
		opts.MaxEvents = 50_000_000
	}
	return cfg, opts, nil
}
