// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx_test

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/shims/fsx"
)

func TestErrWriteAtInAppendMode(t *testing.T) {
	if fsx.ErrWriteAtInAppendMode.Error() != "fsx: invalid use of WriteAt on file opened with O_APPEND" {
		t.Fatal(fsx.ErrWriteAtInAppendMode)
	}
}

// TestErrorMapping checks FSX-006's errno/sentinel pairs through fsx.MapForTest (export_test.go).
func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		in     error
		errno  syscall.Errno
		extras []error
	}{
		{simdisk.ErrNotExist, syscall.ENOENT, []error{fs.ErrNotExist}},
		{simdisk.ErrExist, syscall.EEXIST, []error{fs.ErrExist}},
		{simdisk.ErrInvalid, syscall.EINVAL, []error{fs.ErrInvalid}},
		{simdisk.ErrIsDir, syscall.EISDIR, []error{simdisk.ErrIsDir}},
		{simdisk.ErrNotDir, syscall.ENOTDIR, []error{simdisk.ErrNotDir}},
		{simdisk.ErrNotEmpty, syscall.ENOTEMPTY, []error{simdisk.ErrNotEmpty, fs.ErrExist}},
		{simdisk.ErrIO, syscall.EIO, []error{simdisk.ErrIO}},
		{simdisk.ErrNoSpace, syscall.ENOSPC, []error{simdisk.ErrNoSpace}},
		{simdisk.ErrStale, syscall.ESTALE, []error{simdisk.ErrStale}},
		{simdisk.ErrTooLarge, syscall.EFBIG, []error{simdisk.ErrTooLarge}},
	} {
		err := fsx.MapForTest("write", "a/b", &fs.PathError{Op: "writeat", Path: "/a/b", Err: tc.in})
		var pe *fs.PathError
		if !errors.As(err, &pe) || pe.Op != "write" || pe.Path != "a/b" {
			t.Fatalf("%v: %#v", tc.in, err)
		}
		var errno syscall.Errno
		if !errors.As(err, &errno) || errno != tc.errno || !errors.Is(err, tc.errno) {
			t.Fatalf("%v: errno %v, want %v", tc.in, errno, tc.errno)
		}
		for _, x := range tc.extras {
			if !errors.Is(err, x) {
				t.Fatalf("%v: errors.Is(%v) false", tc.in, x)
			}
		}
		if pe.Err.Error() != tc.errno.Error() {
			t.Fatalf("simErr text %q, want the errno's %q", pe.Err.Error(), tc.errno.Error())
		}
	}
	closed := fsx.MapForTest("close", "f", &fs.PathError{Op: "close", Path: "/f", Err: simdisk.ErrClosed})
	if !errors.Is(closed, fs.ErrClosed) || !errors.Is(closed, os.ErrClosed) {
		t.Fatalf("ErrClosed maps to %v", closed)
	}
	if fsx.MapForTest("read", "f", nil) != nil {
		t.Fatal("nil error mapped to non-nil")
	}
	if fsx.LinkErrForTest("a", "b", nil) != nil {
		t.Fatal("linkErr(nil) is non-nil")
	}
	le := fsx.LinkErrForTest("a", "b", &fs.PathError{Op: "rename", Path: "/a", Err: simdisk.ErrNotExist})
	var lerr *os.LinkError
	if !errors.As(le, &lerr) || lerr.Op != "rename" || lerr.Old != "a" || lerr.New != "b" {
		t.Fatalf("linkErr = %#v", le)
	}
	if !errors.Is(lerr.Err, syscall.ENOENT) || !errors.Is(lerr.Err, simdisk.ErrNotExist) {
		t.Fatalf("linkErr Err = %v, want ENOENT and simdisk.ErrNotExist", lerr.Err)
	}
}

func TestClean(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a/b", "/a/b"}, {"/a/b", "/a/b"}, {"x/../a/b", "/a/b"}, {"../../a", "/a"}, {".", "/"}, {"/", "/"}, {`a\b`, `/a\b`},
	} {
		got, err := fsx.CleanForTest("open", tc.in)
		if err != nil || got != tc.want {
			t.Errorf("clean(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := fsx.CleanForTest("open", ""); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("clean(\"\") = %v, want ENOENT", err)
	}
	if _, err := fsx.CleanForTest("open", "a\x00b"); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("clean(NUL) = %v, want EINVAL", err)
	}
}

