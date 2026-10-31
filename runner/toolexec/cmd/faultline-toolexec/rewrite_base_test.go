// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// AT-XTE-07: a cgo package compiles from sources outside its module; it is not rewritten and
// registers one gap.
func TestATXTE07_SourcesOutsideAModule(t *testing.T) {
	keep, log := t.TempDir(), filepath.Join(t.TempDir(), "wrapper.log")
	dir := userModule(t,
		"u.go", "package u\n\n// int one(void) { return 1; }\nimport \"C\"\n\nfunc One() int { return int(C.one()) }\n",
		"u_ext_test.go", "package u_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/u\"\n)\n\nfunc TestOne(t *testing.T) {\n\tif u.One() != 1 {\n\t\tt.Fatal(\"one\")\n\t}\n}\n")
	bin := filepath.Join(t.TempDir(), "u.test")
	build := exec.Command("go", "test", "-c", "-o", bin, "-toolexec="+wrapper, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=1", "FAULTLINE_TOOLEXEC_KEEP="+keep, "FAULTLINE_TOOLEXEC_LOG="+log)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -toolexec: %v\n%s", err, out)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "compile example.com/u outside-module\n") {
		t.Errorf("log lacks the outside-module line:\n%s", b)
	}
	gen, err := os.ReadFile(filepath.Join(keep, "example.com_u", "__fl_gaps.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(gen), "package u\n") || !strings.Contains(string(gen), ":1: sources outside a module\")\n") {
		t.Errorf("generated gap file:\n%s", gen)
	}
	if m := regexp.MustCompile(`Gaps\((".*")\)`).FindSubmatch(gen); m == nil {
		t.Errorf("generated gap file registers no gap:\n%s", gen)
	} else if g, _ := strconv.Unquote(string(m[1])); !filepath.IsAbs(strings.TrimSuffix(g, ":1: sources outside a module")) {
		t.Errorf("gap %q does not name an absolute file", g)
	} else if b, err := os.ReadFile(bin); err != nil || !strings.Contains(string(b), g) {
		t.Errorf("the test binary does not register the gap %q (%v)", g, err)
	}
	if out, err := exec.Command(bin, "-test.count=1", "-test.timeout=1m").CombinedOutput(); err != nil { //nolint:gosec // the binary the test built
		t.Fatalf("the test binary: %v\n%s", err, out)
	}
}

// positionsSrc has a multi-line go call, a multi-line select and a multi-line Lock call, each
// followed by a runtime.Caller line.
const positionsSrc = `package u

import (
	"fmt"
	"runtime"
	"sync"
)

func line() int {
	_, _, l, _ := runtime.Caller(1)
	return l
}

// Lines returns the lines runtime.Caller reports after each multi-line rewrite.
func Lines() string {
	var mu sync.Mutex
	done := make(chan int, 1)
	go func(a,
		b int) {
		done <- a + b
	}(1,
		2)
	a := line()
	select {
	case v := <-done:
		_ = v
	case <-make(chan int):
	}
	b := line()
	mu.
		Lock()
	c := line()
	mu.Unlock()
	return fmt.Sprint(a, " ", b, " ", c)
}
`

// AT-XTE-08
func TestATXTE08_PositionsSurviveRewrites(t *testing.T) {
	dir := userModule(t, "u.go", positionsSrc,
		"u_test.go", "package u\n\nimport \"testing\"\n\nfunc TestLines(t *testing.T) { t.Log(\"LINES \" + Lines()) }\n")
	bin := filepath.Join(t.TempDir(), "u.test")
	build := exec.Command("go", "test", "-c", "-o", bin, "-toolexec="+wrapper, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -toolexec: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "-test.v", "-test.count=1", "-test.timeout=1m").CombinedOutput() //nolint:gosec // the binary the test built
	if err != nil {
		t.Fatalf("the test binary: %v\n%s", err, out)
	}
	var want []string
	for i, l := range strings.Split(positionsSrc, "\n") {
		if strings.Contains(l, " := line()") {
			want = append(want, strconv.Itoa(i+1))
		}
	}
	m := regexp.MustCompile(`LINES (\d+) (\d+) (\d+)`).FindStringSubmatch(string(out))
	if m == nil || !slices.Equal(m[1:], want) {
		t.Fatalf("runtime.Caller lines %v, want %v\n%s", m, want, out)
	}
}

