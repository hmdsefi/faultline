# Get started with faultline

In this tutorial you write a small key-value server and a faultline test for it. faultline finds a
seed that loses a write the server acknowledged. You replay that seed and find the cause in the
timeline. Then you fix the server, prove the fix with the same seed, and keep the seed as a
regression test.

## What you build

The server keeps every put in memory and appends it to a log file on its simulated disk. It has a
bug on purpose. It acknowledges a put as soon as the put is in the log, but it syncs the log only
every 5 ms. A crash between the acknowledgment and the next sync loses a put that the client was
told is stored.

The test runs one server and one client for 10 seconds of virtual time. It crashes the server at
random moments and checks after every event that the server still holds every acknowledged put.

## Before you start

You need Go 1.26 or newer. Check your version:

```sh
go version
```

```text
go version go1.27.1 darwin/arm64
```

## Create the module

Create an empty module and add faultline to it:

```sh
mkdir kv && cd kv
go mod init example.com/kv
go get github.com/hmdsefi/faultline
```

Keep the module path `example.com/kv`. It appears in faultline's output, and the output on this page
then matches yours.

## Write the server

Create `kv.go` with the message types and the server. The imports also cover the client that you add
in the next step.

```go
// Package kv is a key-value server with a write-ahead log, and a client that writes to it.
package kv

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// Put asks the server to store Value under Key.
type Put struct{ Key, Value string }

// Ack tells the client that Key is stored.
type Ack struct{ Key string }

// Describe returns the text the trace shows for a Put.
func (p Put) Describe() string { return "put " + p.Key + "=" + p.Value }

// Describe returns the text the trace shows for an Ack.
func (a Ack) Describe() string { return "ack " + a.Key }

// Server stores puts in memory and appends them to /wal.
type Server struct {
	W    *faultline.World
	Data map[string]string // rebuilt from /wal at every boot
}

// Boot runs at every boot of the node: its first start and every restart.
func (s *Server) Boot(n *kernel.Node) {
	vol := s.W.Disk.Volume(n) // the same volume in every incarnation
	s.Data = load(vol)        // memory starts empty: rebuild it from the disk
	wal, err := vol.Open("/wal")
	if err != nil {
		n.Sim().Fail(err)
		return
	}
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		p := msg.(Put)
		if _, err := wal.Append([]byte(p.Key + "=" + p.Value + "\n")); err != nil {
			return // no ack: the client retries
		}
		s.Data[p.Key] = p.Value
		s.W.Net.Send(n, from, Ack{Key: p.Key})
	})
	var flush func()
	flush = func() {
		_ = wal.Sync()
		n.After(5*time.Millisecond, "flush", flush)
	}
	n.After(5*time.Millisecond, "flush", flush)
}

// load reads the records of /wal and cuts a torn last record off the file.
func load(vol *simdisk.Volume) map[string]string {
	data := map[string]string{}
	log, err := vol.ReadFile("/wal")
	if err != nil {
		return data
	}
	end := bytes.LastIndexByte(log, '\n') + 1
	for _, line := range bytes.Split(log[:end], []byte("\n")) {
		if k, v, ok := bytes.Cut(line, []byte("=")); ok {
			data[string(k)] = string(v)
		}
	}
	if end < len(log) {
		if f, err := vol.Open("/wal"); err == nil {
			_ = f.Truncate(int64(end))
			_ = f.Sync()
			_ = f.Close()
		}
	}
	return data
}
```

The server is code written against faultline's node API, which is what faultline runs. [Limitations
and FAQ](faq.md) lists the code it does not run. Here is what each part does:

- `Boot` is the server's *boot function*. faultline calls it when the node first starts and again
  after every restart, and memory starts empty each time. `Boot` rebuilds the map from the log.
- `s.W.Disk.Volume(n)` is the node's simulated disk. It keeps its files across crashes, but a write
  is durable only after a successful `Sync`.
- `s.W.Net.Handle` installs the handler that receives messages. The handler appends the put to the
  log, stores it in memory and sends the acknowledgment at once. That is the bug.
- `n.After` schedules the `flush` timer on the node's clock. Every 5 ms of virtual time, `flush`
  syncs the log.
- A crash can cut the last record of the log in half. `load` drops a record that has no final
  newline.
- The `Describe` methods give each message a readable text in the trace, such as `put k1=v1`.

