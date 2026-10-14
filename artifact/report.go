// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
)

// Report is the content of report.json: what ran, how it ended, and how to replay it.
type Report struct {
	Version     int        `json:"faultline_report"` // ReportVersion
	Status      string     `json:"status"`           // "fail" or "pass"
	Package     string     `json:"package"`
	Test        string     `json:"test"`
	Subtest     string     `json:"subtest"`
	Seed        string     `json:"seed"`
	SeedSource  string     `json:"seed_source"`           // "derived", "env", "list", "code"
	BaseSeed    string     `json:"base_seed,omitempty"`   // derived only
	BaseSource  string     `json:"base_source,omitempty"` // "test_name", "options", "env", "explore"
	SeedIndex   int        `json:"seed_index"`
	Failure     *Failure   `json:"failure,omitempty"` // nil when Status is "pass"
	Warnings    []string   `json:"warnings,omitempty"`
	Replay      Replay     `json:"replay"`
	Versions    Versions   `json:"versions"`
	Options     RunOptions `json:"options"`
	OptionsHash string     `json:"options_hash"`
	Run         RunInfo    `json:"run"`
	Nodes       []Node     `json:"nodes"`

	// Reserved for goroutine mode, coverage points and swarm testing. faultline.Run does not set
	// them, and they are omitted when empty.
	DeterminismLevel string          `json:"determinism_level,omitempty"` // "exact" or "best-effort"
	ExactBackend     string          `json:"exact_backend,omitempty"`     // "wasip1" or "toolexec"; absent: none
	Leaks            *Leaks          `json:"leaks,omitempty"`             // goroutines alive after the run
	Asserts          json.RawMessage `json:"asserts,omitempty"`           // [{name, kind, calls, true}] by name; [] when none
	Swarm            json.RawMessage `json:"swarm,omitempty"`             // the swarm config as JSON, or null when swarm is off

	Dir   string `json:"dir"`   // set by Write
	Files []File `json:"files"` // set by Write
}

// Failure describes why a seed failed. Kind says how: invariant or final for a check that
// returned an error, panic, fail for a call to Sim.Fail, limit, or determinism.
type Failure struct {
	Kind        string         `json:"kind"`  // invariant, final, panic, fail, limit, determinism, leak
	Check       string         `json:"check"` // may be ""
	Signature   string         `json:"signature"`
	Headline    string         `json:"headline"`
	Message     string         `json:"message"`
	AtNS        int64          `json:"at_ns"`
	At          string         `json:"at"`
	Node        string         `json:"node,omitempty"`
	NodeID      int32          `json:"node_id,omitempty"`
	Event       uint64         `json:"event,omitempty"`
	RecordSeq   uint64         `json:"record_seq,omitempty"`
	Panic       *Panic         `json:"panic,omitempty"`
	Limit       *Limit         `json:"limit,omitempty"`
	Finals      []FinalFailure `json:"finals,omitempty"`
	Determinism *Determinism   `json:"determinism,omitempty"`
}

// Panic describes a panic failure.
type Panic struct {
	Value string `json:"value"`          // panic text: a string as is, an error's Error(), else kernel.Describe
	In    string `json:"in"`             // "callback", "invariant", "final", "body"
	Name  string `json:"name,omitempty"` // invariant or final name
	Site  string `json:"site"`           // function that panicked, for example "main.(*Store).apply"; equal to Failure.Check
	Stack string `json:"stack"`
}

// Limit describes a limit failure.
type Limit struct {
	Name  string `json:"name"` // "MaxEvents" or "MaxTime"
	Value uint64 `json:"value"`
}

// FinalFailure is one failing final check.
type FinalFailure struct {
	Check   string `json:"check"`
	Message string `json:"message"`
	Panic   *Panic `json:"panic,omitempty"`
}

// Determinism describes a determinism failure.
type Determinism struct {
	Context  string      `json:"context"` // "artifact_rerun" or "check_determinism"
	Hashes   []string    `json:"hashes"`  // one per attempt, in order
	Original *Failure    `json:"original,omitempty"`
	Diff     *RecordDiff `json:"diff,omitempty"` // nil: the two full-trace attempts were identical
}

// RecordDiff is the first difference between two record lists.
type RecordDiff struct {
	Index int          `json:"index"`       // 0-based position in the record lists
	A     *TraceRecord `json:"a,omitempty"` // nil: list A ended
	B     *TraceRecord `json:"b,omitempty"` // nil: list B ended
}

