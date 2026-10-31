// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"
)

// schedPath is the import path of prototype B's runtime.
const schedPath = "github.com/hmdsefi/faultline/runner/toolexec/sched"

// edit replaces src[pos:end] (an insertion when pos == end) with text().
type edit struct {
	pos, end int
	text     func() string
}

// rewriter applies EXB-034 to one file as text edits, so that lines stay where they were.
type rewriter struct {
	fset    *token.FileSet
	tf      *token.File
	file    *ast.File
	src     []byte
	info    *types.Info
	pkg     *types.Package
	path    string // original absolute file name
	edits   []edit
	gaps    []string
	n       int                   // temporary-name counter
	sched   bool                  // the file now uses sched
	reflect bool                  // the file now uses reflect
	keeps   map[string]string     // local name -> declaration that keeps a replaced import used
	points  map[int]bool          // offsets where a point was inserted
	gotos   map[types.Object]bool // labels a goto statement names
}

func (r *rewriter) off(p token.Pos) int { return r.tf.Offset(p) }

func (r *rewriter) text(n ast.Node) string { return r.render(r.off(n.Pos()), r.off(n.End())) }

func (r *rewriter) tmp(prefix string) string {
	r.n++
	return "__fl_" + prefix + strconv.Itoa(r.n)
}

func (r *rewriter) gap(n ast.Node, what string) {
	p := r.fset.Position(n.Pos())
	r.gaps = append(r.gaps, fmt.Sprintf("%s:%d: %s", r.path, p.Line, what))
}

func (r *rewriter) add(pos, end int, text func() string) {
	r.edits = append(r.edits, edit{pos, end, text})
}

// insert adds s at the position at. A second point before the same statement is dropped, so
// each statement gets one (XTE-041).
func (r *rewriter) insert(at token.Pos, s string) {
	r.sched = true
	if s == point {
		if r.points[r.off(at)] {
			return
		}
		r.points[r.off(at)] = true
	}
	r.add(r.off(at), r.off(at), func() string { return s })
}

// point is inserted before a statement; bodyPoint at the start of a loop body.
const (
	point     = "__fl_sched.Point(); "
	bodyPoint = "__fl_sched.Point();"
)

// render returns src[lo:hi] with the edits inside it applied. Edits nested in another edit are
// applied by that edit's text function (through render on sub-ranges). An insertion at either end
// of a sub-range belongs to the enclosing text, which applies it around the edit.
func (r *rewriter) render(lo, hi int) string {
	var b strings.Builder
	cur := lo
	for _, e := range r.edits {
		if e.pos < cur || e.end > hi || (e.pos == e.end && (e.pos == lo || e.pos >= hi)) {
			continue
		}
		b.Write(r.src[cur:e.pos])
		text := e.text()
		b.WriteString(text)
		if strings.Count(text, "\n") != bytes.Count(r.src[e.pos:e.end], []byte("\n")) { // XTE-044
			p := r.tf.PositionFor(r.tf.Pos(e.end), false)
			fmt.Fprintf(&b, "/*line :%d:%d*/", p.Line, p.Column)
		}
		cur = max(cur, e.end)
	}
	b.Write(r.src[cur:hi])
	return b.String()
}

// output returns the rewritten file with a //line directive naming the original file and, when the
// file has gaps, the declaration that registers them with sched (XTE-050).
func (r *rewriter) output() string {
	slices.SortStableFunc(r.edits, func(a, b edit) int {
		if a.pos != b.pos {
			return a.pos - b.pos
		}
		az, bz := a.pos == a.end, b.pos == b.end
		switch {
		case az && !bz:
			return -1
		case bz && !az:
			return 1
		}
		return b.end - a.end
	})
	body := r.render(0, len(r.src))
	gaps := r.sortedGaps()
	if len(gaps) > 0 {
		r.sched = true
	}
	var imports []string
	if r.sched {
		imports = append(imports, `import __fl_sched "`+schedPath+`"`)
	}
	if r.reflect {
		imports = append(imports, `import __fl_reflect "reflect"`)
	}
	var tail string
	names := make([]string, 0, len(r.keeps))
	for n := range r.keeps {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		tail += "\nvar _ " + r.keeps[n] + "\n"
	}
	if len(gaps) > 0 {
		tail += "\nvar _ = __fl_sched.Gaps(" + strconv.Quote(strings.Join(gaps, "\n")) + ")\n"
	}
	// Insert the imports right after the package clause, on its line.
	pc := r.off(r.file.Name.End())
	body = body[:pc] + "; " + strings.Join(imports, "; ") + body[pc:]
	return "//line " + r.path + ":1\n" + body + tail
}

