<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/assets/readme-dark.svg">
    <img alt="faultline — deterministic simulation testing for Go" src=".github/assets/readme-light.svg" width="640">
  </picture>
</p>

<p align="center">
  Find the fault lines in your distributed system — before production does.
</p>

> [!WARNING]
> **Heavily under progress.** faultline is not ready for use yet. APIs, file formats and behavior
> change without notice until the first release.
>
> | First version | Scope | Milestone | Release date |
> |---|---|---|---|
> | **v0.1.0** | Phase 1 MVP: simulation kernel, simulated network and disk, fault injection and exact replay, the `go test` API, failure artifacts, and the etcd/raft harness | [v0.1.0 — Phase 1 MVP](https://github.com/hmdsefi/faultline/milestone/1) | **2026-10-19** |

faultline runs your distributed Go code inside a simulated world: virtual time,
a seeded scheduler, and a fake network and disk that inject partitions, delays,
reordering, crashes, and fsync failures. When a seed fails, you replay it exactly
and shrink it to the smallest failing schedule — all from `go test`.

## What "faultline" means here

The name has two meanings, and both are the point.

**1. Geology.** A fault line is a fracture in the earth's crust. Stress builds
there silently for years, nothing looks wrong, and then it releases all at once
as an earthquake. Distributed-system bugs behave the same way: they sit hidden
until a rare combination of conditions (a partition during leader election, a
lost fsync right before a crash) breaks the system. faultline exists to find
those fractures in a test, not in production.

**2. Distributed systems.** A *fault* is the standard term for something going
wrong: a crashed node, a dropped message, a failed disk write. *Fault tolerance*
is what you're trying to build; *fault injection* is how you check it. faultline
injects faults along a seeded timeline (a line of faults) and lets you replay
that exact line.

| Earthquake fault line | faultline (this project) |
|---|---|
| Stress builds invisibly | Latent bugs hide behind rare timing and failures |
| A specific trigger releases it | A specific fault schedule exposes the bug |
| Geologists map faults before the big one | faultline maps where your system breaks before production does |
| Earthquakes can't be replayed | A failing run replays exactly from its seed (`FAULTLINE_SEED=…`) |

**What it doesn't mean:** faultline is **not** a chaos-engineering tool for live
clusters (like Chaos Monkey or Chaos Mesh). It doesn't break real infrastructure.
Everything runs in-process inside `go test`, on simulated time, network, and disk,
so failures are cheap, fast, and exactly reproducible. Fault injection is half of
it; the other half is **determinism**: the same seed always produces the same run.

## Relationship with lockstep

[lockstep](https://github.com/hmdsefi/lockstep) (deterministic actors for Go) **imports** faultline's
simulation kernel. It isn't part of this repo.

```
github.com/hmdsefi/faultline         testing tool
├── kernel/     clock · scheduler · simulated network & disk · faults · replay
├── shims/      adapters for existing Go code
└── harness/    etcd/raft, CometBFT, ...

github.com/hmdsefi/lockstep          actor runtime
└── simulation mode ──imports──▶ github.com/hmdsefi/faultline/kernel
```

The dependency goes one way: lockstep → `faultline/kernel`. faultline never imports
lockstep.

## Built with

[gograph](https://github.com/hmdsefi/gograph) is faultline's only dependency outside
the standard library. It handles network topology and partitions, cycle detection
in the isolation checker, and diagrams of failing runs.
