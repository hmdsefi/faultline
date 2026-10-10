// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// crashApply returns the disk.crash_apply records as summaries.
func (k *disk) crashApply() []string {
	var out []string
	for _, r := range k.recordsOf("disk.crash_apply") {
		out = append(out, summary(r))
	}
	return out
}

// applyRec is the summary of a disk.crash_apply record (DSK §8).
func applyRec(path, model string, pending, kept, torn, size int) string {
	return fmt.Sprintf("disk.crash_apply{path=%s, model=%s, pending=%d, kept=%d, torn_sectors=%d, size=%d}",
		path, model, pending, kept, torn, size)
}

// replayAny replays the draws for one file under CrashAny, one pending one-byte append of b to
// an empty durable file (§6.2, DSK-027), and returns the model, the disk.crash_apply summary and
// the content after the crash.
func replayAny(r *rand.Rand, path, b string) (model, rec, content string) {
	model = []string{"lose_unsynced", "keep_prefix", "keep_subset", "torn"}[r.Uint64N(4)]
	kept, torn := 0, 0
	switch model {
	case "keep_prefix":
		kept = int(r.Uint64N(2)) //nolint:gosec // below 2: replays crash's draw
	case "keep_subset":
		if kernel.Chance(r, 500000) {
			kept = 1
		}
	case "torn":
		r.Uint64N(1) //nolint:staticcheck // k = 0: replays the draw to keep the stream aligned
		if kernel.Chance(r, 500000) {
			torn = 1
		}
	}
	content = b[:kept+torn]
	return model, applyRec(path, model, 1, kept, torn, len(content)), content
}

// appendAll appends each of ss to f, in order, and fails the test on error.
func appendAll(t *testing.T, f *simdisk.File, ss ...string) {
	t.Helper()
	for _, s := range ss {
		if _, err := f.Append([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
}

// AT-DSK-05
func TestSyncCrashStaleHandle(t *testing.T) {
	k := newDisk(1, simdisk.Config{Crash: simdisk.CrashLoseUnsynced, Metadata: simdisk.MetadataImmediate})
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "AAAA", 0)
	must(t, f.Sync())
	if _, err := f.Append([]byte("BBBB")); err != nil {
		t.Fatal(err)
	}
	k.crash()
	meta := k.recordsOf("disk.crash_meta")
	if len(meta) != 1 || summary(meta[0]) != "disk.crash_meta{model=immediate, pending=0, kept=0}" ||
		meta[0].Text != "crash metadata immediate: kept 0 of 0" {
		t.Fatalf("disk.crash_meta records %v", meta)
	}
	ca := k.recordsOf("disk.crash_apply")
	if len(ca) != 1 || summary(ca[0]) != "disk.crash_apply{path=/f, model=lose_unsynced, pending=1, kept=0, torn_sectors=0, size=4}" ||
		ca[0].Text != "crash /f lose_unsynced: kept 0 of 1" {
		t.Fatalf("disk.crash_apply records %v", ca)
	}
	checkLast(t, k, summary(meta[0]), summary(ca[0])) // the crash's records, in order
	k.restart()
	if got := content(t, k.v, "/f"); got != "AAAA" {
		t.Fatalf("ReadFile after restart = %q", got)
	}
	n, err := f.WriteAt([]byte("x"), 0)
	if n != 0 {
		t.Fatalf("stale WriteAt wrote %d bytes", n)
	}
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrStale)
	st := k.recordsOf("disk.stale")
	if len(st) != 1 || summary(st[0]) != "disk.stale{op=writeat, path=/f}" || st[0].Text != "writeat /f: stale handle" {
		t.Fatalf("disk.stale records %v", st)
	}
	if f.Size() != -1 {
		t.Fatalf("stale Size() = %d", f.Size())
	}
	if k.d.Volume(k.a) != k.v {
		t.Fatalf("Volume changed across restart")
	}
}

// AT-DSK-06
func TestKeepPrefix(t *testing.T) {
	seen := map[string]bool{}
	for seed := uint64(1); seed <= 300; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashKeepPrefix, Metadata: simdisk.MetadataImmediate})
		f, err := k.v.Create("/f")
		must(t, err)
		for _, s := range []string{"1", "2", "3", "4", "5"} {
			if _, err := f.Append([]byte(s)); err != nil {
				t.Fatal(err)
			}
		}
		k.crash()
		kept := int(replay(seed, "disk/a").Uint64N(6)) //nolint:gosec // below 6: replays crash's draw
		checkLast(t, k, applyRec("/f", "keep_prefix", 5, kept, 0, kept))
		k.restart()
		want := "12345"[:kept]
		if got := content(t, k.v, "/f"); got != want {
			t.Fatalf("seed %d: ReadFile = %q, want %q", seed, got, want)
		}
		seen[want] = true
	}
	if len(seen) != 6 {
		t.Fatalf("prefixes seen: %v, want all six", seen)
	}
}

