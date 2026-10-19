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
