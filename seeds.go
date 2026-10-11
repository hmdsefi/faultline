// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"fmt"
	"os"
	"time"
)

// gamma is the SplitMix64 increment.
const gamma = 0x9e3779b97f4a7c15

// NameBase returns the default base seed for a test name: FNV-1a 64 of the bytes of testName.
func NameBase(testName string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(testName); i++ {
		h ^= uint64(testName[i])
		h *= 0x100000001b3
	}
	return h
}

// DeriveSeed returns seed i (0-based) of the seed list derived from base with SplitMix64.
// It panics if i < 0.
func DeriveSeed(base uint64, i int) uint64 {
	if i < 0 {
		panic(fmt.Sprintf("faultline: DeriveSeed: negative index %d; want 0 or more", i))
	}
	z := base + (uint64(i)+1)*gamma
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// exploreBase returns a fresh base seed for FAULTLINE_EXPLORE=1 (API-016): exploreBaseAt of the
// wall clock and the process ID. It never reads crypto/rand.
func exploreBase() uint64 {
	return exploreBaseAt(time.Now().UnixNano(), os.Getpid())
}

// exploreBaseAt returns the base seed of a wall clock reading ns and a process ID pid (API-016).
func exploreBaseAt(ns int64, pid int) uint64 {
	return DeriveSeed(uint64(ns)^(uint64(pid)<<32), 0) //nolint:gosec // a process ID is not negative
}
