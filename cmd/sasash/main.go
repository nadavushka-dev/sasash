// Command sasash is a TUI-first ssh host manager (SPEC.md).
//
// Each subcommand lives in its own file in this package and registers
// itself via register from an init func; adding a subcommand never
// requires editing this file.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type command struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

var commands = map[string]command{}

// register wires a subcommand into the dispatch table. Call it from an
// init func in the subcommand's own file.
func register(name, summary string, run func(args []string, stdout, stderr io.Writer) int) {
	if _, dup := commands[name]; dup || name == "help" {
		panic("sasash: duplicate or reserved subcommand " + name)
	}
	commands[name] = command{summary: summary, run: run}
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"tui"} // the default entry is the TUI (tui.go)
	} else if args[0] != "-h" && args[0] != "--help" && strings.HasPrefix(args[0], "-") {
		args = append([]string{"tui"}, args...) // leading flags belong to the default entry
	}
	switch name := args[0]; name {
	case "help", "-h", "--help":
		printHelp(stdout)
		return 0
	default:
		cmd, ok := commands[name]
		if !ok {
			fmt.Fprintf(stderr, "sasash: unknown subcommand %q; try `sasash help`\n", name)
			return 1
		}
		return cmd.run(args[1:], stdout, stderr)
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "usage: sasash [subcommand]")
	fmt.Fprintln(w, "\nsubcommands:")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-10s %s\n", name, commands[name].summary)
	}
	fmt.Fprintf(w, "  %-10s %s\n", "help", "list subcommands")
}
