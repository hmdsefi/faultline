# The faultline-toolexec command

`faultline-toolexec` is the `-toolexec` wrapper of faultline's toolexec backend, which makes
goroutine-mode seeds replay exactly. This page lists how to install it, how the go command calls it,
the state and variables it reads, which packages it rewrites, the kinds of coverage gaps, and its
error messages. [Replay goroutine code exactly](../guides/exact-replay.md) shows how to use it.

## Install

The wrapper lives in the nested module `github.com/hmdsefi/faultline/runner/toolexec`. In the module
that holds your tests, add it as a tool and install it:

```sh
go get -tool github.com/hmdsefi/faultline/runner/toolexec/cmd/faultline-toolexec
go install github.com/hmdsefi/faultline/runner/toolexec/cmd/faultline-toolexec
```

The `tool` line keeps the module required, so `prepare` finds the scheduler package that the
rewritten code calls. Keep the installed binary and the required module at the same version: after
you upgrade faultline, run both commands and `prepare` again. Do not pass
`-toolexec="go tool faultline-toolexec"`: the go command runs the assembler for standard-library
packages with assembly files in their own folders, outside your module, where `go tool` cannot find
the tool.

## Usage

The command has two forms:

```text
faultline-toolexec prepare
faultline-toolexec <tool> [args...]
```

`prepare` takes no other argument and runs in a module that requires `runner/toolexec`. It compiles
the scheduler package for the current Go toolchain, once plain and once with `-race`, and writes
three files to the state directory. Their names start with `<go version>-<GOOS>-<GOARCH>`, such as
`go1.27.1-darwin-arm64`. When the `-race` build fails, for example without cgo, `prepare` prints
`faultline-toolexec: prepare: -race state not written: <reason>` and writes the rest.

The prepared files point at export data in the go build cache. Run `prepare` again after you change
the Go toolchain, upgrade faultline, or clean the build cache. The cache also trims entries that
went unused for some days; a build then stops with the message that the export file is missing.

The go command calls the second form for every toolchain program it runs, after
`go test -toolexec=faultline-toolexec` or `go build -toolexec=faultline-toolexec`:

- A compile of a package in the rewrite set gets rewritten source files, and records the package's
  coverage gaps.
- A compile of a test main adds an import of the scheduler, so every test binary built through the
  wrapper links it.
- A link adds the prepared scheduler and records the wrapper's path for replay commands.
- Every other call runs the tool unchanged.

The wrapper adds `faultline-toolexec=<version>,sched=<hash>` to the version line of every tool,
plus the two opt-in lists when they are set. The go command keys its build cache on that line, so a
build through the wrapper, a new `prepare`, or another opt-in list never reuses a package compiled
under different conditions.

The wrapper exits with the tool's exit code. For a failure of its own, it prints
`faultline-toolexec: <message>` to standard error and exits with code 2.

## Variables

The wrapper and `prepare` read these variables:

| Variable | Default | Meaning |
|---|---|---|
| `FAULTLINE_TOOLEXEC_STATE` | `faultline-toolexec` in the user cache directory | the state directory; it must be an absolute path |
| `FAULTLINE_TOOLEXEC_STD` | none | standard-library packages to rewrite, comma-separated |
| `FAULTLINE_TOOLEXEC_DEPS` | none | modules whose module-cache packages are rewritten, comma-separated |
| `FAULTLINE_TOOLEXEC_KEEP` | none | a directory to copy every rewritten or generated file to, for debugging |
| `FAULTLINE_TOOLEXEC_LOG` | none | a file; the wrapper appends one line per compile and link |

The user cache directory is `~/Library/Caches` on macOS and `$XDG_CACHE_HOME` or `~/.cache` on
Linux. Two checkouts of faultline that share a state directory use whichever scheduler was prepared
last; give each its own `FAULTLINE_TOOLEXEC_STATE`.

Each log line has one of these forms: `compile <import path> rewritten`, `compile <import path>
unchanged`, `compile <import path> testmain`, `compile <import path> outside-module` and
`link <import path>`.

## The rewrite set

For each compile, the wrapper decides from the package's import path and the location of its
files. The first rule that applies wins:

1. Never rewritten: the scheduler's own packages, the standard library unless the package is listed
   in `FAULTLINE_TOOLEXEC_STD`, and the module cache unless the package's module is listed in
   `FAULTLINE_TOOLEXEC_DEPS`. A test main only gets the scheduler import.
2. Not rewritten, with a coverage gap: a compile whose files lie outside any module, outside the
   standard library and outside the module cache. Such files come from cgo, from `go test -cover`,
   which instruments sources in a temporary directory, and from GOPATH mode.
3. faultline's own modules: only their `_test.go` files.
4. Every other package, and every listed package: all files.

The test variants of a package follow the package's rule.

An entry of `FAULTLINE_TOOLEXEC_STD` must be a standard-library import path. `unsafe`, `syscall`,
`sync`, `sync/atomic`, `reflect`, `runtime`, `testing`, the packages below `runtime` and `testing`,
`internal` and `vendor` packages, and the packages the scheduler depends on cannot be rewritten.
`net/http` is the entry faultline tests. An entry of `FAULTLINE_TOOLEXEC_DEPS` must be a module path
whose first element contains a dot, outside `github.com/hmdsefi/faultline`. Empty entries are
ignored.

## What a rewrite changes

In a rewritten file, the wrapper changes:

- `go` statements, channel sends, receives and closes, `for range` over a channel, and `select`;
- `Lock`, `Unlock`, `RLock` and `RUnlock` on `sync.Mutex` and `sync.RWMutex`, including through an
  interface value and as method values, and `RWMutex.RLocker`;
- `WaitGroup.Wait` and `WaitGroup.Go`, `Once.Do`, `Cond.Wait`, `Cond.Signal` and `Cond.Broadcast`,
  and `sync.OnceFunc`, `sync.OnceValue` and `sync.OnceValues`;
- the function passed to `time.AfterFunc` and `context.AfterFunc`, and `runtime.Gosched`;
- `range` over a map whose key is an integer, a string, a boolean, or a struct or array made of
  those, and `maps.Keys`, `maps.Values` and `maps.All` over such a map;
- the package-level drawing functions of `math/rand` and `math/rand/v2`, `rand.N`, and
  `reflect.Select`.

It leaves these unchanged and records nothing: `sync/atomic`, `sync.Pool`, `TryLock` and the other
methods that never block, `WaitGroup.Add` and `WaitGroup.Done`, `*rand.Rand` values your code
creates and seeds, and `runtime.SetFinalizer` and `runtime.AddCleanup`.

A rewritten file keeps the line numbers of the original, so panics, `runtime.Caller` and compiler
errors report the original file and line. The rewrite is a function of the source, its types and
the wrapper version, so a build is reproducible.

## Coverage gaps

A coverage gap is a construct the wrapper leaves unchanged although it can change goroutine order.
Each gap is a line `<absolute file path>:<line>: <kind>`. A failing goroutine-mode seed of a binary
with gaps prints their number, and its artifact directory holds them in `toolexec-gaps.txt`. The
kinds:

| Kind | Construct |
|---|---|
| `range over a map with floating-point keys` | a map whose key type contains a floating-point number |
| `range over a map with unordered keys` | a map with pointer, channel, interface or complex keys, or structs or arrays containing them |
| `range over a channel or map of type-parameter type` | a range over a type parameter with a map in its constraint, or channels and other types mixed |
| `range over a map expression with side effects` | a labeled `range` over a map expression that calls a function |
| `maps iterator with unordered keys or type arguments` | `maps.Keys`, `maps.Values` or `maps.All` with explicit type arguments or unordered keys |
| `maps.DeleteFunc`, `maps.EqualFunc` | a call of that function, which calls a function in map order |
| `sync.Map.Range` | a call of `Range` on a `sync.Map` |
| `reflect map iteration` | `reflect.Value.MapKeys` or `MapRange` |
| `reflect channel operation` | `reflect.Value.Send`, `Recv`, `TrySend`, `TryRecv` or `Close` |
| `method expression <Type>.<Method>` | a method expression of a rewritten `sync` method: `(*sync.Mutex).Lock` gives `method expression Mutex.Lock` |
| `method value of an interface Lock or Unlock` | `l.Lock` or `l.Unlock` taken as a value from an interface |
| `method receiver not nameable in this file` | a `sync` method whose receiver is reached through an unexported embedded field of another package |
| `channel operation in else-if header` | a channel operation in the condition of an `else if` |
| `select named by a goto` | a `select` whose label a `goto` names |
| `select element type not nameable in this file` | a `select` on a channel whose element type the file cannot name |
| `select case with an unexpected receive form` | a receive case other than `<-ch`, `v := <-ch` or `v, ok := <-ch` |
| `select on a channel of unresolved type` | a `select` on a channel whose type does not resolve |
| `go statement argument type not nameable in this file` | a `go` statement with an untyped argument whose type the file cannot name |
| `sources outside a module` | rule 2 of the rewrite set; the line is 1 |

