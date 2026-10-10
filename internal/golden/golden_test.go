// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package golden

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/internal/toys"
	"github.com/hmdsefi/faultline/kernel"
)

var update = flag.Bool("update", false, "rewrite testdata/hashes.txt")

const (
	goldenHeader  = "# faultline golden v1"
	goldenColumns = "# scenario seed hash stop executed"
	goldenFile    = "testdata/hashes.txt"
	updateHint    = `golden hashes changed: if intended, run "go test ./internal/golden -run TestGolden -update" and explain the change in a "Golden-Update:" commit trailer`
)

// entry is one line of hashes.txt (DET-050).
type entry struct {
	scenario string
	seed     uint64
	hash     uint64
	stop     string
	executed uint64
}

func (e entry) String() string {
	return fmt.Sprintf("%s 0x%016x 0x%016x %s %d", e.scenario, e.seed, e.hash, e.stop, e.executed)
}

// compareKeys orders entries by scenario name (bytewise), then by seed (DET-050).
func compareKeys(a, b entry) int {
	return cmp.Or(strings.Compare(a.scenario, b.scenario), cmp.Compare(a.seed, b.seed))
}

func sortEntries(es []entry) { slices.SortFunc(es, compareKeys) }

// produce runs every scenario of list in order, every seed in order, through Check (DET-055), and
// returns the entries sorted by scenario and seed. A scenario and seed listed twice fail.
func produce(t testing.TB, list []scenario) []entry {
	t.Helper()
	var es []entry
	for _, sc := range list {
		for _, seed := range sc.seeds {
			res := Check(t, seed, sc.run)
			es = append(es, entry{sc.name, seed, res.Hash, res.Stop.String(), res.Executed})
		}
	}
	sortEntries(es)
	for i := 1; i < len(es); i++ {
		if compareKeys(es[i-1], es[i]) == 0 {
			t.Fatalf("golden: duplicate scenario %s seed 0x%016x", es[i].scenario, es[i].seed)
		}
	}
	return es
}

// formatGolden returns the file content of DET-050 for es.
func formatGolden(es []entry) string {
	var b strings.Builder
	b.WriteString(goldenHeader + "\n" + goldenColumns + "\n")
	for _, e := range es {
		b.WriteString(e.String() + "\n")
	}
	return b.String()
}

