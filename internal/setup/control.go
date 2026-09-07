package setup

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// The managed-entry ControlMaster trio (SPEC §2). The whole tunnel engine
// rides on it: `-O check` / `-O forward` reach a master only through a
// ControlPath, and the engine's `ssh -fN <alias>` starts one only under
// ControlMaster auto (CONTRACT-tunnel §2's "no -M" rests on that). Every
// entry sasash saves carries it (SASASH.4.18); an older managed entry
// without it fails `-O` with "No ControlPath specified" and is offered
// the missing lines.

// ControlDirectives are the three, in the template's order.
var ControlDirectives = [3][2]string{
	{"ControlMaster", "auto"},
	{"ControlPath", "~/.ssh/cm/%C"},
	{"ControlPersist", "30m"},
}

// ErrNotManaged says the alias has no Host block in the managed file.
var ErrNotManaged = errors.New("not managed by sasash: no Host block in sasash.conf")

// ManagedBlock renders a new managed entry as sasash writes it: the Host
// line, HostName, User and Port when given, then the trio — the SPEC §2
// template verbatim, two-space indent, one line each.
func ManagedBlock(alias, hostName, user, port string) string {
	var b strings.Builder
	b.WriteString("Host " + alias + "\n")
	b.WriteString("  HostName " + hostName + "\n")
	if user != "" {
		b.WriteString("  User " + user + "\n")
	}
	if port != "" {
		b.WriteString("  Port " + port + "\n")
	}
	for _, d := range ControlDirectives {
		b.WriteString("  " + d[0] + " " + d[1] + "\n")
	}
	return b.String()
}

// Control is what a block has of the trio: the lines it lacks, as they
// would be written ("ControlMaster auto"), and the directives it sets to
// another value, as written ("ControlPath none"). A directive present
// with another value is the user's choice — sasash never overrides a
// hand-written setting — so it is never missing; the caller decides
// what to say about it (a ControlPath of none disables -O as surely as
// no ControlPath at all).
type Control struct {
	Missing []string
	Other   []string
}

// InspectControl reads the alias's block. The alias may be any pattern
// on the Host line, not only the first. ErrNotManaged when no block in
// the file names it — an empty alias included: a bare `Host` line has
// no pattern to match.
func InspectControl(confPath, alias string) (Control, error) {
	_, h, err := controlBlock(confPath, alias)
	if err != nil {
		return Control{}, err
	}
	return inspect(h), nil
}

func controlBlock(confPath, alias string) (*sshconf.File, *sshconf.Host, error) {
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return nil, nil, err
	}
	for _, h := range f.Hosts() {
		for _, p := range h.Patterns {
			if strings.EqualFold(p, alias) {
				h := h
				return f, &h, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("host %q: %w", alias, ErrNotManaged)
}

func inspect(h *sshconf.Host) Control {
	var c Control
	for _, d := range ControlDirectives {
		v, ok := h.Get(d[0])
		switch {
		case !ok:
			c.Missing = append(c.Missing, d[0]+" "+d[1])
		case !strings.EqualFold(v, d[1]):
			c.Other = append(c.Other, strings.TrimSpace(d[0]+" "+v))
		}
	}
	return c
}

// EnsureControl adds the trio's missing directives to the alias's block:
// one read-modify-write through the lossless parser, the block's own
// indent, atomic (temp + fsync + rename), nothing else touched. Present
// directives keep their values. It reports the lines it added — none
// when the block already had all three, in which case the file is not
// written at all. ErrNotManaged when the alias has no block.
func EnsureControl(confPath, alias string) ([]string, error) {
	f, h, err := controlBlock(confPath, alias)
	if err != nil {
		return nil, err
	}
	added := inspect(h).Missing
	if len(added) == 0 {
		return nil, nil
	}
	// Set inserts directly under the Host line, so the last one set
	// comes first: reversed here, they read in the template's order.
	for i := len(ControlDirectives) - 1; i >= 0; i-- {
		d := ControlDirectives[i]
		if _, ok := h.Get(d[0]); !ok {
			h.Set(d[0], d[1])
		}
	}
	if err := f.WriteAtomic(confPath); err != nil {
		return nil, err
	}
	return added, nil
}
