---
description: "The faultline skill loads for a flaky replication test built on goroutines and net with no faultline test, and the reply says faultline v0.1.x cannot run that code unchanged and offers a faultline test. Needs --scaffold."
tags: [trigger]
max_turns: 10
allowed_tools: [Read, Glob, Grep, Skill]
---

TestReplicasCatchUp in this module fails now and then in CI with `replica 2 holds k97="", want "v97"`, and it passes when I re-run the job. How do I reproduce the failure and find the bug?
