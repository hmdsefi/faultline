# Code inside the simulation

Code inside the simulation is every function faultline calls during a run: the world function,
boot functions, message handlers, timers, invariants and final checks. faultline controls time,
scheduling, the network, the disk and its own random streams. It cannot see the rest of your Go
code, so that code follows these rules:

- Read time with `n.Now()` and wait with `n.After(d, label, fn)`. Never call `time.Now`,
  `time.Sleep` or `time.After`, and never use Go timers or tickers.
- Draw randomness from `n.Rand()` or `w.Rand(label)`, for example
  `kernel.Uniform(n.Rand(), 0, 40*time.Millisecond)`. Never call the package-level functions of
  `math/rand` or `math/rand/v2`.
- Run as callbacks: start no goroutines and use no `select`.
- Sort map keys before you range over a map whenever the order can change what the code does or the
  text it produces, for example with `slices.Sorted(maps.Keys(m))`.
- Send messages with `w.Net.Send` and receive them with `w.Net.Handle`. Keep files on the node's
  volume, `w.Disk.Volume(n)`; a write is durable only after a successful `Sync`.
- Build all state inside the world function. Package-level variables, `sync.Once` and caches carry
  state from one run into the next.
- Give each message type a `Describe() string` method, so the trace shows what the message holds.
- Report failures with `w.Invariant`, `w.Final` or `w.Sim.Fail(err)`; a boot function calls
  `n.Sim().Fail(err)`. A seed stopped by `t.Fatal` or `t.FailNow` writes no artifact, so
  `w.T().Fatal` suits only setup errors in the world function.
- Keep pointer addresses and wall-clock times out of check messages: the message is part of the
  trace.

## The test function

- Do not call `t.Parallel` in a test that calls `faultline.Run` or `faultline.RunSeeds`. Both seed
  `crypto/rand` for every run, which Go does not allow in a parallel test, so the test fails with a
  setup error. `Options.NoCryptoSeed` allows `t.Parallel`, and `crypto/rand` is then no longer part
  of the replay.
- Choose seeds, the artifact root and the trace level with `FAULTLINE_*` environment variables, not
  test flags. The
  [environment reference](https://raw.githubusercontent.com/hmdsefi/faultline/main/docs/reference/environment.md)
  lists every variable. `Options` fields, such as `Seeds`, `Duration` and `KeepGoing`, are on
  [pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline#Options).

## Determinism failures

A failure whose headline starts with `faultline: determinism failure` means two runs of one seed
differed. Code under test read an input from outside the seed, or kept state from an earlier run. To
fix it:

1. Read the report's `first difference at record #N` line and the two versions of that record
   below it.
2. Find record N in `timeline.txt` and follow its `<-` cause to the code that made the choice.
3. Replace the outside input with the rules above. The usual causes are state kept between runs,
   map order, global `math/rand`, wall-clock time and goroutines.
4. Run `FAULTLINE_CHECK_DETERMINISM=1 go test ./...`: it prints `ok`.

faultline does not inspect code for these rules. A broken rule that changes nothing in the trace
goes unnoticed.

## What v0.1.x cannot do

- Run existing code unchanged. Code that starts goroutines, uses `net` or opens files with `os`
  needs a port to the node API first. `harness/etcdraft` does that port for etcd/raft v3.7.0, and is
  the only ready-made harness.
- Shrink a failing run. Delete faults from a copy of `schedule.json` and replay it instead
  ([artifacts.md](artifacts.md)).
- Check linearizability or isolation. The test's invariants and final checks are the checks.
- Measure performance: a callback takes zero virtual time.

The [roadmap](https://github.com/hmdsefi/faultline/issues/167) lists what later versions add.
