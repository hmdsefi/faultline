# leasekv calibration

Written by calibrate.sh on 2026-10-16 21:38 UTC with go1.27.1 darwin/arm64, faultline from
a local export of the v0.1.1 candidate, whose Go code equals v0.1.1's. Visible runs per plain module: 100,
12 at a time. Hidden invocations per plain module: 10, one at a time. A plain hidden
invocation runs up to 600 clusters of 7 s under -race, in batches of 60 that run in parallel, and
stops after the first batch with a failing cluster. Run times include the build. The last two
rows test a hash check of that export, which calibrate.sh dropped when faultline moved to the
module cache.

Result: every row holds.

| Check | Required | Result | Holds |
|---|---|---|---|
| gofmt and go vet | clean in all eight modules (prepared copies) | clean | yes |
| words and paths in the modules | no bug, benchmark, hidden, variant, planted, calibrat*; no path of the repository, the benchmark or its cache | 0 word hits, 0 path hits | yes |
| visible test files | byte-identical in reference, b1, b2, b3 of each version | identical | yes |
| patches | six patches, none changes a test file | 6 patches, 0 test-file hunks | yes |
| logs | six logs, no path of the repository, the benchmark or its cache | six logs, clean | yes |
| faultline reference | visible test passes, also with FAULTLINE_CHECK_DETERMINISM=1; hidden check passes | visible exit 0, determinism exit 0, hidden exit 0 (5s) | yes |
| faultline b1 | visible test fails on "reads are fresh" or "acked appends survive"; hidden check fails (exit 1) on it | visible: "reads are fresh" at seed 0x641d981e7dba290b (99 of 100 seeds not run); hidden: exit 1, "reads are fresh" (1s) | yes |
| faultline b2 | visible test fails on "no duplicate appends"; hidden check fails (exit 1) on it | visible: "no duplicate appends" at seed 0x23d0735a8cee2a51 (90 of 100 seeds not run); hidden: exit 1, "no duplicate appends" (1s) | yes |
| faultline b3 | visible test fails on "reads are fresh"; hidden check fails (exit 1) on it | visible: "reads are fresh" at seed 0x767ffa710151b30d (98 of 100 seeds not run); hidden: exit 1, "reads are fresh" (1s) | yes |
| plain reference, visible | passes 100 of 100 runs | pass: 100 | yes |
| plain reference, visible under -race | passes 20 of 20 runs | pass: 20 | yes |
| plain reference, hidden | passes 10 of 10 invocations (600 clusters each, -race) | 10 of 10 passed; seconds: 68 68 68 68 68 68 68 68 68 68 | yes |
| plain b1, visible | fails at least once and passes at least once in 100 runs, on "reads are fresh" or "acked appends survive"; rate recorded | failure rate 87/100 (acked appends survive: 7; pass: 13; reads are fresh: 80) | yes |
| plain b1, hidden | fails 10 of 10 invocations (exit 1), on "reads are fresh" or "acked appends survive" | 10 of 10 failed; seconds: 7 8 7 7 8 7 7 8 7 7 | yes |
| plain b2, visible | fails at least once and passes at least once in 100 runs, on "no duplicate appends"; rate recorded | failure rate 35/100 (no duplicate appends: 35; pass: 65) | yes |
| plain b2, hidden | fails 10 of 10 invocations (exit 1), on "no duplicate appends" | 10 of 10 failed; seconds: 8 8 7 7 8 7 7 7 8 7 | yes |
| plain b3, visible | fails at least once and passes at least once in 100 runs, on "reads are fresh"; rate recorded | failure rate 9/100 (pass: 91; reads are fresh: 9) | yes |
| plain b3, hidden | fails 10 of 10 invocations (exit 1), on "reads are fresh" | 10 of 10 failed; seconds: 8 7 8 7 7 7 8 7 7 8 | yes |
| plain reference + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 0 | exit 0 | yes |
| plain b1 + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 1 | exit 1 | yes |
| plain reference + a syntax error | hidden.sh exits 2 | exit 2 | yes |
| plain reference + a go vet finding in a non-test file | hidden.sh exits 0 (vet is off) | exit 0 | yes |
| faultline reference + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 0 | exit 0 | yes |
| faultline b1 + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 1 | exit 1 | yes |
| faultline reference + a syntax error | hidden.sh exits 2 | exit 2 | yes |
| faultline reference + a go vet finding in a non-test file | hidden.sh exits 0 (vet is off) | exit 0 | yes |
| plain reference + its own go.mod replace and require, vendor/, go.work, GOFLAGS=-mod=vendor | hidden.sh exits 0 (reference go.mod and go.sum, GOWORK=off, GOFLAGS=-mod=mod) | exit 0 | yes |
| faultline reference + its own go.mod replace and require, vendor/, go.work, GOFLAGS=-mod=vendor | hidden.sh exits 0 (reference go.mod and go.sum, GOWORK=off, GOFLAGS=-mod=mod) | exit 0 | yes |
| faultline reference with a copy of the export and its hash | hidden.sh exits 0 | exit 0 | yes |
| faultline reference with one file of the export changed | hidden.sh exits 3 | exit 3 | yes |

