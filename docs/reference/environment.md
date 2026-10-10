# Environment variables

The `FAULTLINE_*` environment variables change how `faultline.Run` runs a test, without a code
change. They choose the seeds, the faults, the artifact root and the checks. This page lists every
variable that v0.1.0 reads, its values, its default and how it combines with `Options`.

## How Run reads them

`faultline.Run` reads each variable once per call, before any seed runs. It trims white space from
the value, and an empty value counts as unset. A relative path resolves against the test's working
directory, which `go test` sets to the package directory.

An invalid value is a *setup error*: the test fails before any seed runs, and the message names the
variable and the values it accepts.

```sh
FAULTLINE_SEEDS=0 go test -run '^TestKV$' .
```

```text
--- FAIL: TestKV (0.00s)
    kv_test.go:14: faultline: invalid FAULTLINE_SEEDS value "0": want a decimal integer from 1 to 1000000
FAIL
FAIL	example.com/kv	0.412s
FAIL
```

When several values are invalid, the error names the first one in the order of the following table.

## Summary

The variables, in the order `Run` checks them:

| Variable | Value | Default | Effect |
|---|---|---|---|
| `FAULTLINE_SEED` | a seed | unset | runs only this seed |
| `FAULTLINE_SEEDS` | 1 to 1000000 | `Options.Seeds`, else 20; at most 5 under `-short` | number of derived seeds |
| `FAULTLINE_BASE_SEED` | a seed | `Options.BaseSeed`, else a hash of the test name | base of the derived seeds |
| `FAULTLINE_EXPLORE` | `0` or `1` | `0` | `1` picks a new base seed and logs it |
| `FAULTLINE_SCHEDULE` | path to a `schedule.json` | unset | applies exactly these faults, with planners off |
| `FAULTLINE_ARTIFACTS` | directory path, or `off` | `faultline-<uid>` in the temporary directory | artifact root |
| `FAULTLINE_CHECK_DETERMINISM` | `0` or `1` | `0` | `1` runs every passing seed twice |
| `FAULTLINE_TRACE` | `hash` or `full` | `hash` | `full` keeps every record and writes artifacts for passing seeds |
| `FAULTLINE_MINIMIZE` | `0`, `1` or parameters | unset | checked, then ignored with a log line |
| `FAULTLINE_SWARM` | any | unset | setup error |
| `FAULTLINE_SWARM_CONFIG` | any | unset | setup error |
| `FAULTLINE_EXACT` | any | unset | setup error |
| `FAULTLINE_SEED_LIST` | seeds, or `@file` | unset | runs exactly these seeds, all of them |
| `FAULTLINE_RESULTS` | file path | unset | appends one JSON line per seed |

A *seed* is a 64-bit unsigned integer, written in decimal (`42`) or in hexadecimal with a `0x`
prefix (`0x287372ab06f1482e`). The switches `FAULTLINE_EXPLORE` and `FAULTLINE_CHECK_DETERMINISM`
take exactly `0` or `1`: `true` and `yes` are setup errors.

## Choosing the seeds

By default `Run` derives its seeds from a base seed, so every machine runs the same list. Five
variables change the list. The first one of these rules that applies decides it:

1. `FAULTLINE_SEED_LIST` runs exactly the listed seeds.
2. `FAULTLINE_SEED` runs one seed. It cannot be combined with `FAULTLINE_SEED_LIST`.
3. Otherwise `Run` derives the seeds. The count is `FAULTLINE_SEEDS`, else `Options.Seeds`, else 20.
   Under `go test -short` the count is at most 5, unless `FAULTLINE_SEEDS` is set. The base is
   `FAULTLINE_BASE_SEED`, else a new base when `FAULTLINE_EXPLORE=1`, else `Options.BaseSeed`, else
   a hash of the test name.

A variable that a higher rule overrides is ignored, and `Run` logs that it ignored it.

### FAULTLINE_SEED

`FAULTLINE_SEED=<seed>` runs that one seed. It is how you replay a failure: the replay command that
a failing seed prints sets it.

When artifacts are on, `Run` also reads the artifact that the seed left before. It warns when the
faultline version, the Go minor version, the test binary or the options differ from that run. A seed
that passes in the replay keeps the failing artifact and says so:

```text
    faultline: seed 0x287372ab06f1482e passed; kept the failing artifact at /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/, which recorded invariant:acked puts survive
    warning: the test binary differs from the one that recorded the previous artifact (code, dependencies or build flags changed)
```

