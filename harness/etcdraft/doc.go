// Package etcdraft runs go.etcd.io/raft/v3 v3.7.0 clusters inside a faultline simulation:
// RawNode servers with a WAL on simdisk, a replicated KV state machine, closed-loop clients
// recorded in check/history, fault presets, and safety and liveness checks.
//
// Most tests call Test:
//
//	func TestRaft(t *testing.T) {
//		etcdraft.Test(t, etcdraft.DefaultConfig(), faultline.Options{Seeds: 100})
//	}
//
// A scenario with its own faults calls Setup inside faultline.Run. Config.Duration must
// equal Options.Duration, and Options.NoCryptoSeed must stay false: etcd/raft draws its
// election timeouts from crypto/rand, which faultline seeds for every attempt.
//
//	cfg := etcdraft.DefaultConfig()
//	cfg.Duration = 20 * time.Second
//	faultline.Run(t, faultline.Options{Duration: cfg.Duration}, func(w *faultline.World) {
//		etcdraft.Setup(w, cfg)
//		w.Plan(fault.Script(fault.Event{At: kernel.Time(5 * time.Second), Kind: fault.KindCrash, Role: "leader"}))
//	})
//
// The harness checks election safety, state machine safety, durable state, HardState
// monotonicity, the Ready contract, configuration agreement and leader-is-voter after
// every event, and progress, acknowledged writes, replica agreement and WAL contents at
// the end of each run. Bug switches (Config.Bugs) break the harness on purpose so its
// self-tests can prove the checks catch real ordering mistakes.
package etcdraft