// AT-XTE-09: rewriting is a function of the input; gaps come out sorted by line and are
// registered on the file's last line. The walk records the three gaps in reverse line order.
func TestATXTE09_DeterministicOutput(t *testing.T) {
	src := "package p\n\nfunc f(ch chan int, n map[*int]bool, x bool) {\n\tif x {\n\t\tfor k := range n {\n\t\t\t_ = k\n\t\t}\n" +
		"\t} else if <-ch > 0 {\n\t\tch <- 1\n\t}\n\tfor k := range n {\n\t\t_ = k\n\t}\n}\n"
	a, gapsA := rewriteSource(t, src, "")
	b, gapsB := rewriteSource(t, src, "")
	if a != b || !slices.Equal(gapsA, gapsB) {
		t.Fatalf("two rewrites differ:\n%s\n%s", a, b)
	}
	if len(gapsA) != 3 || !strings.HasPrefix(gapsA[0], "/src/p/a.go:5: ") || !strings.HasPrefix(gapsA[1], "/src/p/a.go:8: ") ||
		!strings.HasPrefix(gapsA[2], "/src/p/a.go:11: ") {
		t.Fatalf("gaps %q, want the map gap of line 5, the else-if gap of line 8, then the map gap of line 11", gapsA)
	}
	if !strings.Contains(a, "\t\t__fl_sched.Point(); ch <- 1\n") {
		t.Fatalf("the send of line 9 has no point:\n%s", a)
	}
	lines := strings.Split(strings.TrimSuffix(a, "\n"), "\n")
	if want := "var _ = __fl_sched.Gaps(" + strconv.Quote(strings.Join(gapsA, "\n")) + ")"; lines[len(lines)-1] != want {
		t.Fatalf("last line %q, want %q", lines[len(lines)-1], want)
	}
}

const header = "//line /src/p/a.go:1\npackage p; import __fl_sched \"github.com/hmdsefi/faultline/runner/toolexec/sched\"\n"

