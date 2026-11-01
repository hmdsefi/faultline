// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"go/ast"
	"go/types"
)

// keyOrder classifies a map key type (XTE-041, XTE-042): "Keys" for integer and string types,
// "KeysCmp" for booleans and for structs and arrays whose fields are orderable, "float" when a
// floating-point type is part of the key, and "" for keys without an order.
func keyOrder(t types.Type) string {
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch info := u.Info(); {
		case info&(types.IsInteger|types.IsString) != 0:
			return "Keys"
		case info&types.IsBoolean != 0:
			return "KeysCmp"
		case info&types.IsFloat != 0:
			return "float"
		}
	case *types.Struct:
		order := "KeysCmp"
		for i := range u.NumFields() {
			switch keyOrder(u.Field(i).Type()) {
			case "":
				return ""
			case "float":
				order = "float"
			}
		}
		return order
	case *types.Array:
		if o := keyOrder(u.Elem()); o == "" || o == "float" {
			return o
		}
		return "KeysCmp"
	}
	return ""
}

// indexedSelector returns the selector of an instantiated function, f[T] or f[T, U], or nil.
func indexedSelector(e ast.Expr) *ast.SelectorExpr {
	switch x := ast.Unparen(e).(type) {
	case *ast.IndexExpr:
		sel, _ := x.X.(*ast.SelectorExpr)
		return sel
	case *ast.IndexListExpr:
		sel, _ := x.X.(*ast.SelectorExpr)
		return sel
	}
	return nil
}

// mapsCall rewrites maps.Keys, maps.Values and maps.All over ordered keys, and records the
// other maps functions that call or yield in map order.
func (r *rewriter) mapsCall(call *ast.CallExpr, sel *ast.SelectorExpr, name string, id *ast.Ident) {
	switch name {
	case "Keys", "Values", "All":
		if len(call.Args) == 1 {
			if mt, ok := r.info.TypeOf(call.Args[0]).Underlying().(*types.Map); ok {
				if o := keyOrder(mt.Key()); o == "Keys" || o == "KeysCmp" {
					r.sched = true
					r.keepImport("maps", id)
					r.add(r.off(sel.Pos()), r.off(sel.End()), func() string { return "__fl_sched.Map" + name })
					return
				}
			}
		}
		r.gap(call, "maps iterator with unordered keys or type arguments")
	case "DeleteFunc", "EqualFunc":
		r.gap(call, "maps."+name)
	}
}

// reflectMethod returns the name of the reflect.Value method s selects, or "".
func (r *rewriter) reflectMethod(s *types.Selection) string {
	fn, ok := s.Obj().(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "reflect" {
		return ""
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return ""
	}
	if n, ok := types.Unalias(sig.Recv().Type()).(*types.Named); ok && n.Obj().Name() == "Value" {
		return fn.Name()
	}
	return ""
}
