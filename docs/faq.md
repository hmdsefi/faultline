# Limitations and FAQ

This page lists what faultline v0.1.0 does not do, the issues known at release, and how faultline
relates to other tools that test distributed systems. Read it before you decide whether faultline
fits your system.

## What v0.1.0 tests

v0.1.0 tests Go code written against faultline's node API. That code runs as callbacks on one
goroutine: boot functions, message handlers and timers. It sends messages with `w.Net` and keeps
files on the node's simulated volume. v0.1.0 also runs etcd/raft v3.7.0 through `harness/etcdraft`,
which does that port for one real system.

Code that starts goroutines, uses package `net` or opens files with `os` does not run inside the
simulation unchanged. You port it to the node API first, the way the etcd/raft harness ports raft.

## Who faultline is not for

faultline fits a Go team that builds a distributed system and wants failures it can replay. It is
the wrong tool in these cases:

- You need to test existing goroutine code without porting it. Running such code unchanged is
  planned for Phase 2, with no date.
- You want to test the system as deployed, with real binaries, a real network and real disks.
  faultline simulates all of them inside one test process.
- You want performance numbers. A callback takes zero virtual time, so a run says nothing about CPU
  cost or throughput.
- Your tests call `t.Parallel`. faultline seeds `crypto/rand` for every run, which Go does not allow
  in a parallel test. `Options.NoCryptoSeed` lifts the restriction, and `crypto/rand` is then no
  longer part of the replay.

## What v0.1.0 does not include

