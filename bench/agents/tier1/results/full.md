# leasekv results: full

Built by `bench.py report` from `runs/`. Model: claude-opus-5-5. CLI: 2.1.299 (Claude Code). faultline: replace:local-export. Spent: $18.32.

Spent on retired attempts (`runs/_retired/`, 0): $0.00.

## Summary per bug and arm

Test runs after edit: `go test` calls after the main session's first edit of a non-test `.go` file (an edit tool or a Bash write); failing: those whose result shows a failing test or a nonzero exit. Both leave out undo checks: test runs with the change stashed or checked out, which the Every run table counts as Undo.

| Bug | Arm | Fixed | Cost, median (min to max) | Weighted tokens | Wall time, min | Rounds | Test runs after edit | Failing after edit |
|---|---|---|---|---|---|---|---|---|
| b1 | plain | 5 of 5 | $0.22 ($0.20 to $0.23) | 0.0M (0.0M to 0.0M) | 2.2 (1.5 to 2.6) | 1 (1 to 1) | 2 (2 to 2) | 0 (0 to 0) |
| b1 | docs | 5 of 5 | $0.33 ($0.29 to $0.36) | 0.1M (0.1M to 0.1M) | 0.9 (0.8 to 1.0) | 1 (1 to 1) | 2 (1 to 3) | 0 (0 to 1) |
| b1 | plugin | 5 of 5 | $0.31 ($0.25 to $0.40) | 0.1M (0.1M to 0.1M) | 0.8 (0.7 to 1.4) | 1 (1 to 1) | 2 (1 to 3) | 0 (0 to 1) |
| b2 | plain | 5 of 5 | $0.20 ($0.20 to $0.21) | 0.0M (0.0M to 0.0M) | 2.2 (1.4 to 2.9) | 1 (1 to 1) | 2 (1 to 2) | 0 (0 to 0) |
| b2 | docs | 5 of 5 | $0.35 ($0.33 to $0.39) | 0.1M (0.1M to 0.1M) | 0.9 (0.7 to 0.9) | 1 (1 to 1) | 2 (2 to 3) | 0 (0 to 1) |
| b2 | plugin | 5 of 5 | $0.37 ($0.33 to $0.40) | 0.1M (0.1M to 0.1M) | 0.9 (0.9 to 1.0) | 1 (1 to 1) | 2 (1 to 2) | 0 (0 to 1) |
| b3 | plain | 5 of 5 | $0.29 ($0.25 to $0.62) | 0.1M (0.1M to 0.2M) | 3.0 (2.1 to 10.1) | 1 (1 to 1) | 2 (2 to 3) | 0 (0 to 2) |
| b3 | docs | 5 of 5 | $0.65 ($0.56 to $0.83) | 0.2M (0.1M to 0.2M) | 2.1 (1.7 to 2.6) | 1 (1 to 1) | 4 (4 to 7) | 1 (0 to 2) |
| b3 | plugin | 5 of 5 | $0.79 ($0.50 to $1.43) | 0.3M (0.2M to 0.4M) | 3.2 (1.7 to 5.9) | 1 (1 to 1) | 4 (2 to 9) | 2 (0 to 5) |

## Every run

