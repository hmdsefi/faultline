// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package exbout writes corpus outcome lines to the file named by EXB_OUT.
package exbout

import (
	"fmt"
	"os"
)

// Write appends "<test>\t0x%016x\t<outcome>\n" to the file named by EXB_OUT. Without EXB_OUT it
// does nothing.
func Write(test string, seed uint64, outcome string) error {
	path := os.Getenv("EXB_OUT")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G703: EXB_OUT names the output file on purpose
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s\t0x%016x\t%s\n", test, seed, outcome)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