## Hidden check run times

- faultline reference: hidden check 5s.
- faultline b1: hidden check 1s.
- faultline b2: hidden check 1s.
- faultline b3: hidden check 1s.
- plain reference hidden invocation 1: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 2: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 3: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 4: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 5: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 6: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 7: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 8: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 9: exit 0, 0 of 600 clusters failed, 68s.
- plain reference hidden invocation 10: exit 0, 0 of 600 clusters failed, 68s.
- plain b1 hidden invocation 1: exit 1, 59 of 60 clusters failed, 7s.
- plain b1 hidden invocation 2: exit 1, 60 of 60 clusters failed, 8s.
- plain b1 hidden invocation 3: exit 1, 57 of 60 clusters failed, 7s.
- plain b1 hidden invocation 4: exit 1, 59 of 60 clusters failed, 7s.
- plain b1 hidden invocation 5: exit 1, 56 of 60 clusters failed, 8s.
- plain b1 hidden invocation 6: exit 1, 59 of 60 clusters failed, 7s.
- plain b1 hidden invocation 7: exit 1, 57 of 60 clusters failed, 7s.
- plain b1 hidden invocation 8: exit 1, 59 of 60 clusters failed, 8s.
- plain b1 hidden invocation 9: exit 1, 57 of 60 clusters failed, 7s.
- plain b1 hidden invocation 10: exit 1, 56 of 60 clusters failed, 7s.
- plain b2 hidden invocation 1: exit 1, 24 of 60 clusters failed, 8s.
- plain b2 hidden invocation 2: exit 1, 26 of 60 clusters failed, 8s.
- plain b2 hidden invocation 3: exit 1, 24 of 60 clusters failed, 7s.
- plain b2 hidden invocation 4: exit 1, 23 of 60 clusters failed, 7s.
- plain b2 hidden invocation 5: exit 1, 26 of 60 clusters failed, 8s.
- plain b2 hidden invocation 6: exit 1, 19 of 60 clusters failed, 7s.
- plain b2 hidden invocation 7: exit 1, 23 of 60 clusters failed, 7s.
- plain b2 hidden invocation 8: exit 1, 27 of 60 clusters failed, 7s.
- plain b2 hidden invocation 9: exit 1, 19 of 60 clusters failed, 8s.
- plain b2 hidden invocation 10: exit 1, 20 of 60 clusters failed, 7s.
- plain b3 hidden invocation 1: exit 1, 6 of 60 clusters failed, 8s.
- plain b3 hidden invocation 2: exit 1, 3 of 60 clusters failed, 7s.
- plain b3 hidden invocation 3: exit 1, 8 of 60 clusters failed, 8s.
- plain b3 hidden invocation 4: exit 1, 4 of 60 clusters failed, 7s.
- plain b3 hidden invocation 5: exit 1, 6 of 60 clusters failed, 7s.
- plain b3 hidden invocation 6: exit 1, 4 of 60 clusters failed, 7s.
- plain b3 hidden invocation 7: exit 1, 15 of 60 clusters failed, 8s.
- plain b3 hidden invocation 8: exit 1, 11 of 60 clusters failed, 7s.
- plain b3 hidden invocation 9: exit 1, 9 of 60 clusters failed, 7s.
- plain b3 hidden invocation 10: exit 1, 5 of 60 clusters failed, 8s.
