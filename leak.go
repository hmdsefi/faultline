// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package faultline

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"regexp"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing/synctest"
	"time"

	"github.com/hmdsefi/faultline/artifact"
	"github.com/hmdsefi/faultline/kernel"
)

const (
	drainFirstChunk = 100 * time.Millisecond // GOR-066
	drainMaxChunk   = 10 * time.Second
	stackBufMin     = 1 << 20 // GOR-067
	stackBufMax     = 64 << 20
)

var (
	// goroutineStart is the first line of a goroutine block in runtime.Stack output (GOR-067).
	goroutineStart = regexp.MustCompile(`^goroutine \d+ \[`)
	// bubbleHeader matches a block header that names a synctest bubble: the goroutine ID, the
	// status text before the bubble ID, and the bubble ID. The bubble ID comes last in the
	// brackets; with GODEBUG=tracebacklabels=1, go1.26 prints the labels after it inside the
	// brackets and go1.27 after them, so the first match before a "]" is the goroutine's bubble.
	bubbleHeader = regexp.MustCompile(`^goroutine (\d+) \[([^\]]*?)synctest bubble (\d+)`)
)

// leakReport is the result of GOR-068 for one attempt.
type leakReport struct {
	total        int                  // countBubble() at E3
	drain        time.Duration        // Options.Drain
	text         string               // GOR-068 report text (no trailing newline)
	groups       []artifact.LeakGroup // ascending (node ID, incarnation)
	unattributed int
	stacks       string // goroutines.txt content
}

// headline is the leak failure's headline (API-067, GOR-069).
func (rep *leakReport) headline() string {
	return fmt.Sprintf("%d goroutine(s) still blocked after the run (drain %v)", rep.total, rep.drain)
}

// leaks returns the report's leaks object of report.json and the results line (API-074, API-087).
func (rep *leakReport) leaks() *artifact.Leaks {
	return &artifact.Leaks{Groups: rep.groups, Unattributed: rep.unattributed}
}

// drainProcesses lets processes exit on their own after the run (GOR-066): it sleeps on bubble
// time in growing chunks until a dump that was not cut counts no bubble goroutine, or Drain has
// passed. It runs on the driver goroutine with the world lock released, and returns its last
// countBubble result. No bubble goroutine runs after that count, so E3 and collectLeaks use it.
func (d *driver) drainProcesses() (blocks []stackBlock, cut bool) {
	ws := d.ga.watch
	ws.phase.Store(watchDrain)
	drain := d.ga.r.plan.opts.Drain
	elapsed, chunk := time.Duration(0), drainFirstChunk
	for {
		ws.progress.Add(1)
		step := min(chunk, drain-elapsed)
		d.waiting.Store(true)
		time.Sleep(step)
		elapsed += step
		synctest.Wait()
		d.waiting.Store(false)
		blocks, cut = d.countBubble()
		if (len(blocks) == 0 && !cut) || elapsed >= drain {
			return blocks, cut
		}
		chunk = min(2*chunk, drainMaxChunk)
	}
}

// stackBlock is one goroutine's block of runtime.Stack output.
type stackBlock struct {
	id   uint64
	text string
}

// lastDump is the length of the last goroutine dump of the process. The next dump starts with a
// buffer that would have held it, so it usually walks the goroutines once (GOR-067).
var lastDump atomic.Int64

// dumpCutWarning is an attempt's warning when a goroutine dump passed stackBufMax (GOR-067).
var dumpCutWarning = fmt.Sprintf("leak detection incomplete: the goroutine dump passed %d MiB, "+
	"so goroutines still blocked after the drain may be missing from the count", stackBufMax>>20)

// stackBufStart is the first buffer size of a dump: the smallest size of the doubling sequence
// from 1 MiB that is larger than last, but at most limit.
func stackBufStart(last int64, limit int) int {
	size := stackBufMin
	for int64(size) <= last && size < limit {
		size *= 2
	}
	return min(size, limit)
}

// dumpStacks returns runtime.Stack(buf, true). The buffer starts at stackBufStart and doubles
// until the dump fits or the buffer holds limit bytes; cut reports a dump that filled the buffer
// at limit, which may miss goroutines (GOR-067).
func dumpStacks(limit int) (dump string, cut bool) {
	size := stackBufStart(lastDump.Load(), limit)
	for {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < size || size >= limit {
			lastDump.Store(int64(n))
			return string(buf[:n]), n == size
		}
		size = min(2*size, limit)
	}
}

// allStacks returns runtime.Stack(buf, true), cut at 64 MiB (GOR-067).
func allStacks() string {
	dump, _ := dumpStacks(stackBufMax)
	return dump
}

