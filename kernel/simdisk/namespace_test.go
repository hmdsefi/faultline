package simdisk_test

import (
	"errors"
	"io/fs"
	"slices"
	"testing"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// AT-DSK-01 (New, Config), AT-DSK-26, DSK-001, DSK-002, DSK-042
func TestDisksAndVolumes(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	if got := simdisk.New(s, simdisk.Config{}).Config().SectorSize; got != 512 {
		t.Fatalf("New(s, Config{}).Config().SectorSize = %d, want 512", got)
	}
	cfg := simdisk.Config{SectorSize: 4096, Metadata: simdisk.MetadataImmediate, Capacity: 7}
	if got := simdisk.New(kernel.New(kernel.Config{Seed: 1}), cfg).Config(); got != cfg {
		t.Fatalf("New(s, %+v).Config() = %+v", cfg, got)
	}
	mustPanic(t, "simdisk: New: invalid config: SectorSize -1 out of range [0, 1048576]",
		func() { simdisk.New(s, simdisk.Config{SectorSize: -1}) })
	mustPanic(t, "simdisk: New: nil *kernel.Sim", func() { simdisk.New(nil, simdisk.Config{}) })

	k := newDisk(1, simdisk.Config{})
	recs := len(k.s.Records())
	b := k.s.AddNode("b", func(*kernel.Node) {})
	vb := k.d.Volume(b) // before b's first boot
	if k.d.Volume(b) != vb || vb.Node() != b || k.d.Volume(k.a) != k.v {
		t.Fatalf("Volume does not return the same pointer per node")
	}
	if got := len(k.s.Records()); got != recs+1 { // only kernel.add_node of b
		t.Fatalf("creating volumes emitted records")
	}
	infos, err := k.v.ReadDir("/")
	if err != nil || len(infos) != 0 {
		t.Fatalf("new volume ReadDir(/) = %v, %v", infos, err)
	}
	mustPanic(t, "simdisk: Volume: nil *kernel.Node", func() { k.d.Volume(nil) })
	foreign := kernel.New(kernel.Config{Seed: 1}).AddNode("a", func(*kernel.Node) {})
	mustPanic(t, "simdisk: Volume: node a belongs to a different Sim", func() { k.d.Volume(foreign) })

	// AT-DSK-26, DSK-042: Lookup finds existing volumes and never creates one.
	c := k.s.AddNode("c", func(*kernel.Node) {})
	e := k.s.AddNode("e", func(*kernel.Node) {})
	ve := k.d.Volume(e) // a volume after c's slot
	for i := 0; i < 2; i++ {
		if got, ok := k.d.Lookup(c); got != nil || ok {
			t.Fatalf("Lookup(c) = %p, %v; want nil, false", got, ok)
		}
	}
	for _, x := range []struct {
		n *kernel.Node
		v *simdisk.Volume
	}{{k.a, k.v}, {b, vb}, {e, ve}} {
		if got, ok := k.d.Lookup(x.n); got != x.v || !ok {
			t.Fatalf("Lookup(%s) = %p, %v; want %p, true", x.n.Name(), got, ok, x.v)
		}
	}
	recs = len(k.s.Records())
	c.Crash() // cancels c's pending initial boot; c still has no volume
	if got, ok := k.d.Lookup(c); got != nil || ok || len(k.s.Records()) != recs+1 {
		t.Fatalf("after crash: Lookup(c) = %p, %v; records +%d, want only kernel.crash", got, ok, len(k.s.Records())-recs)
	}
	mustPanic(t, "simdisk: Lookup: nil *kernel.Node", func() { k.d.Lookup(nil) })
	mustPanic(t, "simdisk: Lookup: node a belongs to a different Sim", func() { k.d.Lookup(foreign) })
}

// AT-DSK-17
func TestPathsAndDirectories(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	_, err := v.Create("f")
	checkPathErr(t, err, "create", "f", fs.ErrInvalid)
	_, err = v.Create("")
	checkPathErr(t, err, "create", "", simdisk.ErrInvalid)
	_, err = v.Create("/x\x00y")
	checkPathErr(t, err, "create", "/x\x00y", simdisk.ErrInvalid)
	must(t, v.Mkdir("/a"))
	f, err := v.Create("/a//b/../c")
	must(t, err)
	if f.Name() != "/a/c" {
		t.Fatalf("Name() = %q, want /a/c", f.Name())
	}
	if c := k.recordsOf("disk.create"); len(c) == 0 || summary(c[len(c)-1]) != "disk.create{path=/a/c, truncated=false}" || c[len(c)-1].Text != "create /a/c" {
		t.Fatalf("disk.create records %v", c)
	}
	_, err = v.Create("/missing/x")
	checkPathErr(t, err, "create", "/missing/x", simdisk.ErrNotExist)
	_, err = v.Create("/x//../missing/y")
	checkPathErr(t, err, "create", "/missing/y", simdisk.ErrNotExist)
	checkPathErr(t, v.Remove("//nope/"), "remove", "/nope", simdisk.ErrNotExist)
	_, err = v.Create("/")
	checkPathErr(t, err, "create", "/", simdisk.ErrIsDir)
	_, err = v.Create("/a")
	checkPathErr(t, err, "create", "/a", simdisk.ErrIsDir)
	checkPathErr(t, v.Mkdir("/a"), "mkdir", "/a", simdisk.ErrExist)
	checkPathErr(t, v.Mkdir("/"), "mkdir", "/", simdisk.ErrExist)
	_, err = v.Create("/f")
	must(t, err)
	checkPathErr(t, v.Mkdir("/f/x"), "mkdir", "/f/x", simdisk.ErrNotDir)

	before := len(k.recordsOf("disk.mkdir"))
	must(t, v.MkdirAll("/p/q/r"))
	mk := k.recordsOf("disk.mkdir")[before:]
	if len(mk) != 3 || attr(mk[0], "path") != "/p" || attr(mk[1], "path") != "/p/q" || attr(mk[2], "path") != "/p/q/r" {
		t.Fatalf("MkdirAll records %v", mk)
	}
	if mk[0].Text != "mkdir /p" {
		t.Fatalf("disk.mkdir Text = %q", mk[0].Text)
	}
	must(t, v.MkdirAll("/p/q/r"))
	if len(k.recordsOf("disk.mkdir")) != before+3 {
		t.Fatalf("second MkdirAll emitted records")
	}
	checkPathErr(t, v.MkdirAll("/f/g"), "mkdir", "/f/g", simdisk.ErrNotDir)

	for _, name := range []string{"/b", "/a2", "/c", "/B"} {
		_, err := v.Create(name)
		must(t, err)
	}
	infos, err := v.ReadDir("/")
	must(t, err)
	if got := names(infos); !slices.Equal(got, []string{"B", "a", "a2", "b", "c", "f", "p"}) {
		t.Fatalf("ReadDir(/) names = %v", got)
	}
	if infos[1] != (simdisk.Info{Name: "a", IsDir: true}) || infos[5] != (simdisk.Info{Name: "f"}) {
		t.Fatalf("ReadDir infos %+v", infos)
	}
	checkPathErr(t, v.Remove("/p"), "remove", "/p", simdisk.ErrNotEmpty)
	checkPathErr(t, v.Remove("/"), "remove", "/", simdisk.ErrInvalid)
	checkPathErr(t, v.Remove("/nope"), "remove", "/nope", simdisk.ErrNotExist)
	if st, err := v.Stat("/"); err != nil || st != (simdisk.Info{Name: "/", IsDir: true}) {
		t.Fatalf("Stat(/) = %+v, %v", st, err)
	}
	if st, err := v.Stat("/p/q"); err != nil || st != (simdisk.Info{Name: "q", IsDir: true}) {
		t.Fatalf("Stat(/p/q) = %+v, %v", st, err)
	}
	_, err = v.ReadDir("/f")
	checkPathErr(t, err, "readdir", "/f", simdisk.ErrNotDir)
	_, err = v.Stat("/f/x")
	checkPathErr(t, err, "stat", "/f/x", simdisk.ErrNotDir)
	_, err = v.Open("/a")
	checkPathErr(t, err, "open", "/a", simdisk.ErrIsDir)
	_, err = v.Open("/zz")
	checkPathErr(t, err, "open", "/zz", simdisk.ErrNotExist)
	must(t, v.Remove("/p/q/r"))
	r := k.recordsOf("disk.remove")
	if summary(r[len(r)-1]) != "disk.remove{path=/p/q/r, dir=true}" || r[len(r)-1].Text != "remove /p/q/r" {
		t.Fatalf("disk.remove record %v", summary(r[len(r)-1]))
	}
	_, err = v.Stat("/p/q/r")
	checkPathErr(t, err, "stat", "/p/q/r", simdisk.ErrNotExist)
}

// DSK-013 (namespace checks; AT-DSK-18 covers content)
func TestRenameChecks(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	for _, p := range []string{"/p/q", "/r/s"} {
		must(t, v.MkdirAll(p))
	}
	must(t, v.Mkdir("/e"))
	_, err := v.Create("/f")
	must(t, err)
	_, err = v.Create("/p/g")
	must(t, err)
	checkPathErr(t, v.Rename("/", "/x"), "rename", "/", simdisk.ErrInvalid)
	checkPathErr(t, v.Rename("/f", "/"), "rename", "/", simdisk.ErrInvalid)
	checkPathErr(t, v.Rename("rel", "/x"), "rename", "rel", simdisk.ErrInvalid)
	checkPathErr(t, v.Rename("/nope", "/x"), "rename", "/nope", simdisk.ErrNotExist)
	checkPathErr(t, v.Rename("/f", "/nope/x"), "rename", "/nope/x", simdisk.ErrNotExist)
	checkPathErr(t, v.Rename("/p", "/p/q/x"), "rename", "/p/q/x", simdisk.ErrInvalid)
	checkPathErr(t, v.Rename("/f", "/e"), "rename", "/e", simdisk.ErrIsDir)
	checkPathErr(t, v.Rename("/e", "/f"), "rename", "/f", simdisk.ErrNotDir)
	checkPathErr(t, v.Rename("/p", "/r"), "rename", "/r", simdisk.ErrNotEmpty)
	// DSK-013 order: each row below fails at an earlier check than a later one would report.
	checkPathErr(t, v.Rename("/nope", "/"), "rename", "/", simdisk.ErrInvalid)
	checkPathErr(t, v.Rename("/nope", "/nope"), "rename", "/nope", simdisk.ErrNotExist)
	checkPathErr(t, v.Rename("/nope", "/nope/x"), "rename", "/nope", simdisk.ErrNotExist)
	checkPathErr(t, v.Rename("/f/x", "/y"), "rename", "/f/x", simdisk.ErrNotDir)
	checkPathErr(t, v.Rename("/f", "/f/x"), "rename", "/f/x", simdisk.ErrNotDir)
	checkPathErr(t, v.Rename("/p", "/p/g"), "rename", "/p/g", simdisk.ErrInvalid)
	checkPathErr(t, v.Rename("/f", "/r"), "rename", "/r", simdisk.ErrIsDir)
	n := len(k.records())
	must(t, v.Rename("/f", "/f"))
	if len(k.records()) != n {
		t.Fatalf("Rename to itself emitted a record")
	}
	must(t, v.Rename("/p", "/e"))
	rr := k.recordsOf("disk.rename")
	if len(rr) != 1 || summary(rr[0]) != "disk.rename{old_path=/p, new_path=/e, replaced=true}" || rr[0].Text != "rename /p -> /e" {
		t.Fatalf("disk.rename records %v", rr)
	}
	if _, err := v.Stat("/e/q"); err != nil {
		t.Fatalf("renamed directory lost its children: %v", err)
	}
	_, err = v.Stat("/p")
	checkPathErr(t, err, "stat", "/p", simdisk.ErrNotExist)
	must(t, v.Rename("/e", "/ex")) // /ex shares a prefix with /e but is not inside it
}

// AT-DSK-14 (Close part), DSK-035
func TestClose(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	must(t, f.Close())
	err = f.Close()
	checkPathErr(t, err, "close", "/f", simdisk.ErrClosed)
	if !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("errors.Is(err, fs.ErrClosed) = false")
	}
	if f.Size() != -1 {
		t.Fatalf("Size() of a closed handle = %d, want -1", f.Size())
	}
}

