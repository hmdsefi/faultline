// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"testing"

	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// durable returns the durable content of path and fails the test on error.
func durable(t *testing.T, v *simdisk.Volume, path string) string {
	t.Helper()
	b, err := v.ReadDurable(path)
	if err != nil {
		t.Fatalf("ReadDurable(%s): %v", path, err)
	}
	return string(b)
}

// failedSync runs the AT-DSK-12/13 steps before the crash with model and returns the disk.
func failedSync(t *testing.T, model simdisk.FailedSyncModel) *disk {
	t.Helper()
	k := newDisk(1, simdisk.Config{Crash: simdisk.CrashLoseUnsynced, Metadata: simdisk.MetadataImmediate, FailedSync: model})
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "AAAA", 0)
	must(t, f.Sync())
	write(t, f, "BBBB", 4)
	k.v.FailSyncs(1)
	checkPathErr(t, f.Sync(), "sync", "/f", simdisk.ErrIO)
	sf := k.recordsOf("disk.sync_fail")
	want := "disk.sync_fail{path=/f, model=" + model.String() + ", ops=1, left=0}"
	if len(sf) != 1 || summary(sf[0]) != want || sf[0].Text != "sync /f failed: "+model.String()+" ops=1" {
		t.Fatalf("disk.sync_fail records %v, want %s", sf, want)
	}
	if got := content(t, k.v, "/f"); got != "AAAABBBB" {
		t.Fatalf("ReadFile after the failed sync = %q", got)
	}
	if got := durable(t, k.v, "/f"); got != "AAAA" {
		t.Fatalf("ReadDurable after the failed sync = %q", got)
	}
	write(t, f, "CCCC", 8)
	must(t, f.Sync())
	if got := content(t, k.v, "/f"); got != "AAAABBBBCCCC" {
		t.Fatalf("ReadFile after the second sync = %q", got)
	}
	return k
}

// AT-DSK-12 (before the crash)
func TestFailedSyncDropDirty(t *testing.T) {
	k := failedSync(t, simdisk.FailedSyncDropDirty)
	if got := durable(t, k.v, "/f"); got != "AAAA\x00\x00\x00\x00CCCC" {
		t.Fatalf("ReadDurable = %q, want AAAA\\0\\0\\0\\0CCCC", got)
	}

	k = newDisk(1, imm) // disk.sync reports the durable size, here shorter than the visible one
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "AAAA", 0)
	must(t, f.Sync())
	write(t, f, "BBBB", 4)
	k.v.FailSyncs(1)
	checkPathErr(t, f.Sync(), "sync", "/f", simdisk.ErrIO)
	must(t, f.Sync())
	sy := k.recordsOf("disk.sync")
	if len(sy) != 2 || summary(sy[1]) != "disk.sync{path=/f, ops=0, size=4}" || sy[1].Text != "sync /f ops=0 size=4" {
		t.Fatalf("disk.sync records %v, want the second with ops=0, size=4", sy)
	}
	if got := content(t, k.v, "/f"); got != "AAAABBBB" {
		t.Fatalf("ReadFile = %q, want AAAABBBB", got)
	}
}

// AT-DSK-13 (before the crash)
func TestFailedSyncKeepDirty(t *testing.T) {
	k := failedSync(t, simdisk.FailedSyncKeepDirty)
	if got := durable(t, k.v, "/f"); got != "AAAABBBBCCCC" {
		t.Fatalf("ReadDurable = %q, want AAAABBBBCCCC", got)
	}

	k = newDisk(1, simdisk.Config{}) // strict: a failed SyncDir keeps the namespace ops pending
	_, err := k.v.Create("/g")
	must(t, err)
	k.v.FailSyncs(1)
	checkPathErr(t, k.v.SyncDir("/"), "syncdir", "/", simdisk.ErrIO)
	sf := k.recordsOf("disk.sync_dir_fail")
	if len(sf) != 1 || summary(sf[0]) != "disk.sync_dir_fail{path=/, left=0}" || sf[0].Text != "syncdir / failed" {
		t.Fatalf("disk.sync_dir_fail records %v", sf)
	}
	_, err = k.v.ReadDurable("/g")
	checkPathErr(t, err, "readdurable", "/g", simdisk.ErrNotExist)
	must(t, k.v.SyncDir("/"))
	if got := durable(t, k.v, "/g"); got != "" {
		t.Fatalf("ReadDurable(/g) = %q, want empty", got)
	}
}

