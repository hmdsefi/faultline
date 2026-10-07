package fault

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Shape is a partition shape of a KindPartition rule. FLT declares it so Rule can carry one; it
// has only Name. PPL (Phase 3) defines the shape types and the PlanShape interface (Shape plus
// Check and Split) that Random requires of a non-nil Shape (PPL-001). A nil Shape selects the
// Phase 1 split (FLT-085).
type Shape interface {
	Name() string
}

// Rule is one fault rule of Random.
type Rule struct {
	Kind   Kind
	Every  time.Duration // mean gap between starts; actual gap uniform in [Every/2, Every+Every/2]
	MinFor time.Duration // automatic undo after a duration uniform in [MinFor, MaxFor];
	MaxFor time.Duration // MinFor = MaxFor = 0: the fault lasts until the recovery window
	Target string        // role name; "" = a uniformly random eligible server

	Shape     Shape        // KindPartition only; nil = Phase 1 split (FLT-085); PPL defines others
	Magnitude int64        // per-kind amount (FLT-083); 0 where not used
	Link      *simnet.Link // KindLink only: the config to apply
	Path      string       // KindCorrupt only: the file to damage
}

// Random starts faults at random times following its rules and ends them all when its
// recovery window starts, at PlanContext.Until (FLT-080 to FLT-089). A value may be reused
// across runs: Start resets all run state.
type Random struct {
	Rules   []Rule
	MaxDown int           // max servers down or paused at once for crash/pause rules; 0 = (n-1)/2
	Quiet   time.Duration // recovery window length; read by API-044 to set Until, not by Random

	// run state, reset by Start (FLT-087)
	ctx          *PlanContext
	rules        []Rule
	recoverAt    kernel.Time
	maxDown      int
	netBusyUntil kernel.Time
	active       []*randomEntry
	linkMarks    map[[2]kernel.NodeID]bool // indexed only
	occurs       []func()                  // occurs[i] runs an occurrence of rule i

	// buffers that occurrences reuse to allocate less; no content outlives one occurrence
	servers, peers []*kernel.Node
	faults         []Event
}

// randomEntry is one started fault whose undos are still owed.
type randomEntry struct {
	rule    int
	undos   []Event
	link    [2]kernel.NodeID
	hasLink bool
}

// Name returns "random".
func (r *Random) Name() string { return "random" }

// RecoverAt returns the start of the recovery window of the last Start, or 0 before Start.
func (r *Random) RecoverAt() kernel.Time { return r.recoverAt }

// Start validates r against ctx and schedules the first occurrence of every rule and the
// recovery. It panics on invalid configuration (FLT-081).
func (r *Random) Start(ctx *PlanContext) {
	switch {
	case ctx.Sim == nil:
		panic("fault: Random: PlanContext.Sim is nil")
	case ctx.Rand == nil:
		panic("fault: Random: PlanContext.Rand is nil")
	case ctx.Inject == nil:
		panic("fault: Random: PlanContext.Inject is nil")
	case r.ctx != nil && r.ctx.Sim == ctx.Sim:
		panic("fault: Random: started twice on the same simulation")
	case r.MaxDown < 0:
		panic("fault: Random: MaxDown must be >= 0 (got " + strconv.Itoa(r.MaxDown) + ")")
	}
	for i, rule := range r.Rules {
		if p := rule.problem(ctx); p != "" {
			panic(fmt.Sprintf("fault: Random: rules[%d]: %s", i, p))
		}
	}
	now := ctx.Sim.Now()
	r.ctx = ctx
	r.rules = slices.Clone(r.Rules)
	r.recoverAt = max(ctx.Until, now)
	r.maxDown = r.MaxDown
	if r.maxDown == 0 {
		r.maxDown = max(len(ctx.Servers)-1, 0) / 2
	}
	r.netBusyUntil = 0
	r.active = nil
	r.linkMarks = map[[2]kernel.NodeID]bool{}
	r.occurs = make([]func(), len(r.rules))
	for i := range r.rules {
		r.occurs[i] = func() { r.occur(i) }
		r.scheduleNext(i, now)
	}
	ctx.Sim.AtFront(r.recoverAt, "fault/random/recover", r.recover)
}

// magnitudeRange returns the Magnitude range of FLT-083 for k; used is false for range "0".
func magnitudeRange(k Kind) (lo, hi int64, text string, used bool) {
	switch k {
	case KindClockJump:
		return 1, int64(MaxRuleDuration), "[1, 36000000000000000]", true
	case KindClockDrift:
		return 1, 500000, "[1, 500000]", true
	case KindSyncFail:
		return 0, 2147483647, "[0, 2147483647]", true
	case KindDiskCapacity:
		return 1, 9223372036854775807, "[1, 9223372036854775807]", true
	case KindCorrupt:
		return 0, 1048576, "[0, 1048576]", true
	}
	return 0, 0, "", false
}

