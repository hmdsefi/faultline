// Command faultline is the faultline CLI: help, version and render in Phase 1 (spec ART §5.14);
// sweep and minimize (Phase 3) and view (Phase 4) add one file and one table entry each.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// command is one entry of the command table (ART-095).
type command struct {
	name    string // "render"
	summary string // one line for the command list
	usage   string // "faultline render [-slice-cap n] [-max-records n] <dir>"
	help    string // paragraph(s) shown by "faultline help <name>"
	run     func(args []string, stdout, stderr io.Writer) int
	// flags returns the command's FlagSet so "faultline help <name>" can print its defaults;
	// nil when the command has no flags.
	flags func(stderr io.Writer) *flag.FlagSet
}

// commands returns the command table, sorted by name.
func commands() []command {
	return []command{helpCommand(), renderCommand(), versionCommand()}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches args and returns the exit code (ART-095).
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText())
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help":
		fmt.Fprint(stdout, usageText())
		return 0
	}
	c, ok := lookup(args[0])
	if !ok {
		unknownCommand(stderr, args[0])
		return 2
	}
	return c.run(args[1:], stdout, stderr)
}

func lookup(name string) (command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

func unknownCommand(stderr io.Writer, name string) {
	fmt.Fprintf(stderr, "faultline: unknown command %q\nRun 'faultline help' for usage.\n", name)
}

// usageText is the top-level usage (ART-095).
func usageText() string {
	cmds := commands()
	width := 0
	for _, c := range cmds {
		width = max(width, len(c.name))
	}
	var b strings.Builder
	b.WriteString("faultline is a deterministic simulation testing tool for Go.\n\nUsage:\n\n\tfaultline <command> [arguments]\n\nCommands:\n\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "\t%-*s%s\n", width+2, c.name, c.summary)
	}
	b.WriteString("\nUse \"faultline help <command>\" for more information about a command.\n")
	return b.String()
}

// newFlagSet returns a FlagSet with the conventions of ART-096.
func newFlagSet(name, usage string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses args; ok is false when the command must return code (ART-096).
func parseFlags(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0, false
		}
		return 2, false
	}
	return 0, true
}

// usageError prints "faultline <command>: <message>" and the usage, and returns 2 (ART-096).
func usageError(fs *flag.FlagSet, stderr io.Writer, name, message string) int {
	fmt.Fprintf(stderr, "faultline %s: %s\n", name, message)
	fs.Usage()
	return 2
}
