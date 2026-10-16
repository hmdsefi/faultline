---
name: faultline
description: "Debug, replay and pin failures of faultline simulation tests (deterministic simulation testing of distributed Go code inside go test), and offer a faultline test for a flaky distributed test that has none. Use when a Go test of a distributed system (consensus, replication, a database, a queue, a cluster) is flaky or failed once and passed on re-run, whether or not it is a faultline test; when test output or a CI log shows a faultline seed (seed=0x...), a replay: line, an artifacts: path, report.txt or an artifact directory; when a test calls faultline.Run, faultline.RunSeeds, etcdraft.Test or etcdraft.TestSeeds; or when writing a new faultline simulation test."
when_to_use: "Example requests: TestKV failed once in CI and passed when I re-ran it; the replication test is flaky and I cannot reproduce it; why did seed 0x287372ab06f1482e fail; replay this seed; add a faultline test for the replication code."
---

# faultline

A faultline test runs a distributed system in virtual time, on a simulated network and disk, with
injected faults. Every random choice comes from one seed, so a failing seed replays the identical
run: the same check fails at the same virtual time and event number. faultline settings are
`FAULTLINE_*` environment variables, never test flags.

When `superpowers:systematic-debugging` or `superpowers:test-driven-development` is loaded, this
loop is their reproduce, root-cause and red-green steps for a faultline test. It needs neither.

A faultline test calls `faultline.Run`, `faultline.RunSeeds`, `etcdraft.Test` or
`etcdraft.TestSeeds`. For a flaky test that is not one, go to "A flaky test with no faultline test"
below.

## Fix a failing seed

1. **Get the failure.** A failing seed fails its subtest, `TestKV/seed=0x287372ab06f1482e` in this
   example, and prints:

   ```text
   faultline: invariant "acked puts survive" violated at t=5.873034010s (event 1455)
     k169=v169 was acknowledged, but n1 holds ""
   replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
   artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
   ```

   Without that output, for example after a failure seen once in CI, run the test:
   `go test -run '^TestKV$' .`. The seeds come from the test name, so a local run of the same
   code runs the same seeds as CI. A CI job with `FAULTLINE_EXPLORE=1` ran fresh seeds: take the
   seed and the replay command from its log.

   A faultline seed does not fail by chance: on the same code and Go minor version, it fails
   again on every run. So a re-run that passed ran other seeds (`-short` caps the count;
   `FAULTLINE_EXPLORE`, `FAULTLINE_BASE_SEED`, `FAULTLINE_SEED` and `FAULTLINE_SEEDS` change the
   list), ran changed code or another Go minor version, or the test is not deterministic. Check
   the last case with `FAULTLINE_CHECK_DETERMINISM=1 go test -run '^TestKV$' .`. Never tell the
   user that a random fault happened to miss the re-run.

2. **Triage in a subagent.** Give the `artifacts:` path, or the seed, to the `faultline:triage`
   agent and ask why the seed failed. It returns the failed check, the events that led to it, the
   replay command and one next step, in at most 20 lines. Do not read `trace.jsonl` or all of
   `timeline.txt` in this session: they hold every record of the run. Before you open an artifact
   file yourself, read [artifacts.md](artifacts.md).

