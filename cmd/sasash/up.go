package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

func init() {
	register("up", "bring forward sets up: one ssh -N process per port", cmdUp)
}

// tunneler is the slice of tunnel.Engine the CLI consumes (CONTRACT-
// tunnel, Model B): the set composition and the host-wide kill.
type tunneler interface {
	SpawnSet(ctx context.Context, host, set string, specs []string) error
	KillAll(ctx context.Context, host string) error
}

var _ tunneler = tunnel.Engine{} // the wiring funcs are uncovered; pin this at compile time

// stateDir is the tunnel state cache location; up and down (and later the
// TUI) must agree on it or the cache silently splits.
func stateDir(home string) string { return filepath.Join(home, ".local", "state", "sasash") }

func sshConfigPath(home string) string { return filepath.Join(home, ".ssh", "config") }

// cmdUp wires the real world; every decision lives in runUp.
func cmdUp(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash up: %v\n", err)
		return 3
	}
	eng := tunnel.Engine{StateDir: stateDir(home)}
	return runUp(args, stdout, stderr, sshConfigPath(home), home, eng)
}

// runUp brings forward sets up, one process per port: the named set, or
// every declared set (meta order) when none is named. A host with no
// declared sets has nothing to bring up and says so. Exit codes follow the
// addr/status contract: 0 = success (a forward already up is success,
// SpawnSet only starts what is missing), 1 = the action failed (ssh's own
// message on stderr), 2 = usage, 3 = config problem (unknown host, unknown
// set, no sets, unreadable config).
//
// A mid-sequence failure aborts the remaining sets but leaves the earlier
// ones up — the cache is never truth, and the next status re-derives what is
// actually running.
func runUp(args []string, stdout, stderr io.Writer, root, home string, eng tunneler) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(stderr, "usage: sasash up <host> [set]")
		return 2
	}
	alias := args[0]

	h, code := lookupHost(stderr, "up", root, home, alias)
	if code != 0 {
		return code
	}

	sets := h.Forwards
	if len(args) == 2 {
		name := args[1]
		found := false
		for _, s := range h.Forwards {
			if s.Name == name {
				sets, found = []hostmodel.ForwardSet{s}, true
				break
			}
		}
		if !found {
			fmt.Fprintf(stderr, "sasash up: unknown forward set %q for %q; declared: %s\n",
				name, alias, declaredSets(h.Forwards))
			return 3
		}
	}

	ctx := context.Background()
	if len(sets) == 0 {
		fmt.Fprintf(stderr, "sasash up: no forward sets declared for %q — nothing to start\n", alias)
		return 3
	}
	for _, s := range sets {
		err := eng.SpawnSet(ctx, alias, s.Name, s.Specs)
		switch {
		case err == nil:
		case errors.Is(err, tunnel.ErrStateWrite):
			// The ssh side succeeded; only the cache write failed. Warn and
			// keep going — the next Status re-derives reality anyway.
			fmt.Fprintf(stderr, "sasash up: warning: %v\n", err)
		default:
			if s.Name != "" {
				fmt.Fprintf(stderr, "sasash up: set %q: %v\n", s.Name, err)
			} else {
				fmt.Fprintf(stderr, "sasash up: %v\n", err)
			}
			return 1
		}
	}
	return 0
}

// lookupHost resolves alias through the config, sharing the warning and
// exit-code behavior between up and down: warnings always surface on stderr
// (these are interactive actions, stderr is free), unknown host and an
// unreadable config are both exit 3.
func lookupHost(stderr io.Writer, cmd, root, home, alias string) (hostmodel.Host, int) {
	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil {
		fmt.Fprintf(stderr, "sasash %s: %v\n", cmd, err)
		return hostmodel.Host{}, 3
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "sasash %s: warning: %v\n", cmd, w)
	}
	for _, h := range hosts {
		if h.Alias == alias {
			return h, 0
		}
	}
	fmt.Fprintf(stderr, "sasash %s: unknown host %q\n", cmd, alias)
	return hostmodel.Host{}, 3
}

func declaredSets(sets []hostmodel.ForwardSet) string {
	if len(sets) == 0 {
		return "none"
	}
	out := ""
	for i, s := range sets {
		if i > 0 {
			out += ", "
		}
		out += s.Name
	}
	return out
}
