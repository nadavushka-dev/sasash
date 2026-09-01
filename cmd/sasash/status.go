package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

func init() {
	register("status", "one line per host: alias, active path, dead paths", cmdStatus)
}

// cmdStatus wires the real world; every decision lives in runStatus.
func cmdStatus(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash status: %v\n", err)
		return 3
	}
	root := filepath.Join(home, ".ssh", "config")
	return runStatus(args, stdout, stderr, root, home, probe.Net{})
}

// runStatus prints one line per host in config order, with exactly five
// fields per line so awk and grep can parse it:
//
//	<alias> <active-name|-> <active-addr|-> <latency|down> down:<names|->
//
// The active path is the first alive one in config order (probe.Select); a
// derived, unnamed fallback path shows "-" for its name. The address is the
// full dial target (host:port, IPv6 bracketed) because status reports what
// was actually probed — deliberately unlike `sasash addr`, which prints the
// bare host as data for {addr}-style substitution. Latency is integer
// milliseconds. Dead paths are listed by name (dial address, when unnamed)
// in the down: field, "-" when every path answered, so NF is always 5.
//
// status reports, it does not judge: hosts being down is a normal answer, so
// the exit code is 0. Non-zero only when status cannot report at all —
// 2 = usage, 3 = config problem — matching `sasash addr`'s contract.
func runStatus(args []string, stdout, stderr io.Writer, root, home string, dialer probe.Dialer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: sasash status")
		return 2
	}

	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil {
		fmt.Fprintf(stderr, "sasash status: %v\n", err)
		return 3
	}
	// Unlike addr, warnings print on every run: stdout is the data channel
	// here, so stderr is free, and status IS the diagnostic surface.
	for _, w := range warnings {
		fmt.Fprintf(stderr, "sasash status: warning: %v\n", w)
	}

	// One flat ProbeAll across every host: the paths probe concurrently, so
	// a blackholed path costs one timeout for the whole listing, not one per
	// host. ProbeAll returns input order, so slicing recovers each host.
	var paths []probe.Path
	for _, h := range hosts {
		paths = append(paths, h.Paths...)
	}
	statuses := probe.Prober{Dialer: dialer}.ProbeAll(context.Background(), paths)

	offset := 0
	for _, h := range hosts {
		own := statuses[offset : offset+len(h.Paths)]
		offset += len(h.Paths)
		fmt.Fprintln(stdout, statusLine(h.Alias, own))
	}
	return 0
}

// statusLine renders one host. See runStatus for the field contract.
func statusLine(alias string, statuses []probe.Status) string {
	name, addr, latency := "-", "-", "down"
	if s, ok := probe.Select(statuses); ok {
		if s.Path.Name != "" {
			name = s.Path.Name
		}
		addr = s.Path.Addr()
		latency = fmt.Sprintf("%dms", s.Latency.Round(time.Millisecond)/time.Millisecond)
	}

	var dead []string
	for _, s := range statuses {
		if s.Alive() {
			continue
		}
		if s.Path.Name != "" {
			dead = append(dead, s.Path.Name)
		} else {
			dead = append(dead, s.Path.Addr())
		}
	}

	down := "-"
	if len(dead) > 0 {
		down = strings.Join(dead, ",")
	}
	return alias + " " + name + " " + addr + " " + latency + " down:" + down
}
