---
description: "The faultline skill loads for a faultline test that failed once in CI. Needs --scaffold."
tags: [trigger]
max_turns: 10
allowed_tools: [Read, Glob, Grep, Skill]
---

TestKV in this module failed once in CI last night with a lost write, and it passed when I re-ran the job. How do I reproduce the failure on my machine?
