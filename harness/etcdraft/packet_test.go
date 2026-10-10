package etcdraft

import (
	"testing"

	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// ETC-091
func TestPacketDescriptions(t *testing.T) {
	app := &raftpb.Message{Type: raftpb.MsgApp.Enum(), From: new(uint64(1)), To: new(uint64(2)), Term: new(uint64(3)),
		Index: new(uint64(17)), LogTerm: new(uint64(3)), Commit: new(uint64(16)),
		Entries: []*raftpb.Entry{{Index: new(uint64(18))}, {Index: new(uint64(19))}}}
	rej := &raftpb.Message{Type: raftpb.MsgAppResp.Enum(), From: new(uint64(2)), To: new(uint64(1)), Term: new(uint64(3)),
		Index: new(uint64(17)), Reject: new(true)}
	snap := &raftpb.Message{Type: raftpb.MsgSnap.Enum(), From: new(uint64(1)), To: new(uint64(3)), Term: new(uint64(4)),
		Snapshot: &raftpb.Snapshot{Metadata: &raftpb.SnapshotMetadata{Index: new(uint64(250))}}}
	cases := []struct {
		p    *packet
		want string
	}{
		{raftPacket(app), "raft MsgApp 1->2 t=3 i=17 lt=3 c=16 n=2"},
		{raftPacket(rej), "raft MsgAppResp 2->1 t=3 i=17 lt=0 c=0 n=0 rej"},
		{raftPacket(snap), "raft MsgSnap 1->3 t=4 i=0 lt=0 c=0 n=0 snap=250"},
		{requestPacket("c2", wire.Request{Client: 2, ID: 14, Attempt: 1, Op: kv.OpPut, Key: "k3", Value: "c2-14"}), "req c2#14.1 put k3"},
		{requestPacket("c1", wire.Request{Client: 1, ID: 3, Attempt: 2, Op: kv.OpGet, Key: "k0"}), "req c1#3.2 get k0"},
		{requestPacket("admin", wire.Request{Client: 5, ID: 1, Attempt: 1, Op: kv.OpConfRead}), "req admin#1.1 conf-read "},
		{replyPacket(wire.Reply{ID: 14, Attempt: 1, Status: wire.StatusOK, Leader: 1}), "rep #14.1 ok lead=1"},
		{replyPacket(wire.Reply{ID: 15, Attempt: 2, Status: wire.StatusDropped}), "rep #15.2 dropped lead=0"},
		{snapAckPacket(250), "snapack 250"},
	}
	for _, c := range cases {
		if got := kernel.Describe(c.p); got != c.want {
			t.Errorf("Describe = %q, want %q", got, c.want)
		}
	}
	m, err := wire.DecodeRaft(raftPacket(app).data)
	if err != nil || m.GetIndex() != 17 || len(m.GetEntries()) != 2 {
		t.Fatalf("raft packet data does not decode to the message: %v", err)
	}
}
