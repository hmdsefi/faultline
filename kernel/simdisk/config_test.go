// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// AT-DSK-01 (DefaultConfig, Validate), DSK-039, DSK §7
func TestConfigValidate(t *testing.T) {
	if got := simdisk.DefaultConfig(); got != (simdisk.Config{SectorSize: 512}) {
		t.Fatalf("DefaultConfig() = %+v, want {SectorSize: 512}", got)
	}
	if err := (simdisk.Config{}).Validate(); err != nil {
		t.Fatalf("Config{}.Validate() = %v", err)
	}
	top := simdisk.Latency{Base: simdisk.MaxLatency, Jitter: simdisk.MaxLatency}
	if err := (simdisk.Config{Crash: simdisk.CrashTorn, Metadata: simdisk.MetadataImmediate,
		FailedSync: simdisk.FailedSyncKeepDirty, SectorSize: simdisk.MaxSectorSize, Capacity: math.MaxInt64,
		Latency: simdisk.LatencyConfig{Read: top, Write: top, Sync: top, Meta: top, SlowPPM: 1_000_000,
			Slow: simdisk.MaxLatency}}).Validate(); err != nil {
		t.Fatalf("maximal valid config: %v", err)
	}
	cases := []struct {
		c    simdisk.Config
		want string
	}{
		{simdisk.Config{Crash: 5}, "invalid config: Crash 5 is not a CrashModel"},
		{simdisk.Config{Metadata: 2}, "invalid config: Metadata 2 is not a MetadataModel"},
		{simdisk.Config{FailedSync: 2}, "invalid config: FailedSync 2 is not a FailedSyncModel"},
		{simdisk.Config{SectorSize: -1}, "invalid config: SectorSize -1 out of range [0, 1048576]"},
		{simdisk.Config{SectorSize: 1<<20 + 1}, "invalid config: SectorSize 1048577 out of range [0, 1048576]"},
		{simdisk.Config{Capacity: -5}, "invalid config: Capacity -5 is negative"},
		{simdisk.Config{Capacity: -1}, "invalid config: Capacity -1 is negative"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Read: simdisk.Latency{Base: simdisk.MaxLatency + 1}}},
			"invalid config: Latency.Read.Base 1h0m0.000000001s out of range [0s, 1h0m0s]"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Read: simdisk.Latency{Base: -1}}},
			"invalid config: Latency.Read.Base -1ns out of range [0s, 1h0m0s]"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Write: simdisk.Latency{Jitter: 2 * time.Hour}}},
			"invalid config: Latency.Write.Jitter 2h0m0s out of range [0s, 1h0m0s]"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Sync: simdisk.Latency{Base: 2 * time.Hour}, Meta: simdisk.Latency{Base: -1}}},
			"invalid config: Latency.Sync.Base 2h0m0s out of range [0s, 1h0m0s]"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Meta: simdisk.Latency{Jitter: -time.Second}}},
			"invalid config: Latency.Meta.Jitter -1s out of range [0s, 1h0m0s]"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Slow: -1}}, "invalid config: Latency.Slow -1ns out of range [0s, 1h0m0s]"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Slow: simdisk.MaxLatency + 1}},
			"invalid config: Latency.Slow 1h0m0.000000001s out of range [0s, 1h0m0s]"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{SlowPPM: 1_000_001}}, "invalid config: Latency.SlowPPM 1000001 exceeds 1000000"},
		// order: Crash before SectorSize, Base before Jitter
		{simdisk.Config{Crash: 9, SectorSize: -1}, "invalid config: Crash 9 is not a CrashModel"},
		{simdisk.Config{Latency: simdisk.LatencyConfig{Read: simdisk.Latency{Base: -1, Jitter: -1}}},
			"invalid config: Latency.Read.Base -1ns out of range [0s, 1h0m0s]"},
	}
	for _, c := range cases {
		err := c.c.Validate()
		if err == nil || err.Error() != c.want {
			t.Errorf("%+v.Validate() = %v, want %q", c.c, err, c.want)
		}
	}
}