// AT-DSK-07
func TestKeepSubset(t *testing.T) {
	reordered := false
	for seed := uint64(1); seed <= 100; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashKeepSubset, Metadata: simdisk.MetadataImmediate})
		f, err := k.v.Create("/f")
		must(t, err)
		for _, s := range []string{"a", "b", "c", "d", "e"} {
			if _, err := f.Append([]byte(s)); err != nil {
				t.Fatal(err)
			}
		}
		k.crash()
		r := replay(seed, "disk/a")
		var want []byte
		kept := 0
		lostEarlier := false
		for i := 0; i < 5; i++ {
			if !kernel.Chance(r, 500000) {
				lostEarlier = true
				continue
			}
			kept++
			if lostEarlier {
				reordered = true
			}
			for len(want) < i+1 {
				want = append(want, 0)
			}
			want[i] = byte('a' + i)
		}
		if got := content(t, k.v, "/f"); got != string(want) {
			t.Fatalf("seed %d: ReadFile = %q, want %q", seed, got, want)
		}
		checkLast(t, k, applyRec("/f", "keep_subset", 5, kept, 0, len(want)))
	}
	if !reordered {
		t.Fatalf("no seed kept an op while losing an earlier one")
	}

	// overlapping writes: the kept ops are applied oldest first
	both := false
	for seed := uint64(1); seed <= 40; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashKeepSubset, Metadata: simdisk.MetadataImmediate})
		f, err := k.v.Create("/f")
		must(t, err)
		write(t, f, "A", 0)
		write(t, f, "B", 0)
		k.crash()
		r := replay(seed, "disk/a")
		a, b := kernel.Chance(r, 500000), kernel.Chance(r, 500000)
		want := ""
		if a {
			want = "A"
		}
		if b {
			want = "B"
		}
		if got := content(t, k.v, "/f"); got != want {
			t.Fatalf("seed %d: ReadFile after overlapping writes = %q, want %q", seed, got, want)
		}
		both = both || a && b
	}
	if !both {
		t.Fatalf("no seed kept both overlapping writes")
	}
}

// tornCase runs AT-DSK-08 for one seed: a synced file of 1536 bytes of 0x11 and one pending
// write of n bytes of fill at off; it returns the content after the crash and the record.
func tornCase(t *testing.T, seed uint64, off int64, n int, fill byte) (string, kernel.Record) {
	t.Helper()
	k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashTorn, Metadata: simdisk.MetadataImmediate, SectorSize: 512})
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, string(bytes.Repeat([]byte{0x11}, 1536)), 0)
	must(t, f.Sync())
	write(t, f, string(bytes.Repeat([]byte{fill}, n)), off)
	k.crash()
	return content(t, k.v, "/f"), k.recordsOf("disk.crash_apply")[0]
}

