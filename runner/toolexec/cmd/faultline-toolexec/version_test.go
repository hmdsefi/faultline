// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The wrapper's version and the hash of its non-test sources (XTE-022). A change to a non-test
// file of this package needs a new version: raise version in main.go, then set these two to the
// values the failing test prints.
const (
	stampVersion = "3"
	stampHash    = "4dcd2df755235960f2502880d61276530c01d0b01e15af74a87a7732e3a56859"
)

// AT-XTE-04
func TestATXTE04_VersionStamp(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	h := sha256.New()
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n) //nolint:gosec // this package's own sources
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s\n%s", n, b)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if version != stampVersion || got != stampHash {
		t.Fatalf("version %q, sources hash %s; pinned version %q, hash %q. Raise version in main.go for every change to the rewriter, the rewrite set or a generated file, then set stampVersion to it and stampHash to this hash", version, got, stampVersion, stampHash)
	}
}