3. **Reproduce.** Run the replay command (the user's `/faultline:replay <seed>` does the same). It
   must fail the same check at the same virtual time and event before you change any code. Any
   other result is a determinism problem: read [rules.md](rules.md).

4. **Fix the code under test**, following [rules.md](rules.md). Never weaken, skip or delete an
   invariant or final check to make a seed pass.

5. **Check the fix.**
   - Run the replay command: it passes. The warning `the test binary differs from the one that
     recorded the previous artifact` is expected after a code change.
   - Undo only the fix and run the replay command again: it fails the same check. Restore the fix.
     This proves the seed tests the fix.
   - `go test ./...` and `FAULTLINE_CHECK_DETERMINISM=1 go test ./...` both print `ok`.

6. **Pin the seed.** The user can run `/faultline:pin <seed>`. Otherwise add the test yourself,
   right after the failing test, with the arguments the failing test passes after `t`:

   ```go
   // TestKVSeed287372ab runs seed 0x287372ab06f1482e, which lost an acknowledged put.
   func TestKVSeed287372ab(t *testing.T) {
   	faultline.RunSeeds(t, kvOptions, kvWorld, 0x287372ab06f1482e)
   }
   ```

   - A test that calls `faultline.Run`: `faultline.RunSeeds`, as above. It needs
     `github.com/hmdsefi/faultline` v0.1.1 or later in `go.mod`; upgrade an older version only
     when the user agrees.
   - A test that calls `etcdraft.Test(t, cfg, opts)`: `etcdraft.TestSeeds(t, cfg, opts,
     0x287372ab06f1482e)`, when `go.mod` requires `github.com/hmdsefi/faultline/harness/etcdraft`
     v0.1.1 or later.
   - An older harness, or another helper that calls `faultline.Run`: the same helper call, after
     `t.Setenv("FAULTLINE_SEED", "0x287372ab06f1482e")` and `t.Setenv("FAULTLINE_SEED_LIST", "")`.

   If the fix changes when the code sends messages or fires timers, pin the faults too: copy the
   artifact's `schedule.json` to `testdata/seed-287372ab-schedule.json`, and add
   `t.Setenv("FAULTLINE_SCHEDULE", "testdata/seed-287372ab-schedule.json")` before the pin's call.

Done when the replay passes, fails again with the fix undone, both `go test` runs print `ok`, and
`go test -run '^TestKVSeed287372ab$' .` prints `ok`.

## Write a new simulation test

1. Read [Get started](https://raw.githubusercontent.com/hmdsefi/faultline/main/docs/getting-started.md)
   and copy the shape of its `TestKV`: an `Options` value, a test that calls
   `faultline.Run(t, opts, world)`, and a world function. The world function adds nodes with
   `w.AddServer` and `w.AddClient`, plans faults with `w.Plan(&fault.Random{...})`, and registers
   `w.Invariant` (checked after every event) and `w.Final` (checked when a run ends) checks.
2. Write the code under test on the node API, following [rules.md](rules.md).
3. Plant a known bug, confirm that a seed fails the check you meant to catch it, and remove the bug.

For etcd/raft, use `etcdraft.Test` or `etcdraft.Setup` from the
[harness guide](https://raw.githubusercontent.com/hmdsefi/faultline/main/docs/guides/etcd-raft.md).

Done when `go test ./...` and `FAULTLINE_CHECK_DETERMINISM=1 go test ./...` print `ok`, and the
planted bug made a seed fail.

## When there is no artifact directory

- `artifacts: off`: `FAULTLINE_ARTIFACTS=off` was set. Run the replay command without it.
- `artifacts: not written: <reason>`: fix what the reason names, then replay.
- `stopped by t.FailNow or t.Fatal ...; no artifacts were written`: code inside the run called
  `t.Fatal`. Its replay command still works. Report that failure with `w.Invariant`, `w.Final` or
  `w.Sim.Fail(err)` instead, and the next failure leaves an artifact.
- No seed ran: a setup error stopped the test. Its message names the variable or option to fix.

## A flaky test with no faultline test

Its failures do not replay from a seed. If the module already has a faultline test of the same
code, use the loop above with that test. Otherwise, answer the user's question about the flaky
test, and in the same reply:

- Say plainly what faultline v0.1.x cannot run unchanged: code that starts goroutines, uses `net`
  or opens files with `os` needs a port to the node API first ([rules.md](rules.md) has the rules
  for ported code). etcd/raft v3.7.0 runs through `harness/etcdraft` (`etcdraft.Test`), which does
  that port.
- Offer to write a faultline test of the behavior the flaky test checks, following "Write a new
  simulation test" above. Write it only when the user agrees.

Done when the reply states that limit for this code and offers the faultline test.

## Reference

- [artifacts.md](artifacts.md): read before you open an artifact file yourself, query
  `report.json`, find a seed's artifact directory, narrow a failure by deleting faults from
  `schedule.json`, or need the artifact of a passing seed.
- [rules.md](rules.md): read before you write or change code that runs inside the simulation, on a
  determinism failure or a `t.Parallel` setup error, or when you decide whether faultline can test
  a system.
- [The docs index for agents](https://raw.githubusercontent.com/hmdsefi/faultline/main/llms.txt):
  every page, for anything these files do not cover.
