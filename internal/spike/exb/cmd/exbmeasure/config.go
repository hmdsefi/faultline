// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// base is EXB-060's base seed.
const base = 0x0e0b5eed00000001

// errRequirement marks a measurement whose requirement is not met (exit code 1).
var errRequirement = errors.New("requirement not met")

// config holds the flags shared by the subcommands.
type config struct {
	backend, corpus, out, work, root, wasmRunner, toolexec string
	seeds, runs, reps, parallel                            int
	reuse                                                  bool
}

// findRoot walks up from the working directory to the go.mod of github.com/hmdsefi/faultline.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.Contains(string(b), "module github.com/hmdsefi/faultline\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no faultline checkout above the working directory; pass -root")
		}
		dir = parent
	}
}

// goCmd returns a go command running in dir with extra environment, outside any go.work
// (GOWORK=off), since a workspace at the checkout root would change the modules a build uses.
func goCmd(dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GOWORK=off"), env...)
	return cmd
}
