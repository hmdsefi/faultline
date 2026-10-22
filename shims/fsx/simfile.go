// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx

import (
	"io"
	"io/fs"
	"os"
	"path"
	"syscall"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// simInfo is FSX-025's FileInfo.
type simInfo struct {
	name string
	size int64
	dir  bool
}

func (i simInfo) Name() string { return i.name }
func (i simInfo) Size() int64  { return i.size }
func (i simInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i simInfo) ModTime() time.Time { return kernel.Epoch }
func (i simInfo) IsDir() bool        { return i.dir }
func (i simInfo) Sys() any           { return nil }

// simDirEntry is FSX-026's DirEntry.
type simDirEntry struct{ info simInfo }

func (e simDirEntry) Name() string { return e.info.name }
func (e simDirEntry) IsDir() bool  { return e.info.dir }
func (e simDirEntry) Type() fs.FileMode {
	if e.info.dir {
		return fs.ModeDir
	}
	return 0
}
func (e simDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }

// simFile is a Sim file or directory handle. Its state is guarded by the world lock in driver mode.
type simFile struct {
	fs     *simFS
	name   string        // as given
	path   string        // clean
	f      *simdisk.File // nil for directories
	dir    bool
	acc    int
	flag   int
	off    int64
	closed bool
}

func (h *simFile) Name() string { return h.name }

func (h *simFile) err(op string, err error) error {
	return &fs.PathError{Op: op, Path: h.name, Err: err}
}

func (h *simFile) stale(op string) error { return h.err(op, errStale) }

// Read is FSX-030/031.
func (h *simFile) Read(b []byte) (int, error) {
	var n int
	err := h.fs.frame(simdisk.OpRead, "read", h.path, h.stale("read"), func() error {
		switch {
		case h.closed:
			return h.err("read", fs.ErrClosed)
		case h.acc == os.O_WRONLY:
			return h.err("read", syscall.EBADF)
		case h.dir:
			return h.err("read", syscall.EISDIR)
		case len(b) == 0:
			return errSkip
		}
		return nil
	}, func() error {
		m, err := h.f.ReadAt(b, h.off)
		h.off += int64(m)
		n = m
		if err == io.EOF {
			if m > 0 {
				return nil
			}
			return io.EOF
		}
		return pathErr("read", h.name, err)
	})
	if err == errSkip {
		return 0, nil
	}
	return n, err
}

// writeClass is OpSync for O_SYNC handles, else OpWrite (FSX-050).
func (h *simFile) writeClass() simdisk.Op {
	if h.flag&os.O_SYNC != 0 {
		return simdisk.OpSync
	}
	return simdisk.OpWrite
}

// Write is FSX-030/031.
func (h *simFile) Write(b []byte) (int, error) {
	var n int
	err := h.fs.frame(h.writeClass(), "write", h.path, h.stale("write"), func() error {
		switch {
		case h.closed:
			return h.err("write", fs.ErrClosed)
		case h.acc == os.O_RDONLY:
			return h.err("write", syscall.EBADF)
		case len(b) == 0:
			return errSkip
		}
		return nil
	}, func() error {
		var err error
		if h.flag&os.O_APPEND != 0 {
			n, err = h.f.Append(b)
			if size := h.f.Size(); size >= 0 {
				h.off = size
			}
		} else {
			n, err = h.f.WriteAt(b, h.off)
			h.off += int64(n)
		}
		if err == nil && h.flag&os.O_SYNC != 0 {
			err = h.f.Sync()
		}
		return pathErr("write", h.name, err)
	})
	if err == errSkip {
		return 0, nil
	}
	return n, err
}

// ReadAt is FSX-030/031.
func (h *simFile) ReadAt(b []byte, off int64) (int, error) {
	var n int
	err := h.fs.frame(simdisk.OpRead, "readat", h.path, h.stale("readat"), func() error {
		switch {
		case h.closed:
			return h.err("readat", fs.ErrClosed)
		case h.acc == os.O_WRONLY:
			return h.err("readat", syscall.EBADF)
		case h.dir:
			return h.err("readat", syscall.EISDIR)
		case off < 0:
			return h.err("readat", syscall.EINVAL)
		case len(b) == 0:
			return errSkip
		}
		return nil
	}, func() error {
		m, err := h.f.ReadAt(b, off)
		n = m
		return pathErr("readat", h.name, err)
	})
	if err == errSkip {
		return 0, nil
	}
	return n, err
}

// WriteAt is FSX-030/031.
func (h *simFile) WriteAt(b []byte, off int64) (int, error) {
	var n int
	err := h.fs.frame(h.writeClass(), "writeat", h.path, h.stale("writeat"), func() error {
		switch {
		case h.closed:
			return h.err("writeat", fs.ErrClosed)
		case h.acc == os.O_RDONLY:
			return h.err("writeat", syscall.EBADF)
		case h.flag&os.O_APPEND != 0:
			return h.err("writeat", ErrWriteAtInAppendMode)
		case off < 0:
			return h.err("writeat", syscall.EINVAL)
		case len(b) == 0:
			return errSkip
		}
		return nil
	}, func() error {
		m, err := h.f.WriteAt(b, off)
		n = m
		if err == nil && h.flag&os.O_SYNC != 0 {
			err = h.f.Sync()
		}
		return pathErr("writeat", h.name, err)
	})
	if err == errSkip {
		return 0, nil
	}
	return n, err
}

// Seek is FSX-030/031; no latency, no sample.
func (h *simFile) Seek(offset int64, whence int) (int64, error) {
	var ret int64
	err := h.fs.frame(0, "seek", h.path, h.stale("seek"), func() error {
		switch {
		case h.closed:
			return h.err("seek", fs.ErrClosed)
		case whence != io.SeekStart && whence != io.SeekCurrent && whence != io.SeekEnd:
			return h.err("seek", syscall.EINVAL)
		}
		return nil
	}, func() error {
		base := int64(0)
		switch whence {
		case io.SeekCurrent:
			base = h.off
		case io.SeekEnd:
			if !h.dir {
				base = h.f.Size()
			}
		}
		if base+offset < 0 {
			return h.err("seek", syscall.EINVAL)
		}
		h.off = base + offset
		ret = h.off
		return nil
	})
	return ret, err
}

// Truncate is FSX-030/031; the offset is unchanged.
func (h *simFile) Truncate(size int64) error {
	return h.fs.frame(simdisk.OpWrite, "truncate", h.path, h.stale("truncate"), func() error {
		switch {
		case h.closed:
			return h.err("truncate", fs.ErrClosed)
		case h.acc == os.O_RDONLY:
			return h.err("truncate", syscall.EINVAL)
		case size < 0:
			return h.err("truncate", syscall.EINVAL)
		}
		return nil
	}, func() error {
		return pathErr("truncate", h.name, h.f.Truncate(size))
	})
}

// Stat is FSX-031; no latency, no sample.
func (h *simFile) Stat() (fs.FileInfo, error) {
	var info fs.FileInfo
	err := h.fs.frame(0, "stat", h.path, h.stale("stat"), func() error {
		if h.closed {
			return h.err("stat", fs.ErrClosed)
		}
		return nil
	}, func() error {
		if h.dir {
			i, err := h.fs.v.Stat(h.path)
			if err != nil {
				return pathErr("stat", h.name, err)
			}
			info = simInfo{name: i.Name, size: i.Size, dir: i.IsDir}
			return nil
		}
		size := h.f.Size()
		if size < 0 {
			return h.stale("stat")
		}
		info = simInfo{name: path.Base(h.path), size: size}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

// Sync is FSX-031: Sync for files, SyncDir for directories.
func (h *simFile) Sync() error {
	return h.fs.frame(simdisk.OpSync, "sync", h.path, h.stale("sync"), func() error {
		if h.closed {
			return h.err("sync", fs.ErrClosed)
		}
		return nil
	}, func() error {
		if h.dir {
			return pathErr("sync", h.name, h.fs.v.SyncDir(h.path))
		}
		return pathErr("sync", h.name, h.f.Sync())
	})
}

// Close is FSX-031: a stale handle is closed and returns ESTALE.
func (h *simFile) Close() error {
	return h.fs.frame(simdisk.OpMeta, "close", h.path, h.stale("close"), func() error {
		if h.closed {
			return h.err("close", fs.ErrClosed)
		}
		return nil
	}, func() error {
		h.closed = true
		if h.dir {
			return nil
		}
		return pathErr("close", h.name, h.f.Close())
	})
}
