// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// TestTriage re-runs seeds with the variants of the ETC-201 triage steps 3 to 5. It runs
// only with ETCDRAFT_TRIAGE=1 (0 or unset skips it, anything else fails); combine it with
// FAULTLINE_SEED and FAULTLINE_SCHEDULE. An unknown value of any variable below fails the
// test, so a typo never runs the default configuration. triageOptions maps the variables
// and TestTriageOptions checks the mapping.
//
//	ETCDRAFT_TRIAGE_STAGE                1a (default), 1b (SnapshotEvery 50) or 1c (Membership.Enabled)
//	ETCDRAFT_TRIAGE_NODES                3 (default) or 5
//	ETCDRAFT_TRIAGE_NET                  default, fifo, nodup, nodrop or notail (variants of NetConfig)
//	ETCDRAFT_TRIAGE_CRASH                any (default), lose_unsynced, keep_prefix, keep_subset or torn
//	ETCDRAFT_TRIAGE_SYNC_ONLY_MUST_SYNC  0 (default) or 1
//	ETCDRAFT_TRIAGE_SYNC_LATENCY         default or 0 (SyncLatencyMin = SyncLatencyMax = 0)
//
// With every variable at its default, the configuration equals TestHunt's for that stage
// and node count, so a hunt seed reproduces with the same trace hash.
func TestTriage(t *testing.T) {
	switch v := os.Getenv("ETCDRAFT_TRIAGE"); v {
	case "1":
	case "", "0":
		t.Skip("set ETCDRAFT_TRIAGE=1 (with FAULTLINE_SEED and optionally FAULTLINE_SCHEDULE) to run")
	default:
		t.Fatalf("ETCDRAFT_TRIAGE=%q: want 0 or 1", v)
	}
	cfg, opts, err := triageOptions(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	Test(t, cfg, opts)
}

// triageOptions maps TestTriage's variables to the harness configuration and the faultline
// options of the run. lookup returns the value of a variable, "" when it is unset. A value
// that is not listed in TestTriage's doc is an error.
func triageOptions(lookup func(string) string) (Config, faultline.Options, error) {
	unknown := func(name, want string) error { return fmt.Errorf("%s=%q: want %s", name, lookup(name), want) }
	cfg := DefaultConfig()
	switch lookup("ETCDRAFT_TRIAGE_STAGE") {
	case "", "1a":
	case "1b":
		cfg.SnapshotEvery = 50
	case "1c":
		cfg.Membership.Enabled = true
	default:
		return Config{}, faultline.Options{}, unknown("ETCDRAFT_TRIAGE_STAGE", "1a, 1b or 1c")
	}
	switch lookup("ETCDRAFT_TRIAGE_NODES") {
	case "", "3":
	case "5":
		cfg.Nodes = 5
	default:
		return Config{}, faultline.Options{}, unknown("ETCDRAFT_TRIAGE_NODES", "3 or 5")
	}
	net := NetConfig()
	switch lookup("ETCDRAFT_TRIAGE_NET") {
	case "", "default":
	case "fifo":
		net.Default.FIFO = true
	case "nodup":
		net.Default.DupPPM = 0
	case "nodrop":
		net.Default.DropPPM = 0
	case "notail":
		net.Default.TailPPM, net.Default.Tail = 0, 0
	default:
		return Config{}, faultline.Options{}, unknown("ETCDRAFT_TRIAGE_NET", "default, fifo, nodup, nodrop or notail")
	}
	var disk simdisk.Config
	switch lookup("ETCDRAFT_TRIAGE_CRASH") {
	case "", "any":
	case "lose_unsynced":
		disk.Crash = simdisk.CrashLoseUnsynced
	case "keep_prefix":
		disk.Crash = simdisk.CrashKeepPrefix
	case "keep_subset":
		disk.Crash = simdisk.CrashKeepSubset
	case "torn":
		disk.Crash = simdisk.CrashTorn
	default:
		return Config{}, faultline.Options{}, unknown("ETCDRAFT_TRIAGE_CRASH", "any, lose_unsynced, keep_prefix, keep_subset or torn")
	}
	switch lookup("ETCDRAFT_TRIAGE_SYNC_ONLY_MUST_SYNC") {
	case "", "0":
	case "1":
		cfg.SyncOnlyMustSync = true
	default:
		return Config{}, faultline.Options{}, unknown("ETCDRAFT_TRIAGE_SYNC_ONLY_MUST_SYNC", "0 or 1")
	}
	switch lookup("ETCDRAFT_TRIAGE_SYNC_LATENCY") {
	case "", "default":
	case "0":
		cfg.SyncLatencyMin, cfg.SyncLatencyMax = 0, 0
	default:
		return Config{}, faultline.Options{}, unknown("ETCDRAFT_TRIAGE_SYNC_LATENCY", "default or 0")
	}
	return cfg, faultline.Options{Duration: 60 * time.Second, Net: net, Disk: disk}, nil
}

// TestTriageOptions checks how TestTriage maps its variables (ETC-201.3 to ETC-201.5): with
// none set, the effective configuration equals the one of the 60 s default-faults runs
// (TestDefaultFaults, and the hunt's stage 1a), so a hunt seed reproduces with the same
// trace hash; each value changes exactly the fields it names and nothing else; an unknown
// value is an error that names the variable and the allowed values.
func TestTriageOptions(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(name string) string { return m[name] }
	}
	defCfg, defOpts, err := triageOptions(env())
	if err != nil {
		t.Fatal(err)
	}
	// What the default-faults runs pass to Test, less the seed count.
	wantCfg, wantOpts, err := validateOptions(DefaultConfig(), faultline.Options{Duration: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	gotCfg, gotOpts, err := validateOptions(defCfg, defOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCfg, wantCfg) || !reflect.DeepEqual(gotOpts, wantOpts) {
		t.Fatalf("defaults differ from DefaultConfig() with Options{Duration: 60s}:\n got %+v %+v\nwant %+v %+v", gotCfg, gotOpts, wantCfg, wantOpts)
	}

	rows := []struct {
		name string
		env  func(string) string
		edit func(c *Config, o *faultline.Options) // the change to the defaults; nil: none
	}{
		{"explicit defaults", env("ETCDRAFT_TRIAGE_STAGE", "1a", "ETCDRAFT_TRIAGE_NODES", "3", "ETCDRAFT_TRIAGE_NET", "default",
			"ETCDRAFT_TRIAGE_CRASH", "any", "ETCDRAFT_TRIAGE_SYNC_ONLY_MUST_SYNC", "0", "ETCDRAFT_TRIAGE_SYNC_LATENCY", "default"), nil},
		{"stage 1b", env("ETCDRAFT_TRIAGE_STAGE", "1b"), func(c *Config, _ *faultline.Options) { c.SnapshotEvery = 50 }},
		{"stage 1c", env("ETCDRAFT_TRIAGE_STAGE", "1c"), func(c *Config, _ *faultline.Options) { c.Membership.Enabled = true }},
		{"5 nodes", env("ETCDRAFT_TRIAGE_NODES", "5"), func(c *Config, _ *faultline.Options) { c.Nodes = 5 }},
		{"net fifo", env("ETCDRAFT_TRIAGE_NET", "fifo"), func(_ *Config, o *faultline.Options) { o.Net.Default.FIFO = true }},
		{"net nodup", env("ETCDRAFT_TRIAGE_NET", "nodup"), func(_ *Config, o *faultline.Options) { o.Net.Default.DupPPM = 0 }},
		{"net nodrop", env("ETCDRAFT_TRIAGE_NET", "nodrop"), func(_ *Config, o *faultline.Options) { o.Net.Default.DropPPM = 0 }},
		{"net notail", env("ETCDRAFT_TRIAGE_NET", "notail"), func(_ *Config, o *faultline.Options) { o.Net.Default.TailPPM, o.Net.Default.Tail = 0, 0 }},
		{"crash lose_unsynced", env("ETCDRAFT_TRIAGE_CRASH", "lose_unsynced"), func(_ *Config, o *faultline.Options) { o.Disk.Crash = simdisk.CrashLoseUnsynced }},
		{"crash keep_prefix", env("ETCDRAFT_TRIAGE_CRASH", "keep_prefix"), func(_ *Config, o *faultline.Options) { o.Disk.Crash = simdisk.CrashKeepPrefix }},
		{"crash keep_subset", env("ETCDRAFT_TRIAGE_CRASH", "keep_subset"), func(_ *Config, o *faultline.Options) { o.Disk.Crash = simdisk.CrashKeepSubset }},
		{"crash torn", env("ETCDRAFT_TRIAGE_CRASH", "torn"), func(_ *Config, o *faultline.Options) { o.Disk.Crash = simdisk.CrashTorn }},
		{"sync only must sync", env("ETCDRAFT_TRIAGE_SYNC_ONLY_MUST_SYNC", "1"), func(c *Config, _ *faultline.Options) { c.SyncOnlyMustSync = true }},
		{"sync latency 0", env("ETCDRAFT_TRIAGE_SYNC_LATENCY", "0"), func(c *Config, _ *faultline.Options) { c.SyncLatencyMin, c.SyncLatencyMax = 0, 0 }},
		{"all at once", env("ETCDRAFT_TRIAGE_STAGE", "1c", "ETCDRAFT_TRIAGE_NODES", "5", "ETCDRAFT_TRIAGE_NET", "notail", "ETCDRAFT_TRIAGE_CRASH", "torn",
			"ETCDRAFT_TRIAGE_SYNC_ONLY_MUST_SYNC", "1", "ETCDRAFT_TRIAGE_SYNC_LATENCY", "0"), func(c *Config, o *faultline.Options) {
			c.Membership.Enabled, c.Nodes, c.SyncOnlyMustSync = true, 5, true
			c.SyncLatencyMin, c.SyncLatencyMax = 0, 0
			o.Net.Default.TailPPM, o.Net.Default.Tail = 0, 0
			o.Disk.Crash = simdisk.CrashTorn
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			wantCfg, wantOpts := defCfg, defOpts
			if r.edit != nil {
				r.edit(&wantCfg, &wantOpts)
				if reflect.DeepEqual(wantCfg, defCfg) && reflect.DeepEqual(wantOpts, defOpts) {
					t.Fatal("the row's change equals the defaults, so it would test nothing")
				}
			}
			cfg, opts, err := triageOptions(r.env)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg, wantCfg) || !reflect.DeepEqual(opts, wantOpts) {
				t.Fatalf("got %+v %+v\nwant %+v %+v", cfg, opts, wantCfg, wantOpts)
			}
		})
	}

	for _, b := range []struct{ name, want string }{
		{"ETCDRAFT_TRIAGE_STAGE", "1a, 1b or 1c"},
		{"ETCDRAFT_TRIAGE_NODES", "3 or 5"},
		{"ETCDRAFT_TRIAGE_NET", "default, fifo, nodup, nodrop or notail"},
		{"ETCDRAFT_TRIAGE_CRASH", "any, lose_unsynced, keep_prefix, keep_subset or torn"},
		{"ETCDRAFT_TRIAGE_SYNC_ONLY_MUST_SYNC", "0 or 1"},
		{"ETCDRAFT_TRIAGE_SYNC_LATENCY", "default or 0"},
	} {
		want := fmt.Sprintf("%s=%q: want %s", b.name, "bogus", b.want)
		if _, _, err := triageOptions(env(b.name, "bogus")); err == nil || err.Error() != want {
			t.Fatalf("%s=bogus: error %v; want %q", b.name, err, want)
		}
	}
}

// TestZeroSyncLatency covers the inline sync path of ETC-061 step 8, which no other test
// runs: every other test has SyncLatencyMin above 0, so its sync is always a later event.
// TestTriage's ETCDRAFT_TRIAGE_SYNC_LATENCY=0 (ETC-201.5) relies on it. With
// SyncLatencyMax = 0 each Ready that wrote records is synced inside the ready event, so
// the trace must show that: every etcdraft.persist record follows the etcdraft.ready
// record of its node with no other event between, and no etcdraft/sync event runs. The
// Syncs counter must equal the bootstrap syncs plus one per persist record, and no Ready
// is ever in flight, so no input event is handled during a persist.
func TestZeroSyncLatency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.SyncLatencyMin, cfg.SyncLatencyMax = 0, 0
	opts := testOptions(t, &cfg, 20*time.Second, true)
	var st Stats
	var persists, apart, syncEvents int
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(Faults(cfg))
		w.Final("test: observe", func() error {
			st, persists, apart, syncEvents = c.Stats(), 0, 0, 0
			events := 0                        // kernel.event and kernel.defer records so far
			readyAt := map[kernel.NodeID]int{} // per node: events when its last etcdraft.ready came
			for _, r := range w.Sim.Records() {
				switch r.Kind {
				case "kernel.event", "kernel.defer":
					events++
					if r.Text == "etcdraft/sync" {
						syncEvents++
					}
				case "etcdraft.ready":
					readyAt[r.Node] = events
				case "etcdraft.persist":
					persists++
					if at, ok := readyAt[r.Node]; !ok || at != events {
						apart++
					}
				}
			}
			return nil
		})
	})
	if st.Readies == 0 || persists == 0 {
		t.Fatalf("%d Readys and %d persist records; want Readys that wrote records", st.Readies, persists)
	}
	if apart != 0 || syncEvents != 0 {
		t.Fatalf("%d of %d persist records are not in the event of their ready record and %d etcdraft/sync events ran; want 0 of each with zero sync latency (ETC-061 step 8)", apart, persists, syncEvents)
	}
	if st.Syncs != uint64(st.Boots+persists) { //nolint:gosec // both are small counts: Boots is 3 (no crashes with FaultsNone)
		t.Fatalf("Syncs = %d with %d boots and %d persist records; want the bootstrap syncs plus one per persist record", st.Syncs, st.Boots, persists)
	}
	if st.StepsDuringPersist != 0 {
		t.Fatalf("StepsDuringPersist = %d with zero sync latency; want 0, no Ready is in flight", st.StepsDuringPersist)
	}
}
