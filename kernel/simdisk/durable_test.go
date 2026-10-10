// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"testing"

	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// AT-DSK-27, DSK-043: DurableSize counts the synced bytes, the ones Corrupt can damage, and not an
// unsynced tail or an unsynced shrink. It resolves the path in the visible namespace as Corrupt
// does, works while the node is paused or down, and emits no record and draws nothing.
func TestDurableSize(t *testing.T) {
	k := newDisk(1, simdisk.Config{Crash: simdisk.CrashLoseUnsynced})
	size := func(path string, want int64) {
		t.Helper()
		n := len(k.s.Records())
		got, err := k.v.DurableSize(path)
		if err != nil || got != want {
			t.Fatalf("DurableSize(%s) = %d, %v; want %d", path, got, err, want)
		}
		if len(k.s.Records()) != n {
			t.Fatalf("DurableSize(%s) emitted a record", path)
		}
	}
	visible := func(path string, want int64) {
		t.Helper()
		if info, err := k.v.Stat(path); err != nil || info.Size != want {
			t.Fatalf("Stat(%s) = %+v, %v; want size %d", path, info, err, want)
		}
	}
	f, err := k.v.Create("/f")
	must(t, err)
	appendAll(t, f, "abcd")
	must(t, f.Sync())
	appendAll(t, f, "efghij") // unsynced tail
	size("/f", 4)
	visible("/f", 10)

	// /f has no durable entry yet (MetadataStrict, no SyncDir): DurableSize resolves the visible
	// namespace, as Corrupt does, and Corrupt damages exactly the bytes it counts.
	_, err = k.v.ReadDurable("/f")
	checkPathErr(t, err, "readdurable", "/f", simdisk.ErrNotExist)
	must(t, k.v.Corrupt("/f", 0, 10))
	checkLast(t, k, "disk.corrupt{path=/f, off=0, len=4, visible=false}")

	must(t, f.Truncate(2)) // unsynced shrink: the durable bytes still count
	size("/f", 4)
	visible("/f", 2)

	must(t, k.v.Mkdir("/d"))
	n := len(k.s.Records())
	for _, c := range []struct {
		path   string
		want   string
		target error
	}{
		{"f", "f", simdisk.ErrInvalid},
		{"", "", simdisk.ErrInvalid},
		{"/x/../nope", "/nope", simdisk.ErrNotExist},
		{"/f/x", "/f/x", simdisk.ErrNotDir},
		{"/d", "/d", simdisk.ErrIsDir},
		{"/d/..", "/", simdisk.ErrIsDir},
	} {
		got, err := k.v.DurableSize(c.path)
		if got != 0 {
			t.Errorf("DurableSize(%q) = %d with an error", c.path, got)
		}
		checkPathErr(t, err, "durablesize", c.want, c.target)
	}
	if len(k.s.Records()) != n {
		t.Fatalf("a failed DurableSize emitted a record")
	}
	size("/d/../f", 4)

	k.a.Pause()
	size("/f", 4)
	k.a.Resume()
	must(t, k.v.SyncDir("/"))
	k.crash() // nothing in the namespace log and CrashLoseUnsynced: the crash draws nothing
	size("/f", 4)

	r := replay(1, "disk/a")
	for range 4 {
		r.Uint64N(8) // Corrupt's draws
	}
	if k.s.Rand("disk/a").Uint64() != r.Uint64() {
		t.Fatalf("DurableSize drew from disk/a")
	}
}
