// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package golden

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/internal/toys"
	"github.com/hmdsefi/faultline/kernel"
)

// AT-DET-10
func TestEnabled(t *testing.T) {
	cases := []struct {
		val     string
		want    bool
		wantErr string
	}{
		{"", false, ""},
		{"0", false, ""},
		{"1", true, ""},
		{"yes", false, `golden: invalid FAULTLINE_CHECK_DETERMINISM="yes": want 0 or 1`},
	}
	for _, c := range cases {
		t.Setenv(EnvCheckDeterminism, c.val)
		got, err := Enabled()
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		if got != c.want || errText != c.wantErr {
			t.Errorf("%q: Enabled() = %v, %q; want %v, %q", c.val, got, errText, c.want, c.wantErr)
		}
	}
}

// pingPongSeeded is the pingpong/seeded scenario of DET-051.
func pingPongSeeded(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
	s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
	toys.PingPong(s, toys.NewWire(s, time.Millisecond, 5*time.Millisecond), 50)
	return s, s.Run()
}

// AT-DET-11
func TestCheckRuns(t *testing.T) {
	var levels []kernel.TraceConfig
	counting := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		levels = append(levels, trace)
		return pingPongSeeded(seed, trace)
	}
	want := Result{Seed: 1, Hash: 0x9360c9a84df78ce9, Stop: kernel.StopIdle, Executed: 102, Now: 292032661, Err: ""}
	hash := kernel.TraceConfig{Level: kernel.TraceHash}
	full := kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 0}

	t.Setenv(EnvCheckDeterminism, "")
	if got := Check(t, 1, counting); got != want {
		t.Errorf("Check = %+v, want %+v", got, want)
	}
	if len(levels) != 1 || levels[0] != hash {
		t.Errorf("unset: calls %v", levels)
	}

	levels = nil
	t.Setenv(EnvCheckDeterminism, "1")
	if got := Check(t, 1, counting); got != want {
		t.Errorf("Check = %+v", got)
	}
	if len(levels) != 2 || levels[0] != hash || levels[1] != full {
		t.Errorf("enabled: calls %v", levels)
	}

	levels = nil
	t.Setenv(EnvCheckDeterminism, "0")
	if got := MustBeDeterministic(t, 1, counting); got != want {
		t.Errorf("MustBeDeterministic = %+v", got)
	}
	if len(levels) != 2 || levels[0] != hash || levels[1] != full {
		t.Errorf("MustBeDeterministic: calls %v", levels)
	}
}

// fakeTB records failures; Fatal and Fatalf stop the calling goroutine like testing.T does.
type fakeTB struct {
	testing.TB
	mu   sync.Mutex
	msgs []string
}

func (f *fakeTB) Helper()                           {}
func (f *fakeTB) Logf(format string, args ...any)   {}
func (f *fakeTB) Errorf(format string, args ...any) { f.record(fmt.Sprintf(format, args...)) }
func (f *fakeTB) Fatal(args ...any)                 { f.record(fmt.Sprint(args...)); runtime.Goexit() }
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.record(fmt.Sprintf(format, args...))
	runtime.Goexit()
}

func (f *fakeTB) record(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
}

// failures runs fn with a fakeTB on its own goroutine and returns the recorded messages.
func failures(fn func(tb testing.TB)) []string {
	f := &fakeTB{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		fn(f)
	}()
	wg.Wait()
	return f.msgs
}

