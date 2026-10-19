---
description: "/faultline:pin writes a RunSeeds test next to TestKV. Needs --scaffold and --allow-tools Edit Write. The run cannot download faultline v0.1.1, so the graders check the test file, not a go test run."
tags: [pin]
max_turns: 15
allowed_tools: [Read, Glob, Grep, Edit, Write]
---

/faultline:pin artifacts/example.com_kv/TestKV/287372ab06f1482e