// AT-DSK-08
func TestTornWrites(t *testing.T) {
	chunks := [][2]int{{0, 512}, {512, 1024}, {1024, 1536}}
	unaligned := [][2]int{{300, 512}, {512, 1024}, {1024, 1300}}
	mixed := false
	for seed := uint64(1); seed <= 100; seed++ {
		for _, c := range []struct {
			off    int64
			n      int
			fill   byte
			chunks [][2]int
		}{{0, 1536, 0x22, chunks}, {300, 1000, 0x33, unaligned}} {
			got, rec := tornCase(t, seed, c.off, c.n, c.fill)
			r := replay(seed, "disk/a")
			r.Uint64N(1) //nolint:staticcheck // k = 0: replays the draw to keep the stream aligned
			want := bytes.Repeat([]byte{0x11}, 1536)
			torn := 0
			for _, ch := range c.chunks {
				if kernel.Chance(r, 500000) {
					copy(want[ch[0]:ch[1]], bytes.Repeat([]byte{c.fill}, ch[1]-ch[0]))
					torn++
				}
			}
			if got != string(want) {
				t.Fatalf("seed %d off %d: torn content differs from the replay", seed, c.off)
			}
			if attr(rec, "torn_sectors") != string(rune('0'+torn)) || attr(rec, "kept") != "0" || attr(rec, "model") != "torn" {
				t.Fatalf("seed %d off %d: record %s, want torn_sectors=%d", seed, c.off, summary(rec), torn)
			}
			if torn == 1 || torn == 2 {
				mixed = true
			}
		}
	}
	if !mixed {
		t.Fatalf("no seed gave a mixed result")
	}
}

// AT-DSK-08 with SectorSize 4096: a pending write over two 4096-byte sectors tears into two chunks.
func TestTornSectorSize(t *testing.T) {
	mixed := false
	for seed := uint64(1); seed <= 20; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashTorn, Metadata: simdisk.MetadataImmediate, SectorSize: 4096})
		f, err := k.v.Create("/f")
		must(t, err)
		write(t, f, string(bytes.Repeat([]byte{0x11}, 8192)), 0)
		must(t, f.Sync())
		write(t, f, string(bytes.Repeat([]byte{0x22}, 8192)), 0)
		k.crash()
		r := replay(seed, "disk/a")
		r.Uint64N(1) //nolint:staticcheck // k = 0: replays the draw to keep the stream aligned
		want := bytes.Repeat([]byte{0x11}, 8192)
		torn := 0
		for _, ch := range [][2]int{{0, 4096}, {4096, 8192}} {
			if kernel.Chance(r, 500000) {
				copy(want[ch[0]:ch[1]], bytes.Repeat([]byte{0x22}, 4096))
				torn++
			}
		}
		if got := content(t, k.v, "/f"); got != string(want) {
			t.Fatalf("seed %d: torn content differs from the replay", seed)
		}
		checkLast(t, k, applyRec("/f", "torn", 1, 0, torn, 8192))
		mixed = mixed || torn == 1
	}
	if !mixed {
		t.Fatalf("no seed gave a mixed result")
	}
}

// DSK-027, CrashTorn with more than one pending op: P[0:k] is applied, P[k] is torn, the ops after
// it are lost, and kept is k. A torn truncate is applied whole on one Chance, as one torn sector.
func TestTornPrefix(t *testing.T) {
	seen := map[string]bool{}
	for seed := uint64(1); seed <= 30; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashTorn, Metadata: simdisk.MetadataImmediate})
		f, err := k.v.Create("/f")
		must(t, err)
		appendAll(t, f, "1", "2", "3")
		k.crash()
		r := replay(seed, "disk/a")
		kept := int(r.Uint64N(3)) //nolint:gosec // below 3: replays crash's draw
		want, torn := "123"[:kept], 0
		if kernel.Chance(r, 500000) { // P[kept] is one byte: one chunk
			want, torn = "123"[:kept+1], 1
		}
		if got := content(t, k.v, "/f"); got != want {
			t.Fatalf("seed %d: ReadFile = %q, want %q", seed, got, want)
		}
		checkLast(t, k, applyRec("/f", "torn", 3, kept, torn, len(want)))
		seen[want] = true
	}
	if len(seen) != 4 {
		t.Fatalf("results seen: %v, want all four prefixes", seen)
	}

	seen = map[string]bool{}
	for seed := uint64(1); seed <= 20; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashTorn, Metadata: simdisk.MetadataImmediate})
		f, err := k.v.Create("/f")
		must(t, err)
		write(t, f, "abcdef", 0)
		must(t, f.Sync())
		must(t, f.Truncate(2))
		k.crash()
		r := replay(seed, "disk/a")
		r.Uint64N(1) //nolint:staticcheck // k = 0: replays the draw to keep the stream aligned
		want, torn := "abcdef", 0
		if kernel.Chance(r, 500000) {
			want, torn = "ab", 1
		}
		if got := content(t, k.v, "/f"); got != want {
			t.Fatalf("seed %d: ReadFile after a torn truncate = %q, want %q", seed, got, want)
		}
		checkLast(t, k, applyRec("/f", "torn", 1, 0, torn, len(want)))
		seen[want] = true
	}
	if len(seen) != 2 {
		t.Fatalf("torn truncate results seen: %v, want both", seen)
	}
}