## Write the client

Add the client to the end of `kv.go`:

```go
// Client writes keys k1 to kN one at a time, retries a put every 50 ms until it is
// acknowledged, and records each put in the world's history.
type Client struct {
	W      *faultline.World
	Server kernel.NodeID
	Puts   int
	Acked  map[string]string
}

// Boot runs at every boot of the client node.
func (c *Client) Boot(n *kernel.Node) {
	c.Acked = map[string]string{}
	i := 0
	var cur Put
	var op int64
	var retry kernel.EventID
	var send func()
	send = func() {
		c.W.Net.Send(n, c.Server, cur)
		retry = n.After(50*time.Millisecond, "retry", send)
	}
	next := func() {
		if i++; i > c.Puts {
			return
		}
		cur = Put{Key: "k" + strconv.Itoa(i), Value: "v" + strconv.Itoa(i)}
		op = c.W.History.Invoke(n.Name(), "put", cur.Key+"="+cur.Value)
		send()
	}
	c.W.Net.Handle(n, func(_ kernel.NodeID, msg any) {
		if a := msg.(Ack); a.Key != cur.Key || i > c.Puts || c.Acked[a.Key] != "" {
			return
		}
		n.Cancel(retry)
		c.Acked[cur.Key] = cur.Value
		c.W.History.Complete(op, history.OK, nil)
		n.After(kernel.Uniform(n.Rand(), 0, 40*time.Millisecond), "next", next)
	})
	n.Post("next", next)
}

// Lost returns an error for the first acknowledged put, in key order, that the server
// does not hold while it is up.
func (c *Client) Lost(s *Server, server *kernel.Node) error {
	if server.State() != kernel.NodeUp {
		return nil
	}
	for _, k := range slices.Sorted(maps.Keys(c.Acked)) {
		if got := s.Data[k]; got != c.Acked[k] {
			return fmt.Errorf("%s=%s was acknowledged, but n1 holds %q", k, c.Acked[k], got)
		}
	}
	return nil
}

// Done returns an error unless every put was acknowledged.
func (c *Client) Done() error {
	if len(c.Acked) != c.Puts {
		return fmt.Errorf("%d of %d puts acknowledged", len(c.Acked), c.Puts)
	}
	return nil
}
```

The client sends one put, resends it every 50 ms until the acknowledgment arrives, and then waits 0
to 40 ms before the next put. It draws that wait from `n.Rand()`, the node's random stream, so the
seed decides it. `Invoke` and `Complete` record each put in the run's history.

`Lost` and `Done` are the checks. `Lost` compares the acknowledged puts with the server's memory,
and it sorts the keys so that its error names the same key in every run. `Done` checks that every
put was acknowledged.

## Write the test

Create `kv_test.go`:

```go
package kv_test

import (
	"testing"
	"time"

	"example.com/kv"
	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/fault"
)

var kvOptions = faultline.Options{Seeds: 50, Duration: 10 * time.Second}

func TestKV(t *testing.T) { faultline.Run(t, kvOptions, kvWorld) }

// kvWorld builds one simulated world. faultline calls it once per run.
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

`faultline.Run` runs 50 seeds, each for 10 seconds of virtual time. For every run it calls
`kvWorld`, which builds a fresh world:

- `w.AddServer` adds the server `n1`, and `w.AddClient` adds the client `c1` with 200 puts.
- `WriteFileDurable` creates an empty, durable `/wal` before the server's first boot.
- `fault.Random` crashes the server about once a second and restarts it 10 to 500 ms later.
  `MaxDown: 1` lets it take down the only server.
- `w.Invariant` runs `Lost` after every event. `w.Final` runs `Done` once, at the end of a run that
  did not fail. In the last quarter of the run the planner starts no new crashes, so the client can
  finish.

## Run the test

Run `go test` in the module:

```sh
go test ./...
```

```text
--- FAIL: TestKV (0.05s)
    kv_test.go:14: faultline: running 50 seeds from base 0x75c66bf05fc438d4 (test name)
    --- FAIL: TestKV/seed=0x287372ab06f1482e (0.05s)
        faultline: invariant "acked puts survive" violated at t=5.873034010s (event 1455)
          k169=v169 was acknowledged, but n1 holds ""
        replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
        artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
    kv_test.go:14: faultline: stopping after failing seed 0x287372ab06f1482e; 49 of 50 seeds not run (set Options.KeepGoing to run all)
