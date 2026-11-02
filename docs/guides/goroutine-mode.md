# Test goroutine code

This guide runs an ordinary Go service inside faultline: code that starts goroutines, serves
`net/http` over TCP, writes files and calls `time.Sleep`. You add the service as a *process*, give it
the simulated network and disk, and faultline crashes and restarts it in virtual time while your
checks run. The guide also covers gRPC, the options of goroutine mode, and what to do when a run
hangs.

In this mode a failing seed replays on a best-effort basis. [Replay goroutine code
exactly](exact-replay.md) builds the same test so that its seeds replay exactly.

## Prerequisites

You need Go 1.26 or newer, which `go version` reports, and faultline v0.2.0 or newer in your
module, which `go list -m github.com/hmdsefi/faultline` reports. To add or upgrade it:

```sh
go get github.com/hmdsefi/faultline
```

## Pass the service its network and disk

Goroutine mode cannot redirect a call to `net.Listen`, `net.Dial` or `os.Open`: such a call does
real I/O outside the simulation. The code under test therefore takes its listener, its dialer and its
filesystem as values. Each process gets two of them, and `shims/netsim` adds HTTP helpers:

- `p.Net`, a `*netsim.Host` from `shims/netsim`, with `Listen`, `Dial` and `ListenPacket`. It
  returns ordinary `net.Listener`, `net.Conn` and `net.PacketConn` values that run over simulated
  TCP and UDP.
- `p.FS`, an `fsx.FS` from `shims/fsx`, on the node's simulated volume. `fsx.FS` is an interface
  modelled on package `os`: `OpenFile`, `Create`, `Rename`, `Remove`, `ReadDir`, `SyncDir` and the
  rest. In production, `fsx.OS(dir)` implements it on a real directory.
- `netsim.HTTPClient(p.Net)` and `netsim.HTTPTransport(p.Net)`, an `http.Client` and an
  `http.Transport` that dial through `p.Net`.

The following store keeps every write in an append-only log on an `fsx.FS` and serves GET and PUT
over HTTP. Save it as `kv.go`:

```go
// Package kv is a small key-value store served over HTTP. It keeps every write in an append-only
// log and rebuilds its map from the log when it starts.
package kv

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/hmdsefi/faultline/shims/fsx"
)

// Server serves GET and PUT on /<key>.
type Server struct {
	mu   sync.Mutex
	data map[string]string
	log  fsx.File
}

// Open replays log.txt from fsys and opens it for appending.
func Open(fsys fsx.FS) (*Server, error) {
	s := &Server{data: map[string]string{}}
	b, err := fsx.ReadFile(fsys, "log.txt")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "="); ok && strings.HasSuffix(line, "\n") {
			s.data[k] = v
		}
	}
	if s.log, err = fsys.OpenFile("log.txt", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644); err != nil {
		return nil, err
	}
	return s, fsys.SyncDir(".") // make the new file's directory entry durable
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		if _, err := fmt.Fprintf(s.log, "%s=%s\n", key, body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.log.Sync(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.data[key] = string(body)
	case http.MethodGet:
		v, ok := s.data[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, v)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
```

Nothing in the store knows about faultline. Its `main` function in production would call
`kv.Open(fsx.OS("/var/lib/kv"))` and serve on a listener from `net.Listen`.

## Write the test

A goroutine-mode test sets `Options.Mode` to `faultline.ModeGoroutine` and adds processes with
`w.AddProcess`, tagged `server`, or `w.AddClientProcess`, tagged `client`. A process is a function
`func(ctx context.Context, p *faultline.Process) error` that runs on its own goroutine and may start
as many goroutines as it needs. Save the following test as `kv_test.go`:

```go
package kv_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel/fault"
	"github.com/hmdsefi/faultline/shims/netsim"

	"example.com/kv"
)

var opts = faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 100, Duration: 30 * time.Second}

func world(w *faultline.World) {
	var mu sync.Mutex
	var lost []string
	checked := false

	w.AddProcess("kv", func(ctx context.Context, p *faultline.Process) error {
		s, err := kv.Open(p.FS)
		if err != nil {
			return err
		}
		ln, err := p.Net.Listen("tcp", ":8080")
		if err != nil {
			return err
		}
		srv := &http.Server{Handler: s}
		go func() { <-ctx.Done(); srv.Close() }()
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	w.AddClientProcess("client", func(ctx context.Context, p *faultline.Process) error {
		c := netsim.HTTPClient(p.Net)
		c.Timeout = time.Second
		defer c.CloseIdleConnections()
		var keys, vals []string
		start := time.Now()
		for i := 0; time.Since(start) < 20*time.Second; i++ {
			time.Sleep(50 * time.Millisecond)
			key, val := fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://kv:8080/"+key, strings.NewReader(val))
			if resp, err := c.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					keys, vals = append(keys, key), append(vals, val)
				}
			}
		}
		time.Sleep(2 * time.Second) // faults stop at 20 s and kv restarts if it is down
		for i, key := range keys {
			resp, err := c.Get("http://kv:8080/" + key)
			if err != nil {
				return err
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(got) != vals[i] {
				mu.Lock()
				lost = append(lost, fmt.Sprintf("%s=%s (read %d %q)", key, vals[i], resp.StatusCode, got))
				mu.Unlock()
			}
		}
		mu.Lock()
		checked = true
		mu.Unlock()
		<-ctx.Done()
		return nil
	})

	w.Plan(&fault.Random{MaxDown: 1, Quiet: 10 * time.Second, Rules: []fault.Rule{
		{Kind: fault.KindCrash, Every: 4 * time.Second, MinFor: 500 * time.Millisecond, MaxFor: 2 * time.Second},
	}})

	w.Final("acknowledged writes survive crashes", func() error {
		mu.Lock()
		defer mu.Unlock()
		if !checked {
			return errors.New("the client did not finish reading its writes back")
		}
		if len(lost) > 0 {
			return fmt.Errorf("%d acknowledged writes lost, the first: %s", len(lost), lost[0])
		}
		return nil
	})
}

func TestKVKeepsAcknowledgedWrites(t *testing.T) {
	faultline.Run(t, opts, world)
}
```

The test relies on these parts of goroutine mode:

- Each run happens inside a [`testing/synctest`](https://pkg.go.dev/testing/synctest) bubble, so
  `time.Sleep`, `time.Now`, timers and context deadlines in the process follow virtual time. Every
  run starts at the same virtual instant. The 30 seconds of a run take milliseconds of wall time.
- A node's name works as a host name: `http://kv:8080/` reaches the process named `kv`.
- `ctx` is cancelled when the node crashes, when the process function returns, and when the run
  ends. The `kv` process closes its server then, and `Serve` returns.
- The final check runs on faultline's driver goroutine while process goroutines exist, so it reads
  the shared result under a mutex.
- `Quiet` keeps the last 10 seconds free of new faults, and the planner restarts a crashed `kv` when
  that window starts.

## Run it

Run the test:

```sh
go test -run '^TestKVKeepsAcknowledgedWrites$' .
```

```text
ok  	example.com/kv	1.089s
```

Each of the 100 seeds ran 30 seconds of virtual time, with the store crashed and restarted at
random.

## When a seed fails

The `SyncDir` call at the end of `Open` matters. Without it, a crash can drop the directory entry of
a new `log.txt`, together with every write the file holds, although each write was synced. To see a
failure, replace the last four lines of `Open` with these two and run the test again:

```go
	s.log, err = fsys.OpenFile("log.txt", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	return s, err
```

```text
faultline: TestKVKeepsAcknowledgedWrites/seed=0xa0aba4d73b141760: goroutine mode; replay: FAULTLINE_SEED=0xa0aba4d73b141760 go test -v -run '^TestKVKeepsAcknowledgedWrites$' example.com/kv
faultline: TestKVKeepsAcknowledgedWrites/seed=0x6e1023a46368738d: goroutine mode; replay: FAULTLINE_SEED=0x6e1023a46368738d go test -v -run '^TestKVKeepsAcknowledgedWrites$' example.com/kv
--- FAIL: TestKVKeepsAcknowledgedWrites (0.07s)
    kv_test.go:101: faultline: running 100 seeds from base 0x7e70256660a1f7d2 (test name)
    --- FAIL: TestKVKeepsAcknowledgedWrites/seed=0x6e1023a46368738d (0.06s)
        faultline: final check "acknowledged writes survive crashes" failed after the run at t=30.000000000s
          54 acknowledged writes lost, the first: k0=v0 (read 404 "404 page not found\n")
        replay:    FAULTLINE_SEED=0x6e1023a46368738d go test -v -run '^TestKVKeepsAcknowledgedWrites$' example.com/kv
        artifacts: /tmp/faultline-501/example.com_kv/TestKVKeepsAcknowledgedWrites/6e1023a46368738d/
          determinism: level 3 (goroutine mode, best-effort replay)
    kv_test.go:101: faultline: stopping after failing seed 0x6e1023a46368738d; 98 of 100 seeds not run (set Options.KeepGoing to run all)
FAIL
```

The report has the same parts as in event mode: the failed check, the replay command and the
artifact directory. The last line, `determinism: level 3`, says that this seed replays on a
best-effort basis. The first two lines come from standard error: before each goroutine-mode seed,
faultline prints the seed's replay command there, so a panic that ends the whole test binary still
names the seed that ran.

The trace records every connection, segment and datagram of a process, and every change it makes
to a file. `timeline.txt` in the artifact directory shows the file's creation, the crash, and the
directory entry that the crash dropped:

```text
    12  0.000000000s kv#1     disk.create          create /log.txt path=/log.txt truncated=false <-11
   923  3.064925858s kv#1     fault.crash          crash kv id=1 source=random node=kv effect=applied <-922
*  925  3.064925858s kv#1     disk.crash_meta      crash metadata strict: kept 0 of 1 model=strict pending=1 kept=0 <-924
```

Put the `SyncDir` lines back, and the test passes again.

## Crashes, restarts and pauses

The faults of event mode apply to processes too. When a process's node crashes:

- its context is cancelled with the cause `faultline.ErrCrashed`;
- its TCP connections are reset and its listeners closed;
- its open files go stale, and the disk applies its crash model to the writes that were not synced;
- every later call to `p.Net`, `p.FS` or `p.Do` from that incarnation fails.

Go cannot stop a goroutine, so the goroutines of the crashed incarnation keep running until they
return. faultline calls them *ghosts*: their network and file calls fail, so they cannot change the
world. A restart runs the process function again, with a new `Process`.

A paused node's network and file calls, and its `p.Do` calls, wait until the node resumes. Clock
faults change what `p.Clock()` reports, the node's local clock. `time.Now` in the process reads
virtual time without them.

A process reaches the rest of the world through its `Process`:

- `p.Rand()` is the node's random stream, safe for concurrent use;
- `p.Logf` adds a line to the trace;
- `p.Do(label, fn)` runs `fn` on the driver goroutine as one event, for example to record a client
  operation with `w.History`;
- `p.Go(fn)` starts a goroutine whose panic fails the run instead of ending the test binary.

## Serve gRPC

The `shims/grpcsim` module connects gRPC clients and servers to `netsim`. It is a module of its own,
so `google.golang.org/grpc` stays out of your build unless you add it:

```sh
go get github.com/hmdsefi/faultline/shims/grpcsim
```

`grpcsim.Listen(p.Net, address)` returns a listener for `(*grpc.Server).Serve`.
`grpcsim.NewClient(p.Net, address)` connects a client with insecure transport credentials and a
reconnect backoff without jitter. Options you pass to `NewClient` override both. The following test
runs a gRPC health service on node `db`, crashes it at random, and checks that the client `app` saw
it serving:

```go
func TestHealth(t *testing.T) {
	opts := faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 20, Duration: 30 * time.Second}
	faultline.Run(t, opts, func(w *faultline.World) {
		w.AddProcess("db", func(ctx context.Context, p *faultline.Process) error {
			ln, err := grpcsim.Listen(p.Net, ":50051")
			if err != nil {
				return err
			}
			srv := grpc.NewServer()
			healthpb.RegisterHealthServer(srv, health.NewServer())
			p.Go(func() { <-ctx.Done(); srv.Stop() })
			return srv.Serve(ln)
		})

		var mu sync.Mutex
		serving := 0
		w.AddClientProcess("app", func(ctx context.Context, p *faultline.Process) error {
			conn, err := grpcsim.NewClient(p.Net, "db:50051")
			if err != nil {
				return err
			}
			defer conn.Close()
			client := healthpb.NewHealthClient(conn)
			for ctx.Err() == nil {
				cctx, cancel := context.WithTimeout(ctx, time.Second)
				resp, err := client.Check(cctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
				cancel()
				if err == nil && resp.GetStatus() == healthpb.HealthCheckResponse_SERVING {
					mu.Lock()
					serving++
					mu.Unlock()
				}
				time.Sleep(100 * time.Millisecond)
			}
			return nil
		})

		w.Plan(&fault.Random{MaxDown: 1, Rules: []fault.Rule{
			{Kind: fault.KindCrash, Every: 5 * time.Second, MinFor: time.Second, MaxFor: 3 * time.Second},
		}})
		w.Final("app saw db serving", func() error {
			mu.Lock()
			defer mu.Unlock()
			if serving == 0 {
				return errors.New("no health check succeeded")
			}
			return nil
		})
	})
}
```

After a crash, `Serve` can return an error from the closed listener. An error returned after the
node crashed, or after the run ended, does not fail the run; an error from a live process does.

## Files in production and in the test

`shims/fsx` has two implementations of `fsx.FS`. `fsx.OS(root)` works on a real directory and
keeps every path inside `root`, through `os.Root`. `fsx.Sim(volume)` works on a simulated volume,
and `p.FS` is the node's. The helpers `fsx.ReadFile`, `fsx.WriteFile` and `fsx.CreateTemp` work on
either, and `fsx.IOFS(fsys)` gives a read-only `io/fs` view.

The simulated disk keeps the crash behavior of event mode. A write is durable after `Sync` on its
file, and a new name in a directory after `SyncDir` on the directory. A crash can lose the writes
that were not synced, keep a prefix of them, keep some of them, or tear the last one. [How faultline
works](../how-it-works.md#the-simulated-disk) describes the crash models.

`fsx.Conformance()` returns the checks faultline runs against both implementations. Run them
against your own `fsx.FS` to see where it differs from package `os`.

## Options for goroutine mode

These fields of `faultline.Options` apply only in goroutine mode:

- `Procs` sets `GOMAXPROCS` during each run. The default is 1, and exact replay needs 1.
- `Drain` is the virtual time processes get to return after the run ends and their contexts are
  cancelled. The default is 60 seconds.
- `StallTimeout` is the wall-clock time without progress after which faultline reports a stall. The
  default is 60 seconds, and a negative value turns the watchdog off.
- `FailOnLeak` turns goroutines still blocked after the drain into a failure of kind `leak`.
- `NetSim` configures the simulated TCP and UDP: segment size, buffers, backlog, retransmission
  timeouts and loopback delay. The zero value means `netsim.DefaultConfig()`.

`Options.Net` still configures the links between nodes: latency, jitter, loss and the rest.

## When a run hangs or leaks

Virtual time advances only when every goroutine in the bubble is durably blocked, which means that
only another goroutine in the bubble can wake it. A goroutine that waits for real I/O outside
`netsim` and `fsx`, waits for a mutex whose holder waits on the simulated network, or waits on a
channel made outside the bubble keeps that from happening. When the simulation makes no progress
for `StallTimeout`,
faultline prints a report to standard error and to `stall.txt` in the seed's artifact directory,
then ends the test binary. The report gives the seed's replay command, lists the bubble's goroutines
with their stacks, and names what to look for:

```text
faultline: goroutine mode stalled: no driver progress for 1m0s of wall time
  ...
    [sync.Mutex.Lock] / [sync.RWMutex.Lock] / [sync.RWMutex.RLock]: waiting for a mutex whose
        holder is blocked; do not hold a mutex across a blocking netsim, fsx or Process.Do call
    [chan receive] / [chan send] / [select] without "(durable)": a channel created outside the
        bubble, for example a package-level channel made during init
    [IO wait] / [syscall]: real I/O that does not go through netsim or fsx
  ...
```

Goroutines that are still blocked after the drain are a leak. faultline logs them with their stacks
and records them under `leaks` in `report.json`. With `Options.FailOnLeak`, they fail the seed.

## Limits

These hold in every goroutine-mode test:

- A seed replays on a best-effort basis unless you build the test with the toolexec backend.
  [Replay goroutine code exactly](exact-replay.md) lists what each mode controls.
- Code reaches the simulated network and disk only through `p.Net` and `p.FS`, or through values
  built on them. A direct `net.Dial` or `os.Open` does real I/O.
- `netsim` has no TLS, IPv6 addresses, DNS protocol or Unix sockets. You can layer `crypto/tls` over
  a connection yourself.
- A process and an event-mode node can exchange UDP datagrams, not TCP.
- `fsx.Sim` has no symbolic or hard links and no file locks. It ignores permissions, and every
  modification time is the simulator's epoch.
- A goroutine-mode test cannot call `t.Parallel`. A goroutine-mode `Run` or `RunSeeds` never
  overlaps any other `Run` or `RunSeeds` call in the test binary, event mode included.
- `os.Exit`, `log.Fatal` and an unrecovered panic on a goroutine that faultline did not start, such
  as one from a plain `go` statement, end the test binary. The replay line printed before the seed
  names it.

## Verify it works

Run the test with the race detector and the determinism check, which runs every passing seed twice:

```sh
FAULTLINE_CHECK_DETERMINISM=1 go test -race .
```

```text
ok  	example.com/kv	19.298s
```

## Next steps

- [Replay goroutine code exactly](exact-replay.md): build the test with `faultline-toolexec`, so
  its seeds replay exactly.
- [Read a failure artifact](read-an-artifact.md): walk back from the failure in `timeline.txt`.
- [Check that a test is deterministic](check-determinism.md): read a determinism failure in either
  mode.
