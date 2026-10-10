# Replay a failing seed

A failing seed replays the identical run, so you can debug a failure as often as you need and test a
fix against it. This guide shows how to run the replay and confirm that it is the same run. It also
covers the replay warnings, and how to narrow a failure down by editing its fault schedule.

The examples use `TestKV` from [Get started](../getting-started.md), with the server that
acknowledges a put before it syncs the log.

## Run the replay command

Every failing seed prints a replay command, and `report.txt` in its artifact directory holds the
same line:

```text
replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
```

Copy the command and run it from any directory inside your module. It names the package by its
import path and sets `FAULTLINE_SEED` to run only the failing seed. `-v` makes `go test` print the
seed's output even when it passes.

```sh
FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
```

```text
=== RUN   TestKV
    kv_test.go:14: faultline: FAULTLINE_SEED=0x287372ab06f1482e: running 1 seed
=== RUN   TestKV/seed=0x287372ab06f1482e
    faultline: invariant "acked puts survive" violated at t=5.873034010s (event 1455)
      k169=v169 was acknowledged, but n1 holds ""
    replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
    artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
--- FAIL: TestKV (0.04s)
    --- FAIL: TestKV/seed=0x287372ab06f1482e (0.04s)
FAIL
FAIL	example.com/kv	0.488s
FAIL
```

A script can read the command from `report.json` instead. `replay.command` holds the command line,
and `replay.env` holds its environment variables:

```sh
jq -r .replay.command /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/report.json
```

```text
FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
```

## Confirm that it is the same run

A replay that fails the same check at the same virtual time and event number is the same failure. To
compare the whole run, compare the *trace hash*, a hash of every record the run emitted.
`report.json` holds it in `run.trace_hash`:

```sh
jq -r .run.trace_hash /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/report.json
```

```text
0x34d831a670e30100
```

Read it before the replay and again after it, because the replay writes a new artifact in the same
place. Equal hashes mean an identical run.

faultline makes the same comparison on its own. It runs every failing seed a second time to write
the artifact, and checks that both runs have the same trace hash. If they differ, the seed reports a
determinism failure, which also names the check that failed in the first run. [Check that a test is
deterministic](check-determinism.md) explains what to do then.

## Read the replay warnings

A replay is exact while four things stay the same: the faultline version, the Go minor version, your
code and the options. `report.json` records all four. A replay with `FAULTLINE_SEED` reads the
report that the seed left in its artifact directory, unless artifacts are off. faultline then prints
a warning for each difference, such as these:

```text
    warning: previous artifact was recorded with go1.26; this run uses go1.27
    warning: the test binary differs from the one that recorded the previous artifact (code, dependencies or build flags changed)
    warning: options differ from the previous artifact: Options.Duration was 10s, now 20s (options hash 0x5603791a69c69e0d, now 0xa7bd52e853f1e412)
```

A warning means the replay may not be the same run. If it still fails the same check at the same
time and event, carry on. If it does not, go back to the Go version and the code that the report
names. After you change the code, the test binary warning is expected.

## Replay one seed by its subtest name

Each seed runs as a subtest named `seed=0x` plus 16 hex digits. A `-run` pattern can select one seed
of the normal seed list, without `FAULTLINE_SEED`:

```sh
go test -run '^TestKV$/^seed=0x287372ab06f1482e$' .
```

The seed gets the same run either way. This form suits an editor's "run subtest" action. It does not
compare the run with the seed's previous artifact, so it prints no replay warnings.

## Step through a replay in a debugger

A debugger runs the test binary, so set `FAULTLINE_SEED` in the debugger's environment and pass the
same test pattern. The following commands build a test binary without optimizations, as a debugger
does, and run it:

```sh
go test -c -gcflags='all=-N -l' -o kv.test .
FAULTLINE_SEED=0x287372ab06f1482e ./kv.test -test.run '^TestKV$' -test.v
```

The run fails at the same virtual time and event, with the same trace hash. Only the test binary
warning appears, because the build flags changed. A breakpoint does not change the run: virtual time
moves only when the event loop runs the next event.

## Get the full trace of a passing seed

A passing seed writes no artifact by default. Set `FAULTLINE_TRACE=full` to get one, for example to
compare the run after a fix with the failing run. A passing artifact never replaces a failing one,
so give it its own root with `FAULTLINE_ARTIFACTS`:

```sh
FAULTLINE_ARTIFACTS=/tmp/kv-pass FAULTLINE_TRACE=full FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
```

```text
=== RUN   TestKV
    kv_test.go:14: faultline: FAULTLINE_SEED=0x287372ab06f1482e: running 1 seed
=== RUN   TestKV/seed=0x287372ab06f1482e
    faultline: seed 0x287372ab06f1482e passed: 680 events, ended at t=10.000000000s (deadline)
    artifacts: /tmp/kv-pass/example.com_kv/TestKV/287372ab06f1482e/
--- PASS: TestKV (0.02s)
    --- PASS: TestKV/seed=0x287372ab06f1482e (0.02s)
PASS
ok  	example.com/kv	0.885s
```

This output comes from the fixed server. The artifact directory holds the same files as a failing
seed's.

## Keep the seed as a regression test

