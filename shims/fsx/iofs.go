// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx

import (
	"io"
	"io/fs"
)

// IOFS returns a read-only io/fs view of fsys. It implements fs.FS, fs.StatFS,
// fs.ReadDirFS and fs.ReadFileFS. The adapter never writes.
func IOFS(fsys FS) fs.FS { return ioFS{fsys} }

type ioFS struct{ fsys FS }

// name validates an io/fs name; "." maps to the root.
func (i ioFS) name(op, name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return "/", nil
	}
	return name, nil
}

func (i ioFS) Open(name string) (fs.File, error) {
	n, err := i.name("open", name)
	if err != nil {
		return nil, err
	}
	f, err := i.fsys.Open(n)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		return &ioDir{f: f, fsys: i.fsys, name: n, root: name == "."}, nil
	}
	return &ioFile{f: f}, nil
}

func (i ioFS) Stat(name string) (fs.FileInfo, error) {
	n, err := i.name("stat", name)
	if err != nil {
		return nil, err
	}
	info, err := i.fsys.Stat(n)
	if err == nil && name == "." {
		return rootInfo{info}, nil
	}
	return info, err
}

func (i ioFS) ReadDir(name string) ([]fs.DirEntry, error) {
	n, err := i.name("readdir", name)
	if err != nil {
		return nil, err
	}
	return i.fsys.ReadDir(n)
}

func (i ioFS) ReadFile(name string) ([]byte, error) {
	n, err := i.name("readfile", name)
	if err != nil {
		return nil, err
	}
	return ReadFile(i.fsys, n)
}

// rootInfo reports the root's name as "." (FSX-075).
type rootInfo struct{ fs.FileInfo }

func (rootInfo) Name() string { return "." }

// ioFile is a regular file: fs.File, io.Seeker, io.ReaderAt.
type ioFile struct{ f File }

func (x *ioFile) Stat() (fs.FileInfo, error)                { return x.f.Stat() }
func (x *ioFile) Read(b []byte) (int, error)                { return x.f.Read(b) }
func (x *ioFile) Close() error                              { return x.f.Close() }
func (x *ioFile) Seek(off int64, whence int) (int64, error) { return x.f.Seek(off, whence) }
func (x *ioFile) ReadAt(b []byte, off int64) (int, error)   { return x.f.ReadAt(b, off) }

// ioDir is a directory: fs.ReadDirFile.
type ioDir struct {
	f      File
	fsys   FS
	name   string
	root   bool
	ents   []fs.DirEntry
	loaded bool
	pos    int
}

func (d *ioDir) Stat() (fs.FileInfo, error) {
	info, err := d.f.Stat()
	if err == nil && d.root {
		return rootInfo{info}, nil
	}
	return info, err
}

func (d *ioDir) Read(b []byte) (int, error) { return d.f.Read(b) }
func (d *ioDir) Close() error               { return d.f.Close() }

// ReadDir loads fsys.ReadDir once; n <= 0 returns the rest and nil, n > 0 up to n entries and
// io.EOF when none remain.
func (d *ioDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if !d.loaded {
		ents, err := d.fsys.ReadDir(d.name)
		if err != nil {
			return nil, err
		}
		d.ents, d.loaded = ents, true
	}
	rest := d.ents[d.pos:]
	if n <= 0 {
		d.pos = len(d.ents)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(rest))
	d.pos += n
	return rest[:n], nil
}
