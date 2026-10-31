// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// The faultline-toolexec command is the -toolexec wrapper of faultline's exact backend. It rewrites
// the packages of a test binary so that goroutine starts, channel operations, select, sync
// primitives, map ranges and math/rand calls go through the seeded scheduler in package sched.
//
// In the module under test, add the command as a tool, prepare the scheduler, then build through
// the wrapper:
//
//	go get -tool github.com/hmdsefi/faultline/runner/toolexec/cmd/faultline-toolexec
//	go tool faultline-toolexec prepare
//	go test -toolexec="go tool faultline-toolexec" ./...
//
// Run prepare again after you change the Go toolchain or the faultline version.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// version is appended to every tool's -V=full line, followed by the hash of the prepared state
// and, when they are set, the FAULTLINE_TOOLEXEC_STD and FAULTLINE_TOOLEXEC_DEPS lists (XTE-021).
// The line is part of the go command's tool IDs, so change version whenever the rewriter, the
// rewrite set or a generated file changes (XTE-022): otherwise cached packages compiled by an
// older rewriter are reused.
const version = "5"

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "faultline-toolexec: "+format+"\n", args...)
	os.Exit(2)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "prepare" {
		if err := prepare(); err != nil {
			fail("%v", err)
		}
		return
	}
	if len(os.Args) < 2 || os.Args[1] == "prepare" {
		fail("usage: faultline-toolexec prepare | faultline-toolexec <tool> [args...]")
	}
	tool, args := os.Args[1], os.Args[2:]
	base := strings.TrimSuffix(filepath.Base(tool), ".exe")
	ip := os.Getenv("TOOLEXEC_IMPORTPATH")
	switch {
	case len(args) == 1 && args[0] == "-V=full":
		versionLine(tool)
		return
	case base == "compile":
		compile(tool, args)
		return
	case base == "link":
		logf("link %s", ip)
		link(tool, args)
		return
	}
	run(tool, args)
}

// logf appends one line to FAULTLINE_TOOLEXEC_LOG, if set.
func logf(format string, args ...any) {
	p := os.Getenv("FAULTLINE_TOOLEXEC_LOG")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // the log file the user names
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}

// run runs a tool and exits with its exit code.
func run(tool string, args []string) {
	cmd := exec.Command(tool, args...) //nolint:gosec // runs the tool the go command names, as -toolexec does
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		os.Exit(ee.ExitCode())
	case err != nil:
		fail("%v", err)
	}
	os.Exit(0)
}

// mergeImportcfg writes a copy of cfg with the prepared lines whose import path it lacks.
func mergeImportcfg(cfg string, extra []string, dir string) (string, error) {
	b, err := os.ReadFile(cfg) //nolint:gosec // the importcfg the go command passes
	if err != nil {
		return "", err
	}
	have := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "packagefile "); ok {
			p, _, _ := strings.Cut(rest, "=")
			have[p] = true
		}
	}
	var out bytes.Buffer
	out.Write(b)
	if len(b) > 0 && b[len(b)-1] != '\n' {
		out.WriteByte('\n')
	}
	for _, l := range extra {
		rest, ok := strings.CutPrefix(l, "packagefile ")
		if !ok {
			continue
		}
		p, _, _ := strings.Cut(rest, "=")
		if !have[p] {
			out.WriteString(l + "\n")
		}
	}
	name := filepath.Join(dir, "importcfg")
	return name, os.WriteFile(name, out.Bytes(), 0o600)
}

func flagValue(args []string, name string) (string, int) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], i + 1
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, i
		}
	}
	return "", -1
}

// staleState is XTE-014's message after a fingerprint mismatch of sched or internal/gosched.
const staleState = `the prepared sched does not match this build (another checkout or version of faultline, or other build flags); run "faultline-toolexec prepare" in this module with the build's flags, or give each checkout its own FAULTLINE_TOOLEXEC_STATE`

// tempDir creates the directory of a compile's or link's files.
func tempDir() string {
	dir, err := os.MkdirTemp("", "faultline-toolexec-")
	if err != nil {
		fail("%v", err)
	}
	return dir
}

