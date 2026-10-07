package simdisk_test

import (
	"bytes"
	"io"
	"math"
	"strconv"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// write writes s at off and fails the test on error.
func write(t *testing.T, f *simdisk.File, s string, off int64) {
	t.Helper()
	if n, err := f.WriteAt([]byte(s), off); err != nil || n != len(s) {
		t.Fatalf("WriteAt(%q, %d) = %d, %v", s, off, n, err)
	}
}

// content returns the visible content of path and fails the test on error.
func content(t *testing.T, v *simdisk.Volume, path string) string {
	t.Helper()
	b, err := v.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(b)
}

// checkLast checks that the last disk records have the summaries want, in order.
func checkLast(t *testing.T, k *disk, want ...string) {
	t.Helper()
	recs := k.records()
	if len(recs) < len(want) {
		t.Fatalf("%d records, want at least %d", len(recs), len(want))
	}
	last := recs[len(recs)-len(want):]
	for i, w := range want {
		if s := summary(last[i]); s != w {
			t.Errorf("record %d of the last %d = %s, want %s", i, len(want), s, w)
		}
	}
}

// AT-DSK-02; DSK-034: ReadFile returns a new slice.
func TestBasicIO(t *testing.T) {
	k := newDisk(1, simdisk.Config{Metadata: simdisk.MetadataImmediate})
	f, err := k.v.Create("/f")
	must(t, err)
	if n, err := f.WriteAt([]byte("hello"), 0); n != 5 || err != nil {
		t.Fatalf("WriteAt = %d, %v", n, err)
	}
	if n, err := f.Append([]byte(" world")); n != 6 || err != nil {
		t.Fatalf("Append = %d, %v", n, err)
	}
	if f.Size() != 11 {
		t.Fatalf("Size() = %d", f.Size())
	}
	p := make([]byte, 11)
	if n, err := f.ReadAt(p, 0); n != 11 || err != nil || string(p) != "hello world" {
		t.Fatalf("ReadAt = %d, %v, %q", n, err, p)
	}
	b, err := k.v.ReadFile("/f")
	if err != nil || string(b) != "hello world" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	b[0] = 'J'
	if got := content(t, k.v, "/f"); got != "hello world" {
		t.Fatalf("changing ReadFile's result changed the file to %q", got)
	}
	if st, err := k.v.Stat("/f"); err != nil || st != (simdisk.Info{Name: "f", Size: 11}) {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
	want := []string{
		"disk.create{path=/f, truncated=false}",
		"disk.write{path=/f, op=writeat, off=0, len=5}",
		"disk.write{path=/f, op=append, off=5, len=6}",
	}
	texts := []string{"create /f", "writeat /f off=0 len=5", "append /f off=5 len=6"}
	recs := k.records()
	if len(recs) != 3 {
		t.Fatalf("%d records, want 3", len(recs))
	}
	for i, r := range recs {
		if summary(r) != want[i] || r.Text != texts[i] || r.Node != 1 {
			t.Errorf("record %d = %s %q node %d", i, summary(r), r.Text, r.Node)
		}
	}
}

// AT-DSK-03; DSK-017: the copy starts at off, and off is checked before len(p).
func TestReadAtSemantics(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "abc", 0)
	p := make([]byte, 5)
	if n, err := f.ReadAt(p[:5], 0); n != 3 || err != io.EOF {
		t.Fatalf("ReadAt(p[:5], 0) = %d, %v; want 3, io.EOF", n, err)
	}
	if n, err := f.ReadAt(p[:2], 3); n != 0 || err != io.EOF {
		t.Fatalf("ReadAt(p[:2], 3) = %d, %v; want 0, io.EOF", n, err)
	}
	if n, err := f.ReadAt(p[:2], 10); n != 0 || err != io.EOF {
		t.Fatalf("ReadAt(p[:2], 10) = %d, %v; want 0, io.EOF", n, err)
	}
	if n, err := f.ReadAt(p[:0], 100); n != 0 || err != nil {
		t.Fatalf("ReadAt(p[:0], 100) = %d, %v; want 0, nil", n, err)
	}
	if n, err := f.ReadAt(p[:4], 1); n != 2 || err != io.EOF || string(p[:2]) != "bc" {
		t.Fatalf("ReadAt(p[:4], 1) = %d, %v, %q; want 2, io.EOF, bc", n, err, p[:n])
	}
	if n, err := f.ReadAt(p[:1], 2); n != 1 || err != nil || p[0] != 'c' {
		t.Fatalf("ReadAt(p[:1], 2) = %d, %v, %q; want 1, nil, c", n, err, p[:n])
	}
	n, err := f.ReadAt(p[:1], -1)
	if n != 0 {
		t.Fatalf("ReadAt(p[:1], -1) n = %d", n)
	}
	checkPathErr(t, err, "readat", "/f", simdisk.ErrInvalid)
	_, err = f.ReadAt(p[:0], -1)
	checkPathErr(t, err, "readat", "/f", simdisk.ErrInvalid)
}

// AT-DSK-04; DSK-018: off is checked first, then len(p), then the size limit.
func TestSparseWritesAndTruncate(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "x", 10)
	if got := content(t, k.v, "/f"); got != "\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00x" {
		t.Fatalf("sparse write: %q", got)
	}
	g, err := k.v.Create("/g")
	must(t, err)
	write(t, g, "abcdefgh", 0)
	must(t, g.Truncate(3))
	must(t, g.Truncate(8))
	if got := content(t, k.v, "/g"); got != "abc\x00\x00\x00\x00\x00" {
		t.Fatalf("shrink then grow: %q", got)
	}
	_, err = f.WriteAt([]byte{1}, simdisk.MaxFileSize)
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrTooLarge)
	_, err = f.WriteAt([]byte{1}, math.MaxInt64) // off + len(p) overflows
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrTooLarge)
	checkPathErr(t, f.Truncate(simdisk.MaxFileSize+1), "truncate", "/f", simdisk.ErrTooLarge)
	checkPathErr(t, f.Truncate(-1), "truncate", "/f", simdisk.ErrInvalid)
	if n, err := f.WriteAt(nil, 3); n != 0 || err != nil {
		t.Fatalf("empty WriteAt = %d, %v", n, err)
	}
	_, err = f.WriteAt(nil, -1)
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrInvalid)
	if n, err := f.WriteAt(nil, simdisk.MaxFileSize+1); n != 0 || err != nil {
		t.Fatalf("empty WriteAt past MaxFileSize = %d, %v", n, err)
	}
	must(t, g.Truncate(8)) // same size still records
	tr := k.recordsOf("disk.truncate")
	if len(tr) != 3 || summary(tr[0]) != "disk.truncate{path=/g, old_size=8, size=3}" || tr[0].Text != "truncate /g 8 -> 3" ||
		summary(tr[2]) != "disk.truncate{path=/g, old_size=8, size=8}" {
		t.Fatalf("disk.truncate records %v", tr)
	}
}

