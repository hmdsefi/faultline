package etcdraft

import (
	"fmt"

	"go.etcd.io/raft/v3/raftpb"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// packet is the only simnet payload the harness sends (ETC-091).
// packet is never mutated after it is sent.
type packet struct {
	data []byte // wire encoding; data[0] is the tag
	desc string // deterministic description computed at send (§8)
}

func (p *packet) Describe() string { return p.desc }

func raftPacket(m *raftpb.Message) *packet {
	desc := fmt.Sprintf("raft %s %d->%d t=%d i=%d lt=%d c=%d n=%d",
		m.GetType().String(), m.GetFrom(), m.GetTo(), m.GetTerm(), m.GetIndex(), m.GetLogTerm(), m.GetCommit(), len(m.GetEntries()))
	if m.GetReject() {
		desc += " rej"
	}
	if m.GetType() == raftpb.MsgSnap {
		desc += fmt.Sprintf(" snap=%d", m.GetSnapshot().GetMetadata().GetIndex())
	}
	return &packet{data: wire.EncodeRaft(m), desc: desc}
}

func opName(op kv.Op) string {
	switch op {
	case kv.OpPut:
		return "put"
	case kv.OpGet:
		return "get"
	case kv.OpConfRead:
		return "conf-read"
	case kv.OpConfChange:
		return "conf-change"
	}
	return fmt.Sprintf("op(%d)", op)
}

func requestPacket(client string, q wire.Request) *packet {
	return &packet{
		data: wire.EncodeRequest(q),
		desc: fmt.Sprintf("req %s#%d.%d %s %s", client, q.ID, q.Attempt, opName(q.Op), q.Key),
	}
}

func replyPacket(p wire.Reply) *packet {
	status := "ok"
	if p.Status == wire.StatusDropped {
		status = "dropped"
	}
	return &packet{
		data: wire.EncodeReply(p),
		desc: fmt.Sprintf("rep #%d.%d %s lead=%d", p.ID, p.Attempt, status, p.Leader),
	}
}

func snapAckPacket(index uint64) *packet {
	return &packet{data: wire.EncodeSnapAck(index), desc: fmt.Sprintf("snapack %d", index)}
}

func tagName(tag byte) string { //nolint:unused // used by the server's packet dispatch (Task 8a)
	switch tag {
	case wire.TagRaft:
		return "raft"
	case wire.TagRequest:
		return "request"
	case wire.TagReply:
		return "reply"
	case wire.TagSnapAck:
		return "snapack"
	}
	return fmt.Sprintf("tag(%d)", tag)
}
