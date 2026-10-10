// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// phase1Usage is the top-level usage text of ART-095 for the Phase 1 command table.
const phase1Usage = `faultline is a deterministic simulation testing tool for Go.

Usage:

	faultline <command> [arguments]

Commands:

	help     show help for faultline or one of its commands
	render   regenerate timeline.txt, hb.mmd and timeline.html in an artifact directory
	version  print the faultline version

Use "faultline help <command>" for more information about a command.
`

// renderDefaults is what PrintDefaults prints for render's flags (ART-098).
const renderDefaults = "  -max-records int\n    \tmaximum number of records embedded in timeline.html (at least 1000) (default 50000)\n" +
	"  -slice-cap int\n    \tmaximum number of records in the causal slice (hb.mmd, timeline marks) (default 200)\n"

// renderFlagUsage is render's FlagSet usage (ART-096); renderHelp is "faultline help render" (ART-095).
const (
	renderFlagUsage = "usage: faultline render [-slice-cap n] [-max-records n] <dir>\n" + renderDefaults
	renderHelp      = "usage: faultline render [-slice-cap n] [-max-records n] <dir>\n\nRender regenerates timeline.txt, hb.mmd and timeline.html in an artifact directory from its\nreport.json and trace.jsonl. It changes no other file, except that it removes the temporary\nfiles of an interrupted render.\n" + renderDefaults
)

// unknownText is what ART-095 prints for an unknown command name.
func unknownText(name string) string {
	return "faultline: unknown command \"" + name + "\"\nRun 'faultline help' for usage.\n"
}

// writeArtifact writes a failing artifact of n records, each caused by the one before, and returns
// its directory.
func writeArtifact(t *testing.T, n uint64) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "a")
	recs := []kernel.Record{{Seq: 1, Kind: "kernel.start", Text: "start"}}
	for seq := uint64(2); seq < n; seq++ {
		recs = append(recs, kernel.Record{Seq: seq, Kind: "x.step", Cause: seq - 1, Text: "step"})
	}
	recs = append(recs, kernel.Record{Seq: n, Kind: "check.violation", Cause: n - 1, Text: "simulation failed"})
	a := &artifact.Artifact{
		Report: artifact.Report{
			Status: "fail", Package: "example.com/toy", Test: "TestToy", Subtest: "TestToy/seed=0x0000000000000001",
			Seed: "0x0000000000000001", SeedSource: "env",
			Failure: &artifact.Failure{Kind: "fail", Signature: "fail:", Headline: "simulation failed at t=0.000000000s", Message: "bad", RecordSeq: n},
		},
		Text:  "--- FAIL: TestToy/seed=0x0000000000000001\n",
		Trace: &artifact.Trace{Records: recs},
	}
	if err := artifact.Write(dir, a); err != nil {
		t.Fatal(err)
	}
	return dir
}

// childContext returns a context that ends a little before the test's deadline, so that a child
// process that hangs is stopped: when go test's -timeout ends the test binary, its children keep
// running.
func childContext(t *testing.T) context.Context {
	ctx := t.Context()
	if d, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, d.Add(-time.Until(d)/10))
		t.Cleanup(cancel)
	}
	return ctx
}

