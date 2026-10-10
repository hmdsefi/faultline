package etcdraft

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// isolateN3 is the AT-ETC-19 script: isolate n3 from 2 s to 12 s.
func isolateN3() []fault.Event {
	return []fault.Event{
		{At: kernel.Time(2 * time.Second), Kind: fault.KindIsolate, Node: "n3"},
		{At: kernel.Time(12 * time.Second), Kind: fault.KindHeal},
	}
}

// AT-ETC-19
func TestSnapshotTransfer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.SnapshotEvery = 50
	opts := testOptions(t, &cfg, 30*time.Second, false)
	var st Stats
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		w.Plan(fault.Script(isolateN3()...))
		w.Final("test: stats", func() error { st = c.Stats(); return nil })
	})
	if st.SnapshotsCreated < 1 || st.SnapshotsInstalled < 1 || st.SnapshotReportsFinish < 1 {
		t.Fatalf("stats %+v; want at least one snapshot created, installed and acknowledged", st)
	}
}

// AT-ETC-20: a 3 s latency on the leader -> n3 link (with every leader -> n3 message
// lost instead, n3 never answers, the leader never marks it recently active, and no
// MsgSnap is sent).
func TestSnapshotTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Faults = FaultsNone
	cfg.SnapshotEvery = 50
	opts := testOptions(t, &cfg, 30*time.Second, false)
	var st Stats
	runSeed(t, 1, opts, func(w *faultline.World) {
		c := Setup(w, cfg)
		var linked kernel.NodeID
		w.Role("snap-source", func() []kernel.NodeID { // the leader at 12 s, remembered for the undo at 20 s
			if linked == 0 {
				if id, _ := c.Leader(); id != 0 {
					linked = c.Server(id).ID()
				}
			}
			if linked == 0 {
				return nil
			}
			return []kernel.NodeID{linked}
		})
		slow := &simnet.Link{Latency: 3 * time.Second}
		w.Plan(fault.Script(append(isolateN3(),
			fault.Event{At: kernel.Time(12 * time.Second), Kind: fault.KindLink, Role: "snap-source", Peer: "n3", Link: slow},
			fault.Event{At: kernel.Time(20 * time.Second), Kind: fault.KindLinkReset, Role: "snap-source", Peer: "n3"},
		)...))
		w.Final("test: stats", func() error { st = c.Stats(); return nil })
	})
	if st.SnapshotReportsFailure < 1 {
		t.Fatalf("stats %+v; want at least one ReportSnapshot(SnapshotFailure)", st)
	}
}