// AT-DSK-09, §6.2: each file with pending ops gets its own model draw, followed by that model's
// draws, and its content follows the model its record names. A second file /g shows both.
func TestCrashAny(t *testing.T) {
	seen := map[string]bool{}
	differ := false
	for seed := uint64(1); seed <= 200; seed++ {
		k := newDisk(seed, simdisk.Config{Metadata: simdisk.MetadataImmediate})
		for _, p := range []string{"/f", "/g"} {
			f, err := k.v.Create(p)
			must(t, err)
			appendAll(t, f, "x")
		}
		k.crash()
		r := replay(seed, "disk/a")
		modelF, recF, wantF := replayAny(r, "/f", "x")
		modelG, recG, wantG := replayAny(r, "/g", "x")
		checkLast(t, k, recF, recG)
		if got := content(t, k.v, "/f"); got != wantF {
			t.Fatalf("seed %d: ReadFile(/f) = %q, want %q (%s)", seed, got, wantF, modelF)
		}
		if got := content(t, k.v, "/g"); got != wantG {
			t.Fatalf("seed %d: ReadFile(/g) = %q, want %q (%s)", seed, got, wantG, modelG)
		}
		seen[modelF] = true
		differ = differ || modelF != modelG
	}
	if len(seen) != 4 {
		t.Fatalf("models seen: %v", seen)
	}
	if !differ {
		t.Fatalf("no seed drew different models for /f and /g")
	}
}

// DSK-026, §6.2, §8: the data step visits the files with pending data ops depth-first in pre-order
// from the root, children in ascending byte order, names each by its full path and draws in the
// same order. A clean file gets no record and no draws.
func TestCrashTraversalOrder(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashKeepPrefix, Metadata: simdisk.MetadataImmediate})
		must(t, k.v.Mkdir("/d"))
		c, err := k.v.Create("/c")
		must(t, err)
		write(t, c, "c", 0)
		must(t, c.Sync()) // clean
		for _, p := range []string{"/e", "/d/b", "/d/a"} {
			f, err := k.v.Create(p)
			must(t, err)
			appendAll(t, f, "1", "2")
		}
		k.crash()
		r := replay(seed, "disk/a")
		paths := []string{"/d/a", "/d/b", "/e"}
		kept, want := make([]int, len(paths)), make([]string, len(paths))
		for i, p := range paths {
			kept[i] = int(r.Uint64N(3)) //nolint:gosec // below 3: replays crash's draw
			want[i] = applyRec(p, "keep_prefix", 2, kept[i], 0, kept[i])
		}
		if got := k.crashApply(); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("seed %d: disk.crash_apply records\n%s\nwant\n%s", seed, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		for i, p := range paths {
			if got := content(t, k.v, p); got != "12"[:kept[i]] {
				t.Fatalf("seed %d: ReadFile(%s) = %q, want %q", seed, p, got, "12"[:kept[i]])
			}
		}
		if got := content(t, k.v, "/c"); got != "c" {
			t.Fatalf("seed %d: ReadFile(/c) = %q", seed, got)
		}
	}
}

// AT-DSK-10
func TestCreateWithoutSyncDir(t *testing.T) {
	gone, kept := 0, 0
	for seed := uint64(1); seed <= 64; seed++ {
		for _, syncDir := range []bool{false, true} {
			k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashLoseUnsynced})
			f, err := k.v.Create("/f")
			must(t, err)
			if _, err := f.Append([]byte("x")); err != nil {
				t.Fatal(err)
			}
			must(t, f.Sync())
			if syncDir {
				must(t, k.v.SyncDir("/"))
			}
			k.crash()
			k.restart()
			st, err := k.v.Stat("/f")
			lost := !syncDir && replay(seed, "disk/a").Uint64N(2) == 0
			switch {
			case lost:
				checkPathErr(t, err, "stat", "/f", simdisk.ErrNotExist)
				gone++
			case err != nil || st != (simdisk.Info{Name: "f", Size: 1}):
				t.Fatalf("seed %d syncDir %v: Stat = %+v, %v", seed, syncDir, st, err)
			case !syncDir:
				kept++
			}
			if syncDir && attr(k.recordsOf("disk.crash_meta")[0], "pending") != "0" {
				t.Fatalf("seed %d: crash_meta pending after SyncDir", seed)
			}
		}
	}
	if gone == 0 || kept == 0 {
		t.Fatalf("gone=%d kept=%d: both outcomes must occur", gone, kept)
	}
}

