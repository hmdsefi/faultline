# Test etcd/raft with the harness

The `etcdraft` harness runs clusters of [etcd/raft](https://github.com/etcd-io/raft)
(`go.etcd.io/raft/v3` v3.7.0) inside faultline. Each server keeps its write-ahead log on the
simulated disk, and clients write and read keys. Faults hit the cluster, and checks for raft's
safety properties run after every event. This guide shows how to run the harness, configure it,
write your own scenario, and prove that its checks catch real mistakes.

## Add the harness module

The harness is a Go module of its own, so etcd/raft and protobuf stay out of faultline's core
module. Add it to your module:

```sh
go get github.com/hmdsefi/faultline/harness/etcdraft
```

## Run the default test

One call runs the whole harness. Create `raft_test.go`:

```go
package raftdemo

import (
	"testing"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/harness/etcdraft"
)

func TestRaft(t *testing.T) {
	etcdraft.Test(t, etcdraft.DefaultConfig(), faultline.Options{Seeds: 100})
}
```

Run it:

```sh
go test -run '^TestRaft$' .
```

```text
ok  	example.com/raftdemo	7.170s
```

`etcdraft.Test` fills in the options that you leave at zero and then calls `faultline.Run`. With
`DefaultConfig`, each of the 100 seeds runs this world:

- three servers, `n1` to `n3`, and three clients, `c1` to `c3`, that write and read eight keys;
- 60 seconds of virtual time;
- the harness's network: 1 ms of latency and up to 2 ms of jitter. One message in 100 gets up to 50
  ms more, one in 1,000 is lost, and one in 1,000 is duplicated;
- the default faults: partitions, an isolated leader, cut links, and slow and lossy links. Servers
  and leaders crash, servers pause, syncs fail, and clocks drift;
- a quiet last 10 seconds, in which the cluster heals and must make progress again.

## Choose the faults and the cluster size

`Config` starts from `DefaultConfig`. `Nodes` sets the number of servers, 3 or 5. `Faults` picks one
of these fault presets:

- `FaultsDefault`: every fault above;
- `FaultsNetwork`: partitions, an isolated leader, cut links, and slow and lossy links;
- `FaultsCrash`: crashes of any server and of the leader, and failed syncs;
- `FaultsNone`: no faults, for a scenario that plans its own.

The following test runs five servers under network faults only:

```go
func TestRaftFiveNodes(t *testing.T) {
	cfg := etcdraft.DefaultConfig()
	cfg.Nodes = 5
	cfg.Faults = etcdraft.FaultsNetwork
	etcdraft.Test(t, cfg, faultline.Options{Seeds: 50})
}
```

## Turn on snapshots and membership changes

Two settings exercise more of etcd/raft. `SnapshotEvery` makes each server take a snapshot and
compact its in-memory log every time it applies that many entries. A server that falls behind then
receives a snapshot. `Membership.Enabled` adds two spare servers and an admin client that changes
the membership during the run. It adds learners, promotes, demotes, removes and replaces voters:

```go
func TestRaftSnapshots(t *testing.T) {
	cfg := etcdraft.DefaultConfig()
	cfg.SnapshotEvery = 50
	etcdraft.Test(t, cfg, faultline.Options{Seeds: 50})
}

func TestRaftMembership(t *testing.T) {
	cfg := etcdraft.DefaultConfig()
	cfg.Membership.Enabled = true
	etcdraft.Test(t, cfg, faultline.Options{Seeds: 50})
}
```

Run all three:

```sh
go test -v -run 'TestRaftFiveNodes|TestRaftSnapshots|TestRaftMembership' .
```

The end of the output:

```text
--- PASS: TestRaftFiveNodes (7.00s)
--- PASS: TestRaftSnapshots (3.04s)
--- PASS: TestRaftMembership (3.87s)
ok  	example.com/raftdemo	14.242s
```

## Write your own scenario

For your own faults and checks, call `etcdraft.Setup` inside `faultline.Run`. `Setup` adds the
servers, the clients and the harness's checks, and returns a `Cluster` to query. The following test
crashes whichever server leads at 5 seconds, and checks that another server becomes leader:

```go
package raftdemo

import (
	"errors"
	"testing"
	"time"

	"go.etcd.io/raft/v3"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/harness/etcdraft"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

func TestLeaderCrash(t *testing.T) {
	cfg := etcdraft.DefaultConfig()
	cfg.Duration = 20 * time.Second
	opts := faultline.Options{Seeds: 20, Duration: cfg.Duration, Net: etcdraft.NetConfig()}
	faultline.Run(t, opts, func(w *faultline.World) {
		c := etcdraft.Setup(w, cfg)
		w.Plan(fault.Script(
			fault.Event{At: kernel.Time(5 * time.Second), Kind: fault.KindCrash, Role: "leader"},
		))
		w.Final("new leader after the crash", func() error {
			for _, ch := range c.LeaderChanges() {
				if ch.At > kernel.Time(5*time.Second) && ch.State == raft.StateLeader {
					return nil
				}
			}
			return errors.New("no server became leader after the crash")
		})
	})
}
```

```sh
go test -run '^TestLeaderCrash$' .
```

```text
ok  	example.com/raftdemo	0.732s
```

A scenario follows a few rules that `etcdraft.Test` otherwise handles:

- `Config.Duration` must equal `Options.Duration`.
- `Options.Net` set to `etcdraft.NetConfig()` gives the same links as `etcdraft.Test`. Left at zero,
  the run uses faultline's default network.
- `Options.NoCryptoSeed` stays false. etcd/raft draws its election timeouts from `crypto/rand`,
  which faultline seeds for every run.
- `Setup` registers the roles `leader`, `followers`, `voters` and `learners`. A fault's `Role`
  targets whichever servers hold the role at the moment of the fault.

`Cluster` answers questions from checks and roles. `Leader` returns the current leader and its term,
and `Status` returns a server's raft status. `LeaderChanges` and `Stats` describe the whole run.

## What the checks catch

`Setup` registers invariants that run after every event. Each one fails the run at the first event
that breaks it:

- `etcdraft: election safety`: a term never has two leaders.
- `etcdraft: state machine safety`: no two servers apply different entries at the same index, and
  every snapshot holds the state that the applied entries produce.
- `etcdraft: durable state`: after a crash, a server recovers at least what it had synced to its
  log.
- `etcdraft: hardstate monotonic`: a server's term and commit index never go back, and its vote
  never changes within a term.
- `etcdraft: ready contract`: the harness follows etcd/raft's rules for handling a `Ready`, such as
  persisting the new state before it sends messages.
- `etcdraft: configuration agreement` and `etcdraft: leader is voter`: servers agree on each
  membership change, and only a voter leads.
- `etcdraft: harness`: the harness itself hit an error.

Final checks run at the end of a run that did not fail:

- `etcdraft: progress`: a write made after the faults stop is acknowledged within 5 seconds.
- `etcdraft: acked writes survive`: every acknowledged write was applied, and every voter that is up
  applied it.
- `etcdraft: replicas agree`: every voter that is up holds the state and the configuration that its
  applied entries produce.
- `etcdraft: wal agrees with memory`: each server's log file matches what the server holds in
  memory.

## Prove that the checks work

A check that never fails proves nothing on its own. `Config.Bugs` turns on bug switches that break
the harness on purpose. Each one breaks a rule of etcd/raft's contract for the code that drives it:

- `BugSendBeforePersist` sends a `Ready`'s messages before it writes the log.
- `BugSkipSync` never syncs the log.
- `BugApplyBeforeCommit` applies entries before they commit.
- `BugForgetHardState` never writes the term, vote and commit index.
- `BugSnapshotOffByOne` labels each snapshot with the wrong index.

`SelfTestConfig` returns a configuration with harder faults for these tests. The following test
turns on `BugSkipSync`, with a disk on which a crash loses every unsynced write:

```go
package raftdemo

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/harness/etcdraft"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

func TestSkipSyncIsCaught(t *testing.T) {
	cfg := etcdraft.SelfTestConfig(etcdraft.BugSkipSync)
	opts := faultline.Options{
		Seeds:    50,
		Duration: 20 * time.Second,
		Disk:     simdisk.Config{Crash: simdisk.CrashLoseUnsynced},
	}
	etcdraft.Test(t, cfg, opts)
}
```

The first seed fails the durable state check:

```text
--- FAIL: TestSkipSyncIsCaught (0.02s)
    bugs_test.go:19: faultline: running 50 seeds from base 0x7fb72cd7e46c8431 (test name)
    --- FAIL: TestSkipSyncIsCaught/seed=0x089cc95a16ce9a11 (0.02s)
        faultline: invariant "etcdraft: durable state" violated at t=3.121118981s (event 1066)
          n1 recovered 51 WAL bytes but 1039 were persisted
        replay:    FAULTLINE_SEED=0x089cc95a16ce9a11 go test -v -run '^TestSkipSyncIsCaught$' example.com/raftdemo
        artifacts: /tmp/faultline-501/example.com_raftdemo/TestSkipSyncIsCaught/089cc95a16ce9a11/
    bugs_test.go:19: faultline: stopping after failing seed 0x089cc95a16ce9a11; 49 of 50 seeds not run (set Options.KeepGoing to run all)
FAIL
FAIL	example.com/raftdemo	0.353s
FAIL
```

## What faultline's own hunt found

faultline ran 40,000 seeds of the harness against etcd/raft v3.7.0. It ran 10,000 seeds each for
three and five servers, with the default configuration and with a snapshot every 50 entries. Each
seed ran 60 seconds of virtual time under the default faults. No seed failed. In faultline's
self-test of the harness, each of the five bug switches failed on the first seed it ran.

On an Apple M4 Max, a three-server seed took 68 ms on average and a five-server seed 124 ms. The
whole hunt took about 32 minutes, with two `go test` processes at a time.

## Before you report an etcd/raft bug

A failing seed is not yet an etcd/raft bug. Work through these steps first, and keep the result of
each:

1. Replay the seed with `FAULTLINE_SEED`, and confirm that the trace hash matches.
2. Narrow the fault schedule, as [Replay a failing
   seed](replay-a-seed.md#narrow-a-failure-by-editing-its-schedule) shows.
3. Check the failing check's name and the bug switches. A failure of `etcdraft: harness` or
   `etcdraft: ready contract`, or a run with `Config.Bugs` set, points at the harness, not at
   etcd/raft.
4. Run the narrowed schedule again with a gentler network in `Options.Net`: FIFO links, and no
   duplication, loss or tail delay. etcd/raft is designed for networks that lose, duplicate and
   reorder messages, so a failure that needs them is still in scope. Record which ones it needs.
5. Run it again with each crash model of `Options.Disk`, with the clock drift faults deleted from
   the schedule, and with `SyncOnlyMustSync` flipped. Set `SyncLatencyMin` and `SyncLatencyMax` to
   zero to rule out the harness's sync timing.
6. Compare the behavior with etcd/raft's package documentation, and search its issues for a known
   report.
7. Reproduce the bug outside faultline, with a test that drives `RawNode` and `MemoryStorage`
   through the narrowed sequence.

## Next steps

- [Read a failure artifact](read-an-artifact.md): debug a failing seed of the harness.
- [Run faultline in CI](ci.md): run the harness on every pull request and every night.
- [The etcdraft package on
  pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline/harness/etcdraft): every `Config`
  field and `Cluster` method.