// problem returns the first rule problem of FLT-081, or "".
func (rule Rule) problem(ctx *PlanContext) string {
	k := string(rule.Kind)
	if _, ok := fieldSet(rule.Kind); !ok {
		return "unknown kind " + strconv.Quote(k)
	}
	switch rule.Kind {
	case KindHeal, KindHealLink, KindLinkReset, KindRestart, KindResume:
		return "kind " + strconv.Quote(k) + " is not a rule kind"
	}
	if rule.Every <= 0 || rule.Every > MaxRuleDuration {
		return "Every must be in (0s, 10000h0m0s] (got " + rule.Every.String() + ")"
	}
	if rule.MinFor < 0 || rule.MinFor > rule.MaxFor || rule.MaxFor > MaxRuleDuration {
		return "MinFor and MaxFor must satisfy 0 <= MinFor <= MaxFor <= 10000h0m0s (got MinFor=" +
			rule.MinFor.String() + " MaxFor=" + rule.MaxFor.String() + ")"
	}
	switch rule.Kind {
	case KindClockJump, KindClockDrift, KindCorrupt:
		if rule.MaxFor != 0 {
			return k + " has no undo; MinFor and MaxFor must be 0"
		}
	}
	if rule.Target != "" && ctx.Roles[rule.Target] == nil {
		return "unknown role " + strconv.Quote(rule.Target)
	}
	if rule.Shape != nil && rule.Kind != KindPartition {
		return "Shape is only allowed for partition"
	}
	if rule.Shape != nil {
		return "shape " + strconv.Quote(rule.Shape.Name()) + " requires the partition planner (Phase 3)"
	}
	if rule.Link != nil && rule.Kind != KindLink {
		return "Link is only allowed for link"
	}
	if rule.Kind == KindLink && rule.Link == nil {
		return "link requires Link"
	}
	if rule.Kind == KindLink {
		if err := rule.Link.Validate(); err != nil {
			return "link: " + err.Error()
		}
	}
	if rule.Path != "" && rule.Kind != KindCorrupt {
		return "Path is only allowed for corrupt"
	}
	if rule.Kind == KindCorrupt && rule.Path == "" {
		return "corrupt requires Path"
	}
	if rule.Kind == KindCorrupt && ctx.Disks == nil {
		return "corrupt requires PlanContext.Disks"
	}
	lo, hi, text, used := magnitudeRange(rule.Kind)
	if !used && rule.Magnitude != 0 {
		return "Magnitude is not allowed for " + k
	}
	if used && (rule.Magnitude < lo || rule.Magnitude > hi) {
		return k + " requires Magnitude in " + text + " (got " + strconv.FormatInt(rule.Magnitude, 10) + ")"
	}
	return ""
}

// scheduleNext draws the next gap of rule i from `from` (FLT-082).
func (r *Random) scheduleNext(i int, from kernel.Time) {
	every := r.rules[i].Every
	t := from.Add(between(r.ctx.Rand, every/2, every+every/2))
	if t < r.recoverAt {
		r.ctx.Sim.AtFront(t, "fault/random/start", r.occurs[i])
	}
}

// occur is one occurrence of rule i (FLT-084).
func (r *Random) occur(i int) {
	now := r.ctx.Sim.Now()
	r.fire(i, now)
	r.scheduleNext(i, now)
}

