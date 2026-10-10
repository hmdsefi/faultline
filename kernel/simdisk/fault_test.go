// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"bytes"
	"math"
	"math/bits"
	"strconv"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// tooLarge is one byte over MaxFileSize. It is a package-level array, so it stays off the heap;
// WriteFileDurable rejects it by its length without reading it.
var tooLarge [simdisk.MaxFileSize + 1]byte

// AT-DSK-16
func TestCorrupt(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, string(make([]byte, 16)), 0)
	must(t, f.Sync())
	must(t, k.v.Corrupt("/f", 4, 2))
	c := k.recordsOf("disk.corrupt")
	if len(c) != 1 || summary(c[0]) != "disk.corrupt{path=/f, off=4, len=2, visible=true}" || c[0].Text != "corrupt /f off=4 len=2" {
		t.Fatalf("disk.corrupt records %v", c)
	}
	r := replay(1, "disk/a")
	want := make([]byte, 16)
	want[4] = 1 << r.Uint64N(8)
	want[5] = 1 << r.Uint64N(8)
	if got := content(t, k.v, "/f"); got != string(want) {
		t.Fatalf("ReadFile = %v, want %v", []byte(got), want)
	}
	if got := durable(t, k.v, "/f"); got != string(want) {
		t.Fatalf("ReadDurable = %v, want %v", []byte(got), want)
	}

	write(t, f, "\xff", 4) // dirty: only the durable copy changes
	before := durable(t, k.v, "/f")[4]
	must(t, k.v.Corrupt("/f", 4, 1))
	if got := content(t, k.v, "/f")[4]; got != 0xff {
		t.Fatalf("visible byte 4 = %#x, want 0xff", got)
	}
	if diff := durable(t, k.v, "/f")[4] ^ before; bits.OnesCount8(diff) != 1 {
		t.Fatalf("durable byte 4 changed in %d bits", bits.OnesCount8(diff))
	}
	if got := attr(k.recordsOf("disk.corrupt")[1], "visible"); got != "false" {
		t.Fatalf("dirty corrupt visible=%s", got)
	}
	must(t, k.v.Corrupt("/f", 14, 10))
	if got := attr(k.recordsOf("disk.corrupt")[2], "len"); got != "2" {
		t.Fatalf("clipped corrupt len=%s, want 2", got)
	}
	must(t, k.v.Corrupt("/f", 100, 5))
	if got := attr(k.recordsOf("disk.corrupt")[3], "len"); got != "0" {
		t.Fatalf("corrupt beyond the end len=%s, want 0", got)
	}
	must(t, k.v.Corrupt("/f", 1, math.MaxInt)) // off+n would overflow: clipped at the durable size
	checkLast(t, k, "disk.corrupt{path=/f, off=1, len=15, visible=false}")

	k.crash()
	must(t, k.v.Corrupt("/f", 0, 1)) // works while down; visible after restart
	k.restart()
	if got := content(t, k.v, "/f")[0]; bits.OnesCount8(got) != 1 {
		t.Fatalf("byte 0 after restart = %#x, want one bit set", got)
	}

	checkPathErr(t, k.v.Corrupt("/nope", 0, 1), "corrupt", "/nope", simdisk.ErrNotExist)
	checkPathErr(t, k.v.Corrupt("/", 0, 1), "corrupt", "/", simdisk.ErrIsDir)
	checkPathErr(t, k.v.Corrupt("/f", -1, 1), "corrupt", "/f", simdisk.ErrInvalid)
	checkPathErr(t, k.v.Corrupt("/f", 0, -1), "corrupt", "/f", simdisk.ErrInvalid)
}

// DSK-033, DSK-008, §8: Corrupt checks the path first (an invalid one is reported as given, a
// valid one cleaned), then resolves it, then rejects a directory, and only then a negative off or
// n. A failed call emits no record and draws nothing.
func TestCorruptChecks(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "\x00", 0)
	must(t, f.Sync())
	for _, c := range []struct {
		path   string
		off    int64
		n      int
		want   string
		target error
	}{
		{"rel", -1, 1, "rel", simdisk.ErrInvalid},
		{"/x/../nope", -1, 1, "/nope", simdisk.ErrNotExist},
		{"/f/g", -1, 1, "/f/g", simdisk.ErrNotDir},
		{"/", -1, 1, "/", simdisk.ErrIsDir},
		{"/f", -1, 1, "/f", simdisk.ErrInvalid},
		{"/f", 0, -1, "/f", simdisk.ErrInvalid},
	} {
		checkPathErr(t, k.v.Corrupt(c.path, c.off, c.n), "corrupt", c.want, c.target)
	}
	if recs := k.recordsOf("disk.corrupt"); len(recs) != 0 {
		t.Fatalf("disk.corrupt records after failed calls: %v", recs)
	}
	if k.s.Rand("disk/a").Uint64() != replay(1, "disk/a").Uint64() {
		t.Fatalf("a failed Corrupt drew from disk/a")
	}
}

