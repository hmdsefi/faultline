# faultline documentation

These pages show how to test a distributed system in Go with faultline, by hand or through an AI
coding agent. Each page has one job: a tutorial teaches, a guide walks through one task, a
reference page lists facts, and a concept page explains.

## AI coding agents

Start here if an agent writes or debugs your tests:

- [Use faultline with an AI coding agent](ai-agents.md): the loop from a failing seed to a pinned
  regression test, which file answers which question, and a block for the agent's instruction file.

## Tutorial

Start here to learn faultline by hand:

- [Get started](getting-started.md): from an empty module to a key-value server that loses a write,
  the failing seed, its replay, the cause in the timeline, the fix and a pinned seed.

## How-to guides

Each guide covers one task:

- [Replay a failing seed](guides/replay-a-seed.md): run the replay, confirm that it is the same run,
  read the replay warnings, and narrow a failure by editing its fault schedule.
- [Read a failure artifact](guides/read-an-artifact.md): find the artifact directory, walk back
  from the failure in `timeline.txt`, and use the timeline page and `report.json`.
- [Check that a test is deterministic](guides/check-determinism.md): turn on the determinism check,
  read a determinism failure, and fix the usual causes.
- [Run faultline in CI](guides/ci.md): fixed seeds on every pull request, fresh seeds every night,
  artifacts and one result line per seed.
- [Test etcd/raft with the harness](guides/etcd-raft.md): run the harness, configure it, write a
  scenario, and prove that its checks catch real mistakes.

## Reference

These pages list exact facts. The Go API is documented on
[pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline), from the doc comments and examples
in the code.

- [Environment variables](reference/environment.md): every `FAULTLINE_*` variable, its values and
  default, and how it combines with `Options`.
- [Artifact files](reference/artifacts.md): the artifact directory and the format of every file in
  it.
- [The `faultline` command](reference/cli.md): `render` and `version`, their flags and exit codes.

## Concepts

These pages explain how faultline works and why:

- [How faultline works](how-it-works.md): virtual time, the seeded scheduler, nodes, the simulated
  network and disk, faults, checks, seeds, replay, and the rules your code follows.
- [Architecture](architecture.md): the packages, how one seed runs through them, and the design
  choices behind them.
- [Limitations and FAQ](faq.md): what v0.1.0 does not do, the known issues, and how faultline
  relates to other tools.

## Contributing

[CONTRIBUTING](../.github/CONTRIBUTING.md) explains how to build and test faultline and how to send
a change.