// sortedGaps returns the file's gaps sorted by line, then text, without duplicates (XTE-045).
func (r *rewriter) sortedGaps() []string {
	line := func(g string) int {
		n, _, _ := strings.Cut(strings.TrimPrefix(g, r.path+":"), ":")
		v, _ := strconv.Atoi(n)
		return v
	}
	out := slices.Clone(r.gaps)
	slices.SortFunc(out, func(a, b string) int {
		if c := cmp.Compare(line(a), line(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return slices.Compact(out)
}

// rewrite walks the file once and records the edits of EXB-034.
func (r *rewriter) rewrite() {
	r.keeps, r.points, r.gotos = map[string]string{}, map[int]bool{}, map[types.Object]bool{}
	ast.Inspect(r.file, func(n ast.Node) bool {
		if b, ok := n.(*ast.BranchStmt); ok && b.Tok == token.GOTO && b.Label != nil {
			if obj := r.info.Uses[b.Label]; obj != nil {
				r.gotos[obj] = true
			}
		}
		return true
	})
	calls := map[*ast.SelectorExpr]bool{}
	ast.Inspect(r.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BlockStmt:
			r.stmts(n.List)
		case *ast.CaseClause:
			r.stmts(n.Body)
		case *ast.CommClause:
			r.stmts(n.Body)
		case *ast.IfStmt:
			if e, ok := n.Else.(*ast.IfStmt); ok && (r.hasOp(e.Init) || r.hasOp(e.Cond)) {
				r.gap(e, "channel operation in else-if header")
			}
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				calls[sel] = true
				r.call(n, sel)
			}
		case *ast.SelectorExpr:
			r.selector(n, calls[n])
		}
		return true
	})
	r.gotoPoints()
}

// gotoPoints puts a scheduling point before every goto whose label names a statement that got
// a point before it, because the jump skips that point (XTE-041).
func (r *rewriter) gotoPoints() {
	pointed := map[types.Object]bool{}
	ast.Inspect(r.file, func(n ast.Node) bool {
		l, ok := n.(*ast.LabeledStmt)
		if !ok || !r.points[r.off(l.Pos())] {
			return true
		}
		for s := ast.Stmt(l); ; {
			ls, ok := s.(*ast.LabeledStmt)
			if !ok {
				break
			}
			if obj := r.info.Defs[ls.Label]; obj != nil {
				pointed[obj] = true
			}
			s = ls.Stmt
		}
		return true
	})
	ast.Inspect(r.file, func(n ast.Node) bool {
		if b, ok := n.(*ast.BranchStmt); ok && b.Tok == token.GOTO && b.Label != nil && pointed[r.info.Uses[b.Label]] {
			r.insert(b.Pos(), point)
		}
		return true
	})
}

// stmts handles the statements of one list: scheduling points, go, select and range.
func (r *rewriter) stmts(list []ast.Stmt) {
	for _, s := range list {
		start := s.Pos()
		inner := s
		var labels []*ast.LabeledStmt
		for {
			l, ok := inner.(*ast.LabeledStmt)
			if !ok {
				break
			}
			labels = append(labels, l)
			inner = l.Stmt
		}
		switch st := inner.(type) {
		case *ast.GoStmt:
			if r.hasOp(st.Call) {
				r.insert(start, point)
			}
			r.goStmt(st)
		case *ast.SelectStmt:
			r.selectStmt(start, st, labels)
		case *ast.RangeStmt:
			r.rangeStmt(start, st)
		case *ast.ForStmt:
			if r.hasOp(st.Init) || r.hasOp(st.Cond) || r.hasOp(st.Post) {
				r.insert(start, point)
				r.insert(st.Body.Lbrace+1, " "+bodyPoint)
			}
		case *ast.IfStmt:
			if r.hasOp(st.Init) || r.hasOp(st.Cond) {
				r.insert(start, point)
			}
		case *ast.SwitchStmt:
			if r.hasOp(st.Init) || r.hasOp(st.Tag) || r.caseOps(st.Body) {
				r.insert(start, point)
			}
		case *ast.TypeSwitchStmt:
			if r.hasOp(st.Init) || r.hasOp(st.Assign) {
				r.insert(start, point)
			}
		case *ast.DeferStmt:
			r.deferStmt(start, st)
		case *ast.BlockStmt, *ast.EmptyStmt, *ast.CaseClause, *ast.CommClause:
			// clause containers of switch and select bodies; their bodies are lists of their own
		default:
			if r.hasOp(inner) {
				r.insert(start, point)
			}
		}
	}
}

// caseOps reports whether a case expression of a switch body holds a channel operation.
func (r *rewriter) caseOps(body *ast.BlockStmt) bool {
	for _, c := range body.List {
		if cc, ok := c.(*ast.CaseClause); ok {
			for _, e := range cc.List {
				if r.hasOp(e) {
					return true
				}
			}
		}
	}
	return false
}

// deferStmt gives a deferred close or WaitGroup.Wait a scheduling point when it runs, and a
// channel operation in the deferred call's function value or arguments, which the defer
// evaluates, a point before the defer (XTE-041).
func (r *rewriter) deferStmt(start token.Pos, st *ast.DeferStmt) {
	call := st.Call
	ops := r.hasOp(call.Fun)
	for _, a := range call.Args {
		ops = ops || r.hasOp(a)
	}
	if ops {
		r.insert(start, point)
	}
	d := ""
	if r.builtin(call.Fun) && ast.Unparen(call.Fun).(*ast.Ident).Name == "close" && len(call.Args) == 1 {
		d = r.tmp("d")
		r.add(r.off(st.Pos()), r.off(st.End()), func() string {
			return "{ " + d + " := " + r.text(call.Args[0]) + "; defer func() { __fl_sched.Point(); close(" + d + ") }() }"
		})
	} else if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if name, ok := r.syncMethod(sel); ok && name == "WaitGroup.Wait" && !r.recvGap(st, sel) {
			d = r.tmp("d")
			r.add(r.off(st.Pos()), r.off(st.End()), func() string {
				return "{ " + d + " := " + r.recvAddr(sel) + "; defer func() { __fl_sched.Point(); " + d + ".Wait() }() }"
			})
		}
	}
	if d != "" {
		r.sched = true
	}
}

