# leasekv tier 2 calibration

Written by calibrate.sh on 2026-10-19 12:38 UTC with go1.27.1 darwin/arm64, faultline from
the module cache at the version prepare.sh requires, in 1837 s. Visible runs per plain module:
100, 12 at a time. Hidden invocations per plain module: 10, one at a time. A plain
hidden invocation runs up to 600 clusters of 7 s under -race, in batches of 60 that run in parallel, and stops
after the first batch with a failing cluster. faultline seed counts use TestLeaseKVSeeds (faultline/extra) with
KeepGoing: the 2000 hidden seeds are those of TestLeaseKVHidden. Run times include the build.

Result: every row holds, except the pending ones.

| Check | Required | Result | Holds |
|---|---|---|---|
| gofmt and go vet | clean in all ten modules (prepared copies) | clean | yes |
| words and paths in the modules | no bug, benchmark, hidden, variant, planted, candidate, decoy, calibrat*; no path of the repository, the benchmark or its cache | 0 word hits, 0 path hits | yes |
| visible test files | byte-identical in the reference and the four candidates of each version | identical | yes |
| patches | eight patches equal to diff -ru reference <candidate>, none changes a test file | 8 patches, 0 test-file hunks | yes |
| logs | eight logs of real failing runs on the designed check; no path of the repository, the benchmark or its cache; faultline artifacts under /tmp/faultline-1001/ | eight logs, clean | yes |
| faultline reference | visible test passes, also with FAULTLINE_CHECK_DETERMINISM=1; hidden check passes | visible exit 0, determinism exit 0, hidden exit 0 (8s) | yes |
| faultline reference, 20000 seeds | 20000 seeds of TestLeaseKV's base pass with KeepGoing | 0 failing, exit 0 (68s) | yes |
| faultline reference, robustness: crashes every 500 ms, partitions every 300 ms | the 2000 hidden seeds pass with KeepGoing | 0 failing, exit 0 (7s) | yes |
| faultline reference, robustness: 20 s runs, crashes every 700 ms | the 2000 hidden seeds pass with KeepGoing | 0 failing, exit 0 (30s) | yes |
| faultline reference, robustness: a pause rule on s1, s2, st1, st2 (10 to 400 ms) | the 2000 hidden seeds pass with KeepGoing | 0 failing, exit 0 (7s) | yes |
| faultline reference, robustness: tail delays on every link (2 % of messages, up to 40 ms) | the 2000 hidden seeds pass with KeepGoing | 0 failing, exit 0 (7s) | yes |
| faultline trunc | visible test fails within 100 seeds on "reads are fresh" or "acked appends survive"; hidden check fails (exit 1) on it | visible: "reads are fresh" at seed 0x767ffa710151b30d (seed 2 of 100); hidden: exit 1, "reads are fresh" (1s) | yes |
| faultline trunc, 2000 hidden seeds | at least 20 fail with KeepGoing | 719 of 2000 fail (reads are fresh: 559; acked appends survive: 127; every op completes: 18; replicas agree: 15) (23s) | yes |
| trigger trunc | in 3 failing faultline seeds the trace shows the designed trigger, in order (trigger.py) | 11 of 60 failing hidden seeds read show it; first three in the notes | yes |
| faultline sync | visible test fails within 100 seeds on "reads are fresh"; hidden check fails (exit 1) on it | visible: "reads are fresh" at seed 0x78eb4fa9f502ca7f (seed 3 of 100); hidden: exit 1, "reads are fresh" (2s) | yes |
| faultline sync, 2000 hidden seeds | at least 20 fail with KeepGoing | 625 of 2000 fail (reads are fresh: 625) (7s) | yes |
| trigger sync | in 3 failing faultline seeds the trace shows the designed trigger, in order (trigger.py) | 42 of 60 failing hidden seeds read show it; first three in the notes | yes |
| faultline fwd | visible test fails within 100 seeds on "every op completes"; hidden check fails (exit 1) on it | visible: "every op completes" at seed 0x641d981e7dba290b (seed 1 of 100); hidden: exit 1, "every op completes" (4s) | yes |
| faultline fwd, 2000 hidden seeds | at least 20 fail with KeepGoing | 1414 of 2000 fail (every op completes: 1414) (9s) | yes |
| trigger fwd | in 3 failing faultline seeds the trace shows the designed trigger, in order (trigger.py) | 51 of 60 failing hidden seeds read show it; first three in the notes | yes |
| faultline epoch | visible test fails within 100 seeds on "reads are fresh" or "every op completes"; hidden check fails (exit 1) on it | visible: "every op completes" at seed 0x641d981e7dba290b (seed 1 of 100); hidden: exit 1, "every op completes" (1s) | yes |
| faultline epoch, 2000 hidden seeds | at least 20 fail with KeepGoing | 1582 of 2000 fail (every op completes: 1003; reads are fresh: 398; acked appends survive: 153; replicas agree: 28) (12s) | yes |
| trigger epoch | in 3 failing faultline seeds the trace shows the designed trigger, in order (trigger.py) | 14 of 60 failing hidden seeds read show it; first three in the notes | yes |
| plain reference, visible | passes 100 of 100 runs | pass: 100 | yes |
| plain reference, visible under -race | passes 20 of 20 runs | pass: 20 | yes |
| plain reference, hidden | passes 10 of 10 invocations (600 clusters each, -race) | 10 of 10 passed; seconds: 72 73 72 73 72 72 73 72 72 73 | yes |
| plain trunc, visible | fails in at least 10 of 100 runs on "reads are fresh" or "acked appends survive"; rate recorded | 33 of 100 on the designed check; all runs: pass: 63; reads are fresh: 28; acked appends survive: 5; replicas agree: 3; every op completes: 1 | yes |
| plain trunc, hidden | fails 10 of 10 invocations (exit 1) | 10 of 10 failed; failed clusters by check: reads are fresh: 232; acked appends survive: 37; every op completes: 17; replicas agree: 8; seconds: 8 8 7 8 8 8 7 8 8 8 | yes |
| plain sync, visible | fails in at least 10 of 100 runs on "reads are fresh"; rate recorded | 33 of 100 on the designed check; all runs: pass: 67; reads are fresh: 33 | yes |
| plain sync, hidden | fails 10 of 10 invocations (exit 1) | 10 of 10 failed; failed clusters by check: reads are fresh: 210; seconds: 8 8 8 8 7 8 8 8 8 7 | yes |
| plain fwd, visible | fails in at least 10 of 100 runs on "every op completes"; rate recorded | 66 of 100 on the designed check; all runs: every op completes: 66; pass: 34 | yes |
| plain fwd, hidden | fails 10 of 10 invocations (exit 1) | 10 of 10 failed; failed clusters by check: every op completes: 348; seconds: 8 8 8 8 7 8 8 8 8 7 | yes |
| plain epoch, visible | fails in at least 10 of 100 runs on "reads are fresh" or "every op completes"; rate recorded | 75 of 100 on the designed check; all runs: every op completes: 54; reads are fresh: 21; pass: 13; acked appends survive: 8; replicas agree: 4 | yes |
| plain epoch, hidden | fails 10 of 10 invocations (exit 1) | 10 of 10 failed; failed clusters by check: every op completes: 329; reads are fresh: 56; acked appends survive: 52; replicas agree: 11; seconds: 9 7 8 8 7 8 8 8 7 8 | yes |
| plain reference + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 0 | exit 0 | yes |
| plain trunc + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 1 | exit 1 | yes |
| plain reference + a syntax error | hidden.sh exits 2 | exit 2 | yes |
| plain reference + a go vet finding in a non-test file | hidden.sh exits 0 (vet is off) | exit 0 | yes |
| faultline reference + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 0 | exit 0 | yes |
| faultline trunc + TestMain(exit 0) + init(panic) in a test file | hidden.sh still exits 1 | exit 1 | yes |
| faultline reference + a syntax error | hidden.sh exits 2 | exit 2 | yes |
| faultline reference + a go vet finding in a non-test file | hidden.sh exits 0 (vet is off) | exit 0 | yes |
| plain reference + its own go.mod replace and require, vendor/, go.work, GOFLAGS=-mod=vendor | hidden.sh exits 0 (reference go.mod and go.sum, GOWORK=off, GOFLAGS=-mod=mod) | exit 0 | yes |
| faultline reference + its own go.mod replace and require, vendor/, go.work, GOFLAGS=-mod=vendor | hidden.sh exits 0 (reference go.mod and go.sum, GOWORK=off, GOFLAGS=-mod=mod) | exit 0 | yes |
| tells trunc | a reader who sees only the candidate and its logs finds nothing that states or points at the fix | checked by hand, not by this script | pending |
| tells sync | a reader who sees only the candidate and its logs finds nothing that states or points at the fix | checked by hand, not by this script | pending |
| tells fwd | a reader who sees only the candidate and its logs finds nothing that states or points at the fix | checked by hand, not by this script | pending |
| tells epoch | a reader who sees only the candidate and its logs finds nothing that states or points at the fix | checked by hand, not by this script | pending |
| fix experiments | correct fixes pass and named wrong fixes fail, in both versions | checked by hand after the screen, for the three candidates it keeps | pending |