// splitStacks returns the goroutine blocks of dump whose header names bubble, in goroutine ID
// order. With exclude it drops the driver, the caller of synctest.Test (status "synctest.Run…")
// and the bubble's main goroutine (GOR-067).
func splitStacks(dump string, bubble, driverID uint64, exclude bool) []stackBlock {
	var out []stackBlock
	var cur []string
	flush := func() {
		text := strings.TrimRight(strings.Join(cur, "\n"), "\n")
		cur = nil
		header, _, _ := strings.Cut(text, "\n")
		m := bubbleHeader.FindStringSubmatch(header)
		if m == nil {
			return
		}
		id, err1 := strconv.ParseUint(m[1], 10, 64)
		b, err2 := strconv.ParseUint(m[3], 10, 64)
		if err1 != nil || err2 != nil || b != bubble {
			return
		}
		if exclude {
			if id == driverID || strings.HasPrefix(m[2], "synctest.Run") {
				return
			}
			for _, l := range strings.Split(text, "\n") {
				if strings.HasPrefix(l, "testing.testingSynctestTest(") || strings.HasPrefix(l, "testing/synctest.testingSynctestTest(") {
					return
				}
			}
		}
		out = append(out, stackBlock{id: id, text: text})
	}
	for _, l := range strings.Split(dump, "\n") {
		if goroutineStart.MatchString(l) && len(cur) > 0 {
			flush()
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		flush()
	}
	slices.SortFunc(out, func(a, b stackBlock) int { return cmp.Compare(a.id, b.id) })
	return out
}

// countBubble returns the bubble goroutines it counts, all but the driver, the caller of
// synctest.Test and the bubble's main goroutine, in goroutine ID order. cut reports a dump cut at
// the cap, from which goroutines may be missing (GOR-067).
func (d *driver) countBubble() (blocks []stackBlock, cut bool) {
	dump, cut := dumpStacks(cmp.Or(d.stackCap, stackBufMax))
	return splitStacks(dump, d.ga.bubble.Load(), d.ga.gid, true), cut
}

// profRecord is one record of a goroutine profile: goroutines with the same stack and labels.
type profRecord struct {
	count  int
	labels map[string]string // lookup only
	frames []string          // "function (file:line)", innermost first
}

// parseGoroutineProfile parses pprof.Lookup("goroutine").WriteTo(w, 1) output (GOR-068 step 2).
func parseGoroutineProfile(text string) []profRecord {
	var out []profRecord
	cur := -1
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "# labels: "):
			if cur >= 0 {
				out[cur].labels = parseLabels(strings.TrimPrefix(line, "# labels: "))
			}
		case strings.HasPrefix(line, "#\t"):
			if cur < 0 {
				continue
			}
			var f []string
			for _, x := range strings.Split(line[2:], "\t") {
				if x != "" {
					f = append(f, x)
				}
			}
			if len(f) >= 3 { // pc, function+offset, file:line
				fn := f[1]
				if i := strings.LastIndex(fn, "+"); i >= 0 {
					fn = fn[:i]
				}
				out[cur].frames = append(out[cur].frames, fn+" ("+f[2]+")")
			}
		default:
			if c, _, ok := strings.Cut(line, " @ "); ok {
				if n, err := strconv.Atoi(c); err == nil {
					out = append(out, profRecord{count: n})
					cur = len(out) - 1
					continue
				}
			}
			if strings.TrimSpace(line) == "" {
				cur = -1
			}
		}
	}
	return out
}

// parseLabels parses the labels of a goroutine profile record, {"key":"value", ...} with each key
// and value quoted as Go's %q quotes them. It returns nil when s is not in that form.
func parseLabels(s string) map[string]string {
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil
	}
	s = s[1 : len(s)-1]
	m := map[string]string{}
	for s != "" {
		k, rest, okKey := cutQuoted(s)
		rest, okColon := strings.CutPrefix(rest, ":")
		v, rest, okValue := cutQuoted(rest)
		if !okKey || !okColon || !okValue {
			return nil
		}
		m[k] = v
		if rest == "" {
			break
		}
		var okSep bool
		if s, okSep = strings.CutPrefix(rest, ", "); !okSep || s == "" {
			return nil
		}
	}
	return m
}

// cutQuoted unquotes the Go string literal at the start of s and returns the rest of s.
func cutQuoted(s string) (value, rest string, ok bool) {
	q, err := strconv.QuotedPrefix(s)
	if err != nil {
		return "", "", false
	}
	value, err = strconv.Unquote(q)
	return value, s[len(q):], err == nil
}

