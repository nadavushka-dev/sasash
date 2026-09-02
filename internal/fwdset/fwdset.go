// Package fwdset declares durable forward sets: it appends exactly one
// `# @sasash forwards <name> <spec>...` line to a host's own meta run in
// sasash.conf (CONTRACT-fwdset). It writes DESIRE only — starting the
// tunnel is space / `+` / `sasash up`'s job — and follows the write
// invariants: re-read at call time, atomic write, byte-identity outside
// the inserted line.
package fwdset

import (
	"fmt"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// Add inserts the set's meta line immediately above the `Host <alias>`
// line in the file at confPath. By the attachment rule the unbroken
// comment run above a Host line belongs to that host, so the line lands
// in the host's own meta run whether one exists already or not, and the
// previous block's OwnMeta cannot claim it.
func Add(confPath, alias, name string, specs []string) error {
	if err := validate(name, specs); err != nil {
		return err
	}
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return err
	}
	h, ok := f.Host(alias)
	if !ok {
		return fmt.Errorf("host %q not managed by sasash", alias)
	}
	for _, m := range h.OwnMetaVerb("forwards") {
		if len(m.Args) > 0 && strings.EqualFold(m.Args[0], name) {
			return fmt.Errorf("forward set %q already declared for %q", name, alias)
		}
	}
	line := "# @sasash forwards " + name + " " + strings.Join(specs, " ") + lineTerm(f.Line(h.Start).Raw)
	raw := make([]string, 0, f.Len()+1)
	for i := 0; i < f.Len(); i++ {
		if i == h.Start {
			raw = append(raw, line)
		}
		raw = append(raw, f.Line(i).Raw)
	}
	out := sshconf.Parse(confPath, []byte(strings.Join(raw, "")))
	out.Perm = f.Perm
	return out.WriteAtomic(confPath)
}

// validate rejects what cannot be written as one meta line, or what the
// engine could never verify (CONTRACT-tunnel §5: a spec needs an all-digit
// local port among its first two ':'-fields).
func validate(name string, specs []string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n#") {
		return fmt.Errorf("forward set name %q: want a single word", name)
	}
	if len(specs) == 0 {
		return fmt.Errorf("forward set %q: no specs", name)
	}
	seen := make(map[string]bool)
	for _, s := range specs {
		if strings.ContainsAny(s, " \t\r\n") || !hasLocalPort(s) {
			return fmt.Errorf("spec %q: no local port", s)
		}
		if seen[s] {
			return fmt.Errorf("spec %q repeated", s)
		}
		seen[s] = true
	}
	return nil
}

func hasLocalPort(spec string) bool {
	fields := strings.SplitN(spec, ":", 3)
	for i := 0; i < len(fields) && i < 2; i++ {
		if fields[i] != "" && strings.Trim(fields[i], "0123456789") == "" {
			return true
		}
	}
	return false
}

// lineTerm is the Host line's own terminator, so the inserted line
// matches the file's style; a last line without one gets a newline.
func lineTerm(raw string) string {
	switch {
	case strings.HasSuffix(raw, "\r\n"):
		return "\r\n"
	default:
		return "\n"
	}
}