## Notes: run times, patches, triggers

- patch plain-trunc: 1 added, 0 removed lines, files: store.go.
- patch plain-sync: 25 added, 8 removed lines, files: store.go.
- patch plain-fwd: 6 added, 3 removed lines, files: server.go.
- patch plain-epoch: 10 added, 8 removed lines, files: lock.go.
- patch faultline-trunc: 1 added, 0 removed lines, files: store.go.
- patch faultline-sync: 25 added, 8 removed lines, files: store.go.
- patch faultline-fwd: 6 added, 3 removed lines, files: server.go.
- patch faultline-epoch: 10 added, 8 removed lines, files: lock.go.
- faultline reference: hidden check 8s.
- faultline trunc: hidden check 1s.
- trigger trunc (11 of 60; record numbers are trace seq):
  - 0x1b059710b2536c18 match: st2 acks entry 36 (seq 4134), learned commit 35; crash st2 (seq 4151); restart (seq 4391) during partition (seq 4324); grant store epoch 10 (seq 4459); heal (seq 4756); st1 gets replicate prev=35 replacing entries from 36 (seq 4768)
  - 0x254a1c1cc3797436 match: st2 acks entry 23 (seq 2639), learned commit 22; crash st2 (seq 2650); restart (seq 2912) during partition (seq 2739); grant store epoch 8 (seq 2996); heal (seq 3129); st1 gets replicate prev=22 replacing entries from 23 (seq 3178)
  - 0x26af27ccafdc9808 match: st1 acks entry 15 (seq 1292), learned commit 14; crash st1 (seq 1318); restart (seq 1423) during partition (seq 1381); grant store epoch 6 (seq 1443); heal (seq 1615); st2 gets replicate prev=14 replacing entries from 15 (seq 1637)
