package order

import (
	"fmt"
	"os"
)

// exbout stands in for the spike's exbout package.
var exbout outWriter

type outWriter struct{}

// Write appends "<test>\t0x%016x\t<outcome>\n" to the file named by EXB_OUT, if set.
func (outWriter) Write(test string, seed uint64, outcome string) error {
	path := os.Getenv("EXB_OUT")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s\t0x%016x\t%s\n", test, seed, outcome)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
