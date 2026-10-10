package etcdraft

import (
	"fmt"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"

	"github.com/hmdsefi/faultline/harness/etcdraft/internal/kv"
	"github.com/hmdsefi/faultline/harness/etcdraft/internal/wire"
)

type clientKind uint8

const (
	workloadClient clientKind = iota
	probeClient
)

// clientOp is a client's pending operation.
type clientOp struct {
	hid     int64 // history op ID
	req     wire.Request
	timeout time.Duration            // OpTimeout of this operation
	done    func(rep wire.Reply)     // continuation after StatusOK (history already completed)
	output  func(rep wire.Reply) any // history output on StatusOK
}

// client is the harness state of one client node across incarnations.
type client struct {
	c      *Cluster
	node   *kernel.Node
	name   string
	cid    uint32
	kind   clientKind
	nextID uint64 // last op ID used; survives crashes (ETC-092 step 3)

	op      *clientOp // pending operation; completed as Info by the crash hook
	hint    uint64    // leader hint (raft ID), 0 = none
	exclude uint64    // server to skip in the next pick after an attempt timeout
	attempt kernel.EventID
	opTimer kernel.EventID
	retry   kernel.EventID
}

func (cl *client) now() kernel.Time { return cl.c.w.Sim.Now() }

// boot is the client's kernel.BootFunc.
func (cl *client) boot(n *kernel.Node) {
	cl.hint, cl.exclude, cl.op = 0, 0, nil
	cl.c.w.Net.Handle(n, cl.onPacket)
	switch cl.kind {
	case workloadClient:
		cl.think()
	case probeClient:
		cl.probeBoot()
	}
}

// think is ETC-092 step 1.
func (cl *client) think() {
	w := cl.c.cfg.Workload
	if w.ThinkMax > 0 {
		cl.node.After(kernel.Uniform(cl.node.Rand(), w.ThinkMin, w.ThinkMax), "etcdraft/think", cl.startOp)
		return
	}
	cl.node.Post("etcdraft/think", cl.startOp)
}

// startOp is ETC-092 steps 2 to 5.
func (cl *client) startOp() {
	c := cl.c
	if cl.now() >= c.drainStart {
		return
	}
	r := cl.node.Rand()
	read := kernel.Chance(r, c.cfg.Workload.ReadPPM)
	key := "k" + strconv.Itoa(r.IntN(c.cfg.Workload.Keys))
	cl.nextID++
	id := cl.nextID
	if read {
		hid := c.w.History.Invoke(cl.name, "read", KVInput{Key: key})
		cl.begin(&clientOp{hid: hid, req: wire.Request{Client: cl.cid, ID: id, Op: kv.OpGet, Key: key}, timeout: c.cfg.Workload.OpTimeout,
			output: func(rep wire.Reply) any {
				if rep.Found {
					return rep.Value
				}
				return nil
			},
			done: func(wire.Reply) { cl.think() }})
		return
	}
	value := cl.name + "-" + strconv.FormatUint(id, 10)
	hid := c.w.History.Invoke(cl.name, "write", KVInput{Key: key, Value: value})
	cl.begin(&clientOp{hid: hid, req: wire.Request{Client: cl.cid, ID: id, Op: kv.OpPut, Key: key, Value: value}, timeout: c.cfg.Workload.OpTimeout,
		output: func(wire.Reply) any { return nil },
		done:   func(wire.Reply) { cl.think() }})
}

// begin arms the op timer and sends attempt 1 (ETC-092 step 4 and 5).
func (cl *client) begin(op *clientOp) {
	cl.op = op
	cl.c.opIDs[op.hid] = op.req.ID
	cl.opTimer = cl.node.After(op.timeout, "etcdraft/op-timeout", cl.onOpTimeout)
	cl.sendAttempt()
}

// draining reports whether the client may send no new attempts (ETC-095).
func (cl *client) draining() bool {
	return cl.kind != probeClient && cl.now() >= cl.c.drainStart
}

