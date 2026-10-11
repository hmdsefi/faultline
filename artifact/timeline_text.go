// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// bareAttr matches attr values written without quotes in timeline.txt (ART-040).
var bareAttr = regexp.MustCompile(`^[A-Za-z0-9._:/@%+,#|-]+$`)

// esc returns s made valid UTF-8 (validUTF8) and then escaped (escape), so that it stays on its
// line and reads the same after a round trip through trace.jsonl or report.json (ART-040).
func esc(s string) string { return escape(validUTF8(s)) }

// escape writes `\` as `\\` and every rune that strconv.IsPrint rejects the way strconv.QuoteRune
// writes it, without the quotes: \n, \r, \t, \x00, \x1b, \x7f, \u009b, \u2028, \u202e (ART-040).
// The space, U+FFFD and every other printable rune stay as they are.
func escape(s string) string {
	i := strings.IndexFunc(s, needsEscape)
	if i < 0 {
		return s
	}
	var b strings.Builder
	b.WriteString(s[:i])
	for _, r := range s[i:] {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case needsEscape(r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsEscape reports whether escape rewrites r.
func needsEscape(r rune) bool { return r == '\\' || !strconv.IsPrint(r) }

// WriteTimelineText writes timeline.txt: a header that summarizes the run, then one line per
// trace record with its Seq, time, node, kind, text, attributes and cause. A "!" marks the root of
// the causal slice s and a "*" its other records.
func WriteTimelineText(w io.Writer, rep *Report, tr *Trace, s Slice) error {
	if rep == nil {
		return errors.New("artifact: report is nil")
	}
	if tr == nil {
		return errors.New("artifact: trace is nil")
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	line := func(text string) { // write errors stick in bw; Flush returns the first
		_, _ = bw.WriteString(strings.TrimRight(text, " "))
		_ = bw.WriteByte('\n')
	}
	failure := "none"
	if rep.Failure != nil {
		failure = esc(rep.Failure.Headline)
	}
	nodes := "none"
	if len(tr.Header.Nodes) > 0 {
		parts := make([]string, len(tr.Header.Nodes))
		for i, n := range tr.Header.Nodes {
			tags := make([]string, len(n.Tags))
			for j, tag := range n.Tags {
				tags[j] = esc(tag)
			}
			parts[i] = fmt.Sprintf("%d=%s [%s]", n.ID, esc(n.Name), strings.Join(tags, ","))
		}
		nodes = strings.Join(parts, ", ")
	}
	slice := "none"
	if s.Root != 0 {
		slice = fmt.Sprintf("%d records, root %d", len(s.Seqs), s.Root)
		if s.Truncated {
			slice += fmt.Sprintf(", truncated at cap %d", s.Cap)
		}
	}
	line(fmt.Sprintf("faultline timeline v%d", TimelineVersion))
	line("test:     " + esc(rep.Subtest))
	line("package:  " + esc(rep.Package))
	line("seed:     " + esc(rep.Seed))
	line("status:   " + esc(rep.Status))
	line("failure:  " + failure)
	line(fmt.Sprintf("records:  %d (dropped %d), trace hash %s", len(tr.Records), tr.Header.Dropped, esc(rep.Run.TraceHash)))
	line("nodes:    " + nodes)
	line("slice:    " + slice)
	line("legend:   ! failure record, * causal slice, <- cause")
	line("")

	names := nodeNames(tr.Header.Nodes)
	member := make(map[uint64]bool, len(s.Seqs)) // lookups only
	for _, q := range s.Seqs {
		member[q] = true
	}
	wSeq, wTime, wNode, wKind := 3, 4, 4, 4
	for _, r := range tr.Records {
		wSeq = max(wSeq, len(strconv.FormatUint(r.Seq, 10)))
		wTime = max(wTime, len(r.At.String()))
		wNode = max(wNode, len(esc(nodeLabel(r, names))))
		wKind = max(wKind, len(esc(r.Kind)))
	}
	row := func(mark, seq, t, node, kind, rest string) string {
		return mark + " " + padLeft(seq, wSeq) + " " + padLeft(t, wTime) + " " + padRight(node, wNode) + " " + padRight(kind, wKind) + " " + rest
	}
	line(row(" ", "SEQ", "TIME", "NODE", "KIND", "TEXT"))
	if tr.Header.Dropped > 0 {
		line(fmt.Sprintf("  ... %d earlier records were not retained", tr.Header.Dropped))
	}
	for _, r := range tr.Records {
		mark := " "
		switch {
		case s.Root != 0 && r.Seq == s.Root:
			mark = "!"
		case member[r.Seq]:
			mark = "*"
		}
		var rest strings.Builder
		rest.WriteString(esc(r.Text))
		for _, a := range r.Attrs {
			v := validUTF8(a.Value)
			if !bareAttr.MatchString(v) {
				v = strconv.Quote(v)
			}
			rest.WriteString(" " + esc(a.Key) + "=" + v)
		}
		if r.Cause != 0 {
			rest.WriteString(" <-" + strconv.FormatUint(r.Cause, 10))
		}
		line(row(mark, strconv.FormatUint(r.Seq, 10), r.At.String(), esc(nodeLabel(r, names)), esc(r.Kind), rest.String()))
	}
	return bw.Flush()
}

func padLeft(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