// fire runs FLT-084 up to the label "next".
func (r *Random) fire(i int, now kernel.Time) {
	ctx, rule := r.ctx, r.rules[i]
	cands := r.candidates(i, rule)
	net := rule.Kind == KindPartition || rule.Kind == KindIsolate || rule.Kind == KindCut
	if net && now < r.netBusyUntil {
		r.skip(i, rule.Kind, "network-busy")
		return
	}
	var faults, undos []Event
	var linkKey [2]kernel.NodeID
	if rule.Kind == KindPartition {
		if len(cands) < 2 {
			r.skip(i, rule.Kind, "too-few-nodes")
			return
		}
		var reason string
		faults, undos, reason = r.shape(rule, cands)
		if reason != "" {
			r.skip(i, rule.Kind, reason)
			return
		}
	} else {
		if len(cands) == 0 {
			r.skip(i, rule.Kind, "no-target")
			return
		}
		if (rule.Kind == KindCrash || rule.Kind == KindPause) && r.downCount() >= r.maxDown {
			cands = slices.DeleteFunc(cands, r.isServer)
			if len(cands) == 0 {
				r.skip(i, rule.Kind, "max-down")
				return
			}
		}
		n := pick(ctx.Rand, cands)
		emitTarget(ctx, "random", "rule", i, rule.Kind, rule.Target, cands, n)
		f, u, key, reason := r.build(rule, n)
		if reason != "" {
			r.skip(i, rule.Kind, reason)
			return
		}
		r.faults = append(r.faults[:0], f)
		faults, linkKey = r.faults, key
		if u.Kind != "" {
			undos = []Event{u}
		}
	}
	hasUndo := len(undos) > 0 && rule.MaxFor > 0
	end := r.recoverAt
	if hasUndo {
		end = min(now.Add(between(ctx.Rand, rule.MinFor, rule.MaxFor)), r.recoverAt)
	}
	for _, f := range faults {
		ctx.Inject(f)
	}
	if net {
		r.netBusyUntil = max(r.netBusyUntil, end)
	}
	if len(undos) == 0 {
		return // clock-jump, clock-drift, corrupt: nothing to track
	}
	entry := &randomEntry{rule: i, undos: undos}
	r.active = append(r.active, entry)
	if rule.Kind == KindLink {
		entry.link, entry.hasLink = linkKey, true
		r.linkMarks[linkKey] = true
	}
	if end < r.recoverAt {
		ctx.Sim.AtFront(end, "fault/random/undo", func() {
			for _, u := range entry.undos {
				ctx.Inject(u)
			}
			r.active = slices.DeleteFunc(r.active, func(e *randomEntry) bool { return e == entry })
			if entry.hasLink {
				delete(r.linkMarks, entry.link)
			}
		})
	}
}

// candidates returns the eligible candidates of rule i (FLT-084): the role's nodes (FLT-060) or
// the servers, ascending ID, deduplicated, filtered by node state.
func (r *Random) candidates(i int, rule Rule) []*kernel.Node {
	var cands []*kernel.Node
	if rule.Target != "" {
		cands = resolveRole(r.ctx, rule.Target, "Random: rules["+strconv.Itoa(i)+"]")
	} else {
		r.servers = sortByID(r.servers, r.ctx.Servers)
		cands = slices.CompactFunc(r.servers, func(a, b *kernel.Node) bool { return a.ID() == b.ID() })
	}
	return slices.DeleteFunc(cands, func(n *kernel.Node) bool {
		switch st := n.State(); rule.Kind {
		case KindPartition:
			return false
		case KindCrash, KindPause:
			return st != kernel.NodeUp
		default:
			return st != kernel.NodeUp && st != kernel.NodePaused
		}
	})
}

// sortByID returns nodes sorted by ascending ID in buf, as byID does, reusing buf's array.
func sortByID(buf, nodes []*kernel.Node) []*kernel.Node {
	buf = append(buf[:0], nodes...)
	slices.SortFunc(buf, func(a, b *kernel.Node) int { return int(a.ID()) - int(b.ID()) })
	return buf
}

// isServer reports membership in ctx.Servers, by ID.
func (r *Random) isServer(n *kernel.Node) bool {
	return slices.ContainsFunc(r.ctx.Servers, func(s *kernel.Node) bool { return s.ID() == n.ID() })
}

// downCount is the number of servers that are down or paused.
func (r *Random) downCount() int {
	c := 0
	for _, s := range r.ctx.Servers {
		if st := s.State(); st == kernel.NodeDown || st == kernel.NodePaused {
			c++
		}
	}
	return c
}