// AT-DSK-11
func TestSyncDirClosureCrash(t *testing.T) {
	seen := map[uint64]bool{}
	for seed := uint64(1); seed <= 60; seed++ {
		k := syncDirScenario(t, seed)
		k.crash()
		k.restart()
		kk := replay(seed, "disk/a").Uint64N(3)
		seen[kk] = true
		root, err := k.v.ReadDir("/")
		must(t, err)
		switch kk {
		case 0:
			if len(root) != 0 {
				t.Fatalf("seed %d k=0: ReadDir(/) = %v", seed, names(root))
			}
		case 1:
			a, err := k.v.ReadDir("/a")
			must(t, err)
			if strings.Join(names(root), ",") != "a" || len(a) != 0 {
				t.Fatalf("seed %d k=1: / = %v, /a = %v", seed, names(root), names(a))
			}
		case 2:
			b, err := k.v.ReadDir("/b")
			must(t, err)
			if strings.Join(names(root), ",") != "a,b" || strings.Join(names(b), ",") != "y,z" {
				t.Fatalf("seed %d k=2: / = %v, /b = %v", seed, names(root), names(b))
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("outcomes seen: %v", seen)
	}
}

// DSK-025 step 1, §6.2, §8: under MetadataStrict the crash keeps the oldest Uint64N(P+1) namespace
// ops and applies them in order: keeping 0, 1, 2 or 3 of create /f, remove /f and mkdir /d leaves
// the root empty, with f, empty, or with d. disk.crash_meta reports the model, P and the kept
// count. With an empty log the crash makes no metadata draw.
func TestStrictMetadataCrash(t *testing.T) {
	seen := map[int]bool{}
	for seed := uint64(1); seed <= 20; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashLoseUnsynced})
		_, err := k.v.Create("/f")
		must(t, err)
		must(t, k.v.Remove("/f"))
		must(t, k.v.Mkdir("/d"))
		k.crash()
		kept := int(replay(seed, "disk/a").Uint64N(4)) //nolint:gosec // below 4: replays crash's draw
		seen[kept] = true
		want := fmt.Sprintf("disk.crash_meta{model=strict, pending=3, kept=%d}", kept)
		meta := k.recordsOf("disk.crash_meta")
		if len(meta) != 1 || summary(meta[0]) != want || meta[0].Text != fmt.Sprintf("crash metadata strict: kept %d of 3", kept) {
			t.Fatalf("seed %d: disk.crash_meta records %v, want %s", seed, meta, want)
		}
		root, err := k.v.ReadDir("/")
		must(t, err)
		if got := strings.Join(names(root), ","); got != []string{"", "f", "", "d"}[kept] {
			t.Fatalf("seed %d kept %d: ReadDir(/) = %q", seed, kept, got)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("kept counts seen: %v, want 0 to 3", seen)
	}

	// P = 0: the KeepPrefix draw for /f is the stream's first
	for seed := uint64(1); seed <= 20; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashKeepPrefix})
		f, err := k.v.Create("/f")
		must(t, err)
		must(t, k.v.SyncDir("/"))
		appendAll(t, f, "1", "2", "3")
		k.crash()
		kept := int(replay(seed, "disk/a").Uint64N(4)) //nolint:gosec // below 4: replays crash's draw
		checkLast(t, k, "disk.crash_meta{model=strict, pending=0, kept=0}", applyRec("/f", "keep_prefix", 3, kept, 0, kept))
		if got := content(t, k.v, "/f"); got != "123"[:kept] {
			t.Fatalf("seed %d: ReadFile = %q, want %q", seed, got, "123"[:kept])
		}
	}
}

