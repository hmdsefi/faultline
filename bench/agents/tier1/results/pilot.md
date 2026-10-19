# leasekv results: pilot

Built by `bench.py report` from `runs/`. Model: claude-opus-5-5. CLI: 2.1.299 (Claude Code). faultline: replace:local-export. Spent: $4.48.

Spent on retired attempts (`runs/_retired/`, 0): $0.00.

## Summary per bug and arm

Test runs after edit: `go test` calls after the main session's first edit of a non-test `.go` file (an edit tool or a Bash write); failing: those whose result shows a failing test or a nonzero exit. Both leave out undo checks: test runs with the change stashed or checked out, which the Every run table counts as Undo.

| Bug | Arm | Fixed | Cost, median (min to max) | Weighted tokens | Wall time, min | Rounds | Test runs after edit | Failing after edit |
|---|---|---|---|---|---|---|---|---|
| b1 | plain | 1 of 1 | $0.28 ($0.28 to $0.28) | 0.1M (0.1M to 0.1M) | 1.9 (1.9 to 1.9) | 1 (1 to 1) | 2 (2 to 2) | 0 (0 to 0) |
| b1 | docs | 1 of 1 | $0.43 ($0.43 to $0.43) | 0.1M (0.1M to 0.1M) | 1.3 (1.3 to 1.3) | 1 (1 to 1) | 2 (2 to 2) | 0 (0 to 0) |
| b1 | plugin | 1 of 1 | $0.53 ($0.53 to $0.53) | 0.2M (0.2M to 0.2M) | 1.6 (1.6 to 1.6) | 1 (1 to 1) | 5 (5 to 5) | 0 (0 to 0) |
| b2 | plain | 1 of 1 | $0.27 ($0.27 to $0.27) | 0.1M (0.1M to 0.1M) | 1.0 (1.0 to 1.0) | 1 (1 to 1) | 1 (1 to 1) | 0 (0 to 0) |
| b2 | docs | 1 of 1 | $0.43 ($0.43 to $0.43) | 0.1M (0.1M to 0.1M) | 1.0 (1.0 to 1.0) | 1 (1 to 1) | 3 (3 to 3) | 0 (0 to 0) |
| b2 | plugin | 1 of 1 | $0.40 ($0.40 to $0.40) | 0.1M (0.1M to 0.1M) | 0.9 (0.9 to 0.9) | 1 (1 to 1) | 2 (2 to 2) | 0 (0 to 0) |
| b3 | plain | 1 of 1 | $0.33 ($0.33 to $0.33) | 0.1M (0.1M to 0.1M) | 1.1 (1.1 to 1.1) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| b3 | docs | 1 of 1 | $1.00 ($1.00 to $1.00) | 0.3M (0.3M to 0.3M) | 3.2 (3.2 to 3.2) | 1 (1 to 1) | 9 (9 to 9) | 2 (2 to 2) |
| b3 | plugin | 1 of 1 | $0.81 ($0.81 to $0.81) | 0.2M (0.2M to 0.2M) | 2.6 (2.6 to 2.6) | 1 (1 to 1) | 2 (2 to 2) | 1 (1 to 1) |

## Every run

| Run | Fixed | Round | Wrong fixes | Cost | Weighted | Output | Turns | Wall, min | go test | Tests after edit | Failing | Undo | Regression test | Visible checks changed | Denials | Isolation | Forbidden path | Budget out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| pilot-b1-plain-1 | yes | 1 | 0 | $0.28 | 0.1M | 3830 | 8 | 1.9 | 3 | 2 | 0 | 0 | none | none | 1 | pass | no | no |
| pilot-b1-docs-1 | yes | 1 | 0 | $0.43 | 0.1M | 5904 | 21 | 1.3 | 4 | 2 | 0 | 0 | protects | none | 3 | pass | no | no |
| pilot-b1-plugin-1 | yes | 1 | 0 | $0.53 | 0.2M | 9319 | 17 | 1.6 | 8 | 5 | 0 | 0 | protects | none | 2 | pass | no | no |
| pilot-b2-plain-1 | yes | 1 | 0 | $0.27 | 0.1M | 3629 | 10 | 1.0 | 2 | 1 | 0 | 0 | none | none | 1 | pass | no | no |
| pilot-b2-docs-1 | yes | 1 | 0 | $0.43 | 0.1M | 5320 | 15 | 1.0 | 5 | 3 | 0 | 0 | protects | none | 2 | pass | no | no |
| pilot-b2-plugin-1 | yes | 1 | 0 | $0.40 | 0.1M | 5199 | 19 | 0.9 | 5 | 2 | 0 | 0 | none | none | 2 | pass | no | no |
| pilot-b3-plain-1 | yes | 1 | 0 | $0.33 | 0.1M | 5567 | 18 | 1.1 | 0 | 0 | 0 | 0 | none | none | 2 | pass | no | no |
| pilot-b3-docs-1 | yes | 1 | 0 | $1.00 | 0.3M | 12943 | 51 | 3.2 | 12 | 9 | 2 | 0 | protects | none | 5 | pass | no | no |
| pilot-b3-plugin-1 | yes | 1 | 0 | $0.81 | 0.2M | 13557 | 33 | 2.6 | 4 | 2 | 1 | 0 | none | none | 2 | pass | no | no |

## Token cross-check

Weighted tokens come from the CLI's `modelUsage`; `weighted_tokens_transcripts` is the runner's count over the transcripts. Runs where the two differ by more than 2 %:

- pilot-b1-plugin-1: modelUsage 157464, transcripts 145905 (7.9 %)

## Where faultline did not help

Not written yet. Put the hand-written section in `results/pilot-notes.md`; `bench.py report` copies it here.
