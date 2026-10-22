// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
)

// OS returns an FS confined to the directory root (os.OpenRoot). If root cannot be opened, every
// method returns an *fs.PathError wrapping the open error. The result also implements io.Closer.
func OS(root string) FS {
	r, err := os.OpenRoot(root)
	return &osFS{root: r, err: err}
}

type osFS struct {
	root *os.Root
	err  error // os.OpenRoot error
}

func (o *osFS) Close() error {
	if o.root == nil {
		return o.err
	}
	return o.root.Close()
}

// rel cleans name (FSX-001) and returns the os.Root path.
func (o *osFS) rel(op, name string) (string, error) {
	p, err := clean(op, name)
	if err != nil {
		return "", err
	}
	if o.err != nil {
		return "", &fs.PathError{Op: op, Path: name, Err: o.err}
	}
	if p == "/" {
		return ".", nil
	}
	return strings.TrimPrefix(p, "/"), nil
}

// rewrite gives an os error FSX-002's op and the name as given (FSX-060).
func rewrite(op, name string, err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &fs.PathError{Op: op, Path: name, Err: pe.Err}
	}
	return &fs.PathError{Op: op, Path: name, Err: err}
}

func (o *osFS) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	r, err := o.rel("open", name)
	if err != nil {
		return nil, err
	}
	f, err := o.root.OpenFile(r, flag, perm)
	if err != nil {
		return nil, rewrite("open", name, err)
	}
	return &osFile{f: f, name: name, flag: flag}, nil
}

func (o *osFS) Open(name string) (File, error) { return o.OpenFile(name, os.O_RDONLY, 0) }

func (o *osFS) Create(name string) (File, error) {
	return o.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (o *osFS) Remove(name string) error {
	r, err := o.rel("remove", name)
	if err != nil {
		return err
	}
	return rewrite("remove", name, o.root.Remove(r))
}

func (o *osFS) RemoveAll(name string) error {
	if name == "" {
		return nil
	}
	r, err := o.rel("removeall", name)
	if err != nil {
		return err
	}
	return rewrite("removeall", name, o.root.RemoveAll(r))
}

func (o *osFS) Rename(oldname, newname string) error {
	or, err := o.rel("rename", oldname)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: errors.Unwrap(err)}
	}
	nr, err := o.rel("rename", newname)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: errors.Unwrap(err)}
	}
	if or == "." || nr == "." {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: syscall.EINVAL}
	}
	// An existing directory at newname: the check os.Root.Rename makes itself from Go 1.26.5 on
	// (os.Root.Rename's rule, which compares last path elements), repeated here for earlier Go
	// versions.
	if ninfo, serr := o.root.Lstat(nr); serr == nil && ninfo.IsDir() {
		oinfo, serr := o.root.Lstat(or)
		if serr != nil {
			var pe *fs.PathError
			if errors.As(serr, &pe) {
				serr = pe.Err
			}
			return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: serr}
		}
		if path.Base(or) == path.Base(nr) || !os.SameFile(ninfo, oinfo) {
			return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: syscall.EEXIST}
		}
	}
	if err := o.root.Rename(or, nr); err != nil {
		var le *os.LinkError
		if errors.As(err, &le) {
			err = le.Err
		}
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: err}
	}
	return nil
}

func (o *osFS) Mkdir(name string, perm fs.FileMode) error {
	r, err := o.rel("mkdir", name)
	if err != nil {
		return err
	}
	return rewrite("mkdir", name, o.root.Mkdir(r, perm))
}

func (o *osFS) MkdirAll(name string, perm fs.FileMode) error {
	r, err := o.rel("mkdir", name)
	if err != nil {
		return err
	}
	err = o.root.MkdirAll(r, perm)
	if errors.Is(err, syscall.EEXIST) { // FSX-061
		if info, serr := o.root.Stat(r); serr == nil && !info.IsDir() {
			return &fs.PathError{Op: "mkdir", Path: name, Err: syscall.ENOTDIR}
		}
	}
	return rewrite("mkdir", name, err)
}

func (o *osFS) Stat(name string) (fs.FileInfo, error) {
	r, err := o.rel("stat", name)
	if err != nil {
		return nil, err
	}
	info, err := o.root.Stat(r)
	return info, rewrite("stat", name, err)
}