// DSK-025 step 2, §6.2: the data step walks the durable namespace. A file whose create does not
// survive is unreachable, so its pending data ops are dropped without draws or records; a file
// whose create survives is drawn for after the metadata draw.
func TestLostCreateDropsData(t *testing.T) {
	lost, survived := 0, 0
	for seed := uint64(1); seed <= 20; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashKeepPrefix})
		f, err := k.v.Create("/f")
		must(t, err)
		appendAll(t, f, "x")
		k.crash()
		r := replay(seed, "disk/a")
		if r.Uint64N(2) == 0 { // the create is lost
			_, err := k.v.Stat("/f")
			checkPathErr(t, err, "stat", "/f", simdisk.ErrNotExist)
			if ca := k.crashApply(); len(ca) != 0 {
				t.Fatalf("seed %d: disk.crash_apply for an unreachable file: %v", seed, ca)
			}
			if k.s.Rand("disk/a").Uint64() != r.Uint64() {
				t.Fatalf("seed %d: the crash drew for an unreachable file", seed)
			}
			lost++
			continue
		}
		kept := int(r.Uint64N(2)) //nolint:gosec // below 2: replays crash's draw
		checkLast(t, k, applyRec("/f", "keep_prefix", 1, kept, 0, kept))
		if got := content(t, k.v, "/f"); got != "x"[:kept] {
			t.Fatalf("seed %d: ReadFile = %q, want %q", seed, got, "x"[:kept])
		}
		survived++
	}
	if lost == 0 || survived == 0 {
		t.Fatalf("lost=%d survived=%d: both outcomes must occur", lost, survived)
	}
}

// AT-DSK-12, AT-DSK-13 (after the crash)
func TestFailedSyncAfterCrash(t *testing.T) {
	for _, c := range []struct {
		model simdisk.FailedSyncModel
		want  string
	}{
		{simdisk.FailedSyncDropDirty, "AAAA\x00\x00\x00\x00CCCC"},
		{simdisk.FailedSyncKeepDirty, "AAAABBBBCCCC"},
	} {
		k := failedSync(t, c.model)
		k.crash()
		k.restart()
		if got := content(t, k.v, "/f"); got != c.want {
			t.Fatalf("%v: ReadFile after restart = %q, want %q", c.model, got, c.want)
		}
	}
}

// AT-DSK-14 (after the crash)
func TestStaleHandles(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "abc", 0)
	must(t, f.Sync())
	k.crash()
	p := make([]byte, 1)
	_, err = f.ReadAt(p, 0)
	checkPathErr(t, err, "readat", "/f", simdisk.ErrStale)
	_, err = f.WriteAt(p, 0)
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrStale)
	_, err = f.Append(p)
	checkPathErr(t, err, "append", "/f", simdisk.ErrStale)
	checkPathErr(t, f.Truncate(0), "truncate", "/f", simdisk.ErrStale)
	checkPathErr(t, f.Sync(), "sync", "/f", simdisk.ErrStale)
	if f.Size() != -1 {
		t.Fatalf("stale Size() = %d", f.Size())
	}
	k.restart()
	g, err := k.v.Open("/f")
	must(t, err)
	_, err = f.ReadAt(p, 0)
	checkPathErr(t, err, "readat", "/f", simdisk.ErrStale) // still stale after restart
	checkPathErr(t, f.Close(), "close", "/f", simdisk.ErrStale)
	checkPathErr(t, f.Close(), "close", "/f", simdisk.ErrClosed) // Close marked it closed
	if n, err := g.ReadAt(p, 0); n != 1 || err != nil || p[0] != 'a' {
		t.Fatalf("new handle ReadAt = %d, %v, %q", n, err, p)
	}
	if len(k.recordsOf("disk.stale")) != 7 {
		t.Fatalf("%d disk.stale records, want 7", len(k.recordsOf("disk.stale")))
	}
}

