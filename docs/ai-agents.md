# Use faultline with an AI coding agent

This guide shows how an AI coding agent finds a distributed-systems bug with faultline. The agent
checks its fix against the seed that failed, and pins that seed as a regression test. The guide
covers the loop, which artifact file answers which question, and what faultline cannot do. It ends
with a block to paste into your agent's instruction file, which also holds the rules for code
inside the simulation. The agent needs only a shell that runs `go test`, so the guide applies to
Claude Code, Codex, Cursor and other coding agents.

## Claude Code plugin

For Claude Code, the faultline plugin holds this guide as a skill, an agent and two commands. Add
its marketplace and install it from a Claude Code session:

```text
/plugin marketplace add hmdsefi/faultline
/plugin install faultline@faultline
```

From a shell, run `claude plugin marketplace add hmdsefi/faultline`, then
`claude plugin install faultline@faultline`. The plugin adds these parts:

- The `faultline` skill. Claude loads it when a distributed Go test is flaky or fails once, when a
  faultline test fails, when output shows a seed or an artifact directory, or when it writes a
  simulation test. It follows the loop on this page. For a flaky test with no faultline test yet, it
  offers to write one, in goroutine mode when the code starts goroutines, and says what the code
  must take as values first: its listener, dialer and filesystem.
- The `faultline:triage` agent. It reads an artifact directory and answers in at most 20 lines: the
  failed check, the events that led to it, the replay command and one next step, so the main
  session does not read `trace.jsonl`.
- `/faultline:replay <seed>`. It runs the seed's replay command and says whether the failure and the
  trace hash match the artifact.
- `/faultline:pin <seed>`. It adds a regression test next to the test that failed. The test calls
  `faultline.RunSeeds`, which needs faultline v0.1.1 or newer, or `etcdraft.TestSeeds` for a test
  that calls `etcdraft.Test`, which needs the harness module v0.1.1 or newer.

