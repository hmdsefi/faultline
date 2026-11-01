// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/types"
)

// syncFuncs maps the sync methods whose method values are rewritten to sched's helpers.
var syncFuncs = map[string]string{
	"Mutex.Lock": "LockFunc", "Mutex.Unlock": "UnlockFunc",
	"RWMutex.RLock": "RLockFunc", "RWMutex.RUnlock": "RUnlockFunc", "RWMutex.Lock": "WLockFunc", "RWMutex.Unlock": "WUnlockFunc",
	"Once.Do": "OnceDoFunc", "WaitGroup.Wait": "WaitFunc", "WaitGroup.Go": "GoFunc",
	"Cond.Wait": "CondWaitFunc", "Cond.Signal": "CondSignalFunc", "Cond.Broadcast": "CondBroadcastFunc",
}

// pkgFuncs maps package-level functions to the sched functions that replace them, called or as
// values.
var pkgFuncs = map[string]string{
	"runtime.Gosched": "Point",
	"sync.OnceFunc":   "OnceFunc", "sync.OnceValue": "OnceValue", "sync.OnceValues": "OnceValues",
}

// importKeep is, per import path, the declaration that keeps an import used after the rewriter
// replaced its last use (%s is the import's local name).
var importKeep = map[string]string{
	"runtime": "= %s.Gosched", "sync": "%s.Mutex", "math/rand": "%s.Source", "math/rand/v2": "%s.Source",
	"reflect": "%s.Value", "maps": "= %s.Copy[map[int]int, map[int]int]",
}

// keepImport records that the file must keep the import id names in use.
func (r *rewriter) keepImport(path string, id *ast.Ident) {
	r.keeps[id.Name] = fmt.Sprintf(importKeep[path], id.Name)
}

// pkgFunc returns the import path and name of a package-level function sel names, and the
// package identifier, or "" when sel is not a qualified identifier.
func (r *rewriter) pkgFunc(sel *ast.SelectorExpr) (path, name string, id *ast.Ident) {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", "", nil
	}
	pn, ok := r.info.Uses[id].(*types.PkgName)
	if !ok {
		return "", "", nil
	}
	return pn.Imported().Path(), sel.Sel.Name, id
}

// selector rewrites the functions of pkgFuncs, the drawing functions of math/rand and
// math/rand/v2, math/rand/v2.N and reflect.Select, called or not, and, for a selector that is not
// called, the method values of syncFuncs; method expressions of the methods of syncFuncs and
// syncCalls, called or not, and method values of interface Lock and Unlock are gaps, and so is a
// method value of syncFuncs or of an interface Lock or Unlock whose receiver the file cannot name
// (XTE-041, XTE-042).
func (r *rewriter) selector(sel *ast.SelectorExpr, called bool) {
	if path, name, id := r.pkgFunc(sel); id != nil {
		if repl, ok := pkgFuncs[path+"."+name]; ok {
			r.sched = true
			r.keepImport(path, id)
			r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched." + repl })
			return
		}
		switch {
		case path == "math/rand" && randV1[name]:
			r.sched = true
			r.keepImport(path, id)
			r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched.RandV1()." + name })
		case path == "math/rand/v2" && randV2[name]:
			r.sched = true
			r.keepImport(path, id)
			r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched.Rand()." + name })
		case path == "math/rand/v2" && name == "N", path == "reflect" && name == "Select":
			repl := map[string]string{"N": "N", "Select": "ReflectSelect"}[name]
			r.sched = true
			r.keepImport(path, id)
			r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched." + repl })
		}
		return
	}
	s, ok := r.info.Selections[sel]
	if !ok || (called && s.Kind() != types.MethodExpr) {
		return
	}
	if name, ok := r.syncMethodOf(s); ok {
		helper := syncFuncs[name]
		switch {
		case s.Kind() == types.MethodExpr && (helper != "" || syncCalls[name] != ""):
			r.gap(sel, "method expression "+name)
		case helper == "", r.recvGap(sel, sel):
		default:
			r.sched = true
			r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched." + helper + "(" + r.recvAddr(sel) + ")" })
		}
		return
	}
	if s.Kind() == types.MethodVal && r.ifaceLock(s) && !r.recvGap(sel, sel) {
		r.gap(sel, "method value of an interface Lock or Unlock")
	}
}

// syncMethodOf names the method a selection selects ("Mutex.Lock"), as a method value or a
// method expression, when its receiver is a sync or sync/atomic type other than an interface.
func (r *rewriter) syncMethodOf(s *types.Selection) (string, bool) {
	if s.Kind() != types.MethodVal && s.Kind() != types.MethodExpr {
		return "", false
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok || fn.Pkg() == nil || (fn.Pkg().Path() != "sync" && fn.Pkg().Path() != "sync/atomic") {
		return "", false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "", false
	}
	recv := sig.Recv().Type()
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok || types.IsInterface(named) {
		return "", false
	}
	return named.Obj().Name() + "." + fn.Name(), true
}

// ifaceLock reports whether s selects a method Lock or Unlock, without parameters and results,
// of an interface.
func (r *rewriter) ifaceLock(s *types.Selection) bool {
	fn, ok := s.Obj().(*types.Func)
	if !ok || (fn.Name() != "Lock" && fn.Name() != "Unlock") {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || sig.Params().Len() != 0 || sig.Results().Len() != 0 {
		return false
	}
	return types.IsInterface(sig.Recv().Type())
}

// ifaceRecv returns the interface value a Lock or Unlock call selects, following embedded
// struct fields.
func (r *rewriter) ifaceRecv(sel *ast.SelectorExpr) string {
	path, _, _ := r.fieldPath(sel)
	return "(" + r.text(sel.X) + ")" + path
}

// fieldPath returns the embedded fields a method selection is promoted through (".T.Locker")
// and the type of the last one, or of sel.X when there is none, and true; or false when the file
// cannot name one of them: an unexported field of another package.
func (r *rewriter) fieldPath(sel *ast.SelectorExpr) (string, types.Type, bool) {
	path, t := "", r.info.TypeOf(sel.X)
	idx := r.info.Selections[sel].Index()
	for _, i := range idx[:len(idx)-1] {
		if p, ok := t.Underlying().(*types.Pointer); ok {
			t = p.Elem()
		}
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			return "", nil, false
		}
		f := st.Field(i)
		if !f.Exported() && f.Pkg() != r.pkg {
			return "", nil, false
		}
		path, t = path+"."+f.Name(), f.Type()
	}
	return path, t, true
}

// recvGap records a gap at n and reports true when the file cannot name the receiver of the
// method selection sel, so that its sync row leaves it as written (XTE-042).
func (r *rewriter) recvGap(n ast.Node, sel *ast.SelectorExpr) bool {
	if _, _, ok := r.fieldPath(sel); ok {
		return false
	}
	r.gap(n, "method receiver not nameable in this file")
	return true
}
