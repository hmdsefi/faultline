// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"testing/cryptotest"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/fault"
)

var _ = scenario("ticker-seeds", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		if os.Getenv("OUT") != "" {
			outLine(t, fmt.Sprintf("0x%016x", w.Seed()))
		}
	})
})

var subtestRe = regexp.MustCompile(`=== RUN   TestScenario/seed=(0x[0-9a-f]{16})`)

// ranSeeds returns the seed subtests started, in order.
func ranSeeds(out string) []string {
	var seeds []string
	for _, m := range subtestRe.FindAllStringSubmatch(out, -1) {
		seeds = append(seeds, m[1])
	}
	return seeds
}

func hexSeeds(seeds ...uint64) []string {
	var out []string
	for _, s := range seeds {
		out = append(out, fmt.Sprintf("0x%016x", s))
	}
	return out
}

// replayFlags returns the build flags that the replay command repeats for this test binary
// (API-080): " -tags <tags>" and " -race" when it was built with them. runScenario runs this
// binary, so its replay lines carry the same flags.
func replayFlags() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var tags, race string
	for _, s := range bi.Settings {
		switch {
		case s.Key == "-tags":
			tags = " -tags " + s.Value
		case s.Key == "-race" && s.Value == "true":
			race = " -race"
		}
	}
	return tags + race
}

func firstSeeds(n int) []uint64 {
	var out []uint64
	for k := 0; k < n; k++ {
		out = append(out, seedK(k))
	}
	return out
}

// AT-API-02
func TestRunDefaultSeeds(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	o, code := runScenario(t, "ticker-seeds", []string{"OUT=" + out})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	want := hexSeeds(firstSeeds(20)...)
	if got := ranSeeds(o); !slices.Equal(got, want) {
		t.Fatalf("subtests %v\nwant %v", got, want)
	}
	if got := readLines(t, out); !slices.Equal(got, want) {
		t.Fatalf("OUT %v", got)
	}
}

// AT-API-03
func TestRunShort(t *testing.T) {
	o, code := runScenario(t, "ticker-seeds", nil, "-test.short")
	if code != 0 || !slices.Equal(ranSeeds(o), hexSeeds(firstSeeds(5)...)) || !strings.Contains(o, "faultline: -short: running 5 of 20 seeds") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	o, code = runScenario(t, "ticker-seeds", []string{"FAULTLINE_SEEDS=7"}, "-test.short")
	if code != 0 || !slices.Equal(ranSeeds(o), hexSeeds(firstSeeds(7)...)) {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

// AT-API-04
func TestRunSeedEnv(t *testing.T) {
	for _, v := range []string{"0x2a", "0X2A", "42", " 42 "} {
		o, code := runScenario(t, "ticker-seeds", []string{"FAULTLINE_SEED=" + v, "FAULTLINE_SEEDS=9"})
		if code != 0 || !slices.Equal(ranSeeds(o), []string{"0x000000000000002a"}) || !strings.Contains(o, "faultline: FAULTLINE_SEED is set; ignoring FAULTLINE_SEEDS") {
			t.Fatalf("FAULTLINE_SEED=%q: exit %d\n%s", v, code, o)
		}
	}
}

// AT-API-05
func TestRunInvalidEnv(t *testing.T) {
	artifactsErr := func(v string) string {
		return fmt.Sprintf("faultline: invalid FAULTLINE_ARTIFACTS value %q: artifacts are on by default; set a directory path for the artifact root, or off to turn artifacts off", v)
	}
	cases := []struct {
		env  []string
		want string
	}{
		{[]string{"FAULTLINE_SEED=0xZZ"}, `faultline: invalid FAULTLINE_SEED value "0xZZ": want a decimal or 0x-prefixed hexadecimal uint64`},
		{[]string{"FAULTLINE_SEED=-1"}, `faultline: invalid FAULTLINE_SEED value "-1": want a decimal or 0x-prefixed hexadecimal uint64`},
		{[]string{"FAULTLINE_SEED=0x"}, `faultline: invalid FAULTLINE_SEED value "0x": want a decimal or 0x-prefixed hexadecimal uint64`},
		{[]string{"FAULTLINE_SEED=0x1_0"}, `faultline: invalid FAULTLINE_SEED value "0x1_0": want a decimal or 0x-prefixed hexadecimal uint64`},
		{[]string{"FAULTLINE_SEED=18446744073709551616"}, `faultline: invalid FAULTLINE_SEED value "18446744073709551616": want a decimal or 0x-prefixed hexadecimal uint64`},
		{[]string{"FAULTLINE_SEEDS=0"}, `faultline: invalid FAULTLINE_SEEDS value "0": want a decimal integer from 1 to 1000000`},
		{[]string{"FAULTLINE_SEEDS=1000001"}, `faultline: invalid FAULTLINE_SEEDS value "1000001": want a decimal integer from 1 to 1000000`},
		{[]string{"FAULTLINE_EXPLORE=yes"}, `faultline: invalid FAULTLINE_EXPLORE value "yes": want 0 or 1`},
		{[]string{"FAULTLINE_CHECK_DETERMINISM=true"}, `faultline: invalid FAULTLINE_CHECK_DETERMINISM value "true": want 0 or 1`},
		{[]string{"FAULTLINE_TRACE=Full"}, `faultline: invalid FAULTLINE_TRACE value "Full": want hash or full`},
		{[]string{"FAULTLINE_ARTIFACTS=0"}, artifactsErr("0")},
		{[]string{"FAULTLINE_ARTIFACTS=false"}, artifactsErr("false")},
		{[]string{"FAULTLINE_ARTIFACTS=NO"}, artifactsErr("NO")},
		{[]string{"FAULTLINE_ARTIFACTS=1"}, artifactsErr("1")},
		{[]string{"FAULTLINE_ARTIFACTS=True"}, artifactsErr("True")},
		{[]string{"FAULTLINE_ARTIFACTS=yes"}, artifactsErr("yes")},
		{[]string{"FAULTLINE_ARTIFACTS=ON"}, artifactsErr("ON")},
		{[]string{"FAULTLINE_SEED_LIST=1,,0xZZ"}, `faultline: invalid FAULTLINE_SEED_LIST entry 2 "0xZZ": want a decimal or 0x-prefixed hexadecimal uint64`},
		{[]string{"FAULTLINE_SEED=1", "FAULTLINE_SEED_LIST=2"}, `faultline: FAULTLINE_SEED and FAULTLINE_SEED_LIST are both set; set only one`},
	}
	for _, c := range cases {
		o, code := runScenario(t, "ticker-seeds", c.env)
		if code != 1 || len(ranSeeds(o)) != 0 || !strings.Contains(o, c.want) {
			t.Errorf("%v: exit %d, want 1 and %q\n%s", c.env, code, c.want, o)
		}
	}
}

var _ = scenario("ticker-base", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 3, BaseSeed: 1, Duration: time.Second}, func(w *faultline.World) { addTicker(w) })
})

