//go:build !unix || aix || solaris

package artifact

// mkfifo reports that it cannot create a FIFO here (syscall has no Mkfifo).
func mkfifo(string) (ok bool, err error) {
	return false, nil
}
