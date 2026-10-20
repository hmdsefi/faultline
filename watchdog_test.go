// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// stallingProcess stalls its run: A holds mu and blocks durably; B waits for mu, which is not a
// durable block (GOR §3).
func stallingProcess(ctx context.Context, p *Process) error {
	var mu sync.Mutex
	locked := make(chan struct{})
	p.Go(func() {
		mu.Lock()
		close(locked)
		<-ctx.Done()
		mu.Unlock()
	})
	<-locked
	time.Sleep(time.Second)
	p.Go(func() {
		mu.Lock()
		mu.Unlock() //nolint:staticcheck // SA2001: B only waits for mu
	})
	<-ctx.Done()
	return nil
}

// tickProcess calls Process.Do once per value received from tick, a channel made outside the
// bubble: waiting on it is not a durable block, so the driver makes progress only through Do.
func tickProcess(tick <-chan struct{}) func(ctx context.Context, p *Process) error {
	return func(ctx context.Context, p *Process) error {
		for range tick {
			if err := p.Do("tick", func() {}); err != nil {
				return err
			}
		}
		return nil
	}
}

// sendTicks sends n values to tick, 20 ms of wall time apart.
func sendTicks(tick chan<- struct{}, n int) {
	for range n {
		tick <- struct{}{}
		time.Sleep(20 * time.Millisecond)
	}
}

var stallRerunAttempts atomic.Int32