// groupLeaks keeps the records of run, groups them by (faultline_node, faultline_inc) ascending
// with records sorted by frame text, and drops node -1 (GOR-068 steps 2 and 3). describe names a
// group's node and status. sum is the number of goroutines in the groups.
func groupLeaks(recs []profRecord, run string, describe func(node int64, inc uint64) (name, status string)) (groups []artifact.LeakGroup, sum int) {
	type entry struct {
		node int64
		inc  uint64
		key  string
		rec  profRecord
	}
	var es []entry
	for _, r := range recs {
		if r.labels["faultline_run"] != run {
			continue
		}
		node, err1 := strconv.ParseInt(r.labels["faultline_node"], 10, 32) // a kernel.NodeID
		inc, err2 := strconv.ParseUint(r.labels["faultline_inc"], 10, 32)
		if err1 != nil || err2 != nil || node < 0 {
			continue
		}
		es = append(es, entry{node: node, inc: inc, key: strings.Join(r.frames, "\n"), rec: r})
	}
	slices.SortStableFunc(es, func(a, b entry) int {
		if c := cmp.Compare(a.node, b.node); c != 0 {
			return c
		}
		if c := cmp.Compare(a.inc, b.inc); c != 0 {
			return c
		}
		return strings.Compare(a.key, b.key)
	})
	for i := 0; i < len(es); {
		name, status := describe(es[i].node, es[i].inc)
		g := artifact.LeakGroup{Node: name, Inc: uint32(es[i].inc), Status: status} //nolint:gosec // parsed with bitSize 32
		j := i
		for ; j < len(es) && es[j].node == es[i].node && es[j].inc == es[i].inc; j++ {
			g.Count += es[j].rec.count
			g.Records = append(g.Records, artifact.LeakRecord{Count: es[j].rec.count, Frames: es[j].rec.frames})
		}
		sum += g.Count
		groups = append(groups, g)
		i = j
	}
	return groups, sum
}

// leakText is GOR-068's report text, without a trailing newline.
func leakText(total int, drain time.Duration, groups []artifact.LeakGroup, unattributed int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "faultline: %d goroutine(s) still blocked after the run (drain %v)", total, drain)
	for _, g := range groups {
		if g.Status == "world" {
			fmt.Fprintf(&b, "\n  world: %d", g.Count)
		} else {
			fmt.Fprintf(&b, "\n  %s incarnation %d (%s): %d", g.Node, g.Inc, g.Status, g.Count)
		}
		for _, r := range g.Records {
			for i, f := range r.Frames {
				if i == 0 {
					fmt.Fprintf(&b, "\n    %d × %s", r.Count, f)
				} else {
					fmt.Fprintf(&b, "\n          %s", f)
				}
			}
		}
	}
	if unattributed > 0 {
		fmt.Fprintf(&b, "\n  unattributed: %d", unattributed)
	}
	return b.String()
}

// collectLeaks builds the leak report of blocks, the goroutines the drain's last countBubble
// found (GOR-068). It runs on the driver goroutine with the world lock released, while every other
// bubble goroutine is durably blocked, so steps 4 and 5 use that count and those stacks.
func (d *driver) collectLeaks(blocks []stackBlock) *leakReport {
	ga := d.ga
	total := len(blocks)
	pprof.SetGoroutineLabels(pprof.WithLabels(context.Background(), pprof.Labels(
		"faultline_run", ga.run, "faultline_node", "-1", "faultline_inc", "0"))) // step 1
	var b bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&b, 1) // step 2
	describe := func(node int64, inc uint64) (string, string) {
		if node == 0 {
			return "", "world"
		}
		id := kernel.NodeID(node) //nolint:gosec // groupLeaks parsed it with bitSize 32
		n, p := d.w.Sim.Node(id), d.proc(id, uint32(inc))
		switch {
		case n == nil || p == nil:
			return "", "running at the end"
		case p.status == incCrashed:
			return n.Name(), "crashed at t=" + p.endAt.String()
		case p.status == incExited:
			return n.Name(), "exited at t=" + p.endAt.String()
		default:
			return n.Name(), "running at the end"
		}
	}
	groups, sum := groupLeaks(parseGoroutineProfile(b.String()), ga.run, describe) // step 3
	unattributed := max(total-sum, 0)                                              // step 4
	stacks := make([]string, len(blocks))                                          // step 5
	for i, blk := range blocks {
		stacks[i] = blk.text
	}
	drain := ga.r.plan.opts.Drain
	return &leakReport{
		total:        total,
		drain:        drain,
		text:         leakText(total, drain, groups, unattributed),
		groups:       groups,
		unattributed: unattributed,
		stacks:       strings.Join(stacks, "\n\n") + "\n",
	}
}
