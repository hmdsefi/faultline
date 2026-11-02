# Limitations and FAQ

This page lists what faultline does not do, the issues known at release, and how faultline relates
to other tools that test distributed systems. Read it before you decide whether faultline fits your
system.

## What faultline tests

faultline runs code under test in one of two modes:

- In event mode, the default, code is written against faultline's node API. It runs as callbacks on
  one goroutine: boot functions, message handlers and timers. It sends messages with `w.Net` and
  keeps files on the node's simulated volume. Every seed replays exactly. etcd/raft v3.7.0 runs this
  way through `harness/etcdraft`.
- In goroutine mode, ordinary Go code runs as processes inside a `testing/synctest` bubble. It can
  start goroutines, sleep, serve `net/http` or gRPC over simulated TCP, and write files on the
  simulated disk. A seed replays on a best-effort basis, or exactly when the test binary is built
  through `faultline-toolexec` and `Options.Procs` keeps its default, 1.

Goroutine mode needs the code to take its listener, dialer and filesystem as values: `p.Net` and
`p.FS` in the test, `net.Listen` and `fsx.OS` in production. A direct `net.Dial` or `os.Open` does
real I/O outside the simulation. [Test goroutine code](guides/goroutine-mode.md) shows the change.

## Who faultline is not for

faultline fits a Go team that builds a distributed system and wants failures it can replay. It is
the wrong tool in these cases:

- You want to test the system as deployed, with real binaries, a real network and real disks.
  faultline simulates all of them inside one test process.
- You want performance numbers. Computation takes zero virtual time, so a run says nothing about CPU
  cost or throughput.
- Your tests call `t.Parallel`. faultline seeds `crypto/rand` for every run, which Go does not allow
  in a parallel test. In event mode, `Options.NoCryptoSeed` lifts the restriction, and `crypto/rand`
  is then no longer part of the replay. A goroutine-mode test never runs in parallel.

## What faultline does not include