// ReadDir is FSX-061: Open, ReadDir(-1), Close, sort by name.
func (o *osFS) ReadDir(name string) ([]fs.DirEntry, error) {
	r, err := o.rel("readdir", name)
	if err != nil {
		return nil, err
	}
	f, err := o.root.Open(r)
	if err != nil {
		return nil, rewrite("readdir", name, err)
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	if err != nil {
		return nil, rewrite("readdir", name, err)
	}
	slices.SortFunc(ents, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return ents, nil
}

// SyncDir is FSX-061: Stat (a file is ENOTDIR), then Open, Sync, Close.
func (o *osFS) SyncDir(name string) error {
	r, err := o.rel("syncdir", name)
	if err != nil {
		return err
	}
	info, err := o.root.Stat(r)
	if err != nil {
		return rewrite("syncdir", name, err)
	}
	if !info.IsDir() {
		return &fs.PathError{Op: "syncdir", Path: name, Err: syscall.ENOTDIR}
	}
	f, err := o.root.Open(r)
	if err != nil {
		return rewrite("syncdir", name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return rewrite("syncdir", name, err)
	}
	return rewrite("syncdir", name, f.Close())
}

// osFile wraps *os.File (FSX-062).
type osFile struct {
	f    *os.File
	name string
	flag int
}

func (h *osFile) Name() string { return h.name }

// check runs FSX-030's closed, access-mode and directory checks for the inputs os checks in
// another order or not at all. Stat tells a closed handle and a directory; bad is the access mode
// that gives EBADF. A directory handle is always O_RDONLY, so WriteAt's EBADF comes first.
func (h *osFile) check(op string, bad int) error {
	info, err := h.f.Stat()
	if err != nil {
		return rewrite(op, h.name, err)
	}
	if h.flag&(os.O_RDONLY|os.O_WRONLY|os.O_RDWR) == bad {
		return &fs.PathError{Op: op, Path: h.name, Err: syscall.EBADF}
	}
	if info.IsDir() {
		return &fs.PathError{Op: op, Path: h.name, Err: syscall.EISDIR}
	}
	return nil
}

func (h *osFile) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, h.check("read", os.O_WRONLY)
	}
	n, err := h.f.Read(b)
	return n, rewrite("read", h.name, err)
}

func (h *osFile) Write(b []byte) (int, error) {
	n, err := h.f.Write(b)
	return n, rewrite("write", h.name, err)
}

func (h *osFile) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 || len(b) == 0 {
		if err := h.check("readat", os.O_WRONLY); err != nil {
			return 0, err
		}
		if off < 0 {
			return 0, &fs.PathError{Op: "readat", Path: h.name, Err: syscall.EINVAL}
		}
		return 0, nil
	}
	n, err := h.f.ReadAt(b, off)
	return n, rewrite("readat", h.name, err)
}

func (h *osFile) WriteAt(b []byte, off int64) (int, error) {
	if h.flag&os.O_APPEND != 0 || off < 0 || len(b) == 0 {
		if err := h.check("writeat", os.O_RDONLY); err != nil {
			return 0, err
		}
		if h.flag&os.O_APPEND != 0 {
			return 0, &fs.PathError{Op: "writeat", Path: h.name, Err: ErrWriteAtInAppendMode}
		}
		if off < 0 {
			return 0, &fs.PathError{Op: "writeat", Path: h.name, Err: syscall.EINVAL}
		}
		return 0, nil
	}
	n, err := h.f.WriteAt(b, off)
	return n, rewrite("writeat", h.name, err)
}

func (h *osFile) Seek(off int64, whence int) (int64, error) {
	n, err := h.f.Seek(off, whence)
	return n, rewrite("seek", h.name, err)
}

func (h *osFile) Stat() (fs.FileInfo, error) {
	info, err := h.f.Stat()
	return info, rewrite("stat", h.name, err)
}

func (h *osFile) Sync() error               { return rewrite("sync", h.name, h.f.Sync()) }
func (h *osFile) Truncate(size int64) error { return rewrite("truncate", h.name, h.f.Truncate(size)) }
func (h *osFile) Close() error              { return rewrite("close", h.name, h.f.Close()) }