// AT-XTE-10 (with TestRewriteGolden): the rows XTE-041 adds to EXB-034's points and go statements.
func TestATXTE10_BaseRows(t *testing.T) {
	for i, c := range []struct {
		src, want string
		gaps      []string
	}{
		{ // a goto skips the point before its label, so it gets one itself
			src:  "package p\n\nfunc f(ch chan int) {\nL:\n\tfor v := range ch {\n\t\tif v > 0 {\n\t\t\tgoto L\n\t\t}\n\t}\n}\n",
			want: header + "\nfunc f(ch chan int) {\n__fl_sched.Point(); L:\n\tfor v := range ch { __fl_sched.Point();\n\t\tif v > 0 {\n\t\t\t__fl_sched.Point(); goto L\n\t\t}\n\t}\n}\n",
		},
		{ // a receive in a case expression
			src:  "package p\n\nfunc f(ch chan int, x int) int {\n\tswitch x {\n\tcase <-ch:\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
			want: header + "\nfunc f(ch chan int, x int) int {\n\t__fl_sched.Point(); switch x {\n\tcase <-ch:\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		},
		{ // deferred close and Wait get their point when they run; an operand's receive one now
			src: "package p\n\nimport \"sync\"\n\nfunc f(ch chan int, wg *sync.WaitGroup, in chan int) {\n\tvar w sync.WaitGroup\n\tdefer close(ch)\n\tdefer wg.Wait()\n\tdefer w.Wait()\n\tdefer println(<-in)\n}\n",
			want: header + "\nimport \"sync\"\n\nfunc f(ch chan int, wg *sync.WaitGroup, in chan int) {\n\tvar w sync.WaitGroup\n" +
				"\t{ __fl_d1 := ch; defer func() { __fl_sched.Point(); close(__fl_d1) }() }\n" +
				"\t{ __fl_d2 := (wg); defer func() { __fl_sched.Point(); __fl_d2.Wait() }() }\n" +
				"\t{ __fl_d3 := &(w); defer func() { __fl_sched.Point(); __fl_d3.Wait() }() }\n" +
				"\t__fl_sched.Point(); defer println(<-in)\n}\n",
		},
		{ // a defer evaluates its function value now, so a receive in it gets a point
			src:  "package p\n\nfunc f(fns chan func()) {\n\tdefer (<-fns)()\n}\n",
			want: header + "\nfunc f(fns chan func()) {\n\t__fl_sched.Point(); defer (<-fns)()\n}\n",
		},
		{
			src:  "package p\n\nfunc f(fs []func(), i chan int) {\n\tdefer fs[<-i]()\n}\n",
			want: header + "\nfunc f(fs []func(), i chan int) {\n\t__fl_sched.Point(); defer fs[<-i]()\n}\n",
		},
		{ // and so does a receive in a deferred Wait's receiver
			src:  "package p\n\nimport \"sync\"\n\nfunc f(ws chan *sync.WaitGroup) {\n\tdefer (<-ws).Wait()\n}\n",
			want: header + "\nimport \"sync\"\n\nfunc f(ws chan *sync.WaitGroup) {\n\t__fl_sched.Point(); { __fl_d1 := ((<-ws)); defer func() { __fl_sched.Point(); __fl_d1.Wait() }() }\n}\n",
		},
		{ // go of builtins: the builtin is called by name
			src:  "package p\n\nfunc f(ch chan int, n int) {\n\tgo close(ch)\n\tgo println(n, \"x\")\n}\n",
			want: header + "\nfunc f(ch chan int, n int) {\n\t__fl_sched.Point(); { __fl_g1 := ch; __fl_sched.Go(func() { close(__fl_g1) }) }\n\t{ __fl_g2 := n; __fl_sched.Go(func() { println(__fl_g2, \"x\") }) }\n}\n",
		},
		{ // go statements in a generic function
			src:  "package p\n\nfunc g[T any](v T) {}\n\nfunc f[T any](v T) {\n\tgo g(v)\n\tgo func() {}()\n}\n",
			want: header + "\nfunc g[T any](v T) {}\n\nfunc f[T any](v T) {\n\t{ __fl_g1 := v; __fl_sched.Go(func() { g(__fl_g1) }) }\n\t{ __fl_g2 := func() {}; __fl_sched.Go(func() { __fl_g2() }) }\n}\n",
		},
		{ // a range over a type parameter whose terms are all channels
			src:  "package p\n\nfunc h[C ~chan int](c C) {\n\tfor v := range c {\n\t\t_ = v\n\t}\n}\n",
			want: header + "\nfunc h[C ~chan int](c C) {\n\t__fl_sched.Point(); for v := range c { __fl_sched.Point();\n\t\t_ = v\n\t}\n}\n",
		},
		{ // a range over a type parameter with a map term stays a gap
			src:  "package p\n\nfunc h[M ~map[string]int](m M) (n int) {\n\tfor k := range m {\n\t\tn += len(k)\n\t}\n\treturn\n}\n",
			want: header + "\nfunc h[M ~map[string]int](m M) (n int) {\n\tfor k := range m {\n\t\tn += len(k)\n\t}\n\treturn\n}\n",
			gaps: []string{"/src/p/a.go:4: range over a channel or map of type-parameter type"},
		},
		{ // a file whose only edit is a deferred close imports sched
			src:  "package p\n\nfunc f(ch chan int) {\n\tdefer close(ch)\n}\n",
			want: header + "\nfunc f(ch chan int) {\n\t{ __fl_d1 := ch; defer func() { __fl_sched.Point(); close(__fl_d1) }() }\n}\n",
		},
		{ // a multi-line go call: the directive names the line and column after the call
			src:  "package p\n\nfunc g(a, b int) {}\n\nfunc f(a int) {\n\tgo g(a,\n\t\ta); _ = a\n}\n",
			want: header + "\nfunc g(a, b int) {}\n\nfunc f(a int) {\n\t{ __fl_g1, __fl_g2, __fl_g3 := g, a, a; __fl_sched.Go(func() { __fl_g1(__fl_g2, __fl_g3) }) }/*line :7:5*/; _ = a\n}\n",
		},
		{ // every label of a pointed statement gets its gotos a point; a goto to another label does not
			src: "package p\n\nfunc f(ch chan int, x int) {\nL1:\nL2:\n\tfor v := range ch {\n\t\tswitch v {\n\t\tcase 1:\n\t\t\tgoto L1\n\t\tcase 2:\n\t\t\tgoto L2\n\t\t}\n\t\tgoto L3\n\t}\nL3:\n\t_ = x\n}\n",
			want: header + "\nfunc f(ch chan int, x int) {\n__fl_sched.Point(); L1:\nL2:\n\tfor v := range ch { __fl_sched.Point();\n\t\tswitch v {\n\t\tcase 1:\n" +
				"\t\t\t__fl_sched.Point(); goto L1\n\t\tcase 2:\n\t\t\t__fl_sched.Point(); goto L2\n\t\t}\n\t\tgoto L3\n\t}\nL3:\n\t_ = x\n}\n",
		},
		{ // gaps on one line: sorted by kind, a repeated gap once
			src:  "package p\n\nfunc h[M ~map[string]int](m M, ch chan int, x bool) {\n\tif x {} else if <-ch > 0 {}; for range m {}; for range m {}\n}\n",
			want: header + "\nfunc h[M ~map[string]int](m M, ch chan int, x bool) {\n\tif x {} else if <-ch > 0 {}; for range m {}; for range m {}\n}\n",
			gaps: []string{"/src/p/a.go:4: channel operation in else-if header", "/src/p/a.go:4: range over a channel or map of type-parameter type"},
		},
		{ // the registration comes after the line that keeps math/rand imported
			src: "package p\n\nimport \"math/rand\"\n\nfunc f(ch chan int, x bool) int {\n\tif x {\n\t} else if <-ch > 0 {\n\t}\n\treturn rand.Intn(6)\n}\n",
			want: header + "\nimport \"math/rand\"\n\nfunc f(ch chan int, x bool) int {\n\tif x {\n\t} else if <-ch > 0 {\n\t}\n\treturn __fl_sched.RandV1().Intn(6)\n}\n" +
				"\nvar _ rand.Source\n",
			gaps: []string{"/src/p/a.go:7: channel operation in else-if header"},
		},
		{ // a select header that copies a multi-line send value: the directive after it keeps the case lines
			src: "package p\n\ntype ev struct{ a, b int }\n\nfunc f(done chan int, out chan ev, a, b int) {\n\tselect {\n\tcase <-done:\n\t\tpanic(a)\n\tcase out <- ev{\n\t\ta: a,\n\t\tb: b,\n\t}:\n\t}\n}\n",
			want: header[:len(header)-1] + "; import __fl_reflect \"reflect\"\n\ntype ev struct{ a, b int }\n\nfunc f(done chan int, out chan ev, a, b int) {\n" +
				"\t__fl_sched.Point(); { __fl_c1, __fl_v2, __fl_ok3 := __fl_sched.Select([]__fl_reflect.SelectCase{{Dir: __fl_reflect.SelectRecv, Chan: __fl_reflect.ValueOf(done)}, " +
				"{Dir: __fl_reflect.SelectSend, Chan: __fl_reflect.ValueOf(out), Send: __fl_reflect.ValueOf(&[]ev{ev{\n\t\ta: a,\n\t\tb: b,\n\t}}[0]).Elem()}}, false); _, _ = __fl_v2, __fl_ok3; switch __fl_c1 {/*line :6:10*/\n" +
				"\tcase 0:\n\t\tpanic(a)\n\tcase 1:/*line :12:4*/\n\tdefault: panic(\"faultline-toolexec: sched.Select returned no case\") }}\n}\n",
		},
		{ // a map range head that repeats a multi-line map expression: the directive keeps the later lines
			src:  "package p\n\ntype S struct{ m map[string]int }\n\nfunc f(s S) (n int) {\n\tfor k := range s.\n\t\tm {\n\t\tn += len(k)\n\t}\n\tpanic(n)\n}\n",
			want: header + "\ntype S struct{ m map[string]int }\n\nfunc f(s S) (n int) {\n\tfor _, __fl_k1 := range __fl_sched.Keys(s.\n\t\tm) { if _, __fl_ok2 := s.\n\t\tm[__fl_k1]; !__fl_ok2 { continue }; k := __fl_k1;/*line :7:6*/\n\t\tn += len(k)\n\t}\n\tpanic(n)\n}\n",
		},
		{ // break and continue to a pointed label get no point
			src:  "package p\n\nfunc f(ch chan int) {\nL:\n\tfor v := range ch {\n\t\tif v > 0 {\n\t\t\tcontinue L\n\t\t}\n\t\tbreak L\n\t}\n}\n",
			want: header + "\nfunc f(ch chan int) {\n__fl_sched.Point(); L:\n\tfor v := range ch { __fl_sched.Point();\n\t\tif v > 0 {\n\t\t\tcontinue L\n\t\t}\n\t\tbreak L\n\t}\n}\n",
		},
		{ // a receive in a later case clause, as a later expression
			src:  "package p\n\nfunc f(ch chan int, x int) int {\n\tswitch x {\n\tcase 1:\n\t\treturn 1\n\tcase 2, <-ch:\n\t\treturn 2\n\t}\n\treturn 0\n}\n",
			want: header + "\nfunc f(ch chan int, x int) int {\n\t__fl_sched.Point(); switch x {\n\tcase 1:\n\t\treturn 1\n\tcase 2, <-ch:\n\t\treturn 2\n\t}\n\treturn 0\n}\n",
		},
		{ // a receive in a later deferred argument
			src:  "package p\n\nfunc f(in chan int, x int) {\n\tdefer println(x, <-in)\n}\n",
			want: header + "\nfunc f(in chan int, x int) {\n\t__fl_sched.Point(); defer println(x, <-in)\n}\n",
		},
		{ // a receive in an else-if init only records the gap
			src:  "package p\n\nfunc f(ch chan int, x bool) {\n\tif x {\n\t} else if v := <-ch; v > 0 {\n\t}\n}\n",
			want: header + "\nfunc f(ch chan int, x bool) {\n\tif x {\n\t} else if v := <-ch; v > 0 {\n\t}\n}\n",
			gaps: []string{"/src/p/a.go:5: channel operation in else-if header"},
		},
		{ // a range over a received channel gets one point before it
			src: "package p\n\nfunc f(cs chan chan int) {\n\tfor v := range <-cs {\n\t\t_ = v\n\t}\n}\n\nfunc h[C ~chan int](cs chan C) {\n\tfor v := range <-cs {\n\t\t_ = v\n\t}\n}\n",
			want: header + "\nfunc f(cs chan chan int) {\n\t__fl_sched.Point(); for v := range <-cs { __fl_sched.Point();\n\t\t_ = v\n\t}\n}\n\n" +
				"func h[C ~chan int](cs chan C) {\n\t__fl_sched.Point(); for v := range <-cs { __fl_sched.Point();\n\t\t_ = v\n\t}\n}\n",
		},
	} {
		got, gaps := rewriteSource(t, c.src, "")
		want := c.want
		if len(c.gaps) > 0 {
			want += "\nvar _ = __fl_sched.Gaps(" + strconv.Quote(strings.Join(c.gaps, "\n")) + ")\n"
		}
		if got != want || !slices.Equal(gaps, c.gaps) {
			t.Errorf("case %d: got\n%s\ngaps %q\nwant\n%s\ngaps %q", i, got, gaps, want, c.gaps)
		}
	}
}
