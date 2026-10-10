// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// forbiddenImports are packages non-test harness code must not import: crypto/rand,
// the output packages (ETC-002: any write to stdout or stderr; log/slog's default
// handler writes to stderr) and the proto text formats (ETC-005: their output is
// not stable across builds).
var forbiddenImports = []string{
	"crypto/rand", "log", "log/slog",
	"google.golang.org/protobuf/encoding/prototext", "google.golang.org/protobuf/encoding/protojson",
}

// forbiddenFuncs are the package-level functions ETC-002 forbids, by import path. Every
// package-level function of math/rand and math/rand/v2 is forbidden too (methods on
// kernel streams are not).
var forbiddenFuncs = map[string][]string{
	"time": {"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick"},
	"os":   {"Getenv", "LookupEnv"},
	"fmt":  {"Print", "Printf", "Println"},
}

var (
	// mapIterFuncs list a map in random order; slices.Sorted around them is fine.
	mapIterFuncs = []string{"All", "Keys", "Values"}
	sortedFuncs  = []string{"Sorted", "SortedFunc", "SortedStableFunc"}
	// unorderedMethods list a map in random order, by types.Func.FullName.
	unorderedMethods = []string{"(reflect.Value).MapRange", "(reflect.Value).MapKeys", "(*sync.Map).Range"}
	// formatVerbs call the String method of the argument (fmt's handleMethods).
	formatVerbs = "vsqxX"
)

// finding is one violation: where it is and what it says.
type finding struct {
	pos token.Position
	msg string
}

func (f finding) String() string { return fmt.Sprintf("%s: %s", f.pos, f.msg) }

// AT-ETC-26: ETC-002 and ETC-005 over every non-test Go file of the module.
func TestDeterminismRules(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	imp := sourceImporter(t, fset)
	var findings []finding
	err = filepath.WalkDir(root, func(dir string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if name := d.Name(); dir != root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return filepath.SkipDir
		}
		f, err := lintDir(fset, imp, dir)
		findings = append(findings, f...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("%d determinism finding(s):\n%s", len(findings), joinFindings(findings))
	}
}

// TestDeterminismRulesCatch proves the linter is not vacuous: every line of
// testdata/lintbad that ends in a `// want` comment must have exactly one finding per
// backquoted text, containing it, and no other line may have a finding. The fixture
// breaks every rule at least once, in each form the rule must see: a call, a function
// value, a dot import and an explicit instantiation.
func TestDeterminismRulesCatch(t *testing.T) {
	fset := token.NewFileSet()
	dir := fixtureDir(t, "lintbad")
	findings, err := lintDir(fset, sourceImporter(t, fset), dir)
	if err != nil {
		t.Fatal(err)
	}
	wants := wantComments(t, dir)
	if len(wants) < 80 {
		t.Fatalf("only %d `// want` lines in %s: the fixture was trimmed", len(wants), dir)
	}
	got := map[string][]string{} // "file.go:line" -> messages
	for _, f := range findings {
		key := fmt.Sprintf("%s:%d", filepath.Base(f.pos.Filename), f.pos.Line)
		got[key] = append(got[key], f.msg)
	}
	for key, want := range wants {
		msgs := got[key]
		if len(msgs) != len(want) {
			t.Errorf("%s: %d finding(s) %q, want %d matching %q", key, len(msgs), msgs, len(want), want)
			continue
		}
		for _, w := range want {
			if !slices.ContainsFunc(msgs, func(m string) bool { return strings.Contains(m, w) }) {
				t.Errorf("%s: no finding contains %q in %q", key, w, msgs)
			}
		}
	}
	for key, msgs := range got {
		if _, ok := wants[key]; !ok {
			t.Errorf("%s: unexpected finding(s) %q", key, msgs)
		}
	}
}

// TestDeterminismRulesAllow is the negative control: testdata/lintok holds code the
// rules must accept (methods on kernel streams, sorted iteration, Duration arithmetic,
// %v of errors, ...), so a rule that grows too wide fails here.
func TestDeterminismRulesAllow(t *testing.T) {
	fset := token.NewFileSet()
	dir := fixtureDir(t, "lintok")
	if files, _ := filepath.Glob(filepath.Join(dir, "*.go")); len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	findings, err := lintDir(fset, sourceImporter(t, fset), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("%d finding(s) in code that is allowed:\n%s", len(findings), joinFindings(findings))
	}
}

func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

var wantRE = regexp.MustCompile("`([^`]*)`")

// wantComments returns, for each line of the .go files in dir that has a `// want`
// comment, the backquoted texts of the comment, keyed "file.go:line".
func wantComments(t *testing.T, dir string) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string][]string{}
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			_, comment, ok := strings.Cut(line, "// want ")
			if !ok {
				continue
			}
			var texts []string
			for _, m := range wantRE.FindAllStringSubmatch(comment, -1) {
				texts = append(texts, m[1])
			}
			if len(texts) == 0 {
				t.Fatalf("%s:%d: `// want` without a backquoted text", name, i+1)
			}
			wants[fmt.Sprintf("%s:%d", filepath.Base(name), i+1)] = texts
		}
	}
	return wants
}

