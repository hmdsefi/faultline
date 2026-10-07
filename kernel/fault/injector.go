package fault

import (
	"strconv"
	"strings"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Injector applies events to one simulated world and records the concrete schedule.
type Injector struct {
	s         *kernel.Sim
	nw        *simnet.Network
	d         *simdisk.Disks
	applied   [][]Event                        // the concrete schedule, in application order, in blocks (add)
	count     int                              // the number of applied events
	pairs     pairer                           // active set over every applied event (FLT-030 step 4)
	saved     map[[2]kernel.NodeID]simnet.Link // config before the first override; indexed only
	replaying bool
	attrs     []kernel.Attr // record's attribute buffer, reused: Emit copies the attributes
}

// NewInjector returns an injector for s. nw or d may be nil; events that need a nil component
// then panic (FLT-031). It panics if s is nil.
func NewInjector(s *kernel.Sim, nw *simnet.Network, d *simdisk.Disks) *Injector {
	if s == nil {
		panic("fault: NewInjector: nil *kernel.Sim")
	}
	return &Injector{s: s, nw: nw, d: d, saved: map[[2]kernel.NodeID]simnet.Link{}}
}

// Inject applies e now (FLT-030). e.At, e.ID and e.Undoes are ignored.
func (in *Injector) Inject(e Event) { in.apply(e, "inject") }

// Applied returns a deep copy of the concrete schedule of every event applied so far, with
// End and Recovery 0.
func (in *Injector) Applied() Schedule {
	var events []Event
	if in.count > 0 {
		events = make([]Event, 0, in.count)
		for _, block := range in.applied {
			for _, e := range block {
				events = append(events, e.clone())
			}
		}
	}
	return Schedule{Version: ScheduleVersion, Events: events}
}

// add appends a to the applied list. The list is kept in blocks of 16, 32, ... up to 256 events
// that are never reallocated, so it grows without copying the events applied so far.
func (in *Injector) add(a Event) {
	last := len(in.applied) - 1
	if last < 0 || len(in.applied[last]) == cap(in.applied[last]) {
		in.applied = append(in.applied, make([]Event, 0, 16<<min(len(in.applied), 4)))
		last++
	}
	in.applied[last] = append(in.applied[last], a)
	in.count++
}

// prefix returns the panic prefix for source (FLT-030).
func prefix(source string) string {
	switch source {
	case "inject":
		return "Inject: "
	case "load":
		return "Load: "
	case "replay":
		return "Replay: "
	}
	return "planner " + source + ": "
}

// fail panics with the message of FLT-030 for source.
func fail(source, msg string) {
	panic("fault: " + prefix(source) + msg)
}

// apply is the apply procedure of FLT-030.
func (in *Injector) apply(e Event, source string) {
	// 2. structure (also in replay mode)
	if p := e.problem(); p != "" {
		fail(source, p)
	}
	k := string(e.Kind)
	if e.Role != "" {
		fail(source, k+": role "+strconv.Quote(e.Role)+" is not resolved; only planners resolve roles")
	}
	// 1. replay mode: only the replayed schedule applies faults
	if in.replaying && source != "replay" {
		in.suppressed(source, e.String())
		return
	}
	// 3. names (Node, Peer, then Groups) and components
	var n, p *kernel.Node
	if e.Node != "" {
		n = in.lookup(e.Node, source, k)
	}
	if e.Peer != "" {
		p = in.lookup(e.Peer, source, k)
	}
	for _, g := range e.Groups {
		for _, name := range g {
			in.lookup(name, source, k)
		}
	}
	if isNet(e.Kind) && in.nw == nil {
		fail(source, k+": injector has no simnet.Network")
	}
	if isDisk(e.Kind) && in.d == nil {
		fail(source, k+": injector has no simdisk.Disks")
	}
	// 4. the applied event (its Undoes are replaced, so clone does not copy them)
	e.Undoes = nil
	a := e.clone()
	a.At = in.s.Now()
	a.Role = ""
	a.ID = in.count + 1
	a.Undoes = in.pairs.pair(a)
	in.add(a)
	// 5. effect, 6. record, 7. call
	applied := in.effect(a, n, p)
	in.record(a, source, n, applied)
	if !applied {
		return
	}
	if err := in.call(a, n, p); err != nil {
		in.emit(n, "fault.error", k+" "+strconv.Itoa(a.ID)+" failed: "+err.Error(),
			attr("id", strconv.Itoa(a.ID)), attr("error", err.Error()))
	}
}

// lookup returns the node named name, or panics with FLT-030's unknown-node message.
func (in *Injector) lookup(name, source, k string) *kernel.Node {
	n := in.s.Lookup(name)
	if n == nil {
		fail(source, k+": unknown node "+strconv.Quote(name))
	}
	return n
}

// effect decides whether a is applied or a no-op (FLT-032), without changing anything.
func (in *Injector) effect(a Event, n, p *kernel.Node) bool {
	switch a.Kind {
	case KindLinkReset:
		_, ok := in.saved[[2]kernel.NodeID{n.ID(), p.ID()}]
		return ok
	case KindCrash:
		st := n.State()
		return st == kernel.NodeUp || st == kernel.NodePaused || st == kernel.NodeDown && n.Incarnation() == 0
	case KindRestart:
		return n.State() == kernel.NodeDown
	case KindPause:
		return n.State() == kernel.NodeUp
	case KindResume:
		return n.State() == kernel.NodePaused
	}
	return true
}

// call performs the per-kind call of FLT-032. It returns Corrupt's error.
func (in *Injector) call(a Event, n, p *kernel.Node) error {
	switch a.Kind {
	case KindPartition:
		groups := make([][]kernel.NodeID, len(a.Groups))
		for i, g := range a.Groups {
			for _, name := range g {
				groups[i] = append(groups[i], in.s.Lookup(name).ID())
			}
		}
		in.nw.Partition(groups...)
	case KindIsolate:
		in.nw.Isolate(n.ID())
	case KindCut:
		in.nw.Cut(n.ID(), p.ID())
	case KindHeal:
		in.nw.Heal()
	case KindHealLink:
		in.nw.HealLink(n.ID(), p.ID())
	case KindLink:
		key := [2]kernel.NodeID{n.ID(), p.ID()}
		if _, ok := in.saved[key]; !ok {
			in.saved[key] = in.nw.Link(n.ID(), p.ID())
		}
		in.nw.SetLink(n.ID(), p.ID(), *a.Link)
	case KindLinkReset:
		key := [2]kernel.NodeID{n.ID(), p.ID()}
		if saved := in.saved[key]; saved == in.nw.Config().Default {
			in.nw.ResetLink(n.ID(), p.ID())
		} else {
			in.nw.SetLink(n.ID(), p.ID(), saved)
		}
		delete(in.saved, key)
	case KindCrash:
		n.Crash()
	case KindRestart:
		n.Restart()
	case KindPause:
		n.Pause()
	case KindResume:
		n.Resume()
	case KindClockJump:
		n.JumpClock(time.Duration(a.N))
	case KindClockDrift:
		n.SetDrift(int32(a.N)) //nolint:gosec // in [MinDriftPPM, MaxDriftPPM]: apply validated the event
	case KindSyncFail:
		in.d.Volume(n).FailSyncs(int(a.N))
	case KindDiskCapacity:
		in.d.Volume(n).SetCapacity(a.N)
	case KindCorrupt:
		return in.d.Volume(n).Corrupt(a.Path, a.Off, a.Len)
	}
	return nil
}

// record emits the fault.<kind> record of an applied event (FLT-070).
func (in *Injector) record(a Event, source string, n *kernel.Node, applied bool) {
	attrs := in.attrs[:0]
	attrs = append(attrs, attr("id", strconv.Itoa(a.ID)), attr("source", source))
	set, _ := fieldSet(a.Kind)
	if set&fNode != 0 {
		attrs = append(attrs, attr("node", a.Node))
	}
	if set&fPeer != 0 {
		attrs = append(attrs, attr("peer", a.Peer))
	}
	if set&fGroups != 0 {
		attrs = append(attrs, attr("groups", groupsAttr(a.Groups)))
	}
	if set&fLink != 0 {
		l := a.Link
		attrs = append(attrs,
			attr("latency_ns", strconv.FormatInt(int64(l.Latency), 10)),
			attr("jitter_ns", strconv.FormatInt(int64(l.Jitter), 10)),
			attr("tail_ppm", strconv.FormatUint(uint64(l.TailPPM), 10)),
			attr("tail_ns", strconv.FormatInt(int64(l.Tail), 10)),
			attr("drop_ppm", strconv.FormatUint(uint64(l.DropPPM), 10)),
			attr("dup_ppm", strconv.FormatUint(uint64(l.DupPPM), 10)),
			attr("fifo", strconv.FormatBool(l.FIFO)))
	}
	if set&fN != 0 {
		attrs = append(attrs, attr("n", strconv.FormatInt(a.N, 10)))
	}
	if set&fPath != 0 {
		attrs = append(attrs, attr("path", a.Path))
	}
	if set&fOff != 0 {
		attrs = append(attrs, attr("off", strconv.FormatInt(a.Off, 10)))
	}
	if set&fLen != 0 {
		attrs = append(attrs, attr("len", strconv.Itoa(a.Len)))
	}
	if len(a.Undoes) > 0 {
		attrs = append(attrs, attr("undoes", joinIDs(a.Undoes)))
	}
	text, effect := a.String(), "applied"
	if !applied {
		text, effect = text+" (noop)", "noop"
	}
	attrs = append(attrs, attr("effect", effect))
	in.attrs = attrs
	in.emit(n, recordKind(a.Kind), text, attrs...)
}

// joinIDs formats ids as "1,2,3".
func joinIDs(ids []int) string {
	if len(ids) == 1 {
		return strconv.Itoa(ids[0])
	}
	var buf [64]byte
	b := buf[:0]
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, int64(id), 10)
	}
	return string(b)
}