// DSK-021, DSK-022, DSK-035: forced failures are consumed by Sync and SyncDir in call order, only
// after their own checks pass (a closed handle consumes none), also by a Sync with no pending ops.
// FailSyncs replaces or clears the count, emits disk.fail_syncs, panics for n < 0 without changing
// anything (AT-DSK-01), and works while paused.
func TestFailSyncsConsumption(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	k.v.FailSyncs(2)
	checkPathErr(t, k.v.SyncDir("/nope"), "syncdir", "/nope", simdisk.ErrNotExist)
	checkPathErr(t, k.v.SyncDir("/f"), "syncdir", "/f", simdisk.ErrNotDir)
	checkPathErr(t, f.Sync(), "sync", "/f", simdisk.ErrIO) // no pending ops, still consumed
	checkPathErr(t, k.v.SyncDir("/"), "syncdir", "/", simdisk.ErrIO)
	must(t, f.Sync())
	must(t, k.v.SyncDir("/"))
	k.v.FailSyncs(3)
	k.v.FailSyncs(0) // replaces the count
	must(t, f.Sync())
	fs := k.recordsOf("disk.fail_syncs")
	if len(fs) != 3 || summary(fs[0]) != "disk.fail_syncs{n=2}" || fs[0].Text != "fail next 2 syncs" || summary(fs[2]) != "disk.fail_syncs{n=0}" {
		t.Fatalf("disk.fail_syncs records %v", fs)
	}
	k.v.FailSyncs(2)
	n := len(k.records())
	mustPanic(t, "simdisk: FailSyncs on a (id 1): negative count -1", func() { k.v.FailSyncs(-1) }) // AT-DSK-01

	// The panicking call left the count at 2 and emitted nothing.
	checkPathErr(t, f.Sync(), "sync", "/f", simdisk.ErrIO)
	if r := k.records(); len(r) != n+1 || summary(r[n]) != "disk.sync_fail{path=/f, model=drop_dirty, ops=0, left=1}" {
		t.Fatalf("records after FailSyncs(-1) %v, want only disk.sync_fail with left=1", r[n:])
	}
	g, err := k.v.Open("/f")
	must(t, err)
	must(t, g.Close())
	checkPathErr(t, g.Sync(), "sync", "/f", simdisk.ErrClosed) // does not consume the remaining failure
	checkPathErr(t, f.Sync(), "sync", "/f", simdisk.ErrIO)
	k.a.Pause()
	k.v.FailSyncs(1) // every node state
	mustPanic(t, "simdisk: SyncDir on a (id 1): node is paused", func() { _ = k.v.SyncDir("/") })
	mustPanic(t, "simdisk: Sync on a (id 1): node is paused", func() { _ = f.Sync() })
}

