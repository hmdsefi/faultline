// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// AT-XTE-11: the sync rows of XTE-041.
func TestATXTE11_SyncRows(t *testing.T) {
	for i, c := range []struct{ src, want string }{
		{ // method values of sync methods
			src: "package p\n\nimport \"sync\"\n\ntype S struct {\n\tmu sync.Mutex\n\twg sync.WaitGroup\n\tc  *sync.Cond\n}\n\n" +
				"func f(s *S, rw *sync.RWMutex, o *sync.Once) []any {\n\treturn []any{s.mu.Lock, s.mu.Unlock, rw.RLock, rw.Lock, o.Do, s.wg.Wait, s.wg.Go, s.c.Signal}\n}\n",
			want: header + "\nimport \"sync\"\n\ntype S struct {\n\tmu sync.Mutex\n\twg sync.WaitGroup\n\tc  *sync.Cond\n}\n\n" +
				"func f(s *S, rw *sync.RWMutex, o *sync.Once) []any {\n\treturn []any{__fl_sched.LockFunc(&(s.mu)), __fl_sched.UnlockFunc(&(s.mu)), " +
				"__fl_sched.RLockFunc((rw)), __fl_sched.WLockFunc((rw)), __fl_sched.OnceDoFunc((o)), __fl_sched.WaitFunc(&(s.wg)), " +
				"__fl_sched.GoFunc(&(s.wg)), __fl_sched.CondSignalFunc((s.c))}\n}\n",
		},
		{ // go of a sync method binds the rewritten method value
			src:  "package p\n\nimport \"sync\"\n\nfunc f(mu *sync.Mutex, o *sync.Once, fn func()) {\n\tgo mu.Lock()\n\tgo o.Do(fn)\n}\n",
			want: header + "\nimport \"sync\"\n\nfunc f(mu *sync.Mutex, o *sync.Once, fn func()) {\n\t{ __fl_g1 := __fl_sched.LockFunc((mu)); __fl_sched.Go(func() { __fl_g1() }) }\n\t{ __fl_g2, __fl_g3 := __fl_sched.OnceDoFunc((o)), fn; __fl_sched.Go(func() { __fl_g2(__fl_g3) }) }\n}\n",
		},
		{ // Lock and Unlock through interface values, promoted ones too, and RLocker
			src: "package p\n\nimport \"sync\"\n\ntype locker interface {\n\tLock()\n\tUnlock()\n\tName() string\n}\n\ntype S struct{ sync.Locker }\n\n" +
				"func f(l sync.Locker, m locker, s S, rw *sync.RWMutex) {\n\tl.Lock()\n\tl.Unlock()\n\tm.Lock()\n\tdefer m.Unlock()\n\ts.Lock()\n\tr := rw.RLocker()\n\tr.Lock()\n\tr.Unlock()\n}\n",
			want: header + "\nimport \"sync\"\n\ntype locker interface {\n\tLock()\n\tUnlock()\n\tName() string\n}\n\ntype S struct{ sync.Locker }\n\n" +
				"func f(l sync.Locker, m locker, s S, rw *sync.RWMutex) {\n\t__fl_sched.ILock((l))\n\t__fl_sched.IUnlock((l))\n\t__fl_sched.ILock((m))\n\tdefer __fl_sched.IUnlock((m))\n" +
				"\t__fl_sched.ILock((s).Locker)\n\tr := __fl_sched.RLocker((rw))\n\t__fl_sched.ILock((r))\n\t__fl_sched.IUnlock((r))\n}\n",
		},
		{ // Cond, WaitGroup.Go, the Once functions, AfterFunc and Gosched
			src: "package p\n\nimport (\n\t\"context\"\n\t\"runtime\"\n\t\"sync\"\n\t\"time\"\n)\n\n" +
				"func f(ctx context.Context, c *sync.Cond, wg *sync.WaitGroup, mu *sync.Mutex) {\n\tc.Wait()\n\tc.Signal()\n\tc.Broadcast()\n\twg.Go(func() {})\n" +
				"\tg := sync.OnceFunc(func() {})\n\tv := sync.OnceValue(func() int { return 1 })\n\tvs := sync.OnceValues[int, error](func() (int, error) { return 1, nil })\n\t_, _, _ = g, v, vs\n" +
				"\ttime.AfterFunc(time.Second, mu.Unlock)\n\tcontext.AfterFunc(ctx, func() {})\n\truntime.Gosched()\n\tyield := runtime.Gosched\n\tyield()\n}\n",
			want: header + "\nimport (\n\t\"context\"\n\t\"runtime\"\n\t\"sync\"\n\t\"time\"\n)\n\n" +
				"func f(ctx context.Context, c *sync.Cond, wg *sync.WaitGroup, mu *sync.Mutex) {\n\t__fl_sched.CondWait((c))\n\t__fl_sched.CondSignal((c))\n\t__fl_sched.CondBroadcast((c))\n\t__fl_sched.WaitGroupGo((wg), func() {})\n" +
				"\tg := __fl_sched.OnceFunc(func() {})\n\tv := __fl_sched.OnceValue(func() int { return 1 })\n\tvs := __fl_sched.OnceValues[int, error](func() (int, error) { return 1, nil })\n\t_, _, _ = g, v, vs\n" +
				"\ttime.AfterFunc(time.Second, __fl_sched.Func(__fl_sched.UnlockFunc((mu))))\n\tcontext.AfterFunc(ctx, __fl_sched.Func(func() {}))\n\t__fl_sched.Point()\n\tyield := __fl_sched.Point\n\tyield()\n}\n" +
				"\nvar _ = runtime.Gosched\n\nvar _ sync.Mutex\n",
		},
	} {
		got, gaps := rewriteSource(t, c.src, "")
		if got != c.want || !slices.Equal(gaps, nil) {
			t.Errorf("case %d: got\n%s\ngaps %q\nwant\n%s", i, got, gaps, c.want)
		}
	}
}

