// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// AT-XTE-12: the map, maps, rand and reflect rows of XTE-041.
func TestATXTE12_MapRandReflectRows(t *testing.T) {
	for i, c := range []struct{ src, want string }{
		{ // key orders: Keys for integers and strings, KeysCmp for arrays, bools and structs; each map is bound once
			src: "package p\n\ntype K struct {\n\ta int\n\tb string\n}\n\nfunc f(m map[string]int, n map[[2]int]bool, b map[bool]int, s map[K]int) (t int) {\n" +
				"\tfor k, v := range m {\n\t\tt += len(k) + v\n\t}\n\tfor k := range n {\n\t\tt += k[0]\n\t}\n\tfor k := range b {\n\t\t_ = k\n\t}\n\tfor k, v := range s {\n\t\tt += k.a + v\n\t}\n\treturn\n}\n",
			want: header + "\ntype K struct {\n\ta int\n\tb string\n}\n\nfunc f(m map[string]int, n map[[2]int]bool, b map[bool]int, s map[K]int) (t int) {\n" +
				"\t{ __fl_m1 := m; for _, __fl_k2 := range __fl_sched.Keys(__fl_m1) { v, __fl_ok3 := __fl_m1[__fl_k2]; if !__fl_ok3 { continue }; k := __fl_k2; {\n\t\tt += len(k) + v\n\t}}}\n" +
				"\t{ __fl_m4 := n; for _, __fl_k5 := range __fl_sched.KeysCmp(__fl_m4) { if _, __fl_ok6 := __fl_m4[__fl_k5]; !__fl_ok6 { continue }; k := __fl_k5; {\n\t\tt += k[0]\n\t}}}\n" +
				"\t{ __fl_m7 := b; for _, __fl_k8 := range __fl_sched.KeysCmp(__fl_m7) { if _, __fl_ok9 := __fl_m7[__fl_k8]; !__fl_ok9 { continue }; k := __fl_k8; {\n\t\t_ = k\n\t}}}\n" +
				"\t{ __fl_m10 := s; for _, __fl_k11 := range __fl_sched.KeysCmp(__fl_m10) { v, __fl_ok12 := __fl_m10[__fl_k11]; if !__fl_ok12 { continue }; k := __fl_k11; {\n\t\tt += k.a + v\n\t}}}\n\treturn\n}\n",
		},
		{ // a map expression with side effects is evaluated once, before the loop
			src: "package p\n\nfunc g() map[string]int { return nil }\n\nfunc f() (t int) {\n\tfor k := range g() {\n\t\tt += len(k)\n\t}\n\treturn\n}\n",
			want: header + "\nfunc g() map[string]int { return nil }\n\nfunc f() (t int) {\n" +
				"\t{ __fl_m1 := g(); for _, __fl_k2 := range __fl_sched.Keys(__fl_m1) { if _, __fl_ok3 := __fl_m1[__fl_k2]; !__fl_ok3 { continue }; k := __fl_k2; {\n\t\tt += len(k)\n\t}}}\n\treturn\n}\n",
		},
		{ // maps iterators, a math/rand function value, math/rand/v2.N and reflect.Select
			src: "package p\n\nimport (\n\t\"maps\"\n\t\"math/rand\"\n\tr2 \"math/rand/v2\"\n\t\"reflect\"\n\t\"time\"\n)\n\n" +
				"func f(m map[string]int, cases []reflect.SelectCase) {\n\tfor k := range maps.Keys(m) {\n\t\t_ = k\n\t}\n\tfor v := range maps.Values(m) {\n\t\t_ = v\n\t}\n" +
				"\tfor k, v := range maps.All(m) {\n\t\t_, _ = k, v\n\t}\n\tpick := rand.Intn\n\t_ = pick(6)\n\t_ = r2.N(10 * time.Second)\n\t_, _, _ = reflect.Select(cases)\n}\n",
			want: header + "\nimport (\n\t\"maps\"\n\t\"math/rand\"\n\tr2 \"math/rand/v2\"\n\t\"reflect\"\n\t\"time\"\n)\n\n" +
				"func f(m map[string]int, cases []reflect.SelectCase) {\n\tfor k := range __fl_sched.MapKeys(m) {\n\t\t_ = k\n\t}\n\tfor v := range __fl_sched.MapValues(m) {\n\t\t_ = v\n\t}\n" +
				"\tfor k, v := range __fl_sched.MapAll(m) {\n\t\t_, _ = k, v\n\t}\n\tpick := __fl_sched.RandV1().Intn\n\t_ = pick(6)\n\t_ = __fl_sched.N(10 * time.Second)\n\t_, _, _ = __fl_sched.ReflectSelect(cases)\n}\n" +
				"\nvar _ = maps.Copy[map[int]int, map[int]int]\n\nvar _ r2.Source\n\nvar _ rand.Source\n\nvar _ reflect.Value\n",
		},
	} {
		got, gaps := rewriteSource(t, c.src, "")
		if got != c.want || len(gaps) != 0 {
			t.Errorf("case %d: got\n%s\ngaps %q\nwant\n%s", i, got, gaps, c.want)
		}
	}
}