// AT-DSK-24
func TestExactRecordSequence(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	must(t, v.Mkdir("/d"))
	f, err := v.Create("/d/f")
	must(t, err)
	if _, err := f.Append([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	must(t, f.Sync())
	must(t, v.Rename("/d/f", "/d/g"))
	must(t, v.SyncDir("/d"))
	must(t, v.Remove("/d/g"))
	v.FailSyncs(2)
	v.SetCapacity(100)
	want := []struct{ summary, text string }{
		{"disk.mkdir{path=/d}", "mkdir /d"},
		{"disk.create{path=/d/f, truncated=false}", "create /d/f"},
		{"disk.write{path=/d/f, op=append, off=0, len=3}", "append /d/f off=0 len=3"},
		{"disk.sync{path=/d/f, ops=1, size=3}", "sync /d/f ops=1 size=3"},
		{"disk.rename{old_path=/d/f, new_path=/d/g, replaced=false}", "rename /d/f -> /d/g"},
		{"disk.sync_dir{path=/d, ops=0}", "syncdir /d ops=0"},
		{"disk.remove{path=/d/g, dir=false}", "remove /d/g"},
		{"disk.fail_syncs{n=2}", "fail next 2 syncs"},
		{"disk.capacity{bytes=100, used=0}", "capacity 100 used=0"},
	}
	recs := k.records()
	if len(recs) != len(want) {
		t.Fatalf("%d records, want %d", len(recs), len(want))
	}
	for i, r := range recs {
		if summary(r) != want[i].summary || r.Text != want[i].text || r.Node != 1 {
			t.Errorf("record %d = %s %q, want %s %q", i, summary(r), r.Text, want[i].summary, want[i].text)
		}
	}
	if _, ok := k.s.NextAt(); ok {
		t.Fatalf("a disk operation scheduled a kernel event (DSK-003)")
	}
}

// AT-DSK-18
func TestRename(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	f, err := v.Create("/f")
	must(t, err)
	write(t, f, "old", 0)
	g, err := v.Create("/g")
	must(t, err)
	write(t, g, "new!", 0)
	must(t, v.MkdirAll("/p/q"))
	must(t, v.MkdirAll("/r/s"))
	must(t, v.Mkdir("/e"))
	hf, err := v.Open("/f")
	must(t, err)
	usage := v.Usage()
	must(t, v.Rename("/g", "/f"))
	rr := k.recordsOf("disk.rename")
	if len(rr) != 1 || attr(rr[0], "replaced") != "true" {
		t.Fatalf("disk.rename records %v", rr)
	}
	if got := content(t, v, "/f"); got != "new!" {
		t.Fatalf("ReadFile(/f) = %q", got)
	}
	p := make([]byte, 3)
	if n, _ := hf.ReadAt(p, 0); n != 3 || string(p) != "old" {
		t.Fatalf("open handle reads %q", p[:n])
	}
	if v.Usage() != usage-3 {
		t.Fatalf("Usage = %d, want %d", v.Usage(), usage-3)
	}
	checkPathErr(t, v.Rename("/p", "/p/q/x"), "rename", "/p/q/x", simdisk.ErrInvalid)
	checkPathErr(t, v.Rename("/f", "/e"), "rename", "/e", simdisk.ErrIsDir)
	checkPathErr(t, v.Rename("/p", "/r"), "rename", "/r", simdisk.ErrNotEmpty)
	must(t, v.Rename("/p", "/e"))
	n := len(k.records())
	must(t, v.Rename("/f", "/f"))
	if len(k.records()) != n {
		t.Fatalf("Rename(/f, /f) emitted a record")
	}
	checkPathErr(t, v.Rename("/missing", "/x"), "rename", "/missing", simdisk.ErrNotExist)

	k = newDisk(1, simdisk.Config{}) // strict atomic replace
	tmp, err := k.v.Create("/tmp")
	must(t, err)
	if _, err := tmp.Append([]byte("v2")); err != nil {
		t.Fatal(err)
	}
	must(t, tmp.Sync())
	must(t, k.v.Rename("/tmp", "/data"))
	must(t, k.v.SyncDir("/"))
	if got := durable(t, k.v, "/data"); got != "v2" {
		t.Fatalf("ReadDurable(/data) = %q", got)
	}
	_, err = k.v.ReadDurable("/tmp")
	checkPathErr(t, err, "readdurable", "/tmp", simdisk.ErrNotExist)
}

// AT-DSK-11 (before the crash), DSK-024
func TestSyncDirClosure(t *testing.T) {
	k := syncDirScenario(t, 1)
	_, err := k.v.ReadDurable("/b/y")
	checkPathErr(t, err, "readdurable", "/b/y", simdisk.ErrNotExist)
	_, err = k.v.ReadDurable("/b") // SyncDir("/b") does not make /b durable in /
	checkPathErr(t, err, "readdurable", "/b", simdisk.ErrNotExist)

	// The rename is selected through its source /a, so the closure adds its other directory /b
	// and selects the earlier Create("/b/w").
	k = newDisk(1, simdisk.Config{})
	must(t, k.v.Mkdir("/a"))
	must(t, k.v.Mkdir("/b"))
	_, err = k.v.Create("/b/w")
	must(t, err)
	_, err = k.v.Create("/a/x")
	must(t, err)
	must(t, k.v.Rename("/a/x", "/b/y"))
	must(t, k.v.SyncDir("/a"))
	must(t, k.v.SyncDir("/"))
	sd := k.recordsOf("disk.sync_dir")
	if len(sd) != 2 || summary(sd[0]) != "disk.sync_dir{path=/a, ops=3}" || summary(sd[1]) != "disk.sync_dir{path=/, ops=2}" {
		t.Fatalf("disk.sync_dir records %v, want /a ops=3, then / ops=2", sd)
	}
	for _, p := range []string{"/b/w", "/b/y"} {
		if got := durable(t, k.v, p); got != "" {
			t.Fatalf("ReadDurable(%s) = %q, want empty", p, got)
		}
	}
}

// DSK-023, DSK-036, DSK-008, DSK-037: SyncDir checks the node state before the path, and its
// errors carry the raw argument for an invalid path and the cleaned path otherwise.
func TestSyncDirChecks(t *testing.T) {
	k := newDisk(1, imm)
	_, err := k.v.Create("/f")
	must(t, err)
	checkPathErr(t, k.v.SyncDir("rel"), "syncdir", "rel", simdisk.ErrInvalid)
	checkPathErr(t, k.v.SyncDir("//nope/"), "syncdir", "/nope", simdisk.ErrNotExist)
	checkPathErr(t, k.v.SyncDir("/f/"), "syncdir", "/f", simdisk.ErrNotDir)
	checkPathErr(t, k.v.SyncDir("/f/x"), "syncdir", "/f/x", simdisk.ErrNotDir)
	k.v.FailSyncs(1)
	checkPathErr(t, k.v.SyncDir("/./"), "syncdir", "/", simdisk.ErrIO)
	must(t, k.v.SyncDir("//"))
	sf, sd := k.recordsOf("disk.sync_dir_fail"), k.recordsOf("disk.sync_dir")
	if len(sf) != 1 || summary(sf[0]) != "disk.sync_dir_fail{path=/, left=0}" || len(sd) != 1 || summary(sd[0]) != "disk.sync_dir{path=/, ops=0}" {
		t.Fatalf("disk.sync_dir_fail records %v, disk.sync_dir records %v", sf, sd)
	}
	k.a.Pause()
	mustPanic(t, "simdisk: SyncDir on a (id 1): node is paused", func() { _ = k.v.SyncDir("rel") })
	k.crash()
	mustPanic(t, "simdisk: SyncDir on a (id 1): node is down", func() { _ = k.v.SyncDir("") })
}

// DSK-034, DSK-008, DSK-037: ReadDurable works in every node state, returns a new slice, emits no
// record, and reports errors like ReadFile: the raw argument for an invalid path, the cleaned
// path otherwise, and ErrIsDir for a directory.
func TestReadDurable(t *testing.T) {
	k := newDisk(1, imm)
	must(t, k.v.Mkdir("/d"))
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "abc", 0)
	must(t, f.Sync())
	n := len(k.records())
	b, err := k.v.ReadDurable("/f")
	must(t, err)
	b[0] = 'X'
	if got := durable(t, k.v, "/f"); got != "abc" {
		t.Fatalf("ReadDurable after changing the returned slice = %q, want abc", got)
	}
	_, err = k.v.ReadDurable("rel")
	checkPathErr(t, err, "readdurable", "rel", simdisk.ErrInvalid)
	_, err = k.v.ReadDurable("//nope/../missing")
	checkPathErr(t, err, "readdurable", "/missing", simdisk.ErrNotExist)
	_, err = k.v.ReadDurable("/f/x")
	checkPathErr(t, err, "readdurable", "/f/x", simdisk.ErrNotDir)
	_, err = k.v.ReadDurable("/d/")
	checkPathErr(t, err, "readdurable", "/d", simdisk.ErrIsDir)
	_, err = k.v.ReadDurable("/")
	checkPathErr(t, err, "readdurable", "/", simdisk.ErrIsDir)
	k.a.Pause()
	if got := durable(t, k.v, "/f"); got != "abc" {
		t.Fatalf("ReadDurable while paused = %q, want abc", got)
	}
	if r := k.records(); len(r) != n {
		t.Fatalf("ReadDurable emitted records %v", r[n:])
	}
	k.crash()
	if got := durable(t, k.v, "/f"); got != "abc" {
		t.Fatalf("ReadDurable while down = %q, want abc", got)
	}
}

// syncDirScenario runs the AT-DSK-11 steps up to SyncDir("/b") with a strict, lose-unsynced
// config and checks the disk.sync_dir record.
func syncDirScenario(t *testing.T, seed uint64) *disk {
	t.Helper()
	k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashLoseUnsynced})
	must(t, k.v.Mkdir("/a"))
	must(t, k.v.Mkdir("/b"))
	_, err := k.v.Create("/a/x")
	must(t, err)
	must(t, k.v.Rename("/a/x", "/b/y"))
	_, err = k.v.Create("/b/z")
	must(t, err)
	must(t, k.v.SyncDir("/b"))
	sd := k.recordsOf("disk.sync_dir")
	if len(sd) != 1 || summary(sd[0]) != "disk.sync_dir{path=/b, ops=3}" || sd[0].Text != "syncdir /b ops=3" {
		t.Fatalf("disk.sync_dir records %v", sd)
	}
	return k
}

