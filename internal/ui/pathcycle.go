package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// switchDoneMsg reports a path cycle (pathswitch.Cycle through the hook).
type switchDoneMsg struct {
	host string
	res  pathswitch.Result
	err  error
}

// namedPaths are the paths p cycles over: the named ones, in config order.
func namedPaths(host hostmodel.Host) []probe.Path {
	var out []probe.Path
	for _, p := range host.Paths {
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

// nextPath is what p will make active, by the switcher's own rule: the
// path after the LAST named path whose host equals the block's HostName,
// wrapping; the first named path when none matches (a hand-edited
// HostName). ok is false with fewer than two named paths — the switcher
// then changes nothing.
func nextPath(host hostmodel.Host, hostName string) (probe.Path, bool) {
	named := namedPaths(host)
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
// will make active, or why it cannot.
func (m Model) switchPlan(host hostmodel.Host) string {
	if m.switchPath == nil || m.hostName == nil {
		return ""
	}
	cur, managed := m.hostName(host.Alias)
	if !managed {
		return "p: not managed by sasash"
	}
	next, ok := nextPath(host, cur)
	if !ok {
		return "p: only one path"
	}
	return "p → " + next.Name + " " + next.Addr()
}

// switchCmd runs the cycle off the UI thread.
func (m Model) switchCmd(host hostmodel.Host) tea.Cmd {
	sw, alias := m.switchPath, host.Alias
	return func() tea.Msg {
		res, err := sw(alias)
		return switchDoneMsg{host: alias, res: res, err: err}
	}
}

// switchNotice renders a Result honestly: what moved where, and what
// the generator left alone.
func switchNotice(res pathswitch.Result) string {
	if !res.Changed {
		return "only one named path — nothing to switch"
	}
	from := "?"
	if res.From.Host != "" {
		from = res.From.Name + " " + res.From.Addr()
	}
	s := fmt.Sprintf("HostName: %s → %s %s", from, res.To.Name, res.To.Addr())
	if n := len(res.Unmanaged); n > 0 {
		s += fmt.Sprintf(" · %d pinned %s left as-is (%s)", n, plural(n, "variant"), strings.Join(res.Unmanaged, ", "))
	}
	return s
}
