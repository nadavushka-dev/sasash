package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// switchDoneMsg reports a path cycle (pathswitch.Cycle through the
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

// namedPaths are the paths p cycles over: the named ones, in config order.
func namedPaths(paths []probe.Path) []probe.Path {
	var out []probe.Path
	for _, p := range paths {
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

// nextPath is what p will make active, by the switcher's own rule over
// the block's paths: the one after the LAST named path whose host equals
// the block's HostName, wrapping; the first named path when none matches
// (a hand-edited HostName). ok is false with fewer than two named paths
// — the switcher then changes nothing.
func nextPath(paths []probe.Path, hostName string) (probe.Path, bool) {
	named := namedPaths(paths)
	if len(named) < 2 {
		return probe.Path{}, false
	}
	cur := -1
	for i, p := range named {
		if strings.EqualFold(p.Host, hostName) {
			cur = i
		}
	}
	return named[(cur+1)%len(named)], true
}

// switchPlan is the p segment of the preview line: exactly which path p
// will make active, or why it cannot. The target is named by host, not
// port: the switcher's port-ownership rule decides the port at write
// time, and a guessed one would be the one thing on the line that could
// be wrong.
func (m Model) switchPlan(host hostmodel.Host) string {
	if m.switchPath == nil || m.pathBlock == nil {
		return ""
	}
	b := m.block(host.Alias)
	if !b.managed {
		return "p: not managed by sasash"
	}
	next, ok := nextPath(b.paths, b.hostName)
	if !ok {
		if len(namedPaths(b.paths)) == 0 {
			return "p: no named paths"
		}
		return "p: only one path"
	}
	return "p → " + next.Name + " " + next.Host
}

// switchCmd runs the cycle off the UI thread, carrying the block as
// read before the switch.
func (m Model) switchCmd(host hostmodel.Host) tea.Cmd {
	sw, alias, before := m.switchPath, host.Alias, m.block(host.Alias)
	return func() tea.Msg {
		res, err := sw(alias)
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