// AT-DSK-20 (crash part), DSK-031
func TestUnlinkedFileCrash(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	if _, err := f.Append([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	must(t, k.v.Remove("/f"))
	k.v.SetCapacity(1)
	if n, err := f.Append([]byte("5678")); n != 4 || err != nil {
		t.Fatalf("Append to an unlinked file = %d, %v", n, err)
	}
	must(t, f.Sync())
	k.crash()
	k.restart()
	_, err = k.v.Stat("/f")
	checkPathErr(t, err, "stat", "/f", simdisk.ErrNotExist)
	if len(k.recordsOf("disk.crash_apply")) != 0 {
		t.Fatalf("crash_apply for an unlinked file: %v", k.crashApply())
	}

	// strict: a removal that does not survive the crash brings the file back, with its pending
	// data op (the data step walks the durable namespace) and linked again
	back, gone := 0, 0
	for seed := uint64(1); seed <= 20; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashLoseUnsynced})
		f, err := k.v.Create("/f")
		must(t, err)
		write(t, f, "v1", 0)
		must(t, f.Sync())
		must(t, k.v.SyncDir("/"))
		must(t, k.v.Remove("/f"))
		appendAll(t, f, "x")
		k.crash()
		k.restart()
		if replay(seed, "disk/a").Uint64N(2) == 0 {
			if got := content(t, k.v, "/f"); got != "v1" || k.v.Usage() != 2 {
				t.Fatalf("seed %d: removal lost but /f = %q usage %d", seed, got, k.v.Usage())
			}
			if ca := k.crashApply(); len(ca) != 1 || ca[0] != applyRec("/f", "lose_unsynced", 1, 0, 0, 2) {
				t.Fatalf("seed %d: removal lost, disk.crash_apply records %v", seed, ca)
			}
			g, err := k.v.Open("/f")
			must(t, err)
			appendAll(t, g, "zz")
			if k.v.Usage() != 4 {
				t.Fatalf("seed %d: Usage after an Append to the file brought back = %d, want 4", seed, k.v.Usage())
			}
			back++
		} else {
			if _, err := k.v.Stat("/f"); err == nil || k.v.Usage() != 0 {
				t.Fatalf("seed %d: removal kept but /f exists or usage %d", seed, k.v.Usage())
			}
			if ca := k.crashApply(); len(ca) != 0 {
				t.Fatalf("seed %d: removal kept, disk.crash_apply records %v", seed, ca)
			}
			gone++
		}
	}
	if back == 0 || gone == 0 {
		t.Fatalf("back=%d gone=%d: both outcomes must occur", back, gone)
	}
}

// AT-DSK-21
func TestTruncateUnderCrash(t *testing.T) {
	seen := map[string]bool{}
	for seed := uint64(1); seed <= 30; seed++ {
		k := newDisk(seed, simdisk.Config{Crash: simdisk.CrashKeepPrefix, Metadata: simdisk.MetadataImmediate})
		f, err := k.v.Create("/f")
		must(t, err)
		write(t, f, "ABCDEFGH", 0)
		must(t, f.Sync())
		must(t, f.Truncate(2))
		if _, err := f.Append([]byte("xy")); err != nil {
			t.Fatal(err)
		}
		k.crash()
		want := []string{"ABCDEFGH", "AB", "ABxy"}[replay(seed, "disk/a").Uint64N(3)]
		if got := content(t, k.v, "/f"); got != want {
			t.Fatalf("seed %d: ReadFile = %q, want %q", seed, got, want)
		}
		seen[want] = true
	}
	if len(seen) != 3 {
		t.Fatalf("outcomes seen: %v", seen)
	}

	k := newDisk(1, simdisk.Config{Crash: simdisk.CrashLoseUnsynced, Metadata: simdisk.MetadataImmediate})
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "abc", 0)
	must(t, f.Sync())
	g, err := k.v.Create("/f")
	must(t, err)
	if g.Size() != 0 || attr(k.recordsOf("disk.create")[1], "truncated") != "true" {
		t.Fatalf("re-Create: size %d, record %v", g.Size(), summary(k.recordsOf("disk.create")[1]))
	}
	k.crash()
	if got := content(t, k.v, "/f"); got != "abc" {
		t.Fatalf("after crash ReadFile = %q, want abc", got)
	}
}