// DSK-035, DSK-036, AT-DSK-19 (panics on a paused or down node)
func TestNodeStatePanics(t *testing.T) {
	k := newDisk(1, imm)
	f, err := k.v.Create("/f")
	must(t, err)
	k.a.Pause()
	mustPanic(t, "simdisk: Mkdir on a (id 1): node is paused", func() { k.v.Mkdir("/z") })
	mustPanic(t, "simdisk: Create on a (id 1): node is paused", func() { k.v.Create("not even a valid path") })
	must(t, f.Close()) // Close skips the node check
	k.a.Resume()
	k.a.Crash()
	for _, c := range []struct { // invalid paths: the node check runs first
		method string
		call   func()
	}{
		{"Create", func() { k.v.Create("x") }},
		{"Open", func() { k.v.Open("f") }},
		{"Remove", func() { k.v.Remove("f") }},
		{"Rename", func() { k.v.Rename("f", "g") }},
		{"Mkdir", func() { k.v.Mkdir("d") }},
		{"MkdirAll", func() { k.v.MkdirAll("d/e") }},
	} {
		mustPanic(t, "simdisk: "+c.method+" on a (id 1): node is down", c.call)
	}
	if _, err := k.v.Stat("/f"); err != nil {
		t.Fatalf("Stat on a down node: %v", err)
	}
	if _, err := k.v.ReadDir("/"); err != nil {
		t.Fatalf("ReadDir on a down node: %v", err)
	}
}