// The XTE-042 gaps next to the sync rows: a called method expression, go of an interface Lock, an
// interface Unlock method value and a method expression of RLocker; and a rewritten Lock whose
// receiver starts the statement that got a point, so the point stays out of the receiver.
func TestSyncRowsGaps(t *testing.T) {
	src := "package p\n\nimport \"sync\"\n\ntype S struct{ mu sync.Mutex }\n\n" +
		"func f(ch chan *S, mu *sync.Mutex, l sync.Locker) any {\n\t(<-ch).mu.Lock()\n\t(*sync.Mutex).Lock(mu)\n\tgo l.Lock()\n\treturn l.Unlock\n}\n" +
		"\nvar rl = (*sync.RWMutex).RLocker\n"
	wantGaps := []string{"/src/p/a.go:9: method expression Mutex.Lock", "/src/p/a.go:10: method value of an interface Lock or Unlock",
		"/src/p/a.go:11: method value of an interface Lock or Unlock", "/src/p/a.go:14: method expression RWMutex.RLocker"}
	want := header + "\nimport \"sync\"\n\ntype S struct{ mu sync.Mutex }\n\n" +
		"func f(ch chan *S, mu *sync.Mutex, l sync.Locker) any {\n\t__fl_sched.Point(); __fl_sched.Lock(&((<-ch).mu))\n\t(*sync.Mutex).Lock(mu)\n" +
		"\t{ __fl_g1 := l.Lock; __fl_sched.Go(func() { __fl_g1() }) }\n\treturn l.Unlock\n}\n\nvar rl = (*sync.RWMutex).RLocker\n" +
		"\nvar _ = __fl_sched.Gaps(" + strconv.Quote(strings.Join(wantGaps, "\n")) + ")\n"
	if got, gaps := rewriteSource(t, src, ""); got != want || !slices.Equal(gaps, wantGaps) {
		t.Errorf("got\n%s\ngaps %q\nwant\n%s\ngaps %q", got, gaps, want, wantGaps)
	}
}

