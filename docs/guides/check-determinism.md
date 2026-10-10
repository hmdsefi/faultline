# Check that a test is deterministic

A seed replays exactly only when every input of the run comes from the seed. This guide shows how to
turn on faultline's determinism check, how to read a determinism failure, and how to fix the usual
causes.

## Turn on the check

Set `FAULTLINE_CHECK_DETERMINISM=1` to run every passing seed twice and compare the two trace
hashes:

```sh
FAULTLINE_CHECK_DETERMINISM=1 go test ./...
```

To turn the check on in code, set `Options.CheckDeterminism`. The variable can turn the check on,
but it cannot turn off a check that the options turned on.

A failing seed runs twice even without the check, because faultline runs it again to write its
artifact. The check adds a second run to the seeds that pass. We recommend it in CI on every pull
request, as faultline's own CI does: [Run faultline in CI](ci.md) shows a workflow.

## Read a determinism failure

The following line is a typical mistake. The client from [Get started](../getting-started.md) draws
its wait before the next put from `math/rand/v2`, instead of from the node's random stream:

```go
		n.After(rand.N(40*time.Millisecond), "next", next)
```

The seed does not control that package's generator, so two runs of one seed wait for different
times. Every check in the test still passes, and so does a plain `go test ./...`. The determinism
check finds the problem:

```sh
FAULTLINE_CHECK_DETERMINISM=1 go test ./...
```

```text
--- FAIL: TestKV (0.05s)
    kv_test.go:14: faultline: running 50 seeds from base 0x75c66bf05fc438d4 (test name)
    --- FAIL: TestKV/seed=0x287372ab06f1482e (0.05s)
        faultline: determinism failure: two runs of seed 0x287372ab06f1482e gave trace hashes 0xc02e9a65ec9f18ec and 0x932f947dd3fe98ec
          first difference at record #25:
            run 3: #25 t=0.030798187s node=2#1 kernel.event cause=24 "next" id="9"
            run 4: #25 t=0.043346030s node=2#1 kernel.event cause=24 "next" id="9"
          common causes: state kept between runs in the same process (package-level variables, sync.Once, caches), map iteration order, global math/rand, wall-clock time, goroutines
          next step: fix the cause; until the seed gives the same run every time, the replay command may not reproduce this failure
        replay:    FAULTLINE_SEED=0x287372ab06f1482e FAULTLINE_CHECK_DETERMINISM=1 go test -v -run '^TestKV$' example.com/kv
        artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
    kv_test.go:14: faultline: stopping after failing seed 0x287372ab06f1482e; 49 of 50 seeds not run (set Options.KeepGoing to run all)
FAIL
FAIL	example.com/kv	0.363s
FAIL
```

The hashes and times in your output differ from these, because the bug makes every run different.
Read the failure line by line:

1. The headline gives the trace hashes of the first two runs of the seed. They differ, so the seed
   does not decide the whole run.
2. faultline then ran the seed twice more with every record kept, and compared runs 3 and 4 record
   by record. `first difference at record #25` names the first record where they differ.
3. Record 25 is a `kernel.event` on node 2, the client: its `next` timer. It fired at 0.030798187s
   in run 3 and at 0.043346030s in run 4. `cause=24` names the record that scheduled it.
4. The artifact holds run 3. Its `timeline.txt` shows record 24:

   ```text
       24  0.008869292s c1#1   history.complete   c1 ok put null id=1 process=c1 f=put status=ok output=null <-23
       25  0.030798187s c1#1   kernel.event       next id=9 <-24
   ```

   The client completed its first put and scheduled `next` from the acknowledgment handler. The
   delay it chose is the input that came from outside the seed.

The fix is to draw the wait from the node's stream again:

```go
		n.After(kernel.Uniform(n.Rand(), 0, 40*time.Millisecond), "next", next)
```

The replay command of a determinism failure that only the check found sets
`FAULTLINE_CHECK_DETERMINISM=1` too, so the replay runs the check again.

## A failing seed that does not replay

When a seed fails and its second run differs, faultline reports a determinism failure for that seed
and names the first run's failure inside it. The same client mistake, with the server that
acknowledges before it syncs, gives:

```text
        faultline: determinism failure: re-running seed 0x151ffa3c67c5dd90 gave trace hash 0x74b907c69e2d8f2e; the first run gave 0x5e46be47b878692f
          the first run failed: invariant "acked puts survive" violated at t=6.116646387s (event 1406)
            k162=v162 was acknowledged, but n1 holds ""
          first difference at record #28:
            run 2: #28 t=0.012717332s node=2#1 kernel.event cause=23 "next" id="10"
            run 3: #28 t=0.015000000s node=1#1 kernel.event cause=27 "flush" id="12"
```

The invariant failure may be real, but its replay command may not reproduce it. Fix the
nondeterminism first, then run the test again to find a seed that fails the same way every time.

## Common causes and fixes

Each of these makes a run depend on something other than its seed. The fix keeps the input inside
the simulation:

- Wall-clock time: `time.Now`, `time.Since`, `time.Sleep`, Go timers and tickers. Read time with
  `n.Now()` and wait with `n.After`.
- The package-level functions of `math/rand` and `math/rand/v2`. Draw from `n.Rand()`, or from
  `w.Rand(label)` in the world function.
- Ranging over a map when the order changes what the code does or prints. Sort the keys first, for
  example with `slices.Sorted(maps.Keys(m))`.
- Goroutines and `select`. Write the work as callbacks that `n.After` and `n.Post` schedule.
- State kept between runs: package-level variables, `sync.Once` and caches. faultline runs the world
  function once per run, so build all state inside it.
- Pointers and wall-clock times in log lines and check messages. faultline hashes every record's
  text, so a printed address changes the trace hash. Give each message type a `Describe() string`
  method, and print values, not pointers.

The first differing record usually points at the cause. A `kernel.log` record with different text
points at a log line. A timer that fires at different times points at the code that scheduled it.

## Parallel tests and crypto/rand

faultline seeds `crypto/rand` for every run, through Go's `testing/cryptotest`. Go does not allow
that in a parallel test, so a test that calls `t.Parallel` and then `faultline.Run` fails its seeds
with this setup error:

```text
        faultline: crypto seeding: testing: test using t.Setenv, t.Chdir, or cryptotest.SetGlobalRandom can not use t.Parallel (Run cannot be used in a parallel test; remove t.Parallel or set Options.NoCryptoSeed)
```

Remove `t.Parallel` from tests that call `faultline.Run`. `Options.NoCryptoSeed` turns the seeding
off instead, and allows a parallel test. Then `crypto/rand` in the code under test is no longer
deterministic, and the check reports it when that code reads from it.

## Verify it works

After the fix, run the check again. It passes:

```sh
FAULTLINE_CHECK_DETERMINISM=1 go test ./...
```

```text
ok  	example.com/kv	1.500s
```

## Next steps

- [How faultline works](../how-it-works.md#the-determinism-rules-your-code-follows): the rules for
  code inside the simulation, and why each one matters.
- [Run faultline in CI](ci.md): run the check on every pull request.
- [Replay a failing seed](replay-a-seed.md): replay a seed once it is deterministic.
