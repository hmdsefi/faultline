---
name: pin
description: "Pin a failing faultline seed in a regression test next to the test that failed, with faultline.RunSeeds, or etcdraft.TestSeeds for a test that calls etcdraft.Test."
argument-hint: "<seed or artifact-dir>"
disable-model-invocation: true
allowed-tools: Read, Glob, Grep, Edit, Bash(go test *), Bash(go doc *)
---

Pin the faultline seed or artifact directory `$ARGUMENTS` in a regression test.

1. **Find the artifact directory.**
   - A path: use it.
   - A seed, `0x` and 16 hex digits: run this command with the seed's 16 hex digits, without `0x`:

     ```sh
     find "${FAULTLINE_ARTIFACTS:-${TMPDIR:-/tmp}/faultline-$(id -u)}" -path '*/<16 hex digits>*/report.json'
     ```

     With no match, ask the user for the artifact directory, or for the test that failed. With
     several, list them and ask which one to pin.

2. **Read `report.json`.** Note `test`, `package` (the import path), `seed`, `failure.signature`,
   `replay.dir` (the module root) and `replay.package_dir` (the test package's directory).

3. **Find the failing test.** Grep for `func <test>(` in the `_test.go` files of
   `replay.package_dir`, or of the current module when that directory does not exist, as for an
   artifact from CI. For a name with `/`, grep for the part before the first `/`; the rest names
   the `t.Run` subtest. Read the test and find the call that runs its seeds.

4. **Choose the pin and check its version.** A version check reads the requirement in `go.mod` at
   the module root. If `go.mod` replaces that module or requires a pseudo-version, such as
   `v0.1.1-0.20261014...`, run `go doc <module> <function>` instead: an error means the function
   is missing.
   - `faultline.Run(t, <options>, <world>)`: the pin calls
     `faultline.RunSeeds(t, <options>, <world>, <seed>)`, which needs `github.com/hmdsefi/faultline`
     v0.1.1 or later. With an older version, say `faultline.RunSeeds needs faultline v0.1.1 or
     later, and go.mod requires <version>. Upgrade with go get github.com/hmdsefi/faultline@latest,
     then run /faultline:pin again.` and stop.
   - `etcdraft.Test(t, <config>, <options>)`: the pin calls
     `etcdraft.TestSeeds(t, <config>, <options>, <seed>)` when `go.mod` requires
     `github.com/hmdsefi/faultline/harness/etcdraft` v0.1.1 or later. With an older version, use
     the next form.
   - Another helper that reaches `faultline.Run`, or `etcdraft.Test` with an older harness: the pin
     makes the failing test's helper call after `t.Setenv("FAULTLINE_SEED", "<seed>")` and
     `t.Setenv("FAULTLINE_SEED_LIST", "")`. This form needs no version check.

   For `RunSeeds` and `TestSeeds`, reuse each argument after `t` that is a package-level name, such
   as `kvOptions` and `kvWorld`, or a call such as `etcdraft.DefaultConfig()`. Move a literal or a
   local variable to a package-level declaration named after the test (`kvOptions`, `kvWorld` for
   `TestKV`), make the failing test use it, and change nothing else in that test.

5. **Write the pin** right after the failing test function, in the same file. Name it `Test`, the
   letters and digits of the test name after its `Test` prefix, `Seed`, then the first 8 hex digits
   of the seed. Use the seed exactly as `report.json` gives it. For seed `0x287372ab06f1482e` of
   `TestKV`:

   ```go
   // TestKVSeed287372ab runs seed 0x287372ab06f1482e, which failed with invariant:acked puts survive.
   func TestKVSeed287372ab(t *testing.T) {
   	faultline.RunSeeds(t, kvOptions, kvWorld, 0x287372ab06f1482e)
   }
   ```

   If a test of that name already exists, say so and stop.

6. **Run it** from a directory inside the module: `go test -run '^TestKVSeed287372ab$' <package>`,
   with the pin's name and the import path from `report.json`. If that command holds `;`, `&&`, `|`,
   `$(`, a backtick, `<` or `>`, show it to the user and stop.
   - It fails with the same check: the bug is still in the code. The test passes once the fix lands.
   - It passes: the fix holds for this seed.
   - It does not build, or `go` cannot run here: report the error as it is.

Answer with the file and the test you added, any declaration you moved, and the `go test` result in
one line.