// hasOp reports whether n contains, outside function literals and nested blocks, a channel
// send, receive or close, or a sync.WaitGroup.Wait call.
func (r *rewriter) hasOp(n ast.Node) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if found {
			return false
		}
		switch m := m.(type) {
		case *ast.FuncLit, *ast.BlockStmt:
			return false
		case *ast.SendStmt:
			found = true
		case *ast.UnaryExpr:
			found = m.Op == token.ARROW
		case *ast.CallExpr:
			if id, ok := ast.Unparen(m.Fun).(*ast.Ident); ok {
				if b, ok := r.info.Uses[id].(*types.Builtin); ok && b.Name() == "close" {
					found = true
				}
			}
			if sel, ok := m.Fun.(*ast.SelectorExpr); ok {
				if name, ok := r.syncMethod(sel); ok && name == "WaitGroup.Wait" {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// goStmt rewrites go f(a, b) to { t0, t1, t2 := f, a, b; sched.Go(func() { t0(t1, t2) }) }.
func (r *rewriter) goStmt(st *ast.GoStmt) {
	call := st.Call
	// An operand a short variable declaration cannot hold with its type (a generic function
	// without instantiation, nil, a constant) is passed as written: none of them has side
	// effects, so evaluating it in the new goroutine changes nothing. An untyped operand that
	// is not a constant (a == b, 1 << n) is bound converted to the type it has in the call, so
	// that it is still evaluated here. written marks the first kind, conv holds the type.
	ops := append([]ast.Expr{call.Fun}, call.Args...)
	written, conv := make([]bool, len(ops)), make([]string, len(ops))
	for i, e := range ops {
		switch {
		case i == 0:
			written[i] = r.genericFunc(e) || r.builtin(e)
		case r.asWritten(e):
			written[i] = true
		case r.untyped(e):
			if conv[i] = r.typeText(r.info.TypeOf(e), st.Pos()); conv[i] == "" {
				r.gap(st, "go statement argument type not nameable in this file")
				return
			}
		}
	}
	// A sync method is bound as its rewritten method value (XTE-041); an interface Lock or Unlock,
	// or a sync method whose receiver the file cannot name, is bound as a method value, which runs
	// outside sched (XTE-042).
	sel, _ := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	name, _ := r.syncMethod(sel)
	helper := syncFuncs[name]
	s := r.info.Selections[sel]
	iface := s != nil && s.Kind() == types.MethodVal && r.ifaceLock(s)
	if (helper != "" || iface) && r.recvGap(st, sel) {
		helper = ""
	} else if iface {
		r.gap(st, "method value of an interface Lock or Unlock")
	}
	r.sched = true
	names := make([]string, len(ops))
	for i := range ops {
		if !written[i] {
			names[i] = r.tmp("g")
		}
	}
	r.add(r.off(st.Pos()), r.off(st.End()), func() string {
		var bound, vals, use []string
		for i, e := range ops {
			if names[i] == "" {
				use = append(use, r.text(e))
				continue
			}
			val := r.text(e)
			if i == 0 && helper != "" {
				val = "__fl_sched." + helper + "(" + r.recvAddr(sel) + ")"
			}
			if i == 2 && sel != nil { // AfterFunc binds its function wrapped, as in a call (XTE-041)
				if p, n, _ := r.pkgFunc(sel); (p == "time" || p == "context") && n == "AfterFunc" && len(call.Args) == 2 {
					val = "__fl_sched.Func(" + val + ")"
				}
			}
			if conv[i] != "" {
				val = conv[i] + "(" + val + ")"
			}
			bound, vals, use = append(bound, names[i]), append(vals, val), append(use, names[i])
		}
		args := strings.Join(use[1:], ", ")
		if call.Ellipsis.IsValid() {
			args += "..."
		}
		decl := ""
		if len(bound) > 0 {
			decl = strings.Join(bound, ", ") + " := " + strings.Join(vals, ", ") + "; "
		}
		return "{ " + decl + "__fl_sched.Go(func() { " + use[0] + "(" + args + ") }) }"
	})
}

// genericFunc reports whether e names a generic function without instantiating it.
func (r *rewriter) genericFunc(e ast.Expr) bool {
	var id *ast.Ident
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		id = e
	case *ast.SelectorExpr:
		id = e.Sel
	default:
		return false
	}
	fn, ok := r.info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	return ok && sig.TypeParams().Len() > 0
}

// builtin reports whether e names a builtin function.
func (r *rewriter) builtin(e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	if !ok {
		return false
	}
	_, b := r.info.Uses[id].(*types.Builtin)
	return b
}

// asWritten reports whether a go statement's argument e is nil or a constant. A short
// variable declaration would give a constant its default type, not the parameter's.
func (r *rewriter) asWritten(e ast.Expr) bool {
	tv := r.info.Types[e]
	if tv.IsNil() || tv.Value != nil {
		return true
	}
	id, ok := ast.Unparen(e).(*ast.Ident)
	return ok && r.info.Uses[id] == types.Universe.Lookup("nil")
}

// untyped reports whether e has an untyped type of its own, which its use then replaces: a
// literal, an untyped constant, a comparison, a shift of an untyped operand, and the operators
// and the builtins real, imag, complex, min and max applied to untyped operands.
func (r *rewriter) untyped(e ast.Expr) bool {
	untypedConst := func(id *ast.Ident) bool {
		c, ok := r.info.Uses[id].(*types.Const)
		if !ok {
			return false
		}
		b, ok := c.Type().(*types.Basic)
		return ok && b.Info()&types.IsUntyped != 0
	}
	switch e := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return untypedConst(e)
	case *ast.SelectorExpr:
		return untypedConst(e.Sel)
	case *ast.BinaryExpr:
		switch e.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return true
		case token.SHL, token.SHR:
			return r.untyped(e.X)
		}
		return r.untyped(e.X) && r.untyped(e.Y)
	case *ast.UnaryExpr:
		return e.Op != token.ARROW && e.Op != token.AND && r.untyped(e.X)
	case *ast.CallExpr:
		id, ok := ast.Unparen(e.Fun).(*ast.Ident)
		if !ok {
			return false
		}
		b, ok := r.info.Uses[id].(*types.Builtin)
		if !ok || !slices.Contains([]string{"real", "imag", "complex", "min", "max"}, b.Name()) {
			return false
		}
		for _, a := range e.Args {
			if !r.untyped(a) {
				return false
			}
		}
		return len(e.Args) > 0
	}
	return false
}

