// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Command exbmeasure runs the measurement procedures of the exact-backend spike and writes their
// result files. It exits 0 when the measured requirement is met, 1 when it is not, and 2 on a
// usage or tool error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const usageText = `usage:
  exbmeasure exactness   -backend B -corpus C -seeds N -runs R -out FILE
  exbmeasure sensitivity -backend B -corpus C -seeds N -out FILE
  exbmeasure slowdown    -backend B -corpus C -seeds N -reps K -out FILE
  exbmeasure compat      -backend B -out FILE
  exbmeasure size        -backend B -out FILE
  exbmeasure deps        -out FILE
common flags: -root DIR (faultline checkout), -work DIR, -reuse, -parallel P, -wasm PATH, -toolexec PATH`

// backends are the values of -backend.
var backends = []string{"parta", "wasm", "toolexec"}

// commands maps each subcommand to its procedure; a variable so that tests can replace one.
var commands = map[string]func(*config) error{
	"exactness":   exactness,
	"sensitivity": sensitivity,
	"slowdown":    slowdown,
	"compat":      compat,
	"size":        size,
	"deps":        deps,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run runs the subcommand of args and returns the exit code: 0 and "<cmd>: PASS" on stdout when
// the requirement is met, 1 and "<cmd>: FAIL" when it is not, 2 on a usage or tool error.
func run(args []string, stdout, stderr io.Writer) int {
	usage := func() { fmt.Fprintln(stderr, usageText) }
	if len(args) == 0 {
		usage()
		return 2
	}
	cmd := args[0]
	procedure, ok := commands[cmd]
	if !ok {
		usage()
		return 2
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = usage
	var c config
	fs.StringVar(&c.backend, "backend", "", "parta, wasm or toolexec")
	fs.StringVar(&c.corpus, "corpus", "", "C1 to C5; C7 with sensitivity")
	fs.StringVar(&c.out, "out", "", "result file")
	fs.StringVar(&c.work, "work", filepath.Join(os.TempDir(), "exbmeasure"), "work directory (builds, raw results)")
	fs.StringVar(&c.root, "root", "", "faultline checkout (default: found from the working directory)")
	fs.StringVar(&c.wasmRunner, "wasm", "faultline-wasm", "faultline-wasm binary")
	fs.StringVar(&c.toolexec, "toolexec", "", "faultline-toolexec binary (default $(go env GOPATH)/bin/faultline-toolexec)")
	fs.IntVar(&c.seeds, "seeds", 0, "number of seeds")
	fs.IntVar(&c.runs, "runs", 2, "runs per seed")
	fs.IntVar(&c.reps, "reps", 3, "repetitions")
	fs.IntVar(&c.parallel, "parallel", runtime.NumCPU(), "concurrent runs (exactness, sensitivity)")
	fs.BoolVar(&c.reuse, "reuse", false, "use raw results already in -work instead of running again")
	if err := fs.Parse(args[1:]); err != nil {
		return 2 // the flag package has printed the error and the usage
	}
	if fs.NArg() > 0 || c.out == "" {
		usage()
		return 2
	}
	if c.root == "" {
		r, err := findRoot()
		if err != nil {
			fmt.Fprintln(stderr, "exbmeasure:", err)
			return 2
		}
		c.root = r
	}
	if err := c.absolute(); err != nil {
		fmt.Fprintln(stderr, "exbmeasure:", err)
		return 2
	}
	err := procedure(&c)
	switch {
	case errors.Is(err, errRequirement):
		fmt.Fprintln(stdout, cmd+": FAIL")
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "exbmeasure:", err)
		return 2
	}
	fmt.Fprintln(stdout, cmd+": PASS")
	return 0
}

// absolute makes the paths of c absolute. Test binaries run in their package directory, so a
// relative -work would put FAULTLINE_RESULTS files elsewhere, and a relative -wasm or -toolexec
// would name another binary. A runner given without a path separator stays a PATH lookup.
func (c *config) absolute() error {
	for _, p := range []*string{&c.root, &c.work, &c.out, &c.wasmRunner, &c.toolexec} {
		if *p == "" || (p == &c.wasmRunner || p == &c.toolexec) && !strings.ContainsRune(*p, filepath.Separator) {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = abs
	}
	return nil
}