FAIL
FAIL	example.com/kv	0.766s
FAIL
```

faultline derives the 50 seeds from the test name, so every machine runs the same ones. The first
seed, `0x287372ab06f1482e`, failed. Your run fails on the same seed, at the same virtual time and
event number. Apart from the timings, only the artifact path can differ. It sits in a folder named
`faultline-` plus your user ID, under your system's temporary directory.

## Read the report

The failure says which check failed, when, and how to replay it. The same lines are in `report.txt`
in the artifact directory:

```sh
cat /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/report.txt
```

```text
--- FAIL: TestKV/seed=0x287372ab06f1482e
    faultline: invariant "acked puts survive" violated at t=5.873034010s (event 1455)
      k169=v169 was acknowledged, but n1 holds ""
    replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
    artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
```

The invariant failed at virtual time 5.873034010s, after event 1455 of the run. The client had an
acknowledgment for `k169`, but the server's memory had no value for it.

## Replay the seed

Run the replay command from the report:

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

The replay fails the same check at the same virtual time and the same event. It is the same run,
event for event, so you can debug it as often as you need.

## Find the cause

`timeline.txt` in the artifact directory lists every record of the run, one per line. Each line has
the record's number, the virtual time, the node, the record's kind and its text. The node carries
its incarnation: `n1#6` is the server's sixth boot. `<-3589` at the end names the record that caused
it. `!` marks the failure, and `*` marks the records that led to it.

Search for the `!` line and read upward. These are the lines around `k169`, with a gap before the
restart:

```text
* 3585 5.548233266s n1#6   kernel.event       flush id=1613 <-3578
* 3586 5.548233266s n1#6   disk.sync          sync /wal ops=0 size=1464 path=/wal ops=0 size=1464 <-3585
* 3587 5.551630705s global kernel.event       net.deliver id=1615 <-3584
* 3588 5.551630705s n1#6   net.deliver        deliver #362.1 c1 -> n1 msg=362 copy=1 from=c1 to=n1 latency_ns=4939839 <-3587
* 3589 5.551630705s n1#6   disk.write         append /wal off=1464 len=10 path=/wal op=append off=1464 len=10 <-3588
* 3590 5.551630705s n1#6   net.send           send #363 n1 -> c1: ack k169 msg=363 from=n1 to=c1 payload="ack k169" <-3589
* 3591 5.551909068s global kernel.event       fault/random/start id=1371 <-3043
* 3592 5.551909068s global fault.target       target random rules[0] crash -> n1 planner=random rule=0 candidates=n1 chosen=n1 <-3591
* 3593 5.551909068s n1#6   fault.crash        crash n1 id=11 source=random node=n1 effect=applied <-3592
* 3594 5.551909068s n1#6   kernel.crash       crash from=up <-3593
* 3595 5.551909068s n1#6   disk.crash_meta    crash metadata strict: kept 0 of 0 model=strict pending=0 kept=0 <-3594
* 3596 5.551909068s n1#6   disk.crash_apply   crash /wal torn: kept 0 of 1 path=/wal model=torn pending=1 kept=0 torn_sectors=0 size=1464 <-3595
* 3597 5.553747583s global kernel.event       net.deliver id=1618 <-3590
* 3598 5.553747583s c1#1   net.deliver        deliver #363.1 n1 -> c1 msg=363 copy=1 from=n1 to=c1 latency_ns=2116878 <-3597
* 3599 5.553747583s c1#1   history.complete   c1 ok put null id=169 process=c1 f=put status=ok output=null <-3598
...
* 3626 5.873034010s n1#6   fault.restart      restart n1 id=12 source=random node=n1 undoes=11 effect=applied <-3625
* 3627 5.873034010s n1#7   kernel.boot        boot <-3626
* 3628 5.873034010s n1#7   net.handle         handler installed on n1 node=n1 installed=true <-3627
! 3629 5.873034010s global check.violation    invariant "acked puts survive" violated kind=invariant check="acked puts survive" error="k169=v169 was acknowledged, but n1 holds \"\"" event=1455 <-3628
```

From the bottom up, these lines tell the whole story:

