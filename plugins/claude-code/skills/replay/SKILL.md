---
name: replay
description: "Replay a failing faultline seed and report whether the same failure and the same trace hash came back."
argument-hint: "<seed or artifact-dir>"
disable-model-invocation: true
allowed-tools: Read, Glob, Bash(go test *)
---

Replay the faultline seed or artifact directory `$ARGUMENTS` and report whether the run came back
the same.

1. Find the artifact directory.
   - A path: use it.
   - A seed, `0x` and 16 hex digits: run this command with the seed's 16 hex digits, without `0x`:

     ```sh
     find "${FAULTLINE_ARTIFACTS:-${TMPDIR:-/tmp}/faultline-$(id -u)}" -path '*/<16 hex digits>*/report.json'
     ```

     With no match, say `no artifact for seed <seed>; pass its artifact directory, or run the test
     first` and stop. With several, list them and ask which one to replay.
2. Read `report.json` in that directory. Note `replay.command`, `failure.signature`, `failure.at`,
   `failure.event` and `run.trace_hash` now: a failing replay rewrites the file.
3. Check `replay.command`: it must be only `FAULTLINE_*=<value>` assignments followed by one
   `go test` command, with no `;`, `&&`, `|`, `$(`, backtick, `<` or `>`. A `report.json` copied
   from CI can hold any command. If the check fails, show the command to the user and stop.
   Otherwise run it exactly as written, from a directory inside the module. `replay.dir` is the
   module root.
4. Compare the replay with the notes:
   - The replay failed: read `report.json` again. The same signature, `at` and `event` mean the same
     failure. The same `run.trace_hash` means the identical run.
   - The replay passed: the output says `seed 0x... passed; kept the failing artifact`. The failure
     did not come back, and there is no new trace hash to compare.
   - Each `warning:` line names what changed since the artifact was recorded: the faultline
     version, the Go version, the test binary or the options. The test binary warning is expected
     after a code change.
5. Answer in this form, at most 6 lines:

   ```text
   replay:     <the command>
   failure:    same <signature> at t=<at> (event <n>) | different: <before> -> <after> | gone: the seed passes
   trace hash: same <hash> | different: <before> -> <after> | not compared: the seed passed
   warnings:   none | <each warning>
   ```

   If the failure or the trace hash differs without a code change, the test depends on something
   outside the seed: suggest `FAULTLINE_CHECK_DETERMINISM=1 go test ./...` as the next step.