// The sync rows AT-XTE-11 leaves unpinned: the method values of RUnlock, Unlock on an RWMutex, Wait
// and Broadcast; Lock calls and method values through interfaces whose Lock takes an argument or
// returns a result, which stay as written; an interface Lock reached through an embedded pointer;
// a file whose only rewrite is one of Gosched, OnceFunc, an interface Lock or AfterFunc, which
// still imports sched; AfterFunc under go, which wraps the function it binds; and an AfterFunc of
// another package, which stays as written.
func TestSyncRowsPinned(t *testing.T) {
	for i, c := range []struct{ src, dep, want string }{
		{
			src: "package p\n\nimport (\n\t\"context\"\n\t\"sync\"\n)\n\ntype T struct{ sync.Locker }\n\ntype S struct{ *T }\n\n" +
				"type errLocker interface{ Lock() error }\n\ntype argLocker interface{ Lock(context.Context) }\n\n" +
				"func f(ctx context.Context, rw *sync.RWMutex, c *sync.Cond, p *S, e errLocker, a argLocker) []any {\n" +
				"\tp.Lock()\n\t_ = e.Lock()\n\ta.Lock(ctx)\n\treturn []any{rw.RUnlock, rw.Unlock, c.Wait, c.Broadcast, e.Lock, a.Lock}\n}\n",
			want: header + "\nimport (\n\t\"context\"\n\t\"sync\"\n)\n\ntype T struct{ sync.Locker }\n\ntype S struct{ *T }\n\n" +
				"type errLocker interface{ Lock() error }\n\ntype argLocker interface{ Lock(context.Context) }\n\n" +
				"func f(ctx context.Context, rw *sync.RWMutex, c *sync.Cond, p *S, e errLocker, a argLocker) []any {\n" +
				"\t__fl_sched.ILock((p).T.Locker)\n\t_ = e.Lock()\n\ta.Lock(ctx)\n\treturn []any{__fl_sched.RUnlockFunc((rw)), " +
				"__fl_sched.WUnlockFunc((rw)), __fl_sched.CondWaitFunc((c)), __fl_sched.CondBroadcastFunc((c)), e.Lock, a.Lock}\n}\n",
		},
		{
			src:  "package p\n\nimport \"runtime\"\n\nfunc f() {\n\truntime.Gosched()\n}\n",
			want: header + "\nimport \"runtime\"\n\nfunc f() {\n\t__fl_sched.Point()\n}\n\nvar _ = runtime.Gosched\n",
		},
		{
			src:  "package p\n\nimport \"sync\"\n\nvar g = sync.OnceFunc(func() {})\n",
			want: header + "\nimport \"sync\"\n\nvar g = __fl_sched.OnceFunc(func() {})\n\nvar _ sync.Mutex\n",
		},
		{
			src:  "package p\n\nimport \"sync\"\n\nfunc f(l sync.Locker) {\n\tl.Lock()\n}\n",
			want: header + "\nimport \"sync\"\n\nfunc f(l sync.Locker) {\n\t__fl_sched.ILock((l))\n}\n",
		},
		{
			src:  "package p\n\nimport \"time\"\n\nfunc f(g func()) {\n\ttime.AfterFunc(time.Second, g)\n}\n",
			want: header + "\nimport \"time\"\n\nfunc f(g func()) {\n\ttime.AfterFunc(time.Second, __fl_sched.Func(g))\n}\n",
		},
		{
			src: "package p\n\nimport (\n\t\"context\"\n\t\"sync\"\n\t\"time\"\n)\n\nfunc f(ctx context.Context, g func(), mu *sync.Mutex) {\n" +
				"\tgo time.AfterFunc(time.Second, g)\n\tgo context.AfterFunc(ctx, func() { g() })\n\tgo time.AfterFunc(time.Second, mu.Unlock)\n}\n",
			want: header + "\nimport (\n\t\"context\"\n\t\"sync\"\n\t\"time\"\n)\n\nfunc f(ctx context.Context, g func(), mu *sync.Mutex) {\n" +
				"\t{ __fl_g1, __fl_g2 := time.AfterFunc, __fl_sched.Func(g); __fl_sched.Go(func() { __fl_g1(time.Second, __fl_g2) }) }\n" +
				"\t{ __fl_g3, __fl_g4, __fl_g5 := context.AfterFunc, ctx, __fl_sched.Func(func() { g() }); __fl_sched.Go(func() { __fl_g3(__fl_g4, __fl_g5) }) }\n" +
				"\t{ __fl_g6, __fl_g7 := time.AfterFunc, __fl_sched.Func(__fl_sched.UnlockFunc((mu))); __fl_sched.Go(func() { __fl_g6(time.Second, __fl_g7) }) }\n}\n",
		},
		{ // an AfterFunc of another package is not wrapped, called or under go
			src: "package p\n\nimport \"example.com/q\"\n\nfunc f(s string) {\n\tq.AfterFunc(1, s)\n\tgo q.AfterFunc(1, s)\n}\n",
			dep: "package q\n\nfunc AfterFunc(n int, s string) {}\n",
			want: header + "\nimport \"example.com/q\"\n\nfunc f(s string) {\n\tq.AfterFunc(1, s)\n" +
				"\t{ __fl_g1, __fl_g2 := q.AfterFunc, s; __fl_sched.Go(func() { __fl_g1(1, __fl_g2) }) }\n}\n",
		},
		{ // Lock and Unlock through interfaces that are not a sync.Locker, and a type parameter
			src:  "package p\n\n" + narrowLockers + "\n" + narrowLockersFunc,
			want: header + "\n" + narrowLockers + "\n" + strings.NewReplacer("l.Lock()", "__fl_sched.ILock((l))", "c.Unlock()", "__fl_sched.IUnlock((c))", "e.Unlock()", "__fl_sched.IUnlock((e))", "t.Lock()", "__fl_sched.ILock((t))").Replace(narrowLockersFunc),
		},
	} {
		got, gaps := rewriteSource(t, c.src, c.dep)
		if got != c.want || !slices.Equal(gaps, nil) {
			t.Errorf("case %d: got\n%s\ngaps %q\nwant\n%s", i, got, gaps, c.want)
		}
	}
}

