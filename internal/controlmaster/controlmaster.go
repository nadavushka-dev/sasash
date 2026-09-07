// Package controlmaster gives a managed host the SPEC §2 ControlMaster
// trio (ControlMaster auto / ControlPath ~/.ssh/cm/%C / ControlPersist
// 30m) the tunnel engine rides on: without a ControlPath, every `-O`
// command ssh runs for sasash fails with "No ControlPath specified", so
// a saved host shows its master down and its tunnels dead (SASASH.4.18).
// Enable inserts exactly the directives a block lacks — a hand-chosen
// value for any of the three is kept, never fought — in one atomic write
// with byte-identity outside the inserted lines. Trio is the single
// owner of the literal: the save path renders it into new entries.
// Contract: .squad/CONTRACT-controlmaster.md.
package controlmaster

import (
	"fmt"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// Directive is one ssh_config keyword/value pair.
type Directive struct{ Keyword, Value string }

// Trio is SPEC §2's managed-entry ControlMaster template, in template
// order. A test pins it to SPEC.md's literal lines.
var Trio = [3]Directive{
	{"ControlMaster", "auto"},
	{"ControlPath", "~/.ssh/cm/%C"},
	{"ControlPersist", "30m"},
}

// Missing lists the Trio directives the managed host alias lacks in the
// file at confPath, in template order; nil when the block carries all
// three. Read-only. A keyword counts as present whatever its value, in
// either the `Keyword value` or `Keyword=value` form; a commented-out
// line is a comment, not a directive, and does not count.
func Missing(confPath, alias string) ([]Directive, error) {
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return nil, err
	}
	h, ok := f.Host(alias)
	if !ok {
		return nil, fmt.Errorf("host %q not managed by sasash", alias)
	}
	return missing(h), nil
}

// Enable inserts exactly the directives Missing reports into the block
// and returns them: one atomic write, the file re-read at call time, its
// mode preserved. Each line lands directly under the Host line (Host.Set's
// placement) with the block's indent and the Host line's terminator, so
// the block reads in Trio order. Nil, nil and no write when nothing is
// missing — a second call is a no-op.
func Enable(confPath, alias string) ([]Directive, error) {
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return nil, err
	}
	h, ok := f.Host(alias)
	if !ok {
		return nil, fmt.Errorf("host %q not managed by sasash", alias)
	}
	add := missing(h)
	if add == nil {
		return nil, nil
	}
	// Set inserts each new keyword directly under the Host line, so the
	// last one set ends up first: walk the list backwards to leave the
	// block in Trio order.
	for i := len(add) - 1; i >= 0; i-- {
		h.Set(add[i].Keyword, add[i].Value)
	}
	if err := f.WriteAtomic(confPath); err != nil {
		return nil, err
	}
	return add, nil
}

func missing(h *sshconf.Host) []Directive {
	var out []Directive
	for _, d := range Trio {
		if _, ok := h.Get(d.Keyword); !ok {
			out = append(out, d)
		}
	}
	return out
}
