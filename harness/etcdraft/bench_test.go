package etcdraft

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
)

func init() { //nolint:gochecknoinits // registers the child-process scenario
	// "bench-seeds": ETCDRAFT_NODES (3 or 5) nodes, DefaultConfig, FaultsDefault, 60 s seeds;
	// the seed count comes from FAULTLINE_SEEDS.
	scenarios["bench-seeds"] = func(t *testing.T) {
		nodes, err := strconv.Atoi(os.Getenv("ETCDRAFT_NODES"))
		if err != nil || (nodes != 3 && nodes != 5) {
			t.Fatalf("ETCDRAFT_NODES=%q: want 3 or 5", os.Getenv("ETCDRAFT_NODES"))
		}
		cfg := DefaultConfig()
		cfg.Nodes = nodes
		Test(t, cfg, faultline.Options{Duration: 60 * time.Second})
	}
}

// benchSeeds runs b.N full seeds in a child process, because faultline.Run needs a
// *testing.T, and reports the mean per-seed wall time from FAULTLINE_RESULTS as ns/op
// (AT-ETC-30). With ETCDRAFT_BENCH_MAX_NS set, a larger mean fails the benchmark, and a
// value that is not a positive whole number of nanoseconds fails it too (a typo must not
// turn the gate off). The limit applies only to the measured run, where b.N > 1: Go first
// runs the benchmark once with b.N == 1 to size the run, and one seed alone is not the
// b.N-seed mean that ETC-198's limit comes from. So `-benchtime 1x` gates nothing.
func benchSeeds(b *testing.B, nodes int) {
	var limit int64 // 0: no limit
	if v := os.Getenv("ETCDRAFT_BENCH_MAX_NS"); v != "" {
		var err error
		if limit, err = strconv.ParseInt(v, 10, 64); err != nil || limit <= 0 {
			b.Fatalf("ETCDRAFT_BENCH_MAX_NS=%q: want a positive whole number of nanoseconds, such as 300000000", v)
		}
	}
	results := resultsFile(b)
	out, code := runScenario(b, "bench-seeds", []string{
		"ETCDRAFT_NODES=" + strconv.Itoa(nodes),
		"FAULTLINE_SEEDS=" + strconv.Itoa(b.N),
		"FAULTLINE_RESULTS=" + results,
		"FAULTLINE_ARTIFACTS=off",
	}, "-test.timeout=0")
	if code != 0 {
		b.Fatalf("seeds failed (exit %d):\n%s", code, tail(out))
	}
	rs := readResults(b, results)
	if len(rs) != b.N {
		b.Fatalf("%d results for %d seeds", len(rs), b.N)
	}
	var total int64
	for _, r := range rs {
		total += r.WallNS
	}
	mean := total / int64(b.N)
	b.ReportMetric(float64(mean), "ns/op")
	if limit > 0 && b.N > 1 && mean > limit {
		b.Fatalf("mean wall time per seed %v exceeds %v", time.Duration(mean), time.Duration(limit))
	}
}

func BenchmarkSeed3Nodes60s(b *testing.B) { benchSeeds(b, 3) }

func BenchmarkSeed5Nodes60s(b *testing.B) { benchSeeds(b, 5) }
