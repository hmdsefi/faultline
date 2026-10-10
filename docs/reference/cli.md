# The faultline command

The `faultline` command works on the artifact directory that a failing seed leaves. It regenerates
the rendered files of an artifact and prints its own version. Your tests do not need it:
`faultline.Run` writes every artifact file itself.

## Install

The command needs Go 1.26 or newer, which `go version` reports. Install it with `go install`:

```sh
go install github.com/hmdsefi/faultline/cmd/faultline@latest
```

`go install` puts the binary in `$(go env GOPATH)/bin`, or in `$GOBIN` when it is set. That folder
must be on your `PATH`. Check the installed version:

```sh
faultline version
```

```text
faultline v0.1.0 go1.27.1 darwin/arm64
```

## Usage

Run `faultline` with a command and its arguments. `faultline help` lists the commands:

```text
faultline is a deterministic simulation testing tool for Go.

Usage:

	faultline <command> [arguments]

Commands:

	help     show help for faultline or one of its commands
	render   regenerate timeline.txt, hb.mmd and timeline.html in an artifact directory
	version  print the faultline version

Use "faultline help <command>" for more information about a command.
```

Results and help go to standard output. Errors, and the usage text that follows an error, go to
standard error.

## render

`faultline render` regenerates `timeline.txt`, `hb.mmd` and `timeline.html` in an artifact directory
from its `report.json` and `trace.jsonl`:

```text
usage: faultline render [-slice-cap n] [-max-records n] <dir>
```

Use it to look at a larger causal slice or to fit more records into `timeline.html`. It also
rebuilds the rendered files when you have only `report.json` and `trace.jsonl`, for example from a
CI run. It reads `schedule.json` too when the directory has one.

The flags come before the directory:

| Flag | Default | Effect |
|---|---|---|
| `-slice-cap n` | 200 | the most records in the causal slice, which `timeline.txt` marks with `*` and `hb.mmd` draws; at least 1 |
| `-max-records n` | 50000 | the most records `timeline.html` holds; at least 1000 |

On success, `render` prints the paths it wrote and exits with 0. This example raises the slice cap
of the key-value example's failing seed to 500. Set `dir` to the `artifacts:` path that your failing
seed printed. The example path is from a machine with user ID 501 and the temporary directory
`/tmp`:

```sh
dir=/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e
faultline render -slice-cap 500 "$dir"
```

```text
/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/timeline.txt
/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/hb.mmd
/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/timeline.html
```

After this render, `timeline.txt` marks 500 records. `hb.mmd` keeps to Mermaid's default limits of
50,000 bytes and 500 edges, so it halves the slice until the chart fits. Its second line says what
it drew:

```sh
grep '^slice:' "$dir/timeline.txt"
sed -n 2p "$dir/hb.mmd"
```

```text
slice:    500 records, root 3629, truncated at cap 500
    %% faultline causal slice v1: root=3629 records=250 cap=250 truncated=true
```

`render` writes all three files before it replaces any of them, so a failed render changes nothing.
It changes no other file, except that it removes the temporary files of an interrupted render. When
it cannot read the artifact, it exits with 1:

```text
faultline render: artifact: render /tmp: not a faultline artifact directory (no report.json); pass the directory of one seed, which holds report.json and trace.jsonl
```

A wrong argument, such as a flag after the directory, prints the problem and the usage, and exits
with 2:

```text
faultline render: flags must come before the artifact directory
usage: faultline render [-slice-cap n] [-max-records n] <dir>
  -max-records int
    	maximum number of records embedded in timeline.html (at least 1000) (default 50000)
  -slice-cap int
    	maximum number of records in the causal slice (hb.mmd, timeline marks) (default 200)
```

The other usage errors are `want exactly one artifact directory`, `-slice-cap must be at least 1`
and `-max-records must be at least 1000`. An unknown flag prints the `flag provided but not defined`
message.

## version

`faultline version` takes no arguments. It prints the faultline version, the Go version that built
the binary, and the platform, as the output under [Install](#install) shows.

## help

`faultline help <command>` prints the usage, the help text and the flags of one command:

```sh
faultline help version
```

```text
usage: faultline version

Version prints the faultline version, the Go version, and the platform.
```

`faultline -h`, `-help` and `--help` print the command list, as `faultline help` does.
`faultline render -h` and `faultline version -h` print the usage of their command. `faultline`
without a command prints the list to standard error and exits with 2.

## Exit codes

The command exits with one of three codes:

| Code | Meaning |
|---|---|
| 0 | the command succeeded, or you asked for help |
| 1 | the command failed, such as a `render` that could not read or write the artifact |
| 2 | a usage error: an unknown command, or wrong arguments or flags |

## Next steps

- [Artifact files](artifacts.md): what `timeline.txt`, `hb.mmd` and `timeline.html` contain.
- [Environment variables](environment.md): the artifact root and full traces.