Once the fix passes the replay, pin the seed in a test of its own, as [Get
started](../getting-started.md#pin-the-seed) shows. The test sets `FAULTLINE_SEED` with `t.Setenv`
and clears `FAULTLINE_SEED_LIST`. To pin the faults as well, copy `schedule.json` into `testdata/`
and set `FAULTLINE_SCHEDULE` to it in the same test.

## Narrow a failure by editing its schedule

A failing run can apply dozens of faults, and most of them may not matter to the failure. You can
delete faults from the run's fault schedule and replay the rest, until only the faults the failure
needs are left.

### Replay a schedule

`schedule.json` in the artifact directory lists every fault the run applied: its virtual time, its
kind and its node. A restart that ends a crash names the crash in `undoes`. Replay a schedule by
setting `FAULTLINE_SCHEDULE` next to `FAULTLINE_SEED`. faultline then applies exactly the faults in
the file and turns the planners off.

Replaying the unedited schedule gives the same failure at the same virtual time and event. The trace
hash differs: the replay has no records from the planner that chose the faults, and each fault
record names `replay` as its source.

### Delete faults one at a time

Seed `0x50304c12f019f421` of the same test loses a put after eight crashes:

```sh
FAULTLINE_SEED=0x50304c12f019f421 go test -run '^TestKV$' .
```

```text
--- FAIL: TestKV (0.05s)
    kv_test.go:14: faultline: FAULTLINE_SEED=0x50304c12f019f421: running 1 seed
    --- FAIL: TestKV/seed=0x50304c12f019f421 (0.05s)
        faultline: invariant "acked puts survive" violated at t=6.512504832s (event 1665)
          k195=v195 was acknowledged, but n1 holds ""
        replay:    FAULTLINE_SEED=0x50304c12f019f421 go test -v -run '^TestKV$' example.com/kv
        artifacts: /tmp/faultline-501/example.com_kv/TestKV/50304c12f019f421/
FAIL
FAIL	example.com/kv	0.343s
FAIL
```

To narrow it down:

1. Optional: copy the artifact directory. Every failing replay replaces the seed's artifact, so the
   copy keeps the original run.
2. Copy the schedule to a working file:

   ```sh
   cp /tmp/faultline-501/example.com_kv/TestKV/50304c12f019f421/schedule.json /tmp/narrow.json
   ```

3. Delete one fault from `/tmp/narrow.json`, and the event whose `undoes` names it. Leave `id` and
   `undoes` as they are: faultline numbers the events again when it reads the file.
4. Replay the seed with the edited schedule:

   ```sh
   FAULTLINE_SEED=0x50304c12f019f421 FAULTLINE_SCHEDULE=/tmp/narrow.json go test -run '^TestKV$' .
   ```

5. If the same check still fails, keep the deletion. If the run passes or fails another check, put
   the fault back.
6. Repeat with the next fault until none can be deleted.

Each replay also warns that the options differ from the previous artifact, because the schedule
changed. That warning is expected here.

For this seed, one pass over the eight crashes kept every deletion except crash 7. Deleting crash 7
made the run pass. The schedule that is left has one crash and its restart:

```json
{
  "faultline_schedule": 1,
  "end": "10s",
  "recovery": "7.5s",
  "events": [
    {"id": 7, "at": "3.432974247s", "kind": "crash", "node": "n1"},
    {"id": 8, "at": "3.889260138s", "kind": "restart", "node": "n1", "undoes": [7]}
  ]
}
```

The replay of that schedule still fails the same invariant:

```text
=== RUN   TestKV
    kv_test.go:14: faultline: FAULTLINE_SEED=0x50304c12f019f421: running 1 seed
    kv_test.go:14: faultline: FAULTLINE_SCHEDULE=/tmp/narrow.json: 2 events; planners are disabled
=== RUN   TestKV/seed=0x50304c12f019f421
    faultline: invariant "acked puts survive" violated at t=3.889260138s (event 1119)
      k137=v137 was acknowledged, but n1 holds ""
    replay:    FAULTLINE_SEED=0x50304c12f019f421 FAULTLINE_SCHEDULE=/tmp/narrow.json go test -v -run '^TestKV$' example.com/kv
    artifacts: /tmp/faultline-501/example.com_kv/TestKV/50304c12f019f421/
--- FAIL: TestKV (0.03s)
    --- FAIL: TestKV/seed=0x50304c12f019f421 (0.03s)
FAIL
FAIL	example.com/kv	0.341s
FAIL
```

The failure moved: it now loses `k137` at 3.889260138s instead of `k195` at 6.512504832s. A deleted
fault changes everything that happens after it, so an edited replay is a new run of the same bug.
Its artifact shows one crash instead of eight.

Not every seed narrows. Deleting any crash from seed `0x287372ab06f1482e` makes it pass. Without its
first crash, for example, the client is at `k185` when the last crash comes at 5.551909068s. The
server synced that put 3.7 ms before the crash.

### Errors in an edited schedule

faultline reads the file before any seed runs, and stops the test with a setup error when the file
is not a valid schedule. A deletion that leaves a comma after the last event gives this error:

```text
    kv_test.go:14: faultline: FAULTLINE_SCHEDULE=/tmp/narrow.json: fault: schedule: invalid JSON: invalid character ']' looking for beginning of value
```

A misspelled kind names the event by its position in the list:

```text
    kv_test.go:14: faultline: FAULTLINE_SCHEDULE=/tmp/narrow.json: fault: schedule: events[0]: unknown kind "crsh"
```

### When a schedule replay is not exact

A schedule replay applies all faults of an instant in one event at the start of that instant. Two
kinds of fault ran later than that in the original run, so they replay in a different order:

- a fault that your own code injected with `w.Faults.Inject` from a callback;
- a planner fault with a zero gap or zero duration, at an instant where a paused node resumed and
  its waiting events ran first.

## Next steps

- [Read a failure artifact](read-an-artifact.md): what each file in the artifact directory answers.
- [Check that a test is deterministic](check-determinism.md): what to do when a replay differs.
- [Environment variables](../reference/environment.md): every `FAULTLINE_*` variable.
