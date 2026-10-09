package faultline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

// faultlineModule is the module path of faultline itself (API-081).
const faultlineModule = "github.com/hmdsefi/faultline"

// buildInfo is what Run needs from runtime/debug.ReadBuildInfo (API-080, API-081).
type buildInfo struct {
	importPath string // test package import path; "unknown" if build info is unavailable
	modulePath string // main module path; "" for go test file_test.go
	faultline  string // versions.faultline
	tags       string // the binary's -tags build setting (comma-separated); "" without -tags
	race       bool   // the binary was built with -race
}

var (
	buildOnce   sync.Once
	buildCached buildInfo
	binOnce     sync.Once
	binSHA      string
)

// readBuild returns the build info of the test binary, read once per process.
func readBuild() buildInfo {
	buildOnce.Do(func() {
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			buildCached = buildInfo{importPath: "unknown", faultline: "unknown"}
			return
		}
		buildCached = buildInfoOf(bi)
	})
	return buildCached
}

// buildInfoOf returns the buildInfo of a binary's build info (API-080, API-081). Go records the
// -tags (comma-separated) and -race ("true") build settings only when they were given.
func buildInfoOf(bi *debug.BuildInfo) buildInfo {
	b := buildInfo{
		importPath: strings.TrimSuffix(bi.Path, ".test"),
		modulePath: bi.Main.Path,
		faultline:  versionOf(bi),
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "-tags":
			b.tags = s.Value
		case "-race":
			b.race = s.Value == "true"
		}
	}
	return b
}

// versionOf implements versions.faultline (API-081).
func versionOf(bi *debug.BuildInfo) string {
	if bi.Main.Path == faultlineModule {
		return bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path != faultlineModule {
			continue
		}
		v := d.Version
		if d.Replace != nil {
			v += " => " + d.Replace.Path
			if rv := d.Replace.Version; rv != "" && rv != "(devel)" {
				v += "@" + rv
			}
		}
		return v
	}
	return "unknown"
}

// testBinarySHA256 returns the lowercase hex SHA-256 of the running binary, computed once per
// process, or "" on error (API-081).
func testBinarySHA256() string {
	binOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			return
		}
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return
		}
		binSHA = hex.EncodeToString(h.Sum(nil))
	})
	return binSHA
}

// versions returns report.versions (API-081).
func versions(b buildInfo) artifact.Versions {
	return artifact.Versions{
		Faultline:        b.faultline,
		Go:               runtime.Version(),
		GOOS:             runtime.GOOS,
		GOARCH:           runtime.GOARCH,
		TestBinarySHA256: testBinarySHA256(),
	}
}

var goMinorRe = regexp.MustCompile(`go1\.[0-9]+`)

// goMinor returns the Go minor version of v ("go1.26"), or v itself (API-083).
func goMinor(v string) string {
	if m := goMinorRe.FindString(v); m != "" {
		return m
	}
	return v
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func singleQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return singleQuote(s)
}

// runPattern returns the -run pattern that selects exactly the test named name (API-080).
func runPattern(name string) string {
	elems := strings.Split(name, "/")
	for i, e := range elems {
		elems[i] = "^" + regexp.QuoteMeta(e) + "$"
	}
	return strings.Join(elems, "/")
}