// narrowLockers declares interfaces with a Lock or an Unlock that are not a sync.Locker: Lock
// only, Unlock next to another method, and Unlock next to a Lock with a result.
const narrowLockers = "type onlyLock interface{ Lock() }\n\ntype unlockCloser interface {\n\tUnlock()\n\tClose() error\n}\n\n" +
	"type errLock interface {\n\tLock() error\n\tUnlock()\n}\n"

// narrowLockersFunc locks and unlocks through narrowLockers and a type parameter.
const narrowLockersFunc = "func F[T onlyLock](l onlyLock, c unlockCloser, e errLock, t T) {\n\tl.Lock()\n\tc.Unlock()\n\te.Unlock()\n\tt.Lock()\n}\n"

// receiverDep is package example.com/q, whose types promote Lock, Unlock and Wait through
// unexported embedded fields; E promotes them through an exported one.
const receiverDep = "package q\n\nimport \"sync\"\n\ntype locker interface {\n\tLock()\n\tUnlock()\n}\n\n" +
	"type inner struct{ sync.Mutex }\n\ntype group struct{ sync.WaitGroup }\n\n" +
	"type I struct{ locker }\n\ntype M struct{ inner }\n\ntype W struct{ group }\n\ntype E struct{ sync.Mutex }\n"

// receiverUse calls the sync rows on receiverDep's types.
const receiverUse = "func G(i q.I, m *q.M, w *q.W) func() {\n\ti.Lock()\n\tm.Lock()\n\tgo i.Unlock()\n\tgo m.Unlock()\n\tgo (i.Lock)()\n" +
	"\tdefer w.Wait()\n\t_ = i.Unlock\n\treturn m.Lock\n}\n"