// typeText prints t for use at pos in this file, or "" if t cannot be named there: a package
// it names is not imported in this file, a name it uses is shadowed at pos, or it uses a type,
// struct field or method that another package does not export. Such a text would fail to
// compile, or compile and name a different type.
func (r *rewriter) typeText(t types.Type, pos token.Pos) string {
	scope := r.pkg.Scope().Innermost(pos)
	if scope == nil {
		return ""
	}
	lookup := func(name string) types.Object {
		_, obj := scope.LookupParent(name, pos)
		return obj
	}
	ok := true
	bare := map[*types.Package]bool{} // dot imports: their names are printed unqualified
	q := func(p *types.Package) string {
		if p == r.pkg {
			return ""
		}
		for _, imp := range r.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path != p.Path() {
				continue
			}
			name := p.Name()
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name == "." {
				bare[p] = true
				return ""
			}
			if pn, isPkg := lookup(name).(*types.PkgName); isPkg && pn.Imported() == p {
				return name
			}
		}
		ok = false
		return p.Name()
	}
	s := types.TypeString(t, q)
	if !ok || !r.nameable(t, lookup, bare) {
		return ""
	}
	return s
}

// nameable reports whether every name in t's text (see typeText) denotes t's own part: lookup
// resolves an unqualified name at the place of use, and bare holds the dot-imported packages.
func (r *rewriter) nameable(t types.Type, lookup func(string) types.Object, bare map[*types.Package]bool) bool {
	mine := func(p *types.Package) bool { return p == nil || p == r.pkg }
	typeName := func(o *types.TypeName) bool {
		if !mine(o.Pkg()) && !o.Exported() {
			return false
		}
		return (!mine(o.Pkg()) && !bare[o.Pkg()]) || lookup(o.Name()) == o
	}
	all := func(l *types.TypeList) bool {
		for i := range l.Len() {
			if !r.nameable(l.At(i), lookup, bare) {
				return false
			}
		}
		return true
	}
	vars := func(tu *types.Tuple) bool {
		for i := range tu.Len() {
			if !r.nameable(tu.At(i).Type(), lookup, bare) {
				return false
			}
		}
		return true
	}
	switch t := t.(type) {
	case *types.Basic:
		if t.Kind() == types.UnsafePointer {
			return true // printed as unsafe.Pointer, checked by the qualifier
		}
		tn, ok := lookup(t.Name()).(*types.TypeName)
		return ok && types.Identical(tn.Type(), t)
	case *types.Named:
		return typeName(t.Obj()) && all(t.TypeArgs())
	case *types.Alias:
		return typeName(t.Obj()) && all(t.TypeArgs())
	case *types.TypeParam:
		return lookup(t.Obj().Name()) == t.Obj()
	case *types.Pointer:
		return r.nameable(t.Elem(), lookup, bare)
	case *types.Slice:
		return r.nameable(t.Elem(), lookup, bare)
	case *types.Array:
		return r.nameable(t.Elem(), lookup, bare)
	case *types.Chan:
		return r.nameable(t.Elem(), lookup, bare)
	case *types.Map:
		return r.nameable(t.Key(), lookup, bare) && r.nameable(t.Elem(), lookup, bare)
	case *types.Signature:
		return vars(t.Params()) && vars(t.Results())
	case *types.Struct:
		for i := range t.NumFields() {
			f := t.Field(i)
			if (!mine(f.Pkg()) && !f.Exported()) || !r.nameable(f.Type(), lookup, bare) {
				return false
			}
		}
		return true
	case *types.Interface:
		for i := range t.NumExplicitMethods() {
			m := t.ExplicitMethod(i)
			if (!mine(m.Pkg()) && !m.Exported()) || !r.nameable(m.Type(), lookup, bare) {
				return false
			}
		}
		for i := range t.NumEmbeddeds() {
			if !r.nameable(t.EmbeddedType(i), lookup, bare) {
				return false
			}
		}
		return true
	case *types.Union:
		for i := range t.Len() {
			if !r.nameable(t.Term(i).Type(), lookup, bare) {
				return false
			}
		}
		return true
	}
	return false
}

