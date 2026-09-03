package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// switchDoneMsg reports a path switch (pathswitch.Set through the
// hook). before is the block as it was at the keypress — the notice
// needs the pre-switch HostName and path count, and the file has
// changed by the time this lands.
type switchDoneMsg struct {
	host   string
	before pathBlock
	res    pathswitch.Result
	err    error
}

// pathBlock is what the switcher will read: the sasash.conf block's own
// paths (NOT the include-following host model, whose first-value-wins
// block may be another file's) and its HostName. managed is false when
// sasash.conf has no block for the alias.
type pathBlock struct {
	paths    []probe.Path
	hostName string
	managed  bool
}

// block reads the alias's sasash.conf block through the hook, cached
// per alias so the render path does not parse the file on every frame;
// the cache is dropped on reload and after a switch, the two events that
// change it from inside the TUI.
func (m Model) block(alias string) pathBlock {
	if b, ok := m.blocks[alias]; ok {
		return b
	}
	paths, hostName, managed := m.pathBlock(alias)
	b := pathBlock{paths: paths, hostName: hostName, managed: managed}
	m.blocks[alias] = b
	return b
}

// namedPaths are the paths p offers: the named ones, in config order.
func namedPaths(paths []probe.Path) []probe.Path {
	var out []probe.Path
	for _, p := range paths {
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

// switchPlan is the p segment of the host row's preview line: the paths
// the picker will offer, by name, or why there is nothing to pick.
func (m Model) switchPlan(host hostmodel.Host) string {
	if m.setPath == nil || m.pathBlock == nil {
		return ""
	}
	b := m.block(host.Alias)
	if !b.managed {
		return "p: not managed by sasash"
	}
	named := namedPaths(b.paths)
	switch len(named) {
	case 0:
		return "p: no named paths"
	case 1:
		return "p: only one path"
	}
	names := make([]string, len(named))
	for i, p := range named {
		names[i] = p.Name
	}
	return "p: pick " + strings.Join(names, " / ")
}

// pathswitchNoop is the Result the picker's no-op notices render from:
// nothing changed, which switchNotice words by the named-path count.
var pathswitchNoop = pathswitch.Result{}

// switchCmd runs pathswitch.Set for one named path off the UI thread.
// The block is read fresh there, just before the switch — the cache may
// lag an out-of-band edit, and the notice must state what was actually
// there.
func (m Model) switchCmd(host hostmodel.Host, name string) tea.Cmd {
	sw, read, alias := m.setPath, m.pathBlock, host.Alias
	return func() tea.Msg {
		paths, hostName, managed := read(alias)
		before := pathBlock{paths: paths, hostName: hostName, managed: managed}
		res, err := sw(alias, name)
		return switchDoneMsg{host: alias, before: before, res: res, err: err}
	}
}

// switchNotice renders a Result honestly: what moved where, and what
// the generator left alone. named is how many named paths the block
// has (for the nothing-to-switch wording) and hostName its HostName
// before the switch (for a From that matched no path).
func switchNotice(res pathswitch.Result, named int, hostName string) string {
	if !res.Changed {
		if named == 0 {
			return "no named paths — nothing to switch"
		}
		return "only one named path — nothing to switch"
	}
	from := hostName + " (no path)"
	if res.From.Host != "" {
		from = res.From.Name + " " + res.From.Addr()
	}
	s := fmt.Sprintf("HostName: %s → %s %s", from, res.To.Name, res.To.Addr())
	if n := len(res.Unmanaged); n > 0 {
		s += fmt.Sprintf(" · %d pinned %s left as-is (%s)", n, plural(n, "variant"), strings.Join(res.Unmanaged, ", "))
	}
	return s
}
