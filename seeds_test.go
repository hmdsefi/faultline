package faultline

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// AT-API-01
func TestNameBaseAndDeriveSeed(t *testing.T) {
	names := []struct {
		in   string
		want uint64
	}{
		{"", 0xcbf29ce484222325},
		{"TestKV", 0x75c66bf05fc438d4},
		{"TestScenario", 0xb83f592e2ee6cccf},
		{"TestKV/raft_3_nodes", 0x054d07925584037e},
		{"\u00e9", 0x0ac21707b7181e01}, // FNV-1a of the bytes c3 a9, not of the rune U+00E9
	}
	for _, c := range names {
		if got := NameBase(c.in); got != c.want {
			t.Errorf("NameBase(%q) = %#016x, want %#016x", c.in, got, c.want)
		}
	}
	seeds := []struct {
		base uint64
		i    int
		want uint64
	}{
		{0, 0, 0xe220a8397b1dcdaf}, {0, 1, 0x6e789e6aa1b965f4}, {0, 2, 0x06c45d188009454f},
		{1, 0, 0x910a2dec89025cc1}, {1, 1, 0xbeeb8da1658eec67},
		{0x2a, 0, 0xbdd732262feb6e95}, {0x2a, 1, 0x28efe333b266f103}, {0x2a, 2, 0x47526757130f9f52},
		{0xffffffffffffffff, 0, 0xe4d971771b652c20},
		{NameBase("TestKV"), 0, 0x287372ab06f1482e}, {NameBase("TestKV"), 1, 0xac1b1f508c3fd743},
		{NameBase("TestKV"), 2, 0x9cb5a1e8588ac16e}, {NameBase("TestKV"), 3, 0x151ffa3c67c5dd90},
		{NameBase("TestKV"), 4, 0x4ba1f47d35c501dd}, {NameBase("TestKV"), 999, 0x5b73cfd52c51b909},
		{NameBase("TestScenario"), 0, 0x9a33dad17ee0bb7d}, {NameBase("TestScenario"), 1, 0x8a216e8699751f87},
		{NameBase("TestScenario"), 2, 0xbfa29075015c7f9a}, {NameBase("TestScenario"), 3, 0x894121045090449b},
		{NameBase("TestScenario"), 4, 0xcc3568470ec1dc08}, {NameBase("TestScenario"), 5, 0x43befb9000153529},
		{NameBase("TestScenario"), 6, 0x4ab253d616f0e7dc}, {NameBase("TestScenario"), 7, 0x9a47d07cc62c2cf2},
		{NameBase("TestScenario"), 8, 0xdc19b7f10a68a337}, {NameBase("TestScenario"), 9, 0x1fd084013f4226dd},
	}
	for _, c := range seeds {
		if got := DeriveSeed(c.base, c.i); got != c.want {
			t.Errorf("DeriveSeed(%#x, %d) = %#016x, want %#016x", c.base, c.i, got, c.want)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("DeriveSeed(0, -1) did not panic")
		}
	}()
	DeriveSeed(0, -1)
}

// AT-API-06 (a) needs DeriveSeed(1, 2).
func TestDeriveSeedOneTwo(t *testing.T) {
	if got := DeriveSeed(1, 2); got != 0xf893a2eefb32555e {
		t.Fatalf("DeriveSeed(1, 2) = %#016x", got)
	}
}

// API-015: DeriveSeed(base, i) equals explore.SeedAt(base, uint64(i)) for every index, also
// from 2^32 on. A 32-bit int cannot hold such an index.
func TestDeriveSeedWideIndex(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("int has 32 bits")
	}
	shift := 32
	if got := DeriveSeed(0, 1<<shift); got != 0x46093cf9861ec2e4 {
		t.Fatalf("DeriveSeed(0, 1<<32) = %#016x, want 0x46093cf9861ec2e4", got)
	}
}

// API-016: exploreBase is DeriveSeed(wall clock in ns ^ process ID << 32, 0), with the clock
// read during the call. If the wall clock steps back, or forward by more than 100 ms, during a
// call, that call gives no usable window and is made again.
func TestExploreBase(t *testing.T) {
	pid := uint64(os.Getpid()) << 32 //nolint:gosec // a process ID is not negative
	for range 5 {
		before := time.Now().UnixNano()
		got := exploreBase()
		after := time.Now().UnixNano()
		if after < before || after-before > int64(100*time.Millisecond) {
			continue
		}
		for ns := before; ns <= after; ns++ {
			if DeriveSeed(uint64(ns)^pid, 0) == got {
				return
			}
		}
		t.Fatalf("exploreBase() = %#016x: no clock reading in [%d, %d] gives it", got, before, after)
	}
	t.Fatal("the wall clock stepped during each of 5 calls of exploreBase")
}