// selectStmt applies the select rewrites of EXB-034.
// The select's labels move to the switch, so that break statements naming them stay valid.
func (r *rewriter) selectStmt(start token.Pos, st *ast.SelectStmt, labels []*ast.LabeledStmt) {
	var clauses []*ast.CommClause // a select body holds only these
	for _, c := range st.Body.List {
		if cc, ok := c.(*ast.CommClause); ok {
			clauses = append(clauses, cc)
		}
	}
	comm, hasDefault := 0, false
	for _, cc := range clauses {
		if cc.Comm == nil {
			hasDefault = true
		} else {
			comm++
		}
	}
	if comm < 2 {
		r.insert(start, point)
		return
	}
	// The labels move into the rewritten form, where a goto from inside a case body would run
	// the case again without a new receive, and a goto from outside would jump into a block.
	for _, l := range labels {
		if obj := r.info.Defs[l.Label]; obj != nil && r.gotos[obj] {
			r.insert(start, point)
			r.gap(st, "select named by a goto")
			return
		}
	}
	type rcase struct {
		clause *ast.CommClause
		dir    string // "recv" or "send"
		ch     ast.Expr
		val    ast.Expr // send value
		lhs    []ast.Expr
		define bool
		elem   string
		iface  bool // the element type is an interface (or a type parameter)
	}
	var cases []rcase
	for _, cc := range clauses {
		if cc.Comm == nil {
			continue
		}
		rc := rcase{clause: cc}
		var recv ast.Expr
		switch s := cc.Comm.(type) {
		case *ast.SendStmt:
			rc.dir, rc.ch, rc.val = "send", s.Chan, s.Value
		case *ast.ExprStmt:
			recv = s.X
		case *ast.AssignStmt:
			recv, rc.lhs, rc.define = s.Rhs[0], s.Lhs, s.Tok == token.DEFINE
		}
		if recv != nil {
			u, ok := ast.Unparen(recv).(*ast.UnaryExpr)
			if !ok || u.Op != token.ARROW {
				r.insert(start, point)
				r.gap(st, "select case with an unexpected receive form")
				return
			}
			rc.dir, rc.ch = "recv", u.X
		}
		ct, ok := r.info.TypeOf(rc.ch).Underlying().(*types.Chan)
		if !ok {
			r.insert(start, point)
			r.gap(st, "select on a channel of unresolved type")
			return
		}
		if rc.elem = r.typeText(ct.Elem(), st.Pos()); rc.elem == "" {
			r.insert(start, point)
			r.gap(st, "select element type not nameable in this file")
			return
		}
		rc.iface = types.IsInterface(ct.Elem())
		cases = append(cases, rc)
	}
	r.sched, r.reflect = true, true
	c, v, ok := r.tmp("c"), r.tmp("v"), r.tmp("ok")
	var label string
	for _, l := range labels {
		label += l.Label.Name + ": "
		r.add(r.off(l.Pos()), r.off(l.Colon)+1, func() string { return "" })
	}
	// Header: select { → Point; { c, v, ok := sched.Select(...); switch c {
	r.add(r.off(st.Pos()), r.off(st.Body.Lbrace)+1, func() string {
		var list []string
		for _, rc := range cases {
			if rc.dir == "send" {
				list = append(list, "{Dir: __fl_reflect.SelectSend, Chan: __fl_reflect.ValueOf("+r.text(rc.ch)+
					"), Send: __fl_reflect.ValueOf(&[]"+rc.elem+"{"+r.text(rc.val)+"}[0]).Elem()}")
			} else {
				list = append(list, "{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf("+r.text(rc.ch)+")}")
			}
		}
		return point + "{ " + c + ", " + v + ", " + ok + " := __fl_sched.Select([]__fl_reflect.SelectCase{" +
			strings.Join(list, ", ") + "}, " + strconv.FormatBool(hasDefault) + "); _, _ = " + v + ", " + ok +
			"; " + label + "switch " + c + " {"
	})
	i := 0
	for _, cc := range clauses {
		if cc.Comm == nil {
			r.add(r.off(cc.Case), r.off(cc.Colon)+1, func() string { return "default:" })
			continue
		}
		rc, idx := cases[i], i
		i++
		r.add(r.off(cc.Case), r.off(cc.Colon)+1, func() string {
			h := "case " + strconv.Itoa(idx) + ":"
			if rc.dir == "recv" && len(rc.lhs) > 0 {
				op := " = "
				if rc.define {
					op = " := "
				}
				// A plain assertion panics if the element type's text names another type. A
				// nil interface value fails any assertion, so an interface keeps comma-ok.
				if !isBlank(rc.lhs[0]) {
					blank := ""
					if rc.iface {
						blank = ", _"
					}
					h += " " + r.text(rc.lhs[0]) + blank + op + v + ".Interface().(" + rc.elem + ");"
				}
				if len(rc.lhs) == 2 && !isBlank(rc.lhs[1]) {
					h += " " + r.text(rc.lhs[1]) + op + ok + ";"
				}
			}
			return h
		})
	}
	// Without a default the original select blocks until a case runs, so it may be the
	// terminating statement of a function; a panicking default keeps the switch terminating.
	r.add(r.off(st.Body.Rbrace), r.off(st.Body.Rbrace)+1, func() string {
		if hasDefault {
			return "}}"
		}
		return `default: panic("faultline-toolexec: sched.Select returned no case") }}`
	})
}

