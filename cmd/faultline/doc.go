// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// The faultline command works on the artifact directory that a failing faultline test writes for
// one seed.
//
// Install it with:
//
//	go install github.com/hmdsefi/faultline/cmd/faultline@latest
//
// Usage:
//
//	faultline <command> [arguments]
//
// The commands are:
//
//	help     show help for faultline or one of its commands
//	render   regenerate timeline.txt, hb.mmd and timeline.html in an artifact directory
//	version  print the faultline version
//
// "faultline help <command>" prints the usage, help text and flags of one command.
//
// # Render
//
// Usage:
//
//	faultline render [-slice-cap n] [-max-records n] <dir>
//
// Render regenerates timeline.txt, hb.mmd and timeline.html in an artifact directory from its
// report.json and trace.jsonl, and prints the absolute paths it wrote, one per line. It changes
// no other file, except that it removes the temporary files of an interrupted render. The flags
// come before the directory:
//
//	-max-records n
//		maximum number of records embedded in timeline.html, at least 1000 (default 50000)
//	-slice-cap n
//		maximum number of records in the causal slice that hb.mmd draws and timeline.txt and
//		timeline.html mark, at least 1 (default 200)
//
// # Version
//
// Version prints one line: "faultline", the faultline version, the Go version and the platform,
// separated by spaces.
//
// # Exit codes
//
// The command exits with 0 on success, 1 when render fails, and 2 on a usage error, such as an
// unknown command or flag.
package main