// AT-API-06
func TestRunBaseSeed(t *testing.T) {
	o, code := runScenario(t, "ticker-base", nil)
	if code != 0 || !slices.Equal(ranSeeds(o), hexSeeds(0x910a2dec89025cc1, 0xbeeb8da1658eec67, 0xf893a2eefb32555e)) || !strings.Contains(o, "(Options.BaseSeed)") {
		t.Fatalf("(a) exit %d\n%s", code, o)
	}
	b := hexSeeds(0xbdd732262feb6e95, 0x28efe333b266f103, 0x47526757130f9f52)
	o, code = runScenario(t, "ticker-base", []string{"FAULTLINE_BASE_SEED=0x2a"})
	if code != 0 || !slices.Equal(ranSeeds(o), b) {
		t.Fatalf("(b) exit %d\n%s", code, o)
	}
	o, code = runScenario(t, "ticker-base", []string{"FAULTLINE_BASE_SEED=0x2a", "FAULTLINE_EXPLORE=1"})
	if code != 0 || !slices.Equal(ranSeeds(o), b) || !strings.Contains(o, "faultline: FAULTLINE_BASE_SEED is set; ignoring FAULTLINE_EXPLORE") {
		t.Fatalf("(c) exit %d\n%s", code, o)
	}
}

var _ = scenario("ticker-one", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, func(w *faultline.World) { addTicker(w) })
})

// AT-API-07
func TestRunExplore(t *testing.T) {
	re := regexp.MustCompile(`(?m)^\s*.*faultline: FAULTLINE_EXPLORE: base seed (0x[0-9a-f]{16}) \(rerun this set with FAULTLINE_BASE_SEED=(0x[0-9a-f]{16})\)$`)
	var bases []string
	for i := 0; i < 2; i++ {
		o, code := runScenario(t, "ticker-one", []string{"FAULTLINE_EXPLORE=1"})
		m := re.FindAllStringSubmatch(o, -1)
		if code != 0 || len(m) != 1 || m[0][1] != m[0][2] {
			t.Fatalf("exit %d\n%s", code, o)
		}
		var base uint64
		if _, err := fmt.Sscanf(m[0][1], "0x%x", &base); err != nil {
			t.Fatal(err)
		}
		if got := ranSeeds(o); !slices.Equal(got, hexSeeds(faultline.DeriveSeed(base, 0))) {
			t.Fatalf("subtest %v for base %s", got, m[0][1])
		}
		bases = append(bases, m[0][1])
	}
	if bases[0] == bases[1] {
		t.Fatalf("two explore runs used the same base %s", bases[0])
	}
}

var _ = scenario("crypto", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second, CheckDeterminism: true}, func(w *faultline.World) {
		b := make([]byte, 16)
		rand.Read(b)
		outLine(t, fmt.Sprintf("0x%016x %s", w.Seed(), hex.EncodeToString(b)))
	})
})

// AT-API-08
func TestRunCryptoSeeded(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	o, code := runScenario(t, "crypto", []string{"OUT=" + out, "FAULTLINE_SEED_LIST=0x1,0x2,0x1"})
	if code != 0 || !slices.Equal(ranSeeds(o), []string{"0x0000000000000001", "0x0000000000000002"}) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	lines := readLines(t, out)
	var want []string
	for _, s := range []uint64{1, 2} {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			cryptotest.SetGlobalRandom(t, kernel.New(kernel.Config{Seed: s}).Rand("crypto").Uint64())
			b := make([]byte, 16)
			rand.Read(b)
			line := fmt.Sprintf("0x%016x %s", s, hex.EncodeToString(b))
			want = append(want, line, line) // primary and check attempt
		})
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("crypto bytes %q\nwant %q", lines, want)
	}
}

var _ = scenario("crypto-parallel-noseed", func(t *testing.T) {
	t.Parallel()
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second, NoCryptoSeed: true, CheckDeterminism: true}, func(w *faultline.World) {
		b := make([]byte, 16)
		rand.Read(b)
		outLine(t, hex.EncodeToString(b))
	})
})

// AT-API-09
func TestRunNoCryptoSeedParallel(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	o, code := runScenario(t, "crypto-parallel-noseed", []string{"OUT=" + out})
	lines := readLines(t, out)
	if code != 0 || len(lines) != 2 || lines[0] == lines[1] {
		t.Fatalf("exit %d lines %q\n%s", code, lines, o)
	}
}

