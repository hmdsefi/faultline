package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/hmdsefi/faultline/kernel"
)

// AT-ART-04
func TestReportRoundTrip(t *testing.T) {
	rep := fixtureReport()
	var a, b bytes.Buffer
	if err := WriteReport(&a, &rep); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(&b, &rep); err != nil {
		t.Fatal(err)
	}
	out := a.String()
	if out != b.String() {
		t.Fatal("two WriteReport calls differ")
	}
	if !strings.HasPrefix(out, "{\n  \"faultline_report\": 1,\n  \"status\": \"fail\",\n") || !strings.HasSuffix(out, "}\n") || strings.HasSuffix(out, "\n\n") {
		t.Fatalf("unexpected layout:\n%s", out)
	}
	if !strings.Contains(out, `"headline": "invariant \"no pong\" violated at t=0.003000000s on n2 (event 4)"`) {
		t.Fatalf("headline not encoded without HTML escaping:\n%s", out)
	}
	got, err := ReadReport(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var again bytes.Buffer
	if err := WriteReport(&again, got); err != nil {
		t.Fatal(err)
	}
	if again.String() != out {
		t.Fatalf("round trip differs:\n%s\n---\n%s", again.String(), out)
	}
	if got.Failure.RecordSeq != 12 || got.Seed != "0x0000000000000001" || got.Replay.Env["FAULTLINE_SEED"] != "0x0000000000000001" {
		t.Fatalf("round trip lost fields: %+v", got)
	}

	withExtra := strings.Replace(out, "{\n", "{\n  \"zzz\": 1,\n", 1)
	if _, err := ReadReport(strings.NewReader(withExtra)); err != nil {
		t.Fatalf("unknown field not ignored: %v", err)
	}
	v2 := strings.Replace(out, `"faultline_report": 1`, `"faultline_report": 2`, 1)
	if _, err := ReadReport(strings.NewReader(v2)); err == nil || err.Error() != "artifact: report.json: version 2 is newer than this faultline supports (1); upgrade faultline" {
		t.Fatalf("version 2: %v", err)
	}
	removed := strings.Replace(out, "  \"faultline_report\": 1,\n", "", 1)
	if _, err := ReadReport(strings.NewReader(removed)); err == nil || err.Error() != "artifact: report.json: not a faultline report" {
		t.Fatalf("missing version: %v", err)
	}
}

func TestWriteReportNeverNull(t *testing.T) {
	rep := Report{Status: "pass", Nodes: []Node{{ID: 1, Name: "n1"}}}
	var buf bytes.Buffer
	if err := WriteReport(&buf, &rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`"tags": []`, `"planners": []`, `"files": []`, `"faultline_report": 1`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Contains(out, `"failure"`) {
		t.Errorf("pass report has a failure field:\n%s", out)
	}
}

// fullReport sets every field of Report, nested types included, each to a value that is not its
// zero value. Replay.Env's keys are out of order and Replay.Command holds &, < and >.
func fullReport() Report {
	rec := FromRecord(kernel.Record{Seq: 7, At: 5, Node: 1, Inc: 2, Kind: "k", Cause: 6, Text: "x", Attrs: attrs("a", "b")})
	return Report{
		Version: 1, Status: "fail", Package: "p", Test: "T", Subtest: "T/s", Seed: "0x1", SeedSource: "derived",
		BaseSeed: "0x2", BaseSource: "test_name", SeedIndex: 3,
		Failure: &Failure{
			Kind: "panic", Check: "pkg.f", Signature: "panic:pkg.f", Headline: "h", Message: "m", AtNS: 4, At: "0.000000004s",
			Node: "n1", NodeID: 1, Event: 5, RecordSeq: 6, Panic: &Panic{Value: "boom", In: "callback", Name: "inv", Site: "pkg.f", Stack: "goroutine 1"}, Limit: &Limit{Name: "MaxEvents", Value: 7},
			Finals: []FinalFailure{{Check: "c", Message: "fm", Panic: &Panic{Value: "v2", In: "final", Site: "s2", Stack: "st2"}}},
			Determinism: &Determinism{
				Context: "artifact_rerun", Hashes: []string{"0xa", "0xb"},
				Original: &Failure{Kind: "invariant", Check: "c2", Signature: "invariant:c2", Headline: "h2", Message: "m2", AtNS: 8, At: "0.000000008s"},
				Diff:     &RecordDiff{Index: 9, A: &rec, B: &TraceRecord{Seq: 8, T: "0.000000000s", Kind: "k2"}},
			},
		},
		Warnings: []string{"w1", "w2"},
		Replay: Replay{
			Command: "FAULTLINE_SEED=0x1 go test -run '^T$' . && echo <ok>", Env: map[string]string{"FAULTLINE_SEED": "0x1", "FAULTLINE_SCHEDULE": "/s.json"},
			Run: "^T$", PackageArg: ".", Dir: "/m", PackageDir: "/m/p",
		},
		Versions: Versions{Faultline: "v1", Go: "go1.26.0", GOOS: "linux", GOARCH: "amd64", TestBinarySHA256: "ab"},
		Options: RunOptions{
			Seeds: 1, DurationNS: 2, Duration: "2ns", MaxEvents: 3, Mode: "goroutine", Trace: "full", TraceBuffer: 4,
			CheckDeterminism: true, KeepGoing: true, NoCryptoSeed: true, AllowLimit: true, Schedule: "/s.json", ScheduleHash: "0xc",
			Net: json.RawMessage(`{"Default":{"Latency":1}}`), Disk: json.RawMessage(`{"Crash":0}`),
			Procs: 5, DrainNS: 6, StallTimeoutNS: -1, FailOnLeak: true, NetSim: json.RawMessage(`{"a":1}`), Swarm: true,
		},
		OptionsHash: "0xd",
		Run: RunInfo{
			TraceHash: "0xe", Events: 10, Records: 11, Dropped: 12, EndNS: 13, End: "0.000000013s", Stop: "failed",
			RecoveryNS: 14, Recovery: "0.000000014s", Planners: []string{"random"}, Attempts: 2,
		},
		Nodes:            []Node{{ID: 1, Name: "n1", Tags: []string{"server", "leader"}}},
		DeterminismLevel: "best-effort", ExactBackend: "wasip1",
		Leaks: &Leaks{
			Groups:       []LeakGroup{{Node: "n1", Inc: 2, Status: "world", Count: 3, Records: []LeakRecord{{Count: 3, Frames: []string{"f (a.go:1)"}}}}},
			Unattributed: 1,
		},
		Asserts: json.RawMessage(`[{"name":"a","kind":"always","calls":1,"true":1}]`),
		Swarm:   json.RawMessage(`{"Faults":true}`),
		Dir:     "/tmp/d",
		Files:   []File{{Name: "report.txt", Type: "report_text"}, {Name: "trace.jsonl", Type: "trace", Version: 1}},
	}
}

// fullReportText is WriteReport's output for fullReport: ART-021's names in §4.1's field order.
const fullReportText = `{
  "faultline_report": 1,
  "status": "fail",
  "package": "p",
  "test": "T",
  "subtest": "T/s",
  "seed": "0x1",
  "seed_source": "derived",
  "base_seed": "0x2",
  "base_source": "test_name",
  "seed_index": 3,
  "failure": {
    "kind": "panic",
    "check": "pkg.f",
    "signature": "panic:pkg.f",
    "headline": "h",
    "message": "m",
    "at_ns": 4,
    "at": "0.000000004s",
    "node": "n1",
    "node_id": 1,
    "event": 5,
    "record_seq": 6,
    "panic": {
      "value": "boom",
      "in": "callback",
      "name": "inv",
      "site": "pkg.f",
      "stack": "goroutine 1"
    },
    "limit": {
      "name": "MaxEvents",
      "value": 7
    },
    "finals": [
      {
        "check": "c",
        "message": "fm",
        "panic": {
          "value": "v2",
          "in": "final",
          "site": "s2",
          "stack": "st2"
        }
      }
    ],
    "determinism": {
      "context": "artifact_rerun",
      "hashes": [
        "0xa",
        "0xb"
      ],
      "original": {
        "kind": "invariant",
        "check": "c2",
        "signature": "invariant:c2",
        "headline": "h2",
        "message": "m2",
        "at_ns": 8,
        "at": "0.000000008s"
      },
      "diff": {
        "index": 9,
        "a": {
          "seq": 7,
          "at": 5,
          "t": "0.000000005s",
          "node": 1,
          "inc": 2,
          "kind": "k",
          "cause": 6,
          "text": "x",
          "attrs": [
            [
              "a",
              "b"
            ]
          ]
        },
        "b": {
          "seq": 8,
          "at": 0,
          "t": "0.000000000s",
          "kind": "k2"
        }
      }
    }
  },
  "warnings": [
    "w1",
    "w2"
  ],
  "replay": {
    "command": "FAULTLINE_SEED=0x1 go test -run '^T$' . && echo <ok>",
    "env": {
      "FAULTLINE_SCHEDULE": "/s.json",
      "FAULTLINE_SEED": "0x1"
    },
    "run": "^T$",
    "package_arg": ".",
    "dir": "/m",
    "package_dir": "/m/p"
  },
  "versions": {
    "faultline": "v1",
    "go": "go1.26.0",
    "goos": "linux",
    "goarch": "amd64",
    "test_binary_sha256": "ab"
  },
  "options": {
    "seeds": 1,
    "duration_ns": 2,
    "duration": "2ns",
    "max_events": 3,
    "mode": "goroutine",
    "trace": "full",
    "trace_buffer": 4,
    "check_determinism": true,
    "keep_going": true,
    "no_crypto_seed": true,
    "allow_limit": true,
    "schedule": "/s.json",
    "schedule_hash": "0xc",
    "net": {
      "Default": {
        "Latency": 1
      }
    },
    "disk": {
      "Crash": 0
    },
    "procs": 5,
    "drain_ns": 6,
    "stall_timeout_ns": -1,
    "fail_on_leak": true,
    "netsim": {
      "a": 1
    },
    "swarm": true
  },
  "options_hash": "0xd",
  "run": {
    "trace_hash": "0xe",
    "events": 10,
    "records": 11,
    "dropped": 12,
    "end_ns": 13,
    "end": "0.000000013s",
    "stop": "failed",
    "recovery_ns": 14,
    "recovery": "0.000000014s",
    "planners": [
      "random"
    ],
    "attempts": 2
  },
  "nodes": [
    {
      "id": 1,
      "name": "n1",
      "tags": [
        "server",
        "leader"
      ]
    }
  ],
  "determinism_level": "best-effort",
  "exact_backend": "wasip1",
  "leaks": {
    "groups": [
      {
        "node": "n1",
        "inc": 2,
        "status": "world",
        "count": 3,
        "records": [
          {
            "count": 3,
            "frames": [
              "f (a.go:1)"
            ]
          }
        ]
      }
    ],
    "unattributed": 1
  },
  "asserts": [
    {
      "name": "a",
      "kind": "always",
      "calls": 1,
      "true": 1
    }
  ],
  "swarm": {
    "Faults": true
  },
  "dir": "/tmp/d",
  "files": [
    {
      "name": "report.txt",
      "type": "report_text"
    },
    {
      "name": "trace.jsonl",
      "type": "trace",
      "version": 1
    }
  ]
}
`

// emptyReport sets every pointer to an empty value and gives every slice one empty element, so
// that each nested object shows the keys it writes when empty (ART-021). Its Determinism has nil
// Hashes and an Original whose pointers are nil, and one LeakGroup has nil Records.
func emptyReport() Report {
	return Report{
		Failure: &Failure{
			Panic: &Panic{}, Limit: &Limit{}, Finals: []FinalFailure{{}},
			Determinism: &Determinism{Original: &Failure{}, Diff: &RecordDiff{}},
		},
		Warnings: []string{},
		Replay:   Replay{Env: map[string]string{}},
		Options:  RunOptions{Net: json.RawMessage("{}"), Disk: json.RawMessage("{}")},
		Nodes:    []Node{{}},
		Leaks:    &Leaks{Groups: []LeakGroup{{}, {Records: []LeakRecord{{}}}}},
		Files:    []File{{}},
	}
}

// emptyReportText is WriteReport's output for emptyReport.
const emptyReportText = `{
  "faultline_report": 1,
  "status": "",
  "package": "",
  "test": "",
  "subtest": "",
  "seed": "",
  "seed_source": "",
  "seed_index": 0,
  "failure": {
    "kind": "",
    "check": "",
    "signature": "",
    "headline": "",
    "message": "",
    "at_ns": 0,
    "at": "",
    "panic": {
      "value": "",
      "in": "",
      "site": "",
      "stack": ""
    },
    "limit": {
      "name": "",
      "value": 0
    },
    "finals": [
      {
        "check": "",
        "message": ""
      }
    ],
    "determinism": {
      "context": "",
      "hashes": null,
      "original": {
        "kind": "",
        "check": "",
        "signature": "",
        "headline": "",
        "message": "",
        "at_ns": 0,
        "at": ""
      },
      "diff": {
        "index": 0
      }
    }
  },
  "replay": {
    "command": "",
    "env": {},
    "run": "",
    "package_arg": "",
    "package_dir": ""
  },
  "versions": {
    "faultline": "",
    "go": "",
    "goos": "",
    "goarch": ""
  },
  "options": {
    "seeds": 0,
    "duration_ns": 0,
    "duration": "",
    "max_events": 0,
    "mode": "",
    "trace": "",
    "trace_buffer": 0,
    "check_determinism": false,
    "keep_going": false,
    "no_crypto_seed": false,
    "allow_limit": false,
    "net": {},
    "disk": {}
  },
  "options_hash": "",
  "run": {
    "trace_hash": "",
    "events": 0,
    "records": 0,
    "dropped": 0,
    "end_ns": 0,
    "end": "",
    "stop": "",
    "planners": [],
    "attempts": 0
  },
  "nodes": [
    {
      "id": 0,
      "name": "",
      "tags": []
    }
  ],
  "leaks": {
    "groups": [
      {
        "node": "",
        "inc": 0,
        "status": "",
        "count": 0,
        "records": null
      },
      {
        "node": "",
        "inc": 0,
        "status": "",
        "count": 0,
        "records": [
          {
            "count": 0,
            "frames": null
          }
        ]
      }
    ],
    "unattributed": 0
  },
  "dir": "",
  "files": [
    {
      "name": "",
      "type": ""
    }
  ]
}
`

// minimalReport has every pointer, slice and map nil.
func minimalReport() Report { return Report{} }

// minimalReportText is WriteReport's output for minimalReport: nil pointers are left out, nil
// Nodes, Planners and Files are [], and other nil slices, maps and json.RawMessage values are null.
const minimalReportText = `{
  "faultline_report": 1,
  "status": "",
  "package": "",
  "test": "",
  "subtest": "",
  "seed": "",
  "seed_source": "",
  "seed_index": 0,
  "replay": {
    "command": "",
    "env": null,
    "run": "",
    "package_arg": "",
    "package_dir": ""
  },
  "versions": {
    "faultline": "",
    "go": "",
    "goos": "",
    "goarch": ""
  },
  "options": {
    "seeds": 0,
    "duration_ns": 0,
    "duration": "",
    "max_events": 0,
    "mode": "",
    "trace": "",
    "trace_buffer": 0,
    "check_determinism": false,
    "keep_going": false,
    "no_crypto_seed": false,
    "allow_limit": false,
    "net": null,
    "disk": null
  },
  "options_hash": "",
  "run": {
    "trace_hash": "",
    "events": 0,
    "records": 0,
    "dropped": 0,
    "end_ns": 0,
    "end": "",
    "stop": "",
    "planners": [],
    "attempts": 0
  },
  "nodes": [],
  "dir": "",
  "files": []
}
`

// ART-020, ART-021: every key, in order, indented by two spaces, without HTML escaping, with sorted
// env keys and the json.RawMessage values indented in place; which keys an empty or nil value
// omits, and which nil values are written as [] or null. ReadReport reads each one back.
func TestWriteReportFields(t *testing.T) {
	for _, c := range []struct {
		name string
		rep  Report
		want string
	}{
		{"full", fullReport(), fullReportText},
		{"empty", emptyReport(), emptyReportText},
		{"minimal", minimalReport(), minimalReportText},
	} {
		var buf bytes.Buffer
		if err := WriteReport(&buf, &c.rep); err != nil {
			t.Fatal(err)
		}
		if buf.String() != c.want {
			t.Errorf("%s report:\n%s", c.name, buf.String())
			continue
		}
		got, err := ReadReport(&buf)
		if err != nil {
			t.Fatal(err)
		}
		var again bytes.Buffer
		if err := WriteReport(&again, got); err != nil || again.String() != c.want {
			t.Errorf("%s report read back: %v\n%s", c.name, err, again.String())
		}
	}

	// Leaks without groups: groups is null. Determinism without original and diff: both are left
	// out.
	for _, c := range []struct {
		rep  Report
		want string
	}{
		{Report{Leaks: &Leaks{}}, "\n  \"leaks\": {\n    \"groups\": null,\n    \"unattributed\": 0\n  },\n"},
		{Report{Failure: &Failure{Determinism: &Determinism{}}}, "\n    \"determinism\": {\n      \"context\": \"\",\n      \"hashes\": null\n    }\n  },\n"},
		// A non-nil empty slice is [], not null.
		{Report{Failure: &Failure{Determinism: &Determinism{Hashes: []string{}}}}, "\n      \"hashes\": []\n"},
		{Report{Leaks: &Leaks{Groups: []LeakGroup{}}}, "\n    \"groups\": [],\n"},
		{Report{Leaks: &Leaks{Groups: []LeakGroup{{Records: []LeakRecord{}}}}}, "\n        \"records\": []\n"},
		{Report{Leaks: &Leaks{Groups: []LeakGroup{{Records: []LeakRecord{{Frames: []string{}}}}}}}, "\n            \"frames\": []\n"},
	} {
		var buf bytes.Buffer
		if err := WriteReport(&buf, &c.rep); err != nil || !strings.Contains(buf.String(), c.want) {
			t.Errorf("want %q in: %v\n%s", c.want, err, buf.String())
		}
	}
}

// ART-020: every string, at any depth and in map keys, has each run of invalid UTF-8 replaced by
// one U+FFFD before encoding, so Go 1.26 and 1.27 write the same bytes; json.RawMessage values are
// written as given; WriteReport does not change its input. Keys that become equal keep the value
// of the key that sorts last.
func TestWriteReportUTF8(t *testing.T) {
	mk := func(bad string) Report {
		rec := FromRecord(kernel.Record{Seq: 1, Kind: "k", Text: "t"})
		rec.Text, rec.Attrs = "t"+bad, [][2]string{{"k" + bad, "v" + bad}}
		return Report{
			Status: "fail" + bad, Warnings: []string{"w" + bad},
			Failure: &Failure{
				Headline: "h" + bad, Panic: &Panic{Stack: "s" + bad}, Finals: []FinalFailure{{Message: "m" + bad}},
				Determinism: &Determinism{Hashes: []string{"0x" + bad}, Original: &Failure{Headline: "o" + bad}, Diff: &RecordDiff{A: &rec}},
			},
			Replay:  Replay{Env: map[string]string{"K" + bad: "v" + bad}},
			Options: RunOptions{Net: json.RawMessage(`{}`), Disk: json.RawMessage(`{}`)},
			Run:     RunInfo{Planners: []string{"p" + bad}},
			Nodes:   []Node{{ID: 1, Name: "n" + bad, Tags: []string{"t" + bad}}},
			Leaks:   &Leaks{Groups: []LeakGroup{{Records: []LeakRecord{{Frames: []string{"f" + bad}}}}}},
			Files:   []File{{Name: "x" + bad}},
		}
	}
	bad := mk("\xff\xfe")
	var got, want bytes.Buffer
	if err := WriteReport(&got, &bad); err != nil {
		t.Fatal(err)
	}
	clean := mk("~")
	if err := WriteReport(&want, &clean); err != nil {
		t.Fatal(err)
	}
	if w := strings.ReplaceAll(want.String(), "~", string(utf8.RuneError)); got.String() != w {
		t.Fatalf("invalid UTF-8:\n got: %q\nwant: %q", got.String(), w)
	}
	if !reflect.DeepEqual(bad, mk("\xff\xfe")) {
		t.Fatalf("WriteReport changed its input: %+v", bad)
	}
	raw := Report{Options: RunOptions{Net: json.RawMessage("{\"a\":\"\xff\"}"), Disk: json.RawMessage(`{}`)}}
	got.Reset()
	if err := WriteReport(&got, &raw); err != nil || !strings.Contains(got.String(), "\"net\": {\n      \"a\": \"\xff\"\n    },") {
		t.Fatalf("json.RawMessage not written as given: %v\n%q", err, got.String())
	}

	// 20 keys that all become "K" + U+FFFD: the last in byte order, "K\x93", wins.
	env := map[string]string{}
	for i := range 20 {
		env["K"+string([]byte{byte(0x80 + i)})] = string(rune('a' + i))
	}
	// Map iteration order is random, so without the sort the right key would still win about one
	// run in ten; 64 runs make a missing sort fail for certain.
	r := Report{Replay: Replay{Env: env}}
	for range 64 {
		var buf bytes.Buffer
		if err := WriteReport(&buf, &r); err != nil {
			t.Fatal(err)
		}
		if want := "\"env\": {\n      \"K" + string(utf8.RuneError) + "\": \"t\"\n    },"; !strings.Contains(buf.String(), want) {
			t.Fatalf("colliding keys:\n%s", buf.String())
		}
	}
}

// ART-020: a nil report, a failed write and a json.RawMessage that is not JSON are returned as
// errors.
func TestWriteReportErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteReport(&buf, nil); err == nil || err.Error() != "artifact: report is nil" || buf.Len() != 0 {
		t.Errorf("nil report: %v, %q", err, buf.String())
	}
	errDisk := errors.New("disk full")
	rep := fullReport()
	if err := WriteReport(errWriter{errDisk}, &rep); !errors.Is(err, errDisk) {
		t.Errorf("failed write: %v", err)
	}
	rep.Options.Net = json.RawMessage("{")
	if err := WriteReport(&bytes.Buffer{}, &rep); err == nil {
		t.Error("bad json.RawMessage: no error")
	}

	// A pointer cycle is bad input (ART-021: original has no determinism); a pointer shared by two
	// fields is not a cycle.
	self := &Failure{Kind: "determinism"}
	self.Determinism = &Determinism{Original: self}
	a, b := &Failure{Kind: "a"}, &Failure{Kind: "b"}
	a.Determinism, b.Determinism = &Determinism{Original: b}, &Determinism{Original: a}
	for _, c := range []struct {
		name string
		f    *Failure
	}{{"self", self}, {"two steps", a}} {
		buf.Reset()
		r := Report{Status: "fail", Failure: c.f}
		if err := WriteReport(&buf, &r); err == nil || err.Error() != "artifact: report has a pointer cycle" || buf.Len() != 0 {
			t.Errorf("%s cycle: %v, %q", c.name, err, buf.String())
		}
		// Write (Task 11) calls normReport itself, so the check must be there.
		if _, err := normReport(&r); err == nil || err.Error() != "artifact: report has a pointer cycle" {
			t.Errorf("%s cycle, normReport: %v", c.name, err)
		}
	}
	rec := &TraceRecord{Seq: 1, T: "0.000000000s", Kind: "k"}
	shared := Report{Status: "fail", Failure: &Failure{Determinism: &Determinism{Original: &Failure{}, Diff: &RecordDiff{A: rec, B: rec}}}}
	buf.Reset()
	one := "{\n          \"seq\": 1,\n          \"at\": 0,\n          \"t\": \"0.000000000s\",\n          \"kind\": \"k\"\n        }"
	if err := WriteReport(&buf, &shared); err != nil || !strings.Contains(buf.String(), "\"a\": "+one+",\n        \"b\": "+one+"\n") {
		t.Errorf("shared pointer: %v\n%s", err, buf.String())
	}
}

// ART-086, ART-087: what ReadReport rejects, in its check order, and what it accepts.
func TestReadReportErrors(t *testing.T) {
	var buf bytes.Buffer
	rep := fixtureReport()
	if err := WriteReport(&buf, &rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	version := func(v string) string {
		return strings.Replace(out, `"faultline_report": 1,`, `"faultline_report": `+v+`,`, 1)
	}
	const notReport = "artifact: report.json: not a faultline report"
	const newer = "artifact: report.json: version 2 is newer than this faultline supports (1); upgrade faultline"
	for _, c := range []struct{ name, text, want string }{
		{"version 0", version("0"), notReport},
		{"version -1", version("-1"), notReport},
		{"version 1.0", version("1.0"), notReport},
		{"version a string", version(`"1"`), notReport},
		{"version null", version("null"), notReport},
		{"empty input", "", notReport},
		{"not JSON", "{", notReport},
		{"an array", "[1]", notReport},
		{"null", "null", notReport},
		{"version before field types", strings.Replace(version("2"), `"seed": "0x0000000000000001"`, `"seed": 1`, 1), newer},
	} {
		if _, err := ReadReport(strings.NewReader(c.text)); err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}

	// After the version checks, a field of the wrong type gives encoding/json's error. Its text
	// differs between Go versions, so the test computes it.
	wrong := strings.Replace(out, `"seed": "0x0000000000000001"`, `"seed": 1`, 1)
	var probe Report
	jerr := json.Unmarshal([]byte(wrong), &probe)
	if _, err := ReadReport(strings.NewReader(wrong)); jerr == nil || err == nil || err.Error() != "artifact: report.json: "+jerr.Error() {
		t.Errorf("wrong field type: %v, want encoding/json's %v", err, jerr)
	}

	// The report keeps the version it checked, whatever a key that differs only in case says.
	twin := strings.Replace(out, `"faultline_report": 1,`, `"faultline_report": 1, "FAULTLINE_REPORT": 7,`, 1)
	if got, err := ReadReport(strings.NewReader(twin)); err != nil || got.Version != 1 {
		t.Errorf("FAULTLINE_REPORT: %v, %+v", err, got)
	}

	errRead := errors.New("read failed")
	if got, err := ReadReport(iotest.ErrReader(errRead)); got != nil || !errors.Is(err, errRead) || err.Error() != "artifact: report.json: read failed" {
		t.Errorf("read error: %v, %v", got, err)
	}

	// Unknown enum values are opaque strings (ART-087 rule 2).
	odd := strings.NewReplacer(`"status": "fail"`, `"status": "maybe"`, `"kind": "invariant"`, `"kind": "zzz"`).Replace(out)
	if got, err := ReadReport(strings.NewReader(odd)); err != nil || got.Status != "maybe" || got.Failure.Kind != "zzz" {
		t.Errorf("unknown enum values: %v, %+v", err, got)
	}
}

// TestReportKinds walks the report's type graph and fails on a kind that validCopy and hasCycle do
// not handle: a later field of such a kind would skip the UTF-8 replacement (an interface) or
// panic (an unexported field) instead of failing here.
func TestReportKinds(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int32, reflect.Int64, reflect.Uint32, reflect.Uint64:
		case reflect.Pointer, reflect.Array:
			walk(typ.Elem(), path+"[]")
		case reflect.Slice:
			if typ.Elem().Kind() != reflect.Uint8 { // json.RawMessage is written as given
				walk(typ.Elem(), path+"[]")
			}
		case reflect.Map:
			if typ.Key().Kind() != reflect.String {
				t.Errorf("%s: map key %s is not a string", path, typ.Key())
			}
			walk(typ.Elem(), path+"{}")
		case reflect.Struct:
			for i := range typ.NumField() {
				f := typ.Field(i)
				if !f.IsExported() {
					t.Errorf("%s.%s: unexported field", path, f.Name)
				}
				walk(f.Type, path+"."+f.Name)
			}
		default:
			t.Errorf("%s: kind %s is not handled by validCopy", path, typ.Kind())
		}
	}
	walk(reflect.TypeFor[Report](), "Report")
}
