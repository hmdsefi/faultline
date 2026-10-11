// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/hmdsefi/faultline/ui"
)

// Timeline is the timeline data object, embedded in timeline.html as JSON.
type Timeline struct {
	Version  int             `json:"faultline_timeline"` // TimelineVersion
	Title    string          `json:"title"`
	Report   *Report         `json:"report"`
	Trace    string          `json:"trace"`    // trace.jsonl text of the included records
	Schedule string          `json:"schedule"` // schedule.json text; "" when absent
	Total    uint64          `json:"total"`    // record lines in trace.jsonl
	Dropped  uint64          `json:"dropped"`  // from the trace header
	Window   *TimelineWindow `json:"window"`   // null when every record is included
	Slice    *TimelineSlice  `json:"slice"`    // null when there is no root
}

// TimelineWindow describes which records a truncated timeline includes.
type TimelineWindow struct {
	FromSeq uint64 `json:"from_seq"` // every record with Seq >= FromSeq is included
	FromNS  int64  `json:"from_ns"`  // At of that record
	Extra   int    `json:"extra"`    // included records with Seq < FromSeq
}

// TimelineSlice is the causal slice in the timeline data.
type TimelineSlice struct {
	Root      uint64   `json:"root"`
	Seqs      []uint64 `json:"seqs"` // ascending
	Cap       int      `json:"cap"`
	Truncated bool     `json:"truncated"`
}

// specialKinds are the node-lifecycle and failure kinds kept in a truncated timeline (ART-060).
var specialKinds = []string{"kernel.add_node", "kernel.boot", "kernel.crash", "kernel.pause", "kernel.resume", "kernel.fail", "kernel.panic"}

// special reports whether a truncated timeline keeps records of kind before the other records
// (ART-060).
func special(kind string) bool {
	return strings.HasPrefix(kind, "fault.") || strings.HasPrefix(kind, "check.") || strings.HasPrefix(kind, "run.") || slices.Contains(specialKinds, kind)
}

// TimelineData builds the timeline data with at most maxRecords records. schedule is the
// content of schedule.json, or nil when there is none. When the trace has more records than
// maxRecords, it keeps the members of s found in the trace, then the newest special records
// (fault.*, check.* and run.* kinds, the node lifecycle kinds, kernel.fail and kernel.panic) while
// fewer than maxRecords/4 are kept, then the newest other records. When it truncates, it returns
// an error if the members of s found in the trace number maxRecords or more.
func TimelineData(rep *Report, tr *Trace, schedule []byte, s Slice, maxRecords int) (*Timeline, error) {
	if rep == nil {
		return nil, errors.New("artifact: report is nil")
	}
	if tr == nil {
		return nil, errors.New("artifact: trace is nil")
	}
	if maxRecords < MinTimelineRecords {
		return nil, fmt.Errorf("artifact: maxRecords %d is below %d", maxRecords, MinTimelineRecords)
	}
	recs := tr.Records
	n := len(recs)
	include := make([]bool, n)
	var window *TimelineWindow
	count := n
	if n > maxRecords {
		x := newSeqIndex(recs)
		count = 0
		for _, q := range s.Seqs {
			if i, ok := x.index(q); ok && !include[i] {
				include[i] = true
				count++
			}
		}
		if count >= maxRecords {
			return nil, fmt.Errorf("artifact: causal slice has %d records; it must be smaller than maxRecords %d (with faultline render: lower -slice-cap or raise -max-records)", count, maxRecords)
		}
		for i := n - 1; i >= 0; i-- {
			if include[i] || !special(recs[i].Kind) {
				continue
			}
			if count >= maxRecords/4 {
				break
			}
			include[i] = true
			count++
		}
		for i := n - 1; i >= 0; i-- {
			if include[i] {
				continue
			}
			if count >= maxRecords {
				break
			}
			include[i] = true
			count++
		}
		k := n
		for k > 0 && include[k-1] {
			k--
		}
		extra := 0
		for i := 0; i < k; i++ {
			if include[i] {
				extra++
			}
		}
		window = &TimelineWindow{FromSeq: recs[k].Seq, FromNS: int64(recs[k].At), Extra: extra}
	} else {
		for i := range include {
			include[i] = true
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	h := headerFor(tr)
	h.Records = uint64(count)
	if err := enc.Encode(h); err != nil {
		return nil, err
	}
	for i, r := range recs {
		if include[i] {
			if err := enc.Encode(FromRecord(r)); err != nil {
				return nil, err
			}
		}
	}

	t := &Timeline{
		Version:  TimelineVersion,
		Title:    "faultline " + rep.Subtest,
		Report:   rep,
		Trace:    buf.String(),
		Schedule: validUTF8(string(schedule)),
		Total:    uint64(n),
		Dropped:  tr.Header.Dropped,
		Window:   window,
	}
	if s.Root != 0 {
		seqs := slices.Clone(s.Seqs)
		slices.Sort(seqs)
		t.Slice = &TimelineSlice{Root: s.Root, Seqs: seqs, Cap: s.Cap, Truncated: s.Truncated}
	}
	return t, nil
}

// WriteTimelineHTML writes timeline.html: ui.TimelineHTML with the JSON of TimelineData.
func WriteTimelineHTML(w io.Writer, rep *Report, tr *Trace, schedule []byte, s Slice, maxRecords int) error {
	t, err := TimelineData(rep, tr, schedule, s, maxRecords)
	if err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return ui.TimelineHTML(w, t.Title, data)
}
