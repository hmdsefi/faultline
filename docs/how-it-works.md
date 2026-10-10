# How faultline works

faultline turns one Go test into many simulated runs, and each run replays exactly from its seed.
This page explains what faultline controls in a run and which rules your code follows inside it. It
ends with the files a failing run leaves behind for you to debug.

## One test, many runs

A faultline test is a normal Go test that calls `faultline.Run` with options and a *world function*,
which builds the simulated world. The following test runs a key-value server `n1` and a client `c1`
for 10 seconds of virtual time. It crashes the server at random times and checks after every event
that no acknowledged put is lost:

```go
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

`kv` is a small example package. Its client sends 200 puts, one at a time, retries each one until
the server acknowledges it, and remembers what was acknowledged. The server appears in
[Nodes](#nodes).

`faultline.Run` turns the 50 seeds into 50 subtests named `seed=0x` plus 16 hex digits. For each run
it creates a fresh `World`, calls `kvWorld` to add nodes, faults and checks, and then advances
virtual time to `Options.Duration`. The whole run happens on one goroutine, the seed's subtest.

## Virtual time

*Virtual time* is the simulator's clock. It jumps from one scheduled event to the next instead of
waiting. Its type is `kernel.Time`, a count of nanoseconds since `kernel.Epoch`, which is midnight
UTC on January 1, 2000.

Everything a simulated system does is a callback scheduled at a virtual time: a timer, a message
delivery, a node boot, a fault. The event loop takes the earliest event, sets the clock to its time,
runs the callback, runs your invariants, and repeats. A callback takes zero virtual time, however
long it runs on the CPU.

A run ends when the clock reaches `Options.Duration` or when a check fails. A run that hits
`Options.MaxEvents` (10,000,000 events by default) also ends, and faultline reports it as a likely
livelock or runaway timer.

The clock skips the idle time between events, so a run costs only the CPU time of its callbacks.
The etcd/raft guide gives measured times in [Results of 40,000
seeds](guides/etcd-raft.md#results-of-40000-seeds).

## The seeded scheduler

Events at different virtual times run in time order. Events at the same virtual time run in an order
that comes from the seed. When code schedules an event, the kernel draws a tie-break number for it
from the `kernel/sched` random stream.

Take two messages that arrive in the same nanosecond. One seed runs them in one order, and another
seed may run them in the other. A set of seeds explores different interleavings of the same test
this way.

Fault events are the exception. A fault goes to the front of its instant and draws nothing from
`kernel/sched`. It runs before the other events of the same virtual time. A replay of a recorded
fault schedule therefore makes the same scheduling decisions as the run that recorded it. [Replay a
failing seed](guides/replay-a-seed.md#when-a-schedule-replay-is-not-exact) lists two exceptions.

## Named random streams

Every random choice in a run comes from a *stream*: a random number generator seeded from the run's
seed and a label. Each subsystem draws from its own streams:

- the network: one stream per directed link, labeled `net/link/<from>/<to>`;
- the disk: one stream per node, labeled `disk/<node>`;
- a fault planner: `fault/<name>`;
- a node: `node/<name>/<incarnation>`, which `n.Rand()` returns;
- your world function: `workload/<label>`, which `w.Rand(label)` returns.

Separate streams keep one subsystem's draws from shifting another's. One more message on a link
changes that link's draws, and every other link draws the same sequence of numbers as before.

faultline makes every random decision with integers. A probability is a number of parts per million
(`kernel.Chance(r, ppm)`), and a random duration is a whole number of nanoseconds
(`kernel.Uniform(r, min, max)`). Your code can call the same two helpers.
[Architecture](architecture.md#named-streams-and-integer-decisions) explains the choice.

## Nodes

A *node* is one simulated machine. It has a name, tags such as `server` or `client`, a state (up,
down or paused), a local clock and a disk volume. `w.AddServer` and `w.AddClient` add a node with a
boot function. faultline calls the boot function at the node's first start and again at every
restart. The boot function installs everything the node does.

[Get started](getting-started.md) builds two versions of `kv.Server`: one acknowledges a put before
it syncs the log, and the fix syncs before every acknowledgment. A third version holds each
acknowledgment until the next sync. It boots like this:

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
	var acks []Ack
	var to []kernel.NodeID
	s.W.Net.Handle(n, func(from kernel.NodeID, msg any) {
		p := msg.(Put)
		if _, err := wal.Append([]byte(p.Key + "=" + p.Value + "\n")); err != nil {
			return // no ack: the client retries
		}
		s.Data[p.Key] = p.Value
		acks, to = append(acks, Ack{Key: p.Key}), append(to, from)
	})
	var flush func()
	flush = func() {
		if wal.Sync() == nil {
			for i, a := range acks {
				s.W.Net.Send(n, to[i], a)
			}
			acks, to = nil, nil
		}
		n.After(5*time.Millisecond, "flush", flush)
	}
	n.After(5*time.Millisecond, "flush", flush)
}
```