// recordKind returns the record kind of k (FLT-070): "fault." and k with "-" replaced by "_".
func recordKind(k Kind) string {
	switch k {
	case KindPartition:
		return "fault.partition"
	case KindIsolate:
		return "fault.isolate"
	case KindCut:
		return "fault.cut"
	case KindHeal:
		return "fault.heal"
	case KindHealLink:
		return "fault.heal_link"
	case KindLink:
		return "fault.link"
	case KindLinkReset:
		return "fault.link_reset"
	case KindCrash:
		return "fault.crash"
	case KindRestart:
		return "fault.restart"
	case KindPause:
		return "fault.pause"
	case KindResume:
		return "fault.resume"
	case KindClockJump:
		return "fault.clock_jump"
	case KindClockDrift:
		return "fault.clock_drift"
	case KindSyncFail:
		return "fault.sync_fail"
	case KindDiskCapacity:
		return "fault.disk_capacity"
	case KindCorrupt:
		return "fault.corrupt"
	}
	return "fault." + strings.ReplaceAll(string(k), "-", "_")
}

// emit emits a record on n (if the kind's field set has Node) or globally.
func (in *Injector) emit(n *kernel.Node, kind, text string, attrs ...kernel.Attr) {
	r := kernel.Record{Kind: kind, Text: text, Attrs: attrs}
	if n != nil {
		r.Node = n.ID()
	}
	in.s.Emit(r)
}

// suppressed emits fault.suppressed (FLT-043).
func (in *Injector) suppressed(source, detail string) {
	in.s.Emit(kernel.Record{Kind: "fault.suppressed", Text: "suppressed (replay): " + detail,
		Attrs: []kernel.Attr{attr("source", source), attr("detail", detail)}})
}

func attr(key, value string) kernel.Attr { return kernel.Attr{Key: key, Value: value} }