// AT-DET-12
func TestCheckFailures(t *testing.T) {
	if msgs := failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, pingPongSeeded) }); len(msgs) != 0 {
		t.Errorf("(a) deterministic run reported %q", msgs)
	}

	levelDependent := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.PingPong(s, toys.NewWire(s, time.Millisecond, 5*time.Millisecond), 3)
		if trace.Level == kernel.TraceFull {
			s.Logf("x")
		}
		return s, s.Run()
	}
	msgs := failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, levelDependent) })
	const wantB = `determinism: seed 0x0000000000000001: the trace level changes the run: ` +
		`TraceHash run hash=0x2661a4a5aab70232 stop=idle executed=8 now=0.013294714s err="", ` +
		`TraceFull run hash=0xd4267eea7ca41485 stop=idle executed=8 now=0.013294714s err=""`
	if len(msgs) != 1 || msgs[0] != wantB {
		t.Errorf("(b) messages %q\nwant %q", msgs, wantB)
	}

	calls := 0
	callDependent := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		calls++
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.PingPong(s, toys.NewWire(s, time.Millisecond, 5*time.Millisecond), 3)
		s.Logf("call %d", calls)
		return s, s.Run()
	}
	msgs = failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, callDependent) })
	const wantC = `determinism: seed 0x0000000000000001: two runs differ (` +
		`hash=0x2c63926e74c45076 stop=idle executed=8 now=0.013294714s err="" vs ` +
		`hash=0x85d1998faf104827 stop=idle executed=8 now=0.013294714s err=""); first difference at record 4:` + "\n" +
		`  run 1: seq=4 at=0.000000000s node=0 inc=0 kind=kernel.log cause=3 text="call 2" attrs=[]` + "\n" +
		`  run 2: seq=4 at=0.000000000s node=0 inc=0 kind=kernel.log cause=3 text="call 3" attrs=[]`
	if len(msgs) != 1 || msgs[0] != wantC {
		t.Errorf("(c) messages %q\nwant %q", msgs, wantC)
	}

	hashCalls := 0
	hashDependent := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		toys.PingPong(s, toys.NewWire(s, time.Millisecond, 5*time.Millisecond), 3)
		if trace.Level == kernel.TraceHash {
			hashCalls++
			s.Logf("hash call %d", hashCalls)
		}
		return s, s.Run()
	}
	msgs = failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, hashDependent) })
	const wantE = `determinism: seed 0x0000000000000001: two runs differ (` +
		`hash=0x530f1ba7419f6f72 stop=idle executed=8 now=0.013294714s err="" vs ` +
		`hash=0x2b3c3aacb392bb39 stop=idle executed=8 now=0.013294714s err=""); ` +
		`the TraceFull runs agree, so there is no record diff`
	if len(msgs) != 1 || msgs[0] != wantE {
		t.Errorf("(e) messages %q\nwant %q", msgs, wantE)
	}

	deadline := 0
	deadlineDependent := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		deadline++
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		return s, s.RunUntil(kernel.Time(deadline))
	}
	msgs = failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, deadlineDependent) })
	const wantF = `determinism: seed 0x0000000000000001: two runs differ (` +
		`hash=0x8627ec127688a847 stop=deadline executed=0 now=0.000000002s err="" vs ` +
		`hash=0x8627ec127688a847 stop=deadline executed=0 now=0.000000003s err="") with the same records`
	if len(msgs) != 1 || msgs[0] != wantF {
		t.Errorf("(f) messages %q\nwant %q", msgs, wantF)
	}

	attrCalls := 0
	attrDependent := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		attrCalls++
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		s.Emit(kernel.Record{Kind: "test.call", Text: "call", Attrs: []kernel.Attr{{Key: "n", Value: strconv.Itoa(attrCalls)}}})
		return s, s.Run()
	}
	msgs = failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, attrDependent) })
	const wantG = `determinism: seed 0x0000000000000001: two runs differ (` +
		`hash=0x8c0efd14c7df01fe stop=idle executed=0 now=0.000000000s err="" vs ` +
		`hash=0x8c0efe14c7df03b1 stop=idle executed=0 now=0.000000000s err=""); first difference at record 2:` + "\n" +
		`  run 1: seq=2 at=0.000000000s node=0 inc=0 kind=test.call cause=1 text="call" attrs=[n="2"]` + "\n" +
		`  run 2: seq=2 at=0.000000000s node=0 inc=0 kind=test.call cause=1 text="call" attrs=[n="3"]`
	if len(msgs) != 1 || msgs[0] != wantG {
		t.Errorf("(g) messages %q\nwant %q", msgs, wantG)
	}

	extraCalls := 0
	trailing := func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		extraCalls++
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		stop := s.Run()
		for i := 1; i < extraCalls; i++ {
			s.Logf("extra %d", i)
		}
		return s, stop
	}
	msgs = failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, trailing) })
	const wantH = `determinism: seed 0x0000000000000001: two runs differ (` +
		`hash=0x87fcd05ab9747d87 stop=idle executed=0 now=0.000000000s err="" vs ` +
		`hash=0xcb3d68042dafce78 stop=idle executed=0 now=0.000000000s err=""); first difference at record 3:` + "\n" +
		`  run 1: <end of trace>` + "\n" +
		`  run 2: seq=3 at=0.000000000s node=0 inc=0 kind=kernel.log cause=2 text="extra 2" attrs=[]`
	if len(msgs) != 1 || msgs[0] != wantH {
		t.Errorf("(h) messages %q\nwant %q", msgs, wantH)
	}

	t.Setenv(EnvCheckDeterminism, "yes")
	const wantInvalid = `golden: invalid FAULTLINE_CHECK_DETERMINISM="yes": want 0 or 1`
	msgs = failures(func(tb testing.TB) { Check(tb, 1, pingPongSeeded) })
	if len(msgs) != 1 || msgs[0] != wantInvalid {
		t.Errorf("invalid variable: Check messages %q", msgs)
	}
	msgs = failures(func(tb testing.TB) { MustBeDeterministic(tb, 1, pingPongSeeded) })
	if len(msgs) != 1 || msgs[0] != wantInvalid {
		t.Errorf("invalid variable: MustBeDeterministic messages %q", msgs)
	}
}

// AT-DET-13
func TestFormatRecord(t *testing.T) {
	r := kernel.Record{Seq: 3, At: 1500000000, Node: 2, Inc: 1, Kind: "toys.send", Cause: 2, Text: "ping 1",
		Attrs: []kernel.Attr{{Key: "to", Value: "b"}, {Key: "latency_ns", Value: "5"}}}
	const want = `seq=3 at=1.500000000s node=2 inc=1 kind=toys.send cause=2 text="ping 1" attrs=[to="b" latency_ns="5"]`
	if got := FormatRecord(r); got != want {
		t.Errorf("FormatRecord = %s\nwant          %s", got, want)
	}
	r.Attrs = nil
	if got := FormatRecord(r); !strings.HasSuffix(got, " attrs=[]") {
		t.Errorf("no attrs: %s", got)
	}
}
