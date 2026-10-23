// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package exbout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	t.Setenv("EXB_OUT", path)
	if err := Write("TestMapOrder", 0x2a, "01234567"); err != nil {
		t.Fatal(err)
	}
	if err := Write("TestSelectOrder", 1, "0x00ff"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "TestMapOrder\t0x000000000000002a\t01234567\nTestSelectOrder\t0x0000000000000001\t0x00ff\n"
	if string(b) != want {
		t.Fatalf("EXB_OUT = %q, want %q", b, want)
	}
	t.Setenv("EXB_OUT", "")
	if err := Write("x", 1, "y"); err != nil {
		t.Fatal(err)
	}
}
