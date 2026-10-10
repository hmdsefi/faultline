[![build](https://github.com/hmdsefi/faultline/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/hmdsefi/faultline/actions/workflows/ci.yml?query=branch%3Amain)
[![coverage](https://img.shields.io/github/issues/detail/title/hmdsefi/faultline/172?label=coverage&color=brightgreen)](https://github.com/hmdsefi/faultline/actions/workflows/ci.yml?query=branch%3Amain)
[![CodeRabbit Pull Request Reviews](https://img.shields.io/coderabbit/prs/github/hmdsefi/faultline?labelColor=171717&color=FF570A&label=CodeRabbit+Reviews)](https://coderabbit.ai)
[![Go Reference](https://pkg.go.dev/badge/github.com/hmdsefi/faultline.svg)](https://pkg.go.dev/github.com/hmdsefi/faultline)
[![Sponsor](https://img.shields.io/badge/sponsor-hmdsefi-ea4aaa?logo=githubsponsors)](https://github.com/sponsors/hmdsefi)
[![GitHub stars](https://img.shields.io/github/stars/hmdsefi/faultline?style=social)](https://github.com/hmdsefi/faultline)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/readme-dark.svg">
    <img alt="faultline: deterministic simulation testing for Go" src=".github/assets/readme-light.svg" width="640">
  </picture>
</p>

<p align="center">
  Find the bugs in your distributed system before production does.
</p>

faultline lets an AI coding agent find the bugs that unit tests miss in a distributed Go system,
inside `go test`. It runs the system in virtual time on a simulated network and disk, with faults
such as dropped messages, partitions, crashes and failed syncs. Every random choice comes from one
seed, so a failing seed replays the identical run. An agent that changes distributed code gets:

- retries, timeouts and crash recovery tested under faults, with messages reordered in every run;
- proof of a fix: the same seed fails before the change and passes after it;
- a regression test: the failing seed, pinned in a test of its own;
- invariants as code: Go functions that faultline runs after every event;
- explicit inputs: time, randomness, messages and files all come from the simulation.

Every step is a command to run or a file to read, and people follow the same loop by hand. [Use
faultline with an AI coding agent](docs/ai-agents.md) walks through it and ends with a block for
your agent's `AGENTS.md` or `CLAUDE.md`.

## Who it is for

faultline is for Go teams, and their agents, that build a distributed system: a consensus or
replication library, a database, a queue, a controller. In v0.1.0 the code under test runs on
faultline's node API, as boot functions, message handlers and timers that use the simulated network
and disk. The etcd/raft harness ports one real system to that API. It is the wrong tool when:

- You need to test existing goroutine, `net` or `os` code unchanged. It needs a port to the node API
  first; running it unchanged is planned, with no date.
- You want to test the deployed system, or inject faults into production as chaos engineering tools
  do. faultline simulates the machines, the network and the disks inside one test process.
- You want performance numbers. A callback takes zero virtual time.

## Example

The following test from the [tutorial](docs/getting-started.md) runs a key-value server `n1` and a
client `c1` for 10 seconds of virtual time. It crashes the server about once a second and checks
after every event that no acknowledged put is lost:

```go
var kvOptions = faultline.Options{Seeds: 50, Duration: 10 * time.Second}

func TestKV(t *testing.T) { faultline.Run(t, kvOptions, kvWorld) }

func kvWorld(w *faultline.World) {
	s := &kv.Server{W: w}
	n1 := w.AddServer("n1", s.Boot)
	if err := w.Disk.Volume(n1).WriteFileDurable("/wal", nil); err != nil {
		w.T().Fatal(err)
	}
	c := &kv.Client{W: w, Server: n1.ID(), Puts: 200}
	w.AddClient("c1", c.Boot)

	w.Plan(&fault.Random{MaxDown: 1, Rules: []fault.Rule{{
		Kind: fault.KindCrash, Every: time.Second,
		MinFor: 10 * time.Millisecond, MaxFor: 500 * time.Millisecond,
	}}})
	w.Invariant("acked puts survive", func() error { return c.Lost(s, n1) })
	w.Final("every put acked", c.Done)
}
```

The tutorial builds `kv.Server` and `kv.Client` on the node API. The server has a bug on purpose:
it acknowledges a put before it syncs its log. `go test` finds it:

```text
--- FAIL: TestKV (0.04s)
    kv_test.go:14: faultline: running 50 seeds from base 0x75c66bf05fc438d4 (test name)
    --- FAIL: TestKV/seed=0x287372ab06f1482e (0.04s)
        faultline: invariant "acked puts survive" violated at t=5.873034010s (event 1455)
          k169=v169 was acknowledged, but n1 holds ""
        replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
        artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
    kv_test.go:14: faultline: stopping after failing seed 0x287372ab06f1482e; 49 of 50 seeds not run (set Options.KeepGoing to run all)
FAIL
FAIL	example.com/kv	0.896s
FAIL
```

The seeds come from the test name, so every machine runs the same ones. The replay command runs the
failing seed again and gets the identical run: the same check fails at the same virtual time and
event number.

The artifact directory holds the report, the trace, a text timeline, the fault schedule, the client
history and `timeline.html`. Read backward from the failure, the timeline shows the server sending
the acknowledgment for `k169` and crashing 0.3 ms later, before its next sync:

![The timeline page of seed 0x287372ab06f1482e: the failure headline and the replay command at the top, lanes for global, n1 and c1 with six hatched crash bands on n1, a failure marker at 5.873034010s, and side panels with the failed check and the six crashes](.github/assets/timeline.png)

## etcd/raft

`harness/etcdraft` runs clusters of [etcd/raft](https://github.com/etcd-io/raft) v3.7.0 inside
faultline, with checks for raft's safety properties after every event. One call, `etcdraft.Test`,
runs a whole test, and [Test etcd/raft with the harness](docs/guides/etcd-raft.md) shows the rest.

faultline ran 40,000 seeds of the harness: 10,000 each for three and five servers, with and without
snapshots, 60 seconds of virtual time each under the default faults. No seed failed. Five bug
switches break the harness on purpose, and each one failed on the first seed it ran. On an Apple M4
Max, a three-server seed took 68 ms on average.

## Install

faultline needs Go 1.26 or newer. The first command adds faultline to your module, and the second
adds the etcd/raft harness. The third installs the `faultline` command, which re-renders an artifact
directory; your tests do not need it.

```sh
go get github.com/hmdsefi/faultline
go get github.com/hmdsefi/faultline/harness/etcdraft
go install github.com/hmdsefi/faultline/cmd/faultline@latest
```

The root module depends only on the standard library and
[gograph](https://github.com/hmdsefi/gograph). The harness is a module of its own, so etcd/raft and
protobuf stay out of your module unless you add it.

## Status

v0.1.0, early: the API may change before v1.0; artifact formats are versioned. The
[roadmap](https://github.com/hmdsefi/faultline/issues/167) lists what the later phases add.

## Documentation and help

- [The documentation index](docs/README.md): the tutorial, guides, reference and concept pages.
- [The Go API on pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline).
- [Limitations and FAQ](docs/faq.md): what v0.1.0 does not do, and how faultline relates to
  FoundationDB's simulation testing, TigerBeetle's VOPR, Antithesis, Jepsen and `testing/synctest`.
- [CONTRIBUTING](.github/CONTRIBUTING.md) and [SECURITY](.github/SECURITY.md): how to send a change,
  and how to report a vulnerability privately.

## Why the name

A fault line is a crack in the earth's crust. Stress builds up there for years without any sign,
and then it is released all at once in an earthquake. Bugs in distributed systems often behave the
same way. In software, a "fault" is also the usual word for something going wrong, like a crashed
server or a lost message. faultline helps you find those weak spots in a test instead of in
production.

## License

faultline is licensed under the [Mozilla Public License 2.0](LICENSE). You can use it in any
project, including closed-source ones. If you change faultline's own files and distribute them,
those files stay under the MPL 2.0. You must make their source available and keep the copyright
and license notices.
