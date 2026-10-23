// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package wasm

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
)

func TestStreamSeedsGolden(t *testing.T) {
	// KRN §10 golden vectors.
	if a, b := streamSeeds(1, "kernel/sched"); a != 0x46efa33d0374af3a || b != 0x79016df9a4656287 {
		t.Fatalf("streamSeeds(1, kernel/sched) = %#x, %#x", a, b)
	}
	if a, b := streamSeeds(1, "net/link/1/2"); a != 0x68c90d3ccb254143 || b != 0xaca4fac5f37f3ed1 {
		t.Fatalf("streamSeeds(1, net/link/1/2) = %#x, %#x", a, b)
	}
}

func TestPRNGGolden(t *testing.T) {
	// Computed with KRN-010 for stream "wasm/random_get" under seed 1 and pinned here.
	b := make([]byte, 24)
	_, _ = NewPRNG(1).Read(b)
	if got := hex.EncodeToString(b); got != "6a550e840230ad444ea4e5a2f36b93de6be000efd4df7a24" {
		t.Fatalf("first 24 bytes of seed 1 = %s", got)
	}
}

func TestPRNGMatchesKernelStream(t *testing.T) {
	for _, seed := range []uint64{1, 5, 0x0e0b5eed00000001} {
		r := kernel.New(kernel.Config{Seed: seed}).Rand("wasm/random_get")
		want := make([]byte, 32)
		for i := 0; i < 4; i++ {
			binary.LittleEndian.PutUint64(want[i*8:], r.Uint64())
		}
		got := make([]byte, 32)
		g := NewPRNG(seed)
		for off := 0; off < 32; off += 3 { // odd chunks: unused bytes carry over (EXB-012)
			end := min(off+3, 32)
			_, _ = g.Read(got[off:end])
		}
		if hex.EncodeToString(got) != hex.EncodeToString(want) {
			t.Fatalf("seed %#x: PRNG %x, kernel stream %x", seed, got, want)
		}
	}
}
