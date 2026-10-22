// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strconv"
	"syscall"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

type simOptions struct {
	d   Driver
	inc uint32
	set bool
}

// SimOption configures Sim.
type SimOption func(*simOptions)

// WithDriver puts the FS in driver mode for incarnation inc of the volume's node.
// It panics if d is nil.
func WithDriver(d Driver, inc uint32) SimOption {
	if d == nil {
		panic("fsx: WithDriver: nil Driver")
	}
	return func(o *simOptions) { o.d, o.inc, o.set = d, inc, true }
}

// Sim returns an FS on v. Without WithDriver it binds to the current incarnation of v.Node() and
// must be used on the kernel goroutine. It panics if v is nil.
func Sim(v *simdisk.Volume, opts ...SimOption) FS {
	if v == nil {
		panic("fsx: Sim: nil *simdisk.Volume")
	}
	var o simOptions
	for _, f := range opts {
		f(&o)
	}
	s := &simFS{v: v, n: v.Node(), d: o.d, inc: o.inc}
	if !o.set {
		s.inc = s.n.Incarnation()
	}
	return s
}

type simFS struct {
	v   *simdisk.Volume
	n   *kernel.Node
	d   Driver // nil: direct mode
	inc uint32
}

// errSkip is returned by a FSX-030 check whose result is (0, nil) with no operation and no sample.
var errSkip = errors.New("fsx: skip")

// frame runs one operation (FSX-011). check runs FSX-030's handle checks first; run is the simdisk
// work; class 0 means the operation is never sampled (Seek, File.Stat); stale is the error for a
// dead incarnation. In driver mode it unlocks on every return path instead of deferring Unlock:
// on the driver goroutine WaitUnpaused unlocks before it panics.
func (s *simFS) frame(class simdisk.Op, op, cleanPath string, stale error, check, run func() error) error {
	if s.d == nil { // direct mode
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if s.n.State() == kernel.NodeDown || s.n.Incarnation() != s.inc {
			return stale
		}
		return run()
	}
	d, id := s.d, s.n.ID()
	d.Lock()
	if check != nil {
		if err := check(); err != nil {
			d.Unlock()
			return err
		}
	}
	if !d.Alive(id, s.inc) {
		d.Unlock()
		return stale
	}
	if s.n.State() == kernel.NodePaused && !d.WaitUnpaused(id, s.inc) { // FSX-012
		d.Unlock()
		return stale
	}
	d.SyncClock() // records get the current virtual time
	err := run()
	var lat time.Duration
	if class != 0 {
		if lat = s.v.SampleLatency(class); lat > 0 { // FSX-050/051
			s.n.Sim().Emit(kernel.Record{Node: id, Inc: s.inc, Kind: "fsx.latency",
				Text: op + " " + cleanPath + ": " + lat.String(),
				Attrs: []kernel.Attr{
					{Key: "op", Value: op}, {Key: "path", Value: cleanPath},
					{Key: "class", Value: class.String()},
					{Key: "latency_ns", Value: strconv.FormatInt(int64(lat), 10)},
				}})
		}
	}
	d.Unlock()
	if lat > 0 && !d.OnDriver() {
		time.Sleep(lat)
	}
	return err
}

func (s *simFS) pathStale(op, name string) error {
	return &fs.PathError{Op: op, Path: name, Err: errStale}
}

func (s *simFS) Open(name string) (File, error) { return s.OpenFile(name, os.O_RDONLY, 0) }

