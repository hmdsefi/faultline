// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// AT-EXB-09
func TestATEXB09_ToolexecExchangeIsExact(t *testing.T) {
	bin := buildWithWrapper(t, fixture(t, "exb09", true), false)
	orders := map[string]bool{}
	for seed := 1; seed <= 10; seed++ {
		var got [2]string
		for r := range 2 {
			out := filepath.Join(t.TempDir(), "out")
			cmd := exec.Command(bin, "-test.run=^TestExchange$", "-test.count=1")
			cmd.Env = cleanEnv(fmt.Sprintf("FAULTLINE_SEED=%d", seed), "EXB_OUT="+out, "FAULTLINE_ARTIFACTS=off")
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("seed %d run %d: %v\n%s", seed, r+1, err, b)
			}
			line, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			f := strings.Split(strings.TrimSpace(string(line)), "\t")
			if len(f) != 3 || len(f[2]) != 200 {
				t.Fatalf("seed %d run %d: EXB_OUT %q", seed, r+1, line)
			}
			got[r] = f[2]
		}
		if got[0] != got[1] {
			t.Fatalf("seed %d: the two runs differ\n%s\n%s", seed, got[0], got[1])
		}
		orders[got[0]] = true
	}
	if len(orders) < 2 {
		t.Fatalf("all 10 seeds gave the same order")
	}
}
