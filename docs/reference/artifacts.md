# Artifact files

A failing seed leaves an *artifact directory*: the files you, a script or an AI coding agent read to
debug the failure. This page lists where the directory goes, every file in it, the format version of
each file and the fields a program reads. To see which file answers which question while you debug,
read [Use faultline with an AI coding agent](../ai-agents.md#which-file-answers-which-question).

## The artifact directory

faultline writes an artifact directory for every failing seed, from a second run of the seed that
keeps every record. With `FAULTLINE_TRACE=full` it writes one for passing seeds too. It writes none
when artifacts are off, for a setup error, or when a seed stops through `t.Fatal`, `t.FailNow` or
`t.SkipNow`.

The path is `<root>/<package>/<test>/<seed>`:

```text
/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
```

- `<root>` is the artifact root that [`FAULTLINE_ARTIFACTS`](environment.md#faultline_artifacts)
  sets, by default `faultline-<uid>` in the system temporary directory.
- `<package>` is the import path of the test package, with `/` written as `_`.
- `<test>` is the name of the test that called `faultline.Run`, with `/` written as `__`.
- `<seed>` is the seed as 16 lowercase hex digits, without `0x`.

In the package and test names, every byte other than a letter, a digit or one of `.-_+=@,~#` becomes
`_`. A name longer than 100 bytes keeps its first 83 bytes, then `-` and a 16-digit hash of the full
name. Two names can map to the same folder this way. When the folder already holds another test's
artifact, faultline adds `-` and a 16-digit hash of the package and test names to the path. Both
artifacts stay.

A new artifact replaces an older one of the same test and seed as a whole. faultline writes it in a
temporary folder and renames it into place. It never replaces a failing seed's artifact with a
passing one. It refuses to replace a symbolic link or a folder without `report.json`, and the
failure then prints `artifacts: not written:` and the reason.

## The files

An artifact directory holds these files:

| File | Written | Format version | Content |
|---|---|---|---|
| `report.json` | always | `faultline_report` 1 | the report as data: failure, replay command, versions, options, run summary |
| `report.txt` | always | none | the failure report as printed, with the full panic stack |
| `trace.jsonl` | always | `faultline_trace` 1 | every record of the run, one JSON object per line |
| `schedule.json` | always | `faultline_schedule` 1 | the fault schedule: every fault that ran |
| `history.jsonl` | when the test recorded client operations | `faultline_history` 1 | the client operations |
| `timeline.txt` | always | `faultline timeline v1` | the trace as aligned text |
| `hb.mmd` | always | `faultline causal slice v1` | the records that led to the failure, as a Mermaid flowchart |
| `timeline.html` | always | `faultline_timeline` 1 | an interactive page of the run |

A check can add [extra files](#extra-files). The `files` field of `report.json` lists every file
except `report.json` itself, with its type and format version:

```sh
jq -c '.files[]' report.json
```

```text
{"name":"report.txt","type":"report_text"}
{"name":"trace.jsonl","type":"trace","version":1}
{"name":"schedule.json","type":"schedule","version":1}
{"name":"history.jsonl","type":"history"}
{"name":"timeline.txt","type":"timeline_text","version":1}
{"name":"hb.mmd","type":"hb","version":1}
{"name":"timeline.html","type":"timeline_html","version":1}
```

`timeline.txt`, `hb.mmd` and `timeline.html` are derived from `report.json` and `trace.jsonl`.
[`faultline render`](cli.md#render) regenerates them.

## Versions and compatibility

Each versioned format names its version in its first field or its first line. faultline reads every
version up to its own and rejects a newer one:

```text
artifact: report.json: version 2 is newer than this faultline supports (1); upgrade faultline
```

faultline's readers ignore fields they do not know. Within a version, faultline may add optional
fields and new values, such as a failure kind or a record kind. Removing, renaming or retyping a
field raises the version. A script must ignore unknown fields, and treat an unknown kind as an
opaque string.

## report.json

`report.json` is one JSON object with two-space indents, and the file a script reads first. These
are the failure and the run summary of the key-value example's failing seed:

```sh
jq .failure report.json
```

```json
{
  "kind": "invariant",
  "check": "acked puts survive",
  "signature": "invariant:acked puts survive",
  "headline": "invariant \"acked puts survive\" violated at t=5.873034010s (event 1455)",
  "message": "k169=v169 was acknowledged, but n1 holds \"\"",
  "at_ns": 5873034010,
  "at": "5.873034010s",
  "event": 1455,
  "record_seq": 3629
}
```

```sh
jq .run report.json
```

```json
{
  "trace_hash": "0x34d831a670e30100",
  "events": 1455,
  "records": 3631,
  "dropped": 0,
  "end_ns": 5873034010,
  "end": "5.873034010s",
  "stop": "failed",
  "recovery_ns": 7500000000,
  "recovery": "7.500000000s",
  "planners": [
    "random"
  ],
  "attempts": 2
}
```

### Top-level fields

The top-level object has these fields, in this order:

| Field | Type | Meaning |
|---|---|---|
| `faultline_report` | integer | format version, `1` |
| `status` | string | `fail`, or `pass` for an artifact written under `FAULTLINE_TRACE=full` |
| `package` | string | import path of the test package |
| `test` | string | name of the test that called `Run` |
| `subtest` | string | the seed's subtest, such as `TestKV/seed=0x287372ab06f1482e` |
| `seed` | string | `0x` and 16 hex digits |
| `seed_source` | string | `derived`, `env` (`FAULTLINE_SEED`) or `list` (`FAULTLINE_SEED_LIST`) |
| `base_seed`, `base_source` | string | for derived seeds: the base, and where it came from (`test_name`, `options`, `env`, `explore`) |
| `seed_index` | integer | position of the seed in the seed list |
| `failure` | object | why the seed failed; absent when `status` is `pass` |
| `warnings` | array of strings | differences from the seed's previous artifact, and dropped extra files |
| `replay` | object | how to replay the seed |
| `versions` | object | what produced the artifact |
| `options` | object | the effective options, after the environment applied |
| `options_hash` | string | a hash of the options that change a run, including the replayed schedule |
| `run` | object | the run that wrote the artifact |
| `nodes` | array | the node table: `id`, `name` and `tags` of each node |
| `dir` | string | absolute path of the artifact directory |
| `files` | array | the file index: `name`, `type` and `version` of each file |

### failure

The `failure` object describes one failure:

| Field | Meaning |
|---|---|
| `kind` | `invariant`, `final`, `panic`, `fail`, `limit` or `determinism` |
| `check` | the check name; for `panic`, the function that panicked; `max-events` for `limit`; empty for `fail` and `determinism` |
| `signature` | `kind:check`; seeds that fail the same way share it |
| `headline` | the first line of the report |
| `message` | the check's error or the panic value; several lines for a determinism failure |
| `at_ns`, `at` | the virtual time of the failure, in nanoseconds and as text |
| `node`, `node_id` | the node the failure happened on, when faultline can tell |
| `event` | the number of events the run had executed |
| `record_seq` | the `seq` of the failure record in `trace.jsonl`, marked `!` in `timeline.txt` |
| `panic` | for a panic: `value`, `in` (`callback`, `invariant`, `final` or `body`), `name`, `site` and the full `stack` |
| `limit` | for `limit`: `name` (`MaxEvents`) and `value` |
| `finals` | for `final`: every failing final check, with `check`, `message` and `panic` |
| `determinism` | for `determinism`: `context`, the trace hash of each run in `hashes`, the first run's failure in `original`, and the first differing record pair in `diff` |

The kinds mean:

- `invariant`: a function registered with `w.Invariant` returned an error.
- `final`: a function registered with `w.Final` returned an error after the run.
- `panic`: a callback, an invariant, a final check or the world function panicked.
- `fail`: code called `w.Sim.Fail(err)`, or the test was marked failed with `t.Error` during the
  run.
- `limit`: the run executed more than `Options.MaxEvents` events.
- `determinism`: two runs of the seed gave different trace hashes.

### replay, versions and options

`replay.command` is the shell command that replays the seed, as the console prints it. `replay.env`
holds the variables it sets, and `replay.run` its `-run` pattern. To run the test another way,
`replay.dir` is the module root, `replay.package_arg` the package argument relative to it, and
`replay.package_dir` the test package's directory.

`versions` holds `faultline` (the faultline module version in the test binary), `go`, `goos`,
`goarch` and `test_binary_sha256`. A replay is exact only with the same faultline version, Go minor
version, code and options. A replay with `FAULTLINE_SEED` compares them with these fields and
`options_hash`, and warns about each difference.

`options` holds `seeds`, `duration_ns`, `duration`, `max_events`, `mode`, `trace` (`hash` or
`full`), `trace_buffer`, `check_determinism`, `keep_going`, `no_crypto_seed`, `allow_limit`, `net`
and `disk`. `net` and `disk` are the network and disk configurations as JSON. Under
`FAULTLINE_SCHEDULE`, `schedule` and `schedule_hash` name the replayed file.

### run

The `run` object describes the run that wrote the artifact:

| Field | Meaning |
|---|---|
| `trace_hash` | the trace hash of the run |
| `events` | events executed |
| `records` | records emitted |
| `dropped` | records missing from the start of `trace.jsonl`; a `limit` failure keeps the last 1,000,000 |
| `end_ns`, `end` | the virtual time the run stopped |
| `stop` | why it stopped: `failed`, `deadline` (it reached the end of the run), `max-events`, or `none` when it never started |
| `recovery_ns`, `recovery` | the start of the recovery window, when a `fault.Random` planner or a replayed schedule set one |
| `planners` | the planners the test registered: `random`, `script`, `none` |
| `attempts` | how many times the seed ran: 2 for a failure, 3 or 4 for a determinism failure |

## report.txt

`report.txt` is the failure report as the console prints it, under the subtest's `--- FAIL:` line.
For a panic it holds the full stack, where the console shows at most 40 lines:

```text
--- FAIL: TestKV/seed=0x287372ab06f1482e
    faultline: invariant "acked puts survive" violated at t=5.873034010s (event 1455)
      k169=v169 was acknowledged, but n1 holds ""
    replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
    artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
```

## trace.jsonl

`trace.jsonl` holds a header line, then one line per record in `seq` order. Each line is one JSON
object, so `grep` and `jq` work on it line by line:

```sh
grep '"seq":3629,' trace.jsonl
```

```json
{"seq":3629,"at":5873034010,"t":"5.873034010s","kind":"check.violation","cause":3628,"text":"invariant \"acked puts survive\" violated","attrs":[["kind","invariant"],["check","acked puts survive"],["error","k169=v169 was acknowledged, but n1 holds \"\""],["event","1455"]]}
```

The header line has these fields:

| Field | Meaning |
|---|---|
| `faultline_trace` | format version, `1` |
| `faultline_version`, `go_version` | the faultline and Go versions of the test binary |
| `package`, `test`, `subtest`, `seed` | as in `report.json` |
| `trace_hash` | the trace hash of the whole run |
| `records` | the number of record lines that follow |
| `dropped` | records emitted before the first line that follows |
| `nodes` | the node table: `id`, `name`, `tags` |

A record line has these fields:

| Field | Type | Meaning |
|---|---|---|
| `seq` | integer | 1-based position in the run |
| `at` | integer | virtual time in nanoseconds since the run started |
| `t` | string | `at` as text; readers ignore it |
| `node` | integer | the node's ID; absent for a global record (node 0) |
| `inc` | integer | the node's incarnation, which grows by one at every boot; absent when 0 |
| `kind` | string | the record kind, such as `net.send` |
| `cause` | integer | the `seq` of the record that caused this one; absent only for the first record |
| `text` | string | a short description; absent when empty |
| `attrs` | array | `[key, value]` pairs of strings, in order; absent when there are none |

`at` is a JSON integer. A JavaScript reader loses precision above 2^53 nanoseconds, about 104 days
of virtual time; Go readers are exact.

### Record kinds

The prefix of a kind names the part of faultline that emitted it:

| Prefix | Records |
|---|---|
| `kernel.` | the run's start (`start`), every executed event (`event`) and deferred event (`defer`), the node lifecycle (`add_node`, `boot`, `crash`, `pause`, `resume`, `clock_jump`, `clock_drift`), node logs (`log`), `panic` and `fail` |
| `net.` | messages (`send`, `send_raw`, `deliver`, `dup`, `defer`, and `drop` with a `reason` of `partition`, `partition-in-flight`, `loss`, `down` or `no-handler`), handlers (`handle`) and topology changes (`partition`, `isolate`, `cut`, `heal`, `heal_link`, `link_config`, `link_reset`) |
| `disk.` | file operations (`create`, `mkdir`, `write`, `write_durable`, `truncate`, `rename`, `remove`, `sync`, `sync_dir`), their failures (`sync_fail`, `sync_dir_fail`, `nospace`, `stale`), crash effects (`crash_apply`, `crash_meta`) and disk faults (`fail_syncs`, `capacity`, `corrupt`) |
| `fault.` | one record per applied fault, named after its kind with `_` for `-` (`fault.crash`, `fault.heal_link`), and planner records (`target`, `skip`, `recover`, `suppressed`, `error`) |
| `history.` | client operations: `invoke` and `complete` |
| `run.` | the run's phases (`phase`) and planners (`plan`) |
| `check.` | `violation`, the failure record |
| `etcdraft.` | records of the etcd/raft harness |

Code under test can emit its own kinds with `w.Sim.Emit`; the `kernel.` prefix is reserved.

### The trace hash

The trace hash is an FNV-1a 64-bit hash over a binary encoding of every record, not over the JSON
text. When `dropped` is 0, `kernel.HashRecords` recomputes it from the records that
`artifact.ReadTrace` returns, and the result equals the header's `trace_hash`.

## timeline.txt

`timeline.txt` shows every record of the trace as one aligned line, for reading in a terminal or
with `grep`. It starts with a header block:

```text
faultline timeline v1
test:     TestKV/seed=0x287372ab06f1482e
package:  example.com/kv
seed:     0x287372ab06f1482e
status:   fail
failure:  invariant "acked puts survive" violated at t=5.873034010s (event 1455)
records:  3631 (dropped 0), trace hash 0x34d831a670e30100
nodes:    1=n1 [server], 2=c1 [client]
slice:    200 records, root 3629, truncated at cap 200
legend:   ! failure record, * causal slice, <- cause
```

An empty line and a column header follow, then the records. These are the last lines of the example:

```text
* 3626 5.873034010s n1#6   fault.restart      restart n1 id=12 source=random node=n1 undoes=11 effect=applied <-3625
* 3627 5.873034010s n1#7   kernel.boot        boot <-3626
* 3628 5.873034010s n1#7   net.handle         handler installed on n1 node=n1 installed=true <-3627
! 3629 5.873034010s global check.violation    invariant "acked puts survive" violated kind=invariant check="acked puts survive" error="k169=v169 was acknowledged, but n1 holds \"\"" event=1455 <-3628
  3630 5.873034010s global kernel.fail        invariant "acked puts survive": k169=v169 was acknowledged, but n1 holds "" <-3629
  3631 5.873034010s global run.phase          end phase=end stop=failed events=1455 <-3630
```

The columns are a mark, `SEQ`, `TIME`, `NODE` and `KIND`, then the text. `NODE` is `global`, or the
node name and incarnation as `n1#7`. The text ends with each attribute as `key=value` and the cause
as `<-seq`. An attribute value is quoted when it holds a character outside `A-Za-z0-9._:/@%+,#|-`,
such as a space. Control characters are escaped, so each record stays on its line.

The mark `!` flags the failure record, and `*` the *causal slice*: the records that led to the
failure. faultline finds them by following, from the failure record, each record's cause and the
previous record of the same node incarnation, up to 200 records. The `slice:` line says when the cap
cut the slice short, and `faultline render -slice-cap` changes the cap.

## schedule.json

`schedule.json` records every fault that ran, with its virtual time, including the undo of each
fault, such as the restart that ends a crash. `FAULTLINE_SCHEDULE` replays it. This is the schedule
of the example:

```json
{
  "faultline_schedule": 1,
  "end": "10s",
  "recovery": "7.5s",
  "events": [
    {"id": 1, "at": "1.011425347s", "kind": "crash", "node": "n1"},
    {"id": 2, "at": "1.479910348s", "kind": "restart", "node": "n1", "undoes": [1]},
    {"id": 3, "at": "1.56992872s", "kind": "crash", "node": "n1"},
    {"id": 4, "at": "2.026566051s", "kind": "restart", "node": "n1", "undoes": [3]},
    {"id": 5, "at": "2.78777029s", "kind": "crash", "node": "n1"},
    {"id": 6, "at": "2.862679138s", "kind": "restart", "node": "n1", "undoes": [5]},
    {"id": 7, "at": "3.602924326s", "kind": "crash", "node": "n1"},
    {"id": 8, "at": "3.704492528s", "kind": "restart", "node": "n1", "undoes": [7]},
    {"id": 9, "at": "4.762963737s", "kind": "crash", "node": "n1"},
    {"id": 10, "at": "4.843233266s", "kind": "restart", "node": "n1", "undoes": [9]},
    {"id": 11, "at": "5.551909068s", "kind": "crash", "node": "n1"},
    {"id": 12, "at": "5.87303401s", "kind": "restart", "node": "n1", "undoes": [11]}
  ]
}
```

Durations are strings in Go's `time.Duration` format, measured from the start of the run. The
top-level fields are:

- `faultline_schedule`: the format version, `1`.
- `end` and `recovery`: optional. Under `FAULTLINE_SCHEDULE` they set the end of the run and the
  start of the recovery window.
- `events`: the faults, sorted by `at`.

Every event has `at` and `kind`, and exactly the fields its kind needs:

| Kind | Fields | Effect |
|---|---|---|
| `partition` | `groups` | splits the nodes into groups, such as `[["n1", "n2"], ["n3"]]` |
| `isolate` | `node` | removes every link to and from the node |
| `cut` | `node`, `peer` | removes the link from `node` to `peer` |
| `heal` | none | restores every link |
| `heal-link` | `node`, `peer` | restores the link from `node` to `peer` |
| `link` | `node`, `peer`, `link` | changes the configuration of that link |
| `link-reset` | `node`, `peer` | restores the configuration from before the first `link` |
| `crash` | `node` | crashes the node |
| `restart` | `node` | restarts the node if it is down |
| `pause` | `node` | pauses the node |
| `resume` | `node` | resumes the node if it is paused |
| `clock-jump` | `node`, `n` | moves the node's clock by `n` nanoseconds |
| `clock-drift` | `node`, `n` | sets the node's clock drift to `n` parts per million |
| `sync-fail` | `node`, `n` | makes the next `n` syncs on the node's volume fail; 0 clears it |
| `disk-capacity` | `node`, `n` | sets the volume's capacity to `n` bytes; 0 means unlimited |
| `corrupt` | `node`, `path`, `off`, `len` | damages up to `len` synced bytes of `path` at offset `off` |

Nodes are named, not numbered. The `link` object has the optional fields `latency`, `jitter` and
`tail` (durations), `tail_ppm`, `drop_ppm` and `dup_ppm` (integers from 0 to 4294967295), and `fifo`
(a boolean).

`id` and `undoes` are annotations: `id` numbers the events, and `undoes` lists the `id`s of the
faults an event ends. A replay ignores both, so you can delete events from a copy without
renumbering. faultline reads a schedule strictly. An unknown field, a missing field or a field the
kind does not take is a setup error. The error names the event, such as
`fault: schedule: events[2]: unknown field "nodes"`.

## history.jsonl

`history.jsonl` lists the client operations that the test recorded with `w.History.Invoke` and
`w.History.Complete`. Its first line is a header with the format version and the number of
operations. One line per operation follows, in invocation order. These are the first two lines and
the last line of the example:

```sh
sed -n '1,2p;$p' history.jsonl
```

```json
{"faultline_history":1,"ops":170}
{"id":1,"process":"c1","f":"put","status":"ok","call":0,"return":8869292,"call_index":1,"return_index":2,"input":"k1=v1","output":null}
{"id":170,"process":"c1","f":"put","status":"pending","call":5586225234,"return":0,"call_index":339,"return_index":0,"input":"k170=v170","output":null}
```

The fields of an operation:

| Field | Meaning |
|---|---|
| `id` | 1, 2, 3, and so on, in invocation order |
| `process` | the client that invoked it |
| `f` | the operation's name, such as `put` |
| `status` | `ok` (it took effect), `fail` (it did not), `info` (unknown, such as a timeout) or `pending` (never completed) |
| `call`, `return` | virtual times in nanoseconds; `return` is 0 while pending |
| `call_index`, `return_index` | the order of the history's events: every invocation and completion takes the next number, from 1; `return_index` is 0 while pending |
| `input`, `output` | the JSON values the test passed; `output` is `null` while pending |

Operation A *precedes* operation B when A's status is `ok` and A's `return_index` is less than B's
`call_index`. Use the indexes, not the times, to order operations: several events can share one
virtual time.

## hb.mmd

`hb.mmd` draws the causal slice as a Mermaid flowchart, for any Mermaid renderer. Its second line
records the slice:

```text
    %% faultline causal slice v1: root=3629 records=200 cap=200 truncated=true
```

Each node of the chart is one record, labeled with its `seq`, time, node and kind, and the first 40
characters of its text. An edge runs from a record's cause, or from the previous record of the same
node incarnation, to the record. The failure record has the class `violation`, faults `fault` and
messages `net`. faultline keeps the chart within Mermaid's default limits of 50,000 bytes and 500
edges: it halves the slice until the chart fits.

## timeline.html

`timeline.html` is one self-contained page that shows the run in a browser. The stylesheet, the
scripts and the data are inline, and its Content-Security-Policy is `default-src 'none'`, so it
loads nothing from the network.

The data sits in a `<script type="application/json" id="faultline-data">` element, as one JSON
object with these fields:

- `faultline_timeline`: the format version, `1`.
- `title`: the page title.
- `report`: the `report.json` object.
- `trace`: the `trace.jsonl` text of the records the page holds.
- `schedule`: the `schedule.json` text.
- `total` and `dropped`: the record counts of the whole trace.
- `window`: which records the page holds, or `null` when it holds them all.
- `slice`: the causal slice, with its `root`, `seqs`, `cap` and `truncated`.

The page holds at most 50,000 records by default. For a longer trace it keeps the causal slice
first. It then adds the newest fault, check, run and node lifecycle records, up to a quarter of the
cap. The newest other records fill the rest. `window` then says from which `seq` the page holds
every record.

## Extra files

A check can add its own files to the artifact directory. When the error that fails the seed has a
method `ArtifactFiles() map[string][]byte`, faultline writes each entry as a file:

```go
type stateErr struct{ dump []byte }

func (e stateErr) Error() string                    { return "state is wrong" }
func (e stateErr) ArtifactFiles() map[string][]byte { return map[string][]byte{"state.txt": e.dump} }
```

A final check that returns a `stateErr` adds `state.txt`, listed in `report.json` with the type
`extra`. A file name starts with a lowercase letter or a digit, and holds only lowercase letters,
digits, `.`, `_` and `-`. It cannot be the name of a standard file.

## Next steps

- [The `faultline` command](cli.md): regenerate the rendered files of an artifact directory.
- [Environment variables](environment.md): the artifact root, full traces and schedule replay.
