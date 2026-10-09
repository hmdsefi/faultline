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
		"replay:    FAULTLINE_SEED=0x0000000000000001 go test"+replayFlags()+" -run '^TestScenario$' github.com/hmdsefi/faultline\n",
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
