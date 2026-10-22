// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx_test

import (
	"errors"
	"io/fs"
	"os"
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