The plugin's version matches the faultline release it was written for. Claude Code leaves
auto-update off for this marketplace by default. Run `claude plugin update faultline@faultline` to
update, or turn on auto-update for the `faultline` marketplace on the **Marketplaces** tab of
`/plugin`. Other agents use the block in [Instructions for your agent](#instructions-for-your-agent)
instead.

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
- Code inside the simulation gets time, randomness, the network and files only from faultline: the
  node API in event mode, and `p.Net`, `p.FS` and the bubble's clock in goroutine mode. The agent
  can see every input the code depends on.

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
   passed to `faultline.Run`) as the failing test. `faultline.RunSeeds`, in faultline v0.1.1 or
   newer, runs only the seeds it lists. The following test pins `0x287372ab06f1482e`:

   ```go
   // TestKVSeed287372ab runs the seed that lost an acknowledged put before the fix.
   func TestKVSeed287372ab(t *testing.T) {
       faultline.RunSeeds(t, kvOptions, kvWorld, 0x287372ab06f1482e)
   }
   ```

   `RunSeeds` ignores `FAULTLINE_SEED` and the other variables that choose seeds, so the test runs
   its seed under any environment. If it fails, its replay command selects the seed's subtest with
   `-run` instead of setting `FAULTLINE_SEED`.

   The test does not replay the failing run, because a replay needs the same code ([Seeds and
   replay](how-it-works.md#seeds-and-replay) lists what it depends on). It runs the same seed and
   options against the fixed code, and fails again if the bug comes back.

   For a test that calls `etcdraft.Test`, pin the seed with `etcdraft.TestSeeds` (harness module
   v0.1.1 or newer), not `RunSeeds`, so the harness's defaults still apply.

9. If the fix changes when the code sends messages or fires timers, pin the faults too. Copy
   `schedule.json` from the artifact directory to `testdata/seed-287372ab-schedule.json`, and add
   this line before `faultline.RunSeeds`:

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
ok  	example.com/kv	0.316s
```

On the code from before the fix, the same test fails with the original failure.

## Goroutine-mode tests

A test with `Options.Mode` set to `faultline.ModeGoroutine` runs ordinary Go code as processes, as
[Test goroutine code](guides/goroutine-mode.md) shows. The loop is the same, except for these
points:

- Without the toolexec backend, the report says
  `determinism: level 3 (goroutine mode, best-effort replay)` below its `artifacts:` line. A replay usually fails the same way,
  but the Go runtime still picks goroutine order, map order and `select` cases. Build the tests
  through `faultline-toolexec`, as [Replay goroutine code exactly](guides/exact-replay.md) shows,
  and the report says `determinism: exact (toolexec backend)`.
- A test binary built through the wrapper prints replay commands with
  `-toolexec=faultline-toolexec`. Run them as printed: without the flag, the replay builds another
  binary and loses exact replay.
- A determinism failure of a goroutine-mode seed shows the first record where two runs differ, and
  when it can, the event before or during which the first run diverged. The usual causes are a
  direct `net` or `os` call, global `math/rand`, map order, `select`, or a coverage gap that
  `toolexec-gaps.txt` lists.

Pin a goroutine-mode seed with `faultline.RunSeeds`, the same way.

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
  `FAULTLINE_SCHEDULE` to find the ones the failure needs. Pass `-count=1`, so `go test` does not
  print a cached pass.
- What did each client ask for, and what did it get back? `history.jsonl`, when the test records
  operations with `w.History`.
- Which record matches a pattern? `trace.jsonl` holds every record of the run as one JSON object
  per line, for `grep` or `jq`.
- What chain of events caused the failure? `hb.mmd`, a Mermaid flowchart of the records that led to
  it.
- Which constructs did the toolexec backend leave unchanged? `toolexec-gaps.txt`, in the artifact
  of a binary built through `faultline-toolexec` that has coverage gaps.
- Why did a goroutine-mode run stop making progress? `stall.txt`, which the stall watchdog writes
  with the stacks of the blocked goroutines before it ends the test binary.

`timeline.html` shows the same run as an interactive page for a person.

## Rules for code inside the simulation

The block in [Instructions for your agent](#instructions-for-your-agent) lists the rules, and [How
faultline works](how-it-works.md#the-determinism-rules-your-code-follows) explains each one.

## What faultline cannot do

faultline tests code on its node API and, in goroutine mode, ordinary Go code that takes its
network and disk from a process. Plan an agent's work around these limits:

- It does not redirect direct calls to `net` or `os`. Code that dials, listens or opens files
  itself needs a listener, dialer or `fsx.FS` passed in before it runs in goroutine mode.
- Goroutine-mode seeds replay on a best-effort basis unless the tests are built through
  `faultline-toolexec`, and even then the standard library, the module cache and `sync/atomic` stay
  outside its control.
- It does not shrink a failing run. To narrow one down, delete faults from `schedule.json` and
  replay the rest.
- It has no linearizability or isolation checker. Your invariants and final checks are the checks.
- It does not inspect code for the determinism rules. The determinism check finds their effects.
- It has one ready-made harness, for etcd/raft.
- It has no TLS, IPv6, DNS or Unix sockets on the simulated network, and no links or file locks on
  the simulated disk.

The [roadmap](https://github.com/hmdsefi/faultline/issues/167) lists what the later phases add.

## Instructions for your agent

Paste the following block into your agent's instruction file, such as `AGENTS.md` or `CLAUDE.md`.
The test names in it come from the example on this page; change them to match your repository.

````markdown
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
func TestKVSeed287372ab(t *testing.T) {
	faultline.RunSeeds(t, kvOptions, kvWorld, 0x287372ab06f1482e)
}
```

A determinism failure means that two runs of one seed differed: code under test read an input from
outside the simulation, or kept state from an earlier run. Find the cause from the first differing
record in the report, and remove it.

If the replay command contains `-toolexec=faultline-toolexec`, keep that flag in every command you
run for that test, and run `faultline-toolexec prepare` once if the build asks for it.

Event-mode code inside the simulation (boot functions, message handlers, timers, invariants, final
checks):

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

Goroutine-mode code (processes added with `w.AddProcess` or `w.AddClientProcess`, with
`Options.Mode: faultline.ModeGoroutine`):

- reaches the network only through `p.Net` (or `netsim.HTTPClient(p.Net)`, `grpcsim.NewClient`) and
  files only through `p.FS`, never `net.Dial`, `net.Listen` or `os.Open`;
- may start goroutines and use `time.Sleep`, timers and contexts, which follow virtual time;
- draws randomness from `p.Rand()`;
- returns from the process function when `ctx` is done, and starts goroutines that may panic with
  `p.Go`;
- calls `p.Do` to touch the world (history, faults) from a process, and never holds a mutex across a
  blocking network or file call that a check also locks;
- never calls `os.Exit` or `log.Fatal`, which end the whole test binary.

Do not add `t.Parallel` to a test that calls `faultline.Run` or `faultline.RunSeeds`: both seed
`crypto/rand` for every run, and a parallel test fails with a setup error.

faultline settings are `FAULTLINE_*` environment variables, not test flags. The [documentation
index for agents](https://raw.githubusercontent.com/hmdsefi/faultline/main/llms.txt) links every
page.
````

## Next steps

- [Architecture](architecture.md): the packages and why they are built this way.
- [The `faultline` package on pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline): the
  options, the `World` methods and the environment variables.
