# Code inside the simulation

Code inside the simulation is every function faultline calls during a run: the world function,
boot functions, message handlers, timers, processes, invariants and final checks. faultline controls
time, scheduling, the network, the disk and its own random streams. It cannot see the rest of your
Go code, so that code follows these rules.

## Event mode

The default mode, `faultline.ModeEvent`, runs code written as callbacks on the node API:

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

## Goroutine mode

`Options.Mode: faultline.ModeGoroutine` (faultline v0.2.0 or later) runs processes added with
`w.AddProcess` or `w.AddClientProcess`, each a `func(ctx context.Context, p *faultline.Process) error`:

- Reach the network only through `p.Net` (`p.Net.Listen`, `p.Net.Dial`, `netsim.HTTPClient(p.Net)`,
  `grpcsim.Listen(p.Net, addr)`, `grpcsim.NewClient(p.Net, addr)`) and files only through `p.FS`,
  an `fsx.FS`. A direct `net.Listen`, `net.Dial` or `os.Open` does real I/O outside the simulation.
  Production code passes `net.Listen`, a `net.Dialer` and `fsx.OS(dir)` instead.
- Goroutines, `time.Sleep`, timers and context deadlines are fine: they follow virtual time.
- Draw randomness from `p.Rand()`. Without the toolexec backend, global `math/rand`, map order and
  `select` with several ready cases differ between runs.
- Return from the process function when `ctx` is done. Start goroutines that can panic with
  `p.Go`. Touch the world (history, faults, kernel) from a process only inside `p.Do(label, fn)`.
- Never hold a mutex across a blocking `p.Net`, `p.FS` or `p.Do` call while a check or another
  goroutine waits for it: the run stalls until the watchdog ends the test binary and writes
  `stall.txt`.
- Never call `os.Exit` or `log.Fatal`: they end the whole test binary.
- Checks run on the driver goroutine; read state that processes write under a mutex.

## The test function

- Do not call `t.Parallel` in a test that calls `faultline.Run` or `faultline.RunSeeds`. Both seed
  `crypto/rand` for every run, which Go does not allow in a parallel test, so the test fails with a
  setup error. In event mode, `Options.NoCryptoSeed` allows `t.Parallel`, and `crypto/rand` is then
  no longer part of the replay. A goroutine-mode test never calls `t.Parallel`.
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
   map order, global `math/rand`, wall-clock time and goroutines. In goroutine mode, also a direct
   `net` or `os` call, and goroutines woken together whose order changes what they send or write.
4. Run `FAULTLINE_CHECK_DETERMINISM=1 go test ./...`, with the same `-toolexec` flag the test
   uses: it prints `ok`.

A goroutine-mode message has a `determinism level 3` line, or under the toolexec backend a
`determinism: exact (toolexec backend)` line with the number of coverage gaps; `toolexec-gaps.txt`
in the artifact lists them, and they are the first places to look. Building a best-effort test
through `faultline-toolexec` takes goroutine order, map order, `select` and `math/rand` from the
seed: [Replay goroutine code exactly](https://raw.githubusercontent.com/hmdsefi/faultline/main/docs/guides/exact-replay.md).

faultline does not inspect code for these rules. A broken rule that changes nothing in the trace
goes unnoticed.

## What faultline cannot do

- Redirect direct calls to `net` or `os`. Code that listens, dials or opens files itself needs them
  passed in before it runs in goroutine mode. `harness/etcdraft` runs etcd/raft v3.7.0, and is the
  only ready-made harness.
- Replay every goroutine-mode seed exactly. Without the toolexec backend, replay is best-effort.
  With it, the standard library, the module cache, `sync/atomic` and goroutines woken by timers of
  one virtual instant stay outside its control, and the determinism check reports what diverges.
- Shrink a failing run. Delete faults from a copy of `schedule.json` and replay it instead
  ([artifacts.md](artifacts.md)).
- Check linearizability or isolation. The test's invariants and final checks are the checks.
- Measure performance: computation takes zero virtual time.

The [roadmap](https://github.com/hmdsefi/faultline/issues/167) lists what later versions add.
