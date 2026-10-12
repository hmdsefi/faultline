// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hmdsefi/gograph"
	"github.com/hmdsefi/gograph/encoding/mermaid"

	"github.com/hmdsefi/faultline/kernel"
)

// hbKey is the gograph vertex label of a record: zero-padded so that gograph's text sort is
// numeric order (ART-055).
func hbKey(seq uint64) string { return fmt.Sprintf("%020d", seq) }

// nodeLabel returns "<name>#<inc>" for a node record, with the name from the node table or
// "node<id>", and "global" for node 0, in timeline.txt and hb.mmd alike (ART-040, ART-055).
func nodeLabel(r kernel.Record, names map[int32]string) string {
	if r.Node == 0 {
		return "global"
	}
	name, ok := names[int32(r.Node)]
	if !ok {
		name = fmt.Sprintf("node%d", r.Node)
	}
	return fmt.Sprintf("%s#%d", name, r.Inc)
}

// nodeNames maps node IDs to names made valid UTF-8 (lookups only). For an ID the table repeats,
// the first entry wins, as in the run view (ART-055).
func nodeNames(nodes []Node) map[int32]string {
	m := make(map[int32]string, len(nodes))
	for _, n := range nodes {
		if _, ok := m[n.ID]; !ok {
			m[n.ID] = validUTF8(n.Name)
		}
	}
	return m
}

// labelEsc escapes s as timeline.txt's text is escaped (escape), so no control character, line or
// paragraph separator or bidi override reaches the Mermaid text. It then writes a word joiner
// (U+2060) between every backslash and a following n or N (ART-055): Mermaid shows a backslash
// and n in a label as a line break, and a backslash and N too in an SVG label. Mermaid decodes
// entity codes before it looks for the pair, so only a rune between the two keeps them on the
// line, and the joiner has no width.
func labelEsc(s string) string { return joinBreaks.Replace(escape(s)) }

// joinBreaks writes a word joiner between a backslash and a following n or N (labelEsc).
var joinBreaks = strings.NewReplacer(`\n`, "\\\u2060n", `\N`, "\\\u2060N")

// truncateRunes returns s cut to n runes plus "..." when it is longer.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos] + "..."
		}
		i++
	}
	return s
}

// WriteHB writes hb.mmd, a Mermaid flowchart of the causal slice of root (see CausalSlice), with
// one node per record and one edge per happens-before link. It halves cap while the text is
// longer than MermaidMaxBytes bytes or has more than MermaidMaxEdges edges; at cap 1 it writes the
// text whatever its size.
func WriteHB(w io.Writer, tr *Trace, root uint64, cap int) error {
	if tr == nil {
		return errors.New("artifact: trace is nil")
	}
	return writeHB(w, tr, nil, root, cap)
}

// writeHB is WriteHB with x, the index of tr.Records, built once and reused for every halving
// of cap. A nil x is built here when there is a slice to find; Write and Render pass the one
// their causalSlice built.
func writeHB(w io.Writer, tr *Trace, x *hbIndex, root uint64, cap int) error {
	if x == nil && root != 0 && cap >= 1 {
		x = newHBIndex(tr.Records)
	}
	for {
		s := Slice{Cap: cap}
		if x != nil {
			s = x.slice(root, cap)
		}
		if s.Root == 0 {
			_, err := fmt.Fprintf(w, "flowchart TD\n    %%%% faultline causal slice v%d: root=0 records=0 cap=%d truncated=false\n    n0[\"no failure record\"]\n", HBVersion, cap)
			return err
		}
		// A slice of m members has at least m-1 edges: each member but the root was found as a
		// predecessor of another member, and renderHB draws that edge. Such a slice is halved
		// without being rendered, which a cap far above the slice would otherwise repeat.
		if cap > 1 && len(s.Seqs)-1 > MermaidMaxEdges {
			cap /= 2
			continue
		}
		text, edges, err := renderHB(tr, x, s)
		if err != nil {
			return err
		}
		if (len(text) <= MermaidMaxBytes && edges <= MermaidMaxEdges) || cap <= 1 {
			_, err := w.Write(text)
			return err
		}
		cap /= 2
	}
}

// renderHB renders one slice of x's records as Mermaid (ART-055 steps 2 to 4) and returns its edge
// count. The edges come from x.preds, as the slice's members do, so both follow one rule (ART-050).
func renderHB(tr *Trace, x *hbIndex, s Slice) ([]byte, int, error) {
	idx := make([]int, 0, len(s.Seqs)) // the members' positions, ascending, as ART-055 adds them
	for _, q := range s.Seqs {
		if i, ok := x.index(q); ok {
			idx = append(idx, i)
		}
	}
	slices.Sort(idx)
	g := gograph.New[string](gograph.Directed())
	bySeqKey := make(map[string]kernel.Record, len(idx)) // lookups only
	edges := 0
	var preds []int
	for _, i := range idx {
		r := x.records[i]
		v := g.AddVertexByLabel(hbKey(r.Seq))
		bySeqKey[hbKey(r.Seq)] = r
		preds = x.preds(i, preds) // the cause, then the program-order predecessor
		for _, p := range preds {
			if _, member := slices.BinarySearch(idx, p); !member {
				continue
			}
			if _, err := g.AddEdge(g.GetVertexByID(hbKey(x.records[p].Seq)), v); err != nil {
				return nil, 0, fmt.Errorf("artifact: hb.mmd: %w", err)
			}
			edges++
		}
	}
	names := nodeNames(tr.Header.Nodes)
	var buf bytes.Buffer
	err := mermaid.Write(&buf, g,
		mermaid.WithDirection[string]("TD"),
		mermaid.WithVertexLabel(func(v *gograph.Vertex[string]) string {
			r := bySeqKey[v.Label()]
			text := labelEsc(truncateRunes(validUTF8(r.Text), 40)) // cut first, so no escape is split
			return fmt.Sprintf("%d %s %s\n%s: %s", r.Seq, r.At.String(), labelEsc(nodeLabel(r, names)), labelEsc(validUTF8(r.Kind)), text)
		}),
		mermaid.WithVertexClass(func(v *gograph.Vertex[string]) string {
			r := bySeqKey[v.Label()]
			switch {
			case r.Seq == s.Root:
				return "violation"
			case strings.HasPrefix(r.Kind, "fault."):
				return "fault"
			case strings.HasPrefix(r.Kind, "net."):
				return "net"
			}
			return ""
		}),
		mermaid.WithClassDef[string]("violation", "fill:#fdd,stroke:#c00,stroke-width:2px"),
		mermaid.WithClassDef[string]("fault", "fill:#ffe9b3,stroke:#b07800"),
		mermaid.WithClassDef[string]("net", "fill:#e3efff,stroke:#3367d6"),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("artifact: hb.mmd: %w", err)
	}
	out := buf.Bytes()
	nl := bytes.IndexByte(out, '\n')
	comment := fmt.Sprintf("    %%%% faultline causal slice v%d: root=%d records=%d cap=%d truncated=%t\n", HBVersion, s.Root, len(s.Seqs), s.Cap, s.Truncated)
	res := make([]byte, 0, len(out)+len(comment))
	res = append(res, out[:nl+1]...)
	res = append(res, comment...)
	res = append(res, out[nl+1:]...)
	return res, edges, nil
}
