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

faultline lets an AI coding agent test a distributed Go system inside `go test`. It finds the bugs
that appear only when messages are delayed or dropped, nodes crash and disk syncs fail. The system
runs in virtual time on a simulated network and disk, where faultline injects those faults. Every
random choice comes from one seed, so a failing seed replays the identical run. In v0.1.0 the code
under test is written against faultline's node API, as callbacks with no goroutines, or is etcd/raft
through the included harness.

An agent that changes distributed code gets:

- retries, timeouts and crash recovery tested under faults, with message delays of 1 to 5 ms by
  default that can reorder messages on a link;
- a check of the fix: the seed that failed passes after the change, and fails again if the change
  is reverted;
- a regression test: the failing seed, pinned in a test of its own;
- invariants as code: Go functions that faultline runs after every event.

From a failing seed to a pinned regression test, every step except the fix itself is a command to
run or a file to read. People follow the same steps by hand. To start, give your agent [Use
faultline with an AI coding agent](docs/ai-agents.md). It walks through the loop and ends with a
block for the agent's `AGENTS.md` or `CLAUDE.md`. To learn faultline by hand, build the following
example step by step in [Get started](docs/getting-started.md).

## Who it is for

faultline is for Go teams, and their agents, that build a distributed system: a consensus or
replication library, a database, a queue, a controller. Code on the node API consists of boot
functions, message handlers and timers that use the simulated network and disk. The etcd/raft
harness ports one real system to that API. faultline is the wrong tool when:

- You need to test existing goroutine, `net` or `os` code unchanged. It needs a port to the node API
  first.
- You want to test the deployed system, or inject faults into production as chaos engineering tools
  do. faultline simulates the machines, the network and the disks inside one test process.
- You want performance numbers. A callback takes zero virtual time.

[Limitations and FAQ](docs/faq.md) lists what v0.1.0 does not do, and how faultline relates to
FoundationDB's simulation testing, TigerBeetle's VOPR, Antithesis, Jepsen and `testing/synctest`.

## Example

The following test from the tutorial runs a key-value server `n1` and a client `c1` for 10 seconds
of virtual time. It crashes the server about once a second and checks after every event that no
acknowledged put is lost:

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

The tutorial builds `kv.Server` and `kv.Client` on the node API. The server's message handler has a
bug on purpose. It appends each put to the log and acknowledges it at once, while a timer syncs the
log only every 5 ms:

```go
s.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
	p := msg.(Put)
	if _, err := wal.Append([]byte(p.Key + "=" + p.Value + "\n")); err != nil {
		return // no ack: the client retries
	}
	s.Data[p.Key] = p.Value
	s.W.Net.Send(n, from, Ack{Key: p.Key})
})
```

`go test` finds the bug:

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
history and `timeline.html`. Read backward from the failure, and the timeline shows the server
sending the acknowledgment for `k169` and crashing 0.3 ms later, before its next sync:

![The timeline page of seed 0x287372ab06f1482e: the failure headline and the replay command at the top, lanes for global, n1 and c1 with six hatched crash bands on n1, a failure marker at 5.873034010s, and side panels with the failed check and the six crashes](.github/assets/timeline.png)

## etcd/raft

`harness/etcdraft` runs clusters of [etcd/raft](https://github.com/etcd-io/raft) v3.7.0 inside
faultline, with checks for raft's safety properties after every event. One call, `etcdraft.Test`,
runs a whole test.

faultline ran 40,000 seeds of the harness under the default faults, and no seed failed. Five bug
switches break the harness on purpose, and the checks caught each one on the first seed. [Test
etcd/raft with the harness](docs/guides/etcd-raft.md) shows how to write such a test, and gives the
configurations and timings of those runs.

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

Start with the index, or go to the page you need:

- [The documentation index](docs/README.md): the tutorial, guides, reference and concept pages.
- [The Go API on pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline).
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