func init() { //nolint:gochecknoinits // registers the child-process scenarios
	gorScenarios["stall"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second, StallTimeout: 2 * time.Second},
			func(w *World) { w.AddProcess("p1", stallingProcess) })
	}
	// The primary attempt fails a final check, so the seed runs again (API-070); that attempt stalls.
	gorScenarios["stall-rerun"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second, StallTimeout: 2 * time.Second},
			func(w *World) {
				if stallRerunAttempts.Add(1) == 1 {
					w.Final("fails", func() error { return errors.New("first attempt fails") })
					return
				}
				w.AddProcess("p1", stallingProcess)
			})
	}
	// The final check waits on a channel made outside the bubble (a non-durable wait, so bubble
	// time stands still) until a goroutine outside the bubble closes it after 2 s of wall time.
	gorScenarios["stall-final"] = func(t *testing.T) {
		release := make(chan struct{})
		go func() {
			time.Sleep(2 * time.Second)
			close(release)
		}()
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Millisecond, StallTimeout: 500 * time.Millisecond},
			func(w *World) {
				w.Final("slow check", func() error {
					<-release
					return nil
				})
			})
	}
	// The stall scenario with a short StallTimeout, for the tests of where stall.txt goes.
	gorScenarios["stall-quick"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second, StallTimeout: 200 * time.Millisecond},
			func(w *World) { w.AddProcess("p1", stallingProcess) })
	}
	// StallTimeout is 1 ns, so the watchdog period would be 0 without its 1 ns floor.
	gorScenarios["stall-tiny-timeout"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second, StallTimeout: 1},
			func(w *World) { w.AddProcess("p1", stallingProcess) })
	}
	// Seed 5 of a RunSeeds test stalls: the replay command names the seed subtest (API-114).
	gorScenarios["stall-seeds"] = func(t *testing.T) {
		RunSeeds(t, Options{Mode: ModeGoroutine, Duration: 10 * time.Second, StallTimeout: time.Second},
			func(w *World) { w.AddProcess("p1", stallingProcess) }, 5)
	}
	// After the run, B waits for mu, which A holds while it blocks forever, so no drain chunk ends.
	gorScenarios["stall-drain"] = func(t *testing.T) {
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Millisecond, StallTimeout: time.Second},
			func(w *World) {
				w.AddProcess("p1", func(ctx context.Context, p *Process) error {
					var mu sync.Mutex
					locked := make(chan struct{})
					p.Go(func() {
						mu.Lock()
						close(locked)
						<-ctx.Done()
						select {}
					})
					<-locked
					p.Go(func() {
						<-ctx.Done()
						mu.Lock()
						mu.Unlock() //nolint:staticcheck // SA2001: B only waits for mu
					})
					<-ctx.Done()
					return nil
				})
			})
	}
	// The process sleeps 1.5 s of bubble time, runs one Do request, then waits on a channel made
	// outside the bubble for 1.8 s of wall time: StallTimeout 1s passes without progress before the
	// wait ends, and 2 × StallTimeout does not.
	gorScenarios["stall-late"] = func(t *testing.T) {
		waiting, resume := make(chan struct{}), make(chan struct{})
		go func() {
			<-waiting
			time.Sleep(1800 * time.Millisecond)
			close(resume)
		}()
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Second, StallTimeout: time.Second},
			func(w *World) {
				w.AddProcess("p1", func(ctx context.Context, p *Process) error {
					time.Sleep(1500 * time.Millisecond)
					if err := p.Do("x", func() {}); err != nil {
						return err
					}
					close(waiting)
					<-resume
					return nil
				})
			})
	}
	// The body waits 600 ms of wall time on a channel made outside the bubble. With StallTimeout 1s
	// that is not a stall: the first measurement starts when the watchdog starts.
	gorScenarios["watchdog-slow-body"] = func(t *testing.T) {
		release := make(chan struct{})
		go func() {
			time.Sleep(600 * time.Millisecond)
			close(release)
		}()
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Millisecond, StallTimeout: time.Second},
			func(w *World) { <-release })
	}
	// The process makes progress through Do for 1.5 s of wall time, longer than StallTimeout, then
	// waits 650 ms, more than two watchdog periods but less than StallTimeout, then ends.
	gorScenarios["watchdog-progress"] = func(t *testing.T) {
		tick := make(chan struct{})
		go func() {
			defer close(tick)
			sendTicks(tick, 75)
			time.Sleep(650 * time.Millisecond)
			sendTicks(tick, 5)
		}()
		Run(t, Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Millisecond, StallTimeout: 1200 * time.Millisecond},
			func(w *World) { w.AddProcess("p1", tickProcess(tick)) })
	}
	// Attempts that pass, skip and fail, then 5 × StallTimeout of wall time: a watchdog left
	// running after its attempt would report a stall.
	gorScenarios["watchdog-stopped"] = func(t *testing.T) {
		opts := Options{Mode: ModeGoroutine, Seeds: 1, Duration: 10 * time.Millisecond, StallTimeout: 200 * time.Millisecond}
		t.Run("pass", func(t *testing.T) { Run(t, opts, func(w *World) {}) })
		t.Run("skip", func(t *testing.T) { Run(t, opts, func(w *World) { w.T().Skip("skipped") }) })
		t.Run("fatal", func(t *testing.T) { Run(t, opts, func(w *World) { w.T().Fatal("stopped") }) })
		time.Sleep(time.Second)
	}
}