The server appends each put to its log, and every 5 ms it syncs the log and only then sends the
acknowledgments. The handler, the timer and the open file all belong to the current boot.

Each boot starts a new *incarnation* of the node. A crash takes the node down at once:

- faultline drops the events that the incarnation scheduled with `n.After` or `n.Post`;
- the network removes the node's message handler;
- the disk applies its crash model to unsynced writes, and the node's open files go stale.

The next boot starts from what the volume holds. Go values from the old incarnation stay in memory,
but no event reaches them any more.

A paused node models a long garbage collection pause or a stalled virtual machine. Its events and
incoming messages wait, and run in their original order when the node resumes.

Each node also has a local clock with an offset and a drift in parts per million. `n.Now()` reads
that clock, `n.After(d, ...)` measures `d` on it, and the `clock-jump` and `clock-drift` faults
change it. Virtual time stays global; only the node's reading of it moves.

## The simulated network

The network carries messages between nodes the way UDP carries datagrams: a message arrives after a
delay, sometimes twice, or not at all. `w.Net.Send(n, to, payload)` sends from node `n`, and the
handler that the destination installed with `w.Net.Handle` receives it. The payload is passed by
reference and never copied, so send values that nobody changes after sending.

Each directed link has a latency, a uniform jitter, an optional tail delay, a drop probability, a
duplication probability and an optional FIFO guarantee. The default link has 1 ms of latency and up
to 4 ms of jitter, with no FIFO guarantee. Two messages on one link can therefore arrive in either
order. `Options.Net` changes the default link, and the `link` fault changes one link during a run.

The network drops a message in these cases:

- its link is cut when it is sent, or when it would arrive;
- the link loses it by chance;
- its destination is down when it arrives;
- the destination has no handler for its current incarnation.

A message to a paused node waits until the node resumes. A message that a node sent before it
crashed is still delivered, unless one of these cases drops it. The trace records every send,
delivery and drop, and each drop names its reason.

## The simulated disk

Each node has one *volume*, a small file system that survives the node's crashes and restarts. Reads
see a write at once, but the write is durable only after a successful `Sync` on its file. When a
node crashes, the volume decides, for each file, what happens to the writes made since the last
sync. It uses one of four crash models:

- all of them are lost;
- a random prefix survives;
- each one survives on its own, so a later write can survive without an earlier one;
- a random prefix survives, and the next write survives in part, sector by sector (a torn write).

By default a crash picks one of the four for each file, from the node's `disk/<node>` stream.

Creating, renaming and removing files follows the same idea. These changes are durable only after
`SyncDir` on their directory, and a crash keeps a random prefix of the ones that were not synced.
That is why the test above creates `/wal` with `WriteFileDurable` before the server's first boot.