// DSK §7: Validate reports the first invalid field in table order. Starting from a config where
// every field is invalid, each step expects the next message and then repairs that field.
func TestValidateOrder(t *testing.T) {
	bad := simdisk.Latency{Base: -1, Jitter: -1}
	c := simdisk.Config{Crash: 5, Metadata: 2, FailedSync: 2, SectorSize: -1, Capacity: -1,
		Latency: simdisk.LatencyConfig{Read: bad, Write: bad, Sync: bad, Meta: bad, Slow: -1, SlowPPM: 1_000_001}}
	lat := func(f string) string { return "invalid config: Latency." + f + " -1ns out of range [0s, 1h0m0s]" }
	steps := []struct {
		want string
		fix  func(*simdisk.Config)
	}{
		{"invalid config: Crash 5 is not a CrashModel", func(c *simdisk.Config) { c.Crash = 0 }},
		{"invalid config: Metadata 2 is not a MetadataModel", func(c *simdisk.Config) { c.Metadata = 0 }},
		{"invalid config: FailedSync 2 is not a FailedSyncModel", func(c *simdisk.Config) { c.FailedSync = 0 }},
		{"invalid config: SectorSize -1 out of range [0, 1048576]", func(c *simdisk.Config) { c.SectorSize = 0 }},
		{"invalid config: Capacity -1 is negative", func(c *simdisk.Config) { c.Capacity = 0 }},
		{lat("Read.Base"), func(c *simdisk.Config) { c.Latency.Read.Base = 0 }},
		{lat("Read.Jitter"), func(c *simdisk.Config) { c.Latency.Read.Jitter = 0 }},
		{lat("Write.Base"), func(c *simdisk.Config) { c.Latency.Write.Base = 0 }},
		{lat("Write.Jitter"), func(c *simdisk.Config) { c.Latency.Write.Jitter = 0 }},
		{lat("Sync.Base"), func(c *simdisk.Config) { c.Latency.Sync.Base = 0 }},
		{lat("Sync.Jitter"), func(c *simdisk.Config) { c.Latency.Sync.Jitter = 0 }},
		{lat("Meta.Base"), func(c *simdisk.Config) { c.Latency.Meta.Base = 0 }},
		{lat("Meta.Jitter"), func(c *simdisk.Config) { c.Latency.Meta.Jitter = 0 }},
		{lat("Slow"), func(c *simdisk.Config) { c.Latency.Slow = 0 }},
		{"invalid config: Latency.SlowPPM 1000001 exceeds 1000000", func(c *simdisk.Config) { c.Latency.SlowPPM = 0 }},
	}
	for i, s := range steps {
		if err := c.Validate(); err == nil || err.Error() != s.want {
			t.Fatalf("step %d: Validate() = %v, want %q", i, err, s.want)
		}
		s.fix(&c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("all fields repaired: Validate() = %v", err)
	}
}

func TestStrings(t *testing.T) {
	cases := []struct{ got, want string }{
		{simdisk.CrashAny.String(), "any"},
		{simdisk.CrashLoseUnsynced.String(), "lose_unsynced"},
		{simdisk.CrashKeepPrefix.String(), "keep_prefix"},
		{simdisk.CrashKeepSubset.String(), "keep_subset"},
		{simdisk.CrashTorn.String(), "torn"},
		{simdisk.CrashModel(7).String(), "CrashModel(7)"},
		{simdisk.MetadataStrict.String(), "strict"},
		{simdisk.MetadataImmediate.String(), "immediate"},
		{simdisk.MetadataModel(3).String(), "MetadataModel(3)"},
		{simdisk.FailedSyncDropDirty.String(), "drop_dirty"},
		{simdisk.FailedSyncKeepDirty.String(), "keep_dirty"},
		{simdisk.FailedSyncModel(2).String(), "FailedSyncModel(2)"},
		{simdisk.OpRead.String(), "read"},
		{simdisk.OpWrite.String(), "write"},
		{simdisk.OpSync.String(), "sync"},
		{simdisk.OpMeta.String(), "meta"},
		{simdisk.Op(0).String(), "Op(0)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("String() = %q, want %q", c.got, c.want)
		}
	}
}

// DSK-040
func TestDefaultLatency(t *testing.T) {
	us := time.Microsecond
	want := simdisk.LatencyConfig{
		Read:    simdisk.Latency{Base: 20 * us, Jitter: 80 * us},
		Write:   simdisk.Latency{Base: 20 * us, Jitter: 80 * us},
		Sync:    simdisk.Latency{Base: 500 * us, Jitter: 1500 * us},
		Meta:    simdisk.Latency{Base: 10 * us, Jitter: 40 * us},
		SlowPPM: 1000,
		Slow:    20 * time.Millisecond,
	}
	if got := simdisk.DefaultLatency(); got != want {
		t.Fatalf("DefaultLatency() = %+v, want %+v", got, want)
	}
	if simdisk.DefaultConfig().Latency != (simdisk.LatencyConfig{}) {
		t.Fatalf("DefaultConfig() has latency")
	}
}

// DSK-037: aliases match io/fs and os errors.
func TestErrorAliases(t *testing.T) {
	if !errors.Is(simdisk.ErrClosed, os.ErrClosed) || !errors.Is(simdisk.ErrNotExist, os.ErrNotExist) ||
		!errors.Is(simdisk.ErrExist, fs.ErrExist) || !errors.Is(simdisk.ErrInvalid, os.ErrInvalid) {
		t.Fatalf("aliases do not match io/fs and os errors")
	}
	if simdisk.MaxFileSize != 1<<30 || simdisk.DefaultSectorSize != 512 || simdisk.MaxLatency != time.Hour {
		t.Fatalf("constants: MaxFileSize %d, DefaultSectorSize %d, MaxLatency %v, want 1<<30, 512, 1h",
			simdisk.MaxFileSize, simdisk.DefaultSectorSize, simdisk.MaxLatency)
	}
}

// DSK §4, DSK-037: the sentinel texts, and every sentinel is distinct from every other, so only
// the four aliases match io/fs and os errors.
func TestSentinels(t *testing.T) {
	texts := []struct {
		err  error
		want string
	}{
		{simdisk.ErrIO, "simdisk: input/output error"},
		{simdisk.ErrNoSpace, "simdisk: no space left on device"},
		{simdisk.ErrStale, "simdisk: stale file handle"},
		{simdisk.ErrIsDir, "simdisk: is a directory"},
		{simdisk.ErrNotDir, "simdisk: not a directory"},
		{simdisk.ErrNotEmpty, "simdisk: directory not empty"},
		{simdisk.ErrTooLarge, "simdisk: file too large"},
	}
	for _, c := range texts {
		if c.err.Error() != c.want {
			t.Errorf("Error() = %q, want %q", c.err.Error(), c.want)
		}
	}
	all := []error{simdisk.ErrIO, simdisk.ErrNoSpace, simdisk.ErrStale, simdisk.ErrClosed, simdisk.ErrNotExist,
		simdisk.ErrExist, simdisk.ErrInvalid, simdisk.ErrIsDir, simdisk.ErrNotDir, simdisk.ErrNotEmpty, simdisk.ErrTooLarge}
	for i, a := range all {
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("errors.Is(%q, %q) = true; sentinels must be distinct", a, b)
			}
		}
	}
}
