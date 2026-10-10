// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/hmdsefi/faultline/artifact"
)

const renderUsage = "faultline render [-slice-cap n] [-max-records n] <dir>"

func renderCommand() command {
	return command{
		name:    "render",
		summary: "regenerate timeline.txt, hb.mmd and timeline.html in an artifact directory",
		usage:   renderUsage,
		help:    "Render regenerates timeline.txt, hb.mmd and timeline.html in an artifact directory from its\nreport.json and trace.jsonl. It changes no other file, except that it removes the temporary\nfiles of an interrupted render.",
		run:     runRender,
		flags: func(stderr io.Writer) *flag.FlagSet {
			fs, _, _ := renderFlags(stderr)
			return fs
		},
	}
}

// renderFlags defines the flags of render (ART-098). Every flag takes a number: misplacedFlag
// relies on no render flag taking a string value, which could be "--".
func renderFlags(stderr io.Writer) (fs *flag.FlagSet, sliceCap, maxRecords *int) {
	fs = newFlagSet("render", renderUsage, stderr)
	sliceCap = fs.Int("slice-cap", artifact.DefaultSliceCap, "maximum number of records in the causal slice (hb.mmd, timeline marks)")
	maxRecords = fs.Int("max-records", artifact.DefaultTimelineRecords, "maximum number of records embedded in timeline.html (at least 1000)")
	return fs, sliceCap, maxRecords
}

// runRender implements ART-098.
func runRender(args []string, stdout, stderr io.Writer) int {
	fs, sliceCap, maxRecords := renderFlags(stderr)
	if code, ok := parseFlags(fs, args, stdout); !ok {
		return code
	}
	if fs.NArg() != 1 {
		if fs.NArg() > 1 && misplacedFlag(args, fs.NArg()) {
			return usageError(fs, stderr, "render", "flags must come before the artifact directory")
		}
		return usageError(fs, stderr, "render", "want exactly one artifact directory")
	}
	if *sliceCap < 1 {
		return usageError(fs, stderr, "render", "-slice-cap must be at least 1")
	}
	if *maxRecords < artifact.MinTimelineRecords {
		return usageError(fs, stderr, "render", fmt.Sprintf("-max-records must be at least %d", artifact.MinTimelineRecords))
	}
	paths, err := artifact.Render(fs.Arg(0), artifact.RenderOptions{SliceCap: *sliceCap, TimelineRecords: *maxRecords})
	if err != nil {
		fmt.Fprintf(stderr, "faultline render: %v\n", err)
		return 1
	}
	for _, p := range paths {
		fmt.Fprintln(stdout, p)
	}
	return 0
}

// misplacedFlag reports whether an argument after the first positional one is a flag (ART-098):
// the flag package stops at the first argument that is not a flag, as go does, so such a flag
// was not parsed. An argument is a flag when it starts with "-", is not "-" alone, and no "--"
// comes before it. args are the arguments Parse got, and the last nArg of them are the positional
// ones. A "--" that Parse consumed is the argument right before those: no render flag takes a
// string value, which could be "--".
func misplacedFlag(args []string, nArg int) bool {
	first := len(args) - nArg
	if first > 0 && args[first-1] == "--" {
		return false
	}
	for _, a := range args[first+1:] {
		if a == "--" {
			return false
		}
		if len(a) > 1 && a[0] == '-' {
			return true
		}
	}
	return false
}
