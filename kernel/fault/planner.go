package fault

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// Roles maps role names to functions returning candidate node IDs at call time. Code in this
// package only looks roles up by name and never ranges over the map.
type Roles map[string]func() []kernel.NodeID

// PlanContext is what a planner may use. Planners change the world only through Inject.
type PlanContext struct {
	Sim     *kernel.Sim
	Rand    *rand.Rand     // the planner's stream: Sim.Rand("fault/" + Name())
	Inject  func(Event)    // applies an event now (Injector.Inject or NewPlanContext's)
	Servers []*kernel.Node // the world's servers, ascending NodeID
	Roles   Roles
	Until   kernel.Time // faults may start before Until; the recovery window follows

	Disks *simdisk.Disks // read-only use: Lookup(n), then Stat, ReadDir, Capacity; nil if unavailable
}

// Planner decides faults during a run.
type Planner interface {
	// Name returns the planner's name, matching ^[a-z0-9][a-z0-9-]*$ and not one of "inject",
	// "load", "replay". The planner's stream label is "fault/" + Name().
	Name() string
	// Start is called once per run. It schedules the planner's work with ctx.Sim.AtFront
	// (FLT-052) and returns.
	Start(ctx *PlanContext)
}

// NewPlanContext returns the PlanContext for planner p (FLT-050): its stream, an Inject that
// records p's name as the source, the servers sorted by NodeID, roles, until, and the disks.
func (in *Injector) NewPlanContext(p Planner, servers []*kernel.Node, roles Roles, until kernel.Time) *PlanContext {
	name := p.Name()
	if !validPlannerName(name) {
		panic(fmt.Sprintf("fault: NewPlanContext: invalid planner name %q", name))
	}
	return &PlanContext{
		Sim:     in.s,
		Rand:    in.s.Rand("fault/" + name),
		Inject:  func(e Event) { in.apply(e, name) },
		Servers: byID(servers),
		Roles:   roles,
		Until:   until,
		Disks:   in.d,
	}
}

// validPlannerName checks ^[a-z0-9][a-z0-9-]*$ and the reserved sources (FLT-050).
func validPlannerName(name string) bool {
	if name == "" || name == "inject" || name == "load" || name == "replay" || name[0] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// byID returns a copy of nodes sorted by ascending ID.
func byID(nodes []*kernel.Node) []*kernel.Node {
	out := slices.Clone(nodes)
	slices.SortFunc(out, func(a, b *kernel.Node) int { return int(a.ID()) - int(b.ID()) })
	return out
}

// resolveRole produces the candidate list of FLT-060, calling the role function once. who is
// "Random: rules[<i>]" or "Script: events[<i>]".
func resolveRole(ctx *PlanContext, name, who string) []*kernel.Node {
	ids := slices.Clone(ctx.Roles[name]())
	slices.Sort(ids)
	ids = slices.Compact(ids)
	out := make([]*kernel.Node, 0, len(ids))
	for _, id := range ids {
		n := ctx.Sim.Node(id)
		if n == nil {
			panic(fmt.Sprintf("fault: %s: role %q returned unknown node id %d", who, name, id))
		}
		out = append(out, n)
	}
	return out
}

// between is uniform in [lo, hi] with exactly one Int64N draw (FLT-080).
func between(r *rand.Rand, lo, hi time.Duration) time.Duration { //nolint:unused // the Random planner that comes next uses it
	return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
}

// pick draws exactly one IntN (FLT-080).
func pick(r *rand.Rand, xs []*kernel.Node) *kernel.Node { return xs[r.IntN(len(xs))] }

// names joins node names with ",".
func names(nodes []*kernel.Node) string {
	size := 0
	for _, n := range nodes {
		size += len(n.Name()) + 1
	}
	var b strings.Builder
	b.Grow(size)
	for i, n := range nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n.Name())
	}
	return b.String()
}

// emitTarget emits fault.target (FLT §8). field is "rule" or "event".
func emitTarget(ctx *PlanContext, planner, field string, i int, kind Kind, role string, cands []*kernel.Node, chosen *kernel.Node) {
	idx := strconv.Itoa(i)
	attrs := make([]kernel.Attr, 0, 5)
	attrs = append(attrs, attr("planner", planner), attr(field, idx))
	if role != "" {
		attrs = append(attrs, attr("role", role))
	}
	attrs = append(attrs, attr("candidates", names(cands)), attr("chosen", chosen.Name()))
	ctx.Sim.Emit(kernel.Record{Kind: "fault.target", Text: "target " + planner + " " + field + "s[" + idx + "] " + string(kind) + " -> " + chosen.Name(), Attrs: attrs})
}

// emitSkip emits fault.skip (FLT §8).
func emitSkip(ctx *PlanContext, planner, field string, i int, kind Kind, reason string) {
	idx := strconv.Itoa(i)
	ctx.Sim.Emit(kernel.Record{Kind: "fault.skip", Text: "skip " + planner + " " + field + "s[" + idx + "] " + string(kind) + ": " + reason,
		Attrs: []kernel.Attr{attr("planner", planner), attr(field, idx), attr("kind", string(kind)), attr("reason", reason)}})
}