// keep copies a file the wrapper wrote for package ip to FAULTLINE_TOOLEXEC_KEEP, if set; a
// failure removes dir and fails the compile.
func keep(ip, name string, text []byte, dir string) {
	k := os.Getenv("FAULTLINE_TOOLEXEC_KEEP")
	if k == "" {
		return
	}
	kd := filepath.Join(k, strings.NewReplacer("/", "_", " ", "_", "[", "", "]", "").Replace(ip))
	err := os.MkdirAll(kd, 0o750) //nolint:gosec // the directory FAULTLINE_TOOLEXEC_KEEP names
	if err == nil {
		err = os.WriteFile(filepath.Join(kd, name), text, 0o600) //nolint:gosec // the directory FAULTLINE_TOOLEXEC_KEEP names
	}
	if err != nil {
		os.RemoveAll(dir)
		fail("%v", err)
	}
}

// generate writes a generated file of package ip into dir, keeps a copy, and returns its path.
func generate(ip, dir, name, text string) string {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		os.RemoveAll(dir)
		fail("%v", err)
	}
	keep(ip, name, []byte(text), dir)
	return p
}

// runCompile runs compile with args, the extra files appended and an importcfg that adds the
// prepared lines; it removes dir and exits with the tool's exit code.
func runCompile(tool string, args, extra, lines []string, dir string) {
	args = append(append([]string(nil), args...), extra...)
	if cfg, i := flagValue(args, "-importcfg"); i >= 0 {
		merged, err := mergeImportcfg(cfg, lines, dir)
		if err != nil {
			os.RemoveAll(dir)
			fail("%v", err)
		}
		if args[i] == cfg {
			args[i] = merged
		} else {
			args[i] = "-importcfg=" + merged
		}
	}
	runIn(dir, tool, args, false)
}

// runIn runs tool, removes dir and exits with the tool's exit code. For a link, a fingerprint
// mismatch of sched or internal/gosched adds the stale-state message (XTE-014).
func runIn(dir, tool string, args []string, linking bool) {
	var stderr bytes.Buffer
	cmd := exec.Command(tool, args...) //nolint:gosec // runs the tool the go command names, as -toolexec does
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, &stderr)
	err := cmd.Run()
	os.RemoveAll(dir)
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		if e := stderr.String(); linking && strings.Contains(e, "fingerprint mismatch") &&
			(strings.Contains(e, schedPath) || strings.Contains(e, goschedPath)) {
			fail("%s", staleState)
		}
		os.Exit(ee.ExitCode())
	case err != nil:
		fail("%v", err)
	}
	os.Exit(0)
}

// wrapperValue is the -toolexec value of this wrapper for replay commands (XTE-024): its base
// name when PATH finds the same file, else its absolute path.
func wrapperValue() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if e, err := filepath.EvalSymlinks(exe); err == nil {
		exe = e
	}
	base := filepath.Base(exe)
	if p, err := exec.LookPath(base); err == nil {
		if q, err := filepath.EvalSymlinks(p); err == nil && q == exe {
			return base
		}
	}
	return exe
}

// link links with the prepared packages added to importcfg.link and the wrapper's path stored
// for replay commands (XTE-020, XTE-024).
func link(tool string, args []string) {
	cfg, i := flagValue(args, "-importcfg")
	if i < 0 {
		run(tool, args)
	}
	lines, _ := prepared(toolGoVersion(tool), slices.Contains(args, "-race"))
	dir := tempDir()
	merged, err := mergeImportcfg(cfg, lines, dir)
	if err != nil {
		os.RemoveAll(dir)
		fail("%v", err)
	}
	args = append([]string(nil), args...)
	if args[i] == cfg {
		args[i] = merged
	} else {
		args[i] = "-importcfg=" + merged
	}
	if w := wrapperValue(); w != "" {
		args = append([]string{"-X", goschedPath + ".Wrapper=" + w}, args...)
	}
	runIn(dir, tool, args, true)
}

