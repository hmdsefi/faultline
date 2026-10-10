// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

import (
	"path"
	"strings"
)

// clean validates p (DSK-008) and returns path.Clean(p). An invalid path returns a PathError
// with the argument as given.
func clean(op, p string) (string, error) {
	if p == "" || p[0] != '/' || strings.IndexByte(p, 0) >= 0 {
		return "", pathErr(op, p, ErrInvalid)
	}
	return path.Clean(p), nil
}

// split returns the components of a cleaned path; "/" has none.
func split(p string) []string {
	if p == "/" {
		return nil
	}
	return strings.Split(p[1:], "/")
}

// base returns the base name of a cleaned path; "/" for the root.
func base(p string) string { return path.Base(p) }

// child returns the entry named name of directory n in the durable or visible namespace.
func (n *inode) child(name string, durable bool) *inode {
	if durable {
		return n.durable[name]
	}
	return n.entries[name]
}

// walkDir follows comps from root; every component must be a directory (DSK-009).
func walkDir(root *inode, comps []string, durable bool) (*inode, error) {
	cur := root
	for _, c := range comps {
		next := cur.child(c, durable)
		if next == nil {
			return nil, ErrNotExist
		}
		if !next.dir {
			return nil, ErrNotDir
		}
		cur = next
	}
	return cur, nil
}

// lookup resolves the cleaned path p in the visible or durable namespace. Errors are the
// unwrapped sentinels ErrNotExist and ErrNotDir.
func (v *Volume) lookup(p string, durable bool) (*inode, error) {
	if p == "/" {
		return v.root, nil
	}
	comps := split(p)
	dir, err := walkDir(v.root, comps[:len(comps)-1], durable)
	if err != nil {
		return nil, err
	}
	n := dir.child(comps[len(comps)-1], durable)
	if n == nil {
		return nil, ErrNotExist
	}
	return n, nil
}

// parent resolves the visible parent directory of the cleaned path p != "/" and returns it with
// the final component.
func (v *Volume) parent(p string) (*inode, string, error) {
	comps := split(p)
	dir, err := walkDir(v.root, comps[:len(comps)-1], false)
	if err != nil {
		return nil, "", err
	}
	return dir, comps[len(comps)-1], nil
}