// AT-DSK-24 (namespace records), DSK §8
func TestNamespaceRecords(t *testing.T) {
	k := newDisk(1, imm)
	must(t, k.v.Mkdir("/d"))
	_, err := k.v.Create("/d/f")
	must(t, err)
	_, err = k.v.Create("/d/f")
	must(t, err)
	f, err := k.v.Open("/d/f") // Open, Stat, ReadDir and a successful Close emit nothing
	must(t, err)
	_, err = k.v.Stat("/d/f")
	must(t, err)
	_, err = k.v.ReadDir("/d")
	must(t, err)
	must(t, f.Close())
	must(t, k.v.Rename("/d/f", "/d/g"))
	must(t, k.v.Remove("/d/g"))
	want := []string{
		"disk.mkdir{path=/d}",
		"disk.create{path=/d/f, truncated=false}",
		"disk.create{path=/d/f, truncated=true}",
		"disk.rename{old_path=/d/f, new_path=/d/g, replaced=false}",
		"disk.remove{path=/d/g, dir=false}",
	}
	texts := []string{"mkdir /d", "create /d/f", "create /d/f (truncated)", "rename /d/f -> /d/g", "remove /d/g"}
	recs := k.records()
	if len(recs) != len(want) {
		t.Fatalf("%d records, want %d", len(recs), len(want))
	}
	for i, r := range recs {
		if summary(r) != want[i] || r.Text != texts[i] || r.Node != 1 || r.Inc != 1 {
			t.Errorf("record %d = %s %q node %d inc %d, want %s %q node 1 inc 1", i, summary(r), r.Text, r.Node, r.Inc, want[i], texts[i])
		}
	}
}

