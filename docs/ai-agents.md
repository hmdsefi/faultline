# Use faultline with an AI coding agent

This guide shows how an AI coding agent finds a distributed-systems bug with faultline. The agent
checks its fix against the seed that failed, and pins that seed as a regression test. The guide
covers the loop, which artifact file answers which question, and what v0.1.0 cannot do. It ends
with a block to paste into your agent's instruction file, which also holds the rules for code
inside the simulation.

## Why faultline suits an agent

An agent that changes distributed code meets two problems that unit tests leave open. Many of the
bugs appear only under rare orderings of messages, crashes and timeouts. And when a test does fail
once, a passing re-run proves nothing. faultline answers both inside `go test`:

- Every message gets a random delay, 1 to 5 ms on the default network, so two messages on a link
  can arrive out of order. Fault rules add crashes, partitions, lossy links and failed disk syncs.
  The code's own retries and timeouts run against these faults in virtual time.
- A failing seed replays the identical run. The agent sees the failure, changes the code, and runs
  the same seed again to check the fix.
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
the fix itself is a command to run or a file to read, so an agent can run the loop on its own.

### Reproduce

These steps confirm that the failure replays before any code changes.

1. Run the simulation test:

   ```sh
   go test -run '^TestKV$' .
   ```

   Each seed runs as a subtest. A failing seed fails its subtest and prints the failure, a replay
   command and, after `artifacts:`, the path of its artifact directory. faultline stops at the
   first failing seed unless `Options.KeepGoing` is set.

   Use the printed path, because it differs between machines. The default artifact root is
   `faultline-<uid>` in the system temporary directory: under `$TMPDIR` on macOS, and usually in
   `/tmp` on Linux. `FAULTLINE_ARTIFACTS=<dir>` moves the root, as [Environment
   variables](reference/environment.md#faultline_artifacts) describes.

2. Read `report.txt` in the artifact directory. It names the failed check, the virtual time and
   event number of the failure, the check's error message, and the replay command.
3. Run the replay command. For the failing seed of `TestKV`, the report prints this one:

   ```sh
   FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
   ```

   The replay fails with the same check, at the same virtual time and the same event number.

### Fix

These steps find the cause in the artifact and check the change against the same seed.

4. Find the cause. Read `timeline.txt` upward from the failure record, marked `!`, and
   `schedule.json` for the faults that ran. Each timeline line ends with `<-` and the number of the
   record that caused it. Lines marked `*` are the *causal slice*: the records that led to the
   failure, at most 200 by default. When the marked records do not explain the failure and the
   header line `slice:` ends with `truncated at cap 200`, read on upward past the marks.
   `faultline render -slice-cap 1000 <dir>` marks more records, and [The `faultline`
   command](reference/cli.md) shows how to install it.

   In the key-value example, with a server that acknowledged each put before the log sync, seed
   `0x287372ab06f1482e` failed this way. The timeline shows `n1` sending the acknowledgment for
   `k169` and crashing 0.3 ms later. The crash keeps none of the unsynced writes to `/wal`.

5. Fix the code under test. Never weaken or delete the check to make the seed pass.
6. Run the replay command again. It passes, with a warning that the test binary differs from the
   one that recorded the artifact. The warning is expected after a code change.

   To read the passing run's timeline, run the replay command with `FAULTLINE_TRACE=full` and
   another root in `FAULTLINE_ARTIFACTS`. A passing seed then writes an artifact too. Under the old
   root it would not, because a passing run never replaces a failing seed's artifact.

### Protect

These steps check every seed and keep the failing seed as a test.

7. Run every seed, then run every seed twice to check determinism:

   ```sh
   go test ./...
   FAULTLINE_CHECK_DETERMINISM=1 go test ./...
   ```

   Both print `ok` when they pass, and the determinism run prints nothing more. With `-v`, its test
   time is about twice as long, because every seed runs twice.

8. Pin the seed in a regression test that uses the same options and world function (the function
   passed to `faultline.Run`) as the failing test. The following test pins `0x287372ab06f1482e`:

   ```go
   // TestKVSeed287372ab runs the seed that lost an acknowledged put before the fix.
   func TestKVSeed287372ab(t *testing.T) {
       t.Setenv("FAULTLINE_SEED", "0x287372ab06f1482e")
       t.Setenv("FAULTLINE_SEED_LIST", "") // FAULTLINE_SEED_LIST and FAULTLINE_SEED cannot both be set
       faultline.Run(t, kvOptions, kvWorld)
   }
   ```

   The test does not replay the failing run, because a replay needs the same code ([Seeds and
   replay](how-it-works.md#seeds-and-replay) lists what it depends on). It runs the same seed and
   options against the fixed code, and fails again if the bug comes back.

9. If the fix changes when the code sends messages or fires timers, pin the faults too. Copy
   `schedule.json` from the artifact directory to `testdata/seed-287372ab-schedule.json`, and add
   this line before `faultline.Run`:

   ```go
   t.Setenv("FAULTLINE_SCHEDULE", "testdata/seed-287372ab-schedule.json")
   ```

   The test then applies exactly the recorded faults, at their recorded times, and starts no
   planner. Without the schedule, the planner decides the faults again against the fixed code. A
   fault aimed at a role, such as the leader, then hits whichever node holds the role at that
   moment.

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

The block in [Instructions for your agent](#instructions-for-your-agent) lists the rules, and [How
faultline works](how-it-works.md#the-determinism-rules-your-code-follows) explains each one.

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

## Instructions for your agent

Paste the following block into your agent's instruction file, such as `AGENTS.md` or `CLAUDE.md`.
The test names in it come from the example on this page; change them to match your repository.

````markdown
## faultline simulation tests

Tests that call `faultline.Run` run in a deterministic simulation: running a seed again replays the
identical run.

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
7. Add a regression test that pins the seed, with the same options and world function as the
   failing test, as in the following example. It must pass. If the fix changes when the code sends
   messages or fires timers, also copy the run's `schedule.json` to `testdata/` and set
   `FAULTLINE_SCHEDULE` to that path in the same test.

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
- reports failures with `w.Invariant`, `w.Final` or `w.Sim.Fail(err)`, never `t.Fatal`: a seed
  stopped by `t.Fatal` writes no artifact. `n.Sim()` is the same simulator as `w.Sim`, so a boot
  function calls `n.Sim().Fail(err)`. `w.T().Fatal` suits only setup errors in the world function.

Do not add `t.Parallel` to a test that calls `faultline.Run`: Run seeds `crypto/rand` for every
run, and a parallel test fails with a setup error.

faultline settings are `FAULTLINE_*` environment variables, not test flags. The [documentation
index for agents](https://raw.githubusercontent.com/hmdsefi/faultline/main/llms.txt) links every
page.
````

## Next steps

- [Architecture](architecture.md): the packages and why they are built this way.
- [The `faultline` package on pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline): the
  options, the `World` methods and the environment variables.