- faultline sync: hidden check 2s.
- trigger sync (42 of 60; record numbers are trace seq):
  - 0x0007bbe91f3ce9d4 match: partition (seq 4189) cuts st2 and s2 off from the lock; st1 gets the store lease (seq 4239); s1 gets the kv lease (seq 4251); sync ok from st1 (seq 4264) holds k2.len=16, the last written for k2 had len=17 (seq 4168)
  - 0x09ce935e830d2240 match: partition (seq 4090) cuts st2 and s1 off from the lock; st1 gets the store lease (seq 4128); s2 gets the kv lease (seq 4163); sync ok from st1 (seq 4207) holds k1.len=15, the last written for k1 had len=16 (seq 4071)
  - 0x0a9878895ef15b5c match: partition (seq 3599) cuts st2 and s2 off from the lock; st1 gets the store lease (seq 3633); s1 gets the kv lease (seq 3647); sync ok from st1 (seq 3684) holds k1.len=12, the last written for k1 had len=13 (seq 3549)
- faultline fwd: hidden check 4s.
- trigger fwd (51 of 60; record numbers are trace seq):
  - 0x00ba0ca288af50b3 match: s2 passes c1 #21 to s1 (seq 1969); the Fwd dropped (seq 1970); s2 gets kv epoch 7 (seq 2049); 40 repeats of #21 reach s2 from seq 2102 on, none answered
  - 0x018959f68135d42e match: s2 passes c1 #16 to s1 (seq 985); the Fwd dropped (seq 986); s2 gets kv epoch 3 (seq 1048); 59 repeats of #16 reach s2 from seq 1076 on, none answered
  - 0x091b821d7b9e6ade match: s2 passes c1 #43 to s1 (seq 4159); the Fwd dropped (seq 4160); s2 gets kv epoch 8 (seq 4180); 24 repeats of #43 reach s2 from seq 4226 on, none answered
- faultline epoch: hidden check 1s.
- trigger epoch (14 of 60; record numbers are trace seq):
  - 0x018959f68135d42e match: s1 gets kv epoch 1 (seq 59); s2 gets epoch 3 (seq 1052) and acks an append (seq 1324); s1 gets epoch 1 again (seq 4450) and answers a get (seq 4484)
  - 0x04da9a0d72c04324 match: s2 gets kv epoch 2 (seq 99); s1 gets epoch 4 (seq 1324) and acks an append (seq 2327); s2 gets epoch 2 again (seq 2563) and answers a get (seq 2599)
  - 0x0cc94a0ae9f8cf05 match: s1 gets kv epoch 1 (seq 73); s2 gets epoch 4 (seq 1311) and acks an append (seq 1923); s1 gets epoch 1 again (seq 2303) and answers a get (seq 2337)
