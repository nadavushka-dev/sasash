package pathswitch

import (
	"strings"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// Generated reports whether the block named alias in f is one the
// generator provably wrote — a "<parent>.<name>" block of the generated
// shape, anchored in a Header run (anchoredStarts: the on-disk ownership
// token) — and which parent's it is. It is the question the switcher
// itself asks before touching a variant, exported so that any other
// writer (SASASH.4.18: the ControlMaster-trio insert) can refuse the
// same blocks by the same rule instead of reconstructing ownership from
// meta, which a hand edit to a path name or address defeats. Read-only.
func Generated(f *sshconf.File, alias string) (parent string, ok bool) {
	hosts := f.Hosts()
	var block *sshconf.Host
	for i := range hosts {
		if hosts[i].Name() == alias {
			block = &hosts[i]
			break
		}
	}
	if block == nil {
		return "", false
	}
	for _, p := range hosts {
		name := p.Name()
		if name == "" || !strings.HasPrefix(alias, name+".") {
			continue
		}
		if anchoredStarts(f, name+".")[block.Start] {
			return name, true
		}
	}
	return "", false
}
