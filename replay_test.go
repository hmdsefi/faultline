package faultline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// AT-API-30
func TestBuildReplay(t *testing.T) {
	cases := []struct {
		importPath, modulePath, cwd, test string
		seed                              uint64
		schedule                          string
		command, pkgArg, dir, run         string
	}{
		{"github.com/acme/kv", "github.com/acme", "/src/acme/kv", "TestKV", 0x5e1f9a2c4b7d3e80, "",
			"FAULTLINE_SEED=0x5e1f9a2c4b7d3e80 go test -run '^TestKV$' github.com/acme/kv", "./kv", "/src/acme", "^TestKV$"},
		{"github.com/acme", "github.com/acme", "/src/acme", "TestKV/raft_3_nodes", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestKV$/^raft_3_nodes$' github.com/acme", ".", "/src/acme", "^TestKV$/^raft_3_nodes$"},
		{"command-line-arguments", "", "/x", "TestA", 1, "/tmp/my sched.json",
			"FAULTLINE_SEED=0x0000000000000001 FAULTLINE_SCHEDULE='/tmp/my sched.json' go test -run '^TestA$' .", ".", "/x", "^TestA$"},
		{"github.com/acme", "github.com/acme", "/src/acme", "Test/a.b(c)", 1, "",
			`FAULTLINE_SEED=0x0000000000000001 go test -run '^Test$/^a\.b\(c\)$' github.com/acme`, ".", "/src/acme", `^Test$/^a\.b\(c\)$`},
		{"github.com/acme", "github.com/acme", "/src/acme", "Test/it's", 1, "",
			`FAULTLINE_SEED=0x0000000000000001 go test -run '^Test$/^it'\''s$' github.com/acme`, ".", "/src/acme", `^Test$/^it's$`},
		{"github.com/acme/kv", "github.com/acme", "/elsewhere", "TestKV", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestKV$' github.com/acme/kv", "./kv", "", "^TestKV$"},
		// One pattern element per name element, at every level.
		{"github.com/acme", "github.com/acme", "/src/acme", "TestKV/raft/3", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestKV$/^raft$/^3$' github.com/acme", ".", "/src/acme", "^TestKV$/^raft$/^3$"},
		// cwd ends in "kv" but not in "/kv": no dir.
		{"github.com/acme/kv", "github.com/acme", "/src/acme/xkv", "TestKV", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestKV$' github.com/acme/kv", "./kv", "", "^TestKV$"},
		// A package argument outside shellQuote's safe set is quoted.
		{"github.com/acme/a~b", "github.com/acme", "/src/acme/a~b", "TestA", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestA$' 'github.com/acme/a~b'", "./a~b", "/src/acme", "^TestA$"},
		// An import path that only shares the module path's prefix is not in the module.
		{"github.com/acmekv", "github.com/acme", "/m/acmekv", "TestA", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestA$' .", ".", "/m/acmekv", "^TestA$"},
		// A schedule path made of the safe set is not quoted.
		{"github.com/acme", "github.com/acme", "/src/acme", "TestA", 1, "/tmp/a_b@c%d+e=f:g,h-1.json",
			"FAULTLINE_SEED=0x0000000000000001 FAULTLINE_SCHEDULE=/tmp/a_b@c%d+e=f:g,h-1.json go test -run '^TestA$' github.com/acme", ".", "/src/acme", "^TestA$"},
		// A package two directories below the module root.
		{"github.com/acme/internal/raft", "github.com/acme", "/src/acme/internal/raft", "TestRaft", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestRaft$' github.com/acme/internal/raft", "./internal/raft", "/src/acme", "^TestRaft$"},
		// cwd ends in the last element of rel only: no dir.
		{"github.com/acme/internal/raft", "github.com/acme", "/src/acme/other/raft", "TestRaft", 1, "",
			"FAULTLINE_SEED=0x0000000000000001 go test -run '^TestRaft$' github.com/acme/internal/raft", "./internal/raft", "", "^TestRaft$"},
	}
	for _, c := range cases {
		got := buildReplay(buildInfo{importPath: c.importPath, modulePath: c.modulePath}, c.cwd, c.test, c.seed, c.schedule)
		if got.Command != c.command || got.PackageArg != c.pkgArg || got.Dir != c.dir || got.Run != c.run || got.PackageDir != c.cwd {
			t.Errorf("buildReplay(%q, %q, %q, %q, %q) = %+v\nwant command %s, package_arg %q, dir %q", c.importPath, c.modulePath, c.cwd, c.test, c.schedule, got, c.command, c.pkgArg, c.dir)
		}
		// env holds exactly the variables of the command.
		env := map[string]string{"FAULTLINE_SEED": fmt.Sprintf("0x%016x", c.seed)}
		if c.schedule != "" {
			env["FAULTLINE_SCHEDULE"] = c.schedule
		}
		if !maps.Equal(got.Env, env) {
			t.Errorf("env = %v, want %v", got.Env, env)
		}
	}
	// The failing binary's -tags and -race go after "go test", so the replay builds the same test
	// binary; the tags are quoted like a path.
	for _, c := range []struct {
		tags    string
		race    bool
		command string
	}{
		{"sim,x", true, "FAULTLINE_SEED=0x0000000000000001 go test -tags sim,x -race -run '^TestKV$' github.com/acme/kv"},
		{"sim", false, "FAULTLINE_SEED=0x0000000000000001 go test -tags sim -run '^TestKV$' github.com/acme/kv"},
		{"", true, "FAULTLINE_SEED=0x0000000000000001 go test -race -run '^TestKV$' github.com/acme/kv"},
		{"a b", false, "FAULTLINE_SEED=0x0000000000000001 go test -tags 'a b' -run '^TestKV$' github.com/acme/kv"},
	} {
		b := buildInfo{importPath: "github.com/acme/kv", modulePath: "github.com/acme", tags: c.tags, race: c.race}
		if got := buildReplay(b, "/src/acme/kv", "TestKV", 1, ""); got.Command != c.command || got.PackageArg != "./kv" || got.Dir != "/src/acme" {
			t.Errorf("buildReplay(tags %q, race %v) = %+v\nwant command %s", c.tags, c.race, got, c.command)
		}
	}
}

// API-080: buildInfoOf reads the import path, the module path and the -tags and -race build
// settings.
func TestBuildInfoOf(t *testing.T) {
	bi := &debug.BuildInfo{Path: "github.com/acme/kv.test", Main: debug.Module{Path: "github.com/acme"}, Settings: []debug.BuildSetting{
		{Key: "-buildmode", Value: "exe"}, {Key: "-compiler", Value: "gc"}, {Key: "-race", Value: "true"}, {Key: "-tags", Value: "sim,x"}, {Key: "GOOS", Value: "linux"},
	}}
	want := buildInfo{importPath: "github.com/acme/kv", modulePath: "github.com/acme", faultline: "unknown", tags: "sim,x", race: true}
	if got := buildInfoOf(bi); got != want {
		t.Errorf("buildInfoOf(-tags sim,x -race) = %+v, want %+v", got, want)
	}
	bi.Settings = []debug.BuildSetting{{Key: "-buildmode", Value: "exe"}, {Key: "-race", Value: "false"}}
	want.tags, want.race = "", false
	if got := buildInfoOf(bi); got != want {
		t.Errorf("buildInfoOf(no -tags, -race=false) = %+v, want %+v", got, want)
	}
}

func TestVersionOf(t *testing.T) {
	main := &debug.BuildInfo{Main: debug.Module{Path: "github.com/hmdsefi/faultline", Version: "(devel)"}}
	if got := versionOf(main); got != "(devel)" {
		t.Errorf("main module: %q", got)
	}
	main.Main.Version = "v0.2.0"
	if got := versionOf(main); got != "v0.2.0" {
		t.Errorf("main module with a version: %q", got)
	}
	dep := &debug.BuildInfo{Main: debug.Module{Path: "github.com/acme"}, Deps: []*debug.Module{
		{Path: "github.com/hmdsefi/faultline", Version: "v0.1.0", Replace: &debug.Module{Path: "../faultline", Version: "(devel)"}},
	}}
	if got := versionOf(dep); got != "v0.1.0 => ../faultline" {
		t.Errorf("replaced dep: %q", got)
	}
	dep.Deps[0].Replace = &debug.Module{Path: "../faultline"}
	if got := versionOf(dep); got != "v0.1.0 => ../faultline" {
		t.Errorf("replaced dep without a version: %q", got)
	}
	dep.Deps[0].Replace = &debug.Module{Path: "example.com/fork", Version: "v0.1.1"}
	if got := versionOf(dep); got != "v0.1.0 => example.com/fork@v0.1.1" {
		t.Errorf("replaced dep with version: %q", got)
	}
	// A tagged release or a pseudo-version, listed after other modules, with no replacement.
	tagged := &debug.BuildInfo{Main: debug.Module{Path: "github.com/acme"}, Deps: []*debug.Module{
		{Path: "github.com/google/go-cmp", Version: "v0.7.0"},
		{Path: faultlineModule, Version: "v0.3.0"},
		{Path: "github.com/hmdsefi/gograph", Version: "v0.8.2"},
	}}
	if got := versionOf(tagged); got != "v0.3.0" {
		t.Errorf("tagged dep: %q", got)
	}
	tagged.Deps[1].Version = "v0.3.1-0.20261009120000-6340e5e1a2b3"
	if got := versionOf(tagged); got != "v0.3.1-0.20261009120000-6340e5e1a2b3" {
		t.Errorf("pseudo-version dep: %q", got)
	}
	if got := versionOf(&debug.BuildInfo{Main: debug.Module{Path: "x"}}); got != "unknown" {
		t.Errorf("missing: %q", got)
	}
	if goMinor("go1.26.2") != "go1.26" || goMinor("devel +abc") != "devel +abc" || goMinor("devel go1.27-4fbc9a0 Mon Oct 5 2026 go1.26") != "go1.27" {
		t.Error("goMinor")
	}
}

// API-080, API-081: the build info and versions of this test binary.
func TestReadBuildAndVersions(t *testing.T) {
	b := readBuild()
	if b.importPath != faultlineModule || b.modulePath != faultlineModule || b.faultline == "unknown" || readBuild() != b {
		t.Fatalf("readBuild() = %+v", b)
	}
	if bi, ok := debug.ReadBuildInfo(); !ok || buildInfoOf(bi) != b {
		t.Fatalf("readBuild() = %+v, want buildInfoOf(debug.ReadBuildInfo())", b)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	v := versions(b)
	if v.Faultline != b.faultline || v.Go != runtime.Version() || v.GOOS != runtime.GOOS || v.GOARCH != runtime.GOARCH || v.TestBinarySHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("versions(%+v) = %+v", b, v)
	}
	if got := versions(buildInfo{faultline: "v9.9.9"}).Faultline; got != "v9.9.9" {
		t.Errorf("versions(faultline v9.9.9).Faultline = %q", got)
	}
	// Both are computed once per process: later calls return the cached values.
	// Not parallel: swaps the values readBuild and testBinarySHA256 return.
	cachedBuild, cachedSHA := buildCached, binSHA
	t.Cleanup(func() { buildCached, binSHA = cachedBuild, cachedSHA })
	buildCached.importPath, binSHA = "cached", "cached"
	if readBuild().importPath != "cached" || testBinarySHA256() != "cached" {
		t.Error("build info or binary hash computed again")
	}
}

// API-082: the spec's examples.
func TestOptionsHash(t *testing.T) {
	if got := fmt.Sprintf("0x%016x", NameBase(`{"duration_ns":60000000000}`)); got != "0x212556c926f94193" {
		t.Fatalf("NameBase example = %s", got)
	}
	h := hashOptions(hashedOptions{
		DurationNS: 60000000000, MaxEvents: 10000000, Mode: "event",
		Net:  json.RawMessage(`{"Default":{"Latency":1000000,"Jitter":4000000,"TailPPM":0,"Tail":0,"DropPPM":0,"DupPPM":0,"FIFO":false},"NoDeliveryCheck":false}`),
		Disk: json.RawMessage(`{"Crash":0,"Metadata":0,"FailedSync":0,"SectorSize":0,"Capacity":0,"Latency":{"Read":{"Base":0,"Jitter":0},"Write":{"Base":0,"Jitter":0},"Sync":{"Base":0,"Jitter":0},"Meta":{"Base":0,"Jitter":0},"SlowPPM":0,"Slow":0}}`),
	})
	if h != "0x752550e68a36df2e" {
		t.Fatalf("ART §5.3 example options_hash = %s, want 0x752550e68a36df2e", h)
	}
	if got := optionsHash(applyDefaults(Options{}), ""); got != "0x752550e68a36df2e" {
		t.Fatalf("optionsHash(defaults) = %s; json net %s disk %s", got, mustJSON(applyDefaults(Options{}).Net), mustJSON(applyDefaults(Options{}).Disk))
	}
	// 0x%016x keeps leading zeros.
	if got := hashOptions(hashedOptions{DurationNS: 14}); got != "0x00b00039ec84200f" {
		t.Errorf("hashOptions(duration 14) = %s, want 0x00b00039ec84200f", got)
	}
	// Every hashed value is the effective one, and a changed schedule hash changes the hash.
	for _, o := range []Options{
		{Duration: 2 * time.Second, MaxEvents: 7, NoCryptoSeed: true, Net: simnet.Config{Default: simnet.Link{Latency: 5}}, Disk: simdisk.Config{SectorSize: 4096}},
		{AllowLimit: true},
	} {
		o = applyDefaults(o)
		want := hashOptions(hashedOptions{
			DurationNS: int64(o.Duration), MaxEvents: o.MaxEvents, Mode: "event", Net: mustJSON(o.Net), Disk: mustJSON(o.Disk),
			NoCryptoSeed: o.NoCryptoSeed, AllowLimit: o.AllowLimit, ScheduleHash: "0x0000000000000001",
		})
		if got := optionsHash(o, "0x0000000000000001"); got != want || got == optionsHash(o, "") {
			t.Errorf("optionsHash(%+v) = %s, want %s and a different value without the schedule", o, got, want)
		}
	}
	ro := runOptions(applyDefaults(Options{Seeds: 1}), &environment{})
	if ro.Duration != "1m0s" || ro.Trace != "hash" || ro.Mode != "event" || ro.MaxEvents != 10000000 {
		t.Fatalf("runOptions = %+v", ro)
	}
	// ART-021: report.options is the effective options, field by field.
	o := applyDefaults(Options{
		Seeds: 3, Duration: 1500 * time.Millisecond, MaxEvents: 9,
		Trace:            kernel.TraceConfig{Level: kernel.TraceFull, Buffer: 100},
		CheckDeterminism: true, KeepGoing: true, NoCryptoSeed: true, AllowLimit: true,
		Disk: simdisk.Config{SectorSize: 4096},
	})
	want := artifact.RunOptions{
		Seeds: 3, DurationNS: 1500000000, Duration: "1.5s", MaxEvents: 9, Mode: "event", Trace: "full",
		TraceBuffer: 100, CheckDeterminism: true, KeepGoing: true, NoCryptoSeed: true, AllowLimit: true,
		Schedule: "/s.json", ScheduleHash: "0x0000000000000002",
		Net:  json.RawMessage(`{"Default":{"Latency":1000000,"Jitter":4000000,"TailPPM":0,"Tail":0,"DropPPM":0,"DupPPM":0,"FIFO":false},"NoDeliveryCheck":false}`),
		Disk: mustJSON(simdisk.Config{SectorSize: 4096}),
	}
	if got := runOptions(o, &environment{schedulePath: "/s.json", scheduleHash: "0x0000000000000002"}); !reflect.DeepEqual(got, want) {
		t.Errorf("runOptions =\n%+v\nwant\n%+v", got, want)
	}
}