- plain reference hidden invocation 1: exit 0, 0 of 600 clusters failed, 72s.
- plain reference hidden invocation 2: exit 0, 0 of 600 clusters failed, 73s.
- plain reference hidden invocation 3: exit 0, 0 of 600 clusters failed, 72s.
- plain reference hidden invocation 4: exit 0, 0 of 600 clusters failed, 73s.
- plain reference hidden invocation 5: exit 0, 0 of 600 clusters failed, 72s.
- plain reference hidden invocation 6: exit 0, 0 of 600 clusters failed, 72s.
- plain reference hidden invocation 7: exit 0, 0 of 600 clusters failed, 73s.
- plain reference hidden invocation 8: exit 0, 0 of 600 clusters failed, 72s.
- plain reference hidden invocation 9: exit 0, 0 of 600 clusters failed, 72s.
- plain reference hidden invocation 10: exit 0, 0 of 600 clusters failed, 73s.
- plain trunc hidden invocation 1: exit 1, 25 of 60 clusters failed, 8s.
- plain trunc hidden invocation 2: exit 1, 20 of 60 clusters failed, 8s.
- plain trunc hidden invocation 3: exit 1, 25 of 60 clusters failed, 7s.
- plain trunc hidden invocation 4: exit 1, 33 of 60 clusters failed, 8s.
- plain trunc hidden invocation 5: exit 1, 30 of 60 clusters failed, 8s.
- plain trunc hidden invocation 6: exit 1, 36 of 60 clusters failed, 8s.
- plain trunc hidden invocation 7: exit 1, 31 of 60 clusters failed, 7s.
- plain trunc hidden invocation 8: exit 1, 27 of 60 clusters failed, 8s.
- plain trunc hidden invocation 9: exit 1, 31 of 60 clusters failed, 8s.
- plain trunc hidden invocation 10: exit 1, 36 of 60 clusters failed, 8s.
- plain sync hidden invocation 1: exit 1, 23 of 60 clusters failed, 8s.
- plain sync hidden invocation 2: exit 1, 20 of 60 clusters failed, 8s.
- plain sync hidden invocation 3: exit 1, 20 of 60 clusters failed, 8s.
- plain sync hidden invocation 4: exit 1, 22 of 60 clusters failed, 8s.
- plain sync hidden invocation 5: exit 1, 28 of 60 clusters failed, 7s.
- plain sync hidden invocation 6: exit 1, 21 of 60 clusters failed, 8s.
- plain sync hidden invocation 7: exit 1, 23 of 60 clusters failed, 8s.
- plain sync hidden invocation 8: exit 1, 17 of 60 clusters failed, 8s.
- plain sync hidden invocation 9: exit 1, 17 of 60 clusters failed, 8s.
- plain sync hidden invocation 10: exit 1, 19 of 60 clusters failed, 7s.
- plain fwd hidden invocation 1: exit 1, 33 of 60 clusters failed, 8s.
- plain fwd hidden invocation 2: exit 1, 36 of 60 clusters failed, 8s.
- plain fwd hidden invocation 3: exit 1, 34 of 60 clusters failed, 8s.
- plain fwd hidden invocation 4: exit 1, 33 of 60 clusters failed, 8s.
- plain fwd hidden invocation 5: exit 1, 35 of 60 clusters failed, 7s.
- plain fwd hidden invocation 6: exit 1, 35 of 60 clusters failed, 8s.
- plain fwd hidden invocation 7: exit 1, 36 of 60 clusters failed, 8s.
- plain fwd hidden invocation 8: exit 1, 32 of 60 clusters failed, 8s.
- plain fwd hidden invocation 9: exit 1, 39 of 60 clusters failed, 8s.
- plain fwd hidden invocation 10: exit 1, 35 of 60 clusters failed, 7s.
- plain epoch hidden invocation 1: exit 1, 47 of 60 clusters failed, 9s.
- plain epoch hidden invocation 2: exit 1, 41 of 60 clusters failed, 7s.
- plain epoch hidden invocation 3: exit 1, 46 of 60 clusters failed, 8s.
- plain epoch hidden invocation 4: exit 1, 43 of 60 clusters failed, 8s.
- plain epoch hidden invocation 5: exit 1, 48 of 60 clusters failed, 7s.
- plain epoch hidden invocation 6: exit 1, 49 of 60 clusters failed, 8s.
- plain epoch hidden invocation 7: exit 1, 45 of 60 clusters failed, 8s.
- plain epoch hidden invocation 8: exit 1, 44 of 60 clusters failed, 8s.
- plain epoch hidden invocation 9: exit 1, 47 of 60 clusters failed, 7s.
- plain epoch hidden invocation 10: exit 1, 38 of 60 clusters failed, 8s.
