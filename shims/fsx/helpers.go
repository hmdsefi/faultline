// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package fsx

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
)

// ReadFile reads the whole file (os.ReadFile semantics).
func ReadFile(fsys FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	size := 512
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		size += int(info.Size())
	}
	data := make([]byte, 0, size)
	var rerr error
	for {
		if len(data) == cap(data) {
			data = append(data, 0)[:len(data)]
		}
		n, err := f.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if err != nil {
			if err != io.EOF {
				rerr = err
			}
			break
		}
	}
	if cerr := f.Close(); rerr == nil {
		rerr = cerr
	}
	return data, rerr
}

// WriteFile writes data with O_WRONLY|O_CREATE|O_TRUNC and perm; it does not sync
// (os.WriteFile semantics).
func WriteFile(fsys FS, name string, data []byte, perm fs.FileMode) error {
	f, err := fsys.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// CreateTemp creates a new file in dir with a name from pattern (the last "*" is replaced by a
// decimal number; without "*" the number is appended), opened O_RDWR. Names are chosen
// deterministically: the numbers 1, 2, ... are tried in order.
func CreateTemp(fsys FS, dir, pattern string) (File, error) {
	if strings.ContainsRune(pattern, '/') {
		return nil, &fs.PathError{Op: "createtemp", Path: pattern, Err: errors.New("pattern contains path separator")}
	}
	prefix, suffix := pattern, ""
	if i := strings.LastIndexByte(pattern, '*'); i >= 0 {
		prefix, suffix = pattern[:i], pattern[i+1:]
	}
	for i := 1; i <= 10000; i++ {
		f, err := fsys.OpenFile(path.Join(dir, prefix+strconv.Itoa(i)+suffix), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	return nil, &fs.PathError{Op: "createtemp", Path: path.Join(dir, prefix+"*"+suffix), Err: fs.ErrExist}
}