// DSK-018, DSK-020: a write that ends at MaxFileSize and Truncate(MaxFileSize) are within the
// size limit, so on a volume without space they fail with ErrNoSpace, not ErrTooLarge. One byte
// further is ErrTooLarge even then: the size limit is checked before the capacity.
func TestSizeLimitIsInclusive(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	k.v.SetCapacity(1)
	n, err := f.WriteAt([]byte{1}, simdisk.MaxFileSize-1)
	if n != 0 {
		t.Fatalf("WriteAt at MaxFileSize-1 wrote %d bytes", n)
	}
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrNoSpace)
	checkPathErr(t, f.Truncate(simdisk.MaxFileSize), "truncate", "/f", simdisk.ErrNoSpace)
	limit := strconv.FormatInt(simdisk.MaxFileSize, 10)
	checkLast(t, k,
		"disk.capacity{bytes=1, used=0}",
		"disk.nospace{path=/f, op=writeat, need="+limit+", avail=1}",
		"disk.nospace{path=/f, op=truncate, need="+limit+", avail=1}")
	_, err = f.WriteAt([]byte{1}, simdisk.MaxFileSize)
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrTooLarge)
	checkPathErr(t, f.Truncate(simdisk.MaxFileSize+1), "truncate", "/f", simdisk.ErrTooLarge)
	checkLast(t, k, "disk.nospace{path=/f, op=truncate, need="+limit+", avail=1}")
}