func (s *simFS) Create(name string) (File, error) {
	return s.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

// OpenFile is FSX-020.
func (s *simFS) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	p, err := clean("open", name)
	if err != nil {
		return nil, err
	}
	var h *simFile
	err = s.frame(simdisk.OpMeta, "open", p, s.pathStale("open", name), nil, func() error {
		var err error
		h, err = s.open(name, p, flag)
		return err
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s *simFS) open(name, p string, flag int) (*simFile, error) {
	plain := func(errno syscall.Errno) error { return &fs.PathError{Op: "open", Path: name, Err: errno} }
	acc := flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR)
	if acc != os.O_RDONLY && acc != os.O_WRONLY && acc != os.O_RDWR {
		return nil, plain(syscall.EINVAL)
	}
	create, excl, trunc := flag&os.O_CREATE != 0, flag&os.O_EXCL != 0, flag&os.O_TRUNC != 0
	info, err := s.v.Stat(p)
	switch {
	case err == nil && info.IsDir:
		if create && excl {
			return nil, plain(syscall.EEXIST)
		}
		if acc != os.O_RDONLY || trunc || create {
			return nil, plain(syscall.EISDIR)
		}
		return &simFile{fs: s, name: name, path: p, dir: true, acc: acc, flag: flag}, nil
	case err == nil:
		if create && excl {
			return nil, plain(syscall.EEXIST)
		}
		f, err := s.v.Open(p)
		if err != nil {
			return nil, pathErr("open", name, err)
		}
		if trunc && acc != os.O_RDONLY {
			if err := f.Truncate(0); err != nil {
				f.Close()
				return nil, pathErr("open", name, err)
			}
		}
		return &simFile{fs: s, name: name, path: p, f: f, acc: acc, flag: flag}, nil
	case errors.Is(err, simdisk.ErrNotExist):
		if !create {
			return nil, pathErr("open", name, err)
		}
		f, err := s.v.Create(p)
		if err != nil {
			return nil, pathErr("open", name, err)
		}
		return &simFile{fs: s, name: name, path: p, f: f, acc: acc, flag: flag}, nil
	default:
		return nil, pathErr("open", name, err)
	}
}

// Remove is FSX-021.
func (s *simFS) Remove(name string) error {
	p, err := clean("remove", name)
	if err != nil {
		return err
	}
	return s.frame(simdisk.OpMeta, "remove", p, s.pathStale("remove", name), nil, func() error {
		return pathErr("remove", name, s.v.Remove(p))
	})
}

// RemoveAll is FSX-022.
func (s *simFS) RemoveAll(name string) error {
	if name == "" {
		return nil
	}
	p, err := clean("removeall", name)
	if err != nil {
		return err
	}
	return s.frame(simdisk.OpMeta, "removeall", p, s.pathStale("removeall", name), nil, func() error {
		if p == "/" {
			return &fs.PathError{Op: "removeall", Path: name, Err: syscall.EINVAL}
		}
		info, err := s.v.Stat(p)
		if errors.Is(err, simdisk.ErrNotExist) {
			return nil
		}
		if err != nil {
			return pathErr("removeall", name, err)
		}
		if !info.IsDir {
			return pathErr("removeall", name, s.v.Remove(p))
		}
		return s.removeTree(name, p)
	})
}

// removeTree removes p depth-first, post-order, children in ReadDir order (FSX-022).
func (s *simFS) removeTree(name, p string) error {
	ents, err := s.v.ReadDir(p)
	if err != nil {
		return pathErr("removeall", name, err)
	}
	for _, e := range ents {
		c := path.Join(p, e.Name)
		if e.IsDir {
			if err := s.removeTree(name, c); err != nil {
				return err
			}
		} else if err := s.v.Remove(c); err != nil {
			return pathErr("removeall", name, err)
		}
	}
	return pathErr("removeall", name, s.v.Remove(p))
}

// Rename is FSX-023.
func (s *simFS) Rename(oldname, newname string) error {
	le := func(err error) error { return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: err} }
	oldp, err := clean("rename", oldname)
	if err != nil {
		return le(errors.Unwrap(err))
	}
	newp, err := clean("rename", newname)
	if err != nil {
		return le(errors.Unwrap(err))
	}
	return s.frame(simdisk.OpMeta, "rename", oldp, le(errStale), nil, func() error {
		if oldp == "/" || newp == "/" {
			return le(syscall.EINVAL)
		}
		if info, err := s.v.Stat(newp); err == nil && info.IsDir {
			if _, err := s.v.Stat(oldp); err != nil {
				return le(mapSentinel(err))
			}
			return le(syscall.EEXIST)
		}
		return linkErr(oldname, newname, s.v.Rename(oldp, newp))
	})
}

// Mkdir and MkdirAll are FSX-024.
func (s *simFS) Mkdir(name string, perm fs.FileMode) error {
	p, err := clean("mkdir", name)
	if err != nil {
		return err
	}
	return s.frame(simdisk.OpMeta, "mkdir", p, s.pathStale("mkdir", name), nil, func() error {
		return pathErr("mkdir", name, s.v.Mkdir(p))
	})
}

func (s *simFS) MkdirAll(name string, perm fs.FileMode) error {
	p, err := clean("mkdir", name)
	if err != nil {
		return err
	}
	return s.frame(simdisk.OpMeta, "mkdir", p, s.pathStale("mkdir", name), nil, func() error {
		if p == "/" {
			return nil
		}
		return pathErr("mkdir", name, s.v.MkdirAll(p))
	})
}

// Stat is FSX-025.
func (s *simFS) Stat(name string) (fs.FileInfo, error) {
	p, err := clean("stat", name)
	if err != nil {
		return nil, err
	}
	var info fs.FileInfo
	err = s.frame(simdisk.OpMeta, "stat", p, s.pathStale("stat", name), nil, func() error {
		i, err := s.v.Stat(p)
		if err != nil {
			return pathErr("stat", name, err)
		}
		info = simInfo{name: i.Name, size: i.Size, dir: i.IsDir}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// ReadDir is FSX-026.
func (s *simFS) ReadDir(name string) ([]fs.DirEntry, error) {
	p, err := clean("readdir", name)
	if err != nil {
		return nil, err
	}
	var out []fs.DirEntry
	err = s.frame(simdisk.OpMeta, "readdir", p, s.pathStale("readdir", name), nil, func() error {
		ents, err := s.v.ReadDir(p)
		if err != nil {
			return pathErr("readdir", name, err)
		}
		out = make([]fs.DirEntry, 0, len(ents))
		for _, e := range ents {
			out = append(out, simDirEntry{simInfo{name: e.Name, size: e.Size, dir: e.IsDir}})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SyncDir is FSX-027.
func (s *simFS) SyncDir(name string) error {
	p, err := clean("syncdir", name)
	if err != nil {
		return err
	}
	return s.frame(simdisk.OpSync, "syncdir", p, s.pathStale("syncdir", name), nil, func() error {
		info, err := s.v.Stat(p)
		if err != nil {
			return pathErr("syncdir", name, err)
		}
		if !info.IsDir {
			return &fs.PathError{Op: "syncdir", Path: name, Err: syscall.ENOTDIR}
		}
		return pathErr("syncdir", name, s.v.SyncDir(p))
	})
}
