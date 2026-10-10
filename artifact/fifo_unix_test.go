// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

//go:build unix && !aix && !solaris

package artifact

import "syscall"

// mkfifo creates a FIFO at path.
func mkfifo(path string) (ok bool, err error) {
	return true, syscall.Mkfifo(path, 0o600)
}
