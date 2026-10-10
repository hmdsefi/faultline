# faultline documentation

These pages show how to test a distributed system in Go with faultline, by hand or through an AI
coding agent. Each page has one job: a tutorial teaches, a guide walks through one task, a
reference page lists facts, and a concept page explains.

## Start here

Pick the first page by how you work:

- [Use faultline with an AI coding agent](ai-agents.md): the loop from a failing seed to a pinned
  regression test, and a block for the agent's instruction file.
- [owner: getting-started.md, the tutorial from an empty module to a failing seed, its replay and
  the fix; later batch]

## Concepts

These pages explain how faultline works and why:

- [How faultline works](how-it-works.md): virtual time, the seeded scheduler, nodes, the simulated
  network and disk, faults, checks, seeds, replay, and the rules your code follows.
- [Architecture](architecture.md): the packages, how one seed runs through them, and the design
  choices behind them.
- [owner: faq.md, limitations and who faultline is not for; later batch]

## Guides

Each guide covers one task. [owner: docs/guides/, later batch: replay a seed and narrow it with
`schedule.json`, read a failure artifact and the timeline, check determinism, run faultline in CI,
test etcd/raft with the harness]

## Reference

The Go API is documented on [pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline), from
the doc comments and examples in the code. [owner: docs/reference/, later batch: environment
variables, artifact files, the `faultline` command]

## Contributing

[CONTRIBUTING](../.github/CONTRIBUTING.md) explains how to build and test faultline and how to send
a change.