var _ = scenario("crypto-parallel", func(t *testing.T) {
	t.Parallel()
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) { addTicker(w) })
})

// AT-API-10
func TestRunCryptoParallel(t *testing.T) {
	o, code := runScenario(t, "crypto-parallel", nil)
	if code != 1 || len(ranSeeds(o)) != 1 || !strings.Contains(o, "faultline: crypto seeding:") || !strings.Contains(o, "remove t.Parallel or set Options.NoCryptoSeed") {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

var _ = scenario("runfor-past-end", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.RunFor(2 * time.Second)
	})
})

// AT-API-14
func TestRunForPastEnd(t *testing.T) {
	o, code := runScenario(t, "runfor-past-end", nil)
	if code != 1 || !strings.Contains(o, "faultline: World.RunFor(2s) at t=0.000000000s would run past the end of the run (1.000000000s); raise Options.Duration") || strings.Contains(o, "artifacts:") {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

var _ = scenario("invariant-counter", func(t *testing.T) {
	faultline.Run(t, faultline.Options{}, func(w *faultline.World) {
		tk := addTicker(w)
		w.Invariant("counter < 5", func() error {
			if tk.count >= 5 {
				return fmt.Errorf("counter=%d", tk.count)
			}
			return nil
		})
	})
})

// AT-API-15
func TestRunInvariantFailure(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "invariant-counter", []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	dir := seedDir(root, 1)
	head := regexp.MustCompile(`(?m)^ *faultline: invariant "counter < 5" violated at t=` + regexp.QuoteMeta(kernel.Time(50*time.Millisecond).String()) + ` on n1 \(event (\d+)\)$`).FindStringSubmatch(o)
	if head == nil {
		t.Fatalf("headline missing:\n%s", o)
	}
	mustContainInOrder(t, o, head[0], "\n", "  counter=5\n",
		"replay:    FAULTLINE_SEED=0x0000000000000001 go test -v"+replayFlags()+" -run '^TestScenario$' github.com/hmdsefi/faultline\n",
		"artifacts: "+dir+string(os.PathSeparator)+"\n")
	var violations []kernel.Record
	for _, r := range readTrace(t, dir) {
		if r.Kind == "check.violation" {
			violations = append(violations, r)
		}
	}
	want := []kernel.Attr{{Key: "kind", Value: "invariant"}, {Key: "check", Value: "counter < 5"}, {Key: "error", Value: "counter=5"}, {Key: "event", Value: head[1]}}
	if len(violations) != 1 || !slices.Equal(violations[0].Attrs, want) {
		t.Fatalf("check.violation records %+v", violations)
	}
	rep := readReport(t, dir)
	if rep.Failure.Signature != "invariant:counter < 5" || rep.Run.Attempts != 2 || rep.Failure.RecordSeq != violations[0].Seq || rep.Failure.Node != "n1" {
		t.Fatalf("report %+v %+v", rep.Failure, rep.Run)
	}
}

var _ = scenario("finals", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		for _, c := range []struct{ name, err string }{{"a", "A"}, {"b", "B"}, {"c", ""}} {
			w.Final(c.name, func() error {
				outLine(t, c.name)
				if c.err == "" {
					return nil
				}
				return errors.New(c.err)
			})
		}
	})
})

// AT-API-16
func TestRunFinalChecks(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	o, code := runScenario(t, "finals", []string{"OUT=" + out, "FAULTLINE_ARTIFACTS=" + root})
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if got := readLines(t, out); !slices.Equal(got, []string{"a", "b", "c", "a", "b", "c"}) {
		t.Fatalf("final calls %v", got)
	}
	mustContainInOrder(t, o, `faultline: final check "a" failed after the run at t=1.000000000s`+"\n", "  A\n", `  also failed: "b"`+"\n")
	rep := readReport(t, seedDir(root, seedK(0)))
	if len(rep.Failure.Finals) != 2 || rep.Failure.Finals[0].Check != "a" || rep.Failure.Finals[0].Message != "A" || rep.Failure.Finals[1].Check != "b" || rep.Failure.Finals[1].Message != "B" {
		t.Fatalf("finals %+v", rep.Failure.Finals)
	}
}

var _ = scenario("panic-tick", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		tk := addTicker(w)
		tk.panicAt = 3
	})
})

// AT-API-17
func TestRunCallbackPanic(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "panic-tick", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 || !regexp.MustCompile(`faultline: panic at t=0\.030000000s on n1 \(event \d+\)\n`).MatchString(o) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	mustContainInOrder(t, o, "  assignment to entry in nil map\n", "stack:\n")
	rep := readReport(t, seedDir(root, seedK(0)))
	site := "github.com/hmdsefi/faultline_test.(*ticker).tick"
	if rep.Failure.Check != site || rep.Failure.Signature != "panic:"+site || rep.Failure.Panic == nil || rep.Failure.Panic.Site != site {
		t.Fatalf("failure %+v", rep.Failure)
	}
	trace := readTrace(t, seedDir(root, seedK(0)))
	pi := slices.IndexFunc(trace, func(r kernel.Record) bool { return r.Kind == "kernel.panic" })
	vi := slices.IndexFunc(trace, func(r kernel.Record) bool { return r.Kind == "check.violation" })
	if pi < 0 || vi < pi {
		t.Fatalf("kernel.panic at %d, check.violation at %d", pi, vi)
	}
}

var _ = scenario("fail-body", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Sim.Fail(errors.New("bad"))
	})
})

