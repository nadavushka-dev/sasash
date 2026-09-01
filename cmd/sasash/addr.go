package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

func init() {
	register("addr", "print the first alive address for a host", cmdAddr)
}

// cmdAddr wires the real world; every decision lives in runAddr.
func cmdAddr(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash addr: %v\n", err)
		return 3 // config problem, not retryable — matches runAddr's contract
	}
	root := filepath.Join(home, ".ssh", "config")
	return runAddr(args, stdout, stderr, root, home, probe.Net{})
}

// runAddr resolves the host's active address: first alive path in config
// order (probe.Select — order is preference, not latency, SPEC §3) and prints
// the bare address, so `$(sasash addr ubuntu)` is data other tools can use.
// Stdout carries the address and nothing else; everything human goes to
// stderr, and only when resolution fails — this command lives inside `$( )`
// in prompts and loops, so the happy path is silent.
//
// Exit codes are a contract for scripts: 0 = alive, 1 = every path is down
// (retryable), 2 = usage, 3 = the config is the problem (unknown host, or
// unreadable config).
func runAddr(args []string, stdout, stderr io.Writer, root, home string, dialer probe.Dialer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: sasash addr <host>")
		return 2
	}
	alias := args[0]

	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil {
		fmt.Fprintf(stderr, "sasash addr: %v\n", err)
		return 3
	}

	for _, h := range hosts {
		if h.Alias != alias {
			continue
		}
		statuses := probe.Prober{Dialer: dialer}.ProbeAll(context.Background(), h.Paths)
		s, ok := probe.Select(statuses)
		if !ok {
			emitWarnings(stderr, warnings)
			fmt.Fprintf(stderr, "sasash addr: all %d path(s) to %q are down:\n", len(statuses), alias)
			for _, st := range statuses {
				label := st.Path.Addr()
				if st.Path.Name != "" {
					label = st.Path.Name + " " + label
				}
				fmt.Fprintf(stderr, "  %s: %v\n", label, st.Err)
			}
			return 1
		}
		fmt.Fprintln(stdout, s.Path.Host)
		return 0
	}

	emitWarnings(stderr, warnings)
	fmt.Fprintf(stderr, "sasash addr: unknown host %q\n", alias)
	return 3
}

// emitWarnings surfaces malformed-meta diagnostics, but only on failure
// paths: a warning about some unrelated host must not turn every successful
// `$(sasash addr h)` noisy.
func emitWarnings(stderr io.Writer, warnings []error) {
	for _, w := range warnings {
		fmt.Fprintf(stderr, "sasash addr: warning: %v\n", w)
	}
}
