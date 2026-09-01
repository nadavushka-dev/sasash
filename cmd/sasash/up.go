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
	register("up", "start the control master and apply forward sets", cmdUp)
}

// tunneler is the slice of tunnel.Engine the CLI consumes
// (CONTRACT-tunnel §3): the compositions, never the raw command methods.
type tunneler interface {
	Up(ctx context.Context, host, set string, specs []string) error
	Exit(ctx context.Context, host string) error
}

// cmdUp wires the real world; every decision lives in runUp.
func cmdUp(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash up: %v\n", err)
		return 3
	}
	eng := tunnel.Engine{StateDir: filepath.Join(home, ".local", "state", "sasash")}
	return runUp(args, stdout, stderr, filepath.Join(home, ".ssh", "config"), home, eng)
}

// runUp starts the host's control master and applies forward sets: the named
// one, or every declared set (meta order) when none is named. A host with no
// declared sets still gets its master ensured. Exit codes follow the
// addr/status contract: 0 = success (an already-running master is success,
// Engine.Up only starts what is missing), 1 = the action failed (ssh's own
// message on stderr), 2 = usage, 3 = config problem (unknown host, unknown
// set, unreadable config).
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
		sets = []hostmodel.ForwardSet{{}} // no declared sets: master only
	}
	for _, s := range sets {
		err := eng.Up(ctx, alias, s.Name, s.Specs)
		switch {
		case err == nil:
		case errors.Is(err, tunnel.ErrStateWrite):
			// The ssh side succeeded; only the cache write failed. Warn and
			// keep going — the next Status re-derives reality anyway.
			fmt.Fprintf(stderr, "sasash up: warning: %v\n", err)
		default:
			fmt.Fprintf(stderr, "sasash up: %v\n", err)
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