var _ = scenario("fail-tick", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		tk := addTicker(w)
		tk.onTick = func(n *kernel.Node, c int) {
			if c == 1 {
				n.Sim().Fail(errors.New("bad"))
			}
		}
	})
})

// AT-API-18
func TestRunSimFail(t *testing.T) {
	for _, c := range []struct {
		name, headline string
		stopNone       bool
	}{
		{"fail-body", `faultline: simulation failed in body at t=0\.000000000s\n`, true},
		{"fail-tick", `faultline: simulation failed at t=0\.010000000s on n1 \(event \d+\)\n`, false},
	} {
		root := t.TempDir()
		o, code := runScenario(t, c.name, []string{"FAULTLINE_ARTIFACTS=" + root})
		if code != 1 || !regexp.MustCompile(c.headline).MatchString(o) || !strings.Contains(o, "\n      bad\n") && !strings.Contains(o, "  bad\n") {
			t.Fatalf("%s: exit %d\n%s", c.name, code, o)
		}
		dir := seedDir(root, seedK(0))
		rep := readReport(t, dir)
		if rep.Failure.Signature != "fail:" {
			t.Fatalf("%s: signature %q", c.name, rep.Failure.Signature)
		}
		trace := readTrace(t, dir)
		if !slices.ContainsFunc(trace, func(r kernel.Record) bool { return r.Kind == "kernel.fail" && r.Text == "bad" }) {
			t.Fatalf("%s: no kernel.fail record", c.name)
		}
		end := trace[len(trace)-1]
		if end.Kind != "run.phase" || end.Text != "end" || (c.stopNone != slices.Contains(end.Attrs, kernel.Attr{Key: "stop", Value: "none"})) {
			t.Fatalf("%s: last record %+v", c.name, end)
		}
	}
}

var _ = scenario("spin", func(t *testing.T) {
	faultline.Run(t, faultline.Options{MaxEvents: 1000}, func(w *faultline.World) { addSpin(w) })
})

var _ = scenario("spin-allowed", func(t *testing.T) {
	faultline.Run(t, faultline.Options{MaxEvents: 1000, AllowLimit: true, Seeds: 1}, func(w *faultline.World) {
		addSpin(w)
		w.Final("ran", func() error { outLine(t, "final"); return nil })
	})
})

// AT-API-19
func TestRunLimit(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "spin", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 || !strings.Contains(o, "faultline: run exceeded MaxEvents (1000) at t=0.000000000s (event 1000)\n") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	rep := readReport(t, seedDir(root, seedK(0)))
	if rep.Failure.Signature != "limit:max-events" || rep.Run.Dropped != 0 || rep.Failure.Limit == nil || rep.Failure.Limit.Value != 1000 {
		t.Fatalf("report %+v %+v", rep.Failure, rep.Run)
	}
	out := filepath.Join(t.TempDir(), "out")
	o, code = runScenario(t, "spin-allowed", []string{"OUT=" + out})
	if code != 0 || !regexp.MustCompile(`faultline: seed 0x[0-9a-f]{16} stopped at MaxEvents \(1000\) at t=0\.000000000s; Options\.AllowLimit is set, so this is not a failure; final checks were skipped`).MatchString(o) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("the final check ran")
	}
}

var determinismCalls int

var _ = scenario("nondeterministic", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		determinismCalls++
		first := determinismCalls == 1
		addTicker(w)
		w.Invariant("first run only", func() error {
			if first {
				return errors.New("calls == 1")
			}
			return nil
		})
	})
})

// AT-API-20
func TestRunDeterminismFailure(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "nondeterministic", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 || !regexp.MustCompile(`faultline: determinism failure: re-running seed 0x[0-9a-f]{16} gave trace hash 0x[0-9a-f]{16}; the first run gave 0x[0-9a-f]{16}\n`).MatchString(o) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	mustContainInOrder(t, o, `the first run failed: invariant "first run only" violated`, "the two full-trace runs were identical")
	rep := readReport(t, seedDir(root, seedK(0)))
	if rep.Failure.Signature != "determinism:" || len(rep.Failure.Determinism.Hashes) != 3 || rep.Run.Attempts != 3 || rep.Failure.Determinism.Context != "artifact_rerun" {
		t.Fatalf("failure %+v", rep.Failure)
	}
}

var _ = scenario("pingpong-det", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Seeds: 3, Duration: time.Second, CheckDeterminism: true}, func(w *faultline.World) {
		addPingPong(w)
		outLine(t, "body")
	})
})

// AT-API-21
func TestRunCheckDeterminism(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	o, code := runScenario(t, "pingpong-det", []string{"OUT=" + out, "FAULTLINE_ARTIFACTS=" + root})
	if code != 0 || len(readLines(t, out)) != 6 {
		t.Fatalf("exit %d, %d body calls\n%s", code, len(readLines(t, out)), o)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("artifacts written: %v", entries)
	}
}

var _ = scenario("always-fails", func(t *testing.T) {
	keep := os.Getenv("KEEP") == "1"
	faultline.Run(t, faultline.Options{Seeds: 3, Duration: time.Second, KeepGoing: keep}, func(w *faultline.World) {
		addTicker(w)
		w.Invariant("never", func() error { return errors.New("no") })
	})
})

// AT-API-22
func TestRunKeepGoing(t *testing.T) {
	o, code := runScenario(t, "always-fails", nil)
	if code != 1 || !slices.Equal(ranSeeds(o), hexSeeds(seedK(0))) || !strings.Contains(o, "faultline: stopping after failing seed 0x9a33dad17ee0bb7d; 2 of 3 seeds not run (set Options.KeepGoing to run all)") {
		t.Fatalf("(a) exit %d\n%s", code, o)
	}
	o, code = runScenario(t, "always-fails", []string{"KEEP=1"})
	if code != 1 || !slices.Equal(ranSeeds(o), hexSeeds(firstSeeds(3)...)) || strings.Count(o, "--- FAIL: TestScenario/seed=") != 3 || strings.Contains(o, "stopping after failing seed") {
		t.Fatalf("(b) exit %d\n%s", code, o)
	}
}