The [roadmap](https://github.com/hmdsefi/faultline/issues/167) plans these for later phases, with no
dates:

- Shrinking a failing run to its smallest form. In v0.1.0 you delete faults from a copy of
  `schedule.json` and replay it.
- Linearizability and isolation checkers. Your invariants and final checks are the checks.
- Running thousands of seeds in parallel with a `faultline sweep` command. `faultline.Run` runs one
  seed after another.
- The `faultline view` web app. `timeline.html` is the run view of v0.1.0.
- Harnesses for hashicorp/raft, CometBFT, Kubernetes controllers (controller-runtime), and NATS with
  JetStream.
- Pinning a seed in code without environment variables; [issue
  #259](https://github.com/hmdsefi/faultline/issues/259) tracks it. v0.1.0 pins a seed with
  `t.Setenv("FAULTLINE_SEED", ...)`.

## What your code must do

faultline controls time, scheduling, the network, the disk and its own random streams. It cannot see
the rest of your Go code. Code inside the simulation reads time and randomness from the node and
starts no goroutines. It sorts map keys when the order matters, and keeps no state between runs.
[How faultline works](how-it-works.md#the-determinism-rules-your-code-follows) lists the rules.

faultline does not inspect your code for these rules; it detects their effect. A broken rule changes
the trace, and `FAULTLINE_CHECK_DETERMINISM=1` reports the first record where two runs of a seed
differ.

The error text of a check is part of the trace too, so keep pointer addresses and wall-clock times
out of it. Report failures with `w.Invariant`, `w.Final` or `w.Sim.Fail`: a seed stopped by
`t.Fatal` or `t.FailNow` writes no artifact.

## How far a replay reaches

A seed replays the same run when four things match: the faultline version, the Go minor version,
your code and the options. `report.json` records all four, and a replay with `FAULTLINE_SEED` warns
about each difference.

faultline's CI computes the trace hashes of a set of scenarios on Linux on amd64 and macOS on arm64.
It does so with Go 1.26 and Go 1.27, and fails when the hashes disagree. Other platforms are not
checked.

## Known issues

These are the issues known at release:

- A replay with `FAULTLINE_SCHEDULE` fails with the same check, at the same virtual time and event
  number. Its trace hash differs: the replay has no planner records, and its fault records name
  `replay` as their source.
- Deleting a fault from a schedule changes the run from that point on. An edited replay is a
  different run, not a shorter copy of the original, so it can pass where you expected a failure.
- In the etcd/raft harness, every raft panic has the same signature,
  `panic:github.com/hmdsefi/faultline/harness/etcdraft.(*raftLogger).panic`. Raft raises its
  assertions through the harness's logger, so tell raft panics apart by their message.
- The `at` field of `trace.jsonl` is an integer number of nanoseconds. JavaScript loses precision
  above 2^53 nanoseconds, about 104 days of virtual time. Go readers are exact.

## Questions

### Can my coding agent run faultline on its own?

Yes. Every step from a failing seed to a pinned regression test is a command to run or a file to
read. `report.json` holds the failure, the replay command and the trace hash as data.
`FAULTLINE_SEED_LIST` and `FAULTLINE_RESULTS` let a script run a set of seeds and collect one JSON
line per seed. [Use faultline with an AI coding agent](ai-agents.md) walks through the loop.

### Is faultline a chaos engineering tool?

No. Chaos engineering injects faults into a running system: [Chaos
Monkey](https://netflix.github.io/chaosmonkey/) randomly terminates instances in production.
faultline never touches a real server or network. It simulates the machines, the network and the
disks inside `go test`, and injects faults into the simulation.

### How does faultline relate to FoundationDB's simulation testing?

faultline follows the approach of FoundationDB's simulation testing. [FoundationDB's
simulation](https://apple.github.io/foundationdb/testing.html) runs an entire FoundationDB cluster
in a single-threaded process. Its runtime, Flow, supports both production execution and
deterministic simulated execution, and the simulation models the network, the disks and machine
failures. faultline brings the same shape to Go as a library: one thread, a simulated network and
disk, and a seed. FoundationDB wrote its database on Flow so that the production code runs in
simulation. In faultline the equivalent is the node API.

### How does it relate to TigerBeetle's VOPR?

[TigerBeetle's VOPR](https://github.com/tigerbeetle/tigerbeetle/blob/main/docs/internals/vopr.md)
runs the production TigerBeetle code with the clock, the network and the disk stubbed out. A run is
deterministic from a seed and the Git commit. The VOPR injects faults such as dropped or reordered
packets, partitions and corrupted disk reads and writes. faultline gives any Go project the same
contract: the same seed and code give the same run. faultline also ties the run to the Go minor
version and the options.

### How does it relate to Antithesis?

[Antithesis](https://antithesis.com/docs/) is a testing platform that runs next to your CI. It runs
regular, non-deterministic software inside a [deterministic
hypervisor](https://antithesis.com/docs/resources/deterministic_simulation_testing/), so the
software does not have to be built for simulation. Every problem it finds reproduces. faultline
makes the other trade. It runs inside `go test` on your machine and needs no infrastructure, but in
v0.1.0 your code uses its node API.

### How does it relate to Jepsen?

[Jepsen](https://github.com/jepsen-io/jepsen) is a Clojure library that tests a real deployment. A
control node sets up the system on database nodes over SSH, and a nemesis process injects faults. A
checker then analyzes the recorded history of client operations. Jepsen also publishes
[analyses](https://jepsen.io/) of databases and queues. Jepsen tests the system as deployed, with
its real network stack and disks. faultline tests a model of the environment around your code, and
in exchange every failing run replays from its seed. Both record client operations as a history;
faultline writes its history to `history.jsonl`.

### How does it relate to testing/synctest?

[`testing/synctest`](https://pkg.go.dev/testing/synctest), generally available since [Go
1.25](https://go.dev/doc/go1.25), runs a test in a bubble with a fake clock. Time in the bubble
advances when every goroutine in it is durably blocked. It leaves the network to you: its
documentation says to use a fake network implementation. faultline adds the rest a distributed test
needs: a simulated network and disk, nodes that crash and restart, faults, and one seed for every
choice. v0.1.0 does not use `testing/synctest`; the roadmap plans a goroutine mode for Phase 2 that
runs your code inside a `testing/synctest` bubble.

### Can I use the kernel without faultline.Run?

Yes. Package `kernel` is a plain Go API: `kernel.New` creates a simulator, and you drive it with
`Run`, `RunUntil` or `RunFor`. Without `faultline.Run` you give up the seed list, the subtests, the
checks, the second run of failing seeds and the artifacts.

### Does faultline make network calls?

No. Neither the faultline module nor the etcd/raft harness imports a network package, and
`timeline.html` loads nothing from the network. A run happens inside the test process.

### How fast is it?

Virtual time skips the idle time between events, so a run costs only the CPU time of its callbacks.
On an Apple M4 Max, a 60-second run of a three-node etcd/raft cluster under faults took 68 ms on
average. On the same machine, the 50 seeds of the key-value example's `TestKV`, 10 seconds of
virtual time each, run in about one second. A failing seed runs twice, once more to write its
artifact.

## Next steps

- [Use faultline with an AI coding agent](ai-agents.md): the loop from a failing seed to a pinned
  regression test.
- [How faultline works](how-it-works.md): virtual time, the scheduler, the network, the disk and
  replay.
- [Environment variables](reference/environment.md): seeds, replay and checks without code changes.