// AT-XTE-13: every kind of XTE-042 that a source reaches is recorded with its exact text; atomics
// and sync.Pool are neither rewritten nor recorded. "select case with an unexpected receive form"
// is defensive (the parser accepts no other receive form) and rule 2's gap is AT-XTE-07's.
func TestATXTE13_GapKinds(t *testing.T) {
	for i, c := range []struct {
		src   string
		kinds []string
	}{
		{"package p\n\nfunc f(a map[float64]int, b map[*int]int, c map[struct{ x float32 }]int) {\n\tfor k := range a {\n\t\t_ = k\n\t}\n\tfor k := range b {\n\t\t_ = k\n\t}\n\tfor k := range c {\n\t\t_ = k\n\t}\n}\n",
			[]string{"range over a map with floating-point keys", "range over a map with unordered keys", "range over a map with floating-point keys"}},
		{"package p\n\nfunc h[M ~map[string]int](m M) (n int) {\n\tfor k := range m {\n\t\tn += len(k)\n\t}\n\treturn\n}\n",
			[]string{"range over a channel or map of type-parameter type"}},
		{"package p\n\nfunc g() map[string]int { return nil }\n\nfunc f() {\nL:\n\tfor k := range g() {\n\t\tif k == \"\" {\n\t\t\tcontinue L\n\t\t}\n\t}\n}\n",
			[]string{"range over a map expression with side effects"}},
		{"package p\n\nimport \"maps\"\n\nfunc f(mp map[*int]int, ms map[string]int) {\n\tfor k := range maps.Keys(mp) {\n\t\t_ = k\n\t}\n\tfor k := range maps.Keys[map[string]int](ms) {\n\t\t_ = k\n\t}\n" +
			"\tmaps.DeleteFunc(ms, func(string, int) bool { return false })\n\t_ = maps.EqualFunc(ms, ms, func(a, b int) bool { return a == b })\n}\n",
			[]string{"maps iterator with unordered keys or type arguments", "maps iterator with unordered keys or type arguments", "maps.DeleteFunc", "maps.EqualFunc"}},
		{"package p\n\nimport (\n\t\"reflect\"\n\t\"sync\"\n)\n\nfunc f(sm *sync.Map, v reflect.Value) {\n\tsm.Range(func(k, v any) bool { return true })\n\t_ = v.MapKeys()\n\t_ = v.MapRange()\n" +
			"\tv.Send(reflect.ValueOf(1))\n\t_, _ = v.Recv()\n\t_ = v.TrySend(reflect.ValueOf(1))\n\t_, _ = v.TryRecv()\n\tv.Close()\n}\n",
			[]string{"sync.Map.Range", "reflect map iteration", "reflect map iteration", "reflect channel operation", "reflect channel operation", "reflect channel operation", "reflect channel operation", "reflect channel operation"}},
		{"package p\n\nimport (\n\t\"sync\"\n\t\"sync/atomic\"\n)\n\nfunc f(mu *sync.Mutex, l sync.Locker, n *atomic.Int64, b *atomic.Bool) (func(*sync.Mutex), func()) {\n\tn.Add(1)\n\tatomic.AddInt64(new(int64), 1)\n\t_ = b.Load\n\treturn (*sync.Mutex).Lock, l.Lock\n}\n",
			[]string{"method expression Mutex.Lock", "method value of an interface Lock or Unlock"}},
		{"package p\n\nfunc f(ch chan int, x bool) {\n\tif x {\n\t} else if <-ch > 0 {\n\t}\n}\n",
			[]string{"channel operation in else-if header"}},
		{"package p\n\nfunc f(a, b chan int) {\nL:\n\tselect {\n\tcase <-a:\n\t\tgoto L\n\tcase <-b:\n\t}\n}\n",
			[]string{"select named by a goto"}},
		{"package p\n\ntype T struct{}\n\nfunc f(a, b chan T) {\n\ttype T int\n\tselect {\n\tcase <-a:\n\tcase <-b:\n\t}\n}\n",
			[]string{"select element type not nameable in this file"}},
		{"package p\n\nfunc g[C ~chan int](a, b C) {\n\tselect {\n\tcase <-a:\n\tcase <-b:\n\t}\n}\n",
			[]string{"select on a channel of unresolved type"}},
		{"package p\n\ntype B bool\n\nfunc hb(b B) {}\n\nfunc f(a, b int) {\n\ttype B int\n\tgo hb(a == b)\n}\n",
			[]string{"go statement argument type not nameable in this file"}},
		{"package p\n\nimport (\n\t\"sync\"\n\t\"sync/atomic\"\n)\n\nvar pool = sync.Pool{New: func() any { return new(int) }}\n\n" +
			"func f(n *atomic.Int64, b *atomic.Bool) int64 {\n\tn.Add(1)\n\tatomic.AddInt64(new(int64), 1)\n\t_ = b.Load\n\t_ = pool.Get()\n\treturn n.Load()\n}\n", nil},
	} {
		got, gaps := rewriteSource(t, c.src, "")
		var kinds []string
		for _, g := range gaps {
			_, kind, _ := strings.Cut(strings.TrimPrefix(g, "/src/p/a.go:"), ": ")
			kinds = append(kinds, kind)
		}
		if !slices.Equal(kinds, c.kinds) {
			t.Errorf("case %d: kinds %q, want %q", i, kinds, c.kinds)
		}
		if c.kinds == nil && got != c.src {
			t.Errorf("case %d: a file without gaps or rewrites changed:\n%s", i, got)
		}
	}
}