// build returns the fault of a non-partition rule for target n (FLT-083), its undo (Kind "" if
// none), the link key for link rules, and a skip reason.
func (r *Random) build(rule Rule, n *kernel.Node) (f, u Event, key [2]kernel.NodeID, reason string) {
	ctx, M := r.ctx, rule.Magnitude
	ev := func(k Kind) Event { return Event{Kind: k, Node: n.Name()} }
	switch rule.Kind {
	case KindIsolate:
		return ev(KindIsolate), Event{Kind: KindHeal}, key, ""
	case KindCut, KindLink:
		r.peers = sortByID(r.peers, ctx.Servers)
		peers := slices.DeleteFunc(r.peers, func(p *kernel.Node) bool {
			return p.ID() == n.ID() || rule.Kind == KindLink && r.linkMarks[[2]kernel.NodeID{n.ID(), p.ID()}]
		})
		if len(peers) == 0 {
			return Event{}, Event{}, key, "no-peer"
		}
		p := pick(ctx.Rand, peers)
		f = Event{Kind: rule.Kind, Node: n.Name(), Peer: p.Name()}
		u = Event{Kind: KindHealLink, Node: n.Name(), Peer: p.Name()}
		if rule.Kind == KindLink {
			l := *rule.Link
			f.Link = &l
			u.Kind = KindLinkReset
		}
		return f, u, [2]kernel.NodeID{n.ID(), p.ID()}, ""
	case KindCrash:
		return ev(KindCrash), ev(KindRestart), key, ""
	case KindPause:
		return ev(KindPause), ev(KindResume), key, ""
	case KindClockJump:
		f = ev(KindClockJump)
		f.N = 1 + ctx.Rand.Int64N(M)
		if ctx.Rand.IntN(2) == 1 {
			f.N = -f.N
		}
		return f, u, key, ""
	case KindClockDrift:
		f = ev(KindClockDrift)
		f.N = ctx.Rand.Int64N(2*M+1) - M
		return f, u, key, ""
	case KindSyncFail:
		f, u = ev(KindSyncFail), ev(KindSyncFail)
		f.N = max(M, 1)
		return f, u, key, ""
	case KindDiskCapacity:
		f, u = ev(KindDiskCapacity), ev(KindDiskCapacity)
		f.N = M
		return f, u, key, ""
	case KindCorrupt:
		// Lookup never creates a volume, so a skipped occurrence changes nothing (FLT-044).
		v, ok := ctx.Disks.Lookup(n)
		if !ok {
			return Event{}, Event{}, key, "no-file"
		}
		// Corrupt damages only synced bytes (DSK-033), so the range is drawn within the durable
		// size. An error means that Path names no file, or a directory.
		durable, err := v.DurableSize(rule.Path)
		if err != nil {
			return Event{}, Event{}, key, "no-file"
		}
		f = ev(KindCorrupt)
		f.Path, f.Len = rule.Path, int(max(M, 1))
		if durable < int64(f.Len) {
			return Event{}, Event{}, key, "file-too-small"
		}
		f.Off = ctx.Rand.Int64N(durable - int64(f.Len) + 1)
		return f, u, key, ""
	}
	panic("fault: internal error: no rule kind " + string(rule.Kind))
}

// shape returns the faults, undos and skip reason of a partition occurrence (FLT-089). In
// Phase 1 Start rejects non-nil shapes, so only the split of FLT-085 is reachable.
func (r *Random) shape(rule Rule, cands []*kernel.Node) ([]Event, []Event, string) {
	if rule.Shape != nil {
		panic("fault: internal error: shape " + strconv.Quote(rule.Shape.Name()) + " without the partition planner")
	}
	faults, undos := split(r.ctx, cands)
	return faults, undos, ""
}

// split is the Phase 1 split of FLT-085; cands has at least 2 nodes in ascending ID order.
func split(ctx *PlanContext, cands []*kernel.Node) (faults, undos []Event) {
	ids := make([]kernel.NodeID, len(cands))
	for i, n := range cands {
		ids[i] = n.ID()
	}
	perm := slices.Clone(ids)
	ctx.Rand.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
	k := 1 + ctx.Rand.IntN(len(perm)-1)
	g0, g1 := slices.Clone(perm[:k]), slices.Clone(perm[k:])
	for _, x := range ctx.Sim.Nodes() {
		if slices.Contains(ids, x.ID()) {
			continue
		}
		if ctx.Rand.IntN(2) == 0 {
			g0 = append(g0, x.ID())
		} else {
			g1 = append(g1, x.ID())
		}
	}
	slices.Sort(g0)
	slices.Sort(g1)
	if g1[0] < g0[0] {
		g0, g1 = g1, g0
	}
	return []Event{{Kind: KindPartition, Groups: [][]string{nodeNames(ctx, g0), nodeNames(ctx, g1)}}},
		[]Event{{Kind: KindHeal}}
}

func nodeNames(ctx *PlanContext, ids []kernel.NodeID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = ctx.Sim.Node(id).Name()
	}
	return out
}

// skip emits fault.skip for rule i.
func (r *Random) skip(i int, kind Kind, reason string) {
	emitSkip(r.ctx, "random", "rule", i, kind, reason)
}

// recover is the recovery of FLT-086.
func (r *Random) recover() {
	ctx := r.ctx
	ctx.Sim.Emit(kernel.Record{Kind: "fault.recover", Text: "recover random: " + strconv.Itoa(len(r.active)) + " active",
		Attrs: []kernel.Attr{attr("planner", "random"), attr("active", strconv.Itoa(len(r.active)))}})
	for _, cat := range []Kind{KindHeal, KindHealLink, KindLinkReset, KindSyncFail, KindDiskCapacity, KindResume, KindRestart} {
		for _, e := range r.active {
			for _, u := range e.undos {
				if u.Kind == cat {
					ctx.Inject(u)
				}
			}
		}
	}
	r.active = nil
	r.linkMarks = map[[2]kernel.NodeID]bool{}
}