// AT-DSK-15, DSK-029, DSK-030
func TestCapacity(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	if v.Capacity() != 0 || v.Usage() != 0 {
		t.Fatalf("new volume: Capacity %d Usage %d", v.Capacity(), v.Usage())
	}
	v.SetCapacity(10)
	c := k.recordsOf("disk.capacity")
	if len(c) != 1 || summary(c[0]) != "disk.capacity{bytes=10, used=0}" || c[0].Text != "capacity 10 used=0" {
		t.Fatalf("disk.capacity records %v", c)
	}
	f, err := v.Create("/f")
	must(t, err)
	if n, err := f.WriteAt(bytes.Repeat([]byte{1}, 8), 0); n != 8 || err != nil || v.Usage() != 8 {
		t.Fatalf("WriteAt 8 = %d, %v; Usage %d", n, err, v.Usage())
	}
	n, err := f.WriteAt(bytes.Repeat([]byte{2}, 5), 8)
	if n != 2 || f.Size() != 10 {
		t.Fatalf("WriteAt 5 at 8 = %d, size %d; want 2, 10", n, f.Size())
	}
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrNoSpace)
	recs := k.records()
	if summary(recs[len(recs)-2]) != "disk.write{path=/f, op=writeat, off=8, len=2}" ||
		summary(recs[len(recs)-1]) != "disk.nospace{path=/f, op=writeat, need=5, avail=2}" ||
		recs[len(recs)-1].Text != "writeat /f: no space (need 5, avail 2)" {
		t.Fatalf("records %s, %s", summary(recs[len(recs)-2]), summary(recs[len(recs)-1]))
	}
	if n, err := f.WriteAt([]byte{3, 3, 3}, 0); n != 3 || err != nil {
		t.Fatalf("overwrite = %d, %v", n, err)
	}
	checkPathErr(t, f.Truncate(11), "truncate", "/f", simdisk.ErrNoSpace)
	if f.Size() != 10 {
		t.Fatalf("Truncate(11) changed the size to %d", f.Size())
	}
	checkLast(t, k, "disk.nospace{path=/f, op=truncate, need=1, avail=0}")
	if r := k.records(); r[len(r)-1].Text != "truncate /f: no space (need 1, avail 0)" {
		t.Fatalf("truncate disk.nospace text %q", r[len(r)-1].Text)
	}
	g, err := v.Create("/g")
	must(t, err)
	n, err = g.Append([]byte{1})
	if n != 0 {
		t.Fatalf("Append on a full volume wrote %d bytes", n)
	}
	checkPathErr(t, err, "append", "/g", simdisk.ErrNoSpace)
	checkLast(t, k, "disk.create{path=/g, truncated=false}", "disk.nospace{path=/g, op=append, need=1, avail=0}")
	must(t, v.Remove("/f"))
	if v.Usage() != 0 {
		t.Fatalf("Usage after Remove = %d", v.Usage())
	}
	if n, err := g.Append([]byte{1}); n != 1 || err != nil {
		t.Fatalf("Append after Remove = %d, %v", n, err)
	}

	k = newDisk(1, imm) // gap case
	k.v.SetCapacity(5)
	f, err = k.v.Create("/f")
	must(t, err)
	n, err = f.WriteAt([]byte{1, 2, 3}, 4)
	if n != 1 || f.Size() != 5 {
		t.Fatalf("gap WriteAt = %d, size %d; want 1, 5", n, f.Size())
	}
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrNoSpace)
	checkLast(t, k, "disk.write{path=/f, op=writeat, off=4, len=1}", "disk.nospace{path=/f, op=writeat, need=7, avail=5}")
	k.v.SetCapacity(6) // a write that starts past the space left writes nothing
	n, err = f.WriteAt([]byte{1, 2}, 10)
	if n != 0 || f.Size() != 5 {
		t.Fatalf("WriteAt past the space = %d, size %d; want 0, 5", n, f.Size())
	}
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrNoSpace)
	checkLast(t, k, "disk.capacity{bytes=6, used=5}", "disk.nospace{path=/f, op=writeat, need=7, avail=1}")
	mustPanic(t, "simdisk: SetCapacity on a (id 1): negative capacity -1", func() { k.v.SetCapacity(-1) })

	s := kernel.New(kernel.Config{Seed: 1})
	d := simdisk.New(s, simdisk.Config{Capacity: 77})
	if d.Volume(s.AddNode("z", func(*kernel.Node) {})).Capacity() != 77 {
		t.Fatalf("Config.Capacity not applied to new volumes")
	}
}