// wantStall fails t unless the child ended with GOR-081's stall panic and reported no data race.
func wantStall(t *testing.T, res childResult) {
	t.Helper()
	if res.code == 0 || !strings.Contains(res.out, "panic: faultline: goroutine mode stalled; see the report above") ||
		strings.Contains(res.out, "DATA RACE") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

// wantNoStall fails t unless the child exited with code and printed no stall report and no data
// race.
func wantNoStall(t *testing.T, res childResult, code int) {
	t.Helper()
	if res.code != code || strings.Contains(res.out, "goroutine mode stalled") || strings.Contains(res.out, "DATA RACE") {
		t.Fatalf("exit %d, want %d\n%s", res.code, code, res.out)
	}
}

func TestGoroutineStallWatchdog(t *testing.T) {
	start := time.Now()
	res := runGorScenario(t, "stall", "FAULTLINE_SEED=0x1")
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("child ran %v, want < 30s", elapsed)
	}
	if res.code == 0 || strings.Contains(res.out, "DATA RACE") {
		t.Fatalf("child exited %d\n%s", res.code, res.out)
	}
	for _, want := range []string{
		"faultline: goroutine mode stalled: no driver progress for 2s of wall time",
		"  test:     TestGoroutineScenario/seed=0x0000000000000001 (attempt 1)",
		`  virtual:  t=0.000000000s, phase run, 1 events executed, last event ""`,
		"  replay:   FAULTLINE_SEED=0x0000000000000001 go test -v", // API-080: -race when built with it
		"-run '^TestGoroutineScenario$' github.com/hmdsefi/faultline\n",
		"[sync.Mutex.Lock, synctest bubble ", // B's block, not the hint line about [sync.Mutex.Lock]
		"goroutines of this simulation (synctest bubble ",
		"panic: faultline: goroutine mode stalled; see the report above",
	} {
		if !strings.Contains(res.out, want) {
			t.Fatalf("output lacks %q\n%s", want, res.out)
		}
	}
	// The header names the bubble of the blocks below, and the blocks include the driver's
	// (GOR-081: every block whose header names this bubble).
	m := regexp.MustCompile(`goroutines of this simulation \(synctest bubble (\d+)\):\n`).FindStringSubmatch(res.out)
	if m == nil || !strings.Contains(res.out, "[sync.Mutex.Lock, synctest bubble "+m[1]+"]:") {
		t.Fatalf("B's block does not name the bubble of the header %q\n%s", m, res.out)
	}
	if !slices.ContainsFunc(strings.Split(res.out, "\n\n"), func(b string) bool {
		return strings.Contains(b, "synctest bubble "+m[1]+"]:\n") && strings.Contains(b, "github.com/hmdsefi/faultline.(*driver).drive(")
	}) {
		t.Fatalf("no block of the driver goroutine\n%s", res.out)
	}
	dir := artifact.Dir(res.artifacts, "github.com/hmdsefi/faultline", "TestGoroutineScenario", 1)
	b, err := os.ReadFile(filepath.Join(dir, "stall.txt"))
	if err != nil || !strings.HasPrefix(string(b), "faultline: goroutine mode stalled: ") {
		t.Fatalf("stall.txt: %v\n%s", err, b)
	}
	if !strings.Contains(res.out, string(b)) { // GOR-081: the same text as on stderr
		t.Fatalf("stall.txt differs from the report on stderr\nstall.txt:\n%s\noutput:\n%s", b, res.out)
	}
}

