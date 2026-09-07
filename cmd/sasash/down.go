package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

func init() {
	register("down", "kill every tunnel process of a host", cmdDown)
}

// cmdDown wires the real world; every decision lives in runDown.
func cmdDown(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash down: %v\n", err)
		return 3
	}
	eng := tunnel.Engine{StateDir: stateDir(home)}
	return runDown(args, stdout, stderr, sshConfigPath(home), home, eng)
}

// runDown kills every tunnel process the host has recorded (Engine.KillAll)
// with the addr/status exit-code contract on top: 0 = every process gone
// (nothing running is success — down is idempotent), 1 = a process could
// not be ended (the reason on stderr), 2 = usage, 3 = config problem. The
// interactive master is not touched: it is the next connect's to reuse.
func runDown(args []string, stdout, stderr io.Writer, root, home string, eng tunneler) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: sasash down <host>")
		return 2
	}
	alias := args[0]

	h, code := lookupHost(stderr, "down", root, home, alias)
	if code != 0 {
		return code
	}

	switch err := eng.KillAll(context.Background(), h.Alias); {
	case err == nil:
	case tunnel.StateWriteOnly(err):
		// The processes really went down; only the cache clear failed
		// (CONTRACT-tunnel: a state write failure must not fail the
		// action that succeeded). Every leaf must say so: a survivor
		// joined with a cache failure is still a survivor.
		fmt.Fprintf(stderr, "sasash down: warning: %v\n", err)
	default:
		fmt.Fprintf(stderr, "sasash down: %v\n", err)
		return 1
	}
	return 0
}
