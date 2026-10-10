package etcdraft

import (
	"fmt"
	"strconv"
	"testing"

	"go.etcd.io/raft/v3"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/oracle"
)

// Setup adds the cluster to w: servers, clients, roles, invariants, and final checks.
// It plans no faults. Call it exactly once per World, before any other World call that
// adds nodes. Callers must not set Options.NoCryptoSeed (ETC-024): Setup cannot detect
// it, and etcd/raft's election timeouts are then not deterministic.
func Setup(w *faultline.World, cfg Config) *Cluster {
	if w.Sim.Lookup("n1") != nil {
		panic("etcdraft: Setup called twice on this World")
	}
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		w.T().Fatalf("%v", err)
	}
	raft.SetLogger(&raftLogger{sim: w.Sim, level: cfg.LogLevel})
	w.T().Cleanup(raft.ResetDefaultLogger)

	c := &Cluster{
		w:            w,
		cfg:          cfg,
		o:            oracle.New(bootSnapshot(cfg.Nodes)),
		byNode:       map[kernel.NodeID]*server{},
		clientByNode: map[kernel.NodeID]*client{},
		opIDs:        map[int64]uint64{},
	}
	if cfg.selfTestHook != nil {
		hook, seed := cfg.selfTestHook, w.Seed()
		c.o.OnViolation(func(check string, _ error) { hook(seed, check) })
	}
	end := w.End()
	c.recoveryStart = max(end.Add(-cfg.Quiet), 0)
	c.drainStart = end.Add(-cfg.Drain)

	for i := 1; i <= cfg.Nodes; i++ {
		s := &server{c: c, id: uint64(i), initial: true}
		s.node = w.AddServer("n"+strconv.Itoa(i), s.boot)
		c.servers = append(c.servers, s)
		c.byNode[s.node.ID()] = s
	}
	addClient := func(name string, cid uint32, kind clientKind) *client {
		cl := &client{c: c, name: name, cid: cid, kind: kind}
		cl.node = w.AddClient(name, cl.boot)
		c.clientByNode[cl.node.ID()] = cl
		return cl
	}
	for k := 1; k <= cfg.Clients; k++ {
		c.clients = append(c.clients, addClient("c"+strconv.Itoa(k), uint32(k), workloadClient))
	}
	//nolint:gosec // Config.Clients is validated to 1..16
	c.probe = addClient("probe", uint32(cfg.Clients+1), probeClient)
	w.Sim.OnCrash(c.onCrash)

	c.registerRoles()
	c.registerChecks()

	c.emit(nil, "etcdraft.setup", fmt.Sprintf("setup nodes=%d clients=%d spares=0", cfg.Nodes, cfg.Clients),
		attr("nodes", strconv.Itoa(cfg.Nodes)), attr("clients", strconv.Itoa(cfg.Clients)), attr("spares", "0"),
		attr("snapshot_every", u64(cfg.SnapshotEvery)), attr("membership", boolStr(cfg.Membership.Enabled)),
		attr("faults", strconv.Itoa(int(cfg.Faults))), attr("bugs", strconv.FormatUint(uint64(cfg.Bugs), 10)))
	if cfg.Bugs != 0 {
		c.emit(nil, "etcdraft.bugs_enabled", "bugs enabled: "+cfg.Bugs.names(), attr("bugs", cfg.Bugs.names()))
	}
	return c
}

// onCrash is the Setup crash hook: it drops a server's incarnation state and completes
// a client's pending operation as Info (ETC-097).
func (c *Cluster) onCrash(n *kernel.Node) {
	if s, ok := c.byNode[n.ID()]; ok && s.node == n {
		s.inc = nil
		return
	}
	if cl, ok := c.clientByNode[n.ID()]; ok && cl.node == n {
		cl.onCrash()
	}
}

// Test runs the harness under faultline.Run: it fills in options (ETC-022), then for
// every seed calls Setup and plans Faults(cfg).
func Test(t *testing.T, cfg Config, opts faultline.Options) {
	t.Helper()
	cfg, opts, err := validateOptions(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	faultline.Run(t, opts, func(w *faultline.World) {
		Setup(w, cfg)
		w.Plan(Faults(cfg))
	})
}