// DSK-018, DSK-029: a capacity below the usage keeps all data. Overwrites inside the size still
// succeed; only growth fails, with avail 0.
func TestCapacityBelowUsage(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	f, err := v.Create("/f")
	must(t, err)
	write(t, f, "abcd", 0)
	v.SetCapacity(1)
	if c := k.recordsOf("disk.capacity"); len(c) != 1 || c[0].Text != "capacity 1 used=4" {
		t.Fatalf("disk.capacity records %v", c)
	}
	write(t, f, "XY", 1)
	n, err := f.WriteAt([]byte("QRS"), 3)
	if n != 1 {
		t.Fatalf("growing WriteAt wrote %d bytes, want 1", n)
	}
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrNoSpace)
	checkPathErr(t, f.Truncate(5), "truncate", "/f", simdisk.ErrNoSpace)
	if got := content(t, v, "/f"); got != "aXYQ" || v.Usage() != 4 {
		t.Fatalf("content %q, Usage %d; want aXYQ, 4", got, v.Usage())
	}
	checkLast(t, k,
		"disk.write{path=/f, op=writeat, off=0, len=4}",
		"disk.capacity{bytes=1, used=4}",
		"disk.write{path=/f, op=writeat, off=1, len=2}",
		"disk.write{path=/f, op=writeat, off=3, len=1}",
		"disk.nospace{path=/f, op=writeat, need=2, avail=0}",
		"disk.nospace{path=/f, op=truncate, need=1, avail=0}")
}

// DSK-020, DSK-029: only growth is limited, a truncate that exactly fills the capacity fits, and
// SetCapacity(0) clears the limit.
func TestTruncateCapacityEdges(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	f, err := v.Create("/f")
	must(t, err)
	write(t, f, "abcd", 0)
	v.SetCapacity(1)
	must(t, f.Truncate(4)) // same size: no growth
	must(t, f.Truncate(2)) // shrinking is allowed above the capacity and frees space
	if got := content(t, v, "/f"); got != "ab" || v.Usage() != 2 {
		t.Fatalf("content %q, Usage %d; want ab, 2", got, v.Usage())
	}
	v.SetCapacity(3)
	must(t, f.Truncate(3)) // exactly fills the capacity
	v.SetCapacity(0)
	must(t, f.Truncate(100))
	if v.Capacity() != 0 || v.Usage() != 100 {
		t.Fatalf("after SetCapacity(0): Capacity %d, Usage %d; want 0, 100", v.Capacity(), v.Usage())
	}
	checkLast(t, k,
		"disk.truncate{path=/f, old_size=2, size=3}",
		"disk.capacity{bytes=0, used=3}",
		"disk.truncate{path=/f, old_size=3, size=100}")
}

// DSK-030: Truncate, Create of an existing file and replacement by Rename free bytes at once.
func TestUsageAccounting(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "0123456789", 0)
	g, err := k.v.Create("/g")
	must(t, err)
	write(t, g, "abc", 0)
	if k.v.Usage() != 13 {
		t.Fatalf("Usage = %d, want 13", k.v.Usage())
	}
	must(t, f.Truncate(4))
	if k.v.Usage() != 7 {
		t.Fatalf("after Truncate(4): Usage %d, want 7", k.v.Usage())
	}
	_, err = k.v.Create("/f")
	must(t, err)
	if k.v.Usage() != 3 || f.Size() != 0 {
		t.Fatalf("after re-Create: Usage %d size %d", k.v.Usage(), f.Size())
	}
	must(t, k.v.Rename("/f", "/g"))
	if k.v.Usage() != 0 {
		t.Fatalf("after Rename replacing /g: Usage %d", k.v.Usage())
	}
}