// keyOrder on the key types AT-XTE-12 and AT-XTE-13 leave out: a floating-point array element, a
// struct with a float and a pointer, nested orderable fields, complex numbers and interfaces.
func TestKeyOrder(t *testing.T) {
	for _, c := range []struct{ typ, want string }{
		{"[2]float64", "float"}, {"struct{ x float32; p *int }", ""}, {"struct{ a [2]bool; s string }", "KeysCmp"},
		{"uintptr", "Keys"}, {"complex128", ""}, {"any", ""},
	} {
		tv, err := types.Eval(token.NewFileSet(), nil, token.NoPos, c.typ)
		if err != nil {
			t.Fatal(err)
		}
		if got := keyOrder(tv.Type); got != c.want {
			t.Errorf("keyOrder(%s) = %q, want %q", c.typ, got, c.want)
		}
	}
}

// maps.DeleteFunc and maps.EqualFunc with explicit type arguments, and the method-expression call
// of (*sync.Map).Range, record their kinds (XTE-042).
func TestGapFormsTypeArgsAndMethodExpr(t *testing.T) {
	src := "package p\n\nimport (\n\t\"maps\"\n\t\"sync\"\n)\n\nfunc f(m map[string]int, sm *sync.Map) {\n" +
		"\tmaps.DeleteFunc[map[string]int](m, func(string, int) bool { return false })\n" +
		"\t_ = maps.EqualFunc[map[string]int, map[string]int](m, m, func(a, b int) bool { return a == b })\n" +
		"\t(*sync.Map).Range(sm, func(k, v any) bool { return true })\n}\n"
	_, gaps := rewriteSource(t, src, "")
	var kinds []string
	for _, g := range gaps {
		_, kind, _ := strings.Cut(strings.TrimPrefix(g, "/src/p/a.go:"), ": ")
		kinds = append(kinds, kind)
	}
	if want := "maps.DeleteFunc|maps.EqualFunc|sync.Map.Range"; strings.Join(kinds, "|") != want {
		t.Errorf("kinds %q, want %q", kinds, want)
	}
}