func TestATFSX01_OSConformance(t *testing.T) {
	cases := fsx.Conformance()
	if len(cases) != 42 {
		t.Fatalf("%d conformance cases, want 42", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			fsys := fsx.OS(t.TempDir())
			if cl, ok := fsys.(io.Closer); ok {
				defer cl.Close()
			}
			if err := c.Run(fsys); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConformanceOrder(t *testing.T) {
	want := []string{"create-write-read", "open-missing", "open-empty-name", "create-excl", "open-trunc",
		"append", "create-missing-parent", "path-through-file", "mkdir-existing", "mkdir-missing-parent",
		"mkdirall", "mkdirall-file", "remove-file", "remove-nonempty", "remove-missing", "remove-root",
		"removeall", "removeall-file", "rename-replace", "rename-missing", "rename-onto-dir",
		"rename-dir-onto-file", "rename-into-self", "rename-missing-parent", "readdir",
		"readdir-file-missing", "stat", "access-mode", "readat", "seek", "seek-past-end", "close-twice",
		"dir-handle", "open-dir-write", "truncate", "syncdir", "name", "clean-names", "osync",
		"unlinked-handle", "large", "helpers"}
	var got []string
	for _, c := range fsx.Conformance() {
		got = append(got, c.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("case order\n%v\nwant\n%v", got, want)
	}
}

func TestATFSX11_OSConfinement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	if err := os.Symlink("/etc", filepath.Join(dir, "esc")); err != nil {
		t.Fatal(err)
	}
	fsys := fsx.OS(dir)
	if _, err := fsys.Open("esc/hosts"); err == nil {
		t.Fatal("Open through a symlink that leaves the root succeeded")
	}
	if _, err := fsys.Open("../../etc/hosts"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open(../../etc/hosts) = %v, want ErrNotExist (it names <dir>/etc/hosts)", err)
	}
}

func TestATFSX16_OSRootMissing(t *testing.T) {
	_, err := fsx.OS("/nonexistent-dir").Stat("a")
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != "stat" || pe.Path != "a" || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat = %#v", err)
	}
}

func TestCreateTempPatternWithSeparator(t *testing.T) {
	_, err := fsx.CreateTemp(fsx.OS(t.TempDir()), "", "a/b*")
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != "createtemp" || pe.Path != "a/b*" || pe.Err.Error() != "pattern contains path separator" {
		t.Fatalf("CreateTemp = %v", err)
	}
}

// TestOSRenameOntoDir checks Rename onto an existing directory on every Go version: the error of
// Lstat(oldname) if it fails, else EEXIST, also when both names are the same directory.
func TestOSRenameOntoDir(t *testing.T) {
	fsys := fsx.OS(t.TempDir())
	if err := fsys.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Mkdir("e", 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := fsys.Create("f")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, tc := range []struct {
		oldname string
		want    syscall.Errno
	}{{"nope", syscall.ENOENT}, {"f/x", syscall.ENOTDIR}, {"d", syscall.EEXIST}, {"e", syscall.EEXIST}} {
		err := fsys.Rename(tc.oldname, "d")
		var le *os.LinkError
		if !errors.As(err, &le) || le.Op != "rename" || le.Old != tc.oldname || le.New != "d" || !errors.Is(err, tc.want) {
			t.Errorf("Rename(%q, d) = %v, want *os.LinkError %v", tc.oldname, err, tc.want)
		}
	}
	if info, err := fsys.Stat("e"); err != nil || !info.IsDir() {
		t.Fatalf("e after Rename(e, d): %v, %v", info, err)
	}
}

// TestOSRenameOntoDirSymlink checks that a symbolic link to a directory at newname is replaced, as
// os.Rename does: the link itself is not a directory.
func TestOSRenameOntoDirSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	dir := t.TempDir()
	fsys := fsx.OS(dir)
	if err := fsys.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("d", filepath.Join(dir, "ln")); err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteFile(fsys, "f", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Rename("f", "ln"); err != nil {
		t.Fatalf("Rename(f, ln) = %v, want nil", err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "ln")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("ln after Rename: %v, %v; want the regular file", info, err)
	}
	if info, err := fsys.Stat("d"); err != nil || !info.IsDir() {
		t.Fatalf("d after Rename: %v, %v", info, err)
	}
}

// TestOSRenameCaseOnly checks that renaming a directory to its own name in another case renames it
// on a case-insensitive volume, as os.Rename does. It is skipped on a case-sensitive volume.
func TestOSRenameCaseOnly(t *testing.T) {
	dir := t.TempDir()
	fsys := fsx.OS(dir)
	if err := fsys.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "D")); err != nil {
		t.Skip("case-sensitive volume")
	}
	if err := fsys.Rename("d", "D"); err != nil {
		t.Fatalf("Rename(d, D) = %v, want nil", err)
	}
	ents, err := fsys.ReadDir("/")
	if err != nil || len(ents) != 1 || ents[0].Name() != "D" || !ents[0].IsDir() {
		t.Fatalf("ReadDir after Rename(d, D) = %v, %v; want [D]", ents, err)
	}
}

// TestOSHandleCheckOrder checks that OS handles give FSX-030's results, in its order, also for the
// inputs os checks in another order or not at all: zero-length buffers, negative offsets and
// WriteAt in append mode.
func TestOSHandleCheckOrder(t *testing.T) {
	testHandleCheckOrder(t, func(t *testing.T) (fsx.FS, func()) {
		fsys := fsx.OS(t.TempDir())
		if cl, ok := fsys.(io.Closer); ok {
			t.Cleanup(func() { _ = cl.Close() })
		}
		return fsys, nil
	})
}

// testHandleCheckOrder checks FSX-030's handle checks, in order, and FSX-031's offset and size rows.
// Each row gets a fresh FS from newFS with the file "f" ("data") and the directory "d" in it, and
// wants n and the exact Err. newFS returns a nil crash for OS; for Sim, crash kills the node behind
// the FS. The rows on a dead incarnation, and Seek(0, SeekEnd) on a directory (OS gives the
// platform's directory size), run on Sim only.
func testHandleCheckOrder(t *testing.T, newFS func(*testing.T) (fsys fsx.FS, crash func())) {
	t.Helper()
	const (
		closed  = 1 << iota // close the handle before the call
		dead                // crash the node before the call (Sim only)
		simOnly             // OS gives a platform-specific result
	)
	b := []byte("x")
	read := func(b []byte) func(fsx.File) (int, error) {
		return func(f fsx.File) (int, error) { return f.Read(b) }
	}
	write := func(b []byte) func(fsx.File) (int, error) {
		return func(f fsx.File) (int, error) { return f.Write(b) }
	}
	readAt := func(b []byte, off int64) func(fsx.File) (int, error) {
		return func(f fsx.File) (int, error) { return f.ReadAt(b, off) }
	}
	writeAt := func(b []byte, off int64) func(fsx.File) (int, error) {
		return func(f fsx.File) (int, error) { return f.WriteAt(b, off) }
	}
	seek := func(off int64, whence int) func(fsx.File) (int, error) {
		return func(f fsx.File) (int, error) {
			n, err := f.Seek(off, whence)
			return int(n), err
		}
	}
	truncate := func(size int64) func(fsx.File) (int, error) {
		return func(f fsx.File) (int, error) { return 0, f.Truncate(size) }
	}
	stat := func(f fsx.File) (int, error) {
		info, err := f.Stat()
		if err != nil {
			return 0, err
		}
		return int(info.Size()), nil
	}
	syncFile := func(f fsx.File) (int, error) { return 0, f.Sync() }
	closeFile := func(f fsx.File) (int, error) { return 0, f.Close() }
	// readAfter runs call, then reads from the offset: 4 bytes when call left it at 0.
	readAfter := func(call func(fsx.File) (int, error)) func(fsx.File) (int, error) {
		return func(f fsx.File) (int, error) {
			if _, err := call(f); err != nil {
				return 0, err
			}
			return f.Read(make([]byte, 10))
		}
	}
	for _, tc := range []struct {
		desc string
		name string
		flag int
		how  int // closed, dead, simOnly
		op   string
		call func(fsx.File) (int, error)
		n    int
		want error // nil means n, nil
	}{
		{"Read(nil) closed", "f", os.O_RDONLY, closed, "read", read(nil), 0, fs.ErrClosed},
		{"Read(nil) O_WRONLY", "f", os.O_WRONLY, 0, "read", read(nil), 0, syscall.EBADF},
		{"Read(nil) directory", "d", os.O_RDONLY, 0, "read", read(nil), 0, syscall.EISDIR},
		{"Read(nil)", "f", os.O_RDONLY, 0, "read", read(nil), 0, nil},
		{"Write(nil) closed", "f", os.O_WRONLY, closed, "write", write(nil), 0, fs.ErrClosed},
		{"Write(nil) O_RDONLY", "f", os.O_RDONLY, 0, "write", write(nil), 0, syscall.EBADF},
		{"Write(nil)", "f", os.O_WRONLY, 0, "write", write(nil), 0, nil},
		{"ReadAt(b, -1) closed", "f", os.O_RDONLY, closed, "readat", readAt(b, -1), 0, fs.ErrClosed},
		{"ReadAt(b, -1) O_WRONLY", "f", os.O_WRONLY, 0, "readat", readAt(b, -1), 0, syscall.EBADF},
		{"ReadAt(b, -1) directory", "d", os.O_RDONLY, 0, "readat", readAt(b, -1), 0, syscall.EISDIR},
		{"ReadAt(b, -1)", "f", os.O_RDONLY, 0, "readat", readAt(b, -1), 0, syscall.EINVAL},
		{"ReadAt(nil, -1)", "f", os.O_RDONLY, 0, "readat", readAt(nil, -1), 0, syscall.EINVAL},
		{"ReadAt(nil, 0) O_WRONLY", "f", os.O_WRONLY, 0, "readat", readAt(nil, 0), 0, syscall.EBADF},
		{"ReadAt(nil, 0) directory", "d", os.O_RDONLY, 0, "readat", readAt(nil, 0), 0, syscall.EISDIR},
		{"ReadAt(nil, 0)", "f", os.O_RDONLY, 0, "readat", readAt(nil, 0), 0, nil},
		{"WriteAt(b, -1) closed", "f", os.O_RDWR, closed, "writeat", writeAt(b, -1), 0, fs.ErrClosed},
		{"WriteAt(b, 0) closed O_APPEND", "f", os.O_WRONLY | os.O_APPEND, closed, "writeat", writeAt(b, 0), 0, fs.ErrClosed},
		{"WriteAt(b, 0) O_RDONLY|O_APPEND", "f", os.O_RDONLY | os.O_APPEND, 0, "writeat", writeAt(b, 0), 0, syscall.EBADF},
		{"WriteAt(b, -1) O_RDONLY", "f", os.O_RDONLY, 0, "writeat", writeAt(b, -1), 0, syscall.EBADF},
		{"WriteAt(b, -1) directory", "d", os.O_RDONLY, 0, "writeat", writeAt(b, -1), 0, syscall.EBADF},
		{"WriteAt(nil, 0) O_RDONLY", "f", os.O_RDONLY, 0, "writeat", writeAt(nil, 0), 0, syscall.EBADF},
		{"WriteAt(b, -1) O_APPEND", "f", os.O_WRONLY | os.O_APPEND, 0, "writeat", writeAt(b, -1), 0, fsx.ErrWriteAtInAppendMode},
		{"WriteAt(b, -1)", "f", os.O_RDWR, 0, "writeat", writeAt(b, -1), 0, syscall.EINVAL},
		{"WriteAt(nil, -1)", "f", os.O_RDWR, 0, "writeat", writeAt(nil, -1), 0, syscall.EINVAL},
		{"WriteAt(nil, 0)", "f", os.O_RDWR, 0, "writeat", writeAt(nil, 0), 0, nil},

		// The other methods' checks, and directory handles.
		{"Read(b) closed directory", "d", os.O_RDONLY, closed, "read", read(b), 0, fs.ErrClosed},
		{"Write(b) closed directory", "d", os.O_RDONLY, closed, "write", write(b), 0, fs.ErrClosed},
		{"Write(b) directory", "d", os.O_RDONLY, 0, "write", write(b), 0, syscall.EBADF},
		{"ReadAt(b, 0) closed directory", "d", os.O_RDONLY, closed, "readat", readAt(b, 0), 0, fs.ErrClosed},
		{"Seek(0, SeekStart) closed", "f", os.O_RDONLY, closed, "seek", seek(0, io.SeekStart), 0, fs.ErrClosed},
		{"Truncate(0) closed O_RDONLY", "f", os.O_RDONLY, closed, "truncate", truncate(0), 0, fs.ErrClosed},
		{"Truncate(0) directory", "d", os.O_RDONLY, 0, "truncate", truncate(0), 0, syscall.EINVAL},
		{"Truncate(-1)", "f", os.O_RDWR, 0, "truncate", truncate(-1), 0, syscall.EINVAL},
		{"Stat() closed", "f", os.O_RDONLY, closed, "stat", stat, 0, fs.ErrClosed},
		{"Sync() closed directory", "d", os.O_RDONLY, closed, "sync", syncFile, 0, fs.ErrClosed},
		{"Close() closed directory", "d", os.O_RDONLY, closed, "close", closeFile, 0, fs.ErrClosed},

		// FSX-031: Seek's result, Stat's size, and ReadAt and WriteAt leave the offset.
		{"Seek(-1, SeekStart)", "f", os.O_RDONLY, 0, "seek", seek(-1, io.SeekStart), 0, syscall.EINVAL},
		{"Seek(0, SeekEnd) directory", "d", os.O_RDONLY, simOnly, "seek", seek(0, io.SeekEnd), 0, nil},
		{"Stat() size", "f", os.O_RDONLY, 0, "stat", stat, 4, nil},
		{"ReadAt then Read", "f", os.O_RDONLY, 0, "read", readAfter(readAt(make([]byte, 2), 1)), 4, nil},
		{"WriteAt then Read", "f", os.O_RDWR, 0, "read", readAfter(writeAt([]byte("zz"), 0)), 4, nil},

		// A dead incarnation: the checks still come first (FSX-011).
		{"Write(nil) dead", "f", os.O_RDWR, dead, "write", write(nil), 0, nil},
		{"ReadAt(b, -1) dead", "f", os.O_RDWR, dead, "readat", readAt(b, -1), 0, syscall.EINVAL},
		{"WriteAt(b, -1) dead", "f", os.O_RDWR, dead, "writeat", writeAt(b, -1), 0, syscall.EINVAL},
		{"Truncate(-1) dead", "f", os.O_RDWR, dead, "truncate", truncate(-1), 0, syscall.EINVAL},
		{"Write(b) closed dead", "f", os.O_RDWR, closed | dead, "write", write(b), 0, fs.ErrClosed},
		{"WriteAt(b, 0) closed dead", "f", os.O_RDWR, closed | dead, "writeat", writeAt(b, 0), 0, fs.ErrClosed},
		{"Truncate(0) closed dead", "f", os.O_RDWR, closed | dead, "truncate", truncate(0), 0, fs.ErrClosed},
		{"Sync() closed dead", "f", os.O_RDWR, closed | dead, "sync", syncFile, 0, fs.ErrClosed},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			fsys, crash := newFS(t)
			if crash == nil && tc.how&(dead|simOnly) != 0 {
				t.Skip("Sim only")
			}
			if err := fsx.WriteFile(fsys, "f", []byte("data"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := fsys.Mkdir("d", 0o755); err != nil {
				t.Fatal(err)
			}
			f, err := fsys.OpenFile(tc.name, tc.flag, 0)
			if err != nil {
				t.Fatalf("OpenFile: %v", err)
			}
			if tc.how&closed != 0 {
				if err := f.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			} else {
				defer func() { _ = f.Close() }()
			}
			if tc.how&dead != 0 {
				crash()
			}
			n, err := tc.call(f)
			if tc.want == nil {
				if n != tc.n || err != nil {
					t.Errorf("= %d, %v; want %d, nil", n, err, tc.n)
				}
				return
			}
			var pe *fs.PathError
			if n != 0 || !errors.As(err, &pe) || pe.Op != tc.op || pe.Path != tc.name || pe.Err != tc.want {
				t.Errorf("= %d, %#v; want 0, *fs.PathError{%s, %s, %v}", n, err, tc.op, tc.name, tc.want)
			}
		})
	}
}

// TestOSTruncateOnOpen checks that WriteFile and Create truncate an existing longer file.
func TestOSTruncateOnOpen(t *testing.T) {
	fsys := fsx.OS(t.TempDir())
	if cl, ok := fsys.(io.Closer); ok {
		t.Cleanup(func() { _ = cl.Close() })
	}
	if err := fsx.WriteFile(fsys, "f", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteFile(fsys, "f", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := fsx.ReadFile(fsys, "f"); err != nil || string(got) != "x" {
		t.Fatalf("ReadFile after a shorter WriteFile = %q, %v; want \"x\"", got, err)
	}
	f, err := fsys.Create("f")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := fsys.Stat("f"); err != nil || info.Size() != 0 {
		t.Fatalf("Stat after Create of a non-empty file = %v, %v; want size 0", info, err)
	}
}

// newVolume is AT-FSX-02's setup: a fresh kernel, simdisk with defaults, one booted node.
func newVolume(tb testing.TB) *simdisk.Volume {
	tb.Helper()
	s := kernel.New(kernel.Config{Seed: 1})
	d := simdisk.New(s, simdisk.DefaultConfig())
	n := s.AddNode("n", func(*kernel.Node) {})
	s.RunUntil(0)
	return d.Volume(n)
}

func TestATFSX02_SimConformance(t *testing.T) {
	for _, c := range fsx.Conformance() {
		t.Run(c.Name, func(t *testing.T) {
			if err := c.Run(fsx.Sim(newVolume(t))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestATFSX15_SimPanics(t *testing.T) {
	for _, tc := range []struct {
		f    func()
		want string
	}{
		{func() { fsx.Sim(nil) }, "fsx: Sim: nil *simdisk.Volume"},
		{func() { fsx.WithDriver(nil, 1) }, "fsx: WithDriver: nil Driver"},
	} {
		func() {
			defer func() {
				if r := recover(); r != tc.want {
					t.Fatalf("panic %v, want %q", r, tc.want)
				}
			}()
			tc.f()
		}()
	}
}

func TestSimDirectModeStale(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	d := simdisk.New(s, simdisk.DefaultConfig())
	n := s.AddNode("n", func(*kernel.Node) {})
	s.RunUntil(0)
	fsys := fsx.Sim(d.Volume(n))
	if err := fsx.WriteFile(fsys, "a", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	n.Crash()
	_, err := fsys.Stat("a")
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != "stat" || pe.Path != "a" || !errors.Is(err, syscall.ESTALE) || !errors.Is(err, simdisk.ErrStale) {
		t.Fatalf("Stat after crash = %v, want ESTALE", err)
	}
	n.Restart()
	if _, err := fsys.Stat("a"); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("Stat after restart through the old FS = %v, want ESTALE (bound incarnation)", err)
	}
	if _, err := fsx.Sim(d.Volume(n)).Stat("a"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a new FS of incarnation 2: %v", err)
	}
}

// TestSimDirectModeNoLatency pins FSX-011's direct frame: with every latency class configured, Sim
// operations draw nothing from the node's latency stream and emit no fsx.latency record.
func TestSimDirectModeNoLatency(t *testing.T) {
	cfg := simdisk.DefaultConfig()
	l := simdisk.Latency{Base: time.Millisecond, Jitter: 10 * time.Millisecond}
	cfg.Latency.Read, cfg.Latency.Write, cfg.Latency.Sync, cfg.Latency.Meta = l, l, l, l
	newSim := func() (*kernel.Sim, *simdisk.Volume) {
		s := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
		d := simdisk.New(s, cfg)
		n := s.AddNode("n", func(*kernel.Node) {})
		s.RunUntil(0)
		return s, d.Volume(n)
	}
	s, v := newSim()
	fsys := fsx.Sim(v)
	f, err := fsys.Create("f") // meta
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("x"), 0); err != nil { // write
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil { // sync
		t.Fatal(err)
	}
	if _, err := f.ReadAt(make([]byte, 1), 0); err != nil { // read
		t.Fatal(err)
	}
	_, ref := newSim()
	if got, want := v.SampleLatency(simdisk.OpWrite), ref.SampleLatency(simdisk.OpWrite); got != want {
		t.Fatalf("next latency draw %v, want the stream's first draw %v", got, want)
	}
	for _, r := range s.Records() {
		if r.Kind == "fsx.latency" {
			t.Fatalf("direct mode emitted %+v", r)
		}
	}
}

// TestSimDirectModeRules pins the Sim rows that the conformance cases do not reach: FSX-020's flag
// rows, the "/" rows of FSX-022 to FSX-024, FSX-023's rule for a directory as the new name, FSX-030's
// check order, a directory handle's Stat (FSX-031), and the error of each method on a dead
// incarnation (FSX-011): clean errors and handle checks first, then ESTALE with the method's op.
func TestSimDirectModeRules(t *testing.T) {
	type rule struct {
		name    string
		setup   func(fsx.FS) (fsx.File, error) // runs on the live node; the handle goes to call
		crash   bool                           // crash the node between setup and call
		call    func(fsx.FS, fsx.File) error
		op      string             // "" wants nil; "rename" wants an *os.LinkError
		path    string             // PathError.Path, or LinkError.Old
		newPath string             // LinkError.New
		is      []error            // errors.Is holds for each
		plain   bool               // the Err is exactly is[0]
		after   func(fsx.FS) error // checks the volume after call
	}
	stale := []error{syscall.ESTALE, simdisk.ErrStale}
	einval := []error{syscall.EINVAL}
	writeA := func(fsys fsx.FS) error { return fsx.WriteFile(fsys, "a", []byte("abc"), 0o644) }
	fileA := func(fsys fsx.FS) (fsx.File, error) { return nil, writeA(fsys) }
	dirD := func(fsys fsx.FS) (fsx.File, error) { return nil, fsys.Mkdir("d", 0o755) }
	tree := func(fsys fsx.FS) (fsx.File, error) {
		return nil, errors.Join(writeA(fsys), fsys.Mkdir("d", 0o755), fsx.WriteFile(fsys, "d/b", []byte("b"), 0o644))
	}
	handle := func(flag int, closed bool) func(fsx.FS) (fsx.File, error) {
		return func(fsys fsx.FS) (fsx.File, error) {
			if err := writeA(fsys); err != nil {
				return nil, err
			}
			h, err := fsys.OpenFile("a", flag, 0)
			if err == nil && closed {
				err = h.Close()
			}
			return h, err
		}
	}
	opened := func(h fsx.File, err error) error {
		if err != nil {
			return err
		}
		return h.Close()
	}
	openFile := func(name string, flag int) func(fsx.FS, fsx.File) error {
		return func(fsys fsx.FS, _ fsx.File) error { return opened(fsys.OpenFile(name, flag, 0o644)) }
	}
	removeAll := func(name string) func(fsx.FS, fsx.File) error {
		return func(fsys fsx.FS, _ fsx.File) error { return fsys.RemoveAll(name) }
	}
	mkdirAll := func(name string) func(fsx.FS, fsx.File) error {
		return func(fsys fsx.FS, _ fsx.File) error { return fsys.MkdirAll(name, 0o755) }
	}
	rename := func(oldname, newname string) func(fsx.FS, fsx.File) error {
		return func(fsys fsx.FS, _ fsx.File) error { return fsys.Rename(oldname, newname) }
	}
	read := func(_ fsx.FS, h fsx.File) error {
		_, err := h.Read(make([]byte, 1))
		return err
	}
	readEmpty := func(_ fsx.FS, h fsx.File) error {
		if n, err := h.Read(nil); n != 0 || err != nil {
			return fmt.Errorf("Read(nil) = %d, %v; want 0, nil", n, err)
		}
		return nil
	}
	exist := func(names ...string) func(fsx.FS) error {
		return func(fsys fsx.FS) error {
			for _, name := range names {
				if _, err := fsys.Stat(name); err != nil {
					return err
				}
			}
			return nil
		}
	}
	rules := []rule{
		// FSX-020: the access mode and the flags on a directory and with O_RDONLY.
		{name: "open bad access mode", setup: fileA, call: openFile("a", os.O_WRONLY|os.O_RDWR),
			op: "open", path: "a", is: einval, plain: true},
		{name: "open dir create excl", setup: dirD, call: openFile("d", os.O_RDONLY|os.O_CREATE|os.O_EXCL),
			op: "open", path: "d", is: []error{syscall.EEXIST}, plain: true},
		{name: "open dir create", setup: dirD, call: openFile("d", os.O_RDONLY|os.O_CREATE),
			op: "open", path: "d", is: []error{syscall.EISDIR}, plain: true},
		{name: "open read-only trunc keeps the data", setup: fileA, call: openFile("a", os.O_RDONLY|os.O_TRUNC),
			after: func(fsys fsx.FS) error {
				if b, err := fsx.ReadFile(fsys, "a"); err != nil || string(b) != "abc" {
					return fmt.Errorf("ReadFile(a) = %q, %v; want abc", b, err)
				}
				return nil
			}},

		// FSX-022 to FSX-024: the root, live.
		{name: "removeall root keeps the tree", setup: tree, call: removeAll("/"),
			op: "removeall", path: "/", is: einval, plain: true, after: exist("a", "d", "d/b")},
		{name: "mkdirall root", call: mkdirAll("/")},
		{name: "rename root", call: rename("/", "x"), op: "rename", path: "/", newPath: "x", is: einval, plain: true},
		{name: "rename onto root", setup: fileA, call: rename("a", "/"),
			op: "rename", path: "a", newPath: "/", is: einval, plain: true, after: exist("a")},

		// FSX-023: onto a directory, the error of Stat(oldp) wins over EEXIST.
		{name: "rename missing onto dir", setup: tree, call: rename("m", "d"),
			op: "rename", path: "m", newPath: "d", is: []error{syscall.ENOENT, simdisk.ErrNotExist}, after: exist("d/b")},
		{name: "rename through a file onto dir", setup: tree, call: rename("a/x", "d"),
			op: "rename", path: "a/x", newPath: "d", is: []error{syscall.ENOTDIR, simdisk.ErrNotDir}, after: exist("a", "d/b")},

		// FSX-030 and FSX-031 on a live node.
		{name: "read checks closed before the access mode", setup: handle(os.O_WRONLY, true), call: read,
			op: "read", path: "a", is: []error{fs.ErrClosed}, plain: true},
		{name: "read empty", setup: handle(os.O_RDONLY, false), call: readEmpty},
		{name: "dir handle stat reports its path",
			setup: func(fsys fsx.FS) (fsx.File, error) {
				if err := fsys.Mkdir("d", 0o755); err != nil {
					return nil, err
				}
				h, err := fsys.Open("d")
				if err != nil {
					return nil, err
				}
				return h, errors.Join(fsys.Remove("d"), fsx.WriteFile(fsys, "d", []byte("abc"), 0o644))
			},
			call: func(_ fsx.FS, h fsx.File) error {
				fi, err := h.Stat()
				if err != nil {
					return err
				}
				if fi.Name() != "d" || fi.IsDir() || fi.Mode().IsDir() || fi.Size() != 3 {
					return fmt.Errorf("Stat() = %q dir %v mode %v size %d; want the file d of size 3", fi.Name(), fi.IsDir(), fi.Mode(), fi.Size())
				}
				return nil
			}},

		// A dead incarnation: clean errors and handle checks come first.
		{name: "dead removeall empty name", crash: true, call: removeAll("")},
		{name: "dead open empty name", crash: true, call: openFile("", os.O_RDONLY),
			op: "open", path: "", is: []error{syscall.ENOENT}, plain: true},
		{name: "dead read closed", setup: handle(os.O_RDWR, true), crash: true, call: read,
			op: "read", path: "a", is: []error{fs.ErrClosed}, plain: true},
		{name: "dead read write-only", setup: handle(os.O_WRONLY, false), crash: true, call: read,
			op: "read", path: "a", is: []error{syscall.EBADF}, plain: true},
		{name: "dead read empty", setup: handle(os.O_RDONLY, false), crash: true, call: readEmpty},

		// A dead incarnation: the root rows run inside the frame.
		{name: "dead mkdirall root", crash: true, call: mkdirAll("/"), op: "mkdir", path: "/", is: stale},
		{name: "dead removeall root", crash: true, call: removeAll("/"), op: "removeall", path: "/", is: stale},
		{name: "dead rename root", crash: true, call: rename("/", "x"), op: "rename", path: "/", newPath: "x", is: stale},
		{name: "dead rename onto root", crash: true, call: rename("x", "/"), op: "rename", path: "x", newPath: "/", is: stale},

		// A dead incarnation: each FS method's stale error (Stat is TestSimDirectModeStale).
		{name: "dead open", crash: true, call: func(fsys fsx.FS, _ fsx.File) error { return opened(fsys.Open("a")) },
			op: "open", path: "a", is: stale},
		{name: "dead create", crash: true, call: func(fsys fsx.FS, _ fsx.File) error { return opened(fsys.Create("a")) },
			op: "open", path: "a", is: stale},
		{name: "dead openfile", crash: true, call: openFile("a", os.O_RDWR|os.O_CREATE), op: "open", path: "a", is: stale},
		{name: "dead remove", crash: true, call: func(fsys fsx.FS, _ fsx.File) error { return fsys.Remove("a") },
			op: "remove", path: "a", is: stale},
		{name: "dead removeall", crash: true, call: removeAll("a"), op: "removeall", path: "a", is: stale},
		{name: "dead rename", crash: true, call: rename("a", "b"), op: "rename", path: "a", newPath: "b", is: stale},
		{name: "dead mkdir", crash: true, call: func(fsys fsx.FS, _ fsx.File) error { return fsys.Mkdir("d", 0o755) },
			op: "mkdir", path: "d", is: stale},
		{name: "dead mkdirall", crash: true, call: mkdirAll("d/e"), op: "mkdir", path: "d/e", is: stale},
		{name: "dead readdir", crash: true, call: func(fsys fsx.FS, _ fsx.File) error {
			_, err := fsys.ReadDir("/")
			return err
		}, op: "readdir", path: "/", is: stale},
		{name: "dead syncdir", crash: true, call: func(fsys fsx.FS, _ fsx.File) error { return fsys.SyncDir("/") },
			op: "syncdir", path: "/", is: stale},
	}
	// A dead incarnation: each File method's stale error, with the handle's name.
	for _, m := range []struct {
		op   string
		call func(fsx.FS, fsx.File) error
	}{
		{"read", read},
		{"write", func(_ fsx.FS, h fsx.File) error {
			_, err := h.Write([]byte("x"))
			return err
		}},
		{"readat", func(_ fsx.FS, h fsx.File) error {
			_, err := h.ReadAt(make([]byte, 1), 0)
			return err
		}},
		{"writeat", func(_ fsx.FS, h fsx.File) error {
			_, err := h.WriteAt([]byte("x"), 0)
			return err
		}},
		{"seek", func(_ fsx.FS, h fsx.File) error {
			_, err := h.Seek(0, io.SeekStart)
			return err
		}},
		{"truncate", func(_ fsx.FS, h fsx.File) error { return h.Truncate(0) }},
		{"stat", func(_ fsx.FS, h fsx.File) error {
			_, err := h.Stat()
			return err
		}},
		{"sync", func(_ fsx.FS, h fsx.File) error { return h.Sync() }},
		{"close", func(_ fsx.FS, h fsx.File) error { return h.Close() }},
	} {
		rules = append(rules, rule{name: "dead file " + m.op, setup: handle(os.O_RDWR, false), crash: true, call: m.call,
			op: m.op, path: "a", is: stale})
	}
	for _, tc := range rules {
		t.Run(tc.name, func(t *testing.T) {
			v := newVolume(t)
			fsys := fsx.Sim(v)
			var h fsx.File
			if tc.setup != nil {
				var err error
				if h, err = tc.setup(fsys); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			if tc.crash {
				v.Node().Crash()
			}
			err := tc.call(fsys, h)
			var inner error
			switch tc.op {
			case "":
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
			case "rename":
				var le *os.LinkError
				if !errors.As(err, &le) || le.Op != "rename" || le.Old != tc.path || le.New != tc.newPath {
					t.Fatalf("got %T %v, want *os.LinkError rename %q %q", err, err, tc.path, tc.newPath)
				}
				inner = le.Err
			default:
				var pe *fs.PathError
				if !errors.As(err, &pe) || pe.Op != tc.op || pe.Path != tc.path {
					t.Fatalf("got %T %v, want *fs.PathError %s %q", err, err, tc.op, tc.path)
				}
				inner = pe.Err
			}
			for _, target := range tc.is {
				if !errors.Is(err, target) {
					t.Fatalf("got %v, want errors.Is %v", err, target)
				}
			}
			if tc.plain && inner != tc.is[0] {
				t.Fatalf("Err = %T %v, want exactly %v", inner, inner, tc.is[0])
			}
			if tc.after != nil {
				if err := tc.after(fsys); err != nil {
					t.Fatalf("after: %v", err)
				}
			}
		})
	}
}

// TestSimHandleCheckOrder runs the rows of TestOSHandleCheckOrder on Sim, so that both
// implementations agree (FSX-061), and the rows on a dead incarnation, where the handle checks still
// come first (FSX-011).
func TestSimHandleCheckOrder(t *testing.T) {
	testHandleCheckOrder(t, func(t *testing.T) (fsx.FS, func()) {
		v := newVolume(t)
		return fsx.Sim(v), func() { v.Node().Crash() }
	})
}
func populate(t *testing.T, fsys fsx.FS) {
	t.Helper()
	if err := fsx.WriteFile(fsys, "a", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteFile(fsys, "d/b", []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestATFSX09_IOFSPassesFstest(t *testing.T) {
	for name, fsys := range map[string]fsx.FS{"OS": fsx.OS(t.TempDir()), "Sim": fsx.Sim(newVolume(t))} {
		t.Run(name, func(t *testing.T) {
			populate(t, fsys)
			if err := fstest.TestFS(fsx.IOFS(fsys), "a", "d/b"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestATFSX10_IOFSRejectsInvalidPaths(t *testing.T) {
	iofs := fsx.IOFS(fsx.Sim(newVolume(t)))
	for _, name := range []string{"../a", "/a"} {
		if _, err := iofs.Open(name); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("Open(%q) = %v, want fs.ErrInvalid", name, err)
		}
	}
}

// TestIOFSShape checks the parts of FSX-075 that fstest.TestFS treats as optional.
func TestIOFSShape(t *testing.T) {
	fsys := fsx.Sim(newVolume(t))
	populate(t, fsys)
	iofs := fsx.IOFS(fsys)
	statFS, ok1 := iofs.(fs.StatFS)
	readDirFS, ok2 := iofs.(fs.ReadDirFS)
	readFileFS, ok3 := iofs.(fs.ReadFileFS)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("IOFS: StatFS %v, ReadDirFS %v, ReadFileFS %v", ok1, ok2, ok3)
	}
	for op, call := range map[string]func(string) error{
		"open":     func(n string) error { _, err := iofs.Open(n); return err },
		"stat":     func(n string) error { _, err := statFS.Stat(n); return err },
		"readdir":  func(n string) error { _, err := readDirFS.ReadDir(n); return err },
		"readfile": func(n string) error { _, err := readFileFS.ReadFile(n); return err },
	} {
		err := call("a/../a")
		var pe *fs.PathError
		if !errors.As(err, &pe) || pe.Op != op || pe.Path != "a/../a" || pe.Err != fs.ErrInvalid {
			t.Errorf("%s(a/../a) = %#v, want *fs.PathError{%s, a/../a, fs.ErrInvalid}", op, err, op)
		}
	}
	if info, err := statFS.Stat("."); err != nil || info.Name() != "." || !info.IsDir() {
		t.Fatalf("Stat(.) = %v, %v; want the root named .", info, err)
	}
	if data, err := readFileFS.ReadFile("d/b"); err != nil || string(data) != "2" {
		t.Fatalf("ReadFile(d/b) = %q, %v", data, err)
	}
	f, err := iofs.Open("a")
	if err != nil {
		t.Fatal(err)
	}
	ra, ok1 := f.(io.ReaderAt)
	sk, ok2 := f.(io.Seeker)
	if !ok1 || !ok2 {
		t.Fatalf("file: ReaderAt %v, Seeker %v", ok1, ok2)
	}
	b := make([]byte, 1)
	if n, err := ra.ReadAt(b, 0); n != 1 || err != nil || b[0] != '1' {
		t.Fatalf("ReadAt = %d, %v, %q", n, err, b)
	}
	if off, err := sk.Seek(0, io.SeekEnd); off != 1 || err != nil {
		t.Fatalf("Seek(0, SeekEnd) = %d, %v", off, err)
	}
	f.Close()
	root, err := iofs.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if info, err := root.Stat(); err != nil || info.Name() != "." {
		t.Fatalf("Open(.).Stat() = %v, %v; want the name .", info, err)
	}
	dir, ok := root.(fs.ReadDirFile)
	if !ok {
		t.Fatal("Open(.) is not an fs.ReadDirFile")
	}
	names := func(ents []fs.DirEntry) string {
		var s []string
		for _, e := range ents {
			s = append(s, e.Name())
		}
		return strings.Join(s, ",")
	}
	if ents, err := dir.ReadDir(1); names(ents) != "a" || err != nil {
		t.Fatalf("ReadDir(1) = [%s], %v; want [a], nil", names(ents), err)
	}
	if err := fsx.WriteFile(fsys, "c", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if ents, err := dir.ReadDir(0); names(ents) != "d" || err != nil {
		t.Fatalf("ReadDir(0) = [%s], %v; want [d], nil (the entries are loaded once)", names(ents), err)
	}
	if ents, err := dir.ReadDir(1); len(ents) != 0 || err != io.EOF {
		t.Fatalf("ReadDir(1) at the end = [%s], %v; want [], io.EOF", names(ents), err)
	}
	if ents, err := dir.ReadDir(-1); len(ents) != 0 || err != nil {
		t.Fatalf("ReadDir(-1) at the end = [%s], %v; want [], nil", names(ents), err)
	}
	if sub, err := iofs.Open("d"); err != nil {
		t.Fatal(err)
	} else {
		if info, err := sub.Stat(); err != nil || info.Name() != "d" {
			t.Fatalf("Open(d).Stat() = %v, %v; want the name d", info, err)
		}
		sub.Close()
	}
}
