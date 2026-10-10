// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// nodeOrSkip returns the path of node 20 or later, or skips t when there is none (AT-ART-18).
func nodeOrSkip(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; skipping")
	}
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Skipf("node --version: %v", err)
	}
	m := regexp.MustCompile(`^v(\d+)\.`).FindSubmatch(out)
	if m == nil {
		t.Skipf("unrecognized node version %q", out)
	}
	if major, _ := strconv.Atoi(string(m[1])); major < 20 {
		t.Skipf("node %s is older than 20; skipping", bytes.TrimSpace(out))
	}
	return node
}

// runNode runs node with args, and stdin when it is not nil, and returns the combined output. It
// stops node before the test's own deadline: a test binary that times out exits without stopping
// its children, so a suite stuck in a loop would keep node busy after go test ends. It stops node
// with SIGINT, on which node --test stops the per-file processes it started; a kill would orphan
// them.
func runNode(t *testing.T, node string, stdin []byte, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if d, ok := t.Deadline(); ok {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, d.Add(-time.Until(d)/10))
		defer stop()
	}
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("node stopped at the test deadline: %w", ctx.Err())
	}
	return out, err
}

// TestTimelineJS runs the node suites of ui/jstest (AT-ART-18) when node is on PATH.
func TestTimelineJS(t *testing.T) {
	node := nodeOrSkip(t)
	files, err := filepath.Glob(filepath.Join("jstest", "*.test.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no ui/jstest/*.test.mjs suites found")
	}
	// Node 20 and 22 report in TAP when stdout is not a terminal; spec reads the same everywhere.
	if out, err := runNode(t, node, nil, append([]string{"--test", "--test-reporter=spec"}, files...)...); err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out)
	}
}
