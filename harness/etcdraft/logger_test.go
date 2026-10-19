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

// ETC-101: the other described types, and a nil pointer of each of the seven.
func TestLoggerDescribesEveryType(t *testing.T) {
	l, sim := newTestLogger(LogInfo)
	e := &raftpb.Entry{Index: new(uint64(7)), Term: new(uint64(2)), Type: raftpb.EntryNormal.Enum(), Data: []byte("x")}
	m := &raftpb.Message{Type: raftpb.MsgApp.Enum(), From: new(uint64(1)), To: new(uint64(2)), Term: new(uint64(2)),
		LogTerm: new(uint64(2)), Index: new(uint64(6)), Commit: new(uint64(5)), Entries: []*raftpb.Entry{e}}
	snap := &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(9)), Term: new(uint64(2)),
		ConfState: &raftpb.ConfState{Voters: []uint64{1, 2, 3}}}}
	cc := &raftpb.ConfChange{Type: raftpb.ConfChangeAddNode.Enum(), NodeId: new(uint64(4))}
	l.Infof("%v | %v | %v | %v", e, m, snap, cc)
	l.Infof("%v %v %v %v %v %v %v", (*raftpb.HardState)(nil), (*raftpb.Entry)(nil), (*raftpb.Message)(nil),
		(*raftpb.Snapshot)(nil), (*raftpb.ConfState)(nil), (*raftpb.ConfChange)(nil), (*raftpb.ConfChangeV2)(nil))
	recs := logRecords(sim)
	if len(recs) != 2 {
		t.Fatalf("got %d raft_log records, want 2", len(recs))
	}
	want := raft.DescribeEntry(e, nil) + " | " + raft.DescribeMessage(m, nil) + " | " + raft.DescribeSnapshot(snap) + " | " + raft.DescribeConfChange(cc)
	if recs[0].Text != want {
		t.Fatalf("text = %q\nwant  %q", recs[0].Text, want)
	}
	if want := strings.TrimSpace(strings.Repeat("<nil> ", 7)); recs[1].Text != want {
		t.Fatalf("nil pointers: text = %q, want %q", recs[1].Text, want)
	}
}

// ETC-100: the debug level, Warning below LogWarning, a server's logger, Panic and Fatalf.
func TestLoggerLevelsAndNodes(t *testing.T) {
	l, sim := newTestLogger(LogInfo)
	l.Debug("hidden")
	l.Debugf("hidden %d", 1)
	if recs := logRecords(sim); len(recs) != 0 {
		t.Fatalf("LogInfo kept debug records: %+v", recs)
	}
	l, sim = newTestLogger(LogDebug)
	l.Debug("d")
	l.Debugf("d %d", 2)
	if recs := logRecords(sim); len(recs) != 2 || recs[0].Text != "d" || recs[1].Text != "d 2" ||
		attrOf(recs[0], "level") != "debug" || attrOf(recs[1], "level") != "debug" {
		t.Fatalf("LogDebug records = %+v", recs)
	}
	l, sim = newTestLogger(LogError)
	l.Warning("hidden")
	l.Warningf("hidden %d", 3)
	l.Error("e")
	if recs := logRecords(sim); len(recs) != 1 || attrOf(recs[0], "level") != "error" {
		t.Fatalf("LogError records = %+v; want the error only", recs)
	}
	l, sim = newTestLogger(LogInfo)
	l.node = sim.AddNode("n2", func(*kernel.Node) {})
	l.Info("from n2")
	if recs := logRecords(sim); len(recs) != 1 || recs[0].Node != l.node.ID() {
		t.Fatalf("records = %+v; want one with Node %d", recs, l.node.ID())
	}
	l, sim = newTestLogger(LogNone)
	for _, c := range []struct {
		call       func()
		value      string
		level, txt string
	}{
		{func() { l.Panic("p ", 1) }, "p 1", "panic", "p 1"},
		{func() { l.Fatalf("f %d", 2) }, "raft fatal: f 2", "fatal", "f 2"},
	} {
		before := len(logRecords(sim))
		func() {
			defer func() {
				if v := recover(); v != c.value {
					t.Errorf("panic value %v, want %q", v, c.value)
				}
			}()
			c.call()
		}()
		recs := logRecords(sim)
		if len(recs) != before+1 {
			t.Errorf("the call panicking with %q emitted %d records, want 1 with level %s and text %q", c.value, len(recs)-before, c.level, c.txt)
			continue
		}
		if r := recs[len(recs)-1]; attrOf(r, "level") != c.level || r.Text != c.txt {
			t.Errorf("record %+v, want level %s and text %q", r, c.level, c.txt)
		}
	}
}
