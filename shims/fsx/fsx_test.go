// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

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
	fsys := fsx.OS(t.TempDir())
	if cl, ok := fsys.(io.Closer); ok {
		t.Cleanup(func() { _ = cl.Close() })
	}
	if err := fsx.WriteFile(fsys, "f", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
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
	for _, tc := range []struct {
		desc   string
		name   string
		flag   int
		closed bool
		op     string
		call   func(fsx.File) (int, error)
		want   error // nil means 0, nil
	}{
		{"Read(nil) closed", "f", os.O_RDONLY, true, "read", read(nil), fs.ErrClosed},
		{"Read(nil) O_WRONLY", "f", os.O_WRONLY, false, "read", read(nil), syscall.EBADF},
		{"Read(nil) directory", "d", os.O_RDONLY, false, "read", read(nil), syscall.EISDIR},
		{"Read(nil)", "f", os.O_RDONLY, false, "read", read(nil), nil},
		{"Write(nil) closed", "f", os.O_WRONLY, true, "write", write(nil), fs.ErrClosed},
		{"Write(nil) O_RDONLY", "f", os.O_RDONLY, false, "write", write(nil), syscall.EBADF},
		{"Write(nil)", "f", os.O_WRONLY, false, "write", write(nil), nil},
		{"ReadAt(b, -1) closed", "f", os.O_RDONLY, true, "readat", readAt(b, -1), fs.ErrClosed},
		{"ReadAt(b, -1) O_WRONLY", "f", os.O_WRONLY, false, "readat", readAt(b, -1), syscall.EBADF},
		{"ReadAt(b, -1) directory", "d", os.O_RDONLY, false, "readat", readAt(b, -1), syscall.EISDIR},
		{"ReadAt(b, -1)", "f", os.O_RDONLY, false, "readat", readAt(b, -1), syscall.EINVAL},
		{"ReadAt(nil, -1)", "f", os.O_RDONLY, false, "readat", readAt(nil, -1), syscall.EINVAL},
		{"ReadAt(nil, 0) O_WRONLY", "f", os.O_WRONLY, false, "readat", readAt(nil, 0), syscall.EBADF},
		{"ReadAt(nil, 0) directory", "d", os.O_RDONLY, false, "readat", readAt(nil, 0), syscall.EISDIR},
		{"ReadAt(nil, 0)", "f", os.O_RDONLY, false, "readat", readAt(nil, 0), nil},
		{"WriteAt(b, -1) closed", "f", os.O_RDWR, true, "writeat", writeAt(b, -1), fs.ErrClosed},
		{"WriteAt(b, 0) closed O_APPEND", "f", os.O_WRONLY | os.O_APPEND, true, "writeat", writeAt(b, 0), fs.ErrClosed},
		{"WriteAt(b, 0) O_RDONLY|O_APPEND", "f", os.O_RDONLY | os.O_APPEND, false, "writeat", writeAt(b, 0), syscall.EBADF},
		{"WriteAt(b, -1) O_RDONLY", "f", os.O_RDONLY, false, "writeat", writeAt(b, -1), syscall.EBADF},
		{"WriteAt(b, -1) directory", "d", os.O_RDONLY, false, "writeat", writeAt(b, -1), syscall.EBADF},
		{"WriteAt(nil, 0) O_RDONLY", "f", os.O_RDONLY, false, "writeat", writeAt(nil, 0), syscall.EBADF},
		{"WriteAt(b, -1) O_APPEND", "f", os.O_WRONLY | os.O_APPEND, false, "writeat", writeAt(b, -1), fsx.ErrWriteAtInAppendMode},
		{"WriteAt(b, -1)", "f", os.O_RDWR, false, "writeat", writeAt(b, -1), syscall.EINVAL},
		{"WriteAt(nil, -1)", "f", os.O_RDWR, false, "writeat", writeAt(nil, -1), syscall.EINVAL},
		{"WriteAt(nil, 0)", "f", os.O_RDWR, false, "writeat", writeAt(nil, 0), nil},
	} {
		f, err := fsys.OpenFile(tc.name, tc.flag, 0)
		if err != nil {
			t.Fatalf("%s: OpenFile: %v", tc.desc, err)
		}
		if tc.closed {
			if err := f.Close(); err != nil {
				t.Fatalf("%s: Close: %v", tc.desc, err)
			}
		}
		n, err := tc.call(f)
		if !tc.closed {
			_ = f.Close()
		}
		if tc.want == nil {
			if n != 0 || err != nil {
				t.Errorf("%s = %d, %v; want 0, nil", tc.desc, n, err)
			}
			continue
		}
		var pe *fs.PathError
		if n != 0 || !errors.As(err, &pe) || pe.Op != tc.op || pe.Path != tc.name || pe.Err != tc.want {
			t.Errorf("%s = %d, %#v; want 0, *fs.PathError{%s, %s, %v}", tc.desc, n, err, tc.op, tc.name, tc.want)
		}
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
