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
// Since SASASH.2.11 the paths pane is a stop on the l/tab focus cycle
// (list → paths → tunnels → list), reached by the same focusPathsOn as
// p — one flow, two keys — so picking a path is a right-pane action and
// the v-shown variant rows are no longer the way to do it.

// pathRows are the rows p moves over: the rendered named paths that the
// sasash.conf block also declares (by name — Set works by name), in the
// pane's order, with their index among ALL rendered paths. Each row is
// the BLOCK's path of that name: its address is what enter writes, so
// the plan and the active row are judged against it even when the pane
// shows another address for the same name. Unnamed paths cannot be
// made active, and a rendered name the block lacks would only be
// refused at enter, so both are skipped but keep their line. The block
// is the caller's: read fresh at p, cached for frames.
func pathRows(host hostmodel.Host, b pathBlock) (rows []probe.Path, at []int) {
	declared := make(map[string]probe.Path)
	for _, p := range namedPaths(b.paths) {
		declared[p.Name] = p
	}
	for i, p := range host.Paths {
		if bp, ok := declared[p.Name]; p.Name != "" && ok {
			rows = append(rows, bp)
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

// pathChoice is the one gate for the paths focus, read against disk —
// whether a write is possible at all must not lag an out-of-band edit,
// so the render cache is evicted and refilled here (the cycle re-read
// the file on every keypress too; p and l read it once each). choiceOK
// comes with the block the focus will use; choiceNone is a host with
// nothing to choose (not managed, fewer than two named paths) and
// choiceShadowed a block with a choice the pane does not render (a Host
// above the sasash Include shadows it, or an edit not yet reloaded).
func (m Model) pathChoice(host hostmodel.Host) (pathBlock, pathChoiceKind) {
	if m.pathBlock == nil || m.setPath == nil {
		return pathBlock{}, choiceNone // no switcher wired: nothing the focus could do
	}
	delete(m.blocks, host.Alias)
	b := m.block(host.Alias)
	if !b.managed || len(namedPaths(b.paths)) < 2 {
		return b, choiceNone
	}
	if rows, _ := pathRows(host, b); len(rows) < 2 {
		return b, choiceShadowed
	}
	return b, choiceOK
}

type pathChoiceKind int

const (
	choiceNone pathChoiceKind = iota
	choiceShadowed
	choiceOK
)

// shadowedPaths is the diagnostic for choiceShadowed, the same under p
// and on the cycle's way past.
const shadowedPaths = "p: the paths on screen are not sasash.conf's — e reloads on return"

// focusPathsOn is p on the selected host: the paths focus when there is
// a choice, otherwise the reason as a notice — the cycle's own no-op
// wording for a host without one.
func (m Model) focusPathsOn(host hostmodel.Host) Model {
	b, choice := m.pathChoice(host)
	switch choice {
	case choiceShadowed:
		m.notice = shadowedPaths
		return m
	case choiceNone:
		if !b.managed {
			m.notice = "p: not managed by sasash"
			return m
		}
		m.notice = switchNotice(pathswitchNoop, len(namedPaths(b.paths)), b.hostName)
		return m
	}
	return m.focusPathsWith(host, b)
}

// focusPathsWith takes the paths focus over a block pathChoice accepted:
// the mark on the active row.
func (m Model) focusPathsWith(host hostmodel.Host, b pathBlock) Model {
	rows, _ := pathRows(host, b)
	m = m.setFocus(focusPaths)
	m.pathIdx = max(activeIndex(rows, b.hostName), 0)
	return m
}

// cycleKey is the key that advances the pane-focus cycle from inside a
// pane: FocusRight's keys less tab, which moves the mark there — one
// source of truth with the list's binding.
func (m Model) cycleKey(msg tea.KeyMsg) bool {
	return key.Matches(msg, m.keys.FocusRight) && msg.String() != "tab"
}

// pathsFocusKey routes navigation while the paths pane holds focus:
// j/k move the mark, clamped; g/G jump; enter makes the highlighted path
// active; h/esc go back to the list. Everything else is not handled
// here — action keys keep their meanings, as under the tunnels focus.
func (m Model) pathsFocusKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	host, _ := m.Selected()
	rows, _ := pathRows(host, m.block(host.Alias))
	if m.pathIdx < 0 || m.pathIdx >= len(rows) {
		return m.setFocus(focusList), nil, true // settleFocus keeps this true; belt and braces
	}
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
	case key.Matches(msg, m.keys.FocusLeft), key.Matches(msg, m.keys.ClearFilter):
		return m.setFocus(focusList), nil, true
	case m.cycleKey(msg):
		// On to the tunnels pane (2.11) — tab keeps moving the mark, as
		// under the tunnels focus. A host without tunnels closes the
		// cycle instead: back to the list, with the reason.
		if rows, _ := m.forwardRows(host); len(rows) == 0 {
			m.notice = "no tunnels on " + host.Alias + " — + adds one"
			return m.setFocus(focusList), nil, true
		}
		return m.focusTunnelsOn(host), nil, true
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
	rows, at := pathRows(host, m.block(host.Alias))
	if m.pathIdx < 0 || m.pathIdx >= len(rows) {
		return -1
	}
	return at[m.pathIdx]
}

// activePath is the rendered path index the block's HostName makes
// active under the paths focus (what enter changes), -1 when the pane
// does not hold focus or the HostName matches no named path.
func (m Model) activePath(host hostmodel.Host) int {
	if m.focus != focusPaths {
		return -1
	}
	rows, at := pathRows(host, m.block(host.Alias))
	if i := activeIndex(rows, m.block(host.Alias).hostName); i >= 0 {
		return at[i]
	}
	return -1
}

// pathPlan is the bottom line under the paths focus: exactly what enter
// will make active, by name and host (the port follows the switcher's
// ownership rule at write time, so it is not guessed).
func (m Model) pathPlan(host hostmodel.Host) string {
	b := m.block(host.Alias)
	rows, _ := pathRows(host, b)
	if m.pathIdx < 0 || m.pathIdx >= len(rows) {
		return ""
	}
	row := rows[m.pathIdx]
	plan := "p → " + row.Name + " " + row.Host
	switch active := activeIndex(rows, b.hostName); {
	case active < 0:
		plan += "  (HostName " + b.hostName + " matches no named path)"
	case active == m.pathIdx:
		plan += "  (already active — variants regenerate)"
	}
	return plan
}

// pathsKeys is the help shown while the paths pane holds focus.
type pathsKeys struct{}

func (pathsKeys) ShortHelp() []key.Binding {
	// The way out comes first, as the tunnels footer has it: a narrow
	// footer cuts the tail.
	return []key.Binding{
		key.NewBinding(key.WithKeys("esc", "h"), key.WithHelp("esc/h", "keep current")),
		moveKeys,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "make active")),
		key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "tunnels pane")),
	}
}

func (k pathsKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
