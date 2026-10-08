package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hmdsefi/gograph"
	"github.com/hmdsefi/gograph/encoding/mermaid"

	"github.com/hmdsefi/faultline/kernel"
)

// hbKey is the gograph vertex label of a record: zero-padded so that gograph's text sort is
// numeric order (ART-055).
func hbKey(seq uint64) string { return fmt.Sprintf("%020d", seq) }

// nodeLabel returns "<name>#<inc>" for a node record; names come from the node table and fall
// back to "node<id>" (ART-040). Global records use global.
func nodeLabel(r kernel.Record, names map[int32]string, global string) string {
	if r.Node == 0 {
		return global
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

// labelEsc writes the backslash as two and every rune that strconv.IsPrint rejects the way
// strconv.QuoteRune writes it, without the quotes, so no control character, line or paragraph
// separator or bidi override reaches the Mermaid text (ART-055, as ART-040 does for timeline.txt).
func labelEsc(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r == '\\' || !strconv.IsPrint(r) }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case !strconv.IsPrint(r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

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

// MermaidMaxEdges is mermaid.js's default maxEdges: like MermaidMaxBytes, hb.mmd stays within it
// (ART-055).
const MermaidMaxEdges = 500

// WriteHB writes hb.mmd (ART-055) for the causal slice of root. It halves cap while the text is
// longer than MermaidMaxBytes bytes or has more than MermaidMaxEdges edges; at cap 1 it writes the
// text whatever its size.
func WriteHB(w io.Writer, tr *Trace, root uint64, cap int) error {
	if tr == nil {
		return errors.New("artifact: trace is nil")
	}
	for {
		s := CausalSlice(tr.Records, root, cap)
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
		text, edges, err := renderHB(tr, s)
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

// renderHB renders one slice as Mermaid (ART-055 steps 2 to 4) and returns its edge count.
func renderHB(tr *Trace, s Slice) ([]byte, int, error) {
	member := make(map[uint64]bool, len(s.Seqs)) // lookups only
	for _, q := range s.Seqs {
		member[q] = true
	}
	g := gograph.New[string](gograph.Directed())
	bySeqKey := make(map[string]kernel.Record, len(s.Seqs)) // lookups only
	last := make(map[poKey]uint64)                          // lookups only
	edges := 0
	for _, r := range tr.Records {
		var po uint64
		if r.Node != 0 {
			k := poKey{r.Node, r.Inc}
			po = last[k]
			last[k] = r.Seq
		}
		if !member[r.Seq] {
			continue
		}
		v := g.AddVertexByLabel(hbKey(r.Seq))
		bySeqKey[hbKey(r.Seq)] = r
		if r.Cause != 0 && r.Cause < r.Seq && member[r.Cause] {
			if _, err := g.AddEdge(g.GetVertexByID(hbKey(r.Cause)), v); err != nil {
				return nil, 0, fmt.Errorf("artifact: hb.mmd: %w", err)
			}
			edges++
		}
		if po != 0 && member[po] && po != r.Cause {
			if _, err := g.AddEdge(g.GetVertexByID(hbKey(po)), v); err != nil {
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
			return fmt.Sprintf("%d %s %s\n%s: %s", r.Seq, r.At.String(), labelEsc(nodeLabel(r, names, "global")), labelEsc(validUTF8(r.Kind)), text)
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
