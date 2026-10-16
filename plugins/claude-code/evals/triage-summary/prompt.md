---
description: "The faultline:triage agent summarizes a failing seed's artifact folder, and the reply names the failed check and the replay command. Needs --scaffold."
tags: [triage]
max_turns: 15
timeout_seconds: 600
allowed_tools: [Read, Glob, Grep, Skill, Agent]
---

TestKV failed on seed 0x287372ab06f1482e. Its artifact folder is artifacts/example.com_kv/TestKV/287372ab06f1482e. Why did it fail, and how do I run it again?
