// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Command faultline-toolexec is the -toolexec wrapper of prototype B of the exact backend: it
// rewrites a test binary's packages to call the seeded runtime sched at every goroutine start,
// channel operation, lock, map range and math/rand call. Run faultline-toolexec prepare once per
// toolchain, then use it as go test -toolexec faultline-toolexec.
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
	"runtime"
	"strings"
)

// version is appended to every tool's -V=full line (EXB-032), followed by
// FAULTLINE_TOOLEXEC_STD's value when that is set. The line is part of the go command's tool IDs,
// so change version whenever the rewriter or the rewrite set changes: otherwise cached packages
// compiled by an older rewriter are reused.
const version = "0.1.1-spike"

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
	if len(os.Args) < 2 {
		fail("usage: faultline-toolexec prepare | faultline-toolexec <tool> [args...]")
	}
	tool, args := os.Args[1], os.Args[2:]
	base := strings.TrimSuffix(filepath.Base(tool), ".exe")
	ip := os.Getenv("TOOLEXEC_IMPORTPATH")
	switch {
	case len(args) == 1 && args[0] == "-V=full":
		out, err := exec.Command(tool, args...).Output() //nolint:gosec // runs the tool the go command names, as -toolexec does
		if err != nil {
			fail("%s -V=full: %v", tool, err)
		}
		id := "faultline-toolexec=" + version
		if std := os.Getenv("FAULTLINE_TOOLEXEC_STD"); std != "" {
			id += ",std=" + std // the listed packages are rewritten, so they need their own cache keys
		}
		fmt.Printf("%s %s\n", strings.TrimSpace(string(out)), id)
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

// stateDir is $TMPDIR/faultline-toolexec.
func stateDir() string { return filepath.Join(os.TempDir(), "faultline-toolexec") }

func target() (goos, goarch string) {
	goos, goarch = os.Getenv("GOOS"), os.Getenv("GOARCH")
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

// prepare writes the export data of sched and its dependencies (EXB-031). It runs in the
// runner/toolexec module directory.
func prepare() error {
	env, err := exec.Command("go", "env", "GOVERSION", "GOOS", "GOARCH", "GOROOT", "GOMODCACHE").Output()
	if err != nil {
		return fmt.Errorf("go env: %v", err)
	}
	f := strings.Fields(string(env))
	if len(f) != 5 {
		return fmt.Errorf("go env: unexpected output %q", env)
	}
	out, err := exec.Command("go", "list", "-deps", "-export", "-f",
		"{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}", schedPath).Output()
	if err != nil {
		return fmt.Errorf("go list %s: %v", schedPath, err)
	}
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	key := f[0] + "-" + f[1] + "-" + f[2]
	if err := writeAtomic(filepath.Join(stateDir(), key+".importcfg"), out); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(stateDir(), key+".env"), []byte("GOROOT="+f[3]+"\nGOMODCACHE="+f[4]+"\n"))
}

// writeAtomic writes b to a temporary file next to name and renames it to name, so that a
// compile reading name sees the old file or the new one, never a part of it.
func writeAtomic(name string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// prepared returns the prepared importcfg lines and GOROOT/GOMODCACHE for goVersion. A missing
// file fails the compile, and so does a file that is not what prepare writes.
func prepared(goVersion string) (lines []string, roots map[string]string) {
	goos, goarch := target()
	key := goVersion + "-" + goos + "-" + goarch
	again := fmt.Sprintf(`run "faultline-toolexec prepare" for %s %s/%s`, goVersion, goos, goarch)
	cfg, env := filepath.Join(stateDir(), key+".importcfg"), filepath.Join(stateDir(), key+".env")
	b, err := os.ReadFile(cfg) //nolint:gosec // prepare's file in the state directory
	if err != nil {
		fail("%s", again)
	}
	e, err := os.ReadFile(env) //nolint:gosec // prepare's file in the state directory
	if err != nil {
		fail("%s", again)
	}
	if lines, err = preparedLines(string(b)); err != nil {
		fail("%s: %v; %s", cfg, err, again)
	}
	if roots, err = preparedRoots(string(e)); err != nil {
		fail("%s: %v; %s", env, err, again)
	}
	return lines, roots
}

// preparedLines checks that s is an importcfg as prepare writes it: whole packagefile lines with
// an absolute file, sched's among them. go list prints sched last, so a file cut short at a line
// end lacks it.
func preparedLines(s string) ([]string, error) {
	if !strings.HasSuffix(s, "\n") {
		return nil, errors.New("the file does not end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	sched := false
	for _, l := range lines {
		rest, ok := strings.CutPrefix(l, "packagefile ")
		p, f, ok2 := strings.Cut(rest, "=")
		if !ok || !ok2 || p == "" || !filepath.IsAbs(f) {
			return nil, fmt.Errorf("line %q is not a packagefile line", l)
		}
		sched = sched || p == schedPath
	}
	if !sched {
		return nil, fmt.Errorf("no export data for %s", schedPath)
	}
	return lines, nil
}

// preparedRoots reads the GOROOT and GOMODCACHE lines that prepare writes, both absolute paths.
func preparedRoots(s string) (map[string]string, error) {
	lines := strings.Split(s, "\n")
	if len(lines) != 3 || lines[2] != "" {
		return nil, errors.New("want a GOROOT line and a GOMODCACHE line")
	}
	roots := map[string]string{}
	for i, k := range []string{"GOROOT", "GOMODCACHE"} {
		v, ok := strings.CutPrefix(lines[i], k+"=")
		if !ok || !filepath.IsAbs(v) {
			return nil, fmt.Errorf("line %q: want %s=<absolute path>", lines[i], k)
		}
		roots[k] = v
	}
	return roots, nil
}

// toolGoVersion returns the "go1.x.y" field of tool -V=full.
func toolGoVersion(tool string) string {
	out, err := exec.Command(tool, "-V=full").Output() //nolint:gosec // runs the tool the go command names, as -toolexec does
	if err != nil {
		fail("%s -V=full: %v", tool, err)
	}
	f := strings.Fields(string(out))
	if len(f) < 3 {
		fail("%s -V=full: unexpected output %q", tool, out)
	}
	return f[2]
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

// link adds the prepared packages to the link importcfg (EXB-032).
func link(tool string, args []string) {
	cfg, i := flagValue(args, "-importcfg")
	if i < 0 {
		run(tool, args)
	}
	lines, _ := prepared(toolGoVersion(tool))
	dir, err := os.MkdirTemp("", "faultline-toolexec-link-")
	if err != nil {
		fail("%v", err)
	}
	merged, err := mergeImportcfg(cfg, lines, dir)
	if err != nil {
		fail("%v", err)
	}
	args = append([]string(nil), args...)
	if args[i] == cfg {
		args[i] = merged
	} else {
		args[i] = "-importcfg=" + merged
	}
	cmd := exec.Command(tool, args...) //nolint:gosec // runs the tool the go command names, as -toolexec does
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	os.RemoveAll(dir)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	} else if err != nil {
		fail("%v", err)
	}
}

const (
	faultlineRoot = "github.com/hmdsefi/faultline"
	corpusModule  = faultlineRoot + "/internal/spike/exb"
	goschedPath   = faultlineRoot + "/internal/gosched"
)

// moduleOf returns the module path of the go.mod nearest above dir, or "".
func moduleOf(dir string) string {
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // the go.mod files above the package's sources
		if err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
					return strings.Trim(strings.TrimSpace(rest), `"`)
				}
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func under(path, root string) bool {
	return root != "" && (path == root || strings.HasPrefix(path, root+string(filepath.Separator)))
}

// rewriteFiles returns the files of a compile that are rewritten (EXB-033): none for the standard
// library (unless FAULTLINE_TOOLEXEC_STD lists the package), module cache code, test mains, sched
// and gosched; only the _test.go files of faultline's own modules (except the corpus module);
// every file of any other local module.
func rewriteFiles(ip string, files []string, roots map[string]string) map[string]bool {
	pkg, _, _ := strings.Cut(ip, " ")
	if len(files) == 0 || strings.HasSuffix(pkg, ".test") || pkg == schedPath || pkg == goschedPath {
		return nil
	}
	sel := map[string]bool{}
	all := func() map[string]bool {
		for _, f := range files {
			sel[f] = true
		}
		return sel
	}
	first, _ := filepath.Abs(files[0])
	if under(first, filepath.Join(roots["GOROOT"], "src")) {
		for _, s := range strings.Split(os.Getenv("FAULTLINE_TOOLEXEC_STD"), ",") {
			if s != "" && s == pkg {
				return all()
			}
		}
		return nil
	}
	if under(first, roots["GOMODCACHE"]) {
		return nil
	}
	m := moduleOf(filepath.Dir(first))
	if (m == faultlineRoot || strings.HasPrefix(m, faultlineRoot+"/")) && !strings.HasPrefix(m, corpusModule) {
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				sel[f] = true
			}
		}
		return sel
	}
	return all()
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
	lines, roots := prepared(goVersion)
	sel := rewriteFiles(ip, files, roots)
	if len(sel) == 0 {
		logf("compile %s unchanged", ip)
		run(tool, args)
	}
	cfg, ci := flagValue(args, "-importcfg")
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
	dir, err := os.MkdirTemp("", "faultline-toolexec-")
	if err != nil {
		fail("%v", err)
	}
	var gaps []string
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
		gaps = append(gaps, rw.gaps...)
		if len(rw.edits) == 0 {
			continue
		}
		changed = true
		out, text := filepath.Join(dir, fmt.Sprintf("%d_%s", i, filepath.Base(a))), []byte(rw.output())
		if err := os.WriteFile(out, text, 0o600); err != nil {
			fail("%v", err)
		}
		if keep := os.Getenv("FAULTLINE_TOOLEXEC_KEEP"); keep != "" {
			kd := filepath.Join(keep, strings.NewReplacer("/", "_", " ", "_", "[", "", "]", "").Replace(ip))
			if err := os.MkdirAll(kd, 0o750); err != nil { //nolint:gosec // the directory FAULTLINE_TOOLEXEC_KEEP names
				fail("%v", err)
			}
			if err := os.WriteFile(filepath.Join(kd, filepath.Base(a)), text, 0o600); err != nil { //nolint:gosec // the directory FAULTLINE_TOOLEXEC_KEEP names
				fail("%v", err)
			}
		}
		newArgs[i] = out
	}
	recordGaps(gaps)
	if !changed {
		os.RemoveAll(dir)
		logf("compile %s unchanged", ip)
		run(tool, args)
	}
	logf("compile %s rewritten", ip)
	merged, err := mergeImportcfg(cfg, lines, dir)
	if err != nil {
		fail("%v", err)
	}
	if newArgs[ci] == cfg {
		newArgs[ci] = merged
	} else {
		newArgs[ci] = "-importcfg=" + merged
	}
	cmd := exec.Command(tool, newArgs...) //nolint:gosec // runs the tool the go command names, as -toolexec does
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	os.RemoveAll(dir)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	} else if err != nil {
		fail("%v", err)
	}
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

// recordGaps appends coverage gaps to $TMPDIR/faultline-toolexec/gaps.txt in one write, so that
// concurrent compiles do not interleave their lines. The directory exists: prepare created it.
func recordGaps(gaps []string) {
	if len(gaps) == 0 {
		return
	}
	f, err := os.OpenFile(filepath.Join(stateDir(), "gaps.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fail("recording coverage gaps: %v", err)
	}
	_, err = f.WriteString(strings.Join(gaps, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fail("recording coverage gaps: %v", err)
	}
}
