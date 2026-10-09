package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

const versionUsage = "faultline version"

func versionCommand() command {
	return command{
		name:    "version",
		summary: "print the faultline version",
		usage:   versionUsage,
		help:    "Version prints the faultline version, the Go version, and the platform.",
		run:     runVersion,
	}
}

// runVersion implements ART-097.
func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("version", versionUsage, stderr)
	if code, ok := parseFlags(fs, args, stdout); !ok {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(fs, stderr, "version", "unexpected arguments")
	}
	v := "(unknown)"
	if bi, ok := debug.ReadBuildInfo(); ok {
		v = bi.Main.Version
	}
	fmt.Fprintf(stdout, "faultline %s %s %s/%s\n", v, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return 0
}