// AT-XTE-13, the row of XTE-042 for receivers the file cannot name: a sync method or an interface
// Lock or Unlock reached through an unexported embedded field of another package is left as
// written (go binds the method value as written) and records exactly its kind once per site;
// an exported field of another package and an unexported field of the file's own package are
// rewritten.
func TestATXTE13_MethodReceiverNotNameable(t *testing.T) {
	src := "package p\n\nimport (\n\t\"sync\"\n\n\t\"example.com/q\"\n)\n\ntype own struct{ sync.Mutex }\n\ntype T struct{ own }\n\n" +
		receiverUse + "\nfunc H(e *q.E, t *T) {\n\te.Lock()\n\tt.Lock()\n}\n"
	const kind = "method receiver not nameable in this file"
	var wantGaps []string
	for _, l := range []int{14, 15, 16, 17, 18, 19, 20, 21} {
		wantGaps = append(wantGaps, "/src/p/a.go:"+strconv.Itoa(l)+": "+kind)
	}
	want := header + "\nimport (\n\t\"sync\"\n\n\t\"example.com/q\"\n)\n\ntype own struct{ sync.Mutex }\n\ntype T struct{ own }\n\n" +
		"func G(i q.I, m *q.M, w *q.W) func() {\n\ti.Lock()\n\tm.Lock()\n\t{ __fl_g1 := i.Unlock; __fl_sched.Go(func() { __fl_g1() }) }\n" +
		"\t{ __fl_g2 := m.Unlock; __fl_sched.Go(func() { __fl_g2() }) }\n\t{ __fl_g3 := (i.Lock); __fl_sched.Go(func() { __fl_g3() }) }\n" +
		"\tdefer w.Wait()\n\t_ = i.Unlock\n\treturn m.Lock\n}\n" +
		"\nfunc H(e *q.E, t *T) {\n\t__fl_sched.Lock(&(e).Mutex)\n\t__fl_sched.Lock(&(t).own.Mutex)\n}\n" +
		"\nvar _ = __fl_sched.Gaps(" + strconv.Quote(strings.Join(wantGaps, "\n")) + ")\n"
	if got, gaps := rewriteSource(t, src, receiverDep); got != want || !slices.Equal(gaps, wantGaps) {
		t.Errorf("got\n%s\ngaps %q\nwant\n%s\ngaps %q", got, gaps, want, wantGaps)
	}
}

// The sync rows compile through the wrapper for receivers the rewriter cannot pass as a
// sync.Locker or cannot spell: narrowLockers and a type parameter go to ILock and IUnlock, and
// receiverDep's unexported embedded fields are gaps.
func TestSyncRowsCompileThroughTheWrapper(t *testing.T) {
	tc := currentToolchain(t)
	state := stateCopy(t, tc)
	prepared := readPrepared(t, filepath.Join(state, tc.key()+".importcfg"))
	dir := userModule(t, "q/q.go", receiverDep,
		"u.go", "package u\n\nimport \"example.com/u/q\"\n\n"+narrowLockers+"\n"+narrowLockersFunc+"\n"+receiverUse)
	in := func(rel string) string { return filepath.Join(dir, rel) }
	cfg := "packagefile sync=" + prepared["sync"] + "\npackagefile example.com/u/q=" + in("q.a") + "\n"
	if err := os.WriteFile(in("importcfg"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := t.TempDir()
	env := []string{"FAULTLINE_TOOLEXEC_STATE=" + state, "FAULTLINE_TOOLEXEC_KEEP=" + keep}
	for _, c := range []struct{ ip, out, file string }{{"example.com/u/q", "q.a", "q/q.go"}, {"example.com/u", "u.a", "u.go"}} {
		args := compileArgs(tc, c.ip, in("importcfg"), in(c.out), in(c.file))
		if out, code := wrapperRun(t, append(env, "TOOLEXEC_IMPORTPATH="+c.ip), args...); code != 0 {
			t.Fatalf("compile %s: exit %d\n%s", c.ip, code, out)
		}
	}
	kept, err := os.ReadFile(filepath.Join(keep, "example.com_u", "u.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"__fl_sched.ILock((l))", "__fl_sched.IUnlock((c))", "__fl_sched.IUnlock((e))", "__fl_sched.ILock((t))", "method receiver not nameable in this file"} {
		if !strings.Contains(string(kept), s) {
			t.Errorf("kept u.go has no %q:\n%s", s, kept)
		}
	}
}