// testPlanner records Start calls (AT-API-23).
type testPlanner struct {
	t        *testing.T
	returned *bool
	w        **faultline.World
}

func (p *testPlanner) Name() string { return "p" }
func (p *testPlanner) Start(ctx *fault.PlanContext) {
	w := *p.w
	servers := w.Servers()
	ok := ctx.Until == w.End() && ctx.Roles["leader"] != nil && ctx.Rand == w.Sim.Rand("fault/p") && slices.Equal(ctx.Servers, servers)
	outLine(p.t, fmt.Sprintf("start returned=%v until=%d ok=%v", *p.returned, ctx.Until, ok))
}

// attrValue returns the value of r's attr key and whether r has that attr.
func attrValue(r kernel.Record, key string) (string, bool) {
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return "", false
}

var _ = scenario("planners", func(t *testing.T) {
	quiet := time.Duration(0)
	if os.Getenv("QUIET") != "" {
		quiet, _ = time.ParseDuration(os.Getenv("QUIET"))
	}
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: 8 * time.Second, Trace: kernel.TraceConfig{Level: kernel.TraceFull}}, func(w *faultline.World) {
		returned := false
		ww := w
		tk := addTicker(w)
		_ = tk
		w.Role("leader", func() []kernel.NodeID { return []kernel.NodeID{1} })
		w.Plan(&testPlanner{t: t, returned: &returned, w: &ww})
		random := &fault.Random{Rules: []fault.Rule{{Kind: fault.KindPause, Every: time.Hour}}, Quiet: quiet}
		w.Plan(random)
		w.Final("observe", func() error {
			var plans []string
			for _, r := range w.Sim.Records() {
				if r.Kind == "run.plan" {
					until, _ := attrValue(r, "until_ns")
					plans = append(plans, r.Text+" until_ns="+until)
				}
				if _, ok := attrValue(r, "schedule"); ok && r.Kind == "run.phase" && r.Text == "setup" {
					outLine(t, "setup-has-schedule")
				}
			}
			outLine(t, fmt.Sprintf("final now=%d recovery=%d end=%d recoverAt=%d plans=%q", w.Sim.Now(), w.RecoveryStart(), w.End(), random.RecoverAt(), plans))
			return nil
		})
		returned = true
	})
})

// AT-API-23
func TestRunPlanners(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out1")
	o, code := runScenario(t, "planners", []string{"OUT=" + out})
	want := []string{
		"start returned=true until=8000000000 ok=true",
		`final now=8000000000 recovery=6000000000 end=8000000000 recoverAt=6000000000 plans=["planner p until_ns=8000000000" "planner random until_ns=6000000000"]`,
	}
	if code != 0 {
		t.Fatalf("(1) exit %d\n%s", code, o)
	}
	if got := readLines(t, out); !slices.Equal(got, want) {
		t.Fatalf("(1) OUT %q\n%s", got, o)
	}

	writeSchedule := func(name string, s fault.Schedule) string {
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := s.Write(f); err != nil {
			t.Fatal(err)
		}
		return path
	}
	empty := writeSchedule("empty.json", fault.Schedule{Version: 1})
	out2 := filepath.Join(dir, "out2")
	o, code = runScenario(t, "planners", []string{"OUT=" + out2, "FAULTLINE_SCHEDULE=" + empty})
	want2 := []string{"setup-has-schedule", "final now=8000000000 recovery=6000000000 end=8000000000 recoverAt=0 plans=[]"}
	if code != 0 || !slices.Equal(readLines(t, out2), want2) || !strings.Contains(o, "faultline: FAULTLINE_SCHEDULE="+empty+": 0 events; planners are disabled") {
		t.Fatalf("(2) exit %d %q\n%s", code, readLines(t, out2), o)
	}

	short := writeSchedule("short.json", fault.Schedule{Version: 1, End: kernel.Time(4 * time.Second), Recovery: kernel.Time(3 * time.Second)})
	out3 := filepath.Join(dir, "out3")
	o, code = runScenario(t, "planners", []string{"OUT=" + out3, "FAULTLINE_SCHEDULE=" + short})
	want3 := []string{"setup-has-schedule", "final now=4000000000 recovery=3000000000 end=4000000000 recoverAt=0 plans=[]"}
	if code != 0 || !slices.Equal(readLines(t, out3), want3) {
		t.Fatalf("(3) exit %d %q\n%s", code, readLines(t, out3), o)
	}

	o, code = runScenario(t, "planners", []string{"OUT=" + filepath.Join(dir, "out4"), "QUIET=8s"})
	if code != 1 || !strings.Contains(o, "faultline: World.Plan: fault.Random.Quiet is 8s; want 0 (default Duration/4) or a positive duration shorter than Options.Duration (8s)") {
		t.Fatalf("(4) exit %d\n%s", code, o)
	}
}

// AT-API-24
func TestRunScheduleMissing(t *testing.T) {
	o, code := runScenario(t, "ticker-seeds", []string{"FAULTLINE_SCHEDULE=/nonexistent.json"})
	if code != 1 || !strings.Contains(o, "faultline: FAULTLINE_SCHEDULE=/nonexistent.json: open /nonexistent.json: no such file or directory") {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

var _ = scenario("fatal-tick", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		tk := addTicker(w)
		tk.onTick = func(n *kernel.Node, c int) {
			if c == 3 {
				w.T().Fatalf("stop")
			}
		}
	})
})