// sendAttempt sends the next attempt of the pending operation (ETC-095).
func (cl *client) sendAttempt() {
	c, op := cl.c, cl.op
	var target uint64
	if cl.hint != 0 && c.server(cl.hint) != nil {
		target = cl.hint
	} else {
		ids := make([]uint64, 0, len(c.servers))
		for _, s := range c.servers {
			if cl.exclude != 0 && s.id == cl.exclude && len(c.servers) >= 2 {
				continue
			}
			ids = append(ids, s.id)
		}
		target = ids[cl.node.Rand().IntN(len(ids))]
	}
	cl.exclude = 0
	op.req.Attempt++
	c.w.Net.Send(cl.node, c.server(target).node.ID(), requestPacket(cl.name, op.req))
	cl.attempt = cl.node.After(c.cfg.Workload.AttemptTimeout, "etcdraft/attempt-timeout", func() { cl.onAttemptTimeout(target) })
}

func (cl *client) onAttemptTimeout(target uint64) {
	cl.hint, cl.exclude = 0, target
	if cl.op == nil || cl.draining() {
		return
	}
	cl.sendAttempt()
}

func (cl *client) onOpTimeout() {
	op := cl.op
	if op == nil {
		return
	}
	cl.stopTimers()
	cl.op = nil
	cl.c.w.History.Complete(op.hid, history.Info, nil)
	cl.next()
}

// next continues after an operation ended without StatusOK.
func (cl *client) next() {
	if cl.kind == workloadClient {
		cl.think()
	}
}

func (cl *client) stopTimers() {
	cl.node.Cancel(cl.attempt)
	cl.node.Cancel(cl.opTimer)
	cl.node.Cancel(cl.retry)
}

// onPacket is the client's network handler (ETC-095, ETC-098).
func (cl *client) onPacket(from kernel.NodeID, payload any) {
	c := cl.c
	sender := c.w.Sim.Node(from).Name()
	p, ok := payload.(*packet)
	if !ok {
		c.harnessError(cl.node, fmt.Errorf("unexpected payload type %T from %s", payload, sender))
		return
	}
	if p.data[0] != wire.TagReply {
		c.harnessError(cl.node, fmt.Errorf("client %s: unexpected tag %s", cl.name, tagName(p.data[0])))
		return
	}
	rep, err := wire.DecodeReply(p.data)
	if err != nil {
		c.harnessError(cl.node, fmt.Errorf("decode reply from %s: %w", sender, err))
		return
	}
	op := cl.op
	if op == nil || rep.ID != op.req.ID {
		return
	}
	switch {
	case rep.Status == wire.StatusOK:
		cl.stopTimers()
		cl.op = nil
		cl.hint = rep.Leader
		c.w.History.Complete(op.hid, history.OK, op.output(rep))
		op.done(rep)
	case rep.Status == wire.StatusDropped && rep.Attempt == op.req.Attempt:
		cl.node.Cancel(cl.attempt)
		cl.node.Cancel(cl.retry) // a duplicated Dropped reply must not schedule a second retry
		cl.hint = rep.Leader
		if cl.draining() {
			return
		}
		cl.retry = cl.node.After(c.cfg.Workload.RetryDelay, "etcdraft/retry", func() {
			if !cl.draining() { // the drain may have started during RetryDelay: the operation stays pending (ETC-095)
				cl.sendAttempt()
			}
		})
	}
}

// probeBoot implements ETC-096.
func (cl *client) probeBoot() {
	c := cl.c
	if c.probeOp != 0 {
		return // already invoked in an earlier incarnation
	}
	if rs := c.w.RecoveryStart(); rs != c.w.End() && rs > c.recoveryStart {
		c.harnessError(cl.node, fmt.Errorf("fault recovery starts at %s, after the harness recovery start %s; set the planner's Quiet to at least Config.Quiet", rs, c.recoveryStart))
		return
	}
	cl.node.After(max(c.recoveryStart.Sub(cl.now()), 0), "etcdraft/probe", cl.probeWrite)
}

func (cl *client) probeWrite() {
	c := cl.c
	cl.nextID++
	c.probeOp = cl.nextID
	hid := c.w.History.Invoke(cl.name, "write", KVInput{Key: "probe", Value: "probe"})
	cl.begin(&clientOp{hid: hid, req: wire.Request{Client: cl.cid, ID: cl.nextID, Op: kv.OpPut, Key: "probe", Value: "probe"},
		timeout: c.cfg.ProgressWithin,
		output:  func(wire.Reply) any { return nil },
		done: func(wire.Reply) {
			c.probeAcked = true
			c.probeAckedAt = cl.now()
		}})
}

// onCrash completes the pending operation as Info (ETC-097).
func (cl *client) onCrash() {
	if cl.op != nil {
		cl.c.w.History.Complete(cl.op.hid, history.Info, nil)
		cl.op = nil
	}
}