// Replay is the command that replays the seed, and its parts.
type Replay struct {
	Command    string            `json:"command"`     // a shell command line
	Env        map[string]string `json:"env"`         // the variables Command sets; encoded with sorted keys
	Run        string            `json:"run"`         // the -run pattern that selects the test, or the seed's subtest
	PackageArg string            `json:"package_arg"` // the package relative to Dir: "." or "./<path>"
	// Dir is the directory PackageArg is relative to: the module root, or for a package outside a
	// module, the package directory; "" if unknown. Command names a module's package by import
	// path, so it works from any directory of the module; outside a module it runs in Dir.
	Dir        string `json:"dir,omitempty"`
	PackageDir string `json:"package_dir"` // the package directory, where the test ran
}

// Versions identify what produced the artifact.
type Versions struct {
	Faultline        string `json:"faultline"`
	Go               string `json:"go"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	TestBinarySHA256 string `json:"test_binary_sha256,omitempty"`
}

// RunOptions are the effective options of the run.
type RunOptions struct {
	Seeds            int             `json:"seeds"`
	DurationNS       int64           `json:"duration_ns"`
	Duration         string          `json:"duration"` // time.Duration.String()
	MaxEvents        uint64          `json:"max_events"`
	Mode             string          `json:"mode"`  // "event", "goroutine"
	Trace            string          `json:"trace"` // "hash", "full"
	TraceBuffer      int             `json:"trace_buffer"`
	CheckDeterminism bool            `json:"check_determinism"`
	KeepGoing        bool            `json:"keep_going"`
	NoCryptoSeed     bool            `json:"no_crypto_seed"`
	AllowLimit       bool            `json:"allow_limit"`
	Schedule         string          `json:"schedule,omitempty"`      // absolute path
	ScheduleHash     string          `json:"schedule_hash,omitempty"` // 0x%016x
	Net              json.RawMessage `json:"net"`                     // encoding/json of simnet.Config
	Disk             json.RawMessage `json:"disk"`                    // encoding/json of simdisk.Config

	// Reserved for goroutine mode and swarm testing; omitted in event mode.
	Procs          int             `json:"procs,omitempty"`
	DrainNS        int64           `json:"drain_ns,omitempty"`
	StallTimeoutNS int64           `json:"stall_timeout_ns,omitempty"` // negative: watchdog off
	FailOnLeak     bool            `json:"fail_on_leak,omitempty"`
	NetSim         json.RawMessage `json:"netsim,omitempty"` // the goroutine-mode network config as JSON
	Swarm          bool            `json:"swarm,omitempty"`  // whether swarm testing was on
}

// Leaks lists the goroutines still alive after a goroutine-mode run.
type Leaks struct {
	Groups       []LeakGroup `json:"groups"`       // ascending (node ID, incarnation)
	Unattributed int         `json:"unattributed"` // goroutines whose labels code under test replaced
}

// LeakGroup is the leaked goroutines of one node incarnation.
type LeakGroup struct {
	Node    string       `json:"node"` // node name; "" for goroutines of body or checks (node 0)
	Inc     uint32       `json:"inc"`
	Status  string       `json:"status"` // "crashed at t=…", "exited at t=…", "running at the end", "world"
	Count   int          `json:"count"`
	Records []LeakRecord `json:"records"` // sorted by frame text
}

// LeakRecord is a group of goroutines with the same stack.
type LeakRecord struct {
	Count  int      `json:"count"`
	Frames []string `json:"frames"` // "function (file:line)", innermost first
}

// RunInfo describes the run whose trace the artifact holds. A seed can run more than once, an
// attempt each time. A failing seed runs again with a full trace to write its artifact, and the
// determinism check runs a seed twice.
type RunInfo struct {
	TraceHash  string   `json:"trace_hash"`
	Events     uint64   `json:"events"`
	Records    uint64   `json:"records"` // records emitted (last Seq)
	Dropped    uint64   `json:"dropped"` // records not in trace.jsonl
	EndNS      int64    `json:"end_ns"`
	End        string   `json:"end"`
	Stop       string   `json:"stop"` // StopReason.String() or "none"
	RecoveryNS int64    `json:"recovery_ns,omitempty"`
	Recovery   string   `json:"recovery,omitempty"`
	Planners   []string `json:"planners"` // never null
	Attempts   int      `json:"attempts"` // runs of the seed, this one included
}

// File is one entry of the file index.
type File struct {
	Name    string `json:"name"`
	Type    string `json:"type"`              // report_text, trace, schedule, history, extra, timeline_text, hb, timeline_html
	Version int    `json:"version,omitempty"` // format version, when the format has one
}

// normReport returns a deep copy of r for writing (ART-011, ART-020): the version set, every
// string made valid UTF-8, and nil Nodes, Tags, Planners and Files replaced by empty slices. It
// fails on a pointer cycle (ART-010, ART-020).
func normReport(r *Report) (Report, error) {
	c, err := validCopy(reflect.ValueOf(*r), nil)
	if err != nil {
		return Report{}, err
	}
	var rr Report
	reflect.ValueOf(&rr).Elem().Set(c)
	rr.Version = ReportVersion
	rr.Nodes = normNodes(rr.Nodes)
	if rr.Run.Planners == nil {
		rr.Run.Planners = []string{}
	}
	if rr.Files == nil {
		rr.Files = []File{}
	}
	return rr, nil
}

// errCycle is validCopy's error for a pointer cycle.
var errCycle = errors.New("artifact: report has a pointer cycle")

// ptrKey is a pointer on validCopy's path: its address and its type, since a pointer to a struct
// and one to its first field share the address.
type ptrKey struct {
	addr uintptr
	typ  reflect.Type
}

// validCopy returns a deep copy of v in which every string is valid UTF-8 (validUTF8). Byte
// slices (json.RawMessage) are shared as they are; nil pointers, slices and maps stay nil. path
// holds the pointers on the way to v (nil: none). A pointer that leads back to one of them is a
// cycle, which validCopy reports as errCycle instead of following it until the stack runs out
// (ART-020); a pointer reached by two paths is shared, not a cycle, and is copied twice. It
// handles only what the report and trace header types hold, which TestReportKinds checks:
// strings, bools, integers, pointers, arrays, slices, maps with string keys and structs with
// exported fields only, and no interfaces.
func validCopy(v reflect.Value, path map[ptrKey]bool) (reflect.Value, error) {
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(validUTF8(v.String())).Convert(v.Type()), nil
	case reflect.Pointer:
		if v.IsNil() {
			return v, nil
		}
		k := ptrKey{v.Pointer(), v.Type()}
		if path[k] {
			return reflect.Value{}, errCycle
		}
		if path == nil {
			path = map[ptrKey]bool{}
		}
		path[k] = true
		defer delete(path, k)
		e, err := validCopy(v.Elem(), path)
		if err != nil {
			return reflect.Value{}, err
		}
		p := reflect.New(v.Type().Elem())
		p.Elem().Set(e)
		return p, nil
	case reflect.Struct:
		c := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			f, err := validCopy(v.Field(i), path)
			if err != nil {
				return reflect.Value{}, err
			}
			c.Field(i).Set(f)
		}
		return c, nil
	case reflect.Slice:
		if v.IsNil() || v.Type().Elem().Kind() == reflect.Uint8 {
			return v, nil
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			e, err := validCopy(v.Index(i), path)
			if err != nil {
				return reflect.Value{}, err
			}
			c.Index(i).Set(e)
		}
		return c, nil
	case reflect.Array:
		c := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			e, err := validCopy(v.Index(i), path)
			if err != nil {
				return reflect.Value{}, err
			}
			c.Index(i).Set(e)
		}
		return c, nil
	case reflect.Map:
		if v.IsNil() {
			return v, nil
		}
		keys := v.MapKeys() //faultline:maporder sorted next, so keys that become equal resolve the same way every run
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		c := reflect.MakeMapWithSize(v.Type(), len(keys))
		for _, k := range keys {
			e, err := validCopy(v.MapIndex(k), path)
			if err != nil {
				return reflect.Value{}, err
			}
			ck, _ := validCopy(k, path) // a string key holds no pointer
			c.SetMapIndex(ck, e)
		}
		return c, nil
	}
	return v, nil // numbers and bools
}

// WriteReport writes r as report.json: indented JSON, with every string made valid UTF-8.
func WriteReport(w io.Writer, r *Report) error {
	if r == nil {
		return errors.New("artifact: report is nil")
	}
	rr, err := normReport(r)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(&rr)
}

// ReadReport reads and version-checks report.json. It rejects a file that is not a faultline
// report or has a newer version than this package writes. Unknown fields are ignored, and the
// error for a field that encoding/json cannot decode wraps encoding/json's.
func ReadReport(r io.Reader) (*Report, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("artifact: report.json: %w", err)
	}
	var probe map[string]json.RawMessage
	var v int64
	if json.Unmarshal(data, &probe) != nil || json.Unmarshal(probe["faultline_report"], &v) != nil || v < 1 {
		return nil, errors.New("artifact: report.json: not a faultline report")
	}
	if v > ReportVersion {
		return nil, fmt.Errorf("artifact: report.json: version %d is newer than this faultline supports (%d); upgrade faultline", v, ReportVersion)
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("artifact: report.json: %w", err)
	}
	// encoding/json matches keys case-insensitively, so a "FAULTLINE_REPORT" key could overwrite
	// the version just checked; the report keeps the checked one (ART-086).
	rep.Version = int(v)
	return &rep, nil
}
