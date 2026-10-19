# leasekv results, tier 2: screen

Built by `bench.py report` from `runs/`. Model: claude-opus-5-5. CLI: 2.1.299 (Claude Code). faultline: module-cache. Spent: $4.95.

Spent on retired attempts (`runs/_retired/`, 0): $0.00.

## Screen

A candidate passes when at most 1 of its runs, both versions together, fixed it. Touched: runs whose diff changed the defect's function, filled in by hand.

| Candidate | Version | Fixed | Cost | Touched the defect's function |
|---|---|---|---|---|
| trunc | plain | 2 of 2 | $0.77 | 2 of 2 |
| trunc | faultline | 2 of 2 | $0.54 | 2 of 2 |
| sync | plain | 2 of 2 | $0.62 | 2 of 2 |
| sync | faultline | 1 of 2 | $0.62 | 2 of 2 |
| fwd | plain | 2 of 2 | $0.52 | 2 of 2 |
| fwd | faultline | 2 of 2 | $0.51 | 2 of 2 |
| epoch | plain | 2 of 2 | $0.72 | 2 of 2 |
| epoch | faultline | 2 of 2 | $0.65 | 2 of 2 |

## Summary per bug and arm

Test runs after edit: `go test` calls after the main session's first edit of a non-test `.go` file (an edit tool or a Bash write); failing: those whose result shows a failing test or a nonzero exit. Both leave out undo checks: test runs with the change stashed or checked out, which the Every run table counts as Undo.

| Bug | Arm | Fixed | Cost, median (min to max) | Weighted tokens | Wall time, min | Rounds | Test runs after edit | Failing after edit |
|---|---|---|---|---|---|---|---|---|
| trunc | read-plain | 2 of 2 | $0.38 ($0.38 to $0.39) | 0.1M (0.1M to 0.1M) | 1.4 (1.4 to 1.4) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| trunc | read-faultline | 2 of 2 | $0.27 ($0.26 to $0.28) | 0.1M (0.0M to 0.1M) | 0.8 (0.7 to 0.9) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| sync | read-plain | 2 of 2 | $0.31 ($0.29 to $0.33) | 0.1M (0.1M to 0.1M) | 1.0 (1.0 to 1.0) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| sync | read-faultline | 1 of 2 | $0.31 ($0.30 to $0.32) | 0.1M (0.1M to 0.1M) | 0.9 (0.8 to 1.0) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| fwd | read-plain | 2 of 2 | $0.26 ($0.25 to $0.28) | 0.1M (0.0M to 0.1M) | 0.6 (0.5 to 0.6) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| fwd | read-faultline | 2 of 2 | $0.26 ($0.25 to $0.26) | 0.1M (0.0M to 0.1M) | 0.6 (0.6 to 0.6) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| epoch | read-plain | 2 of 2 | $0.36 ($0.35 to $0.36) | 0.1M (0.1M to 0.1M) | 1.2 (1.2 to 1.3) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |
| epoch | read-faultline | 2 of 2 | $0.33 ($0.31 to $0.34) | 0.1M (0.1M to 0.1M) | 1.0 (1.0 to 1.1) | 1 (1 to 1) | 0 (0 to 0) | 0 (0 to 0) |

## Every run

| Run | Fixed | Round | Wrong fixes | Cost | Weighted | Output | Turns | Wall, min | go test | Tests after edit | Failing | Undo | Regression test | Visible checks changed | Denials | Isolation | Forbidden path | Budget out |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| screen-epoch-read-plain-1 | yes | 1 | 0 | $0.35 | 0.1M | 6255 | 13 | 1.2 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-epoch-read-plain-2 | yes | 1 | 0 | $0.36 | 0.1M | 6462 | 13 | 1.3 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-epoch-read-faultline-1 | yes | 1 | 0 | $0.34 | 0.1M | 5976 | 13 | 1.1 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-epoch-read-faultline-2 | yes | 1 | 0 | $0.31 | 0.1M | 5134 | 12 | 1.0 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-fwd-read-plain-1 | yes | 1 | 0 | $0.25 | 0.0M | 2504 | 10 | 0.5 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-fwd-read-plain-2 | yes | 1 | 0 | $0.28 | 0.1M | 3178 | 11 | 0.6 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-fwd-read-faultline-1 | yes | 1 | 0 | $0.26 | 0.1M | 3040 | 10 | 0.6 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-fwd-read-faultline-2 | yes | 1 | 0 | $0.25 | 0.0M | 2950 | 10 | 0.6 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-sync-read-plain-1 | yes | 1 | 0 | $0.29 | 0.1M | 4561 | 12 | 1.0 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-sync-read-plain-2 | yes | 1 | 0 | $0.33 | 0.1M | 5206 | 11 | 1.0 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-sync-read-faultline-1 | yes | 1 | 0 | $0.32 | 0.1M | 5030 | 11 | 1.0 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-sync-read-faultline-2 | no | - | 1 | $0.30 | 0.1M | 4503 | 10 | 0.8 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-trunc-read-plain-1 | yes | 1 | 0 | $0.38 | 0.1M | 7041 | 10 | 1.4 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-trunc-read-plain-2 | yes | 1 | 0 | $0.39 | 0.1M | 7301 | 11 | 1.4 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-trunc-read-faultline-1 | yes | 1 | 0 | $0.28 | 0.1M | 4048 | 10 | 0.9 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |
| screen-trunc-read-faultline-2 | yes | 1 | 0 | $0.26 | 0.0M | 3323 | 9 | 0.7 | 0 | 0 | 0 | 0 | none | none | 0 | pass | no | no |

## Token cross-check

Weighted tokens come from the CLI's `modelUsage`; `weighted_tokens_transcripts` is the runner's count over the transcripts. Runs where the two differ by more than 2 %:

- none

## Where faultline did not help

Not written yet. Put the hand-written section in `results/screen-notes.md`; `bench.py report` copies it here.

## Selection (filled in by hand, 2026-10-19)

Every candidate was fixed in at least 3 of its 4 read-only runs, so none passes (the rule is at most 1 of 4): trunc 4 of 4, sync 3 of 4, fwd 4 of 4, epoch 4 of 4. All 16 runs edited the defect's own line, checked against each run's `final.diff`, not the hunk labels. The one miss, sync faultline run 2, made the Sync answer wait for a commit but kept answering at once when the fence does not change, which is the bug's second path; the hidden check failed it, as designed. Screen spend $4.95 of the $30 cap. Decision 2026-10-17: with fewer than three candidates passing, tier 2's pilot and full set do not run.