// rangeStmt handles range over channels (scheduling points) and over maps (sorted keys).
func (r *rewriter) rangeStmt(start token.Pos, st *ast.RangeStmt) {
	if r.hasOp(st.X) {
		r.insert(start, point)
	}
	switch t := r.info.TypeOf(st.X).Underlying().(type) {
	case *types.Interface:
		// Only a type parameter's underlying type is an interface a range accepts.
		tp, ok := types.Unalias(r.info.TypeOf(st.X)).(*types.TypeParam)
		switch {
		case ok && allChanTerms(tp):
			r.insert(start, point)
			r.insert(st.Body.Lbrace+1, " "+bodyPoint)
		case ok && chanOrMapTerm(tp):
			r.gap(st, "range over a channel or map of type-parameter type")
		}
	case *types.Chan:
		r.insert(start, point)
		r.insert(st.Body.Lbrace+1, " "+bodyPoint)
	case *types.Map:
		b, ok := t.Key().Underlying().(*types.Basic)
		if !ok || b.Info()&types.IsOrdered == 0 {
			r.gap(st, "range over a map with non-ordered keys")
			return
		}
		if !sideEffectFree(st.X) {
			r.gap(st, "range over a map expression with side effects")
			return
		}
		r.sched = true
		blank := func(e ast.Expr) bool {
			id, ok := e.(*ast.Ident)
			return e == nil || ok && id.Name == "_"
		}
		k, okName := r.tmp("k"), r.tmp("ok")
		r.add(r.off(st.For), r.off(st.Body.Lbrace)+1, func() string {
			m := r.text(st.X)
			head := "for _, " + k + " := range __fl_sched.Keys(" + m + ") {"
			if _, ok := st.X.(*ast.StarExpr); ok {
				m = "(" + m + ")" // *p[k] would index p
			}
			switch {
			case st.Tok == token.DEFINE && !blank(st.Value):
				head += " " + r.text(st.Value) + ", " + okName + " := " + m + "[" + k + "]; if !" + okName + " { continue };"
			default:
				head += " if _, " + okName + " := " + m + "[" + k + "]; !" + okName + " { continue };"
				if st.Tok == token.ASSIGN && !blank(st.Value) {
					head += " " + r.text(st.Value) + " = " + m + "[" + k + "];"
				}
			}
			if !blank(st.Key) {
				if st.Tok == token.DEFINE {
					head += " " + r.text(st.Key) + " := " + k + ";"
				} else {
					head += " " + r.text(st.Key) + " = " + k + ";"
				}
			}
			return head
		})
	}
}

