# Replay goroutine code exactly

A goroutine-mode seed replays exactly when you build the test through `faultline-toolexec`, the
wrapper of faultline's *toolexec backend*. The wrapper rewrites your packages at build time, so that
goroutine order, `select` choices, map order and `math/rand` come from the seed. A failing seed then
replays with the same trace hash, as an event-mode seed does, outside the cases that [What exact
replay does not cover](#what-exact-replay-does-not-cover) lists. This guide sets up the backend,
runs a test under it, and lists what it still does not control.

## What best-effort replay leaves open

Without the backend, faultline controls time, the network, the disk and the faults in goroutine
mode. Requests that processes make at the same virtual instant run in an order fixed by node and
by a key the call chooses; only requests with the same node and key keep their arrival order. Some
choices stay with the Go runtime:

- the order in which goroutines woken by the same event run;
- the package-level functions of `math/rand` and `math/rand/v2`;
- the order of a `range` over a map;
- the case a `select` picks when several are ready.

Each of these can make two runs of one seed differ. A failing seed always runs a second time to
write its artifact, and `FAULTLINE_CHECK_DETERMINISM=1` runs passing seeds twice. When the trace
hashes differ, the seed fails as a determinism failure. A difference that changes nothing a process
sends, writes or logs leaves the trace hash alone and goes unnoticed.

## Set up the backend

You need a goroutine-mode test, such as the one in [Test goroutine code](goroutine-mode.md), and
faultline v0.2.0 or newer. Run these steps in the module that holds the test:

1. Add the wrapper as a tool of your module:

   ```sh
   go get -tool github.com/hmdsefi/faultline/runner/toolexec/cmd/faultline-toolexec
   ```

   The `tool` line in `go.mod` keeps the `runner/toolexec` module required, also after
   `go mod tidy`. The next steps read its scheduler package from there.

2. Install the wrapper:

   ```sh
   go install github.com/hmdsefi/faultline/runner/toolexec/cmd/faultline-toolexec
   ```

   Run inside the module, `go install` builds the version that `go.mod` requires. It puts the binary
   in `$(go env GOPATH)/bin`, or in `$GOBIN` when it is set. That folder must be on your `PATH`.

3. Prepare the scheduler for your Go toolchain:

   ```sh
   faultline-toolexec prepare
   ```

   `prepare` compiles the scheduler package, with and without `-race`, and writes what the wrapper
   needs to a state directory: `faultline-toolexec` in your user cache directory, or the absolute
   path in `FAULTLINE_TOOLEXEC_STATE`. Run it again after you change the Go toolchain or the
   faultline version, and when a build says that the build cache was cleaned or trimmed.

4. Build and run the tests through the wrapper:

   ```sh
   go test -toolexec=faultline-toolexec ./...
   ```

   The first build compiles the standard library again through the wrapper, so it takes longer than
   the builds after it.

Pass the installed binary to `-toolexec`. The form `-toolexec="go tool faultline-toolexec"` fails
with `go: no such tool "faultline-toolexec"`: the go command runs the assembler for each
standard-library package with assembly files, such as `sync/atomic`, in that package's own folder,
outside your module, where `go tool` cannot find your module's tools.

## Run a failing seed exactly

The store of [Test goroutine code](goroutine-mode.md), without the `SyncDir` call in `Open`, fails
the same seed under the backend:

```sh
go test -toolexec=faultline-toolexec -run '^TestKVKeepsAcknowledgedWrites$' .
```

```text
faultline: TestKVKeepsAcknowledgedWrites/seed=0xa0aba4d73b141760: goroutine mode; replay: FAULTLINE_SEED=0xa0aba4d73b141760 go test -v -toolexec=faultline-toolexec -run '^TestKVKeepsAcknowledgedWrites$' example.com/kv
faultline: TestKVKeepsAcknowledgedWrites/seed=0x6e1023a46368738d: goroutine mode; replay: FAULTLINE_SEED=0x6e1023a46368738d go test -v -toolexec=faultline-toolexec -run '^TestKVKeepsAcknowledgedWrites$' example.com/kv
--- FAIL: TestKVKeepsAcknowledgedWrites (0.07s)
    kv_test.go:101: faultline: running 100 seeds from base 0x7e70256660a1f7d2 (test name)
    --- FAIL: TestKVKeepsAcknowledgedWrites/seed=0x6e1023a46368738d (0.05s)
        faultline: final check "acknowledged writes survive crashes" failed after the run at t=30.000000000s
          54 acknowledged writes lost, the first: k0=v0 (read 404 "404 page not found\n")
        replay:    FAULTLINE_SEED=0x6e1023a46368738d go test -v -toolexec=faultline-toolexec -run '^TestKVKeepsAcknowledgedWrites$' example.com/kv
        artifacts: /tmp/faultline-501/example.com_kv/TestKVKeepsAcknowledgedWrites/6e1023a46368738d/
          determinism: exact (toolexec backend)
    kv_test.go:101: faultline: stopping after failing seed 0x6e1023a46368738d; 98 of 100 seeds not run (set Options.KeepGoing to run all)
FAIL
```

Two things differ from the plain build. The replay commands carry `-toolexec=faultline-toolexec`,
so the replay builds the same rewritten binary. The last line says
`determinism: exact (toolexec backend)` instead of `level 3`. In `report.json`,
`determinism_level` is `exact` and `exact_backend` is `toolexec`. For this test, the trace hash of
the seed, `0xe22ed0d233124725`, is the same with and without the backend.

A replay command needs the prepared state for the same Go toolchain. On another machine, run steps 2
and 3 first.

## Make a nondeterministic test exact

The following client waits a random time between requests, drawn from the global `math/rand/v2`
generator, which the seed does not control:

```go
		w.AddClientProcess("client", func(ctx context.Context, p *faultline.Process) error {
			c := netsim.HTTPClient(p.Net)
			defer c.CloseIdleConnections()
			for ctx.Err() == nil {
				time.Sleep(time.Duration(1+rand.IntN(100)) * time.Millisecond) // global math/rand
				req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://kv:8080/k", strings.NewReader("v"))
				if resp, err := c.Do(req); err == nil {
					resp.Body.Close()
				}
			}
			return nil
		})
```

With `Options.CheckDeterminism` set, the plain build fails the first seed, because its two runs dial
at different virtual times:

```text
        faultline: determinism failure: two runs of seed 0xfb9241718877d7f3 gave trace hashes 0xfa813712552db3f4 and 0x14ca9b950d3c018b
          first difference at record #20:
            run 3: #20 t=0.035000000s node=2#1 driver.request cause=19 "net.dial dial:10.0.0.1:8080" req="2" kind="net.dial" key="dial:10.0.0.1:8080"
            run 4: #20 t=0.004000000s node=2#1 driver.request cause=19 "net.dial dial:10.0.0.1:8080" req="2" kind="net.dial" key="dial:10.0.0.1:8080"
          determinism level 3 (goroutine mode): replay is best-effort; divergence comes from goroutines woken together, global math/rand, map iteration, select, or state shared between runs
          ...
```

Built with `-toolexec=faultline-toolexec`, the same test passes all five seeds, each run twice. The
wrapper turned `rand.IntN` into a draw from a stream of the seed.

## What the wrapper rewrites

The wrapper sees every compile of the test binary. In the packages it rewrites, it changes these
constructs before the compiler reads them:

- goroutine starts, channel sends, receives and closes, and `select`;
- `sync.Mutex`, `sync.RWMutex`, `sync.WaitGroup`, `sync.Once`, `sync.Cond`, `sync.OnceFunc` and its
  relatives;
- the callbacks of `time.AfterFunc` and `context.AfterFunc`, and `runtime.Gosched`;
- `range` over a map whose keys are integers, strings, booleans, or structs and arrays of those, and
  `maps.Keys`, `maps.Values` and `maps.All` over such maps;
- the package-level functions of `math/rand` and `math/rand/v2`, and `reflect.Select`.

Each operation that can block or wake a goroutine becomes a *scheduling point*. There the goroutine
waits until faultline's driver releases it, and the driver releases one goroutine at a time, chosen
from a stream of the seed. Map orders, `select` choices and `math/rand` draws come from a second
stream. The rewrite keeps every line number, so stack traces and compiler errors point at your code.

By default the wrapper rewrites every package of your module and of other modules outside the module
cache, such as one you point at with a `replace` directive. It leaves the standard library and the
module cache unchanged. Two variables, comma-separated lists, opt more packages in:

- `FAULTLINE_TOOLEXEC_STD` lists standard-library packages, for example `net/http`.
- `FAULTLINE_TOOLEXEC_DEPS` lists modules, for example `github.com/hashicorp/raft`.

Set them for the `go test` command. The build cache keeps builds with different lists apart. [The `faultline-toolexec` command](../reference/toolexec.md) lists the packages the wrapper
never rewrites and every other variable.

## Coverage gaps

Some constructs can change goroutine order, but the wrapper leaves them unchanged, for example a
`range` over a map with pointer keys, `sync.Map.Range`, or a channel operation in an `else if`
header. The wrapper records each one as a *coverage gap*, with its file and line. The following
process logs its peers in map order, and the map has pointer keys:

```go
type peer struct{ name string }

func TestPeers(t *testing.T) {
	opts := faultline.Options{Mode: faultline.ModeGoroutine, Seeds: 20, Duration: time.Second}
	faultline.Run(t, opts, func(w *faultline.World) {
		w.AddProcess("p", func(ctx context.Context, p *faultline.Process) error {
			peers := map[*peer]bool{{"a"}: true, {"b"}: true, {"c"}: true}
			for q := range peers {
				p.Logf("dial %s", q.name)
				time.Sleep(time.Millisecond)
			}
			return nil
		})
	})
}
```

Built with the backend and run with `FAULTLINE_CHECK_DETERMINISM=1`, the first seed fails:

```text
    --- FAIL: TestPeers/seed=0x89c07f950f6e93da (0.01s)
        faultline: determinism failure: two runs of seed 0x89c07f950f6e93da gave trace hashes 0x7c28a585b144fca6 and 0x282761afd6887320
          the two full-trace runs were identical (trace hash 0xd824c396a5f1f04a); the first two runs both differed
          determinism: exact (toolexec backend): a divergence comes from code the rewriter does not control: 1 coverage gaps in this test binary, packages outside the rewrite set, sync/atomic, or state shared between runs
          common causes: state kept between runs in the same process (package-level variables, sync.Once, caches), map iteration order, global math/rand, wall-clock time, goroutines
          next step: fix the cause; until the seed gives the same run every time, the replay command may not reproduce this failure
        replay:    FAULTLINE_SEED=0x89c07f950f6e93da FAULTLINE_CHECK_DETERMINISM=1 go test -v -toolexec=faultline-toolexec -run '^TestPeers$' example.com/peers
        artifacts: /tmp/faultline-501/example.com_peers/TestPeers/89c07f950f6e93da/
          determinism: exact (toolexec backend)
          coverage gaps: 1 in this test binary; toolexec-gaps.txt in the artifact directory lists them
```

The `determinism: exact` line in the message replaces the `level 3` line of a best-effort seed, and
counts the binary's gaps. `toolexec-gaps.txt` in the artifact directory names each gap with the
absolute path of your file, in this form:

```text
/home/dev/peers/peers_test.go:18: range over a map with unordered keys
```

A failing seed of a binary with gaps prints the `coverage gaps` line, whatever check failed. A gap
does not make every run differ; it marks a place where a difference can start, so look at the
listed lines first. Sorting the keys of such a map, or giving it keys of an ordered type, removes
the gap. [The `faultline-toolexec` command](../reference/toolexec.md#coverage-gaps) lists every kind
of gap.

## When a seed is exact

A goroutine-mode seed is exact when the test binary was built with the backend and `Options.Procs`
is 1, the default. The scheduler orders waiting goroutines by when they started, which is a fixed
order only on one processor. With `Procs` above 1, the seeds run on a best-effort basis, and `Run`
logs this line:

```text
faultline: toolexec backend: Options.Procs is 2; goroutine-mode seeds replay exactly only with Procs 1, so these seeds are best-effort
```

Set `FAULTLINE_EXACT=1` where a test must replay exactly, such as a CI job. A goroutine-mode test
that would not be exact then fails with a setup error before its first seed:

```text
faultline: FAULTLINE_EXACT=1, but this test binary was built without the toolexec backend; build it with go test -toolexec=faultline-toolexec after running faultline-toolexec prepare
faultline: FAULTLINE_EXACT=1, but Options.Procs is 2; exact replay needs Procs 1
```

Event-mode tests are exact without the backend and never get these errors.

## What exact replay does not cover

The scheduler controls the code it rewrote. A seed can still diverge through:

- code outside the rewrite set: the standard library, the module cache, and compiles of sources
  outside a module, such as cgo output and `go test -cover` builds;
- `sync/atomic` operations and data races between goroutines;
- network and file calls of two goroutines that the same scheduling step woke;
- goroutines woken by timers that fire at the same virtual instant, which the Go runtime runs in a
  random order. The callbacks of `time.AfterFunc` and `context.AfterFunc` start in the order they
  were registered. A woken goroutine can still change the order when it does one of these before
  its next scheduling point: start a goroutine, register such a callback, draw from `math/rand`,
  range over a map, or call a `sync.Once` first;
- a goroutine that runs for more than about 10 ms of CPU time without a scheduling point, which the
  Go runtime can preempt;
- state kept between runs in the test binary: package-level variables, `sync.Once` and caches.

Divergence detection stays on. When two runs of an exact seed in one `go test` run differ, the seed
fails as a determinism failure, as in [Coverage gaps](#coverage-gaps). A failing seed always runs
twice, and `FAULTLINE_CHECK_DETERMINISM=1` runs passing seeds twice too.

`net/http` runs goroutines and timers of its own, outside the scheduler unless you opt it in. In
faultline's tests, an HTTP program whose three client goroutines share one `http.Client` diverged
in about 1 of 2,500 seed comparisons with the default rewrite set, and in none of 9,960 with
`FAULTLINE_TOOLEXEC_STD=net/http`. Opt `net/http` in when your test leans on concurrent HTTP
requests.

## Cost

The backend adds a scheduling step at every point it rewrote. On an Apple M4 Max with Go 1.27.3,
taking the median of three runs, the backend made these tests take longer:

- 51 goroutine-mode tests of faultline itself (the root package, `shims/netsim` and
  `shims/grpcsim`): 1.21 times as long;
- 100 seeds of a replicated key-value store with three servers and two HTTP clients: 1.61 times;
- 100 seeds of the HTTP program above: 1.01 times with the default rewrite set, and 3.12 times with
  `net/http` rewritten.

One scheduling point plus its release took 2.7 µs at a stack depth of 0, and 11.7 µs at a depth of
40 frames.

## Run it in CI

A CI job installs and prepares the wrapper before it runs the tests. The following GitHub Actions
steps run the exact tests with the determinism check, and fail if the backend is missing:

```yaml
      - name: Prepare faultline-toolexec
        run: |
          go install github.com/hmdsefi/faultline/runner/toolexec/cmd/faultline-toolexec
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
          "$(go env GOPATH)/bin/faultline-toolexec" prepare
      - name: Test with exact replay
        run: go test -count=1 -toolexec=faultline-toolexec ./...
        env:
          FAULTLINE_EXACT: "1"
          FAULTLINE_CHECK_DETERMINISM: "1"
```

The `GITHUB_PATH` line puts the wrapper on `PATH` for the later steps, where
`go test -toolexec=faultline-toolexec` must find it; their replay commands then say
`-toolexec=faultline-toolexec`. A replay command from a CI artifact runs on your machine once you
have done steps 2 and 3 there.

## Verify it works

Run the tests with the determinism check and `FAULTLINE_EXACT=1`:

```sh
FAULTLINE_EXACT=1 FAULTLINE_CHECK_DETERMINISM=1 go test -toolexec=faultline-toolexec ./...
```

They pass, and no setup error appears. Every goroutine-mode seed ran exactly, twice, with the same
trace hash.

## Next steps

- [The `faultline-toolexec` command](../reference/toolexec.md): the command, its variables, the
  packages it never rewrites, the kinds of coverage gaps and its error messages.
- [Check that a test is deterministic](check-determinism.md): read a determinism failure.
- [Run faultline in CI](ci.md): the rest of a CI setup.