1. At 5.873034010s the server restarts, boots for the seventh time, and fails the invariant.
2. At 5.553747583s the client receives the acknowledgment for `k169` and records the put as done.
3. At 5.551909068s the planner crashes the server. The disk has one unsynced write on `/wal`, and
   the crash keeps none of it.
4. At 5.551630705s, 0.3 ms before the crash, the server appended `k169` to `/wal` and sent the
   acknowledgment at once.
5. The last sync was at 5.548233266s, before `k169` arrived.

The acknowledgment left the server before the write was durable. To see the same run as an
interactive page, open `timeline.html` from the artifact directory in a browser.

## Fix the bug

Sync the log before you acknowledge a put. Replace the `Boot` method in `kv.go` with this version,
which syncs in the handler and no longer needs the `flush` timer:

```go
// Boot runs at every boot of the node: its first start and every restart.
func (s *Server) Boot(n *kernel.Node) {
	vol := s.W.Disk.Volume(n) // the same volume in every incarnation
	s.Data = load(vol)        // memory starts empty: rebuild it from the disk
	wal, err := vol.Open("/wal")
	if err != nil {
		n.Sim().Fail(err)
		return
	}
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		p := msg.(Put)
		if _, err := wal.Append([]byte(p.Key + "=" + p.Value + "\n")); err != nil {
			return // no ack: the client retries
		}
		if err := wal.Sync(); err != nil {
			return // not durable: no ack, so the client retries
		}
		s.Data[p.Key] = p.Value
		s.W.Net.Send(n, from, Ack{Key: p.Key})
	})
}
```

A put now gets its acknowledgment only after the sync that makes it durable. The test injects
crashes only. A failed sync needs more care, because the write it dropped leaves a gap in the log.
Add a `fault.KindSyncFail` rule with `Magnitude: 1` to the planner, and faultline finds that bug
too.

## Prove the fix

Run the same replay command again:

```sh
FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
```

```text
=== RUN   TestKV
    kv_test.go:14: faultline: FAULTLINE_SEED=0x287372ab06f1482e: running 1 seed
=== RUN   TestKV/seed=0x287372ab06f1482e
    faultline: seed 0x287372ab06f1482e passed; kept the failing artifact at /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/, which recorded invariant:acked puts survive
    warning: the test binary differs from the one that recorded the previous artifact (code, dependencies or build flags changed)
--- PASS: TestKV (0.02s)
    --- PASS: TestKV/seed=0x287372ab06f1482e (0.01s)
PASS
ok  	example.com/kv	1.130s
```

The seed that failed now passes. faultline keeps the failing artifact, because a passing run never
replaces one. The warning is expected: you changed the code since the artifact was written.

Then run every seed, and run every seed twice to check that each run depends only on its seed:

```sh
go test ./...
FAULTLINE_CHECK_DETERMINISM=1 go test ./...
```

```text
ok  	example.com/kv	0.720s
ok  	example.com/kv	0.987s
```

## Pin the seed

Keep the seed as a regression test. Create `pin_test.go`:

```go
package kv_test

import (
	"testing"

	"github.com/hmdsefi/faultline"
)

// TestKVSeed287372ab replays the seed that lost an acknowledged put before the fix.
func TestKVSeed287372ab(t *testing.T) {
	t.Setenv("FAULTLINE_SEED", "0x287372ab06f1482e")
	t.Setenv("FAULTLINE_SEED_LIST", "") // FAULTLINE_SEED_LIST and FAULTLINE_SEED cannot both be set
	faultline.Run(t, kvOptions, kvWorld)
}
```

The test sets `FAULTLINE_SEED` for itself and reuses the options and the world function of `TestKV`.
It clears `FAULTLINE_SEED_LIST`, because faultline stops with an error when both variables are set.

Run it:

```sh
go test -run '^TestKVSeed287372ab$' .
```

```text
ok  	example.com/kv	0.721s
```

With the fixed code, the seed no longer produces the run that failed, because the code changed. If
the bug comes back, the test catches it: with the old `Boot` method, it fails with the original
failure at 5.873034010s.

## Next steps

- [Read a failure artifact](guides/read-an-artifact.md): every file a failing seed leaves, and the
  timeline page.
- [Replay a failing seed](guides/replay-a-seed.md): replay warnings, and how to narrow a failure by
  editing its fault schedule.
- [How faultline works](how-it-works.md): virtual time, the simulated network and disk, faults, and
  the rules your code follows.