A failed `Sync` returns an error. By default, the writes it covered never become durable, even
though reads return them until the next crash. Files opened before a crash return an error when used
after it. Neither the server above nor the fixed one in [Get
started](getting-started.md#fix-the-bug) survives a failed `Sync`: under a `fault.KindSyncFail`
rule, each loses an acknowledged put.

## Faults and fault schedules

A *fault* is something going wrong on purpose. faultline has 16 kinds of fault:

- network: `partition`, `isolate`, `cut`, `heal`, `heal-link`, `link` and `link-reset`;
- node: `crash`, `restart`, `pause`, `resume`, `clock-jump` and `clock-drift`;
- disk: `sync-fail`, `disk-capacity` and `corrupt`.

A *planner* decides faults during the run. `fault.Random` starts faults at random times from rules,
such as "crash a server about once a second, for 10 to 500 ms". A rule can target a *role*, such as
the current leader. A role is a function that the test registers with `w.Role`, and it returns node
IDs at the moment of the fault. `fault.Script` applies a fixed list of faults instead.

`fault.Random` keeps the system able to recover. `MaxDown` caps how many servers are down or paused
at once. The default is a minority of the servers, which is none for a single server, so the test
above sets `MaxDown` to 1. The last quarter of the run is a recovery window by default. When it
starts, the planner heals the network, restarts crashed nodes and resumes paused ones. It starts no
new faults after that, so a final check can test that the system recovered.

Whatever a planner decides, faultline records each fault it applied, with its virtual time, its kind
and its node names. Undos, such as the restart that ends a crash, are faults in the list too. This
list is the *fault schedule*. A failing run writes it to `schedule.json`, and
`FAULTLINE_SCHEDULE=<path>` replays it with the planners turned off. The schedule is plain JSON, so
you can delete faults from a copy and replay the rest to see which ones the failure needs.

## Invariants and final checks

A check is a Go function that returns an error when the system is wrong. faultline runs two kinds:

- `w.Invariant(name, fn)` runs after every event. It must be cheap and must not change the world.
  The first error stops the run at the event that broke the rule, so the failure names the exact
  virtual time and event number.
- `w.Final(name, fn)` runs once, after a run that reached its end without failing. It suits checks
  that cost too much to run after every event, or that hold only after recovery, such as "every put
  was acknowledged".

Code under test can also stop the run with `w.Sim.Fail(err)`. A panic in a callback fails the run
with its stack, and a run that exceeds `Options.MaxEvents` fails as a likely livelock. Each failure
gets a kind, such as `invariant`, `final`, `panic` or `limit`, and a signature made of the kind and
the check name. Seeds that fail the same check share a signature.

The world also keeps a history of client operations. `w.History.Invoke` and `w.History.Complete`
record what each client asked for and what it got back, with virtual times, and a failing run writes
them to `history.jsonl`. Checks over the history are your own invariants and final checks.

## Seeds and replay

A *seed* is one 64-bit number, and every random choice of a run comes from it. By default
`faultline.Run` derives 20 seeds, or 5 under `go test -short`, from a base seed: a hash of the test
name. Every machine and every CI job therefore runs the same seeds.

`Options.Seeds` and `Options.BaseSeed` change the list in code. Environment variables change it
without a code change:

- `FAULTLINE_SEED` runs one seed;
- `FAULTLINE_SEEDS` sets the number of seeds;
- `FAULTLINE_EXPLORE=1` picks a fresh base seed and prints it.

A *replay* runs a seed again and gets the same run: the same events, in the same order, at the same
virtual times. faultline checks this with the *trace hash*, a hash of every record the run emits.
When a seed fails, faultline runs it a second time with every record kept, to write the artifact,
and compares the two trace hashes. If they differ, the test depends on something outside the seed,
and faultline reports a determinism failure that names the first run's failure.

A replay is exact while four things stay the same: the faultline version, the Go minor version, your
code and the options. `report.json` records all four. When you replay a seed with `FAULTLINE_SEED`,
faultline compares them with the artifact that seed left behind and warns about each difference.

## The determinism rules your code follows

faultline controls time, scheduling, the network, the disk and its own random streams. It cannot see
what the rest of your Go code does, so code that runs inside the simulation follows these rules:

- Read time with `n.Now()` and wait with `n.After`. Never call `time.Now`, `time.Sleep` or
  `time.After`, and never use Go timers or tickers.
- Draw random numbers from `n.Rand()` or `w.Rand(label)`. Never call the package-level functions of
  `math/rand` or `math/rand/v2`.
- Start no goroutines and use no `select`. All work is a callback on the simulator's goroutine.
- Sort map keys before you range over a map, whenever the order can change what the code does or the
  text it produces.
- Send messages with `w.Net` and keep files on the node's volume. Real sockets and files are outside
  the simulation.
- Build all state inside the world function. Package-level variables, `sync.Once` and caches carry
  state from one run into the next.
- Give each message type a `Describe() string` method, so the trace shows what the message holds.
  Without one, the trace shows only the type name of a struct.

`crypto/rand` is the one outside source faultline handles for you. `faultline.Run` seeds it for
every run through `testing/cryptotest`. Go does not allow that in a parallel test, so a parallel
test that calls `faultline.Run` fails with a setup error. `Options.NoCryptoSeed` turns the seeding
off and allows `t.Parallel`, but `crypto/rand` is then no longer part of the replay.

faultline does not inspect your code for these rules. It checks their effect instead. When a broken
rule changes the run, a second run of the same seed gets a different trace hash. Failing seeds
always run twice. To run passing seeds twice as well, set `FAULTLINE_CHECK_DETERMINISM=1`. [Check
that a test is deterministic](guides/check-determinism.md) shows how to read a determinism failure.

## What a failure leaves behind

A failing seed fails its subtest. faultline prints the failure, a command that replays the seed, and
the path of the seed's *artifact directory*. It writes that folder from the second, full-trace run:
the report, the trace, a text timeline, the fault schedule, the client history and an interactive
timeline page. [Read a failure artifact](guides/read-an-artifact.md) shows which file answers which
question, and [Environment variables](reference/environment.md#faultline_artifacts) says where the
folder goes.

## Next steps

- [Architecture](architecture.md): the packages and why they are built this way.
- [The Go API on pkg.go.dev](https://pkg.go.dev/github.com/hmdsefi/faultline): every option, method
  and type.
