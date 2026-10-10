// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// AT-DSK-25
func TestSampleLatency(t *testing.T) {
	us := time.Microsecond
	k := newDisk(1, simdisk.Config{Latency: simdisk.LatencyConfig{Read: simdisk.Latency{Base: 10 * us}}})
	if got := k.v.SampleLatency(simdisk.OpRead); got != 10*us {
		t.Fatalf("SampleLatency(OpRead) = %v, want 10µs", got)
	}
	if k.s.Rand("disk/a/latency").Uint64() != replay(1, "disk/a/latency").Uint64() {
		t.Fatalf("a zero jitter and zero SlowPPM drew from disk/a/latency")
	}

	k = newDisk(1, simdisk.Config{})
	for _, op := range []simdisk.Op{simdisk.OpRead, simdisk.OpWrite, simdisk.OpSync, simdisk.OpMeta} {
		if got := k.v.SampleLatency(op); got != 0 {
			t.Fatalf("zero config: SampleLatency(%v) = %v", op, got)
		}
	}

	lc := simdisk.DefaultLatency()
	k = newDisk(1, simdisk.Config{Latency: lc})
	r := replay(1, "disk/a/latency")
	recs := len(k.s.Records())
	for i := 0; i < 1000; i++ {
		got := k.v.SampleLatency(simdisk.OpSync)
		want := lc.Sync.Base + kernel.Uniform(r, 0, lc.Sync.Jitter)
		if kernel.Chance(r, lc.SlowPPM) {
			want += kernel.Uniform(r, 0, lc.Slow)
		}
		if got != want {
			t.Fatalf("sample %d = %v, replay %v", i, got, want)
		}
		if got < 500*us || got > 22*time.Millisecond {
			t.Fatalf("sample %d = %v outside [500µs, 22ms]", i, got)
		}
	}
	if len(k.s.Records()) != recs {
		t.Fatalf("SampleLatency emitted records")
	}
	if k.s.Rand("disk/a").Uint64() != replay(1, "disk/a").Uint64() {
		t.Fatalf("SampleLatency drew from disk/a")
	}
	mustPanic(t, "simdisk: SampleLatency: unknown Op 9", func() { k.v.SampleLatency(simdisk.Op(9)) })
	mustPanic(t, "simdisk: SampleLatency: unknown Op 0", func() { k.v.SampleLatency(0) })
}

// DSK-038, DSK-036: each op reads its own class; SlowPPM 1_000_000 takes the slow path without a
// Chance draw; Slow 0 still draws the Chance and adds nothing; the slow tail follows the replay
// (seed 4 takes it, AT-DSK-25's seed 1 never does); and SampleLatency works while the node is
// paused or down.
func TestSampleLatencyRules(t *testing.T) {
	k := newDisk(1, simdisk.Config{Latency: simdisk.LatencyConfig{
		Read: simdisk.Latency{Base: 1}, Write: simdisk.Latency{Base: 2},
		Sync: simdisk.Latency{Base: 3}, Meta: simdisk.Latency{Base: 4},
	}})
	for i, op := range []simdisk.Op{simdisk.OpRead, simdisk.OpWrite, simdisk.OpSync, simdisk.OpMeta} {
		if got := k.v.SampleLatency(op); got != time.Duration(i+1) {
			t.Fatalf("SampleLatency(%v) = %v, want %v", op, got, time.Duration(i+1))
		}
	}

	k = newDisk(1, simdisk.Config{Latency: simdisk.LatencyConfig{SlowPPM: 1_000_000, Slow: 5 * time.Millisecond}})
	r := replay(1, "disk/a/latency")
	if got, want := k.v.SampleLatency(simdisk.OpRead), kernel.Uniform(r, 0, 5*time.Millisecond); got != want {
		t.Fatalf("SlowPPM 1_000_000: SampleLatency = %v, want %v", got, want)
	}
	if k.s.Rand("disk/a/latency").Uint64() != r.Uint64() {
		t.Fatalf("SlowPPM 1_000_000: the slow Uniform is not the only draw")
	}

	k = newDisk(1, simdisk.Config{Latency: simdisk.LatencyConfig{SlowPPM: 500_000}})
	r = replay(1, "disk/a/latency")
	kernel.Chance(r, 500_000)
	if got := k.v.SampleLatency(simdisk.OpRead); got != 0 {
		t.Fatalf("Slow 0: SampleLatency = %v, want 0", got)
	}
	if k.s.Rand("disk/a/latency").Uint64() != r.Uint64() {
		t.Fatalf("Slow 0: the Chance is not the only draw")
	}

	lc := simdisk.DefaultLatency()
	k = newDisk(4, simdisk.Config{Latency: lc})
	r = replay(4, "disk/a/latency")
	slow := 0
	for i := 0; i < 1000; i++ {
		got := k.v.SampleLatency(simdisk.OpSync)
		want := lc.Sync.Base + kernel.Uniform(r, 0, lc.Sync.Jitter)
		if kernel.Chance(r, lc.SlowPPM) {
			want += kernel.Uniform(r, 0, lc.Slow)
			slow++
		}
		if got != want {
			t.Fatalf("seed 4 sample %d = %v, replay %v", i, got, want)
		}
	}
	if slow == 0 {
		t.Fatalf("seed 4: no sample took the slow path")
	}

	k = newDisk(1, simdisk.Config{Latency: simdisk.LatencyConfig{Read: simdisk.Latency{Base: 7}}})
	k.a.Pause()
	if got := k.v.SampleLatency(simdisk.OpRead); got != 7 {
		t.Fatalf("paused: SampleLatency = %v, want 7ns", got)
	}
	k.a.Resume()
	k.s.RunFor(0)
	k.crash()
	if got := k.v.SampleLatency(simdisk.OpRead); got != 7 {
		t.Fatalf("down: SampleLatency = %v, want 7ns", got)
	}
}
