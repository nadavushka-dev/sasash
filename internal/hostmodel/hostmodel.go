// Package hostmodel turns parsed ssh config blocks into sasash's domain
// model: a host with an ordered list of probeable paths, tags, and named
// forward sets, all read from "# @sasash" metadata (SPEC §2, §3).
//
// The package does no I/O beyond reading config files and never dials;
// Paths feed probe.Prober at call sites. Malformed metadata is skipped and
// reported, never fatal — a typo in one meta line must not hide the host.
package hostmodel

import (
	"fmt"
	"net"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// Host is one ssh host as sasash sees it.
type Host struct {
	// Alias is the name ssh is invoked with: the block's first pattern.
	Alias string
	// Paths are the routes to the host, in config order — order IS
	// preference (SPEC §3). Never empty: a host without @sasash path meta
	// gets one path derived from its HostName.
	Paths []probe.Path
	// Tags come from "# @sasash tags a,b"; nil when none.
	Tags []string
	// Forwards are named forward sets from "# @sasash forwards <name>
	// <spec>..."; nil when none.
	Forwards []ForwardSet
}

// ForwardSet is a named set of port-forward specs. Specs stay opaque strings
// until M3 parses them.
type ForwardSet struct {
	Name  string
	Specs []string
}

// Build converts one parsed block into a Host. Each malformed @sasash meta
// line is skipped and reported as one error; unknown verbs are ignored.
func Build(h *sshconf.Host) (Host, []error) {
	out := Host{Alias: h.Name()}
	var errs []error

	// ssh dials the Port directive whatever address it resolved, so a path
	// without an explicit :port inherits it — probing 22 on a host that
	// listens on 2222 would report a false "down". A non-numeric Port would
	// poison every inherited path, so it is dropped with a warning instead.
	defaultPort, _ := h.Get("Port")
	if defaultPort != "" && !allDigits(defaultPort) {
		errs = append(errs, fmt.Errorf("host %q: Port %q is not a number, ignored", out.Alias, defaultPort))
		defaultPort = ""
	}

	seenPath := make(map[string]bool)
	for _, m := range h.OwnMetaVerb("path") {
		if len(m.Args) < 2 {
			errs = append(errs, fmt.Errorf("host %q: @sasash path: missing addr (line %d)", out.Alias, m.Index+1))
			continue
		}
		name := m.Args[0]
		// First VALID wins: a name is only claimed by a path that parsed,
		// so a later well-formed line can stand in for an earlier typo.
		if seenPath[name] {
			errs = append(errs, fmt.Errorf("host %q: @sasash path %q: duplicate name, first wins (line %d)", out.Alias, name, m.Index+1))
			continue
		}
		addr, port, err := splitAddr(m.Args[1])
		if err != nil {
			errs = append(errs, fmt.Errorf("host %q: @sasash path %q: %v (line %d)", out.Alias, name, err, m.Index+1))
			continue
		}
		if port == "" {
			port = defaultPort
		}
		seenPath[name] = true
		out.Paths = append(out.Paths, probe.Path{Name: name, Host: addr, Port: port})
	}

	if len(out.Paths) == 0 {
		addr := out.Alias
		if v, ok := h.Get("HostName"); ok {
			addr = v
		}
		out.Paths = []probe.Path{{Host: addr, Port: defaultPort}}
	}

	for _, m := range h.OwnMetaVerb("tags") {
		for _, arg := range m.Args {
			for _, tag := range strings.Split(arg, ",") {
				if tag = strings.TrimSpace(tag); tag != "" {
					out.Tags = append(out.Tags, tag)
				}
			}
		}
	}

	seenSet := make(map[string]bool)
	for _, m := range h.OwnMetaVerb("forwards") {
		if len(m.Args) < 2 {
			errs = append(errs, fmt.Errorf("host %q: @sasash forwards: missing specs (line %d)", out.Alias, m.Index+1))
			continue
		}
		name := m.Args[0]
		if seenSet[name] {
			errs = append(errs, fmt.Errorf("host %q: @sasash forwards %q: duplicate name, first wins (line %d)", out.Alias, name, m.Index+1))
			continue
		}
		seenSet[name] = true
		out.Forwards = append(out.Forwards, ForwardSet{Name: name, Specs: append([]string(nil), m.Args[1:]...)})
	}

	return out, errs
}

// Load enumerates every host reachable from the config at rootPath (following
// Includes, see sshconf.IncludeHosts) and builds each. Pattern blocks such as
// "Host *" are not probeable hosts and are skipped; a duplicate alias keeps
// its first block, matching ssh's first-value-wins resolution.
//
// warnings aggregates Build errors across hosts. err reports real failure
// (unreadable root or include); when err != nil, hosts is nil.
func Load(rootPath, home string) (hosts []Host, warnings []error, err error) {
	blocks, err := sshconf.IncludeHosts(rootPath, home)
	if err != nil {
		return nil, nil, err
	}
	seen := make(map[string]bool)
	for i := range blocks {
		name := blocks[i].Name()
		if name == "" || strings.ContainsAny(name, "*?") || strings.HasPrefix(name, "!") || seen[name] {
			continue
		}
		seen[name] = true
		host, errs := Build(&blocks[i])
		hosts = append(hosts, host)
		warnings = append(warnings, errs...)
	}
	return hosts, warnings, nil
}

// splitAddr splits "addr[:port]". A bare address — including a bare IPv6
// literal like ::1 or [::1] — returns port "". An empty host (":2222") or an
// explicit colon with no port ("192.0.2.1:") is malformed, not defaulted.
func splitAddr(s string) (host, port string, err error) {
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1], "", nil
	}
	h, p, splitErr := net.SplitHostPort(s)
	if splitErr != nil {
		return s, "", nil
	}
	if h == "" || p == "" || !allDigits(p) {
		return "", "", fmt.Errorf("bad addr %q", s)
	}
	return h, p, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