// AT-ART-15
func TestCLI(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH to build faultline with")
	}
	ctx := childContext(t)
	bin := filepath.Join(t.TempDir(), "faultline")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := func(flags ...string) ([]byte, error) {
		args := append(append([]string{"build"}, flags...), "-o", bin, ".")
		return exec.CommandContext(ctx, gobin, args...).CombinedOutput()
	}
	out, err := build()
	// When git refuses the repository (a checkout owned by another user is "dubious ownership"), go
	// build cannot stamp version control information. The version check below reads the binary's
	// own build info, so a build without it serves as well.
	if err != nil && bytes.Contains(out, []byte("error obtaining VCS status")) {
		t.Logf("go build: %s\nbuilding again with -buildvcs=false", bytes.TrimSpace(out))
		out, err = build("-buildvcs=false")
	}
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	dir := writeArtifact(t, 2)
	run := func(args ...string) (stdout, stderr string, code int) {
		cmd := exec.CommandContext(ctx, bin, args...)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err := cmd.Run()
		code = 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return o.String(), e.String(), code
	}
	cases := []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"help"}, 0},
		{[]string{"help", "render"}, 0},
		{[]string{"help", "nope"}, 2},
		{[]string{"nope"}, 2},
		{[]string{"version"}, 0},
		{[]string{"version", "x"}, 2},
		{[]string{"render"}, 2},
		{[]string{"render", "-max-records", "10", "d"}, 2},
		{[]string{"render", "/does/not/exist"}, 1},
		{[]string{"render", dir}, 0},
		{[]string{"render", "-h"}, 0},
	}
	for _, c := range cases {
		stdout, stderr, code := run(c.args...)
		if code != c.code {
			t.Errorf("faultline %v: exit %d, want %d\n%s", c.args, code, c.code, stderr)
		}
		// Each of these writes to one stream only (ART-095): stdout on success, stderr otherwise.
		other := stdout
		if code == 0 {
			other = stderr
		}
		if other != "" {
			t.Errorf("faultline %v: exit %d, but the other stream has %q", c.args, code, other)
		}
	}
	if _, stderr, _ := run(); stderr != phase1Usage {
		t.Errorf("no-args usage:\n%s", stderr)
	}
	for _, h := range []string{"help", "-h", "-help", "--help"} {
		if stdout, _, code := run(h); stdout != phase1Usage || code != 0 {
			t.Errorf("faultline %s: exit %d\n%s", h, code, stdout)
		}
	}
	if stdout, _, _ := run("help", "render"); stdout != renderHelp {
		t.Errorf("help render:\n%s", stdout)
	}
	for _, args := range [][]string{{"help", "nope"}, {"nope"}} {
		if _, stderr, _ := run(args...); stderr != unknownText("nope") {
			t.Errorf("faultline %v: %q", args, stderr)
		}
	}
	// The version is the binary's own build info (ART-097).
	bi, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	wantVersion := fmt.Sprintf("faultline %s %s %s/%s\n", bi.Main.Version, bi.GoVersion, runtime.GOOS, runtime.GOARCH)
	if stdout, _, _ := run("version"); stdout != wantVersion || !regexp.MustCompile(`^faultline \S+ go\S+ \w+/\w+\n$`).MatchString(stdout) {
		t.Errorf("version: %q, want %q", stdout, wantVersion)
	}
	if _, stderr, _ := run("render"); !strings.HasPrefix(stderr, "faultline render: want exactly one artifact directory\nusage: faultline render") {
		t.Errorf("render without argument: %q", stderr)
	}
	if _, stderr, _ := run("render", "-max-records", "10", "d"); !strings.HasPrefix(stderr, "faultline render: -max-records must be at least 1000\n") {
		t.Errorf("render -max-records 10: %q", stderr)
	}
	if _, stderr, code := run("render", "-slice-cap", "0", "d"); !strings.HasPrefix(stderr, "faultline render: -slice-cap must be at least 1\n") || code != 2 {
		t.Errorf("render -slice-cap 0: exit %d, %q", code, stderr)
	}
	if _, stderr, _ := run("render", "/does/not/exist"); !strings.HasPrefix(stderr, "faultline render: artifact: render /does/not/exist: ") {
		t.Errorf("render missing dir: %q", stderr)
	}
	stdout, _, _ := run("render", dir)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	want := []string{"timeline.txt", "hb.mmd", "timeline.html"}
	if len(lines) != 3 {
		t.Fatalf("render printed %q", stdout)
	}
	for i, l := range lines {
		if !filepath.IsAbs(l) || filepath.Base(l) != want[i] {
			t.Errorf("line %d = %q", i, l)
		}
		if _, err := os.Stat(l); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
}

