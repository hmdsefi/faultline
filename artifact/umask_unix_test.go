//go:build unix

package artifact

import "syscall"

// setUmask sets the process umask to mask and returns the previous one.
func setUmask(mask int) (old int, ok bool) {
	return syscall.Umask(mask), true
}
