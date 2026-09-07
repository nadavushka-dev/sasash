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

// MissingControl lists the trio's lines the alias's block lacks, as they
// would be written ("ControlMaster auto"), in the template's order; none
// when the block has all three. A directive present with another value
// is the user's choice and counts as present — sasash never overrides a
// hand-written setting. ErrNotManaged when the alias has no block.
func MissingControl(confPath, alias string) ([]string, error) {
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return nil, err
	}
	h, ok := f.Host(alias)
	if !ok {
		return nil, fmt.Errorf("host %q: %w", alias, ErrNotManaged)
	}
	return missing(h), nil
}

func missing(h *sshconf.Host) []string {
	var out []string
	for _, d := range ControlDirectives {
		if _, ok := h.Get(d[0]); !ok {
			out = append(out, d[0]+" "+d[1])
		}
	}
	return out
}

// EnsureControl adds the trio's missing directives to the alias's block:
// one read-modify-write through the lossless parser, the block's own
// indent, atomic (temp + fsync + rename), nothing else touched. Present
// directives keep their values. It reports the lines it added — none
// when the block already had all three, in which case the file is not
// written at all. ErrNotManaged when the alias has no block.
func EnsureControl(confPath, alias string) ([]string, error) {
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return nil, err
	}
	h, ok := f.Host(alias)
	if !ok {
		return nil, fmt.Errorf("host %q: %w", alias, ErrNotManaged)
	}
	added := missing(h)
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