// TestRunInProcess compares both streams and the exit code of every invocation exactly, without
// building a binary.
func TestRunInProcess(t *testing.T) {
	dir := writeArtifact(t, 2)
	paths := ""
	for _, n := range []string{"timeline.txt", "hb.mmd", "timeline.html"} {
		paths += filepath.Join(dir, n) + "\n"
	}
	renderErr := func(message string) string { return "faultline render: " + message + "\n" + renderFlagUsage }
	cases := []struct {
		args           []string
		stdout, stderr string
		code           int
	}{
		{nil, "", phase1Usage, 2},
		{[]string{"help"}, phase1Usage, "", 0},
		{[]string{"-h"}, phase1Usage, "", 0},
		{[]string{"-help"}, phase1Usage, "", 0},
		{[]string{"--help"}, phase1Usage, "", 0},
		{[]string{"nope"}, "", unknownText("nope"), 2},
		{[]string{"help", "render"}, renderHelp, "", 0},
		{[]string{"help", "version"}, "usage: faultline version\n\nVersion prints the faultline version, the Go version, and the platform.\n", "", 0},
		{[]string{"help", "help"}, "usage: faultline help [command]\n\nHelp prints the list of commands, or the usage and help text of one command.\n", "", 0},
		{[]string{"help", "nope"}, "", unknownText("nope"), 2},
		{[]string{"help", "a", "b"}, "", "faultline help: want at most one command\nusage: faultline help [command]\n", 2},
		// help takes no flags (ART-096), so -h is a command name it does not know.
		{[]string{"help", "-h"}, "", unknownText("-h"), 2},
		{[]string{"version", "x"}, "", "faultline version: unexpected arguments\nusage: faultline version\n", 2},
		{[]string{"version", "-h"}, "usage: faultline version\n", "", 0},
		{[]string{"render"}, "", renderErr("want exactly one artifact directory"), 2},
		{[]string{"render", "a", "b"}, "", renderErr("want exactly one artifact directory"), 2},
		{[]string{"render", "a", "-slice-cap", "5"}, "", renderErr("flags must come before the artifact directory"), 2},
		{[]string{"render", "a", "b", "-h"}, "", renderErr("flags must come before the artifact directory"), 2},
		// "-" ends the flags: only the arguments after the first are checked for flags (ART-098).
		{[]string{"render", "-", "x"}, "", renderErr("want exactly one artifact directory"), 2},
		{[]string{"render", "-slice-cap", "0", "d"}, "", renderErr("-slice-cap must be at least 1"), 2},
		{[]string{"render", "-max-records", "999", "d"}, "", renderErr("-max-records must be at least 1000"), 2},
		{[]string{"render", "-h"}, renderFlagUsage, "", 0},
		{[]string{"render", "-help", "d"}, renderFlagUsage, "", 0},
		{[]string{"render", "-nope", "d"}, "", "flag provided but not defined: -nope\n" + renderFlagUsage, 2},
		{[]string{"render", dir}, paths, "", 0},
		{[]string{"render", "-slice-cap", "1", dir}, paths, "", 0},
		{[]string{"render", "-max-records", "1000", dir}, paths, "", 0},
	}
	for _, c := range cases {
		var o, e bytes.Buffer
		if code := run(c.args, &o, &e); code != c.code || o.String() != c.stdout || e.String() != c.stderr {
			t.Errorf("faultline %q: exit %d, stdout %q, stderr %q; want %d, %q, %q", c.args, code, o.String(), e.String(), c.code, c.stdout, c.stderr)
		}
	}
}

// TestRenderOptions checks that -slice-cap and -max-records reach artifact.Render: on a chain of
// 1,500 records, the files the CLI writes equal those of Render with the same options, which
// differ from those of the defaults. Every run uses one directory, since timeline.html embeds it.
func TestRenderOptions(t *testing.T) {
	dir := writeArtifact(t, 1500)
	read := func() [2][]byte {
		var files [2][]byte
		for i, n := range []string{artifact.FileHB, artifact.FileTimelineHTML} {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				t.Fatal(err)
			}
			files[i] = b
		}
		return files
	}
	var e bytes.Buffer
	if code := run([]string{"render", "-slice-cap", "1", "-max-records", "1000", dir}, io.Discard, &e); code != 0 {
		t.Fatalf("render: exit %d, %s", code, e.String())
	}
	cli := read()
	if _, err := artifact.Render(dir, artifact.RenderOptions{SliceCap: 1, TimelineRecords: 1000}); err != nil {
		t.Fatal(err)
	}
	lib := read()
	if _, err := artifact.Render(dir, artifact.RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	defaults := read()
	for i, n := range []string{artifact.FileHB, artifact.FileTimelineHTML} {
		if !bytes.Equal(cli[i], lib[i]) {
			t.Errorf("%s: the CLI's differs from Render's with the same options", n)
		}
		if bytes.Equal(lib[i], defaults[i]) {
			t.Errorf("%s: the options change nothing, so this test proves nothing", n)
		}
	}
}

// ART-099: render prints absolute paths, also for a relative directory.
func TestRenderRelativeDir(t *testing.T) {
	dir := writeArtifact(t, 2)
	t.Chdir(filepath.Dir(dir))
	want := ""
	for _, n := range []string{"timeline.txt", "hb.mmd", "timeline.html"} {
		want += filepath.Join(dir, n) + "\n"
	}
	var o, e bytes.Buffer
	if code := run([]string{"render", filepath.Base(dir)}, &o, &e); code != 0 || o.String() != want {
		t.Errorf("render %s: exit %d, stdout %q, stderr %q; want stdout %q", filepath.Base(dir), code, o.String(), e.String(), want)
	}
}
