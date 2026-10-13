// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"fmt"
	"testing"
	"time"

	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/fault"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
)

// ETC-061 step 2: the ready event checks the Ready it takes. The leader proposes an entry
// after its MemoryStorage has moved to a snapshot above its log, so the Ready's first entry
// lies below the storage's first index (ETC-114 rule 1).
func TestReadyEventChecksContract(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	craftedRun(t, cfg, func(_ *Cluster, inc *incarnation) (string, string) {
		l, _ := inc.ms.LastIndex()
		if err := inc.rn.Propose(nil); err != nil {
			return oracle.ReadyContract, "propose: " + err.Error()
		}
		if err := inc.ms.ApplySnapshot(snapAt(l+5, &raftpb.ConfState{Voters: []uint64{1, 2, 3}})); err != nil {
			return oracle.ReadyContract, "apply snapshot: " + err.Error()
		}
		inc.onReady()
		return oracle.ReadyContract, fmt.Sprintf("%s ready contract: rule 1: first entry %d outside [%d, %d]", inc.n.Name(), l+1, l+6, l+6)
	})
}

// ETC-061 steps 6 to 8 and ETC-064, read from the trace: a Ready that wrote a record is synced
// and passes one persist point (etcdraft.persist), unless SyncOnlyMustSync skips a Ready that
// raft does not need synced; a Ready that installs a snapshot is always synced. Every sync
// after the bootstrap one is a Ready's or a local snapshot's (ETC-151), and a local snapshot
// syncs outside a Ready. With a sync latency each Ready's sync runs in its own etcdraft/sync
// event; with none it runs inside the ready event, so no Ready is ever in flight.
func TestSyncPolicy(t *testing.T) {
	cases := []struct {
		name                               string
		mustSyncOnly, noLatency, snapshots bool
	}{
		{"every write", false, false, false},
		{"SyncOnlyMustSync", true, false, false},
		{"no sync latency", false, true, false},
		{"SyncOnlyMustSync with a snapshot install", true, false, true}, // the AT-ETC-19 setup
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Faults = FaultsNone
			cfg.SyncOnlyMustSync = tc.mustSyncOnly
			if tc.noLatency {
				cfg.SyncLatencyMin, cfg.SyncLatencyMax = 0, 0
			}
			if tc.snapshots {
				cfg.SnapshotEvery = 50
			}
			opts := testOptions(t, &cfg, 15*time.Second, true)
			var wrote, needSync, persists, syncEvents, inflight, creates, snapNoMust int
			var st Stats
			runSeed(t, 1, opts, func(w *faultline.World) {
				c := Setup(w, cfg)
				if tc.snapshots {
					w.Plan(fault.Script(isolateN3()...))
				}
				w.Final("test: readys", func() error {
					wrote, needSync, persists, syncEvents, inflight, creates, snapNoMust, st = 0, 0, 0, 0, 0, 0, 0, c.Stats()
					for _, r := range w.Sim.Records() {
						switch r.Kind {
						case "etcdraft.ready":
							snap := attrOf(r, "snap_index") != "0"
							if attrOf(r, "entries") == "0" && attrOf(r, "hs") == "" && !snap {
								continue // nothing written
							}
							wrote++
							if !tc.mustSyncOnly || attrOf(r, "must_sync") == "true" || snap {
								needSync++
							}
							if snap && attrOf(r, "must_sync") != "true" {
								snapNoMust++
							}
						case "etcdraft.persist":
							persists++
						case "etcdraft.snapshot":
							if attrOf(r, "action") == "create" {
								creates++
							}
						case "kernel.event":
							if r.Text == "etcdraft/sync" {
								syncEvents++
							}
						}
					}
					for _, s := range c.servers {
						if s.inc != nil && s.inc.inflight != nil {
							inflight++ // synced after the end: no persist record yet
						}
					}
					return nil
				})
			})
			if persists+inflight != needSync || st.Syncs != uint64(st.Boots+persists+creates) { //nolint:gosec // counts are small and positive
				t.Errorf("%d Readys wrote records, %d of them need a sync, %d in flight at the end; %d persist records, %d local snapshots, Syncs %d with %d boots",
					wrote, needSync, inflight, persists, creates, st.Syncs, st.Boots)
			}
			if tc.mustSyncOnly && needSync == wrote {
				t.Errorf("all %d Readys that wrote records needed a sync: the run never tried SyncOnlyMustSync", wrote)
			}
			if tc.snapshots && (snapNoMust == 0 || creates == 0) {
				t.Errorf("%d snapshot installs with must_sync=false and %d local snapshots; want at least one of each, or the row does not test the snapshot clause", snapNoMust, creates)
			}
			if tc.noLatency && (syncEvents != 0 || st.StepsDuringPersist != 0 || inflight != 0) || !tc.noLatency && syncEvents != persists {
				t.Errorf("%d etcdraft/sync events for %d persist points, StepsDuringPersist = %d, %d Readys in flight at the end; want one event per persist point, or none and 0 and 0 with no sync latency",
					syncEvents, persists, st.StepsDuringPersist, inflight)
			}
		})
	}
}
