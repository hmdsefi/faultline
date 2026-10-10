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

// Report is report.json (ART §5.3).
type Report struct {
	Version     int        `json:"faultline_report"` // ReportVersion
	Status      string     `json:"status"`           // "fail" or "pass"
	Package     string     `json:"package"`
	Test        string     `json:"test"`
	Subtest     string     `json:"subtest"`
	Seed        string     `json:"seed"`
	SeedSource  string     `json:"seed_source"`           // "derived", "env", "list"
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

	// Phase 2 (GOR, EXB). Omitted when empty.
	DeterminismLevel string `json:"determinism_level,omitempty"` // "exact" or "best-effort"
	ExactBackend     string `json:"exact_backend,omitempty"`     // "wasip1" or "toolexec"; absent: none
	Leaks            *Leaks `json:"leaks,omitempty"`             // GOR-069
	// Phase 3 (EXP-050). Omitted when nil; from Phase 3 API always sets both.
	Asserts json.RawMessage `json:"asserts,omitempty"` // [{name, kind, calls, true}] by name; [] when none
	Swarm   json.RawMessage `json:"swarm,omitempty"`   // explore.SwarmConfig JSON, or null when swarm is off

	Dir   string `json:"dir"`   // set by Write
	Files []File `json:"files"` // set by Write
}

// Failure describes why a seed failed (API §5.10).
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
	Value string `json:"value"`          // panic text (API-064)
	In    string `json:"in"`             // "callback", "invariant", "final", "body"
	Name  string `json:"name,omitempty"` // invariant or final name
	Site  string `json:"site"`           // panic site (API-063), equal to Failure.Check
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

// Replay is the replay command and its parts (API-080).
type Replay struct {
	Command    string            `json:"command"`
	Env        map[string]string `json:"env"` // encoded with sorted keys
	Run        string            `json:"run"`
	PackageArg string            `json:"package_arg"`
	Dir        string            `json:"dir,omitempty"` // module root to run Command in; "" if unknown
	PackageDir string            `json:"package_dir"`
}

// Versions identify what produced the artifact (API-081).
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

	// Phase 2, goroutine mode only (omitted in event mode).
	Procs          int             `json:"procs,omitempty"`
	DrainNS        int64           `json:"drain_ns,omitempty"`
	StallTimeoutNS int64           `json:"stall_timeout_ns,omitempty"` // negative: watchdog off
	FailOnLeak     bool            `json:"fail_on_leak,omitempty"`
	NetSim         json.RawMessage `json:"netsim,omitempty"` // encoding/json of netsim.Config
	// Phase 3: effective swarm on/off (EXP-010).
	Swarm bool `json:"swarm,omitempty"`
}

// Leaks lists the goroutines still alive after a goroutine-mode run (GOR-068, GOR-069).
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

// RunInfo describes the artifact attempt.
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
	Attempts   int      `json:"attempts"`
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
	// The report types are recursive (Failure.Determinism.Original) and validCopy follows every
	// pointer, so a cycle would never end; encoding/json's own error differs between Go versions.
	if hasCycle(reflect.ValueOf(r), map[ptrKey]bool{}) {
		return Report{}, errors.New("artifact: report has a pointer cycle")
	}
	var rr Report
	reflect.ValueOf(&rr).Elem().Set(validCopy(reflect.ValueOf(*r)))
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

// validCopy returns a deep copy of v in which every string is valid UTF-8 (validUTF8). Byte
// slices (json.RawMessage) are shared as they are; nil pointers, slices and maps stay nil. It
// handles only what the report types hold, which TestReportKinds checks: strings, bools, integers,
// pointers, arrays, slices, maps with string keys and structs with exported fields only; no
// interfaces, and no pointer cycles (normReport rules them out first).
func validCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		return reflect.ValueOf(validUTF8(v.String())).Convert(v.Type())
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		p := reflect.New(v.Type().Elem())
		p.Elem().Set(validCopy(v.Elem()))
		return p
	case reflect.Struct:
		c := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			c.Field(i).Set(validCopy(v.Field(i)))
		}
		return c
	case reflect.Slice:
		if v.IsNil() || v.Type().Elem().Kind() == reflect.Uint8 {
			return v
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			c.Index(i).Set(validCopy(v.Index(i)))
		}
		return c
	case reflect.Array:
		c := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			c.Index(i).Set(validCopy(v.Index(i)))
		}
		return c
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		keys := v.MapKeys() //faultline:maporder sorted next, so keys that become equal resolve the same way every run
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		c := reflect.MakeMapWithSize(v.Type(), len(keys))
		for _, k := range keys {
			c.SetMapIndex(validCopy(k), validCopy(v.MapIndex(k)))
		}
		return c
	}
	return v // numbers and bools
}

// ptrKey is a pointer on hasCycle's path: its address and its type, since a pointer to a struct
// and one to its first field share the address.
type ptrKey struct {
	addr uintptr
	typ  reflect.Type
}

// hasCycle reports whether a pointer reachable from v leads back to a pointer on the path to it
// (ART-020). A pointer reached by two paths is shared, not a cycle.
func hasCycle(v reflect.Value, path map[ptrKey]bool) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return false
		}
		k := ptrKey{v.Pointer(), v.Type()}
		if path[k] {
			return true
		}
		path[k] = true
		defer delete(path, k)
		return hasCycle(v.Elem(), path)
	case reflect.Struct:
		for i := range v.NumField() {
			if hasCycle(v.Field(i), path) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return false // json.RawMessage
		}
		for i := range v.Len() {
			if hasCycle(v.Index(i), path) {
				return true
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() { //faultline:maporder any order gives the same answer
			if hasCycle(v.MapIndex(k), path) {
				return true
			}
		}
	}
	return false
}

// WriteReport writes r as report.json (ART-020).
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

// ReadReport reads and version-checks report.json (ART-086). Unknown fields are ignored.
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
		return nil, fmt.Errorf("artifact: report.json: %v", err)
	}
	// encoding/json matches keys case-insensitively, so a "FAULTLINE_REPORT" key could overwrite
	// the version just checked; the report keeps the checked one (ART-086).
	rep.Version = int(v)
	return &rep, nil
}