// chanOrMapTerm reports whether tp's constraint lists a channel or map type.
func chanOrMapTerm(tp *types.TypeParam) bool {
	iface, ok := tp.Constraint().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	for i := range iface.NumEmbeddeds() {
		terms := []types.Type{iface.EmbeddedType(i)}
		if u, ok := terms[0].(*types.Union); ok {
			terms = terms[:0]
			for j := range u.Len() {
				terms = append(terms, u.Term(j).Type())
			}
		}
		for _, t := range terms {
			switch t.Underlying().(type) {
			case *types.Chan, *types.Map:
				return true
			}
		}
	}
	return false
}

// allChanTerms reports whether every term of tp's constraint is a channel type.
func allChanTerms(tp *types.TypeParam) bool {
	iface, ok := tp.Constraint().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	n := 0
	for i := range iface.NumEmbeddeds() {
		terms := []types.Type{iface.EmbeddedType(i)}
		if u, ok := terms[0].(*types.Union); ok {
			terms = terms[:0]
			for j := range u.Len() {
				terms = append(terms, u.Term(j).Type())
			}
		}
		for _, t := range terms {
			if _, ok := t.Underlying().(*types.Chan); !ok {
				return false
			}
			n++
		}
	}
	return n > 0
}

// isBlank reports whether e is the blank identifier.
func isBlank(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "_"
}

func sideEffectFree(e ast.Expr) bool {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return sideEffectFree(e.X)
	case *ast.StarExpr:
		return sideEffectFree(e.X)
	}
	return false
}

