---
type: llm
---

PASS if the reply tells the user to reproduce the failure from its faultline seed: take the failing seed (a `seed=0x...` subtest name or the `replay:` line) from the CI log and run that replay command or a `FAULTLINE_SEED=0x... go test` command, or run TestKV locally because its seeds come from the test name and replay the seed that fails.
FAIL if the reply relies on re-running the test until it fails (such as `go test -count=N` or a loop), on adding sleeps or retries, or on the race detector, or if it does not mention the seed.