// AT-API-25
func TestRunFatalInCallback(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "fatal-tick", []string{"FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	seed := hexSeeds(seedK(0))[0]
	note := "faultline: seed " + seed + " stopped by t.FailNow or t.Fatal at t=0.030000000s (event 4); no artifacts were written\n" +
		"      report failures with an Invariant, a Final check or w.Sim.Fail(err) to get a replayable report\n" +
		"    replay:    FAULTLINE_SEED=" + seed + " go test -v" + replayFlags() + " -run '^TestScenario$' github.com/hmdsefi/faultline\n"
	if code != 1 || !strings.Contains(o, note) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if _, err := os.Stat(seedDir(root, seedK(0))); !os.IsNotExist(err) {
		t.Fatal("artifact directory exists")
	}
	lines := resultLines(t, results)
	if len(lines) != 1 || lines[0]["status"] != "fail" || lines[0]["kind"] != "fail" ||
		lines[0]["message"] != "stopped by t.FailNow, t.Fatal or t.SkipNow" || lines[0]["at_ns"] != 3e7 {
		t.Fatalf("results %v", lines)
	}
}

var _ = scenario("errorf-tick", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		tk := addTicker(w)
		tk.onTick = func(n *kernel.Node, c int) {
			if c == 3 {
				w.T().Errorf("soft")
			}
		}
	})
})

// AT-API-26
func TestRunErrorfInCallback(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "errorf-tick", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 || !strings.Contains(o, "faultline: test marked failed during the run (t.Error, t.Errorf or t.Fail) at t=") || strings.Count(o, "soft") != 2 ||
		strings.Contains(o, "stopped by t.FailNow") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	rep := readReport(t, seedDir(root, seedK(0)))
	if rep.Failure.RecordSeq != 0 || rep.Failure.Signature != "fail:" {
		t.Fatalf("failure %+v", rep.Failure)
	}
}

var _ = scenario("skip", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) { w.T().Skip("skip me") })
})

// AT-API-27
func TestRunSkip(t *testing.T) {
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "skip", []string{"FAULTLINE_RESULTS=" + results, "FAULTLINE_SEED=0x1"})
	lines := resultLines(t, results)
	if code != 0 || len(lines) != 1 || lines[0]["status"] != "skip" || lines[0]["kind"] != nil {
		t.Fatalf("exit %d results %v\n%s", code, lines, o)
	}
}

var _ = scenario("goroutine-mode", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Mode: faultline.ModeGoroutine}, func(w *faultline.World) {})
})

// AT-API-28 (Phase 1 only; GOR-001 removes API-091)
func TestRunGoroutineModeRejected(t *testing.T) {
	o, code := runScenario(t, "goroutine-mode", nil)
	if code != 1 || !strings.Contains(o, "faultline: Options.Mode is ModeGoroutine, which this version of faultline does not support (goroutine mode arrives in Phase 2)") {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

var _ = scenario("fails-for-0x2", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Invariant("not seed 2", func() error {
			if w.Seed() == 2 {
				return errors.New("seed 2")
			}
			return nil
		})
	})
})

// AT-API-29
func TestRunWorkerProtocol(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "seeds.txt")
	if err := os.WriteFile(list, []byte("0x1\n0x2 3,0x2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(dir, "r.jsonl")
	o, code := runScenario(t, "fails-for-0x2", []string{"FAULTLINE_SEED_LIST=@" + list, "FAULTLINE_RESULTS=" + results})
	if code != 1 || !slices.Equal(ranSeeds(o), hexSeeds(1, 2, 3)) {
		t.Fatalf("exit %d\n%s", code, o)
	}
	lines := resultLines(t, results)
	if len(lines) != 3 {
		t.Fatalf("results %v", lines)
	}
	for i, st := range []string{"pass", "fail", "pass"} {
		l := lines[i]
		wall, _ := l["wall_ns"].(float64)
		if l["faultline_result"] != 1.0 || l["index"] != float64(i) || l["status"] != st || wall <= 0 || l["trace_hash"] == nil {
			t.Fatalf("line %d: %v", i, l)
		}
	}
	if lines[1]["signature"] != "invariant:not seed 2" {
		t.Fatalf("fail line %v", lines[1])
	}
	art, _ := lines[1]["artifact"].(string)
	if fi, err := os.Stat(art); err != nil || !fi.IsDir() {
		t.Fatalf("artifact %v: %v", lines[1]["artifact"], err)
	}
	// API-087: the fields come in the spec's order (trace_hash depends on the Go version).
	prefixes := []string{
		`{"faultline_result":1,"package":"github.com/hmdsefi/faultline","test":"TestScenario","seed":"0x0000000000000001","index":0,"status":"pass","events":101,"trace_hash":"0x`,
		`{"faultline_result":1,"package":"github.com/hmdsefi/faultline","test":"TestScenario","seed":"0x0000000000000002","index":1,"status":"fail","kind":"invariant","check":"not seed 2","signature":"invariant:not seed 2","message":"seed 2","at_ns":0,"at":"0.000000000s","events":1,"trace_hash":"0x`,
		`{"faultline_result":1,"package":"github.com/hmdsefi/faultline","test":"TestScenario","seed":"0x0000000000000003","index":2,"status":"pass","events":101,"trace_hash":"0x`,
	}
	for i, raw := range readLines(t, results) {
		if !strings.HasPrefix(raw, prefixes[i]) {
			t.Fatalf("line %d:\n%s\nwant the prefix\n%s", i, raw, prefixes[i])
		}
	}
	results2 := filepath.Join(dir, "r2.jsonl")
	_, code = runScenario(t, "fails-for-0x2", []string{"FAULTLINE_SEED_LIST=0xZZ", "FAULTLINE_RESULTS=" + results2})
	lines = resultLines(t, results2)
	if code != 1 || len(lines) != 1 || lines[0]["seed"] != "" || lines[0]["index"] != -1.0 || lines[0]["kind"] != "setup" {
		t.Fatalf("setup line %v", lines)
	}
	setup := `{"faultline_result":1,"package":"github.com/hmdsefi/faultline","test":"TestScenario","seed":"","index":-1,"status":"fail","kind":"setup","signature":"setup:",` +
		`"message":"faultline: invalid FAULTLINE_SEED_LIST entry 1 \"0xZZ\": want a decimal or 0x-prefixed hexadecimal uint64","wall_ns":0}`
	if raw := readLines(t, results2); raw[0] != setup {
		t.Fatalf("setup line:\n%s\nwant\n%s", raw[0], setup)
	}
}

// AT-API-31: off in any letter case. A value taken as a path would be a folder in the package
// directory, the child's working directory; the test removes one it created.
func TestRunArtifactsOff(t *testing.T) {
	for _, v := range []string{"off", "OFF", "Off"} {
		if _, err := os.Lstat(v); !os.IsNotExist(err) {
			t.Fatalf("%s exists in the package directory before the run: %v", v, err)
		}
		tmp := t.TempDir()
		o, code := runScenario(t, "invariant-counter", []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + v, "TMPDIR=" + tmp})
		if _, err := os.Lstat(v); !os.IsNotExist(err) {
			_ = os.RemoveAll(v)
			t.Errorf("FAULTLINE_ARTIFACTS=%s wrote artifacts to the package directory", v)
		}
		if code != 1 || !strings.Contains(o, "artifacts: off\n") {
			t.Fatalf("FAULTLINE_ARTIFACTS=%s: exit %d\n%s", v, code, o)
		}
		if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
			t.Fatalf("FAULTLINE_ARTIFACTS=%s: the temporary directory holds %v (%v)", v, entries, err)
		}
	}
}

