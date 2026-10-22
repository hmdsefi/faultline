// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// Case is one conformance check. Run gets an FS whose root is empty and returns nil on success.
type Case struct {
	Name string
	Run  func(fsys FS) error
}

// fails checks FSX-081's "fails with target": errors.Is, a *fs.PathError with op and path.
func fails(step string, err, target error, op, path string) error {
	if err == nil {
		return fmt.Errorf("%s: no error, want %v", step, target)
	}
	if !errors.Is(err, target) {
		return fmt.Errorf("%s: %v, want %v", step, err, target)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != op || pe.Path != path {
		return fmt.Errorf("%s: %T %v, want *fs.PathError op %q path %q", step, err, err, op, path)
	}
	return nil
}

// linkFails is fails for Rename: *os.LinkError with op "rename", old and new.
func linkFails(step string, err, target error, oldname, newname string) error { //nolint:unparam // step mirrors fails; every case so far passes "rename"
	if err == nil || !errors.Is(err, target) {
		return fmt.Errorf("%s: %v, want %v", step, err, target)
	}
	var le *os.LinkError
	if !errors.As(err, &le) || le.Op != "rename" || le.Old != oldname || le.New != newname {
		return fmt.Errorf("%s: %T %v, want *os.LinkError rename %q %q", step, err, err, oldname, newname)
	}
	return nil
}

func step(name string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

func put(fsys FS, name, content string) error {
	return step("write "+name, WriteFile(fsys, name, []byte(content), 0o644))
}

func expectContent(fsys FS, name, want string) error {
	b, err := ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("read %s: %v", name, err)
	}
	if string(b) != want {
		return fmt.Errorf("content of %s = %q, want %q", name, b, want)
	}
	return nil
}

func touch(fsys FS, name string) error {
	f, err := fsys.Create(name)
	if err != nil {
		return fmt.Errorf("create %s: %v", name, err)
	}
	return step("close "+name, f.Close())
}

// first returns the first non-nil error.
func first(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Conformance returns the conformance cases, in a fixed order.
func Conformance() []Case {
	return []Case{
		{"create-write-read", func(fsys FS) error {
			f, err := fsys.Create("a")
			if err != nil {
				return step("create", err)
			}
			defer f.Close()
			if n, err := f.Write([]byte("hello")); n != 5 || err != nil {
				return fmt.Errorf("write = %d, %v", n, err)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return step("seek", err)
			}
			b := make([]byte, 10)
			if n, err := f.Read(b); n != 5 || err != nil || string(b[:n]) != "hello" {
				return fmt.Errorf("read = %d %q, %v; want 5 hello nil", n, b[:n], err)
			}
			if n, err := f.Read(b); n != 0 || err != io.EOF {
				return fmt.Errorf("second read = %d, %v; want 0, io.EOF", n, err)
			}
			return nil
		}},
		{"open-missing", func(fsys FS) error {
			_, err := fsys.Open("nope")
			return fails("open", err, fs.ErrNotExist, "open", "nope")
		}},
		{"open-empty-name", func(fsys FS) error {
			_, err := fsys.Open("")
			return fails("open", err, syscall.ENOENT, "open", "")
		}},
		{"create-excl", func(fsys FS) error {
			if err := touch(fsys, "a"); err != nil {
				return err
			}
			_, err := fsys.OpenFile("a", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			return fails("open excl", err, fs.ErrExist, "open", "a")
		}},
		{"open-trunc", func(fsys FS) error {
			if err := put(fsys, "a", "hello"); err != nil {
				return err
			}
			f, err := fsys.OpenFile("a", os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				return step("open trunc", err)
			}
			f.Close()
			info, err := fsys.Stat("a")
			if err != nil || info.Size() != 0 {
				return fmt.Errorf("stat after O_TRUNC: %v, %v", info, err)
			}
			return nil
		}},
		{"append", func(fsys FS) error {
			if err := put(fsys, "a", "ab"); err != nil {
				return err
			}
			f, err := fsys.OpenFile("a", os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				return step("open append", err)
			}
			defer f.Close()
			if _, err := f.Write([]byte("c")); err != nil {
				return step("write c", err)
			}
			if _, err := f.Write([]byte("d")); err != nil {
				return step("write d", err)
			}
			n, err := f.WriteAt([]byte("x"), 0)
			if n != 0 {
				return fmt.Errorf("WriteAt returned %d, want 0", n)
			}
			return first(fails("writeat", err, ErrWriteAtInAppendMode, "writeat", "a"), expectContent(fsys, "a", "abcd"))
		}},
		{"create-missing-parent", func(fsys FS) error {
			_, err := fsys.Create("d/a")
			return fails("create", err, syscall.ENOENT, "open", "d/a")
		}},
		{"path-through-file", func(fsys FS) error {
			if err := touch(fsys, "f"); err != nil {
				return err
			}
			_, e1 := fsys.Open("f/x")
			_, e2 := fsys.Create("f/x")
			return first(fails("open", e1, syscall.ENOTDIR, "open", "f/x"), fails("create", e2, syscall.ENOTDIR, "open", "f/x"))
		}},
		{"mkdir-existing", func(fsys FS) error {
			if err := step("mkdir", fsys.Mkdir("d", 0o755)); err != nil {
				return err
			}
			return fails("mkdir again", fsys.Mkdir("d", 0o755), fs.ErrExist, "mkdir", "d")
		}},
		{"mkdir-missing-parent", func(fsys FS) error {
			return fails("mkdir", fsys.Mkdir("a/b", 0o755), syscall.ENOENT, "mkdir", "a/b")
		}},
		{"mkdirall", func(fsys FS) error {
			if err := first(step("mkdirall", fsys.MkdirAll("a/b/c", 0o755)), step("mkdirall again", fsys.MkdirAll("a/b/c", 0o755))); err != nil {
				return err
			}
			info, err := fsys.Stat("a/b/c")
			if err != nil || !info.IsDir() {
				return fmt.Errorf("stat a/b/c: %v, %v", info, err)
			}
			return nil
		}},
		{"mkdirall-file", func(fsys FS) error {
			if err := touch(fsys, "f"); err != nil {
				return err
			}
			return first(fails("mkdirall f", fsys.MkdirAll("f", 0o755), syscall.ENOTDIR, "mkdir", "f"),
				fails("mkdirall f/x/y", fsys.MkdirAll("f/x/y", 0o755), syscall.ENOTDIR, "mkdir", "f/x/y"))
		}},
		{"remove-file", func(fsys FS) error {
			if err := first(touch(fsys, "a"), step("remove", fsys.Remove("a"))); err != nil {
				return err
			}
			_, err := fsys.Stat("a")
			return fails("stat", err, fs.ErrNotExist, "stat", "a")
		}},
		{"remove-nonempty", func(fsys FS) error {
			if err := first(step("mkdirall", fsys.MkdirAll("d", 0o755)), touch(fsys, "d/x")); err != nil {
				return err
			}
			return fails("remove", fsys.Remove("d"), syscall.ENOTEMPTY, "remove", "d")
		}},
		{"remove-missing", func(fsys FS) error {
			return fails("remove", fsys.Remove("nope"), fs.ErrNotExist, "remove", "nope")
		}},
		{"remove-root", func(fsys FS) error {
			return first(fails("remove /", fsys.Remove("/"), syscall.EINVAL, "remove", "/"),
				fails("removeall /", fsys.RemoveAll("/"), syscall.EINVAL, "removeall", "/"))
		}},
		{"removeall", func(fsys FS) error {
			if err := first(step("mkdirall", fsys.MkdirAll("d/e", 0o755)), touch(fsys, "d/a"), touch(fsys, "d/e/b")); err != nil {
				return err
			}
			if err := first(step("removeall d", fsys.RemoveAll("d")), step("removeall nope", fsys.RemoveAll("nope")), step("removeall empty", fsys.RemoveAll(""))); err != nil {
				return err
			}
			_, err := fsys.Stat("d")
			return fails("stat", err, fs.ErrNotExist, "stat", "d")
		}},
		{"removeall-file", func(fsys FS) error {
			if err := first(touch(fsys, "f"), step("removeall", fsys.RemoveAll("f"))); err != nil {
				return err
			}
			_, err := fsys.Stat("f")
			return fails("stat", err, fs.ErrNotExist, "stat", "f")
		}},
		{"rename-replace", func(fsys FS) error {
			if err := first(put(fsys, "a", "1"), put(fsys, "b", "2"), step("rename", fsys.Rename("a", "b"))); err != nil {
				return err
			}
			_, err := fsys.Stat("a")
			return first(expectContent(fsys, "b", "1"), fails("stat a", err, fs.ErrNotExist, "stat", "a"))
		}},
		{"rename-missing", func(fsys FS) error {
			return linkFails("rename", fsys.Rename("nope", "x"), fs.ErrNotExist, "nope", "x")
		}},
		{"rename-onto-dir", func(fsys FS) error {
			if err := first(touch(fsys, "f"), step("mkdir", fsys.Mkdir("d", 0o755))); err != nil {
				return err
			}
			return linkFails("rename", fsys.Rename("f", "d"), syscall.EEXIST, "f", "d")
		}},
		{"rename-dir-onto-file", func(fsys FS) error {
			if err := first(step("mkdir", fsys.Mkdir("d", 0o755)), touch(fsys, "f")); err != nil {
				return err
			}
			return linkFails("rename", fsys.Rename("d", "f"), syscall.ENOTDIR, "d", "f")
		}},
		{"rename-into-self", func(fsys FS) error {
			if err := step("mkdir", fsys.Mkdir("d", 0o755)); err != nil {
				return err
			}
			return linkFails("rename", fsys.Rename("d", "d/sub"), syscall.EINVAL, "d", "d/sub")
		}},
		{"rename-missing-parent", func(fsys FS) error {
			if err := touch(fsys, "f"); err != nil {
				return err
			}
			return linkFails("rename", fsys.Rename("f", "q/z"), syscall.ENOENT, "f", "q/z")
		}},
		{"readdir", func(fsys FS) error {
			if err := first(touch(fsys, "b"), touch(fsys, "a"), step("mkdir", fsys.Mkdir("c", 0o755))); err != nil {
				return err
			}
			ents, err := fsys.ReadDir("/")
			if err != nil {
				return step("readdir", err)
			}
			var got []string
			for _, e := range ents {
				got = append(got, fmt.Sprintf("%s:%v", e.Name(), e.IsDir()))
			}
			if fmt.Sprint(got) != "[a:false b:false c:true]" {
				return fmt.Errorf("readdir = %v", got)
			}
			return nil
		}},
		{"readdir-file-missing", func(fsys FS) error {
			if err := touch(fsys, "f"); err != nil {
				return err
			}
			_, e1 := fsys.ReadDir("f")
			_, e2 := fsys.ReadDir("nope")
			return first(fails("readdir f", e1, syscall.ENOTDIR, "readdir", "f"), fails("readdir nope", e2, fs.ErrNotExist, "readdir", "nope"))
		}},
		{"stat", func(fsys FS) error {
			if err := first(put(fsys, "f", "abc"), step("mkdir", fsys.Mkdir("d", 0o755))); err != nil {
				return err
			}
			fi, err1 := fsys.Stat("f")
			di, err2 := fsys.Stat("d")
			if err1 != nil || err2 != nil || fi.Size() != 3 || fi.IsDir() || fi.Name() != "f" || !di.IsDir() || di.Name() != "d" {
				return fmt.Errorf("stat f %v %v, d %v %v", fi, err1, di, err2)
			}
			return nil
		}},
		{"access-mode", func(fsys FS) error {
			if err := put(fsys, "f", "x"); err != nil {
				return err
			}
			r, err := fsys.Open("f")
			if err != nil {
				return step("open", err)
			}
			defer r.Close()
			_, e1 := r.Write([]byte("y"))
			w, err := fsys.OpenFile("f", os.O_WRONLY, 0)
			if err != nil {
				return step("open wronly", err)
			}
			defer w.Close()
			_, e2 := w.Read(make([]byte, 1))
			e3 := r.Truncate(0)
			return first(fails("write on O_RDONLY", e1, syscall.EBADF, "write", "f"),
				fails("read on O_WRONLY", e2, syscall.EBADF, "read", "f"),
				fails("truncate on O_RDONLY", e3, syscall.EINVAL, "truncate", "f"))
		}},
		{"readat", func(fsys FS) error {
			if err := put(fsys, "f", "hello"); err != nil {
				return err
			}
			f, err := fsys.Open("f")
			if err != nil {
				return step("open", err)
			}
			defer f.Close()
			b := make([]byte, 10)
			if n, err := f.ReadAt(b[:3], 2); n != 3 || err != nil || string(b[:3]) != "llo" {
				return fmt.Errorf("ReadAt(3, 2) = %d %q %v", n, b[:3], err)
			}
			if n, err := f.ReadAt(b[:10], 1); n != 4 || err != io.EOF {
				return fmt.Errorf("ReadAt(10, 1) = %d %v; want 4 io.EOF", n, err)
			}
			_, err = f.ReadAt(b, -1)
			return fails("ReadAt(-1)", err, syscall.EINVAL, "readat", "f")
		}},
		{"seek", func(fsys FS) error {
			if err := put(fsys, "f", "hello"); err != nil {
				return err
			}
			f, err := fsys.Open("f")
			if err != nil {
				return step("open", err)
			}
			defer f.Close()
			for _, tc := range []struct {
				off    int64
				whence int
				want   int64
			}{{1, io.SeekStart, 1}, {2, io.SeekCurrent, 3}, {-1, io.SeekEnd, 4}} {
				if n, err := f.Seek(tc.off, tc.whence); n != tc.want || err != nil {
					return fmt.Errorf("Seek(%d, %d) = %d, %v; want %d", tc.off, tc.whence, n, err, tc.want)
				}
			}
			_, e1 := f.Seek(-10, io.SeekStart)
			_, e2 := f.Seek(0, 7)
			return first(fails("seek -10", e1, syscall.EINVAL, "seek", "f"), fails("seek whence 7", e2, syscall.EINVAL, "seek", "f"))
		}},
		{"seek-past-end", func(fsys FS) error {
			f, err := fsys.Create("f")
			if err != nil {
				return step("create", err)
			}
			if _, err := f.Seek(10, io.SeekStart); err != nil {
				return step("seek", err)
			}
			if _, err := f.Write([]byte("z")); err != nil {
				return step("write", err)
			}
			f.Close()
			return expectContent(fsys, "f", string(append(make([]byte, 10), 'z')))
		}},
		{"close-twice", func(fsys FS) error {
			f, err := fsys.Create("f")
			if err != nil {
				return step("create", err)
			}
			if err := step("close", f.Close()); err != nil {
				return err
			}
			_, e := f.Read(make([]byte, 1))
			return first(fails("close again", f.Close(), fs.ErrClosed, "close", "f"), fails("read after close", e, fs.ErrClosed, "read", "f"))
		}},
		{"dir-handle", func(fsys FS) error {
			if err := step("mkdir", fsys.Mkdir("d", 0o755)); err != nil {
				return err
			}
			d, err := fsys.Open("d")
			if err != nil {
				return step("open dir", err)
			}
			defer d.Close()
			_, e := d.Read(make([]byte, 1))
			if err := fails("read dir", e, syscall.EISDIR, "read", "d"); err != nil {
				return err
			}
			if err := step("sync dir handle", d.Sync()); err != nil {
				return err
			}
			info, err := d.Stat()
			if err != nil || !info.IsDir() {
				return fmt.Errorf("stat dir handle: %v, %v", info, err)
			}
			return nil
		}},
		{"open-dir-write", func(fsys FS) error {
			if err := step("mkdir", fsys.Mkdir("d", 0o755)); err != nil {
				return err
			}
			_, e1 := fsys.OpenFile("d", os.O_WRONLY, 0)
			_, e2 := fsys.Create("d")
			return first(fails("open wronly", e1, syscall.EISDIR, "open", "d"), fails("create", e2, syscall.EISDIR, "open", "d"))
		}},
		{"truncate", func(fsys FS) error {
			if err := put(fsys, "f", "hello"); err != nil {
				return err
			}
			f, err := fsys.OpenFile("f", os.O_RDWR, 0)
			if err != nil {
				return step("open", err)
			}
			if err := first(step("truncate 2", f.Truncate(2)), step("truncate 4", f.Truncate(4))); err != nil {
				return err
			}
			f.Close()
			return expectContent(fsys, "f", "he\x00\x00")
		}},
		{"syncdir", func(fsys FS) error {
			if err := first(step("mkdir", fsys.Mkdir("d", 0o755)), step("syncdir d", fsys.SyncDir("d")), touch(fsys, "f")); err != nil {
				return err
			}
			return first(fails("syncdir nope", fsys.SyncDir("nope"), fs.ErrNotExist, "syncdir", "nope"),
				fails("syncdir f", fsys.SyncDir("f"), syscall.ENOTDIR, "syncdir", "f"))
		}},
		{"name", func(fsys FS) error {
			if err := step("mkdir", fsys.Mkdir("d", 0o755)); err != nil {
				return err
			}
			f, err := fsys.Create("d/../x")
			if err != nil {
				return step("create", err)
			}
			defer f.Close()
			if f.Name() != "d/../x" {
				return fmt.Errorf("Name() = %q", f.Name())
			}
			_, err = fsys.Stat("x")
			return step("stat x", err)
		}},
		{"clean-names", func(fsys FS) error {
			if err := touch(fsys, "/a"); err != nil {
				return err
			}
			for _, n := range []string{"a", "../a", "/x/../a"} {
				if _, err := fsys.Stat(n); err != nil {
					return step("stat "+n, err)
				}
			}
			return nil
		}},
		{"osync", func(fsys FS) error {
			f, err := fsys.OpenFile("s", os.O_WRONLY|os.O_CREATE|os.O_SYNC, 0o644)
			if err != nil {
				return step("open", err)
			}
			if n, err := f.Write([]byte("x")); n != 1 || err != nil {
				return fmt.Errorf("write = %d, %v", n, err)
			}
			f.Close()
			return expectContent(fsys, "s", "x")
		}},
		{"unlinked-handle", func(fsys FS) error {
			f, err := fsys.Create("u")
			if err != nil {
				return step("create", err)
			}
			defer f.Close()
			if _, err := f.Write([]byte("abc")); err != nil {
				return step("write", err)
			}
			if err := step("remove", fsys.Remove("u")); err != nil {
				return err
			}
			b := make([]byte, 3)
			if n, err := f.ReadAt(b, 0); n != 3 || string(b) != "abc" || (err != nil && err != io.EOF) {
				return fmt.Errorf("ReadAt after remove = %d %q %v", n, b, err)
			}
			return nil
		}},
		{"large", func(fsys FS) error {
			f, err := fsys.Create("big")
			if err != nil {
				return step("create", err)
			}
			var want bytes.Buffer
			chunk := make([]byte, 4096)
			for i := 0; i < 256; i++ {
				for j := range chunk {
					chunk[j] = byte(i + j) //nolint:gosec // G115: the test pattern wraps at 256 on purpose
				}
				want.Write(chunk)
				if _, err := f.Write(chunk); err != nil {
					return step("write", err)
				}
			}
			f.Close()
			got, err := ReadFile(fsys, "big")
			if err != nil || !bytes.Equal(got, want.Bytes()) {
				return fmt.Errorf("read back %d bytes, %v; want 1 MiB identical", len(got), err)
			}
			return nil
		}},
		{"helpers", func(fsys FS) error {
			data := []byte("helper data")
			if err := step("WriteFile", WriteFile(fsys, "w", data, 0o644)); err != nil {
				return err
			}
			got, err := ReadFile(fsys, "w")
			if err != nil || !bytes.Equal(got, data) {
				return fmt.Errorf("ReadFile = %q, %v", got, err)
			}
			var names []string
			for range 2 {
				f, err := CreateTemp(fsys, "", "t-*.log")
				if err != nil {
					return step("CreateTemp", err)
				}
				names = append(names, f.Name())
				f.Close()
			}
			if fmt.Sprint(names) != "[t-1.log t-2.log]" {
				return fmt.Errorf("CreateTemp names %v", names)
			}
			return nil
		}},
	}
}
