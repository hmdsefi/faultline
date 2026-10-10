// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package kernel is faultline's single-threaded discrete-event simulator. It owns virtual time,
// the event queue and its seeded tie-break, named PRNG streams, nodes with incarnations, pause and
// clocks, and the trace: records, causes and the running hash.
//
// A Sim is not safe for concurrent use and the kernel never starts a goroutine. The behavior of
// every exported name is specified by KRN (.helper/docs/specs/kernel.md).
package kernel