// AT-DSK-14 (data methods after Close), AT-DSK-20 (open file after Remove, before the crash),
// DSK-034 (ReadFile errors)
func TestHandlesAndUnlinkedFiles(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	must(t, f.Close())
	_, err = f.ReadAt(make([]byte, 1), 0)
	checkPathErr(t, err, "readat", "/f", simdisk.ErrClosed)
	_, err = f.WriteAt([]byte{1}, -1)
	checkPathErr(t, err, "writeat", "/f", simdisk.ErrClosed)
	_, err = f.Append([]byte{1})
	checkPathErr(t, err, "append", "/f", simdisk.ErrClosed)
	checkPathErr(t, f.Truncate(-1), "truncate", "/f", simdisk.ErrClosed)

	g, err := k.v.Create("/g")
	must(t, err)
	if _, err := g.Append([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	must(t, k.v.Remove("/g"))
	if k.v.Usage() != 0 {
		t.Fatalf("Usage after Remove = %d", k.v.Usage())
	}
	_, err = k.v.ReadFile("/g")
	checkPathErr(t, err, "readfile", "/g", simdisk.ErrNotExist)
	k.v.SetCapacity(1)
	if n, err := g.Append([]byte("5678")); n != 4 || err != nil {
		t.Fatalf("Append to an unlinked file = %d, %v", n, err)
	}
	p := make([]byte, 8)
	if n, err := g.ReadAt(p, 0); n != 8 || err != nil || string(p) != "12345678" {
		t.Fatalf("unlinked file reads %q, %v", p[:n], err)
	}
	must(t, g.Truncate(100))
	if g.Size() != 100 || k.v.Usage() != 0 {
		t.Fatalf("Truncate(100) of an unlinked file: size %d, Usage %d; want 100, 0", g.Size(), k.v.Usage())
	}
	_, err = k.v.ReadFile("/")
	checkPathErr(t, err, "readfile", "/", simdisk.ErrIsDir)
	_, err = k.v.ReadFile("rel")
	checkPathErr(t, err, "readfile", "rel", simdisk.ErrInvalid)
	_, err = k.v.ReadFile("/f/x")
	checkPathErr(t, err, "readfile", "/f/x", simdisk.ErrNotDir)
	_, err = k.v.ReadFile("//nope/../missing")
	checkPathErr(t, err, "readfile", "/missing", simdisk.ErrNotExist)
}

// DSK-009, DSK-034: ReadFile resolves the path in the visible namespace, so under MetadataStrict
// it sees a file whose create is not durable yet, and not the old name of a renamed file.
func TestReadFileIsVisible(t *testing.T) {
	k := newDisk(1, simdisk.Config{})
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "x", 0)
	if got := content(t, k.v, "/f"); got != "x" {
		t.Fatalf("ReadFile before SyncDir = %q, want x", got)
	}
	must(t, k.v.Rename("/f", "/g"))
	_, err = k.v.ReadFile("/f")
	checkPathErr(t, err, "readfile", "/f", simdisk.ErrNotExist)
}

// DSK-035: File methods panic on a node that is not up. DSK-029, DSK-034: SetCapacity and
// ReadFile work in every node state.
func TestFileNodeState(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	k.a.Pause()
	mustPanic(t, "simdisk: WriteAt on a (id 1): node is paused", func() { _, _ = f.WriteAt([]byte("2"), 0) })
	mustPanic(t, "simdisk: ReadAt on a (id 1): node is paused", func() { _, _ = f.ReadAt(make([]byte, 1), 0) })
	mustPanic(t, "simdisk: Append on a (id 1): node is paused", func() { _, _ = f.Append([]byte("2")) })
	mustPanic(t, "simdisk: Truncate on a (id 1): node is paused", func() { _ = f.Truncate(0) })
	if f.Size() != 0 {
		t.Fatalf("Size on a paused node = %d", f.Size())
	}
	if got := content(t, k.v, "/f"); got != "" {
		t.Fatalf("ReadFile on a paused node = %q", got)
	}
	k.v.SetCapacity(5)
	if k.v.Capacity() != 5 {
		t.Fatalf("SetCapacity on a paused node: Capacity %d", k.v.Capacity())
	}
	k.crash()
	k.v.SetCapacity(6)
	if k.v.Capacity() != 6 {
		t.Fatalf("SetCapacity on a down node: Capacity %d", k.v.Capacity())
	}
	if got := content(t, k.v, "/f"); got != "" {
		t.Fatalf("ReadFile on a down node = %q", got)
	}
}

// DSK-013, DSK-030, DSK-031: a file replaced by Rename is unlinked: writes through an open
// handle count 0 and are never limited by capacity.
func TestReplacedFileIsUnlinked(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	write(t, f, "old", 0)
	g, err := k.v.Create("/g")
	must(t, err)
	write(t, g, "new!", 0)
	must(t, k.v.Rename("/g", "/f"))
	k.v.SetCapacity(k.v.Usage())
	if n, err := f.Append([]byte("zzzz")); n != 4 || err != nil || k.v.Usage() != 4 {
		t.Fatalf("Append to the replaced file = %d, %v; Usage %d; want 4, nil, 4", n, err, k.v.Usage())
	}
}
