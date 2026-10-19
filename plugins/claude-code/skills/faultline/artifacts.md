# Reading a faultline artifact

A failing seed leaves one artifact directory, `<root>/<package>/<test>/<seed>/`: the package import
path with `/` written as `_`, the test name with `/` written as `__`, and the seed as 16 hex digits
without `0x`. The root is `$FAULTLINE_ARTIFACTS`, else `faultline-<uid>` in `$TMPDIR`, or in `/tmp`
when `TMPDIR` is unset. This command finds the directory of seed `0x287372ab06f1482e`:

```sh
find "${FAULTLINE_ARTIFACTS:-${TMPDIR:-/tmp}/faultline-$(id -u)}" -path '*/287372ab06f1482e*/report.json'
```

The `*` after the seed also matches the `-<hash>` that faultline appends when two tests map to the
same directory.

## Which file answers which question

- What failed, when, and how to replay it: `report.txt`, a few lines, plus the full stack for a
  panic.
- The same as data: `report.json`. Read single fields with `jq`:

  ```sh
  jq '{signature: .failure.signature, at: .failure.at, event: .failure.event, trace_hash: .run.trace_hash, replay: .replay.command}' report.json
  ```

  `failure.kind` is `invariant`, `final`, `panic`, `fail` (`w.Sim.Fail` or `t.Error`), `limit`
  (an event or time limit) or `determinism`. `replay.dir` is the module root and
  `replay.package_dir` the test package's directory. `warnings` lists what changed since the
  seed's previous artifact.
- What led up to the failure: `timeline.txt`, one line per record. Never read it whole. Read its
  header, the lines before the first record, then search it:

  ```sh
  grep -n '^!' timeline.txt         # the failure record
  grep -n '^\*' timeline.txt        # the causal slice: the records that led to the failure
  grep -n 'k169' timeline.txt       # records that name a key from the failure message
  ```

  Then read a few dozen lines around a hit. Each line is a mark (`!` failure, `*` causal slice),
  `SEQ`, the virtual time, the node and its incarnation (`n1#6` is the sixth boot of `n1`), the
  kind, the text, and `<-` with the `SEQ` of the record that caused it. The slice holds at most 200
  records by default. When the header's `slice:` line ends with `truncated at cap 200` and the
  marked records do not explain the failure, read upward past the marks, or run
  `faultline render -slice-cap 1000 <dir>` to mark more. The command installs with
  `go install github.com/hmdsefi/faultline/cmd/faultline@latest`.
- Which faults ran, and when: `schedule.json`.
- What each client asked for and got back: `history.jsonl`, when the test records operations with
  `w.History`.
- Records that match a pattern: `trace.jsonl`, a header line and then one JSON record per line, with
  `seq`, `at` (nanoseconds), `t`, `node`, `inc`, `kind`, `cause`, `text` and `attrs`. Search it with
  `grep` or `jq`, never read it whole:

  ```sh
  jq -c 'select(.kind == "fault.crash") | [.t, .text]' trace.jsonl
  ```

- The chain of records that caused the failure, as a graph: `hb.mmd`, a Mermaid flowchart.
- `timeline.html` shows the run as an interactive page for a person; point the user to it.

A record's kind starts with the part that emitted it: `kernel.`, `net.`, `disk.`, `fault.`,
`history.`, `run.`, `check.` (the failure record) or `etcdraft.`.

## Replay warnings

A replay with `FAULTLINE_SEED` compares the run with the seed's previous artifact and warns about
each difference: the faultline version, the Go version, the test binary, or the options. A warning
means the run may differ. If the replay still fails the same check at the same virtual time and
event, carry on. The test binary warning is expected after any code change.

## Narrow a failure by deleting faults

A failing run can apply many faults that the failure does not need. To keep only the ones it needs:

1. Copy the artifact directory: every failing replay replaces it.
2. Copy `schedule.json` to a working file, such as `/tmp/narrow.json`.
3. Delete one fault from the working file, and the event whose `undoes` names it. Leave the `id` and
   `undoes` values as they are.
4. Replay with the edited schedule, with `-count=1`:
   `FAULTLINE_SEED=0x287372ab06f1482e FAULTLINE_SCHEDULE=/tmp/narrow.json go test -count=1 -run '^TestKV$' .`
   `go test` does not check files outside the module for changes, so without `-count=1` it can print
   the cached pass of an earlier edit. From faultline v0.1.2, the replay command of a schedule
   replay passes `-count=1` too.
5. If the same check still fails, keep the deletion. Otherwise put the fault back.
6. Repeat until no fault can be deleted.

A schedule replay turns the planners off, and its trace hash differs from the original run's even
with the file unedited. A deleted fault changes everything after it, so an edited replay is a new
run of the same bug, at another time. Each replay warns that the options changed; that is expected.

## The artifact of a passing seed

A passing seed writes an artifact only with `FAULTLINE_TRACE=full`, and never replaces a failing
seed's artifact. Give it another root:

```sh
FAULTLINE_ARTIFACTS=/tmp/kv-pass FAULTLINE_TRACE=full FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
```

Use it to compare the run after a fix with the failing run.
