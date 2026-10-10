// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

//go:build !unix

package artifact

// setUmask reports that there is no umask to set.
func setUmask(int) (old int, ok bool) {
	return 0, false
}