// AT-DSK-23
func TestNoVolume(t *testing.T) {
	k := newDisk(1, simdisk.Config{})
	b := k.s.AddNode("b", func(*kernel.Node) {})
	c := k.s.AddNode("c", func(*kernel.Node) {})
	k.s.RunUntil(0)
	before := len(k.records())
	b.Crash() // b (ID 2) is just past the end of the volume table
	c.Crash() // c (ID 3) is further past it
	k.d.Volume(c)
	b.Restart()
	k.s.RunFor(0)
	b.Crash() // b has a slot, below c's, that holds no volume
	if err := k.s.Err(); err != nil {
		t.Fatalf("crash of a node without a volume: %v", err)
	}
	if len(k.records()) != before {
		t.Fatalf("crash of a node without a volume emitted disk records")
	}
	for _, label := range []string{"disk/b", "disk/c"} {
		if k.s.Rand(label).Uint64() != replay(1, label).Uint64() {
			t.Fatalf("crash drew from %s", label)
		}
	}
}

// DSK-007 (4), DSK-022, DSK-029, DSK-030: after a crash every file is clean, usage is recomputed
// (clean files included), and forced sync failures and capacity persist.
func TestCrashResetsToDurable(t *testing.T) {
	k := newDisk(3, simdisk.Config{})
	c, err := k.v.Create("/c")
	must(t, err)
	write(t, c, "ccc", 0)
	must(t, c.Sync())
	must(t, k.v.SyncDir("/")) // /c is durable and clean at the crash
	must(t, k.v.MkdirAll("/d/e"))
	f, err := k.v.Create("/d/e/f")
	must(t, err)
	write(t, f, "0123456789", 0)
	must(t, f.Sync())
	write(t, f, "zz", 20)
	g, err := k.v.Create("/g")
	must(t, err)
	write(t, g, "gg", 0)
	k.v.FailSyncs(1)
	k.v.SetCapacity(50)
	k.crash()
	var total int64
	for _, p := range []string{"/c", "/d/e/f", "/g"} {
		vis, err1 := k.v.ReadFile(p)
		dur, err2 := k.v.ReadDurable(p)
		if (err1 == nil) != (err2 == nil) || !bytes.Equal(vis, dur) {
			t.Fatalf("%s after crash: visible %q (%v), durable %q (%v)", p, vis, err1, dur, err2)
		}
		total += int64(len(vis))
	}
	if got := content(t, k.v, "/c"); got != "ccc" {
		t.Fatalf("ReadFile(/c) after crash = %q", got)
	}
	if k.v.Usage() != total {
		t.Fatalf("Usage = %d, want %d", k.v.Usage(), total)
	}
	k.restart()
	if k.v.Capacity() != 50 {
		t.Fatalf("capacity did not persist")
	}
	if _, err := k.v.Create("/h"); err != nil {
		t.Fatal(err)
	}
	checkPathErr(t, k.v.SyncDir("/"), "syncdir", "/", simdisk.ErrIO)
}

// DSK-007 (4), DSK-025 step 3: a crash empties the namespace log and leaves every reachable file
// clean, so a second crash keeps and applies nothing. The visible state is a copy of the durable
// state: a later Create or WriteAt changes only the visible state.
func TestCrashLeavesCleanState(t *testing.T) {
	k := newDisk(1, simdisk.Config{Crash: simdisk.CrashKeepPrefix})
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "abc", 0)
	must(t, f.Sync())
	must(t, k.v.SyncDir("/"))
	appendAll(t, f, "x")     // a pending data op
	must(t, k.v.Mkdir("/d")) // a pending namespace op
	k.crash()
	k.restart()
	n := len(k.records())
	k.crash()
	if recs := k.records()[n:]; len(recs) != 1 || summary(recs[0]) != "disk.crash_meta{model=strict, pending=0, kept=0}" {
		t.Fatalf("second crash records %v, want only disk.crash_meta{model=strict, pending=0, kept=0}", recs)
	}
	k.restart()
	_, err = k.v.Create("/x")
	must(t, err)
	_, err = k.v.ReadDurable("/x")
	checkPathErr(t, err, "readdurable", "/x", simdisk.ErrNotExist)
	g, err := k.v.Open("/f")
	must(t, err)
	before := durable(t, k.v, "/f")
	write(t, g, "X", 0)
	if got := durable(t, k.v, "/f"); got != before {
		t.Fatalf("ReadDurable(/f) after WriteAt = %q, want %q", got, before)
	}
}