// DSK-033, DSK-036, §6.1: Corrupt resolves the path in the visible namespace (under
// MetadataStrict /f has no durable entry yet), works while the node is paused, and draws once per
// flipped byte, none for the bytes of the range beyond the durable size.
func TestCorruptVisiblePaused(t *testing.T) {
	k := newDisk(1, simdisk.Config{})
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "\x00\x00", 0)
	must(t, f.Sync())                 // clean, but not durable in /
	must(t, k.v.Corrupt("/f", 1, 10)) // one byte flipped, nine ignored
	k.a.Pause()
	must(t, k.v.Corrupt("/f", 0, 1))
	checkLast(t, k, "disk.corrupt{path=/f, off=1, len=1, visible=true}", "disk.corrupt{path=/f, off=0, len=1, visible=true}")
	r := replay(1, "disk/a")
	b1 := byte(1) << r.Uint64N(8)
	b0 := byte(1) << r.Uint64N(8)
	if got := content(t, k.v, "/f"); got != string([]byte{b0, b1}) {
		t.Fatalf("ReadFile = %v, want %v", []byte(got), []byte{b0, b1})
	}
	if k.s.Rand("disk/a").Uint64() != r.Uint64() {
		t.Fatalf("Corrupt drew for bytes beyond the durable size")
	}
}

// DSK-033, DSK-021, DSK-025: Corrupt's visible rule after failed syncs. Each case starts with
// Create, WriteAt("AAAA"), Sync and WriteAt("BBBB", 4), then runs its steps in order: 'f' is a
// Sync that fails (after FailSyncs(1)), 's' a Sync that succeeds, 'c' a crash and a restart. Only
// a failed sync that drops pending ops makes the file dirty for good; a later failed sync with no
// pending ops, or a later successful Sync, does not make it clean again, but a crash does.
func TestCorruptAfterFailedSync(t *testing.T) {
	drop, keep := simdisk.FailedSyncDropDirty, simdisk.FailedSyncKeepDirty
	for _, c := range []struct {
		name    string
		model   simdisk.FailedSyncModel
		steps   string
		visible bool
	}{
		{"drop", drop, "f", false},
		{"drop twice", drop, "ff", false},
		{"drop then sync", drop, "fs", false},
		{"sync then drop", drop, "sf", true},
		{"drop then crash", drop, "fc", true},
		{"keep", keep, "f", false},
		{"keep then sync", keep, "fs", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newDisk(1, simdisk.Config{Metadata: simdisk.MetadataImmediate, FailedSync: c.model})
			f, err := k.v.Create("/f")
			must(t, err)
			write(t, f, "AAAA", 0)
			must(t, f.Sync())
			write(t, f, "BBBB", 4)
			for _, step := range c.steps {
				switch step {
				case 'f':
					k.v.FailSyncs(1)
					checkPathErr(t, f.Sync(), "sync", "/f", simdisk.ErrIO)
				case 's':
					must(t, f.Sync())
				case 'c':
					k.crash() // no pending ops and Imm: the crash draws nothing
					k.restart()
				}
			}
			cur, dur := []byte(content(t, k.v, "/f")), []byte(durable(t, k.v, "/f"))
			must(t, k.v.Corrupt("/f", 0, 1))
			want := "disk.corrupt{path=/f, off=0, len=1, visible=" + strconv.FormatBool(c.visible) + "}"
			if recs := k.recordsOf("disk.corrupt"); len(recs) != 1 || summary(recs[0]) != want {
				t.Fatalf("disk.corrupt records %v, want %s", recs, want)
			}
			mask := byte(1) << replay(1, "disk/a").Uint64N(8)
			dur[0] ^= mask
			if c.visible {
				cur[0] ^= mask
			}
			if got := content(t, k.v, "/f"); got != string(cur) {
				t.Fatalf("ReadFile = %q, want %q", got, cur)
			}
			if got := durable(t, k.v, "/f"); got != string(dur) {
				t.Fatalf("ReadDurable = %q, want %q", got, dur)
			}
		})
	}
}

