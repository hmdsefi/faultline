// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"testing"
)

// depImporter serves package example.com/q from source and every other package from the
// default importer.
type depImporter struct {
	q   *types.Package
	std types.Importer
}

func (i depImporter) Import(path string) (*types.Package, error) {
	if path == "example.com/q" && i.q != nil {
		return i.q, nil
	}
	return i.std.Import(path)
}

// rewriteSource type-checks src as package p (file /src/p/a.go) and applies EXB-034; an
// unchanged file is returned as it was. A non-empty dep is the source of package example.com/q,
// which src may import; the standard library comes from the default importer.
func rewriteSource(t *testing.T, src, dep string) (string, []string) {
	t.Helper()
	fset := token.NewFileSet()
	imp := depImporter{std: importer.Default()}
	if dep != "" {
		qf, err := parser.ParseFile(fset, "/src/q/q.go", dep, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if imp.q, err = (&types.Config{Importer: imp.std}).Check("example.com/q", fset, []*ast.File{qf}, nil); err != nil {
			t.Fatal(err)
		}
	}
	f, err := parser.ParseFile(fset, "/src/p/a.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	pkg, err := (&types.Config{Importer: imp}).Check("p", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatal(err)
	}
	rw := &rewriter{fset: fset, tf: fset.File(f.Pos()), file: f, src: []byte(src), info: info, pkg: pkg, path: "/src/p/a.go"}
	rw.rewrite()
	if len(rw.edits) == 0 {
		return src, rw.gaps
	}
	return rw.output(), rw.gaps
}

// TestRewriteGolden pins the EXB-034 rewrites, one construct per case: scheduling points, go
// statements, sync methods, select with and without default, map ranges, channel ranges,
// math/rand, and the recorded gaps; then go statements whose operands a short variable
// declaration cannot hold, labeled selects with blank receive variables, ranges over
// type-parameter values, and a range over a dereferenced map pointer; then select element types
// (an interface keeps the comma-ok assertion; shadowed, unexported and other packages' names),
// go statements with constant and untyped operands and with sync methods, selects a goto names,
// a rewritten select with a default, ordered map ranges without a value, edits nested in a
// rewritten go statement, and the element type names of dot imports, aliases, type parameters,
// shadowed predeclared types and other packages' interface methods, and untyped shifts of named
// and qualified constants and of builtin calls.
func TestRewriteGolden(t *testing.T) {
	for i, tc := range []struct {
		src, dep, want string
		gaps           []string
	}{
		{
			src: "package p\n\nfunc f(ch chan int, v int) {\n\tch <- v\n\tw := <-ch\n\t_ = w\n\tclose(ch)\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func f(ch chan int, v int) {
	__fl_sched.Point(); ch <- v
	__fl_sched.Point(); w := <-ch
	_ = w
	__fl_sched.Point(); close(ch)
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nfunc g(a, b int) {}\n\nfunc f(xs []int) {\n\tgo g(1, len(xs))\n\tgo func() {}()\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func g(a, b int) {}

func f(xs []int) {
	{ __fl_g1, __fl_g2 := g, len(xs); __fl_sched.Go(func() { __fl_g1(1, __fl_g2) }) }
	{ __fl_g3 := func() {}; __fl_sched.Go(func() { __fl_g3() }) }
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nimport \"sync\"\n\ntype S struct {\n\tsync.Mutex\n\tmu sync.Mutex\n\trw *sync.RWMutex\n\to  sync.Once\n\twg sync.WaitGroup\n}\n\nfunc (s *S) f() {\n\ts.Lock()\n\tdefer s.Unlock()\n\ts.mu.Lock()\n\ts.mu.Unlock()\n\ts.rw.RLock()\n\ts.rw.RUnlock()\n\ts.rw.Lock()\n\ts.rw.Unlock()\n\ts.o.Do(func() {})\n\ts.wg.Wait()\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

import "sync"

type S struct {
	sync.Mutex
	mu sync.Mutex
	rw *sync.RWMutex
	o  sync.Once
	wg sync.WaitGroup
}

func (s *S) f() {
	__fl_sched.Lock(&(s).Mutex)
	defer __fl_sched.Unlock(&(s).Mutex)
	__fl_sched.Lock(&(s.mu))
	__fl_sched.Unlock(&(s.mu))
	__fl_sched.RLock((s.rw))
	__fl_sched.RUnlock((s.rw))
	__fl_sched.WLock((s.rw))
	__fl_sched.WUnlock((s.rw))
	__fl_sched.OnceDo(&(s.o), func() {})
	__fl_sched.Point(); s.wg.Wait()
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nfunc f(a chan int, b chan string, q chan struct{}) int {\n\tselect {\n\tcase v, ok := <-a:\n\t\tif ok {\n\t\t\treturn v\n\t\t}\n\tcase b <- \"x\":\n\tcase <-q:\n\t\treturn -1\n\t}\n\treturn 0\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"; import __fl_reflect "reflect"

func f(a chan int, b chan string, q chan struct{}) int {
	__fl_sched.Point(); { __fl_c1, __fl_v2, __fl_ok3 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(a)}, {Dir: __fl_reflect.SelectSend, Chan: __fl_reflect.ValueOf(b), Send: __fl_reflect.ValueOf(&[]string{"x"}[0]).Elem()}, {Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(q)}}, false); _, _ = __fl_v2, __fl_ok3; switch __fl_c1 {
	case 0: v := __fl_v2.Interface().(int); ok := __fl_ok3;
		if ok {
			return v
		}
	case 1:
	case 2:
		return -1
	default: panic("faultline-toolexec: sched.Select returned no case") }}
	return 0
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nfunc f(a chan int) int {\n\tselect {\n\tcase v := <-a:\n\t\treturn v\n\tdefault:\n\t\treturn 0\n\t}\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func f(a chan int) int {
	__fl_sched.Point(); select {
	case v := <-a:
		return v
	default:
		return 0
	}
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nfunc f(m map[string]int, n map[[2]int]bool) (s int) {\n\tfor k, v := range m {\n\t\ts += len(k) + v\n\t}\n\tfor k := range n {\n\t\t_ = k\n\t}\n\treturn\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func f(m map[string]int, n map[[2]int]bool) (s int) {
	for _, __fl_k1 := range __fl_sched.Keys(m) { v, __fl_ok2 := m[__fl_k1]; if !__fl_ok2 { continue }; k := __fl_k1;
		s += len(k) + v
	}
	for k := range n {
		_ = k
	}
	return
}
`,
			gaps: []string{"/src/p/a.go:7: range over a map with non-ordered keys"},
		},
		{
			src: "package p\n\nfunc f(ch chan int) (s int) {\n\tfor v := range ch {\n\t\ts += v\n\t}\n\treturn\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func f(ch chan int) (s int) {
	__fl_sched.Point(); for v := range ch { __fl_sched.Point();
		s += v
	}
	return
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nimport (\n\t\"math/rand\"\n\tr2 \"math/rand/v2\"\n)\n\nfunc f() int { return rand.Intn(6) + r2.IntN(6) }\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

import (
	"math/rand"
	r2 "math/rand/v2"
)

func f() int { return __fl_sched.RandV1().Intn(6) + __fl_sched.Rand().IntN(6) }

var _ r2.Source

var _ rand.Source
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nimport (\n\t\"sync\"\n\t\"sync/atomic\"\n)\n\nfunc f(c *sync.Cond, m *sync.Mutex, n *atomic.Int64) func() {\n\tc.Wait()\n\tn.Add(1)\n\treturn m.Lock\n}\n",
			want: `package p

import (
	"sync"
	"sync/atomic"
)

func f(c *sync.Cond, m *sync.Mutex, n *atomic.Int64) func() {
	c.Wait()
	n.Add(1)
	return m.Lock
}
`,
			gaps: []string{"/src/p/a.go:9: sync.Cond", "/src/p/a.go:10: sync/atomic", "/src/p/a.go:11: method value Mutex.Lock"},
		},
		{
			src: "package p\n\nfunc w[T any](c chan T) {}\n\nfunc r(n int64, p *int) {}\n\nfunc f(c chan int) {\n\tgo w(c)\n\tgo r(1, nil)\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func w[T any](c chan T) {}

func r(n int64, p *int) {}

func f(c chan int) {
	{ __fl_g1 := c; __fl_sched.Go(func() { w(__fl_g1) }) }
	{ __fl_g2 := r; __fl_sched.Go(func() { __fl_g2(1, nil) }) }
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nfunc f(a chan int, b chan string) {\nL:\n\tselect {\n\tcase _, ok := <-a:\n\t\t_ = ok\n\t\tbreak L\n\tcase v, _ := <-b:\n\t\t_ = v\n\t}\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"; import __fl_reflect "reflect"

func f(a chan int, b chan string) {

	__fl_sched.Point(); { __fl_c1, __fl_v2, __fl_ok3 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(a)}, {Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(b)}}, false); _, _ = __fl_v2, __fl_ok3; L: switch __fl_c1 {
	case 0: ok := __fl_ok3;
		_ = ok
		break L
	case 1: v := __fl_v2.Interface().(string);
		_ = v
	default: panic("faultline-toolexec: sched.Select returned no case") }}
}
`,
			gaps: []string(nil),
		},
		{
			src:  "package p\n\nfunc h[C ~chan int](c C) {\n\tfor v := range c {\n\t\t_ = v\n\t}\n}\n",
			want: "package p\n\nfunc h[C ~chan int](c C) {\n\tfor v := range c {\n\t\t_ = v\n\t}\n}\n",
			gaps: []string{"/src/p/a.go:4: range over a channel or map of type-parameter type"},
		},
		{
			src: "package p\n\nfunc f(p *map[string]int) (s int) {\n\tfor k, v := range *p {\n\t\ts += len(k) + v\n\t}\n\treturn\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func f(p *map[string]int) (s int) {
	for _, __fl_k1 := range __fl_sched.Keys(*p) { v, __fl_ok2 := (*p)[__fl_k1]; if !__fl_ok2 { continue }; k := __fl_k1;
		s += len(k) + v
	}
	return
}
`,
			gaps: []string(nil),
		},
		{
			src: `package p

type T int

func f(a chan error, b chan int) error {
	select {
	case e := <-a:
		return e
	case <-b:
	}
	return nil
}

func g(ch chan T, other chan int) {
	type T string
	select {
	case v := <-ch:
		_ = v
	case <-other:
	}
}
`,
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"; import __fl_reflect "reflect"

type T int

func f(a chan error, b chan int) error {
	__fl_sched.Point(); { __fl_c1, __fl_v2, __fl_ok3 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(a)}, {Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(b)}}, false); _, _ = __fl_v2, __fl_ok3; switch __fl_c1 {
	case 0: e, _ := __fl_v2.Interface().(error);
		return e
	case 1:
	default: panic("faultline-toolexec: sched.Select returned no case") }}
	return nil
}

func g(ch chan T, other chan int) {
	type T string
	__fl_sched.Point(); select {
	case v := <-ch:
		_ = v
	case <-other:
	}
}
`,
			gaps: []string{"/src/p/a.go:16: select element type not nameable in this file"},
		},
		{
			dep: "package q\n\ntype event struct{ n int }\n\ntype Event struct{ N int }\n\nfunc Events() chan event { return nil }\n\nfunc Anon() chan struct{ n int } { return nil }\n",
			src: `package p

import qq "example.com/q"

func f(b chan int, c chan qq.Event) {
	select {
	case e := <-qq.Events():
		_ = e
	case <-b:
	}
	select {
	case e := <-qq.Anon():
		_ = e
	case <-b:
	}
	select {
	case e := <-c:
		_ = e
	case <-b:
	}
	{
		qq := 0
		_ = qq
		select {
		case e := <-c:
			_ = e
		case <-b:
		}
	}
}
`,
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"; import __fl_reflect "reflect"

import qq "example.com/q"

func f(b chan int, c chan qq.Event) {
	__fl_sched.Point(); select {
	case e := <-qq.Events():
		_ = e
	case <-b:
	}
	__fl_sched.Point(); select {
	case e := <-qq.Anon():
		_ = e
	case <-b:
	}
	__fl_sched.Point(); { __fl_c1, __fl_v2, __fl_ok3 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(c)}, {Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(b)}}, false); _, _ = __fl_v2, __fl_ok3; switch __fl_c1 {
	case 0: e := __fl_v2.Interface().(qq.Event);
		_ = e
	case 1:
	default: panic("faultline-toolexec: sched.Select returned no case") }}
	{
		qq := 0
		_ = qq
		__fl_sched.Point(); select {
		case e := <-c:
			_ = e
		case <-b:
		}
	}
}
`,
			gaps: []string{"/src/p/a.go:6: select element type not nameable in this file", "/src/p/a.go:11: select element type not nameable in this file",
				"/src/p/a.go:24: select element type not nameable in this file"},
		},
		{
			src: `package p

type B bool

func h(f float64, r int) {}

func hb(b B) {}

func k(n int64) {}

func w[T any](x T) {}

func f(a, b int, n uint) {
	go h(2, 'x')
	go hb(a == b)
	go k(1 << n)
	go w(3)
}

func g(a, b int) {
	type B int
	go hb(a == b)
}
`,
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

type B bool

func h(f float64, r int) {}

func hb(b B) {}

func k(n int64) {}

func w[T any](x T) {}

func f(a, b int, n uint) {
	{ __fl_g1 := h; __fl_sched.Go(func() { __fl_g1(2, 'x') }) }
	{ __fl_g2, __fl_g3 := hb, B(a == b); __fl_sched.Go(func() { __fl_g2(__fl_g3) }) }
	{ __fl_g4, __fl_g5 := k, int64(1 << n); __fl_sched.Go(func() { __fl_g4(__fl_g5) }) }
	{ __fl_sched.Go(func() { w(3) }) }
}

func g(a, b int) {
	type B int
	go hb(a == b)
}
`,
			gaps: []string{"/src/p/a.go:22: go statement argument type not nameable in this file"},
		},
		{
			src: "package p\n\nimport \"sync\"\n\nfunc f(mu *sync.Mutex, o *sync.Once, fn func()) {\n\tgo mu.Lock()\n\tgo o.Do(fn)\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

import "sync"

func f(mu *sync.Mutex, o *sync.Once, fn func()) {
	{ __fl_g1 := mu.Lock; __fl_sched.Go(func() { __fl_g1() }) }
	{ __fl_g2, __fl_g3 := o.Do, fn; __fl_sched.Go(func() { __fl_g2(__fl_g3) }) }
}
`,
			gaps: []string{"/src/p/a.go:6: method value Mutex.Lock", "/src/p/a.go:7: method value Once.Do"},
		},
		{
			src: `package p

func f(a, b chan int) (n int) {
L:
	select {
	case <-a:
		n++
		if n < 3 {
			goto L
		}
	case <-b:
	}
	return
}

func g(a, b chan int) {
	goto M
M:
	select {
	case <-a:
	case <-b:
	}
}
`,
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func f(a, b chan int) (n int) {
__fl_sched.Point(); L:
	select {
	case <-a:
		n++
		if n < 3 {
			goto L
		}
	case <-b:
	}
	return
}

func g(a, b chan int) {
	goto M
__fl_sched.Point(); M:
	select {
	case <-a:
	case <-b:
	}
}
`,
			gaps: []string{"/src/p/a.go:5: select named by a goto", "/src/p/a.go:19: select named by a goto"},
		},
		{
			src: "package p\n\nfunc f(a chan int, b chan string) int {\n\tselect {\n\tcase v := <-a:\n\t\treturn v\n\tcase b <- \"x\":\n\tdefault:\n\t}\n\treturn 0\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"; import __fl_reflect "reflect"

func f(a chan int, b chan string) int {
	__fl_sched.Point(); { __fl_c1, __fl_v2, __fl_ok3 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(a)}, {Dir: __fl_reflect.SelectSend, Chan: __fl_reflect.ValueOf(b), Send: __fl_reflect.ValueOf(&[]string{"x"}[0]).Elem()}}, true); _, _ = __fl_v2, __fl_ok3; switch __fl_c1 {
	case 0: v := __fl_v2.Interface().(int);
		return v
	case 1:
	default:
	}}
	return 0
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nfunc f(m map[string]int) (s int) {\n\tfor k := range m {\n\t\ts += len(k)\n\t}\n\tvar k string\n\tvar v int\n\tfor k, v = range m {\n\t\ts += len(k) + v\n\t}\n\tfor range m {\n\t\ts++\n\t}\n\treturn\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

func f(m map[string]int) (s int) {
	for _, __fl_k1 := range __fl_sched.Keys(m) { if _, __fl_ok2 := m[__fl_k1]; !__fl_ok2 { continue }; k := __fl_k1;
		s += len(k)
	}
	var k string
	var v int
	for _, __fl_k3 := range __fl_sched.Keys(m) { if _, __fl_ok4 := m[__fl_k3]; !__fl_ok4 { continue }; v = m[__fl_k3]; k = __fl_k3;
		s += len(k) + v
	}
	for _, __fl_k5 := range __fl_sched.Keys(m) { if _, __fl_ok6 := m[__fl_k5]; !__fl_ok6 { continue };
		s++
	}
	return
}
`,
			gaps: []string(nil),
		},
		{
			src: "package p\n\nimport (\n\t\"math/rand\"\n\t\"sync\"\n)\n\nfunc f(mu *sync.Mutex, ch chan int) {\n\tgo func() {\n\t\tmu.Lock()\n\t\tch <- rand.Intn(6)\n\t}()\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

import (
	"math/rand"
	"sync"
)

func f(mu *sync.Mutex, ch chan int) {
	{ __fl_g1 := func() {
		__fl_sched.Lock((mu))
		__fl_sched.Point(); ch <- __fl_sched.RandV1().Intn(6)
	}; __fl_sched.Go(func() { __fl_g1() }) }
}

var _ rand.Source
`,
			gaps: []string(nil),
		},
		{
			dep: "package q\n\ntype Event struct{ N int }\n\nfunc Is() chan interface{ m() } { return nil }\n",
			src: `package p

import . "example.com/q"

type A = []int

func f(a chan Event, b chan int, c chan A) {
	select {
	case e := <-a:
		_ = e
	case <-b:
	}
	select {
	case e := <-Is():
		_ = e
	case <-b:
	}
	select {
	case e := <-c:
		_ = e
	case <-b:
	}
	{
		type int = string
		select {
		case <-b:
		case x := <-b:
			_ = x
		}
	}
}

func g[T any](a chan T, b chan int) {
	select {
	case v := <-a:
		_ = v
	case <-b:
	}
}
`,
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"; import __fl_reflect "reflect"

import . "example.com/q"

type A = []int

func f(a chan Event, b chan int, c chan A) {
	__fl_sched.Point(); { __fl_c1, __fl_v2, __fl_ok3 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(a)}, {Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(b)}}, false); _, _ = __fl_v2, __fl_ok3; switch __fl_c1 {
	case 0: e := __fl_v2.Interface().(Event);
		_ = e
	case 1:
	default: panic("faultline-toolexec: sched.Select returned no case") }}
	__fl_sched.Point(); select {
	case e := <-Is():
		_ = e
	case <-b:
	}
	__fl_sched.Point(); { __fl_c4, __fl_v5, __fl_ok6 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(c)}, {Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(b)}}, false); _, _ = __fl_v5, __fl_ok6; switch __fl_c4 {
	case 0: e := __fl_v5.Interface().(A);
		_ = e
	case 1:
	default: panic("faultline-toolexec: sched.Select returned no case") }}
	{
		type int = string
		__fl_sched.Point(); select {
		case <-b:
		case x := <-b:
			_ = x
		}
	}
}