// syncMethod names the sync method sel selects ("Mutex.Lock"), if any.
func (r *rewriter) syncMethod(sel *ast.SelectorExpr) (string, bool) {
	s, ok := r.info.Selections[sel]
	if !ok || s.Kind() != types.MethodVal {
		return "", false
	}
	return r.syncMethodOf(s)
}

// recvAddr returns the address expression of the sync value a method selection is called on,
// for a selection whose receiver the file can name (recvGap).
func (r *rewriter) recvAddr(sel *ast.SelectorExpr) string {
	path, t, _ := r.fieldPath(sel)
	expr := "(" + r.text(sel.X) + ")" + path
	if _, ok := t.Underlying().(*types.Pointer); ok {
		return expr
	}
	return "&" + expr
}

var syncCalls = map[string]string{
	"Mutex.Lock": "Lock", "Mutex.Unlock": "Unlock",
	"RWMutex.Lock": "WLock", "RWMutex.Unlock": "WUnlock", "RWMutex.RLock": "RLock", "RWMutex.RUnlock": "RUnlock",
	"Once.Do": "OnceDo", "RWMutex.RLocker": "RLocker", "WaitGroup.Go": "WaitGroupGo",
	"Cond.Wait": "CondWait", "Cond.Signal": "CondSignal", "Cond.Broadcast": "CondBroadcast",
}

var randV1 = map[string]bool{"ExpFloat64": true, "Float32": true, "Float64": true, "Int": true, "Int31": true,
	"Int31n": true, "Int63": true, "Int63n": true, "Intn": true, "NormFloat64": true, "Perm": true, "Read": true,
	"Shuffle": true, "Uint32": true, "Uint64": true}

var randV2 = map[string]bool{"ExpFloat64": true, "Float32": true, "Float64": true, "Int": true, "Int32": true,
	"Int32N": true, "Int64": true, "Int64N": true, "IntN": true, "NormFloat64": true, "Perm": true, "Shuffle": true,
	"Uint": true, "Uint32": true, "Uint32N": true, "Uint64": true, "Uint64N": true, "UintN": true}

// call rewrites sync method calls, Lock and Unlock calls through interfaces, math/rand
// package-level calls and the function arguments of AfterFunc; a sync method or interface lock
// call whose receiver the file cannot name is a gap.
func (r *rewriter) call(call *ast.CallExpr, sel *ast.SelectorExpr) {
	if s := r.info.Selections[sel]; s != nil && s.Kind() == types.MethodVal && r.ifaceLock(s) {
		if r.recvGap(call, sel) {
			return
		}
		fn := map[string]string{"Lock": "ILock", "Unlock": "IUnlock"}[sel.Sel.Name]
		r.sched = true
		r.add(r.off(call.Pos()), r.off(call.End()), func() string { return "__fl_sched." + fn + "(" + r.ifaceRecv(sel) + ")" })
		return
	}
	if name, ok := r.syncMethod(sel); ok {
		fn, ok := syncCalls[name]
		if !ok || r.recvGap(call, sel) {
			return
		}
		r.sched = true
		r.add(r.off(call.Pos()), r.off(call.End()), func() string {
			args := r.recvAddr(sel)
			for _, a := range call.Args {
				args += ", " + r.text(a)
			}
			return "__fl_sched." + fn + "(" + args + ")"
		})
		return
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	pn, ok := r.info.Uses[id].(*types.PkgName)
	if !ok {
		return
	}
	switch path := pn.Imported().Path(); {
	case path == "math/rand" && randV1[sel.Sel.Name]:
		r.sched = true
		r.keepImport(path, id)
		r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched.RandV1()." + sel.Sel.Name })
	case path == "math/rand/v2" && randV2[sel.Sel.Name]:
		r.sched = true
		r.keepImport(path, id)
		r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched.Rand()." + sel.Sel.Name })
	case path == "math/rand/v2" && sel.Sel.Name == "N":
		r.gap(call, "math/rand/v2.N")
	case (path == "time" || path == "context") && sel.Sel.Name == "AfterFunc" && len(call.Args) == 2:
		arg := call.Args[1]
		r.sched = true
		r.add(r.off(arg.Pos()), r.off(arg.Pos()), func() string { return "__fl_sched.Func(" })
		r.add(r.off(arg.End()), r.off(arg.End()), func() string { return ")" })
	case path == "reflect" && sel.Sel.Name == "Select":
		r.gap(call, "reflect.Select")
	}
}
