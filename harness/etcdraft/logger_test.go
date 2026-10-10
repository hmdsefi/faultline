// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package etcdraft

import (
	"strings"
	"testing"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline/kernel"
)

func newTestLogger(level LogLevel) (*raftLogger, *kernel.Sim) {
	sim := kernel.New(kernel.Config{Seed: 1, Trace: kernel.TraceConfig{Level: kernel.TraceFull}})
	return &raftLogger{sim: sim, level: level}, sim
}

// attrOf returns the value of key in r, or "".
func attrOf(r kernel.Record, key string) string { //nolint:unparam // later tests pass other keys
	for _, a := range r.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

func logRecords(sim *kernel.Sim) []kernel.Record {
	var out []kernel.Record
	for _, r := range sim.Records() {
		if r.Kind == "etcdraft.raft_log" {
			out = append(out, r)
		}
	}
	return out
}

// AT-ETC-25
func TestLoggerSanitizes(t *testing.T) {
	l, sim := newTestLogger(LogInfo)
	cs := &raftpb.ConfState{Voters: []uint64{1, 2, 3}, Learners: []uint64{4}}
	cc := &raftpb.ConfChangeV2{Changes: []*raftpb.ConfChangeSingle{{Type: raftpb.ConfChangeAddLearnerNode.Enum(), NodeId: new(uint64(4))}}}
	single := &raftpb.ConfChangeSingle{Type: raftpb.ConfChangeAddNode.Enum(), NodeId: new(uint64(5))}
	l.Infof("conf %v change %v single %v", cs, cc, single)
	l.Info("hs ", &raftpb.HardState{Term: new(uint64(3)), Vote: new(uint64(1)), Commit: new(uint64(16))})
	recs := logRecords(sim)
	if len(recs) != 2 {
		t.Fatalf("got %d raft_log records, want 2", len(recs))
	}
	want := "conf " + raft.DescribeConfState(cs) + " change " + raft.DescribeConfChange(cc) + " single <raftpb.ConfChangeSingle>"
	if recs[0].Text != want {
		t.Fatalf("text = %q\nwant  %q", recs[0].Text, want)
	}
	if recs[1].Text != "hs Term:3 Vote:1 Commit:16" || attrOf(recs[1], "level") != "info" || recs[1].Node != 0 {
		t.Fatalf("record = %+v", recs[1])
	}
}

// ETC-100: levels, truncation, Panic and Fatal.
func TestLoggerLevels(t *testing.T) {
	l, sim := newTestLogger(LogWarning)
	l.Debug("d")
	l.Infof("i %d", 1)
	l.Warning("w")
	l.Errorf("e %s", "x")
	l.Warningf("%s", strings.Repeat("a", 600))
	recs := logRecords(sim)
	var levels []string
	for _, r := range recs {
		levels = append(levels, attrOf(r, "level"))
	}
	if strings.Join(levels, ",") != "warning,error,warning" {
		t.Fatalf("levels = %v", levels)
	}
	if len(recs[2].Text) != 512 {
		t.Fatalf("long text kept %d bytes, want 512", len(recs[2].Text))
	}
	expectPanic := func(want string, f func()) {
		t.Helper()
		defer func() {
			if v := recover(); v != want {
				t.Fatalf("panic value %v, want %q", v, want)
			}
		}()
		f()
	}
	none, sim2 := newTestLogger(LogNone)
	none.Error("hidden")
	expectPanic("boom 7", func() { none.Panicf("boom %d", 7) })
	expectPanic("raft fatal: bad", func() { none.Fatal("bad") })
	recs = logRecords(sim2)
	if len(recs) != 2 || attrOf(recs[0], "level") != "panic" || attrOf(recs[1], "level") != "fatal" || recs[1].Text != "bad" {
		t.Fatalf("LogNone records = %+v", recs)
	}
}
