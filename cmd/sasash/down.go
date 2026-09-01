package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

func init() {
	register("down", "tear down the control master (ssh -O exit)", cmdDown)
}

// cmdDown wires the real world; every decision lives in runDown.
func cmdDown(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash down: %v\n", err)
		return 3
	}
	eng := tunnel.Engine{StateDir: filepath.Join(home, ".local", "state", "sasash")}
	return runDown(args, stdout, stderr, filepath.Join(home, ".ssh", "config"), home, eng)
}

// runDown is `ssh -O exit` with the addr/status exit-code contract on top:
// 0 = master torn down, 1 = ssh refused (its message on stderr — a master
// that was never up reports here), 2 = usage, 3 = config problem.
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

	if err := eng.Exit(context.Background(), h.Alias); err != nil {
		fmt.Fprintf(stderr, "sasash down: %v\n", err)
		return 1
	}
	return 0
}
