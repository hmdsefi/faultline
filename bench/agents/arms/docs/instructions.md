## faultline simulation tests

Tests that call `faultline.Run` or `faultline.RunSeeds` run in a deterministic simulation: running a
seed again replays the identical run.

When a faultline test fails:

1. Open `report.txt` in the artifact directory that the failure printed after `artifacts:`. It
   names the failed check, the virtual time, the event number and the replay command.
2. Run the replay command. Confirm that it fails with the same check at the same virtual time and
   event number before you change any code.
3. Find the cause in `timeline.txt`: read upward from the failure record. `schedule.json` lists the
   faults that ran, and `history.jsonl` lists client operations.
4. Fix the code under test. Never weaken, skip or delete an invariant or final check to make a seed
   pass.
5. Run the replay command again. It must pass; a warning that the test binary changed is expected.
6. Run `go test ./...` and `FAULTLINE_CHECK_DETERMINISM=1 go test ./...`. Both must pass.
7. Add a regression test that pins the seed with `faultline.RunSeeds` (faultline v0.1.1 or newer),
   as in the following example. Give it the same options and world function as the failing test.
   It must pass. For a test that calls `etcdraft.Test`, pin the seed with `etcdraft.TestSeeds`
   (harness module v0.1.1 or newer), not `RunSeeds`, so the harness's defaults still apply. If the
   fix changes when the code sends messages or fires timers, also copy the run's `schedule.json` to
   `testdata/`. Then set `FAULTLINE_SCHEDULE` to that path with `t.Setenv` in the same test.

```go
func TestLeaseKVSeed287372ab(t *testing.T) {
	faultline.RunSeeds(t, leaseOptions, leaseWorld, 0x287372ab06f1482e)
}
```

A determinism failure means that two runs of one seed differed: code under test read an input from
outside the simulation, or kept state from an earlier run. Find the cause from the first differing
record in the report, and remove it.

Code inside the simulation (boot functions, message handlers, timers, invariants, final checks):

- reads time with `n.Now()` and waits with `n.After`, never `time.Now`, `time.Sleep`, timers or
  tickers;
- draws randomness from `n.Rand()` or `w.Rand(label)`, never the package-level `math/rand`
  functions;
- runs as callbacks, with no goroutines and no `select`;
- sorts map keys before ranging over a map when the order changes behavior or output;
- sends messages with `w.Net` and keeps files on the node's volume (`w.Disk.Volume(n)`);
- keeps all state inside the world function passed to `faultline.Run`;
- reports failures with `w.Invariant`, `w.Final` or `w.Sim.Fail(err)`, never `t.Fatal`: a seed
  stopped by `t.Fatal` writes no artifact. `n.Sim()` is the same simulator as `w.Sim`, so a boot
  function calls `n.Sim().Fail(err)`. `w.T().Fatal` suits only setup errors in the world function.

Do not add `t.Parallel` to a test that calls `faultline.Run` or `faultline.RunSeeds`: both seed
`crypto/rand` for every run, and a parallel test fails with a setup error.

faultline settings are `FAULTLINE_*` environment variables, not test flags. The [documentation
index for agents](https://raw.githubusercontent.com/hmdsefi/faultline/main/llms.txt) links every
page.
