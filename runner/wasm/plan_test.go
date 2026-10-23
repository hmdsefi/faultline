// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package wasm

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestParsePlanLine(t *testing.T) {
	e, err := ParsePlanLine("faultline-exact-plan\tTestA/sub\t0x00000000000000ff")
	if err != nil || e.Test != "TestA/sub" || e.Seed != 0xff {
		t.Fatalf("got %+v, %v", e, err)
	}
	for _, bad := range []string{
		"faultline-exact-plan\tTestA\t0xff",               // not 16 digits
		"faultline-exact-plan\tTestA\tnot-a-seed",         // not hex
		"faultline-exact-plan\t\t0x00000000000000ff",      // no test
		"faultline-exact-plan\tTestA",                     // no seed
		"faultline-exact-plan\tTestA\t00000000000000ff",   // 16 digits, no 0x
		"faultline-exact-plan\tTestA\t0xzzzzzzzzzzzzzzzz", // 16 characters, not hex
	} {
		if _, err := ParsePlanLine(bad); err == nil || err.Error() != fmt.Sprintf("bad plan line %q", bad) {
			t.Errorf("%q: err %v", bad, err)
		}
	}
}

func TestPlanFilter(t *testing.T) {
	var out bytes.Buffer
	f := &PlanFilter{W: &out}
	input := "=== RUN   TestA\nfaultline-exact-plan\tTestA\t0x0000000000000001\nfaultline-exact-plan\tTes" +
		"tA\t0x0000000000000002\n--- SKIP: TestA\npartial"
	for i := 0; i < len(input); i += 7 { // arbitrary write boundaries
		_, _ = f.Write([]byte(input[i:min(i+7, len(input))]))
	}
	f.Flush()
	if out.String() != "=== RUN   TestA\n--- SKIP: TestA\npartial" {
		t.Fatalf("forwarded %q", out.String())
	}
	if len(f.Entries) != 2 || f.Entries[0] != (PlanEntry{"TestA", 1}) || f.Entries[1] != (PlanEntry{"TestA", 2}) || f.Err != nil {
		t.Fatalf("entries %+v, err %v", f.Entries, f.Err)
	}
	g := &PlanFilter{W: &out}
	_, _ = g.Write([]byte("faultline-exact-plan\tX\tzz\nfaultline-exact-plan\tY\tzz\n"))
	if g.Err == nil || g.Err.Error() != `bad plan line "faultline-exact-plan\tX\tzz"` || len(g.Entries) != 0 {
		t.Fatalf("malformed lines: err %v entries %v", g.Err, g.Entries)
	}
	out.Reset()
	h := &PlanFilter{W: &out} // a plan line after output without a newline
	_, _ = h.Write([]byte("."))
	_, _ = h.Write([]byte("faultline-exact-plan\tTestA\t0x0000000000000003\n"))
	if out.String() != "." || len(h.Entries) != 1 || h.Entries[0] != (PlanEntry{"TestA", 3}) || h.Err != nil {
		t.Fatalf("forwarded %q, entries %+v, err %v", out.String(), h.Entries, h.Err)
	}
}

func TestRunPatternAndArgs(t *testing.T) {
	if got := RunPattern("TestA/seed=0x1.x"); got != `^TestA$/^seed=0x1\.x$` {
		t.Fatalf("RunPattern = %q", got)
	}
	got := RunArgs([]string{"-test.paniconexit0", "-test.run=^TestB$", "-test.v=true", "-test.run", "X", "--test.run", "Y",
		"--test.run=Z", "-test.count=3", "-test.count", "4", "--test.count", "5", "--test.count=6", "-test.timeout=10m0s"}, "TestA")
	want := []string{"-test.paniconexit0", "-test.v=true", "-test.timeout=10m0s", "-test.run=^TestA$", "-test.count=1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("RunArgs = %q, want %q", got, want)
	}
}
