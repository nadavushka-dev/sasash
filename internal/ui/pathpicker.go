package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// pathPicker is the p panel (SASASH.2.8): the host's named paths from its
// sasash.conf block, in config order, with the live probe beside each,
// so the choice is informed. enter makes the highlighted one active
// through pathswitch.Set — the same pipeline the cycle used — and esc
// keeps things as they are. It owns the keyboard while open, like the
// settings panel.
type pathPicker struct {
	host   hostmodel.Host
	block  pathBlock
	rows   []probe.Path // the block's named paths
	cursor int
}

// open reports whether the picker owns the body and the keyboard.
func (p pathPicker) open() bool { return len(p.rows) > 0 }

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

// openPicker is p on the selected host: the picker when there is a
// choice, otherwise the cycle's own no-op wording as a notice.
func (m Model) openPicker(host hostmodel.Host) Model {
	b := m.block(host.Alias)
	if !b.managed {
		m.notice = "p: not managed by sasash"
		return m
	}
	rows := namedPaths(b.paths)
	if len(rows) < 2 {
		m.notice = switchNotice(pathswitchNoop, len(rows), b.hostName)
		return m
	}
	cursor := max(activeIndex(rows, b.hostName), 0)
	m.picker = pathPicker{host: host, block: b, rows: rows, cursor: cursor}
	return m
}

// pickerKey routes a key while the picker owns the keyboard.
func (m Model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &m.picker
	switch {
	case key.Matches(msg, m.keys.ClearFilter):
		m.picker = pathPicker{}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		p.cursor = min(p.cursor+1, len(p.rows)-1)
	case key.Matches(msg, m.keys.Up):
		p.cursor = max(p.cursor-1, 0)
	case key.Matches(msg, m.keys.Top):
		p.cursor = 0
	case key.Matches(msg, m.keys.Bottom):
		p.cursor = len(p.rows) - 1
	case msg.Type == tea.KeyEnter:
		host, name := p.host, p.rows[p.cursor].Name
		m.picker = pathPicker{}
		// The write touches this host's files: no poll meanwhile (a
		// marker of its own, so it never frees a poll's gate).
		m.switching[host.Alias] = true
		return m, m.switchCmd(host, name)
	}
	return m, nil
}

// pickerPlan is the bottom line while the picker is open: exactly what
// enter will make active, by name and host (the port follows the
// switcher's ownership rule at write time, so it is not guessed).
func (m Model) pickerPlan() string {
	p := m.picker
	row := p.rows[p.cursor]
	plan := "p → " + row.Name + " " + row.Host
	if p.cursor == activeIndex(p.rows, p.block.hostName) {
		plan += "  (already active — variants regenerate)"
	}
	return plan
}

// pickerView renders the panel that replaces the panes while open: each
// named path with its live probe — the same glyphs and words as the
// detail pane, matched by dial target since the block and the host
// model can disagree — and the active one marked.
func (m Model) pickerView() string {
	p := m.picker
	var b strings.Builder
	b.WriteString(" " + m.styles.SectionLabel.Render("path for "+p.host.Alias+" — enter makes it active, esc keeps it"))
	nameWidth := 0
	for _, r := range p.rows {
		nameWidth = max(nameWidth, len(r.Name))
	}
	active := activeIndex(p.rows, p.block.hostName)
	for i, r := range p.rows {
		mark := "  "
		if i == p.cursor {
			mark = m.styles.RowMark.Render("› ")
		}
		glyph, extra := m.probeWords(p.host.Alias, r)
		b.WriteString("\n " + mark + glyph + " " + m.styles.PathName.Render(padRight(r.Name, nameWidth)) + " " + m.styles.PathAddr.Render(r.Addr()) + " " + extra)
		if i == active {
			b.WriteString(" " + m.styles.ActiveMark.Render("← active"))
		}
	}
	if active < 0 {
		b.WriteString("\n\n " + m.styles.Empty.Render("HostName "+p.block.hostName+" matches no named path (hand-edited)"))
	}
	return b.String()
}

// probeWords is a path's live status as the detail pane spells it:
// glyph and latency, "down", or the pending "…" when this session has
// not probed that dial target for the alias.
func (m Model) probeWords(alias string, p probe.Path) (glyph, extra string) {
	for _, s := range m.statuses[alias] {
		if s.Path.Addr() != p.Addr() {
			continue
		}
		if s.Alive() {
			return m.styles.StatusUp.Render("●"), m.styles.PathExtra.Render(spellLatency(s.Latency))
		}
		return m.styles.StatusDown.Render("○"), m.styles.PathExtra.Render("down")
	}
	return m.styles.StatusWait.Render("◌"), m.styles.PathExtra.Render("…")
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// pickerKeys is the help shown while the picker owns the keyboard.
type pickerKeys struct{}

func (pickerKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "move")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "make active")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "keep current")),
	}
}

func (k pickerKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }
