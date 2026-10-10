package etcdraft

import (
	"errors"
	"fmt"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

// waiterKey identifies a client operation awaiting its reply.
type waiterKey struct {
	client uint32
	id     uint64
}

// waiter is where to send the reply of an applied operation (ETC-093).
type waiter struct {
	attempt uint32
	from    kernel.NodeID
}

// onRequest implements ETC-093. It returns false after a harness error.
func (inc *incarnation) onRequest(from kernel.NodeID, sender string, p *packet) bool {
	q, err := wire.DecodeRequest(p.data)
	if err != nil {
		inc.harnessError(fmt.Errorf("decode request from %s: %w", sender, err))
		return false
	}
	what := "propose" // the raft call, for the harness error
	switch q.Op {
	case kv.OpConfChange:
		cc := new(raftpb.ConfChangeV2)
		if err := proto.Unmarshal(q.Data, cc); err != nil {
			inc.harnessError(&decodeError{text: "conf change decode: undecodable request from " + sender, cause: err})
			return false
		}
		cc.Context = kv.EncodeContext(q.Client, q.ID)
		what, err = "propose conf change", inc.rn.ProposeConfChange(cc)
	default:
		err = inc.rn.Propose(kv.EncodeCommand(kv.Command{Client: q.Client, ID: q.ID, Op: q.Op, Key: q.Key, Value: q.Value}))
	}
	switch {
	case err == nil:
		inc.waiters[waiterKey{q.Client, q.ID}] = waiter{attempt: q.Attempt, from: from}
	case errors.Is(err, raft.ErrProposalDropped):
		inc.c.w.Net.Send(inc.n, from, replyPacket(wire.Reply{ID: q.ID, Attempt: q.Attempt, Status: wire.StatusDropped, Leader: inc.rn.BasicStatus().Lead}))
	default:
		inc.harnessError(fmt.Errorf("%s: %w", what, err))
		return false
	}
	return true
}

// reply answers the waiter of (client, id), if any (ETC-094).
func (inc *incarnation) reply(client uint32, id uint64, res kv.Result, data []byte) {
	k := waiterKey{client, id}
	wt, ok := inc.waiters[k]
	if !ok {
		return
	}
	delete(inc.waiters, k)
	rep := wire.Reply{ID: id, Attempt: wt.attempt, Status: wire.StatusOK, Leader: inc.rn.BasicStatus().Lead, Found: res.Found, Value: res.Value, Data: data}
	inc.c.w.Net.Send(inc.n, wt.from, replyPacket(rep))
}
