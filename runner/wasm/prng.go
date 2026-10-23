// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package wasm is prototype A of the exact backend: it runs a wasip1 test binary with
// deterministic randomness and a virtual clock.
package wasm

import (
	"encoding/binary"
	"math/rand/v2"
)

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// fnv1a64, splitMix64 and streamSeeds are KRN-010's algorithm (the kernel's copies are
// unexported).
func fnv1a64(s string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

func splitMix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func streamSeeds(seed uint64, label string) (seed1, seed2 uint64) {
	st := seed
	k := splitMix64(&st)
	st = k ^ fnv1a64(label)
	seed1 = splitMix64(&st)
	seed2 = splitMix64(&st)
	return seed1, seed2
}

// PRNG is the guest's random source: successive Uint64 values of the stream
// "wasm/random_get", 8 bytes each, little-endian; unused bytes of a value are kept for the next
// Read.
type PRNG struct {
	r    *rand.Rand
	buf  [8]byte
	left int // unread bytes at the end of buf
}

// NewPRNG returns the random source of an instance with the given seed.
func NewPRNG(seed uint64) *PRNG {
	s1, s2 := streamSeeds(seed, "wasm/random_get")
	return &PRNG{r: rand.New(rand.NewPCG(s1, s2))} //nolint:gosec // G404: the guest's randomness must follow the seed
}

// Read fills p. It never fails.
func (g *PRNG) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if g.left == 0 {
			binary.LittleEndian.PutUint64(g.buf[:], g.r.Uint64())
			g.left = 8
		}
		c := copy(p[n:], g.buf[8-g.left:])
		g.left -= c
		n += c
	}
	return n, nil
}
