# Contributing to faultline

Bug reports, fixes, tests and docs are welcome. This page covers how to build and test faultline,
the rules simulation code follows, and how to send a change.

## Before you start

For a bug fix or a small change, send a pull request. For a new feature or a change in behavior,
open an issue first, so we can agree on the design before you write the code. The
[roadmap](https://github.com/hmdsefi/faultline/issues/167) shows what is planned, and the
[milestones](https://github.com/hmdsefi/faultline/milestones) group the open issues by release.

Leave a comment on an issue before you start on it, so others know it's taken.

## Development setup

You need Go 1.26 or newer and, for linting, [golangci-lint](https://golangci-lint.run/) v2.14.
Node 20 or newer is optional: with it on your `PATH`, `go test ./ui` also runs the JavaScript tests
of the timeline page, and without it those tests are skipped.

```shell
git clone https://github.com/<your-username>/faultline.git
cd faultline

gofmt -l .                                    # prints nothing when the code is formatted
go vet ./...
go test ./...
go test -race ./...
FAULTLINE_CHECK_DETERMINISM=1 go test ./...   # runs every passing seed twice and compares traces
golangci-lint run ./...
```

The etcd/raft harness is a separate module with its own `go.mod`:

```shell
go -C harness/etcdraft vet ./...
go -C harness/etcdraft test -race ./...
```

CI runs the tests on Linux and macOS with Go 1.26 and 1.27, and compares the golden hashes across
all four.

## Determinism rules

faultline promises that the same seed, code and Go minor version give the same trace on
linux/amd64 and darwin/arm64. The simulation packages (the packages under `kernel` and `check`,
and the other packages `internal/detlint` lists) keep that promise by following these rules:

- Time comes from the simulation: `Sim.Now`, `Node.Now` and `Node.After`. No `time.Now`,
  `time.Sleep`, timers or tickers.
- Randomness comes from `Sim.Rand(label)` and `Node.Rand()`. No package-level `math/rand`
  functions and no `crypto/rand`.
- No goroutines and no `select`.
- No range over a map without sorting the keys first. A justified exception carries a
  `//faultline:maporder <reason>` comment.
- No floating point in decisions: probabilities are parts per million (`kernel.Chance`) and
  durations are integer nanoseconds.
- Only package `faultline` reads environment variables.
- Every simulated decision emits a `kernel.Record`, and payloads are described with
  `kernel.Describe`, never with `%v`.
- No files specific to one operating system or architecture.

`TestDeterminismLint` in `internal/detlint` checks these rules, and `TestImportBoundaries` checks
which packages may import which. A new package directory needs a class and an import rule in
`internal/detlint/module.go` and an entry in `internal/detlint/inventory_test.go`.

## Golden hashes and the kernel API

`internal/golden/testdata/hashes.txt` pins the trace hashes of known runs, so an accidental change
to a run fails the tests. If your change is meant to change runs, update the file:

```shell
go test ./internal/golden -run TestGolden -update
```

Then add a `Golden-Update: <reason>` line to the commit message. CI checks that the line is there.

The exported API of `kernel` is recorded in `api/kernel.txt`. If you change that API on purpose,
regenerate the file in the same commit:

```shell
go test ./internal/detlint -run TestAPISnapshot -update
```

## Code guidelines

- Every Go, JavaScript and CSS file starts with the license header, written as comments in the
  file's own syntax. `TestLicenseHeaders` checks it:

  ```go
  // Copyright 2026 Hamed Yousefi
  // SPDX-License-Identifier: MPL-2.0
  ```

- A bug fix comes with a test that fails without it.
- Every exported identifier has a doc comment.
- The root module depends only on the standard library and
  [gograph](https://github.com/hmdsefi/gograph). Code that needs another dependency goes in a
  nested module with its own `go.mod`, like `harness/etcdraft`.
- `kernel` never imports `testing`.

## Pull requests

- Keep each pull request to one change.
- Pull requests are squashed into one commit, so the title becomes the commit message. Write it
  in the [Conventional Commits](https://www.conventionalcommits.org/) form, for example
  `feat(etcdraft): add stage 1c membership changes`.
- Reference the issue in the description, for example `Fixes #123`, and say how you tested the
  change.
- Run the commands from the development setup before you push.

## Reporting bugs

Open an issue with the faultline version, the Go version and the smallest test that shows the
problem. A failing seed is the best reproduction: include the seed and the replay command
faultline printed, and attach the artifact folder if you can.

For security problems, don't open a public issue. See [SECURITY.md](SECURITY.md).

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). By taking part, you agree to
follow it.

## License

By contributing, you agree that your contributions are licensed under the
[Mozilla Public License 2.0](../LICENSE), the same license as the project.