// compile rewrites the selected files of a compile in the rewrite set and runs compile.
func compile(tool string, args []string) {
	var files []string
	for _, a := range args {
		if strings.HasSuffix(a, ".go") && !strings.HasPrefix(a, "-") {
			files = append(files, a)
		}
	}
	ip := os.Getenv("TOOLEXEC_IMPORTPATH")
	goVersion, _ := flagValue(args, "-goversion")
	if goVersion == "" {
		run(tool, args)
	}
	lines, roots := prepared(goVersion, slices.Contains(args, "-race"))
	if pkg, _, _ := strings.Cut(ip, " "); strings.HasSuffix(pkg, ".test") { // XTE-023
		logf("compile %s testmain", ip)
		dir := tempDir()
		gen := generate(ip, dir, "__fl_testmain.go", "package main\n\nimport _ \""+schedPath+"\"\n")
		runCompile(tool, args, []string{gen}, lines, dir)
	}
	sel, outside := rewriteFiles(ip, files, roots)
	if outside { // XTE-030 rule 2, XTE-050
		logf("compile %s outside-module", ip)
		dir := tempDir()
		abs, _ := filepath.Abs(files[0])
		f, err := parser.ParseFile(token.NewFileSet(), files[0], nil, parser.PackageClauseOnly)
		if err != nil {
			os.RemoveAll(dir)
			fail("%s: %v", ip, err)
		}
		text := "package " + f.Name.Name + "\n\nimport __fl_sched \"" + schedPath + "\"\n\nvar _ = __fl_sched.Gaps(" +
			strconv.Quote(abs+":1: sources outside a module") + ")\n"
		runCompile(tool, args, []string{generate(ip, dir, "__fl_gaps.go", text)}, lines, dir)
	}
	if len(sel) == 0 {
		logf("compile %s unchanged", ip)
		run(tool, args)
	}
	cfg, _ := flagValue(args, "-importcfg")
	pkgPath, _ := flagValue(args, "-p")
	lang, _ := flagValue(args, "-lang")
	exports, importmap := readImportcfg(cfg)
	fset := token.NewFileSet()
	var asts []*ast.File
	srcs := map[*ast.File][]byte{}
	for _, f := range files {
		src, err := os.ReadFile(f) //nolint:gosec // the source files the go command passes
		if err != nil {
			fail("%s: %v", ip, err)
		}
		af, err := parser.ParseFile(fset, f, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			fail("%s: %v", ip, err)
		}
		asts = append(asts, af)
		srcs[af] = src
	}
	lookup := func(path string) (io.ReadCloser, error) {
		if p, ok := importmap[path]; ok {
			path = p
		}
		f, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(f)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", lookup), GoVersion: lang, FakeImportC: true}
	tpkg, err := conf.Check(pkgPath, fset, asts, info)
	if err != nil {
		fail("%s: %v", ip, err)
	}
	dir := tempDir()
	changed := false
	newArgs := append([]string(nil), args...)
	for i, a := range newArgs {
		if !sel[a] {
			continue
		}
		af := asts[indexOf(files, a)]
		abs, _ := filepath.Abs(a)
		rw := &rewriter{fset: fset, tf: fset.File(af.Pos()), file: af, src: srcs[af], info: info, pkg: tpkg, path: abs}
		rw.rewrite()
		if len(rw.edits) == 0 && len(rw.gaps) == 0 {
			continue
		}
		changed = true
		out, text := filepath.Join(dir, fmt.Sprintf("%d_%s", i, filepath.Base(a))), []byte(rw.output())
		if err := os.WriteFile(out, text, 0o600); err != nil {
			os.RemoveAll(dir)
			fail("%v", err)
		}
		keep(ip, filepath.Base(a), text, dir)
		newArgs[i] = out
	}
	if !changed {
		os.RemoveAll(dir)
		logf("compile %s unchanged", ip)
		run(tool, args)
	}
	logf("compile %s rewritten", ip)
	runCompile(tool, newArgs, nil, lines, dir)
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// readImportcfg parses packagefile and importmap lines.
func readImportcfg(name string) (exports, importmap map[string]string) {
	exports, importmap = map[string]string{}, map[string]string{}
	b, err := os.ReadFile(name) //nolint:gosec // the importcfg the go command passes
	if err != nil {
		fail("%v", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "packagefile "); ok {
			if k, v, ok := strings.Cut(rest, "="); ok {
				exports[k] = v
			}
		} else if rest, ok := strings.CutPrefix(l, "importmap "); ok {
			if k, v, ok := strings.Cut(rest, "="); ok {
				importmap[k] = v
			}
		}
	}
	return exports, importmap
}
