package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// Path selection in the paths pane (SASASH.2.10): p moves the focus onto
// the path rows the detail pane already renders — glyph, latency and
// active marker in place — the way l moves it onto the tunnels rows.
// j/k move the mark, enter makes the highlighted path active through
// pathswitch.Set (the same pipeline the cycle and the 2.8 overlay used),
// esc/h return to the list. Any host change drops the focus (settleFocus).

// pathRows are the rows p moves over: the rendered named paths, in the
// pane's order, with their index among ALL rendered paths (unnamed ones
// cannot be made active, so they are skipped but keep their line).
func pathRows(host hostmodel.Host) (rows []probe.Path, at []int) {
	for i, p := range host.Paths {
		if p.Name != "" {
			rows = append(rows, p)
			at = append(at, i)
		}
	}
	return rows, at
}

// activeIndex is the row the block's HostName makes active: the LAST
// named path whose host matches (the switcher's own rule when several
// share a host), -1 when none does (a hand-edited HostName).
func activeIndex(rows []probe.Path, hostName string) int {
	cur := -1
	for i, p := range rows {
		if strings.EqualFold(p.Host, hostName) {
			cur = i
		}
	}
	return cur
}

// focusPathsOn is p on the selected host: the paths focus when there is
// a choice, otherwise the cycle's own no-op wording as a notice. The
// block is read against disk here, not the render cache: whether a
// write is possible at all must not lag an out-of-band edit (the cycle
// re-read the file on every keypress too).
func (m Model) focusPathsOn(host hostmodel.Host) Model {
	delete(m.blocks, host.Alias)
	b := m.block(host.Alias)
	if !b.managed {
		m.notice = "p: not managed by sasash"
		return m
	}
	if len(namedPaths(b.paths)) < 2 {
		m.notice = switchNotice(pathswitchNoop, len(namedPaths(b.paths)), b.hostName)
		return m
	}
	rows, _ := pathRows(host)
	if len(rows) == 0 {
		m.notice = "p: no named paths rendered for " + host.Alias
		return m
	}
	m = m.setFocus(focusPaths)
	m.pathIdx = max(activeIndex(rows, b.hostName), 0)
	return m
}

// pathsFocusKey routes navigation while the paths pane holds focus:
// j/k move the mark, clamped; g/G jump; enter makes the highlighted path
// active; h/esc go back to the list. Everything else is not handled
// here — action keys keep their meanings, as under the tunnels focus.
func (m Model) pathsFocusKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	host, _ := m.Selected()
	rows, _ := pathRows(host)
	move := func(d int) Model {
		m.pathIdx = min(max(m.pathIdx+d, 0), len(rows)-1)
		return m
	}
	switch {
	case key.Matches(msg, m.keys.Down) || msg.String() == "tab":
		return move(1), nil, true
	case key.Matches(msg, m.keys.Up) || msg.String() == "shift+tab":
		return move(-1), nil, true
	case key.Matches(msg, m.keys.HalfDown):
		return move(m.halfPage()), nil, true
	case key.Matches(msg, m.keys.HalfUp):
		return move(-m.halfPage()), nil, true
	case key.Matches(msg, m.keys.Top):
		m.pathIdx = 0
		return m, nil, true
	case key.Matches(msg, m.keys.Bottom):
		m.pathIdx = len(rows) - 1
		return m, nil, true
	case key.Matches(msg, m.keys.FocusLeft), key.Matches(msg, m.keys.ClearFilter) && !m.help.ShowAll:
		return m.setFocus(focusList), nil, true
	case msg.Type == tea.KeyEnter:
		name := rows[m.pathIdx].Name
		m = m.setFocus(focusList)
		// The write touches this host's files: no poll meanwhile (a
		// marker of its own, so it never frees a poll's gate).
		m.switching[host.Alias] = true
		return m, m.switchCmd(host, name), true
	}
	return m, nil, false
}

// highlightedPath is the rendered path index the mark sits on, -1 when
// the paths pane does not hold focus.
func (m Model) highlightedPath(host hostmodel.Host) int {
	if m.focus != focusPaths {
		return -1
	}
	rows, at := pathRows(host)
	if m.pathIdx < 0 || m.pathIdx >= len(rows) {
		return -1
	}
	return at[m.pathIdx]
}

// pathPlan is the bottom line under the paths focus: exactly what enter
// will make active, by name and host (the port follows the switcher's
// ownership rule at write time, so it is not guessed).
func (m Model) pathPlan(host hostmodel.Host) string {
	rows, _ := pathRows(host)
	if m.pathIdx < 0 || m.pathIdx >= len(rows) {
		return ""
	}
	row := rows[m.pathIdx]
	plan := "p → " + row.Name + " " + row.Host
	if m.pathIdx == activeIndex(rows, m.block(host.Alias).hostName) {
		plan += "  (already active — variants regenerate)"
	}
	return plan
}

// pathsKeys is the help shown while the paths pane holds focus.
type pathsKeys struct{}

func (pathsKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "move")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "make active")),
		key.NewBinding(key.WithKeys("esc", "h"), key.WithHelp("esc/h", "keep current")),
	}
}

func (k pathsKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