// GOR-081: the report numbers the attempt within the seed; the artifact re-run is attempt 2.
func TestGoroutineStallRerunAttempt(t *testing.T) {
	res := runGorScenario(t, "stall-rerun", "FAULTLINE_SEED=0x1")
	wantStall(t, res)
	if !strings.Contains(res.out, "  test:     TestGoroutineScenario/seed=0x0000000000000001 (attempt 2)\n") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

// GOR-081, API-074: when the seed's folder holds another test's artifact, stall.txt goes to
// artifact.AltDir, where the seed's artifact would go, and the other artifact stays as it was.
func TestGoroutineStallFolderCollision(t *testing.T) {
	const pkg = "github.com/hmdsefi/faultline"
	root := t.TempDir()
	dir, alt := artifact.Dir(root, pkg, "TestGoroutineScenario", 1), artifact.AltDir(root, pkg, "TestGoroutineScenario", 1)
	other := &artifact.Artifact{Report: artifact.Report{Package: "example.com/other", Test: "TestGoroutineScenario",
		Seed: "0x0000000000000001", Status: "pass"}, Trace: &artifact.Trace{}}
	if err := artifact.Write(dir, other); err != nil {
		t.Fatal(err)
	}
	res := runGorScenario(t, "stall", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS="+root)
	if res.code == 0 || !strings.Contains(res.out, "panic: faultline: goroutine mode stalled; see the report above") ||
		strings.Contains(res.out, "DATA RACE") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
	b, err := os.ReadFile(filepath.Join(alt, "stall.txt"))
	if err != nil || !strings.HasPrefix(string(b), "faultline: goroutine mode stalled: ") {
		t.Fatalf("AltDir stall.txt: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "stall.txt")); !os.IsNotExist(err) {
		t.Fatalf("stall.txt in the other test's folder: %v", err)
	}
	if rep, err := artifact.ReadReportFile(dir); err != nil || rep == nil || rep.Package != "example.com/other" {
		t.Fatalf("the other test's report changed: %+v, %v", rep, err)
	}
}

// GOR-081, ART-012: stall.txt is written the way artifact.Write writes its files. A symbolic link
// at the seed's folder is not followed (nothing is written), stall.txt replaces a symbolic link or
// a FIFO of that name instead of writing through it, and a write that fails (a directory named
// stall.txt) is ignored. No temporary file is left behind.
func TestGoroutineStallFileNoFollow(t *testing.T) {
	const pkg = "github.com/hmdsefi/faultline"
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir, outside string) // dir is the seed's folder; outside is another folder
		check func(t *testing.T, dir, outside string)
	}{
		{"link at the folder", func(t *testing.T, dir, outside string) {
			if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, dir); err != nil {
				t.Skip(err)
			}
		}, func(t *testing.T, dir, outside string) {
			if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the link at the seed's folder changed: %v, %v", fi, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatalf("written through the link: %v, %v", entries, err)
			}
		}},
		{"link at stall.txt", func(t *testing.T, dir, outside string) {
			if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("precious"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "victim.txt"), filepath.Join(dir, "stall.txt")); err != nil {
				t.Skip(err)
			}
		}, func(t *testing.T, dir, outside string) {
			if b, err := os.ReadFile(filepath.Join(outside, "victim.txt")); err != nil || string(b) != "precious" {
				t.Fatalf("victim.txt changed: %q, %v", b, err)
			}
			checkStallFile(t, dir)
		}},
		{"fifo at stall.txt", func(t *testing.T, dir, outside string) {
			mkfifo, err := exec.LookPath("mkfifo")
			if err != nil {
				t.Skip("no mkfifo command: ", err)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(mkfifo, filepath.Join(dir, "stall.txt")).CombinedOutput(); err != nil {
				t.Skipf("cannot create a FIFO here: %v %s", err, out)
			}
		}, func(t *testing.T, dir, outside string) { checkStallFile(t, dir) }},
		{"directory at stall.txt", func(t *testing.T, dir, outside string) {
			if err := os.MkdirAll(filepath.Join(dir, "stall.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, dir, outside string) {
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || !entries[0].IsDir() {
				t.Fatalf("seed folder: %v, %v", entries, err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			dir := artifact.Dir(root, pkg, "TestGoroutineScenario", 1)
			c.setup(t, dir, outside)
			res := runGorScenario(t, "stall-quick", "FAULTLINE_SEED=0x1", "FAULTLINE_ARTIFACTS="+root)
			wantStall(t, res)
			if !strings.Contains(res.out, "faultline: goroutine mode stalled: no driver progress for 200ms of wall time\n") {
				t.Fatalf("no stall report\n%s", res.out)
			}
			c.check(t, dir, outside)
		})
	}
}

// checkStallFile fails t unless dir holds only stall.txt, a regular file with mode 0o644 that
// holds a stall report.
func checkStallFile(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "stall.txt" {
		t.Fatalf("seed folder: %v, %v", entries, err)
	}
	p := filepath.Join(dir, "stall.txt")
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("stall.txt: %v, %v", fi, err)
	}
	if b, err := os.ReadFile(p); err != nil || !strings.HasPrefix(string(b), "faultline: goroutine mode stalled: ") {
		t.Fatalf("stall.txt: %v\n%s", err, b)
	}
}

// GOR-080, GOR-081: the report shows the driver's last published state: bubble time and the
// executed count after the last step, and the label of the last request whose wrapper ran. The
// stall is reported after StallTimeout without progress, before 2 × StallTimeout.
func TestGoroutineStallReportFields(t *testing.T) {
	res := runGorScenario(t, "stall-late", "FAULTLINE_SEED=0x1")
	wantStall(t, res)
	const want = `  virtual:  t=1.500000000s, phase run, 2 events executed, last event "req do"` + "\n"
	if !strings.Contains(res.out, want) {
		t.Fatalf("output lacks %q\n%s", want, res.out)
	}
}

// GOR-081: only the final phase is exempt, so a drain that cannot finish a chunk is a stall.
func TestGoroutineStallInDrain(t *testing.T) {
	res := runGorScenario(t, "stall-drain", "FAULTLINE_SEED=0x1")
	wantStall(t, res)
	if !strings.Contains(res.out, ", phase drain, ") {
		t.Fatalf("not a stall in phase drain\n%s", res.out)
	}
}

// API-114: a RunSeeds seed's replay command names its seed subtest, as GOR-005's line does.
func TestGoroutineStallRunSeedsReplay(t *testing.T) {
	res := runGorScenario(t, "stall-seeds")
	wantStall(t, res)
	if !strings.Contains(res.out, "  replay:   go test -v") ||
		!strings.Contains(res.out, "-run '^TestGoroutineScenario$/^seed=0x0000000000000005$' github.com/hmdsefi/faultline\n  synctest") {
		t.Fatalf("replay command\n%s", res.out)
	}
}

// The watchdog's period has a floor of 1 ns: StallTimeout 1ns reports a stall instead of
// panicking in time.NewTicker.
func TestGoroutineStallTinyTimeout(t *testing.T) {
	wantStall(t, runGorScenario(t, "stall-tiny-timeout", "FAULTLINE_SEED=0x1"))
}

// GOR-081: the first measurement starts when the watchdog starts.
func TestGoroutineWatchdogSlowBody(t *testing.T) {
	wantNoStall(t, runGorScenario(t, "watchdog-slow-body", "FAULTLINE_SEED=0x1"), 0)
}

// GOR-081: a tick where progress changed restarts the measurement, so a run that keeps making
// progress for longer than StallTimeout, with a gap shorter than StallTimeout, is not a stall.
func TestGoroutineWatchdogProgress(t *testing.T) {
	wantNoStall(t, runGorScenario(t, "watchdog-progress", "FAULTLINE_SEED=0x1"), 0)
}

// GOR-006 step 2: the watchdog stops on every exit path of the attempt: a pass, t.Skip and
// t.Fatal in the bubble.
func TestGoroutineWatchdogStopped(t *testing.T) {
	res := runGorScenario(t, "watchdog-stopped", "FAULTLINE_SEED=0x1")
	wantNoStall(t, res, 1) // the fatal seed fails
	for _, want := range []string{
		"--- PASS: TestGoroutineScenario/pass/seed=0x0000000000000001",
		"--- SKIP: TestGoroutineScenario/skip/seed=0x0000000000000001",
		"--- FAIL: TestGoroutineScenario/fatal/seed=0x0000000000000001",
	} {
		if !strings.Contains(res.out, want) {
			t.Fatalf("output lacks %q\n%s", want, res.out)
		}
	}
}

// stop returns only after the watchdog goroutine has returned, so no watchdog outlives its
// attempt.
func TestStartWatchdogStopWaits(t *testing.T) {
	r := &runner{t: t, plan: &runPlan{opts: Options{StallTimeout: time.Hour}}}
	for range 20 {
		stop := r.startWatchdog(t, &goAttempt{watch: &watchState{}}, 1)
		stop()
		if n := watchdogsRunning.Load(); n != 0 {
			t.Fatalf("%d watchdog goroutines outlived stop", n)
		}
	}
}

// GOR-081, API-074: stall.txt goes where writeArtifact would put the seed's artifact.
func TestStallDir(t *testing.T) {
	for _, c := range []struct {
		name            string
		pkg, test, seed string // report.json in the seed's folder; pkg "": none
		toAlt           bool
	}{
		{"no folder", "", "", "", false},
		{"own artifact", "p", "", "0x0000000000000001", false},
		{"another package", "q", "", "0x0000000000000001", true},
		{"another test", "p", "TestOther", "0x0000000000000001", true},
		{"another seed", "p", "", "0x0000000000000002", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			r := &runner{t: t, build: buildInfo{importPath: "p"}, plan: &runPlan{env: &environment{artifactsRoot: root}}}
			dir := artifact.Dir(root, "p", t.Name(), 1)
			want := dir
			if c.toAlt {
				want = artifact.AltDir(root, "p", t.Name(), 1)
			}
			if c.pkg != "" {
				test := cmp.Or(c.test, t.Name())
				prev := &artifact.Artifact{Report: artifact.Report{Package: c.pkg, Test: test, Seed: c.seed, Status: "pass"}, Trace: &artifact.Trace{}}
				if err := artifact.Write(dir, prev); err != nil {
					t.Fatal(err)
				}
			}
			if got := r.stallDir(1); got != want {
				t.Fatalf("stallDir = %q, want %q", got, want)
			}
		})
	}
	r := &runner{t: t, build: buildInfo{importPath: "p"}, plan: &runPlan{env: &environment{artifactsOff: true}}}
	if got := r.stallDir(1); got != "" {
		t.Fatalf("stallDir with artifacts off = %q, want \"\"", got)
	}
}

// AT-GOR-34: a final check that blocks for 4 × StallTimeout of wall time is not a stall (GOR-081).
func TestATGOR34_WatchdogQuietDuringFinal(t *testing.T) {
	res := runGorScenario(t, "stall-final", "FAULTLINE_SEED=0x1")
	if res.code != 0 || strings.Contains(res.out, "goroutine mode stalled") {
		t.Fatalf("exit %d\n%s", res.code, res.out)
	}
}

func TestGoroutineWatchdogDisabled(t *testing.T) {
	before := watchdogsStarted.Load()
	// API-004: one Run per t, so each call gets its own subtest.
	t.Run("disabled", func(t *testing.T) {
		gw(t, Options{StallTimeout: -1, Duration: time.Millisecond}, func(w *World) {})
	})
	if watchdogsStarted.Load() != before {
		t.Fatal("a watchdog started with StallTimeout < 0")
	}
	t.Run("default", func(t *testing.T) {
		gw(t, Options{Duration: time.Millisecond}, func(w *World) {})
	})
	if watchdogsStarted.Load() == before {
		t.Fatal("no watchdog started with the default StallTimeout")
	}
}

func TestFormatStallReport(t *testing.T) {
	got := formatStallReport(2*time.Second, "TestX/seed=0x0000000000000001", "1",
		kernel.Time(1500*time.Millisecond), "run", 42, "req net.pump",
		"FAULTLINE_SEED=0x0000000000000001 go test -v -run '^TestX$' example.com/x", 3,
		[]string{"goroutine 7 [sync.Mutex.Lock, synctest bubble 3]:\nmain.f()\n\t/a.go:1 +0x1"})
	want := strings.Join([]string{
		"faultline: goroutine mode stalled: no driver progress for 2s of wall time",
		"  test:     TestX/seed=0x0000000000000001 (attempt 1)",
		`  virtual:  t=1.500000000s, phase run, 42 events executed, last event "req net.pump"`,
		"  replay:   FAULTLINE_SEED=0x0000000000000001 go test -v -run '^TestX$' example.com/x",
		"  synctest advances only when every goroutine in the bubble is durably blocked.",
		`  Look for goroutines below whose state has no "(durable)":`,
		"    [sync.Mutex.Lock] / [sync.RWMutex.Lock] / [sync.RWMutex.RLock]: waiting for a mutex whose",
		"        holder is blocked; do not hold a mutex across a blocking netsim, fsx or Process.Do call",
		`    [chan receive] / [chan send] / [select] without "(durable)": a channel created outside the`,
		"        bubble, for example a package-level channel made during init",
		"    [IO wait] / [syscall]: real I/O that does not go through netsim or fsx",
		"    [running] / [runnable]: a goroutine that never blocks (busy loop)",
		"goroutines of this simulation (synctest bubble 3):",
		"goroutine 7 [sync.Mutex.Lock, synctest bubble 3]:",
		"main.f()",
		"\t/a.go:1 +0x1",
		"",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
}