To pin a seed in a regression test, set `FAULTLINE_SEED` with `t.Setenv` and clear
`FAULTLINE_SEED_LIST` the same way. [Use faultline with an AI coding agent](../ai-agents.md#protect)
shows the test.

### FAULTLINE_SEEDS

`FAULTLINE_SEEDS=<n>` sets the number of derived seeds, from 1 to 1000000. It takes the place of
`Options.Seeds`, and `-short` does not lower it.

### FAULTLINE_BASE_SEED

`FAULTLINE_BASE_SEED=<seed>` sets the base that the seed list is derived from. It takes the place of
`Options.BaseSeed` and of `FAULTLINE_EXPLORE`.

### FAULTLINE_EXPLORE

`FAULTLINE_EXPLORE=1` derives the seeds from a new base seed each time the test runs, to try seeds
outside the fixed list. `Run` logs the base and the variables that run the same set again:

```text
    kv_test.go:14: faultline: running 3 seeds from base 0x9f90b25e5010d661 (FAULTLINE_EXPLORE)
    kv_test.go:14: faultline: FAULTLINE_EXPLORE: base seed 0x9f90b25e5010d661 (rerun this set with FAULTLINE_BASE_SEED=0x9f90b25e5010d661 FAULTLINE_SEEDS=3)
```

### FAULTLINE_SEED_LIST

`FAULTLINE_SEED_LIST` runs exactly the seeds it lists, and runs all of them even after one fails. It
is meant for scripts and agents that run a known set, such as the failing seeds of a CI run. The
value lists seeds separated by commas or white space, such as `0x1 0x2,0x3`. It can also be `@` and
the path of a file that holds such a list. Lines may hold several seeds; there is no comment syntax.
A seed listed twice runs once.

## Replaying faults: FAULTLINE_SCHEDULE

`FAULTLINE_SCHEDULE=<path>` applies the faults of a `schedule.json` file, at their recorded times,
and starts no planner. `Run` reads and checks the file before any seed runs, and logs it:

```text
    kv_test.go:14: faultline: FAULTLINE_SCHEDULE=/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/schedule.json: 12 events; planners are disabled
```

A schedule's `end` and `recovery` fields, when set, replace the end of the run and the start of the
recovery window. The replay command of a failure in such a run sets `FAULTLINE_SCHEDULE` too.

With the same seed, a schedule replay fails like the run that recorded it: the same check, at the
same virtual time and event number. Its trace hash differs: the replay has no planner records, and
its fault records name `replay` as their source. The [artifact reference](artifacts.md#schedulejson)
describes the file.

## Artifacts and traces

### FAULTLINE_ARTIFACTS

`FAULTLINE_ARTIFACTS=<path>` sets the artifact root, the folder that holds every artifact directory.
`FAULTLINE_ARTIFACTS=off`, in any letter case, writes no artifacts, and a failure prints
`artifacts: off`.

The default root is `faultline-<uid>` in the system temporary directory, where `<uid>` is your user
ID: for example `/tmp/faultline-501`. The temporary directory is `$TMPDIR`, or `/tmp` when `TMPDIR`
is unset. On Windows the folder is named `faultline`.

The values `0`, `1`, `true`, `false`, `yes`, `no` and `on` are setup errors, because as paths they
would name a folder in the package:

```text
faultline: invalid FAULTLINE_ARTIFACTS value "true": artifacts are on by default; set a directory path for the artifact root, or off to turn artifacts off
```

### FAULTLINE_TRACE

`FAULTLINE_TRACE=full` keeps every record of every run, and writes an artifact for passing seeds
too. A passing seed's artifact never replaces a failing one. `FAULTLINE_TRACE=hash`, the default,
keeps only the trace hash until a seed fails. The value is case-sensitive: `Full` is a setup error.

With `FAULTLINE_ARTIFACTS=off`, `FAULTLINE_TRACE=full` writes nothing, and `Run` logs a warning.

## Checks: FAULTLINE_CHECK_DETERMINISM

`FAULTLINE_CHECK_DETERMINISM=1` runs every passing seed a second time and compares the two trace
hashes. Different hashes fail the seed with a determinism failure. Its replay command sets
`FAULTLINE_CHECK_DETERMINISM=1` too, so the replay runs the check again. faultline's own CI runs its
whole suite with this variable set.

## Output for tools: FAULTLINE_RESULTS

`FAULTLINE_RESULTS=<path>` appends one JSON line per seed to the file, with a single write per line,
and creates the file when it is missing. Here are the lines of a failing seed and a passing one:

```json
{"faultline_result":1,"package":"example.com/kv","test":"TestKV","seed":"0x287372ab06f1482e","index":0,"status":"fail","kind":"invariant","check":"acked puts survive","signature":"invariant:acked puts survive","message":"k169=v169 was acknowledged, but n1 holds \"\"","at_ns":5873034010,"at":"5.873034010s","events":1455,"trace_hash":"0x34d831a670e30100","wall_ns":21706292}
{"faultline_result":1,"package":"example.com/kv","test":"TestKV","seed":"0x0000000000000001","index":1,"status":"pass","events":2424,"trace_hash":"0x88694ecd14e9de1f","wall_ns":24765084}
```

The fields of a line, in order:

| Field | Type | Meaning |
|---|---|---|
| `faultline_result` | integer | format version, `1` |
| `package` | string | import path of the test package |
| `test` | string | name of the test that called `Run` |
| `seed` | string | the seed as `0x` and 16 hex digits; `""` for a setup error |
| `index` | integer | position in the seed list; `-1` for a setup error |
| `status` | string | `pass`, `fail` or `skip` |
| `kind`, `check`, `signature` | string | the failure's kind, check name and signature; failing seeds only |
| `message` | string | the failure message; on every failing line, also when empty |
| `at_ns`, `at` | integer, string | virtual time of the failure; not for a setup error |
| `events` | integer | events the first run of the seed executed |
| `trace_hash` | string | trace hash of the first run of the seed |
| `artifact` | string | artifact directory, when one was written |
| `artifact_error` | string | why the artifact could not be written |
| `wall_ns` | integer | wall-clock time the seed took, in nanoseconds |

A field without a value is left out. A setup error writes one line with `"kind":"setup"`, an `index`
of `-1` and the error as its `message`.

## Reserved for later versions

v0.1.0 reserves four variables for later phases. It checks their values and reports any use, so a
command written for a later version cannot run here unnoticed.

`FAULTLINE_MINIMIZE` accepts `0`, `1`, or a comma-separated list of `runs=<n>`, `time=<duration>`
and `out=<absolute path>`, with `out` last. An invalid value is a setup error, such as
`faultline: invalid FAULTLINE_MINIMIZE "yes": parameter "yes" is not key=value`. A valid value that
turns minimization on changes nothing, and `Run` logs:

```text
faultline: FAULTLINE_MINIMIZE is set, but minimization is not available in this version; ignoring
```

Any value of the other three is a setup error, with these messages:

```text
faultline: FAULTLINE_SWARM is set, but swarm testing is not in this release yet; unset it, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167
faultline: FAULTLINE_SWARM_CONFIG is set, but swarm testing is not in this release yet; unset it, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167
faultline: FAULTLINE_EXACT is set, but exact replay in goroutine mode is not in this release yet; unset it, and see the roadmap at https://github.com/hmdsefi/faultline/issues/167
```

## Options and the environment

A variable either replaces an option or adds to it, and never turns off what an option turned on:

| Option | Variable | Combined effect |
|---|---|---|
| `Options.Seeds` | `FAULTLINE_SEEDS` | the variable wins; `FAULTLINE_SEED` and `FAULTLINE_SEED_LIST` replace the whole list |
| `Options.BaseSeed` | `FAULTLINE_BASE_SEED`, `FAULTLINE_EXPLORE` | the variables win |
| `Options.KeepGoing` | `FAULTLINE_SEED_LIST` | the list turns it on |
| `Options.CheckDeterminism` | `FAULTLINE_CHECK_DETERMINISM` | `1` turns the check on; `0` does not turn it off |
| `Options.Trace.Level` | `FAULTLINE_TRACE` | `full` raises it to `kernel.TraceFull`; `hash` does not lower it |

When a variable cannot turn an option off, `Run` logs it, for example:

```text
faultline: FAULTLINE_CHECK_DETERMINISM=0 does not turn off Options.CheckDeterminism; the determinism check still runs (set Options.CheckDeterminism to false to turn it off)
```

The `options` object of `report.json` records the effective values after the environment applied.

## Log lines

`Run` logs how it read the environment at the start of the test, with `t.Log`. `go test` shows these
lines with `-v` or when the test fails. It prefixes each line with the file and line of the
`faultline.Run` call, such as `kv_test.go:14:`. Every call of `Run` logs its seed list:

```text
faultline: running 50 seeds from base 0x75c66bf05fc438d4 (test name)
faultline: running 2 seeds from base 0x0000000000000001 (FAULTLINE_BASE_SEED)
faultline: FAULTLINE_SEED=0x287372ab06f1482e: running 1 seed
faultline: FAULTLINE_SEED_LIST: running 2 seeds
```

The source in parentheses is `test name`, `Options.BaseSeed`, `FAULTLINE_BASE_SEED` or
`FAULTLINE_EXPLORE`. These lines follow when they apply:

```text
faultline: FAULTLINE_SEED is set; ignoring FAULTLINE_SEEDS
faultline: -short: running 5 of 50 seeds
faultline: FAULTLINE_TRACE=full writes no artifacts while FAULTLINE_ARTIFACTS is off; unset one of them
faultline: FAULTLINE_TRACE=hash does not lower Options.Trace.Level from kernel.TraceFull; runs still record full traces (set Options.Trace.Level to kernel.TraceHash to lower it)
```

## Next steps

- [Artifact files](artifacts.md): what each file in an artifact directory holds.
- [The `faultline` command](cli.md): regenerate the rendered files of an artifact.
- [How faultline works](../how-it-works.md#seeds-and-replay): seeds, replay and trace hashes.
