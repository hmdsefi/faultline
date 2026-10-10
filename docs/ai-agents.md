# Use faultline with an AI coding agent

This guide shows how an AI coding agent finds a distributed-systems bug with faultline. The agent
proves its fix and pins the failing seed as a regression test. The guide covers the loop, which
artifact file answers which question, the rules for code inside the simulation, and what v0.1.0
cannot do. It ends with a block to paste into your agent's instruction file.

## Why faultline suits an agent

An agent that changes distributed code meets two problems that unit tests leave open. Many of the
bugs appear only under rare orderings of messages, crashes and timeouts. And when a test does fail
once, a passing re-run proves nothing. faultline answers both inside `go test`:

- Every run delays and reorders messages, and fault rules add crashes, partitions, lossy links and
  failed disk syncs. The code's own retries and timeouts run against these faults in virtual time.
- A failing seed replays the identical run. The agent sees the failure, changes the code, and
  replays the same seed to prove the fix.
- The seed then becomes a short regression test.
- Invariants are Go functions that run after every event. A property such as "no acknowledged write
  is lost" is something the agent executes and checks.
- Code inside the simulation gets time, randomness, messages and files only from the node API. The
  agent can see every input the code depends on.

## Prerequisites

You need Go 1.26 or newer, which `go version` reports, and a test that calls `faultline.Run`.
[Get started](getting-started.md) builds one, `TestKV`, and the commands on this page use it.
To check that the test is in the current package, list it:

```sh
go test -list '^TestKV$' .
```

```text
TestKV
ok  	example.com/kv	0.431s
```

## The loop

The loop has three stages: reproduce the failure, fix it, and protect the fix. Every step except
the fix itself is a command to run or a file to read. An agent can run the loop on its own.

### Reproduce

1. Run the simulation test:

   ```sh
   go test -run '^TestKV$' .
   ```

   Each seed runs as a subtest. A failing seed fails its subtest and prints the failure, a replay
   command and the path of its artifact directory. faultline stops at the first failing seed unless
   `Options.KeepGoing` is set.

2. Read `report.txt` in the artifact directory. It names the failed check, the virtual time and
   event number of the failure, the check's error message, and the replay command.
3. Run the replay command. For the failing seed of `TestKV`, the report prints this one:

   ```sh
   FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
   ```

   The replay fails with the same check, at the same virtual time and the same event number.

### Fix

4. Find the cause. Read `timeline.txt` upward from the failure record, and `schedule.json` for the
   faults that ran. In the key-value example, with a server that acknowledged each put before the
   log sync, seed `0x287372ab06f1482e` failed this way. The timeline shows `n1` sending the
   acknowledgment for `k169` and crashing 0.3 ms later. The crash keeps none of the unsynced
   writes to `/wal`.
5. Fix the code under test. Never weaken or delete the check to make the seed pass.
6. Run the replay command again. It passes.

### Protect

7. Run every seed, then run every seed twice to check determinism:

   ```sh
   go test ./...
   FAULTLINE_CHECK_DETERMINISM=1 go test ./...
   ```

8. Pin the seed in a regression test that uses the same options and world function (the function
   passed to `faultline.Run`) as the failing test. The following test pins `0x287372ab06f1482e`.

```go
// TestKVSeed287372ab replays the seed that lost an acknowledged put before the fix.
func TestKVSeed287372ab(t *testing.T) {
	t.Setenv("FAULTLINE_SEED", "0x287372ab06f1482e")
	t.Setenv("FAULTLINE_SEED_LIST", "") // FAULTLINE_SEED_LIST and FAULTLINE_SEED cannot both be set
	faultline.Run(t, kvOptions, kvWorld)
}
```

A seed replays a run exactly only with the same options, world function and code. After the fix
the code differs, so the pinned seed no longer replays the failing run event for event. It runs the
same seed, options and planner against the fixed code.

9. Optional: pin the faults too. Copy `schedule.json` from the artifact directory to
   `testdata/seed-287372ab-schedule.json`, and add this line before `faultline.Run`:

   ```go
   t.Setenv("FAULTLINE_SCHEDULE", "testdata/seed-287372ab-schedule.json")
   ```

   The test then applies exactly the recorded faults and starts no planner.

### Verify it works

Run the pinned test:

```sh
go test -run '^TestKVSeed287372ab$' .
```

```text
ok  	example.com/kv	0.325s
```

On the code from before the fix, the same test fails with the original failure.

## Which file answers which question

Each failing seed leaves one artifact directory. These are the questions an agent asks while it
debugs, and the file that answers each one:

- What failed, when, and how do I replay it? `report.txt`.
- Same question, for a script: `report.json`. It holds the failure's kind, check, signature and
  virtual time, the replay command with its environment, the versions, the options and the trace
  hash.
- What led up to the failure? `timeline.txt`. It marks the failure record and the records that led
  to it.
- Which faults ran, and when? `schedule.json`. Delete faults from a copy and replay it with
  `FAULTLINE_SCHEDULE` to find the ones the failure needs.
- What did each client ask for, and what did it get back? `history.jsonl`, when the test records
  operations with `w.History`.
- Which record matches a pattern? `trace.jsonl` holds every record of the run as one JSON object
  per line, for `grep` or `jq`.
- What chain of events caused the failure? `hb.mmd`, a Mermaid flowchart of the records that led to
  it.

`timeline.html` shows the same run as an interactive page for a person.

## Rules for code inside the simulation

Code inside the simulation is every boot function, message handler, timer callback, invariant and
final check. A seed replays exactly only when that code takes all its inputs from the simulation:

- Read time with `n.Now()` and wait with `n.After`, never `time.Now`, `time.Sleep`, timers or
  tickers.
- Draw randomness from `n.Rand()` or `w.Rand(label)`, never the package-level `math/rand`
  functions.
- Start no goroutines and use no `select`.
- Sort map keys before ranging over a map when the order changes behavior or output.
- Send messages with `w.Net` and keep files on the node's volume.
- Build all state inside the world function, never in package-level variables.

Two rules keep a failure replayable:

- Report failures with `w.Invariant`, `w.Final` or `w.Sim.Fail(err)`. A seed stopped by `t.Fatal`
  or `t.FailNow` writes no artifact.
- Never call `t.Parallel` in a test that calls `faultline.Run`. faultline seeds `crypto/rand` for
  every run, and Go does not allow that in a parallel test.

[How faultline works](how-it-works.md#the-determinism-rules-your-code-follows) explains the reason
for each rule. A broken rule that changes the run shows up as a determinism failure. The failure
names the first record where two runs of the seed differ.

## What v0.1.0 cannot do

v0.1.0 tests code written against faultline's node API: boot functions, `w.Net` for messages and
the node's volume for files. Plan an agent's work around these limits:

- It does not run existing code unchanged. Code that starts goroutines, uses `net` or opens files
  with `os` needs a port to the node API. A harness, such as the one for etcd/raft, does that port
  for one system.
- It does not shrink a failing run. To narrow one down, delete faults from `schedule.json` and
  replay the rest.
- It has no linearizability or isolation checker. Your invariants and final checks are the checks.
- It does not inspect code for the determinism rules. The determinism check finds their effects.
- It has one ready-made harness, for etcd/raft.

The [roadmap](https://github.com/hmdsefi/faultline/issues/167) lists what the later phases add.
Running existing goroutine code unchanged is planned for Phase 2, with no date.

## Instructions for your agent

Paste the following block into your agent's instruction file, such as `AGENTS.md` or `CLAUDE.md`.
The test names in it come from the example on this page; change them to match your repository.

````markdown
## faultline simulation tests

Tests that call `faultline.Run` run in a deterministic simulation: running a seed again replays the
identical run.

When a faultline test fails:

1. Open `report.txt` in the artifact directory that the failure printed. It names the failed check,
   the virtual time, the event number and the replay command.
2. Run the replay command. Confirm that it fails with the same check at the same virtual time and
   event number before you change any code.
3. Find the cause in `timeline.txt`: read upward from the failure record. `schedule.json` lists the
   faults that ran, and `history.jsonl` lists client operations.
4. Fix the code under test. Never weaken, skip or delete an invariant or final check to make a seed
   pass.
5. Run the replay command again. It must pass.
6. Run `go test ./...` and `FAULTLINE_CHECK_DETERMINISM=1 go test ./...`. Both must pass.
7. Add a regression test that pins the seed, with the same options and world function as the
   failing test, as in the following example. It must pass.

```go
func TestKVSeed287372ab(t *testing.T) {
	t.Setenv("FAULTLINE_SEED", "0x287372ab06f1482e")
	t.Setenv("FAULTLINE_SEED_LIST", "")
	faultline.Run(t, kvOptions, kvWorld)
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
- reports failures with `w.Invariant`, `w.Final` or `w.Sim.Fail(err)`, never `t.Fatal`.

Never call `t.Parallel` in a test that calls `faultline.Run`.
````

## Next steps

- [Architecture](architecture.md): the packages and why they are built this way.
- [The `faultline` package on pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline): the
  options, the `World` methods and the environment variables.
