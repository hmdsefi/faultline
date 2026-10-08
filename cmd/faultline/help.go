package main

import (
	"fmt"
	"io"
)

// helpCommand is the help entry of the command table (ART-095).
func helpCommand() command {
	return command{
		name:    "help",
		summary: "show help for faultline or one of its commands",
		usage:   "faultline help [command]",
		help:    "Help prints the list of commands, or the usage and help text of one command.",
		run:     runHelp,
	}
}

func runHelp(args []string, stdout, stderr io.Writer) int {
	switch len(args) {
	case 0:
		fmt.Fprint(stdout, usageText())
		return 0
	case 1:
		c, ok := lookup(args[0])
		if !ok {
			unknownCommand(stderr, args[0])
			return 2
		}
		fmt.Fprintf(stdout, "usage: %s\n\n%s\n", c.usage, c.help)
		if c.flags != nil {
			fs := c.flags(stderr)
			fs.SetOutput(stdout)
			fs.PrintDefaults()
		}
		return 0
	default:
		fmt.Fprintf(stderr, "faultline help: want at most one command\nusage: faultline help [command]\n")
		return 2
	}
}
