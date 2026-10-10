// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

//go:build !unix || aix || solaris

package artifact

// mkfifo reports that it cannot create a FIFO here (syscall has no Mkfifo).
func mkfifo(string) (ok bool, err error) {
	return false, nil
}