func joinFindings(fs []finding) string {
	var lines []string
	for _, f := range fs {
		lines = append(lines, f.String())
	}
	return strings.Join(lines, "\n")
}

// sourceImporter returns the importer DET-015 uses: it type-checks imports from source.
func sourceImporter(t *testing.T, fset *token.FileSet) types.ImporterFrom {
	t.Helper()
	imp, ok := importer.ForCompiler(fset, "source", nil).(types.ImporterFrom)
	if !ok {
		t.Fatal("the source importer is not a types.ImporterFrom")
	}
	return imp
}

// lintDir parses and type-checks the non-test files of the package in dir and returns
// its findings.
func lintDir(fset *token.FileSet, imp types.ImporterFrom, dir string) ([]finding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File // os.ReadDir sorts by name
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, nil
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(dir, fset, files, info); err != nil {
		return nil, fmt.Errorf("type-check %s: %w", dir, err)
	}
	var findings []finding
	for _, f := range files {
		findings = append(findings, lintFile(fset, f, info)...)
	}
	return findings, nil
}

// isProto reports whether t is a proto message type (it has a ProtoReflect method).
func isProto(t types.Type) bool {
	if t == nil {
		return false
	}
	obj, _, _ := types.LookupFieldOrMethod(t, true, nil, "ProtoReflect")
	_, ok := obj.(*types.Func)
	return ok
}

// containsProto reports whether t is a proto message or holds one: fmt reaches into
// pointers, slices, arrays, maps and struct fields and calls String on each message.
func containsProto(t types.Type) bool {
	return protoIn(t, map[types.Type]bool{})
}

