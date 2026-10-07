package ui

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestTimelineJS runs the node suites of ui/jstest (AT-ART-18) when node is on PATH.
func TestTimelineJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; skipping ui/jstest")
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
		t.Skipf("node %s is older than 20; skipping ui/jstest", out)
	}
	files, err := filepath.Glob(filepath.Join("jstest", "*.test.mjs"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no ui/jstest suites found: %v", err)
	}
	cmd := exec.Command(node, append([]string{"--test"}, files...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out)
	}
}
