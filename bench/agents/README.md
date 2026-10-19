# AI agent benchmark

This benchmark asks whether an AI coding agent fixes a concurrency bug better with faultline than
with plain Go tests ([issue #261](https://github.com/hmdsefi/faultline/issues/261)). Claude Code
with Opus 5.5 gets leasekv, a small Go key-value service with leases and one planted bug. The
prompt holds the CI log of the failing test and asks for a fix. Each bug runs in three arms:

- **plain**: the service and a plain Go test that runs the cluster for 7 seconds of real time;
- **docs**: the faultline version of the service, with faultline's agent instructions as
  `CLAUDE.md` and `docs/ai-agents.md` from faultline v0.1.1;
- **plugin**: the faultline version with faultline's Claude Code plugin at v0.1.1.

A hidden check that the agent never sees decides whether the bug is fixed. When it fails, the
agent gets a follow-up prompt, for up to three rounds. The runner records cost, weighted tokens,
wall time and `go test` calls per run. It also counts failing test runs after the first edit. It
checks whether the agent left a test that fails on the buggy code, and whether it changed the
visible checks.

## The two tiers

**Tier 1** has three bugs that a careful reader can find. b1 acknowledges an append before the
store has it, b2 applies a retried append twice, and b3 keeps serving after its lease ran out. The
service has a lock, one store, two servers and two clients, in about 550 lines of Go.

**Tier 2** has four candidates meant to show only in a failing run. Its service has two store
replicas and about 930 lines.

- trunc: a restarted store replica cuts its log back to its commit index, so an entry it
  acknowledged can disappear.
- sync: the store answers a Sync with the data it has applied so far, so a new lease holder can
  load a list that misses an acknowledged write.
- fwd: a server drops a client's retry of a request it already passed on, so the operation never
  completes when the forward is lost.
- epoch: the lock hands a server the epoch of its first grant again, so a stale holder answers
  reads.

Tier 2 starts with a read-only screen. Claude Code gets the variant, the prompt and the CI log for
one round, with only the Read, Edit, Write, Glob and Grep tools. A candidate passes when at most 1
of its 4 screen runs fixes it. The pilot and the full set then use three candidates that passed.

## Results

Tier 1's full set (45 runs) is in `tier1/results/full.md` and `full.csv`, with the hand-written
notes in `full-notes.md`. Its pilot (9 runs) is in `pilot.md`. Tier 2's screen is in
`tier2/results/screen.md`. In short:

- Tier 1: all 45 runs fixed their bug in the first round, 15 of 15 in every arm.
- The plain arm was the cheapest on every bug: $3.91 over 15 runs, docs $6.74, plugin $7.68.
- Runs that left a test failing on the buggy code: docs 15 of 15, plugin 13 of 15, plain 0 of 15.
- Tier 2: each candidate was fixed in at least 3 of its 4 screen runs, so none passed the screen
  and tier 2's pilot and full set did not run.

Tier 1's pilot ran with tighter permissions: each pilot run had 1 to 5 denied tool calls, the full
set none. Its numbers are not comparable with the full set's.

Tier 1 ran faultline from a local export of commit 2957609, shown as `replace:local-export` in its
results. That commit's Go code equals v0.1.1's (`git diff 2957609 v0.1.1 -- '*.go' ':!plugins'` is
empty). A rerun takes faultline v0.1.1 from the module cache. It takes the plugin from tag v0.1.1,
whose files equal those the runs used.

The hidden checks, the bugs and their patches are public now. A model trained on this repository
may know the answers, so a rerun on a newer model can measure recall instead of debugging.

## What you need

- A clone of faultline with its tags, in any folder. The runner flags a run whose output names the
  clone's path. Under your home folder, its `~/`, `$HOME/` and `${HOME}/` forms count too. Only
  whole folder names match, so `/tmp/faultline` does not match `/tmp/faultline-bench`.
- Go 1.26 or newer. The first `go mod tidy` in `prepare.sh` downloads faultline v0.1.1.
- Python 3. The runner uses the standard library only.
- Claude Code 2.1.299, logged in with `claude auth login`, or with `claude setup-token` and
  `CLAUDE_CODE_OAUTH_TOKEN` exported. Only the variables in `env_keep` reach the CLI. On macOS the
  login sits in the keychain, so run `bench.py` from a normal terminal, not from a sandboxed shell.

## Steps

Run every command from `bench/agents`. Steps 2 to 5 take the tier, `tier1` or `tier2`.

1. `sh arms/build.sh` exports the plugin to `~/.cache/faultline-bench/plugin-v0.1.1`. Once, free.
2. `python3 bench.py tier1 calibrate` runs `service/calibrate.sh` and rewrites
   `service/calibration.md`. Every row must hold. It makes no model calls; tier 2's last
   calibration took 31 minutes.
3. `python3 bench.py tier1 probe` checks isolation and permissions. The init checks cost nothing;
   the rest call Haiku, at most $0.02 a call and $0.05 in total. Run it after every CLI update.
4. Tier 1: `python3 bench.py tier1 pilot` runs one run per bug and arm: 9 runs, $4 a run, $36
   for the set (it spent $4.48). `run --runs 5` runs the full set: 45 runs, $4 a run, $180 for
   the set (it spent $18.32). Run the same command again to resume.
   Tier 2: `python3 bench.py tier2 screen --runs 2` runs 16 runs, $3 a run, $30 for the set (it
   spent $4.95). Then set `bugs` in `tier2/config.json` to the three candidates that passed.
   `pilot` then costs up to $8 a run and $72, and `run --runs 5` up to $8 a run and $360.
5. `python3 bench.py tier1 report` writes `results/<set>.csv` and `results/<set>.md`. A
   hand-written `results/<set>-notes.md` becomes the page's last section.

A run gets at most 30 minutes of agent time in tier 1 and 45 in tier 2. A set does not start a run
that could pass the set's cap, retired attempts included. It stops on an error or a refused run.
`python3 -m unittest discover -s runner` tests the runner against a fake `claude` and a stub
service, with no API calls.

## Isolation

Every run starts in a fresh workspace under `/tmp/faultline-bench`, with
`--permission-mode bypassPermissions` and deny rules only. They deny Read of the repository, which
holds the hidden checks, and for every arm but plugin Read of the plugin folder. Read rules do not
cover Bash, so a run whose stream or transcripts name the repository or `~/.claude/projects` gets
`forbidden_path` set.

The runner refuses a run whose init message lists an MCP server, a hook or one of your own skills,
agents or commands. The same holds for a plugin other than the built-ins and the plugin arm's
faultline. `isolation_baseline` in both configs lists the built-in skills and agents of CLI
2.1.299. So a built-in that shares a name with one of yours does not refuse the run. On another CLI
version, the probe's `isolation` lines name each skill or agent missing from the baseline as
`unlisted <name>`. Add the CLI's own names to `isolation_baseline` in both config files.

## One run's files

A run writes `<tier>/runs/<set>-<bug>-<arm>-<n>/`: `run.json` with the record and its measures,
and per round `stream-r<k>.jsonl`, `stderr-r<k>.txt` and `hidden-r<k>.log`. `original/`,
`workspace/` and `final.diff` show the workspace before and after. `transcripts/` holds the
sessions, and `claude-project/` the CLI's project folder, moved out of `~/.claude/projects` so that
later runs cannot read it. `tmp/` is the run's `TMPDIR`, and `regression-*.log` are the regression
test runs. `probe` writes to `<tier>/runs/probe/`. Git ignores `runs/`.

## Layout

- `bench.py`: the command line. `runner/`: the runner and its tests, with a fake `claude`, a stub
  service and three tier 1 pilot runs in `runner/testdata/`.
- `prompts/`: the first prompt, `task.md`, and the follow-up, `followup.md`.
- `arms/`: `build.sh`, and `docs/instructions.md`, which the runner writes as `CLAUDE.md` into the
  docs arm's workspace, next to `docs/docs/ai-agents.md`.
- `tier1/`, `tier2/`: `config.json`, `results/` and `service/`. `service/` holds `prepare.sh`,
  `hidden.sh`, `calibrate.sh`, `calibration.md`, the CI logs in `logs/` and each bug as a diff in
  `patches/`. The variants and hidden checks sit in `testdata/plain/` and `testdata/faultline/`,
  where no Go build in the repository sees them. Tier 2 adds `trigger.py` and `tools/`.

`bench/` is its own Go module, so faultline's module zip does not carry the benchmark. From the
repository root, `cd bench && GOWORK=off go test ./...` checks that each patch turns the reference
into its variant.