The [roadmap](https://github.com/hmdsefi/faultline/issues/167) plans these for later phases, with no
dates:

- Shrinking a failing run to its smallest form. You delete faults from a copy of `schedule.json`
  and replay it instead.
- Linearizability and isolation checkers. Your invariants and final checks are the checks.
- Running thousands of seeds in parallel with a `faultline sweep` command. `faultline.Run` runs one
  seed after another.
- The `faultline view` web app. `timeline.html` is the run view.
- Harnesses for hashicorp/raft, CometBFT, Kubernetes controllers (controller-runtime), and NATS with
  JetStream.

## What your code must do

faultline controls time, scheduling, the network, the disk and its own random streams. It cannot see
the rest of your Go code. Event-mode code reads time and randomness from the node and starts no
goroutines. Goroutine-mode code reaches the network and disk only through its process. In both
modes, code sorts map keys when the order matters, unless the toolexec backend orders them, and
keeps no state between runs. [How faultline
works](how-it-works.md#the-determinism-rules-your-code-follows) lists the rules.

faultline does not inspect your code for these rules; it detects their effect on the trace. When a
broken rule changes the trace, `FAULTLINE_CHECK_DETERMINISM=1` reports the first record where two
runs of a seed differ. A broken rule that changes nothing in the trace goes unnoticed.

The error text of a check is part of the trace too, so keep pointer addresses and wall-clock times
out of it. Report failures with `w.Invariant`, `w.Final` or `w.Sim.Fail`: a seed stopped by
`t.Fatal` or `t.FailNow` writes no artifact.

## How far a replay reaches

A seed replays the same run only while the things that [Seeds and
replay](how-it-works.md#seeds-and-replay) lists stay the same. A replay with `FAULTLINE_SEED` warns
when one of them changed.

In goroutine mode, the Go runtime still decides some choices unless you build the tests through
`faultline-toolexec`. Even then, these can make two runs differ:

- code outside the rewrite set, such as the standard library and the module cache;
- `sync/atomic` operations and data races;
- goroutines woken by timers that fire at the same virtual instant.
 [Replay goroutine code exactly](guides/exact-replay.md#what-exact-replay-does-not-cover)
lists these cases. The determinism check reports every difference that reaches the trace.

Across machines, faultline's CI checks that a set of scenarios gives the same trace hashes on the
platforms and Go versions that
[Architecture](architecture.md#how-faultline-checks-its-own-determinism) lists. Other platforms are
not checked.

## Known issues

These are the issues known at release:

- A replay with `FAULTLINE_SCHEDULE` fails with the same check, at the same virtual time and event
  number. Its trace hash differs: the replay has no planner records, and its fault records name
  `replay` as their source.
- Deleting a fault from a schedule changes the run from that point on. An edited replay is a
  different run, not a shorter copy of the original, so it can pass where you expected a failure.
- `go test` does not check files outside your module for changes. After you edit one, the same
  command can print the old file's cached pass. This affects a `FAULTLINE_SEED_LIST` `@file` list,
  and a `FAULTLINE_SCHEDULE` file in a command you type yourself. Add `-count=1` to such a command.
  The replay command that faultline prints for a schedule replay includes it.
- In the etcd/raft harness, every raft panic has the same signature,
  `panic:github.com/hmdsefi/faultline/harness/etcdraft.(*raftLogger).panic`. Raft raises its
  assertions through the harness's logger, so tell raft panics apart by their message.
- The `at` field of `trace.jsonl` is an integer number of nanoseconds. JavaScript loses precision
  above 2^53 nanoseconds, about 104 days of virtual time. Go readers are exact.
- Under the toolexec backend, goroutines woken by timers of the same virtual instant run in the Go
  runtime's random order. The callbacks of `time.AfterFunc` and `context.AfterFunc` start in the
  order they were registered. A woken goroutine can still make a seed diverge when, before its next
  scheduling point, it starts a goroutine, registers such a callback, draws from `math/rand`,
  ranges over a map or calls a `sync.Once` first. The determinism check reports it.
- Under the toolexec backend with the default rewrite set, `net/http`'s own goroutines and timers
  are outside the scheduler. In faultline's tests, an HTTP program with three client goroutines
  diverged in about 1 of 2,500 seed comparisons. `FAULTLINE_TOOLEXEC_STD=net/http` removed the
  divergence, at about three times the run time.
- `go test -cover` instruments sources in a temporary directory, which the toolexec backend does
  not rewrite. A coverage build is therefore not exact; it records a coverage gap for each such
  package.

## Questions

Short answers about using faultline, and about how it relates to other tools.

### Can my coding agent run faultline on its own?

Yes. From a failing seed to a pinned regression test, every step except the fix itself is a command
to run or a file to read. `report.json` holds the failure, the replay command and the trace hash as
data. `FAULTLINE_SEED_LIST` and `FAULTLINE_RESULTS` let a script run a set of seeds and collect one
JSON line per seed. [Use faultline with an AI coding agent](ai-agents.md) walks through the loop.

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
makes the other trade. It runs inside `go test` on your machine and needs no infrastructure, but
your code uses its node API, or takes its network and disk from a process in goroutine mode.

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
documentation says to use a fake network implementation. faultline's goroutine mode runs every run
inside a `testing/synctest` bubble and adds the rest a distributed test needs: a simulated network
and disk, nodes that crash and restart, faults, and one seed for the choices faultline controls.
Event mode does not
use `testing/synctest`.

### Can I use the kernel without faultline.Run?

Yes. Package `kernel` is a plain Go API: `kernel.New` creates a simulator, and you drive it with
`Run`, `RunUntil` or `RunFor`. Without `faultline.Run` you give up the seed list, the subtests, the
checks, the second run of failing seeds and the artifacts.

### Why does `go get` say the module "does not contain package"?

`go get` of a nested module at an explicit version can fail when your `go.mod` already requires
`github.com/hmdsefi/faultline`. You see it only while the module cache does not hold the nested
module yet, as on a new machine or a clean CI runner:

```sh
go get github.com/hmdsefi/faultline/harness/etcdraft@v0.2.0
```

```text
go: module github.com/hmdsefi/faultline@v0.2.0 found, but does not contain package github.com/hmdsefi/faultline/harness/etcdraft
```

`harness/etcdraft`, `shims/grpcsim` and `runner/toolexec`, which holds the `faultline-toolexec`
wrapper, are nested modules with their own version tags, such as `harness/etcdraft/v0.2.0`. When the
error appears, the go command has searched only the root module. It never asked the module proxy
for the nested one.

Leave the version off, and `go get` adds the nested module's latest release:

```sh
go get github.com/hmdsefi/faultline/harness/etcdraft
```

To add a specific release, download the nested module into the module cache first. For the wrapper,
download `runner/toolexec`, then add the wrapper with `go get -tool`. The following commands add the
harness and the wrapper at v0.2.0:

```sh
go mod download github.com/hmdsefi/faultline/harness/etcdraft@v0.2.0
go get github.com/hmdsefi/faultline/harness/etcdraft@v0.2.0
go mod download github.com/hmdsefi/faultline/runner/toolexec@v0.2.0
go get -tool github.com/hmdsefi/faultline/runner/toolexec/cmd/faultline-toolexec@v0.2.0
```

### Does faultline make network calls?

No. `shims/netsim` implements `net.Conn` and `net.Listener` on the simulated network and never
opens a socket, and `grpcsim` and the `net/http` helpers dial through it. `timeline.html` loads
nothing from the network. A run happens inside the test process. Code under test that calls
`net.Dial` itself does open a real connection, outside the simulation.

### How fast is it?

Virtual time skips the idle time between events, so a run costs only the CPU time of its callbacks.
On an Apple M4 Max, a 60-second run of a three-node etcd/raft cluster under faults took 68 ms on
average, as [Results of 40,000 seeds](guides/etcd-raft.md#results-of-40000-seeds) reports. On the
same machine, the 50 seeds of the key-value example's `TestKV`, 10 seconds of virtual time each, run
in about one second. A failing seed runs twice, once more to write its artifact.

Goroutine mode runs real goroutines, so a run costs more. The 100 seeds of the HTTP store in [Test
goroutine code](guides/goroutine-mode.md), 30 seconds of virtual time each, run in about one second
on the same machine. The toolexec backend made faultline's own goroutine-mode tests take 1.01 to
1.61 times as long, and 3.12 times with `net/http` rewritten; [Replay goroutine code
exactly](guides/exact-replay.md#cost) gives the numbers.

### Why is it called faultline?

A fault line is a crack in the earth's crust. Stress builds up there for years without any sign,
and then it is released all at once in an earthquake. Bugs in distributed systems often behave the
same way. In software, a "fault" is also the usual word for something going wrong, like a crashed
server or a lost message. faultline helps you find those weak spots in a test instead of in
production.

## Next steps

- [Use faultline with an AI coding agent](ai-agents.md): the loop from a failing seed to a pinned
  regression test.
- [How faultline works](how-it-works.md): virtual time, the scheduler, the network, the disk and
  replay.
- [Environment variables](reference/environment.md): seeds, replay and checks without code changes.
