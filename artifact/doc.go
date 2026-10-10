// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package artifact reads and writes faultline artifact directories: report.json, report.txt,
// trace.jsonl, schedule.json, history.jsonl, extra files, and the render files timeline.txt,
// hb.mmd and timeline.html (spec ART).
//
// Every function is a pure function of its inputs: no timestamps, no randomness, no environment.
package artifact

// Format versions written by this package.
const (
	ReportVersion   = 1 // report.json "faultline_report"
	TraceVersion    = 1 // trace.jsonl header "faultline_trace"
	TimelineVersion = 1 // timeline data "faultline_timeline"; timeline.txt "faultline timeline v1"
	HBVersion       = 1 // hb.mmd comment "faultline causal slice v1"
)

// Defaults.
const (
	DefaultSliceCap        = 200    // records in the causal slice
	DefaultTimelineRecords = 50_000 // records embedded in timeline.html
	MinTimelineRecords     = 1_000  // smallest accepted TimelineRecords
	MermaidMaxBytes        = 50_000 // mermaid.js default maxTextSize; hb.mmd stays below it
)

// File names inside an artifact directory.
const (
	FileReport       = "report.json"
	FileReportText   = "report.txt"
	FileTrace        = "trace.jsonl"
	FileSchedule     = "schedule.json"
	FileHistory      = "history.jsonl"
	FileTimelineText = "timeline.txt"
	FileHB           = "hb.mmd"
	FileTimelineHTML = "timeline.html"
	FileMinimized    = "minimized" // directory written by MIN (Phase 3)
)