// parseGolden parses hashes.txt content. Empty content is an empty table.
func parseGolden(data string) ([]entry, error) {
	if data == "" {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	if lines[0] != goldenHeader {
		return nil, errors.New(`hashes.txt: missing header "# faultline golden v1"`)
	}
	var es []entry
	for i, l := range lines[1:] {
		if strings.HasPrefix(l, "#") {
			continue
		}
		e, ok := parseEntry(l)
		if !ok {
			return nil, fmt.Errorf("hashes.txt:%d: malformed entry", i+2)
		}
		es = append(es, e)
	}
	return es, nil
}

func parseEntry(l string) (entry, bool) {
	f := strings.Split(l, " ")
	if len(f) != 5 || f[0] == "" || f[3] == "" {
		return entry{}, false
	}
	seed, ok1 := parseHex(f[1])
	hash, ok2 := parseHex(f[2])
	executed, err := strconv.ParseUint(f[4], 10, 64)
	if !ok1 || !ok2 || err != nil {
		return entry{}, false
	}
	return entry{f[0], seed, hash, f[3], executed}, true
}

func parseHex(s string) (uint64, bool) {
	if len(s) != 18 || !strings.HasPrefix(s, "0x") {
		return 0, false
	}
	v, err := strconv.ParseUint(s[2:], 16, 64)
	return v, err == nil
}

// diffGolden returns the report lines of DET-055 step 3 sorted by scenario and seed, followed by
// the update hint, or nil if got equals want.
func diffGolden(got, want []entry) []string {
	find := func(es []entry, k entry) (entry, bool) {
		i := slices.IndexFunc(es, func(e entry) bool { return e.scenario == k.scenario && e.seed == k.seed })
		if i < 0 {
			return entry{}, false
		}
		return es[i], true
	}
	keys := append(slices.Clone(got), want...)
	sortEntries(keys)
	keys = slices.CompactFunc(keys, func(a, b entry) bool { return a.scenario == b.scenario && a.seed == b.seed })
	var lines []string
	for _, k := range keys {
		g, produced := find(got, k)
		w, pinned := find(want, k)
		prefix := fmt.Sprintf("golden: %s seed 0x%016x: ", k.scenario, k.seed)
		switch {
		case !pinned:
			lines = append(lines, prefix+"missing from testdata/hashes.txt")
		case !produced:
			lines = append(lines, prefix+"in testdata/hashes.txt but not produced")
		case g != w:
			lines = append(lines, prefix+fmt.Sprintf("got hash 0x%016x stop %s executed %d, want hash 0x%016x stop %s executed %d",
				g.hash, g.stop, g.executed, w.hash, w.stop, w.executed))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return append(lines, updateHint)
}

// AT-DET-17, AT-DET-18
func TestGolden(t *testing.T) {
	runGolden(t, scenarios, goldenFile, os.Getenv("FAULTLINE_GOLDEN_OUT"), *update)
}

// runGolden is TestGolden (DET-055) for list, the golden file at path and the output file out
// ("" for none), with -update set by rewrite.
func runGolden(t testing.TB, list []scenario, path, out string, rewrite bool) {
	t.Helper()
	got := produce(t, list)
	content := formatGolden(got)
	if out != "" {
		if err := os.WriteFile(out, []byte(content), 0o600); err != nil { //nolint:gosec // out is FAULTLINE_GOLDEN_OUT, a path CI chooses
			t.Fatal(err)
		}
	}
	if rewrite {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	want, err := parseGolden(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if lines := diffGolden(got, want); lines != nil {
		t.Errorf("%s", strings.Join(lines, "\n"))
	}
}

// AT-DET-17: the committed file is exactly what the writer produces from its own entries.
func TestGoldenFileFormat(t *testing.T) {
	data, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	es, err := parseGolden(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if formatGolden(es) != string(data) {
		t.Fatal("testdata/hashes.txt is not in canonical form (DET-050)")
	}
	for i := 1; i < len(es); i++ {
		if compareKeys(es[i-1], es[i]) >= 0 {
			t.Fatalf("testdata/hashes.txt: %q after %q: want one entry per scenario and seed, sorted (DET-050)", es[i], es[i-1])
		}
	}
	const header = `hashes.txt: missing header "# faultline golden v1"`
	for _, c := range []struct{ data, msg string }{
		{"# faultline golden v2\n", header},
		{"# faultline golden v10\n", header},
		{goldenHeader + "\n" + goldenColumns + "\nx 0x1 0x2 idle 3\n", "hashes.txt:3: malformed entry"},
		{goldenHeader + "\n\n", "hashes.txt:2: malformed entry"},
		{goldenHeader + "\nx 000000000000000001 0x0000000000000002 idle 3\n", "hashes.txt:2: malformed entry"},
		{goldenHeader + "\n 0x0000000000000001 0x0000000000000002 idle 3\n", "hashes.txt:2: malformed entry"},
		{goldenHeader + "\nx 0x0000000000000001 0x0000000000000002  3\n", "hashes.txt:2: malformed entry"},
	} {
		if _, err := parseGolden(c.data); err == nil || err.Error() != c.msg {
			t.Errorf("parseGolden(%q): %v, want %q", c.data, err, c.msg)
		}
	}
}

// DET-055: the report lines, sorted by scenario and seed, and the update hint.
func TestDiffGolden(t *testing.T) {
	got := []entry{{"b", 0x10, 0x5, "idle", 1}, {"d", 2, 0x9, "idle", 4}, {"a", 1, 0x2, "idle", 3}, {"e", 3, 0x1, "idle", 1}}
	want := []entry{{"c", 7, 0x6, "idle", 2}, {"a", 1, 0x4, "deadline", 5}, {"d", 2, 0x9, "idle", 5}, {"e", 3, 0x1, "idle", 1}}
	report := []string{
		"golden: a seed 0x0000000000000001: got hash 0x0000000000000002 stop idle executed 3, want hash 0x0000000000000004 stop deadline executed 5",
		"golden: b seed 0x0000000000000010: missing from testdata/hashes.txt",
		"golden: c seed 0x0000000000000007: in testdata/hashes.txt but not produced",
		"golden: d seed 0x0000000000000002: got hash 0x0000000000000009 stop idle executed 4, want hash 0x0000000000000009 stop idle executed 5",
		`golden hashes changed: if intended, run "go test ./internal/golden -run TestGolden -update" and explain the change in a "Golden-Update:" commit trailer`,
	}
	if lines := diffGolden(got, want); !slices.Equal(lines, report) {
		t.Errorf("report:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(report, "\n"))
	}
	if lines := diffGolden(want, want); lines != nil {
		t.Errorf("equal tables: %q", lines)
	}
}

// AT-DET-17: a changed scenario makes TestGolden fail with one line per seed and the update hint.
func TestGoldenReportsChanges(t *testing.T) {
	if *update {
		t.Skip("compares with testdata/hashes.txt, which -update rewrites")
	}
	data, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	es, err := parseGolden(string(data))
	if err != nil {
		t.Fatal(err)
	}
	var pinned []entry
	for _, e := range es {
		if e.scenario == "pingpong/seeded" {
			pinned = append(pinned, e)
		}
	}
	changed := slices.Clone(scenarios)
	for i, sc := range changed {
		if sc.name == "pingpong/seeded" {
			changed[i].run = func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
				s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
				toys.PingPong(s, toys.NewWire(s, time.Millisecond, 5*time.Millisecond), 51)
				return s, s.Run()
			}
		}
	}
	msgs := failures(func(tb testing.TB) { runGolden(tb, changed, goldenFile, "", false) })
	if len(msgs) != 1 {
		t.Fatalf("failures: %q", msgs)
	}
	lines := strings.Split(msgs[0], "\n")
	if len(lines) != len(pinned)+1 || lines[len(pinned)] != updateHint {
		t.Fatalf("report:\n%s", msgs[0])
	}
	for i, w := range pinned {
		prefix := fmt.Sprintf("golden: pingpong/seeded seed 0x%016x: got hash 0x", w.seed)
		// One more round delivers one more ping and pong (DET-041).
		suffix := fmt.Sprintf(" stop %s executed %d, want hash 0x%016x stop %s executed %d",
			w.stop, w.executed+2, w.hash, w.stop, w.executed)
		if l := lines[i]; !strings.HasPrefix(l, prefix) || !strings.HasSuffix(l, suffix) || len(l) != len(prefix)+16+len(suffix) {
			t.Errorf("line %q\nwant %s<16 hex digits>%s", l, prefix, suffix)
		}
	}
}

// DET-055, DET-033: runGolden stops on a malformed golden file, and produce on a scenario and seed
// listed twice and, with FAULTLINE_CHECK_DETERMINISM=1, on a scenario whose two runs differ.
func TestGoldenFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hashes.txt")
	if err := os.WriteFile(path, []byte(goldenHeader+"\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs := failures(func(tb testing.TB) { runGolden(tb, scenarios[:1], path, "", false) })
	if !slices.Equal(msgs, []string{"hashes.txt:2: malformed entry"}) {
		t.Errorf("malformed file: %q", msgs)
	}
	msgs = failures(func(tb testing.TB) { produce(tb, []scenario{scenarios[0], scenarios[0]}) })
	if !slices.Equal(msgs, []string{"golden: duplicate scenario pingpong/fifo seed 0x0000000000000001"}) {
		t.Errorf("duplicate: %q", msgs)
	}
	calls := 0
	callDependent := scenario{"call-dependent", []uint64{1}, func(seed uint64, trace kernel.TraceConfig) (*kernel.Sim, kernel.StopReason) {
		calls++
		s := kernel.New(kernel.Config{Seed: seed, Trace: trace})
		s.Logf("call %d", calls)
		return s, s.Run()
	}}
	t.Setenv(EnvCheckDeterminism, "1")
	msgs = failures(func(tb testing.TB) { produce(tb, []scenario{callDependent}) })
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], "determinism: seed 0x0000000000000001: two runs differ (") {
		t.Errorf("nondeterministic scenario: %q", msgs)
	}
}

// AT-DET-17, DET-055 steps 1 and 2: with -update, TestGolden writes FAULTLINE_GOLDEN_OUT, rewrites a
// stale golden file, both with exactly the committed bytes, and passes.
func TestGoldenUpdate(t *testing.T) {
	if *update {
		t.Skip("compares with testdata/hashes.txt, which -update rewrites")
	}
	dir := t.TempDir()
	path, out := filepath.Join(dir, "hashes.txt"), filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msgs := failures(func(tb testing.TB) { runGolden(tb, scenarios, path, out, true) }); len(msgs) != 0 {
		t.Fatalf("failures: %q", msgs)
	}
	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, out} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from the committed %s (see TestGolden's report)", filepath.Base(p), goldenFile)
		}
	}
}

// AT-DET-17: FAULTLINE_GOLDEN_OUT receives the same content as testdata/hashes.txt.
func TestGoldenOut(t *testing.T) {
	if *update {
		t.Skip("compares with testdata/hashes.txt, which -update rewrites")
	}
	out := filepath.Join(t.TempDir(), "g.txt")
	t.Setenv("FAULTLINE_GOLDEN_OUT", out)
	TestGolden(t)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("FAULTLINE_GOLDEN_OUT content differs from %s (see TestGolden's report above)", goldenFile)
	}
}