// DSK-010: Create of an existing file truncates that file, and the truncation is a pending data
// op that Sync makes durable.
func TestCreateTruncatesDurably(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "abcdef", 0)
	must(t, f.Sync())
	g, err := k.v.Create("/f")
	must(t, err)
	write(t, g, "xy", 0)
	if got := content(t, k.v, "/f"); got != "xy" || f.Size() != 2 {
		t.Fatalf("after re-Create and write: ReadFile = %q, old handle Size %d; want xy, 2", got, f.Size())
	}
	must(t, g.Sync())
	if got := durable(t, k.v, "/f"); got != "xy" {
		t.Fatalf("ReadDurable = %q, want xy", got)
	}
}

// DSK-012, DSK-013: a directory is empty when it has no visible entries, even if its durable
// namespace still lists some.
func TestEmptyMeansNoVisibleEntries(t *testing.T) {
	k := newDisk(1, simdisk.Config{})
	v := k.v
	for _, d := range []string{"/d", "/s", "/t"} {
		must(t, v.Mkdir(d))
	}
	for _, p := range []string{"/d/f", "/t/f"} {
		_, err := v.Create(p)
		must(t, err)
	}
	must(t, v.SyncDir("/d"))
	must(t, v.SyncDir("/t"))
	must(t, v.Remove("/d/f"))
	must(t, v.Remove("/t/f"))
	must(t, v.Remove("/d"))
	must(t, v.Rename("/s", "/t"))
}

// DSK-004, DSK-018, DSK-020: a pending op owns a copy of the bytes it wrote, and a clamped write
// keeps only those bytes. A truncate to the current size is still a pending op.
func TestPendingOpsOwnTheirData(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	p := []byte("abcd")
	if _, err := f.WriteAt(p, 0); err != nil {
		t.Fatal(err)
	}
	must(t, f.Truncate(4))
	k.v.SetCapacity(6)
	q := []byte("efgh")
	n, err := f.Append(q)
	if n != 2 {
		t.Fatalf("Append wrote %d bytes, want 2", n)
	}
	checkPathErr(t, err, "append", "/f", simdisk.ErrNoSpace)
	copy(p, "WXYZ")
	copy(q, "WXYZ")
	must(t, f.Sync())
	if got := durable(t, k.v, "/f"); got != "abcdef" {
		t.Fatalf("ReadDurable = %q, want abcdef", got)
	}
	sy := k.recordsOf("disk.sync")
	if len(sy) != 1 || summary(sy[0]) != "disk.sync{path=/f, ops=3, size=6}" {
		t.Fatalf("disk.sync records %v", sy)
	}
}