func g[T any](a chan T, b chan int) {
	__fl_sched.Point(); { __fl_c7, __fl_v8, __fl_ok9 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(a)}, {Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(b)}}, false); _, _ = __fl_v8, __fl_ok9; switch __fl_c7 {
	case 0: v, _ := __fl_v8.Interface().(T);
		_ = v
	case 1:
	default: panic("faultline-toolexec: sched.Select returned no case") }}
}
`,
			gaps: []string{"/src/p/a.go:13: select element type not nameable in this file", "/src/p/a.go:25: select element type not nameable in this file"},
		},
		{
			src: "package p\n\nimport \"math\"\n\nconst one = 1\n\nfunc k(n int64) {}\n\nfunc f(n uint) {\n\tgo k(one << n)\n\tgo k(math.MaxInt8 << n)\n\tgo k(-(min(1, 2) << n))\n}\n",
			want: `//line /src/p/a.go:1
package p; import __fl_sched "github.com/hmdsefi/faultline/runner/toolexec/sched"

import "math"

const one = 1

func k(n int64) {}

func f(n uint) {
	{ __fl_g1, __fl_g2 := k, int64(one << n); __fl_sched.Go(func() { __fl_g1(__fl_g2) }) }
	{ __fl_g3, __fl_g4 := k, int64(math.MaxInt8 << n); __fl_sched.Go(func() { __fl_g3(__fl_g4) }) }
	{ __fl_g5, __fl_g6 := k, int64(-(min(1, 2) << n)); __fl_sched.Go(func() { __fl_g5(__fl_g6) }) }
}
`,
			gaps: []string(nil),
		},
	} {
		got, gaps := rewriteSource(t, tc.src, tc.dep)
		if got != tc.want {
			t.Errorf("case %d: got\n%s\nwant\n%s", i, got, tc.want)
		}
		if !reflect.DeepEqual(gaps, tc.gaps) {
			t.Errorf("case %d: gaps %q, want %q", i, gaps, tc.gaps)
		}
	}
}