// AT-DSK-19
func TestNodeStateRules(t *testing.T) {
	k := newDisk(1, simdisk.Config{})
	k.crash()
	mustPanic(t, "simdisk: Create on a (id 1): node is down", func() { _, _ = k.v.Create("/x") })
	if _, err := k.v.ReadFile("/nope"); err == nil {
		t.Fatalf("ReadFile of a missing file succeeded")
	}
	if _, err := k.v.Stat("/"); err != nil {
		t.Fatalf("Stat while down: %v", err)
	}
	if _, err := k.v.ReadDir("/"); err != nil {
		t.Fatalf("ReadDir while down: %v", err)
	}
	must(t, k.v.WriteFileDurable("/cfg/x", []byte("1")))
	wd := k.recordsOf("disk.write_durable")
	if len(wd) != 1 || summary(wd[0]) != "disk.write_durable{path=/cfg/x, len=1}" || wd[0].Text != "write_durable /cfg/x len=1" {
		t.Fatalf("disk.write_durable records %v", wd)
	}
	if len(k.recordsOf("disk.mkdir")) != 0 {
		t.Fatalf("WriteFileDurable emitted disk.mkdir")
	}
	k.restart()
	if content(t, k.v, "/cfg/x") != "1" || durable(t, k.v, "/cfg/x") != "1" {
		t.Fatalf("WriteFileDurable content lost")
	}
	mustPanic(t, "simdisk: WriteFileDurable on a (id 1): node is up", func() { _ = k.v.WriteFileDurable("/y", nil) })
	f, err := k.v.Open("/cfg/x")
	must(t, err)
	k.a.Pause()
	mustPanic(t, "simdisk: WriteAt on a (id 1): node is paused", func() { _, _ = f.WriteAt([]byte("2"), 0) })
	mustPanic(t, "simdisk: Mkdir on a (id 1): node is paused", func() { _ = k.v.Mkdir("/z") })
	mustPanic(t, "simdisk: WriteFileDurable on a (id 1): node is paused", func() { _ = k.v.WriteFileDurable("/y", nil) })
	must(t, f.Close())
}

// DSK-032
func TestWriteFileDurable(t *testing.T) {
	k := newDisk(1, simdisk.Config{})
	b := k.s.AddNode("b", func(*kernel.Node) {})
	v := k.d.Volume(b) // before b's first boot
	v.SetCapacity(1)   // capacity is not enforced
	must(t, v.WriteFileDurable("/d/f", bytes.Repeat([]byte("x"), 10)))
	data := []byte("abc")
	must(t, v.WriteFileDurable("/d/f", data))
	data[0] = 'X' // the file has its own copies
	if got, _ := v.ReadFile("/d/f"); string(got) != "abc" {
		t.Fatalf("ReadFile(/d/f) after the caller changed data = %q", got)
	}
	if v.Usage() != 3 {
		t.Fatalf("Usage = %d, want 3", v.Usage())
	}
	checkPathErr(t, v.WriteFileDurable("/", nil), "writedurable", "/", simdisk.ErrIsDir)
	checkPathErr(t, v.WriteFileDurable("/d", nil), "writedurable", "/d", simdisk.ErrIsDir)
	checkPathErr(t, v.WriteFileDurable("/d/f/g", nil), "writedurable", "/d/f/g", simdisk.ErrNotDir)
	checkPathErr(t, v.WriteFileDurable("rel", nil), "writedurable", "rel", simdisk.ErrInvalid)
	checkPathErr(t, v.WriteFileDurable("/p/n", tooLarge[:]), "writedurable", "/p/n", simdisk.ErrTooLarge)
	_, err := v.Stat("/p") // the size is checked before any parent is created
	checkPathErr(t, err, "stat", "/p", simdisk.ErrNotExist)
	if recs := k.recordsOf("disk.write_durable"); len(recs) != 2 {
		t.Fatalf("disk.write_durable records %v, want only the two successful calls", recs)
	}
	k.s.RunUntil(0) // b boots
	got, err := v.ReadDurable("/d/f")
	if err != nil || string(got) != "abc" {
		t.Fatalf("ReadDurable(/d/f) = %q, %v", got, err)
	}
	b.Crash() // nothing is pending, so the crash draws nothing and keeps the file
	if got := content(t, v, "/d/f"); got != "abc" {
		t.Fatalf("after crash ReadFile(/d/f) = %q", got)
	}

	must(t, v.WriteFileDurable("/e", []byte("ab"))) // a new file, after the crash's reset
	b.Restart()
	k.s.RunFor(0) // b is up
	v.SetCapacity(0)
	g, err := v.Open("/e")
	must(t, err)
	write(t, g, "Z", 0)
	appendAll(t, g, "c")
	if v.Usage() != 6 { // /e is linked: its growth counts
		t.Fatalf("Usage after an Append to /e = %d, want 6", v.Usage())
	}
	if got := durable(t, v, "/e"); got != "ab" { // cur and dur are separate copies
		t.Fatalf("ReadDurable(/e) after an unsynced WriteAt = %q, want ab", got)
	}
}