// buildReplay returns report.replay for one seed (API-080). b is the test binary's build info;
// schedulePath is the absolute FAULTLINE_SCHEDULE path, or "". The command names a package of
// the module by its import path, so it works from any directory in the module.
func buildReplay(b buildInfo, cwd, testName string, seed uint64, schedulePath string) artifact.Replay {
	importPath, modulePath := b.importPath, b.modulePath
	pkg := "." // the package argument of command
	var pkgArg, dir string
	switch {
	case modulePath != "" && importPath == modulePath:
		pkgArg, dir, pkg = ".", cwd, importPath
	case modulePath != "" && strings.HasPrefix(importPath, modulePath+"/"):
		rel := importPath[len(modulePath)+1:]
		pkgArg, pkg = "./"+rel, importPath
		if strings.HasSuffix(filepath.ToSlash(cwd), "/"+rel) {
			dir = cwd[:len(cwd)-len(rel)-1]
		}
	default:
		pkgArg, dir = ".", cwd
	}
	run := runPattern(testName)
	seedText := fmt.Sprintf("0x%016x", seed)
	env := map[string]string{"FAULTLINE_SEED": seedText}
	command := "FAULTLINE_SEED=" + seedText
	if schedulePath != "" {
		env["FAULTLINE_SCHEDULE"] = schedulePath
		command += " FAULTLINE_SCHEDULE=" + shellQuote(schedulePath)
	}
	command += " go test"
	if b.tags != "" {
		command += " -tags " + shellQuote(b.tags)
	}
	if b.race {
		command += " -race"
	}
	command += " -run " + singleQuote(run) + " " + shellQuote(pkg)
	return artifact.Replay{Command: command, Env: env, Run: run, PackageArg: pkgArg, Dir: dir, PackageDir: cwd}
}

// hashedOptions is the input of options_hash (API-082). Later-phase fields are omitted when
// zero, so Phase 1 hashes do not change while those fields are zero.
type hashedOptions struct {
	DurationNS   int64           `json:"duration_ns"`
	MaxEvents    uint64          `json:"max_events"`
	Mode         string          `json:"mode"`
	Net          json.RawMessage `json:"net"`
	Disk         json.RawMessage `json:"disk"`
	NoCryptoSeed bool            `json:"no_crypto_seed"`
	AllowLimit   bool            `json:"allow_limit"`
	ScheduleHash string          `json:"schedule_hash"`

	Procs      int             `json:"procs,omitempty"`
	DrainNS    int64           `json:"drain_ns,omitempty"`
	FailOnLeak bool            `json:"fail_on_leak,omitempty"`
	NetSim     json.RawMessage `json:"netsim,omitempty"`
	Swarm      bool            `json:"swarm,omitempty"`
}

// hashOptions returns NameBase of the JSON of h, formatted 0x%016x (API-082).
func hashOptions(h hashedOptions) string {
	b, err := json.Marshal(h)
	if err != nil {
		panic(fmt.Sprintf("faultline: options hash: %v", err))
	}
	return fmt.Sprintf("0x%016x", NameBase(string(b)))
}

// mustJSON returns the encoding/json encoding of v.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("faultline: encoding options: %v", err))
	}
	return b
}

// optionsHash returns options_hash for the effective options o (API-082).
func optionsHash(o Options, scheduleHash string) string {
	return hashOptions(hashedOptions{
		DurationNS:   int64(o.Duration),
		MaxEvents:    o.MaxEvents,
		Mode:         o.Mode.String(),
		Net:          mustJSON(o.Net),
		Disk:         mustJSON(o.Disk),
		NoCryptoSeed: o.NoCryptoSeed,
		AllowLimit:   o.AllowLimit,
		ScheduleHash: scheduleHash,
	})
}

// runOptions returns report.options for the effective options o (ART-021).
func runOptions(o Options, env *environment) artifact.RunOptions {
	trace := "hash"
	if o.Trace.Level == kernel.TraceFull {
		trace = "full"
	}
	return artifact.RunOptions{
		Seeds:            o.Seeds,
		DurationNS:       int64(o.Duration),
		Duration:         o.Duration.String(),
		MaxEvents:        o.MaxEvents,
		Mode:             o.Mode.String(),
		Trace:            trace,
		TraceBuffer:      o.Trace.Buffer,
		CheckDeterminism: o.CheckDeterminism,
		KeepGoing:        o.KeepGoing,
		NoCryptoSeed:     o.NoCryptoSeed,
		AllowLimit:       o.AllowLimit,
		Schedule:         env.schedulePath,
		ScheduleHash:     env.scheduleHash,
		Net:              mustJSON(o.Net),
		Disk:             mustJSON(o.Disk),
	}
}