// The map, maps and rand rows on the forms AT-XTE-12 leaves out: a file whose only rewrite is a
// maps iterator or a math/rand value (it still imports sched), maps iterators over KeysCmp keys,
// a bound map expression with KeysCmp keys, and every name of XTE-041's two rand lists.
func TestMapRandRowsForms(t *testing.T) {
	for i, src := range []string{
		"package p\n\nimport \"maps\"\n\nfunc f(m map[string]int) {\n\tfor k := range maps.Keys(m) {\n\t\t_ = k\n\t}\n}\n",
		"package p\n\nimport \"math/rand\"\n\nfunc f() int { return rand.Intn(6) }\n",
	} {
		if got, gaps := rewriteSource(t, src, ""); !strings.HasPrefix(got, header) || len(gaps) != 0 {
			t.Errorf("case %d: the file does not import sched:\n%s\ngaps %q", i, got, gaps)
		}
	}
	src := "package p\n\nimport \"maps\"\n\nfunc g() map[[2]int]bool { return nil }\n\nfunc f(m map[bool]int) {\n" +
		"\tfor v := range maps.Values(m) {\n\t\t_ = v\n\t}\n\tfor k := range g() {\n\t\t_ = k\n\t}\n}\n"
	got, gaps := rewriteSource(t, src, "")
	if !strings.Contains(got, "range __fl_sched.MapValues(m)") || !strings.Contains(got, "{ __fl_m1 := g(); for _, __fl_k2 := range __fl_sched.KeysCmp(__fl_m1) {") || len(gaps) != 0 {
		t.Errorf("KeysCmp keys: got\n%s\ngaps %q", got, gaps)
	}
	v1 := []string{"ExpFloat64", "Float32", "Float64", "Int", "Int31", "Int31n", "Int63", "Int63n", "Intn", "NormFloat64", "Perm", "Read", "Shuffle", "Uint32", "Uint64"}
	v2 := []string{"ExpFloat64", "Float32", "Float64", "Int", "Int32", "Int32N", "Int64", "Int64N", "IntN", "NormFloat64", "Perm", "Shuffle", "Uint", "Uint32", "Uint32N", "Uint64", "Uint64N", "UintN"}
	src = "package p\n\nimport (\n\t\"math/rand\"\n\tr2 \"math/rand/v2\"\n)\n\n"
	for _, n := range v1 {
		src += "var _ = rand." + n + "\n"
	}
	for _, n := range v2 {
		src += "var _ = r2." + n + "\n"
	}
	got, _ = rewriteSource(t, src, "")
	for _, n := range v1 {
		if !strings.Contains(got, "var _ = __fl_sched.RandV1()."+n+"\n") {
			t.Errorf("math/rand.%s is not rewritten", n)
		}
	}
	for _, n := range v2 {
		if !strings.Contains(got, "var _ = __fl_sched.Rand()."+n+"\n") {
			t.Errorf("math/rand/v2.%s is not rewritten", n)
		}
	}
}