## In reports and replay commands

A test binary built through the wrapper changes what faultline prints for goroutine-mode seeds:

- With `Options.Procs` at 1, the console report says `determinism: exact (toolexec backend)`, and
  `report.json` and `FAULTLINE_RESULTS` lines carry `"determinism_level": "exact"` and
  `"exact_backend": "toolexec"`.
- With `Procs` above 1, `Run` logs `faultline: toolexec backend: Options.Procs is <p>;
  goroutine-mode seeds replay exactly only with Procs 1, so these seeds are best-effort`, and the
  seeds keep the best-effort labels.
- A failing seed prints `coverage gaps: <n> in this test binary` when the binary has gaps, and
  writes `toolexec-gaps.txt`.
- The replay command adds `-toolexec=<wrapper>` after `go test -v`. `<wrapper>` is
  `faultline-toolexec` when the binary of that name on `PATH` is the one that built the test, and
  the wrapper's absolute path otherwise.
- The stall report adds a line for goroutines that reach scheduling points forever without
  blocking, such as a polling loop around a `select` with a `default` case.

`FAULTLINE_EXACT=1` turns a goroutine-mode test that would not be exact into a setup error.
[Environment variables](environment.md) gives its messages.

## Errors

The wrapper's own failures, with what to do about each:

| Message after `faultline-toolexec:` | Fix |
|---|---|
| `run "faultline-toolexec prepare" for <go version> <GOOS>/<GOARCH>` | run `prepare` with that toolchain |
| `run "faultline-toolexec prepare" for <go version> <GOOS>/<GOARCH> with -race support` | run `prepare` where the `-race` build works, with cgo on |
| `<file>: line "<line>": the export file is missing (the go build cache was cleaned or trimmed); run "faultline-toolexec prepare" for ...` | run `prepare` again |
| `the prepared sched does not match this build (another checkout or version of faultline, or other build flags); run "faultline-toolexec prepare" in this module with the build's flags, or give each checkout its own FAULTLINE_TOOLEXEC_STATE` | run `prepare` in the module you build, with the same `GOFLAGS` |
| `FAULTLINE_TOOLEXEC_STATE="<value>": want an absolute path` | set an absolute path |
| `no state directory: <error>; set FAULTLINE_TOOLEXEC_STATE` | set the variable |
| `FAULTLINE_TOOLEXEC_STD: <entry> cannot be rewritten: <reason>` | remove the entry |
| `FAULTLINE_TOOLEXEC_DEPS: <entry> is not a module path outside github.com/hmdsefi/faultline` | list module paths only |
| `prepare: go list github.com/hmdsefi/faultline/runner/toolexec/sched: ...` | run `prepare` in a module that requires `runner/toolexec` |
| `<import path>: <error>` | a package failed to type-check or rewrite; build it without the wrapper to see the compiler's error |

One more failure comes from the scheduler at run time. When an invariant, a final check or the
world function locks a mutex that a blocked process goroutine holds, the run fails with this panic:

```text
faultline: toolexec backend: the driver goroutine (the body, an invariant or a final check) locks a mutex that a blocked goroutine holds; do not lock in the body or a check a mutex that a process holds across a blocking call
```

## Next steps

- [Replay goroutine code exactly](../guides/exact-replay.md): set up the backend and read its
  output.
- [Environment variables](environment.md): the `FAULTLINE_*` variables that `Run` reads.
