# Run faultline in CI

faultline tests are Go tests, so CI runs them with `go test` like any other test. This guide shows
how to run the same seeds on every pull request and fresh seeds every night. It also covers how to
keep the artifacts of a failing seed and how to write one result line per seed for your tools.

## Run the same seeds on every pull request

By default `faultline.Run` derives its seeds from the test name. Every CI run and every laptop runs
the same seeds. A seed that fails in a pull request fails the same way on your machine, with the
replay command from the log.

Run the tests with the determinism check, so that a test that stops replaying fails the pull
request:

```sh
FAULTLINE_CHECK_DETERMINISM=1 go test -count=1 ./...
```

`-count=1` makes `go test` run the tests even when it holds a cached result. For a faster local
loop, `go test -short` runs 5 seeds per test unless `FAULTLINE_SEEDS` is set:

```text
    kv_test.go:14: faultline: -short: running 5 of 50 seeds
```

## Keep the artifacts of a failing seed

On a CI runner the default artifact root sits in the runner's temporary directory, which is gone
when the job ends. Set `FAULTLINE_ARTIFACTS` to a directory the job uploads when a test fails. The
upload then holds `report.txt`, `timeline.html` and the other files for each failing seed.

## Write one result line per seed

Set `FAULTLINE_RESULTS` to a file, and faultline appends one JSON line to it for every seed it runs.
A passing seed's line holds its trace hash and its number of events:

```json
{"faultline_result":1,"package":"example.com/kv","test":"TestKV","seed":"0x287372ab06f1482e","index":0,"status":"pass","events":680,"trace_hash":"0x3db7c81cc7771d70","wall_ns":11602833}
```

A failing seed's line adds the failure and its artifact directory:

```json
{"faultline_result":1,"package":"example.com/kv","test":"TestKV","seed":"0x287372ab06f1482e","index":0,"status":"fail","kind":"invariant","check":"acked puts survive","signature":"invariant:acked puts survive","message":"k169=v169 was acknowledged, but n1 holds \"\"","at_ns":5873034010,"at":"5.873034010s","events":1455,"trace_hash":"0x34d831a670e30100","artifact":"/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e","wall_ns":51237500}
```

faultline creates the file, but not its directory. When it cannot write a line, the seed fails, so
point `FAULTLINE_RESULTS` at a directory that exists before the tests start. [Environment
variables](../reference/environment.md) lists every field of the line.

## Explore new seeds every night

The pull request seeds stay the same, so they never find a bug that needs another seed. A nightly
job can run many fresh seeds instead. `FAULTLINE_EXPLORE=1` picks a new base seed for each test and
logs it, and `FAULTLINE_SEEDS` sets how many seeds each test runs:

```sh
FAULTLINE_EXPLORE=1 FAULTLINE_SEEDS=1000 go test -count=1 -timeout 60m ./...
```

The log line names the base seed and the command that runs the same set again:

```text
    kv_test.go:14: faultline: FAULTLINE_EXPLORE: base seed 0x9ada79e390c96f2f (rerun this set with FAULTLINE_BASE_SEED=0x9ada79e390c96f2f FAULTLINE_SEEDS=1000)
```

`go test` prints a test's log lines when the test fails, or with `-v`. You do not need the base seed
to debug a failure: the replay command of a failing seed sets `FAULTLINE_SEED` and works on its own.
When a nightly seed finds a bug, fix it and pin the seed as a regression test, as [Get
started](../getting-started.md#pin-the-seed) shows.

## Set a timeout

`go test` stops a test binary after 10 minutes by default. Raise the limit with `-timeout` for long
runs. A run's wall-clock time grows with its events. In faultline's own runs on an Apple M4 Max, a
60-second run of a three-node etcd/raft cluster under faults took 68 ms on average. A test of 10,000
such seeds took 692 seconds, more than the default limit.

## A GitHub Actions workflow

The following workflow puts these pieces together. It runs the default seeds with the determinism
check on every pull request, and explores 1,000 fresh seeds per test every night. When a test fails,
it uploads the artifacts and the results file:

```yaml
name: simulation

on:
  pull_request:
  schedule:
    - cron: '0 3 * * *'

permissions:
  contents: read

jobs:
  simulation:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: faultline settings
        run: |
          echo "FAULTLINE_ARTIFACTS=$RUNNER_TEMP/faultline" >> "$GITHUB_ENV"
          echo "FAULTLINE_RESULTS=$RUNNER_TEMP/faultline-results.jsonl" >> "$GITHUB_ENV"
          echo "FAULTLINE_CHECK_DETERMINISM=1" >> "$GITHUB_ENV"
      - name: simulation tests
        if: github.event_name == 'pull_request'
        run: go test -count=1 ./...
      - name: nightly exploration
        if: github.event_name == 'schedule'
        env:
          FAULTLINE_EXPLORE: '1'
          FAULTLINE_SEEDS: '1000'
        run: go test -count=1 -timeout 60m ./...
      - uses: actions/upload-artifact@v4
        if: failure()
        with:
          name: faultline
          path: |
            ${{ runner.temp }}/faultline
            ${{ runner.temp }}/faultline-results.jsonl
```

Adjust the number of seeds and the timeout to your tests. The nightly step also runs the determinism
check, so it runs each passing seed twice.

## Replay a seed from CI

Download the uploaded artifact and open `report.txt` for the failing seed. Run its replay command
from your checkout of the same commit.

To get the replay warnings, set `FAULTLINE_ARTIFACTS` to the folder that holds the downloaded
artifacts. faultline then finds the CI run's `report.json` and warns about each difference, for
example a different Go version:

```text
    warning: previous artifact was recorded with go1.26; this run uses go1.27
```

Expect the test binary warning as well: a binary built on another machine differs from yours.
faultline itself gives the same run on every machine: its own CI runs its golden seeds on Linux on
amd64 and on macOS on arm64, with Go 1.26 and Go 1.27, and checks that every trace hash matches.

## Next steps

- [Environment variables](../reference/environment.md): every `FAULTLINE_*` variable and its
  default.
- [Check that a test is deterministic](check-determinism.md): read a determinism failure from CI.
- [Read a failure artifact](read-an-artifact.md): the files in the uploaded artifact.
