// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package fsx is a filesystem interface modelled on package os, with a real implementation (OS)
// and one on the simulated disk (Sim).
package fsx

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// FS is a filesystem. Names are slash-separated and relative to the FS root.
// Implementations are safe for concurrent use.
type FS interface {
	// OpenFile opens name with os flags (O_RDONLY, O_WRONLY, O_RDWR, O_APPEND, O_CREATE, O_EXCL,
	// O_SYNC, O_TRUNC). perm is used by OS when creating; Sim ignores it.
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	// Open is OpenFile(name, os.O_RDONLY, 0).
	Open(name string) (File, error)
	// Create is OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666).
	Create(name string) (File, error)
	// Remove removes a file or an empty directory.
	Remove(name string) error
	// RemoveAll removes name and everything below it. A missing name is not an error.
	RemoveAll(name string) error
	// Rename renames oldname to newname, replacing a file at newname (os.Rename semantics).
	Rename(oldname, newname string) error
	Mkdir(name string, perm fs.FileMode) error
	// MkdirAll creates name and missing parents; nil if name is already a directory.
	MkdirAll(name string, perm fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
	// ReadDir returns the entries of a directory sorted by name.
	ReadDir(name string) ([]fs.DirEntry, error)
	// SyncDir makes the directory's entries durable (fsync of the directory).
	SyncDir(name string) error
}

// File is an open file or directory. Methods are safe for concurrent use.
type File interface {
	io.Reader
	io.Writer
	io.ReaderAt
	io.WriterAt
	io.Seeker
	io.Closer
	// Name returns the name passed to OpenFile, unchanged.
	Name() string
	Stat() (fs.FileInfo, error)
	// Sync makes the file's written data durable (fsync). On a directory handle it is SyncDir.
	Sync() error
	Truncate(size int64) error
}

// Driver is the subset of the goroutine-mode driver contract that fsx uses.
type Driver interface {
	Lock()
	Unlock()
	Alive(node kernel.NodeID, inc uint32) bool
	SyncClock()
	WaitUnpaused(node kernel.NodeID, inc uint32) bool
	OnDriver() bool
}

// ErrWriteAtInAppendMode is the PathError.Err of WriteAt on a handle opened with O_APPEND.
var ErrWriteAtInAppendMode = errors.New("fsx: invalid use of WriteAt on file opened with O_APPEND")

// clean is FSX-001: "" → ENOENT, NUL → EINVAL, else path.Clean("/" + name).
func clean(op, name string) (string, error) {
	if name == "" {
		return "", &fs.PathError{Op: op, Path: name, Err: syscall.ENOENT}
	}
	if strings.IndexByte(name, 0) >= 0 {
		return "", &fs.PathError{Op: op, Path: name, Err: syscall.EINVAL}
	}
	return path.Clean("/" + name), nil
}

// simErr pairs an errno with a simdisk sentinel; errors.Is and errors.As see both (FSX-006).
type simErr struct {
	errno    syscall.Errno
	sentinel error
}

func (e simErr) Error() string   { return e.errno.Error() }
func (e simErr) Unwrap() []error { return []error{e.errno, e.sentinel} }

// errStale is the Err of operations on a dead incarnation (FSX-007).
var errStale = simErr{syscall.ESTALE, simdisk.ErrStale}

// mapSentinel maps a simdisk error (a *fs.PathError around a sentinel, DSK-037) to FSX-006's Err.
func mapSentinel(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	switch err {
	case simdisk.ErrNotExist:
		return simErr{syscall.ENOENT, simdisk.ErrNotExist}
	case simdisk.ErrExist:
		return simErr{syscall.EEXIST, simdisk.ErrExist}
	case simdisk.ErrInvalid:
		return simErr{syscall.EINVAL, simdisk.ErrInvalid}
	case simdisk.ErrIsDir:
		return simErr{syscall.EISDIR, simdisk.ErrIsDir}
	case simdisk.ErrNotDir:
		return simErr{syscall.ENOTDIR, simdisk.ErrNotDir}
	case simdisk.ErrNotEmpty:
		return simErr{syscall.ENOTEMPTY, simdisk.ErrNotEmpty}
	case simdisk.ErrIO:
		return simErr{syscall.EIO, simdisk.ErrIO}
	case simdisk.ErrNoSpace:
		return simErr{syscall.ENOSPC, simdisk.ErrNoSpace}
	case simdisk.ErrStale:
		return errStale
	case simdisk.ErrTooLarge:
		return simErr{syscall.EFBIG, simdisk.ErrTooLarge}
	case simdisk.ErrClosed:
		return fs.ErrClosed
	}
	return err
}

// pathErr wraps a simdisk error for FSX-005 (nil and io.EOF pass through).
func pathErr(op, name string, err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	return &fs.PathError{Op: op, Path: name, Err: mapSentinel(err)}
}

// linkErr wraps a simdisk Rename error (FSX-005).
func linkErr(oldname, newname string, err error) error {
	if err == nil {
		return nil
	}
	return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: mapSentinel(err)}
}