var _ = scenario("run-twice", func(t *testing.T) {
	body := func(w *faultline.World) { addTicker(w) }
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, body)
	faultline.Run(t, faultline.Options{Seeds: 1, Duration: time.Second}, body)
})

// AT-API-32
func TestRunTwice(t *testing.T) {
	o, code := runScenario(t, "run-twice", nil)
	if code != 1 || !strings.Contains(o, `faultline: Run called twice in test "TestScenario"; wrap each call in t.Run with a distinct name`) || len(ranSeeds(o)) != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

var _ = scenario("logf", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Logf("hello %d", 7)
		w.Invariant("second event", func() error {
			if w.Sim.Executed() == 2 {
				return errors.New("event 2")
			}
			return nil
		})
	})
})

// AT-API-33
func TestRunLogf(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "logf", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 || strings.Count(o, "t=0.000000000s hello 7") != 1 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	trace := readTrace(t, seedDir(root, seedK(0)))
	if !slices.ContainsFunc(trace, func(r kernel.Record) bool { return r.Kind == "kernel.log" && r.Node == 0 && r.Text == "hello 7" }) {
		t.Fatal("no kernel.log record")
	}
}

var _ = scenario("body-panic", func(t *testing.T) {
	late := os.Getenv("LATE") == "1"
	faultline.Run(t, faultline.Options{Duration: 2 * time.Second}, func(w *faultline.World) {
		addTicker(w)
		if late {
			w.RunFor(time.Second)
		}
		panic("boom")
	})
})

// AT-API-36
func TestRunBodyPanic(t *testing.T) {
	root := t.TempDir()
	o, code := runScenario(t, "body-panic", []string{"FAULTLINE_ARTIFACTS=" + root})
	if code != 1 || !regexp.MustCompile(`faultline: body panicked during setup \(before the first event\) for seed 0x[0-9a-f]{16}: boom`).MatchString(o) || strings.Contains(o, "artifacts:") {
		t.Fatalf("(a) exit %d\n%s", code, o)
	}
	o, code = runScenario(t, "body-panic", []string{"FAULTLINE_ARTIFACTS=" + root, "LATE=1"})
	if code != 1 || !strings.Contains(o, "faultline: panic in body at t="+kernel.Time(time.Second).String()+"\n") {
		t.Fatalf("(b) exit %d\n%s", code, o)
	}
	if _, err := os.Stat(filepath.Join(seedDir(root, seedK(0)), "report.json")); err != nil {
		t.Fatal(err)
	}
}

var _ = scenario("step-in-body", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: 2 * time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Sim.Step()
		w.RunFor(time.Second)
	})
})

// AT-API-37
func TestRunStepInBody(t *testing.T) {
	o, code := runScenario(t, "step-in-body", nil)
	if code != 1 || !strings.Contains(o, "faultline: body advanced the simulation with w.Sim before calling w.RunFor; use w.RunFor so planners start at time 0") ||
		strings.Contains(o, "stopped by t.FailNow") {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

var _ = scenario("fail-first-event", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.Invariant("no events", func() error { return errors.New("an event ran") })
	})
})

