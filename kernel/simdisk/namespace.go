// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Create creates the file, or truncates it to size 0 if it exists, and opens it read-write.
// The parent directory must exist.
func (v *Volume) Create(path string) (*File, error) {
	v.mustBeUp("Create")
	p, err := clean("create", path)
	if err != nil {
		return nil, err
	}
	if p == "/" {
		return nil, pathErr("create", p, ErrIsDir)
	}
	dir, name, err := v.parent(p)
	if err != nil {
		return nil, pathErr("create", p, err)
	}
	if f := dir.entries[name]; f != nil {
		if f.dir {
			return nil, pathErr("create", p, ErrIsDir)
		}
		op := dataOp{trunc: true, off: 0}
		v.used -= int64(len(f.cur))
		f.cur = apply(f.cur, op)
		f.pending = append(f.pending, op)
		v.emit("disk.create", "create "+p+" (truncated)", attr("path", p), attr("truncated", "true"))
		return v.open(f, p), nil
	}
	f := &inode{linked: true}
	v.record("create", mutation{dir: dir, name: name, target: f})
	v.emit("disk.create", "create "+p, attr("path", p), attr("truncated", "false"))
	return v.open(f, p), nil
}

// Open opens an existing file read-write.
func (v *Volume) Open(path string) (*File, error) {
	v.mustBeUp("Open")
	p, err := clean("open", path)
	if err != nil {
		return nil, err
	}
	f, err := v.lookup(p, false)
	if err != nil {
		return nil, pathErr("open", p, err)
	}
	if f.dir {
		return nil, pathErr("open", p, ErrIsDir)
	}
	return v.open(f, p), nil
}

// open returns a new handle bound to the current crash epoch and incarnation (DSK-028).
func (v *Volume) open(f *inode, p string) *File {
	return &File{v: v, f: f, name: p, epoch: v.epoch, inc: v.node.Incarnation()}
}

// Remove removes a file or an empty directory.
func (v *Volume) Remove(path string) error {
	v.mustBeUp("Remove")
	p, err := clean("remove", path)
	if err != nil {
		return err
	}
	if p == "/" {
		return pathErr("remove", p, ErrInvalid)
	}
	dir, name, err := v.parent(p)
	if err != nil {
		return pathErr("remove", p, err)
	}
	n := dir.entries[name]
	if n == nil {
		return pathErr("remove", p, ErrNotExist)
	}
	if n.dir && len(n.entries) > 0 {
		return pathErr("remove", p, ErrNotEmpty)
	}
	v.record("remove", mutation{dir: dir, name: name})
	if !n.dir {
		n.linked = false
		v.used -= int64(len(n.cur))
	}
	v.emit("disk.remove", "remove "+p, attr("path", p), attr("dir", strconv.FormatBool(n.dir)))
	return nil
}

// Rename renames oldpath to newpath, replacing a file or an empty directory at newpath.
func (v *Volume) Rename(oldpath, newpath string) error {
	v.mustBeUp("Rename")
	op, err := clean("rename", oldpath)
	if err != nil {
		return err
	}
	np, err := clean("rename", newpath)
	if err != nil {
		return err
	}
	if op == "/" {
		return pathErr("rename", op, ErrInvalid)
	}
	if np == "/" {
		return pathErr("rename", np, ErrInvalid)
	}
	odir, oname, err := v.parent(op)
	if err != nil {
		return pathErr("rename", op, err)
	}
	src := odir.entries[oname]
	if src == nil {
		return pathErr("rename", op, ErrNotExist)
	}
	ndir, nname, err := v.parent(np)
	if err != nil {
		return pathErr("rename", np, err)
	}
	if op == np {
		return nil
	}
	if src.dir && strings.HasPrefix(np, op+"/") {
		return pathErr("rename", np, ErrInvalid)
	}
	dst := ndir.entries[nname]
	if dst != nil {
		switch {
		case src.dir && !dst.dir:
			return pathErr("rename", np, ErrNotDir)
		case !src.dir && dst.dir:
			return pathErr("rename", np, ErrIsDir)
		case dst.dir && len(dst.entries) > 0:
			return pathErr("rename", np, ErrNotEmpty)
		}
	}
	v.record("rename", mutation{dir: odir, name: oname}, mutation{dir: ndir, name: nname, target: src})
	if dst != nil && !dst.dir {
		dst.linked = false
		v.used -= int64(len(dst.cur))
	}
	v.emit("disk.rename", "rename "+op+" -> "+np,
		attr("old_path", op), attr("new_path", np), attr("replaced", strconv.FormatBool(dst != nil)))
	return nil
}

// Mkdir creates a directory. The parent must exist.
func (v *Volume) Mkdir(path string) error {
	v.mustBeUp("Mkdir")
	p, err := clean("mkdir", path)
	if err != nil {
		return err
	}
	if p == "/" {
		return pathErr("mkdir", p, ErrExist)
	}
	dir, name, err := v.parent(p)
	if err != nil {
		return pathErr("mkdir", p, err)
	}
	if dir.entries[name] != nil {
		return pathErr("mkdir", p, ErrExist)
	}
	v.mkdir(dir, name, p)
	return nil
}

// mkdir records namespace op mkdir and emits disk.mkdir (DSK-014).
func (v *Volume) mkdir(dir *inode, name, p string) *inode {
	n := newDir()
	v.record("mkdir", mutation{dir: dir, name: name, target: n})
	v.emit("disk.mkdir", "mkdir "+p, attr("path", p))
	return n
}

// MkdirAll creates a directory and any missing parents. It returns nil if path is already a
// directory.
func (v *Volume) MkdirAll(path string) error {
	v.mustBeUp("MkdirAll")
	p, err := clean("mkdir", path)
	if err != nil {
		return err
	}
	cur, prefix := v.root, ""
	for _, c := range split(p) {
		prefix += "/" + c
		next := cur.entries[c]
		switch {
		case next == nil:
			next = v.mkdir(cur, c, prefix)
		case !next.dir:
			return pathErr("mkdir", p, ErrNotDir)
		}
		cur = next
	}
	return nil
}

// ReadDir returns the entries of a directory in the visible namespace, sorted by name.
func (v *Volume) ReadDir(path string) ([]Info, error) {
	p, err := clean("readdir", path)
	if err != nil {
		return nil, err
	}
	n, err := v.lookup(p, false)
	if err != nil {
		return nil, pathErr("readdir", p, err)
	}
	if !n.dir {
		return nil, pathErr("readdir", p, ErrNotDir)
	}
	names := slices.Sorted(maps.Keys(n.entries))
	out := make([]Info, 0, len(names))
	for _, name := range names {
		out = append(out, info(name, n.entries[name]))
	}
	return out, nil
}

// Stat describes a file or directory in the visible namespace.
func (v *Volume) Stat(path string) (Info, error) {
	p, err := clean("stat", path)
	if err != nil {
		return Info{}, err
	}
	n, err := v.lookup(p, false)
	if err != nil {
		return Info{}, pathErr("stat", p, err)
	}
	return info(base(p), n), nil
}

func info(name string, n *inode) Info {
	if n.dir {
		return Info{Name: name, IsDir: true}
	}
	return Info{Name: name, Size: int64(len(n.cur))}
}