// DSK-008, DSK-037: errors, records and handle names use the cleaned path.
func TestCleanedPaths(t *testing.T) {
	k := newDisk(1, imm)
	v := k.v
	must(t, v.MkdirAll("/d/e"))
	for _, p := range []string{"/d/f", "/d/e/g"} {
		_, err := v.Create(p)
		must(t, err)
	}
	n := len(k.records())
	create := func(p string) error { _, err := v.Create(p); return err }
	open := func(p string) error { _, err := v.Open(p); return err }
	stat := func(p string) error { _, err := v.Stat(p); return err }
	readDir := func(p string) error { _, err := v.ReadDir(p); return err }
	for _, c := range []struct {
		err      error
		op, path string
		want     error
	}{
		{create("/.."), "create", "/", simdisk.ErrIsDir},
		{create("/d/./e/"), "create", "/d/e", simdisk.ErrIsDir},
		{open("/d//e"), "open", "/d/e", simdisk.ErrIsDir},
		{open("/d/zz/"), "open", "/d/zz", simdisk.ErrNotExist},
		{v.Remove("//"), "remove", "/", simdisk.ErrInvalid},
		{v.Remove("/d/./e"), "remove", "/d/e", simdisk.ErrNotEmpty},
		{v.Remove("/d/f/x/"), "remove", "/d/f/x", simdisk.ErrNotDir},
		{v.Mkdir("/./"), "mkdir", "/", simdisk.ErrExist},
		{v.Mkdir("/d/e/"), "mkdir", "/d/e", simdisk.ErrExist},
		{v.Mkdir("/d/f/x/"), "mkdir", "/d/f/x", simdisk.ErrNotDir},
		{v.MkdirAll("/d/f/x/"), "mkdir", "/d/f/x", simdisk.ErrNotDir},
		{v.Rename("/./", "/x"), "rename", "/", simdisk.ErrInvalid},
		{v.Rename("/d/f", "//"), "rename", "/", simdisk.ErrInvalid},
		{v.Rename("/nope/", "/x"), "rename", "/nope", simdisk.ErrNotExist},
		{v.Rename("/d/f/x/", "/x"), "rename", "/d/f/x", simdisk.ErrNotDir},
		{v.Rename("/d/f", "/zz/x/"), "rename", "/zz/x", simdisk.ErrNotExist},
		{v.Rename("/d/", "/d/e/x/"), "rename", "/d/e/x", simdisk.ErrInvalid},
		{v.Rename("/d/e", "/d/f/"), "rename", "/d/f", simdisk.ErrNotDir},
		{v.Rename("/d/f", "/d//e"), "rename", "/d/e", simdisk.ErrIsDir},
		{v.Rename("/d/e", "/d/"), "rename", "/d", simdisk.ErrNotEmpty},
		{stat("/d/f/x/"), "stat", "/d/f/x", simdisk.ErrNotDir},
		{readDir("/d//f"), "readdir", "/d/f", simdisk.ErrNotDir},
		{readDir("/zz/"), "readdir", "/zz", simdisk.ErrNotExist},
	} {
		checkPathErr(t, c.err, c.op, c.path, c.want)
	}
	f, err := v.Open("/d/./f/")
	must(t, err)
	g, err := v.Create("//d/f")
	must(t, err)
	if f.Name() != "/d/f" || g.Name() != "/d/f" {
		t.Errorf("Open(/d/./f/).Name() = %q, Create(//d/f).Name() = %q; want /d/f", f.Name(), g.Name())
	}
	must(t, v.Mkdir("/d/./h/"))
	must(t, v.Rename("/d/h/", "//d/i"))
	must(t, v.Remove("/d/e/g/."))
	want := []string{
		"disk.create{path=/d/f, truncated=true} create /d/f (truncated)",
		"disk.mkdir{path=/d/h} mkdir /d/h",
		"disk.rename{old_path=/d/h, new_path=/d/i, replaced=false} rename /d/h -> /d/i",
		"disk.remove{path=/d/e/g, dir=false} remove /d/e/g",
	}
	recs := k.records()[n:]
	if len(recs) != len(want) {
		t.Fatalf("%d records, want %d", len(recs), len(want))
	}
	for i, r := range recs {
		if got := summary(r) + " " + r.Text; got != want[i] {
			t.Errorf("record %d = %s, want %s", i, got, want[i])
		}
	}
}
