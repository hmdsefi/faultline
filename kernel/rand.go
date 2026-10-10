// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package kernel

import (
	"fmt"
	"math/bits"
	"math/rand/v2"
	"time"
)

const (
	fnvOffset64 = 14695981039346656037 // 0xcbf29ce484222325
	fnvPrime64  = 1099511628211        // 0x100000001b3
)

// fnv1a64 is FNV-1a 64 over the bytes of s.
func fnv1a64(s string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

// fnvFold continues the FNV-1a 64 hash h over the bytes of b.
func fnvFold(h uint64, b []byte) uint64 {
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime64
	}
	return h
}

// splitMix64 advances *state and returns the next SplitMix64 output.
func splitMix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// streamSeeds derives the two PCG seeds of the stream label under root seed (KRN-010).
func streamSeeds(seed uint64, label string) (seed1, seed2 uint64) {
	st := seed
	k := splitMix64(&st)    // mix the root seed
	st = k ^ fnv1a64(label) // combine with the label
	seed1 = splitMix64(&st)
	seed2 = splitMix64(&st)
	return seed1, seed2
}

// newStream returns the PCG stream of label under seed. Creating it draws nothing.
func newStream(seed uint64, label string) *rand.Rand {
	s1, s2 := streamSeeds(seed, label)
	return rand.New(rand.NewPCG(s1, s2)) //nolint:gosec // seeded on purpose: a deterministic simulator needs reproducible streams
}

// Chance reports true with probability ppm/1_000_000. It draws exactly one value from r.
func Chance(r *rand.Rand, ppm uint32) bool {
	x := r.Uint64()
	hi, _ := bits.Mul64(x, 1_000_000) // hi = floor(x * 1e6 / 2^64), in [0, 1e6)
	return hi < uint64(ppm)
}

// Uniform returns a duration uniformly distributed in [min, max], both inclusive. It draws exactly
// one value from r. It panics if min > max.
func Uniform(r *rand.Rand, min, max time.Duration) time.Duration {
	if min > max {
		panic(fmt.Sprintf("kernel: Uniform: min %s > max %s", min, max))
	}
	x := r.Uint64()
	span := uint64(max) - uint64(min) + 1 //nolint:gosec // number of values in [min, max], mod 2^64; 0 means 2^64
	if span == 0 {
		return min + time.Duration(x) //nolint:gosec // wrapping add: the full int64 range
	}
	hi, _ := bits.Mul64(x, span)           // hi in [0, span)
	return time.Duration(uint64(min) + hi) //nolint:gosec // wrapping add: the result is in [min, max]
}
