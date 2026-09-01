// The setup subcommand: one-time bootstrap per SPEC.md §2. The work lives in
// internal/setup; this file owns the confirmation prompt.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/setup"
)

func init() {
	register("setup", "one-time bootstrap: Include line, sasash.conf, ~/.ssh/cm", func(args []string) int {
		return runSetup(args, os.Stdin, os.Stdout, os.Stderr)
	})
}

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	yes := flags.Bool("y", false, "answer yes to the confirmation prompt")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash setup: %v\n", err)
		return 1
	}
	return setupHome(home, *yes, stdin, stdout, stderr)
}

// setupHome takes the home directory explicitly so tests run against a
// t.TempDir instead of the real ~/.ssh.
func setupHome(home string, yes bool, stdin io.Reader, stdout, stderr io.Writer) int {
	st, err := setup.Check(home)
	if err != nil {
		fmt.Fprintf(stderr, "sasash setup: %v\n", err)
		return 1
	}
	if st.Done() {
		fmt.Fprintln(stdout, "already set up")
		return 0
	}

	fmt.Fprintln(stdout, "sasash setup will:")
	if !st.IncludePresent {
		fmt.Fprintf(stdout, "  add %q at the top of ~/.ssh/config\n", setup.IncludeLine)
	}
	if !st.ConfPresent {
		fmt.Fprintln(stdout, "  create ~/.ssh/config.d/sasash.conf")
	}
	if !st.CMDirPresent {
		fmt.Fprintln(stdout, "  create ~/.ssh/cm/ for control sockets")
	}

	if !yes && !confirm(stdin, stdout) {
		fmt.Fprintln(stdout, "aborted; nothing changed")
		return 1
	}

	if err := setup.Ensure(home); err != nil {
		fmt.Fprintf(stderr, "sasash setup: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "done")
	return 0
}

// confirm asks y/N on stdin. Anything but an explicit yes — including EOF
// from a non-interactive stdin — declines.
func confirm(stdin io.Reader, stdout io.Writer) bool {
	fmt.Fprint(stdout, "proceed? [y/N] ")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