func protoIn(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if isProto(t) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return protoIn(u.Elem(), seen)
	case *types.Slice:
		return protoIn(u.Elem(), seen)
	case *types.Array:
		return protoIn(u.Elem(), seen)
	case *types.Map:
		return protoIn(u.Key(), seen) || protoIn(u.Elem(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if protoIn(u.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

func isFloat(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsFloat|types.IsComplex) != 0
}

// isMap reports whether ranging over a value of type t ranges over a map: a map type, or
// a type parameter whose constraint allows a map type.
func isMap(t types.Type) bool {
	if _, ok := t.Underlying().(*types.Map); ok {
		return true
	}
	tp, ok := t.(*types.TypeParam)
	if !ok {
		return false
	}
	iface, ok := tp.Constraint().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	for i := 0; i < iface.NumEmbeddeds(); i++ {
		switch e := iface.EmbeddedType(i).(type) {
		case *types.Union:
			for j := 0; j < e.Len(); j++ {
				if _, ok := e.Term(j).Type().Underlying().(*types.Map); ok {
					return true
				}
			}
		default:
			if _, ok := e.Underlying().(*types.Map); ok {
				return true
			}
		}
	}
	return false
}

// calledFunc returns the function or method a call expression's Fun names, however it
// is written (pkg.F, a dot-imported F, F[T], (F)); nil for a function value or a conversion.
func calledFunc(info *types.Info, fun ast.Expr) *types.Func {
	for {
		switch x := fun.(type) {
		case *ast.ParenExpr:
			fun = x.X
		case *ast.IndexExpr:
			fun = x.X
		case *ast.IndexListExpr:
			fun = x.X
		case *ast.SelectorExpr:
			fn, _ := info.Uses[x.Sel].(*types.Func)
			return fn
		case *ast.Ident:
			fn, _ := info.Uses[x].(*types.Func)
			return fn
		default:
			return nil
		}
	}
}

// sortedArgument reports whether the identifier id (maps.All, maps.Keys or maps.Values) is
// called as the first argument of slices.Sorted, slices.SortedFunc or slices.SortedStableFunc.
func sortedArgument(id *ast.Ident, stack []ast.Node, info *types.Info) bool {
	i := len(stack) - 1
	var fun ast.Expr = id
	if i >= 0 {
		if sel, ok := stack[i].(*ast.SelectorExpr); ok && sel.Sel == id {
			fun = sel
			i--
		}
	}
	if i < 1 {
		return false
	}
	inner, ok := stack[i].(*ast.CallExpr)
	if !ok || inner.Fun != fun {
		return false
	}
	outer, ok := stack[i-1].(*ast.CallExpr)
	if !ok || len(outer.Args) == 0 || outer.Args[0] != inner {
		return false
	}
	fn := calledFunc(info, outer.Fun)
	return fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "slices" && slices.Contains(sortedFuncs, fn.Name())
}

func lintFile(fset *token.FileSet, f *ast.File, info *types.Info) []finding {
	var out []finding
	report := func(n ast.Node, format string, args ...any) {
		out = append(out, finding{fset.Position(n.Pos()), fmt.Sprintf(format, args...)})
	}
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); slices.Contains(forbiddenImports, p) {
			report(imp, "import %q", p)
		}
	}
	var floatEnd token.Pos // end of the last floating-point expression reported
	ast.PreorderStack(f, nil, func(n ast.Node, stack []ast.Node) bool {
		switch x := n.(type) {
		case *ast.GoStmt:
			report(x, "go statement")
		case *ast.SelectStmt:
			report(x, "select statement")
		case *ast.RangeStmt:
			if tv, ok := info.Types[x.X]; ok && isMap(tv.Type) {
				report(x, "range over a map")
			}
		case *ast.Ident:
			lintIdent(x, stack, info, report)
		case *ast.SelectorExpr:
			// String of a proto message: called, taken as a method value or as a method expression.
			if sel := info.Selections[x]; sel != nil && sel.Kind() != types.FieldVal && x.Sel.Name == "String" && isProto(sel.Recv()) {
				report(x, "String() of a proto message")
			}
		case *ast.CallExpr:
			lintFormat(info, x, report)
		}
		if e, ok := n.(ast.Expr); ok && e.Pos() >= floatEnd {
			if tv, ok := info.Types[e]; ok && tv.Type != nil && isFloat(tv.Type) {
				report(e, "floating-point value")
				floatEnd = e.End()
			}
		}
		return true
	})
	return out
}

// lintIdent applies the rules that name an identifier. It looks at every identifier and
// resolves it through types.Info.Uses, so a forbidden function is found however it is
// reached: called, taken as a function value, through a dot import or with an explicit
// type argument.
func lintIdent(id *ast.Ident, stack []ast.Node, info *types.Info, report func(ast.Node, string, ...any)) {
	switch obj := info.Uses[id].(type) {
	case *types.Builtin:
		if obj.Name() == "print" || obj.Name() == "println" {
			report(id, "builtin %s", obj.Name())
		}
	case *types.Var:
		if obj.Pkg() != nil && obj.Pkg().Path() == "os" && !obj.IsField() && (obj.Name() == "Stdout" || obj.Name() == "Stderr") {
			report(id, "use of os.%s", obj.Name())
		}
	case *types.Func:
		if obj.Pkg() == nil {
			return
		}
		pkg, name := obj.Pkg().Path(), obj.Name()
		switch {
		case obj.Signature().Recv() != nil:
			if slices.Contains(unorderedMethods, obj.FullName()) {
				report(id, "unordered iteration: %s", obj.FullName())
			}
		case slices.Contains(forbiddenFuncs[pkg], name), pkg == "math/rand", pkg == "math/rand/v2":
			report(id, "use of %s.%s", pkg, name)
		case pkg == "maps" && slices.Contains(mapIterFuncs, name) && !sortedArgument(id, stack, info):
			report(id, "unordered iteration: maps.%s", name)
		}
	}
}

// formatIndex returns the index of the format parameter of a printf-style function, one
// whose last two parameters are (format string, args ...any): fmt.Sprintf and the other
// fmt functions, World.Logf, Sim.Logf and Node.Logf. It returns -1 for any other function.
func formatIndex(fn *types.Func) int {
	sig := fn.Signature()
	ps := sig.Params()
	n := ps.Len()
	if !sig.Variadic() || n < 2 {
		return -1
	}
	if s, ok := ps.At(n - 1).Type().(*types.Slice); !ok || !isEmptyInterface(s.Elem()) {
		return -1
	}
	if b, ok := ps.At(n - 2).Type().Underlying().(*types.Basic); !ok || b.Kind() != types.String {
		return -1
	}
	return n - 2
}

func isEmptyInterface(t types.Type) bool {
	iface, ok := t.Underlying().(*types.Interface)
	return ok && iface.Empty()
}

// lintFormat reports proto messages that a call formats as text (ETC-005): an argument of a
// printf-style call whose verb is one of v s q x X (they call String), whatever the verb's
// flags, argument index or `*` width, an argument of a non-constant format (the verb is
// unknown), and an argument of the print functions of fmt that have no format. An argument
// counts when its type is a proto message or holds one (a slice of messages, a struct with a
// message field).
func lintFormat(info *types.Info, call *ast.CallExpr, report func(ast.Node, string, ...any)) {
	fn := calledFunc(info, call.Fun)
	if fn == nil || fn.Pkg() == nil {
		return
	}
	fi := formatIndex(fn)
	if fi < 0 {
		if fn.Pkg().Path() == "fmt" { // Sprint, Sprintln, Fprint, Fprintln, Append, Appendln
			for _, a := range call.Args {
				if containsProto(info.Types[a].Type) {
					report(a, "proto message passed to fmt.%s", fn.Name())
				}
			}
		}
		return
	}
	if fi >= len(call.Args) || call.Ellipsis.IsValid() {
		return
	}
	args := call.Args[fi+1:]
	tv := info.Types[call.Args[fi]]
	if tv.Value == nil {
		for _, a := range args { // non-constant format: the verbs are unknown
			if containsProto(info.Types[a].Type) {
				report(a, "proto message passed to %s with a format that is not constant", fn.Name())
			}
		}
		return
	}
	format, _ := strconv.Unquote(tv.Value.ExactString())
	for _, v := range verbArgs(format) {
		if v.arg < len(args) && strings.ContainsRune(formatVerbs, v.verb) && containsProto(info.Types[args[v.arg]].Type) {
			report(args[v.arg], "proto message formatted with %%%c", v.verb)
		}
	}
}

// verbArg is one verb of a format string and the index of the argument it formats.
type verbArg struct {
	verb rune
	arg  int
}

// verbArgs lists the verbs of format with their argument indexes, following fmt: flags, an
// optional `[n]` argument index before the width, the precision and the verb, and `*` for a
// width or precision taken from an argument. `%%` is not a verb.
func verbArgs(format string) []verbArg {
	var out []verbArg
	arg := 0
	// index parses `[n]` at format[i:]; it returns the position after it and the argument index.
	index := func(i, arg int) (int, int) {
		if i >= len(format) || format[i] != '[' {
			return i, arg
		}
		j := strings.IndexByte(format[i:], ']')
		if j < 0 {
			return i, arg
		}
		if n, err := strconv.Atoi(format[i+1 : i+j]); err == nil && n >= 1 {
			arg = n - 1
		}
		return i + j + 1, arg
	}
	// size parses a width or precision: digits, or `*` taking an argument.
	size := func(i, arg int) (int, int) {
		if i < len(format) && format[i] == '*' {
			return i + 1, arg + 1
		}
		for i < len(format) && format[i] >= '0' && format[i] <= '9' {
			i++
		}
		return i, arg
	}
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 {
			i++
		}
		i, arg = index(i, arg)
		i, arg = size(i, arg)
		if i < len(format) && format[i] == '.' {
			i, arg = index(i+1, arg)
			i, arg = size(i, arg)
		}
		i, arg = index(i, arg)
		if i >= len(format) {
			break
		}
		verb, w := utf8.DecodeRuneInString(format[i:])
		i += w - 1
		if verb == '%' {
			continue
		}
		out = append(out, verbArg{verb, arg})
		arg++
	}
	return out
}