// AT-API-39
func TestRunFilteredSeed(t *testing.T) {
	o, code := runScenario(t, "fail-first-event", nil, "-test.run=^TestScenario$/^seed=0x8a216e8699751f87$")
	if code != 1 || !slices.Equal(ranSeeds(o), []string{"0x8a216e8699751f87"}) || !strings.Contains(o, "--- FAIL: TestScenario/seed=0x8a216e8699751f87") ||
		!strings.Contains(o, "replay:    FAULTLINE_SEED=0x8a216e8699751f87 go test -v"+replayFlags()+" -run '^TestScenario$' github.com/hmdsefi/faultline\n") {
		t.Fatalf("exit %d\n%s", code, o)
	}
}

var _ = scenario("later-phase", func(t *testing.T) {
	var o faultline.Options
	switch os.Getenv("ROW") {
	case "procs":
		o.Procs = 2
	case "leak":
		o.FailOnLeak = true
	case "swarm":
		o.Swarm = true
	}
	faultline.Run(t, o, func(w *faultline.World) { addTicker(w) })
})

// AT-API-41 (each row applies until its phase ships)
func TestRunLaterPhase(t *testing.T) {
	cases := []struct {
		env  []string
		want string
	}{
		{[]string{"ROW=procs"}, "faultline: Options.Procs is not available until Phase 2"},
		{[]string{"ROW=leak"}, "faultline: Options.FailOnLeak is not available until Phase 2"},
		{[]string{"ROW=swarm"}, "faultline: Options.Swarm is not available until Phase 3"},
		{[]string{"FAULTLINE_SWARM=0"}, "faultline: FAULTLINE_SWARM is not available until Phase 3"},
		{[]string{"FAULTLINE_SWARM_CONFIG=/x.json"}, "faultline: FAULTLINE_SWARM_CONFIG is not available until Phase 3"},
		{[]string{"FAULTLINE_EXACT=run"}, "faultline: FAULTLINE_EXACT is not available until Phase 2b"},
	}
	for _, c := range cases {
		o, code := runScenario(t, "later-phase", c.env)
		if code != 1 || len(ranSeeds(o)) != 0 || !strings.Contains(o, c.want) {
			t.Errorf("%v: exit %d\n%s", c.env, code, o)
		}
	}
}

var _ = scenario("fatal-body", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		addTicker(w)
		w.T().Fatalf("stop")
	})
})

// resultVary matches the results-line values that change between runs or Go versions.
var resultVary = regexp.MustCompile(`"(trace_hash|wall_ns)":("0x[0-9a-f]{16}"|[0-9]+)`)

// normResult returns a raw results line with its trace_hash and wall_ns values replaced by ?.
func normResult(line string) string { return resultVary.ReplaceAllString(line, `"$1":?`) }

// AT-API-45
func TestRunFatalInBody(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	o, code := runScenario(t, "fatal-body", []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results})
	if code != 1 || strings.Contains(o, "stopped by t.FailNow or t.Fatal") || strings.Contains(o, "artifacts:") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if _, err := os.Stat(seedDir(root, 1)); !os.IsNotExist(err) {
		t.Fatal("artifact directory exists")
	}
	want := `{"faultline_result":1,"package":"github.com/hmdsefi/faultline","test":"TestScenario","seed":"0x0000000000000001","index":0,"status":"fail","kind":"setup","signature":"setup:",` +
		`"message":"stopped by t.FailNow, t.Fatal or t.SkipNow","wall_ns":?}`
	if raw := readLines(t, results); len(raw) != 1 || normResult(raw[0]) != want {
		t.Fatalf("results:\n%s\nwant\n%s", strings.Join(raw, "\n"), want)
	}
}

var fatalRerunCalls int

var _ = scenario("fatal-rerun", func(t *testing.T) {
	faultline.Run(t, faultline.Options{Duration: time.Second}, func(w *faultline.World) {
		fatalRerunCalls++
		outLine(t, "body")
		tk := addTicker(w)
		w.Invariant("tick 5", func() error {
			if tk.count == 5 {
				return errors.New("five")
			}
			return nil
		})
		if fatalRerunCalls == 2 {
			tk.onTick = func(n *kernel.Node, c int) {
				if c == 3 {
					w.T().Fatalf("stop")
				}
			}
		}
	})
})

// AT-API-46
func TestRunFatalInRerun(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(t.TempDir(), "r.jsonl")
	out := filepath.Join(t.TempDir(), "out")
	o, code := runScenario(t, "fatal-rerun", []string{"FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS=" + root, "FAULTLINE_RESULTS=" + results, "OUT=" + out})
	note := `faultline: invariant "tick 5" violated at t=0.050000000s (event 6)` + "\n      five\n" +
		"    faultline: the artifact re-run of seed 0x0000000000000001 stopped by t.FailNow or t.Fatal at t=0.030000000s (event 4); no artifacts were written\n" +
		"      the first run did not stop, so the test depends on something outside the seed; the failure above is the seed's outcome\n"
	if code != 1 || !strings.Contains(o, note) || strings.Contains(o, "artifacts:") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	if calls := readLines(t, out); len(calls) != 2 {
		t.Fatalf("%d body calls, want 2\n%s", len(calls), o)
	}
	if _, err := os.Stat(seedDir(root, 1)); !os.IsNotExist(err) {
		t.Fatal("artifact directory exists")
	}
	want := `{"faultline_result":1,"package":"github.com/hmdsefi/faultline","test":"TestScenario","seed":"0x0000000000000001","index":0,"status":"fail","kind":"invariant","check":"tick 5",` +
		`"signature":"invariant:tick 5","message":"five","at_ns":50000000,"at":"0.050000000s","events":6,"trace_hash":?,"wall_ns":?}`
	if raw := readLines(t, results); len(raw) != 1 || normResult(raw[0]) != want {
		t.Fatalf("results:\n%s\nwant\n%s", strings.Join(raw, "\n"), want)
	}
}