| Run | Fixed | Round | Wrong fixes | Cost | Weighted | Output | Turns | Wall, min | go test | Tests after edit | Failing | Undo | Regression test | Visible checks changed | Denials | Isolation | Forbidden path | Budget out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| full-b1-plain-1 | yes | 1 | 0 | $0.23 | 0.0M | 2770 | 5 | 1.6 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b1-plain-2 | yes | 1 | 0 | $0.22 | 0.0M | 2703 | 5 | 1.5 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b1-plain-3 | yes | 1 | 0 | $0.23 | 0.0M | 3016 | 5 | 2.6 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b1-plain-4 | yes | 1 | 0 | $0.20 | 0.0M | 2780 | 5 | 2.3 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b1-plain-5 | yes | 1 | 0 | $0.22 | 0.0M | 2693 | 5 | 2.2 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b1-docs-1 | yes | 1 | 0 | $0.33 | 0.1M | 3608 | 8 | 0.8 | 3 | 1 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b1-docs-2 | yes | 1 | 0 | $0.33 | 0.1M | 3865 | 8 | 1.0 | 4 | 3 | 1 | 0 | protects | none | 0 | pass | no | no |
| full-b1-docs-3 | yes | 1 | 0 | $0.29 | 0.1M | 3912 | 8 | 1.0 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b1-docs-4 | yes | 1 | 0 | $0.36 | 0.1M | 3683 | 9 | 0.8 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b1-docs-5 | yes | 1 | 0 | $0.34 | 0.1M | 4285 | 8 | 0.9 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b1-plugin-1 | yes | 1 | 0 | $0.31 | 0.1M | 3661 | 10 | 0.8 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b1-plugin-2 | yes | 1 | 0 | $0.25 | 0.1M | 2711 | 6 | 0.7 | 2 | 1 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b1-plugin-3 | yes | 1 | 0 | $0.39 | 0.1M | 6746 | 10 | 1.4 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b1-plugin-4 | yes | 1 | 0 | $0.40 | 0.1M | 6374 | 12 | 1.4 | 6 | 3 | 1 | 2 | protects | none | 0 | pass | no | no |
| full-b1-plugin-5 | yes | 1 | 0 | $0.28 | 0.1M | 3275 | 7 | 0.7 | 2 | 1 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b2-plain-1 | yes | 1 | 0 | $0.21 | 0.0M | 2322 | 4 | 2.9 | 2 | 1 | 0 | 1 | none | none | 0 | pass | no | no |
| full-b2-plain-2 | yes | 1 | 0 | $0.20 | 0.0M | 2072 | 4 | 2.4 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b2-plain-3 | yes | 1 | 0 | $0.20 | 0.0M | 2078 | 4 | 2.2 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b2-plain-4 | yes | 1 | 0 | $0.21 | 0.0M | 2322 | 5 | 2.2 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b2-plain-5 | yes | 1 | 0 | $0.20 | 0.0M | 2123 | 4 | 1.4 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b2-docs-1 | yes | 1 | 0 | $0.33 | 0.1M | 3638 | 8 | 0.7 | 3 | 2 | 0 | 0 | protects | none | 0 | pass | no | no |
| full-b2-docs-2 | yes | 1 | 0 | $0.36 | 0.1M | 4026 | 9 | 0.9 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b2-docs-3 | yes | 1 | 0 | $0.39 | 0.1M | 4425 | 11 | 0.9 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b2-docs-4 | yes | 1 | 0 | $0.33 | 0.1M | 3704 | 9 | 0.8 | 4 | 2 | 1 | 1 | protects | none | 0 | pass | no | no |
| full-b2-docs-5 | yes | 1 | 0 | $0.35 | 0.1M | 3737 | 9 | 0.9 | 4 | 3 | 1 | 0 | protects | none | 0 | pass | no | no |
| full-b2-plugin-1 | yes | 1 | 0 | $0.37 | 0.1M | 5537 | 10 | 1.0 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b2-plugin-2 | yes | 1 | 0 | $0.33 | 0.1M | 4904 | 9 | 0.9 | 3 | 1 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b2-plugin-3 | yes | 1 | 0 | $0.40 | 0.1M | 4440 | 12 | 0.9 | 5 | 2 | 1 | 2 | protects | none | 0 | pass | no | no |
| full-b2-plugin-4 | yes | 1 | 0 | $0.34 | 0.1M | 5131 | 9 | 0.9 | 3 | 1 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b2-plugin-5 | yes | 1 | 0 | $0.38 | 0.1M | 5890 | 11 | 1.0 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |
| full-b3-plain-1 | yes | 1 | 0 | $0.29 | 0.1M | 4203 | 8 | 2.1 | 3 | 2 | 0 | 1 | none | none | 0 | pass | no | no |
| full-b3-plain-2 | yes | 1 | 0 | $0.32 | 0.1M | 4859 | 8 | 5.4 | 3 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b3-plain-3 | yes | 1 | 0 | $0.25 | 0.1M | 4667 | 5 | 2.4 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b3-plain-4 | yes | 1 | 0 | $0.29 | 0.1M | 4314 | 6 | 3.0 | 2 | 2 | 0 | 0 | none | none | 0 | pass | no | no |
| full-b3-plain-5 | yes | 1 | 0 | $0.62 | 0.2M | 12947 | 14 | 10.1 | 5 | 3 | 2 | 2 | none | none | 0 | pass | no | no |
| full-b3-docs-1 | yes | 1 | 0 | $0.83 | 0.2M | 11705 | 25 | 2.6 | 10 | 7 | 0 | 2 | protects | none | 0 | pass | no | no |
| full-b3-docs-2 | yes | 1 | 0 | $0.65 | 0.2M | 10277 | 18 | 2.1 | 7 | 5 | 1 | 1 | protects | none | 0 | pass | no | no |
| full-b3-docs-3 | yes | 1 | 0 | $0.67 | 0.2M | 8820 | 21 | 2.0 | 7 | 4 | 1 | 2 | protects | none | 0 | pass | no | no |
| full-b3-docs-4 | yes | 1 | 0 | $0.63 | 0.2M | 9691 | 16 | 2.3 | 5 | 4 | 1 | 0 | protects | none | 0 | pass | no | no |
| full-b3-docs-5 | yes | 1 | 0 | $0.56 | 0.1M | 7606 | 13 | 1.7 | 6 | 4 | 2 | 1 | protects | none | 0 | pass | no | no |
| full-b3-plugin-1 | yes | 1 | 0 | $1.43 | 0.4M | 27594 | 38 | 5.9 | 13 | 9 | 5 | 3 | protects | none | 0 | pass | no | no |
| full-b3-plugin-2 | yes | 1 | 0 | $0.59 | 0.2M | 10236 | 14 | 2.0 | 6 | 2 | 0 | 3 | protects | none | 0 | pass | no | no |
| full-b3-plugin-3 | yes | 1 | 0 | $0.93 | 0.3M | 20738 | 17 | 4.2 | 6 | 4 | 2 | 1 | protects | none | 0 | pass | no | no |
| full-b3-plugin-4 | yes | 1 | 0 | $0.79 | 0.3M | 17775 | 15 | 3.2 | 6 | 5 | 3 | 0 | protects | none | 0 | pass | no | no |
| full-b3-plugin-5 | yes | 1 | 0 | $0.50 | 0.2M | 8794 | 10 | 1.7 | 4 | 2 | 0 | 1 | protects | none | 0 | pass | no | no |