// The gap rows on the forms AT-XTE-13 leaves out, and gap sites left unchanged (XTE-042: "The
// rewriter leaves these unchanged"); indexed calls of other packages' functions with those names
// and the other sync.Map methods record nothing.
func TestGapFormsUnchanged(t *testing.T) {
	for i, c := range []struct {
		src   string
		kinds []string
	}{
		{"package p\n\nfunc f(a map[float64]int, b map[*int]int) {\n\tfor k := range a {\n\t\t_ = k\n\t}\n\tfor k := range b {\n\t\t_ = k\n\t}\n}\n",
			[]string{"range over a map with floating-point keys", "range over a map with unordered keys"}},
		{"package p\n\nimport \"maps\"\n\nfunc f(m map[string]int, mf map[float32]int) {\n\tfor v := range maps.Values[map[string]int](m) {\n\t\t_ = v\n\t}\n" +
			"\tfor k, v := range maps.All[map[string]int, string, int](m) {\n\t\t_, _ = k, v\n\t}\n\tfor k := range (maps.Keys[map[string]int])(m) {\n\t\t_ = k\n\t}\n" +
			"\tfor k := range maps.Keys(mf) {\n\t\t_ = k\n\t}\n}\n",
			[]string{"maps iterator with unordered keys or type arguments", "maps iterator with unordered keys or type arguments",
				"maps iterator with unordered keys or type arguments", "maps iterator with unordered keys or type arguments"}},
		{"package p\n\nimport \"reflect\"\n\ntype Value struct{}\n\nfunc (Value) Close()               {}\nfunc (Value) MapKeys() []int { return nil }\n\n" +
			"func f(v reflect.Value, u Value) {\n\t_ = reflect.Value.MapKeys(v)\n\treflect.Value.Close(v)\n\tu.Close()\n\t_ = u.MapKeys()\n}\n",
			[]string{"reflect map iteration", "reflect channel operation"}},
		{"package p\n\nimport (\n\t\"slices\"\n\t\"sync\"\n)\n\nfunc f(s []int, sm *sync.Map) {\n\t_ = slices.DeleteFunc[[]int](s, func(int) bool { return false })\n" +
			"\t_ = slices.Values[[]int](s)\n\t_, _ = sm.Load(1)\n\t_, _ = (*sync.Map).Load(sm, 1)\n}\n", nil},
	} {
		got, gaps := rewriteSource(t, c.src, "")
		var kinds []string
		for _, g := range gaps {
			_, kind, _ := strings.Cut(strings.TrimPrefix(g, "/src/p/a.go:"), ": ")
			kinds = append(kinds, kind)
		}
		if strings.Join(kinds, "|") != strings.Join(c.kinds, "|") {
			t.Errorf("case %d: kinds %q, want %q", i, kinds, c.kinds)
		}
		want := c.src
		if len(gaps) > 0 {
			want = header + strings.TrimPrefix(c.src, "package p\n") + "\nvar _ = __fl_sched.Gaps(" + strconv.Quote(strings.Join(gaps, "\n")) + ")\n"
		}
		if got != want {
			t.Errorf("case %d: a gap site changed:\n%s", i, got)
		}
	}
}

// A labeled map range is not bound, since a block would separate the label from its loop: the
// lookups repeat the map expression, a pointer's target in parentheses.
func TestLabeledMapRange(t *testing.T) {
	src := "package p\n\nfunc f(p *map[string]int) (s int) {\nL:\n\tfor k, v := range *p {\n\t\ts += len(k) + v\n\t\tcontinue L\n\t}\n\treturn\n}\n"
	want := header + "\nfunc f(p *map[string]int) (s int) {\nL:\n\tfor _, __fl_k1 := range __fl_sched.Keys(*p) { v, __fl_ok2 := (*p)[__fl_k1]; if !__fl_ok2 { continue }; k := __fl_k1; {\n" +
		"\t\ts += len(k) + v\n\t\tcontinue L\n\t}}\n\treturn\n}\n"
	if got, gaps := rewriteSource(t, src, ""); got != want || len(gaps) != 0 {
		t.Errorf("got\n%s\ngaps %q\nwant\n%s", got, gaps, want)
	}
}

// reassignSrc ranges over three-key maps whose loop bodies replace the map the range expression
// names: a variable, a field and a pointer's target.
const reassignSrc = `package u

type S struct{ items map[string]int }

func Ident() (n int) {
	m := map[string]int{"a": 1, "b": 2, "c": 3}
	for _, v := range m {
		m = nil
		n += v
	}
	return
}

func Field() (n int) {
	s := &S{items: map[string]int{"a": 1, "b": 2, "c": 3}}
	for k, v := range s.items {
		s.items = map[string]int{k: 0}
		n += v
	}
	return
}

func Star() (n int) {
	m := map[string]int{"a": 1, "b": 2, "c": 3}
	p := &m
	var v int
	for _, v = range *p {
		*p = map[string]int{}
		n += v
	}
	return
}
`

// Go evaluates a range expression once, so a loop body that replaces the ranged map still visits
// every key of the original map; built through the wrapper, the sums stay 6 (XTE-041).
func TestMapRangeEvaluatedOnce(t *testing.T) {
	dir := userModule(t, "u.go", reassignSrc, "u_test.go", "package u\n\nimport \"testing\"\n\nfunc TestSums(t *testing.T) {\n"+
		"\tif a, b, c := Ident(), Field(), Star(); a != 6 || b != 6 || c != 6 {\n\t\tt.Fatalf(\"sums %d %d %d, want 6 6 6\", a, b, c)\n\t}\n}\n")
	bin := filepath.Join(t.TempDir(), "u.test")
	build := exec.Command("go", "test", "-c", "-o", bin, "-toolexec="+wrapper, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -toolexec: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin, "-test.count=1", "-test.timeout=1m").CombinedOutput(); err != nil { //nolint:gosec // the binary the test built
		t.Fatalf("the test binary: %v\n%s", err, out)
	}
}

