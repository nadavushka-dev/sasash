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
		return 1
	}
	root := filepath.Join(home, ".ssh", "config")
	return runAddr(args, stdout, stderr, root, home, probe.Net{})
}

// runAddr resolves the host's active address: first alive path in config
// order (probe.Select — order is preference, not latency, SPEC §3) and prints
// the bare address, so `$(sasash addr ubuntu)` is data other tools can use.
// Stdout carries the address and nothing else; everything human goes to
// stderr.
func runAddr(args []string, stdout, stderr io.Writer, root, home string, dialer probe.Dialer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: sasash addr <host>")
		return 2
	}
	alias := args[0]

	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil {
		fmt.Fprintf(stderr, "sasash addr: %v\n", err)
		return 1
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "sasash addr: warning: %v\n", w)
	}

	for _, h := range hosts {
		if h.Alias != alias {
			continue
		}
		statuses := probe.Prober{Dialer: dialer}.ProbeAll(context.Background(), h.Paths)
		s, ok := probe.Select(statuses)
		if !ok {
			fmt.Fprintf(stderr, "sasash addr: all %d path(s) to %q are down\n", len(statuses), alias)
			return 1
		}
		fmt.Fprintln(stdout, s.Path.Host)
		return 0
	}

	fmt.Fprintf(stderr, "sasash addr: unknown host %q\n", alias)
	return 1
}