## Token cross-check

Weighted tokens come from the CLI's `modelUsage`; `weighted_tokens_transcripts` is the runner's count over the transcripts. Runs where the two differ by more than 2 %:

- full-b1-plugin-3: modelUsage 103305, transcripts 93076 (11.0 %)
- full-b1-plugin-4: modelUsage 116743, transcripts 110828 (5.3 %)
- full-b2-plugin-1: modelUsage 103086, transcripts 97721 (5.5 %)
- full-b2-plugin-2: modelUsage 88791, transcripts 85191 (4.2 %)
- full-b2-plugin-4: modelUsage 94643, transcripts 89009 (6.3 %)
- full-b2-plugin-5: modelUsage 105944, transcripts 100430 (5.5 %)
- full-b3-plugin-1: modelUsage 429016, transcripts 416301 (3.1 %)
- full-b3-plugin-2: modelUsage 177228, transcripts 167054 (6.1 %)
- full-b3-plugin-3: modelUsage 308838, transcripts 279833 (10.4 %)
- full-b3-plugin-4: modelUsage 252606, transcripts 221692 (13.9 %)
- full-b3-plugin-5: modelUsage 155412, transcripts 141537 (9.8 %)

## Where faultline did not help

**It did not change whether the bug got fixed.** All 45 runs fixed their bug in the first round: 15 of
15 with plain Go tests, 15 of 15 with `docs/ai-agents.md` and its block, 15 of 15 with the plugin.
Opus 5.5 found each bug by reading the service (about 550 lines) and the CI log. These three bugs are
too easy to show a difference in fix rate.

**It did not lower the cost.** The plain arm was the cheapest on every bug:

| Bug | plain | docs | plugin |
|---|---|---|---|
| b1, ack before durable | $0.22 | $0.33 | $0.31 |
| b2, retry applied twice | $0.20 | $0.35 | $0.37 |
| b3, serving past the lease | $0.29 | $0.65 | $0.79 |

Medians per run. Over 15 runs: plain $3.91, docs $6.74, plugin $7.68. The extra cost is the work the
docs and the plugin ask for: replay the seed, run every seed and the determinism check, undo the fix to
check that the seed fails again, and pin the seed in a regression test. On b3 the plugin arm also read
more of the artifacts: median 253k weighted tokens, against 163k for docs and 66k for plain.

## What faultline changed

**A regression test.** The docs arm left a test that fails on the buggy code in 15 of 15 runs, the
plugin arm in 13 of 15, the plain arm in 0 of 15.

**Wall time on b1 and b2.** The faultline arms took a median of 0.8 to 0.9 minutes, the plain arm 2.2
minutes. A faultline test runs 100 seeds in under a second; the plain test runs 7 seconds of real time
per run. On b3 the arms are close (plain 3.0, docs 2.1, plugin 3.2 minutes), but the plain arm's
slowest run took 10.1 minutes: it re-ran the test 15 to 20 times in a row to see the rare failure.

**Failing test runs after the first edit.** On b3 the faultline arms had a median of 1 (docs) and 2
(plugin) failing test runs after their first change to the service, up to 5; the plain arm had a
median of 0. In the pilot run we read, the failing run was a first fix that failed another seed, which
the agent then corrected before answering. The plain arm's b3 test fails on the original code in about
1 run in 10, so a passing plain run says little about a fix.

## Limits

- One model (Opus 5.5, Claude Code 2.1.299), one small service, three bugs that reading finds. The
  results say nothing about bugs that only show in a failing run; tier 2 (`../../tier2`) is that
  harder tier.
- b1 resembles the key-value example in faultline's docs, which the docs and plugin arms can read.
- The hidden check would accept a b3 lease timed from the grant's arrival. None of the 15 b3 fixes did
  that: all time the lease from when the request was sent.
- faultline came from the v0.1.1 candidate and the plugin from the release commit, before the tag.
- Every arm loaded Claude Code's built-in tools and skills and could see its remaining budget. Every
  arm could run any command; reads of the repository and the benchmark folder were blocked.
- Tokens come from the CLI's own usage. The transcripts undercount the plugin's triage subagent, by up
  to 13.9 % of weighted tokens in one run.