// A map range body that redeclares the loop variables, bound and labeled: the body keeps a block
// of its own, as in Go, where the loop body is a block inside the for statement's.
func TestMapRangeRedeclaredVars(t *testing.T) {
	src := "package p\n\nfunc f(m map[string]int) (n int) {\n\tfor k, v := range m {\n\t\tk, v := k, v\n\t\tn += len(k) + v\n\t}\n" +
		"L:\n\tfor k := range m {\n\t\tk := k + \"x\"\n\t\tn += len(k)\n\t\tcontinue L\n\t}\n\treturn\n}\n"
	want := header + "\nfunc f(m map[string]int) (n int) {\n" +
		"\t{ __fl_m1 := m; for _, __fl_k2 := range __fl_sched.Keys(__fl_m1) { v, __fl_ok3 := __fl_m1[__fl_k2]; if !__fl_ok3 { continue }; k := __fl_k2; {\n" +
		"\t\tk, v := k, v\n\t\tn += len(k) + v\n\t}}}\n" +
		"L:\n\tfor _, __fl_k4 := range __fl_sched.Keys(m) { if _, __fl_ok5 := m[__fl_k4]; !__fl_ok5 { continue }; k := __fl_k4; {\n" +
		"\t\tk := k + \"x\"\n\t\tn += len(k)\n\t\tcontinue L\n\t}}\n\treturn\n}\n"
	if got, gaps := rewriteSource(t, src, ""); got != want || len(gaps) != 0 {
		t.Errorf("got\n%s\ngaps %q\nwant\n%s", got, gaps, want)
	}
}

// redeclareSrc ranges over three-key maps whose loop bodies redeclare the loop variables.
const redeclareSrc = `package u

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Bound redeclares both variables (the capture idiom from before Go 1.22).
func Bound() (n, line int) {
	m := map[string]int{"a": 1, "bb": 2, "ccc": 3}
	var fs []func() int
	for k, v := range m {
		k, v := k, v
		_, _, line, _ = runtime.Caller(0)
		fs = append(fs, func() int { return len(k) * v })
	}
	for _, f := range fs {
		n += f()
	}
	return
}

// Labeled redeclares the key with another value.
func Labeled() (n int) {
	m := map[string]int{"a": 1, "bb": 2, "ccc": 3}
L:
	for k := range m {
		k := k + "x"
		n += len(k)
		continue L
	}
	return
}

// Typed redeclares the key with another type.
func Typed() (n int) {
	m := map[string]int{"1": 1, "2": 2, "3": 3}
	for k, v := range m {
		k, err := strconv.Atoi(k)
		if err != nil {
			panic(err)
		}
		n += k * v
	}
	return
}

// Shadow redeclares the key next to a new name: a closure made before still sees the loop's key.
func Shadow() string {
	m := map[string]int{"a": 1, "b": 2, "c": 3}
	var keys []string
	for k := range m {
		get := func() string { return k }
		k, ok := k+"x", true
		if !ok || k == "" {
			panic(k)
		}
		keys = append(keys, get())
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}
`

// Built through the wrapper, map range bodies that redeclare their loop variables compile and
// keep Go's scoping, and a line inside one keeps its number (XTE-041, XTE-044).
func TestMapRangeRedeclaredVarsBuild(t *testing.T) {
	before, _, _ := strings.Cut(redeclareSrc, "runtime.Caller(0)")
	line := strings.Count(before, "\n") + 1
	dir := userModule(t, "u.go", redeclareSrc, "u_test.go", "package u\n\nimport \"testing\"\n\nfunc TestRedeclare(t *testing.T) {\n"+
		"\tif n, line := Bound(); n != 14 || line != "+strconv.Itoa(line)+" {\n\t\tt.Errorf(\"Bound: %d, line %d\", n, line)\n\t}\n"+
		"\tif n := Labeled(); n != 9 {\n\t\tt.Errorf(\"Labeled: %d\", n)\n\t}\n"+
		"\tif n := Typed(); n != 14 {\n\t\tt.Errorf(\"Typed: %d\", n)\n\t}\n"+
		"\tif s := Shadow(); s != \"a,b,c\" {\n\t\tt.Errorf(\"Shadow: %s\", s)\n\t}\n}\n")
	bin := filepath.Join(t.TempDir(), "u.test")
	build := exec.Command("go", "test", "-c", "-o", bin, "-toolexec="+wrapper, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -toolexec: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin, "-test.count=1", "-test.timeout=1m").CombinedOutput(); err != nil { //nolint:gosec // the binary the test built
		t.Fatalf("the test binary: %v\n%s", err, out)
	}
}

// A select without a default whose closing brace shares a line with its last statement, there
// a bound map range: the default clause the rewrite adds still parses.
func TestSelectBraceAfterStatement(t *testing.T) {
	for i, src := range []string{
		"package p\n\nfunc f(a, b chan int) (x int) {\n\tselect { case <-a: x++; case <-b: x-- }\n\treturn\n}\n",
		"package p\n\nfunc g() map[string]int { return nil }\n\nfunc f(a, b chan int) {\n\tselect {\n\tcase <-a:\n\tcase <-b: for k := range g() { _ = k }}\n}\n",
	} {
		got, gaps := rewriteSource(t, src, "")
		if _, err := parser.ParseFile(token.NewFileSet(), "a.go", got, 0); err != nil || len(gaps) != 0 {
			t.Errorf("case %d: %v, gaps %q:\n%s", i, err, gaps, got)
		}
	}
}

// The map, maps, rand and reflect rows on fields promoted through an unexported embedded field
// of another package keep the receiver as written, so they name nothing the file cannot:
// sync.Map.Range and reflect.Value.MapKeys record their own kinds once, and no site records
// "method receiver not nameable in this file", which belongs to the rows that spell the path.
func TestMapRandRowsUnexportedEmbedded(t *testing.T) {
	dep := "package q\n\nimport (\n\t\"reflect\"\n\t\"sync\"\n)\n\ntype inner struct {\n\tM map[string]int\n\tsync.Map\n\treflect.Value\n}\n\n" +
		"type S struct{ inner }\n"
	src := "package p\n\nimport (\n\t\"maps\"\n\t\"math/rand\"\n\n\t\"example.com/q\"\n)\n\nfunc F(s *q.S) int {\n\tn := 0\n" +
		"\tfor k, v := range s.M {\n\t\tn += len(k) + v\n\t}\n\tfor k := range maps.Keys(s.M) {\n\t\tn += len(k)\n\t}\n" +
		"\ts.Range(func(k, v any) bool { return true })\n\t_ = s.MapKeys()\n\t_, _ = rand.Read(make([]byte, 2))\n\treturn n + rand.Intn(6)\n}\n"
	want := header + "\nimport (\n\t\"maps\"\n\t\"math/rand\"\n\n\t\"example.com/q\"\n)\n\nfunc F(s *q.S) int {\n\tn := 0\n" +
		"\t{ __fl_m1 := s.M; for _, __fl_k2 := range __fl_sched.Keys(__fl_m1) { v, __fl_ok3 := __fl_m1[__fl_k2]; if !__fl_ok3 { continue }; k := __fl_k2; {\n" +
		"\t\tn += len(k) + v\n\t}}}\n\tfor k := range __fl_sched.MapKeys(s.M) {\n\t\tn += len(k)\n\t}\n" +
		"\ts.Range(func(k, v any) bool { return true })\n\t_ = s.MapKeys()\n\t_, _ = __fl_sched.RandV1().Read(make([]byte, 2))\n" +
		"\treturn n + __fl_sched.RandV1().Intn(6)\n}\n\nvar _ = maps.Copy[map[int]int, map[int]int]\n\nvar _ rand.Source\n\n" +
		"var _ = __fl_sched.Gaps(\"/src/p/a.go:18: sync.Map.Range\\n/src/p/a.go:19: reflect map iteration\")\n"
	wantGaps := []string{"/src/p/a.go:18: sync.Map.Range", "/src/p/a.go:19: reflect map iteration"}
	if got, gaps := rewriteSource(t, src, dep); got != want || !slices.Equal(gaps, wantGaps) {
		t.Errorf("got\n%s\ngaps %q\nwant\n%s\ngaps %q", got, gaps, want, wantGaps)
	}
}
