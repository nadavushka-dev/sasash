package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Glanceable rows (SASASH.4.29): every host row carries its state —
// a reachability glyph, the latency of the path ssh dials, and how many
// of its forwards are up — so the whole fleet's health reads from the
// list alone; the detail pane still expands the selected one. The rows
// read the same maps the detail pane reads and never probe or poll on
// their own: a row knows exactly what the pane would show for it.
//
// Row shape, tabular over the longest listed alias:
//
//	› ● api        67ms  2 up
//	  ○ build      down
//	  ◌ nas           …
//
// The "› " prefix is what keeps the selection visible under NO_COLOR
// (bold does not survive it); while the tunnels pane holds focus (dim)
// the row keeps its fill but loses the prefix, so the one "›" on screen
// marks the focused pane's selection. The glyph shape carries the state
// without color the same way.

// hostDelegate renders one single-line host row.
type hostDelegate struct {
	styles Styles
	dim    bool
	// The facts the rows read. statuses is replaced whole per probe
	// round, so the delegate is rebuilt when one lands (m.delegate());
	// the other two are mutated in place and stay live.
	statuses map[string][]probe.Status
	tstatus  map[string]tunnelState
	blocks   map[string]pathBlock
	tunnels  bool // the tunnels section is on: the count column is reserved
}

// delegate builds the list delegate from the model's current facts.
func (m Model) delegate() hostDelegate {
	return hostDelegate{
		styles:   m.styles,
		dim:      m.focus != focusList,
		statuses: m.statuses,
		tstatus:  m.tstatus,
		blocks:   m.blocks,
		tunnels:  m.tunnels != nil,
	}
}

func (hostDelegate) Height() int                               { return 1 }
func (hostDelegate) Spacing() int                              { return 0 }
func (hostDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }

// Column widths: the prefix and glyph, the latency ("1.234s" at most
// from spellLatency's rounding), and the forward count ("12 up").
// minAliasCells is the alias room the count column must leave: below
// it (a narrow pane, the classic 80 columns) the column is dropped
// whole rather than clipping every alias to a stub — the header tally
// still counts the forwards and the detail pane still lists them.
const (
	rowLead       = 4 // "› " + glyph + " "
	latWidth      = 6
	upWidth       = 5
	minAliasCells = 12
)

// rowFixed is what a row spends beside the alias.
func rowFixed(tunnels bool) int {
	n := rowLead + 2 + latWidth
	if tunnels {
		n += 2 + upWidth
	}
	return n
}

// withCount reports whether the rows carry the forward-count column:
// the tunnels section is on and the pane keeps minAliasCells beside it.
func (d hostDelegate) withCount(m list.Model) bool {
	return d.tunnels && m.Width()-rowFixed(true) >= minAliasCells
}

// aliasWidth is the alias column: the longest listed alias, within what
// the pane leaves after the fixed columns, so the rows stay tabular and
// the status columns never fall off the pane's edge.
func (d hostDelegate) aliasWidth(m list.Model) int {
	longest := 1
	for _, it := range m.VisibleItems() {
		if h, ok := it.(hostItem); ok {
			longest = max(longest, lipgloss.Width(h.host.Alias))
		}
	}
	return max(min(longest, m.Width()-rowFixed(d.withCount(m))), 1)
}

// rowStatus picks the path a row reports: the block's HostName path —
// what ssh dials, the detail pane's ← active rule (SASASH.7.2) — when
// one resolves and was probed; else the first alive path in config
// order (what the header counts as reachable); else the first probed.
// ok is false before the host's first probe round. The block is read
// from the cache only: filling it is file I/O, and Render is per row
// per frame.
func (d hostDelegate) rowStatus(h hostmodel.Host) (probe.Status, bool) {
	sts := d.statuses[h.Alias]
	if len(sts) == 0 {
		return probe.Status{}, false
	}
	hostName := h.HostName
	if b, ok := d.blocks[h.Alias]; ok && b.hostName != "" {
		hostName = b.hostName
	}
	if i := activeMark(h.Paths, hostName); i >= 0 && i < len(sts) {
		return sts[i], true
	}
	if s, ok := probe.Select(sts); ok {
		return s, true
	}
	return sts[0], true
}

// upCount is the host's forwards up as of its last poll, 0 when it was
// never polled (the row says nothing rather than guessing).
func (d hostDelegate) upCount(h hostmodel.Host) int {
	ts, ok := d.tstatus[h.Alias]
	if !ok || ts.err != nil {
		return 0
	}
	n := 0
	for _, f := range ts.status.Forwards {
		if f.State == tunnel.Up {
			n++
		}
	}
	return n
}

// clipAlias cuts an alias to the column with a visible ellipsis: the
// status columns after it must survive a long name.
func clipAlias(alias string, width int) string {
	if lipgloss.Width(alias) <= width {
		return alias
	}
	r := []rune(alias)
	return string(r[:max(width-1, 0)]) + "…"
}

func (d hostDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(hostItem)
	if !ok {
		if _, divider := item.(dividerItem); divider {
			renderDivider(w, d.styles, m.Width())
		}
		return // the spacer renders as its blank line
	}
	base, prefix := d.styles.Item, "  "
	switch {
	case index == m.Index() && d.dim:
		base = d.styles.ItemSelectedDim
	case index == m.Index():
		base, prefix = d.styles.ItemSelected, "› "
	case it.ambient:
		base = d.styles.ItemAmbient
	}
	// Every segment inherits the row's fill (and its dimming) so a
	// selected row is one continuous bar; a segment's own color wins.
	seg := func(st lipgloss.Style, s string) string { return st.Inherit(base).Render(s) }

	glyph, extra := seg(d.styles.StatusWait, "◌"), "…"
	if s, ok := d.rowStatus(it.host); ok && s.Alive() {
		glyph, extra = seg(d.styles.StatusUp, "●"), spellLatency(s.Latency)
	} else if ok {
		glyph, extra = seg(d.styles.StatusDown, "○"), "down"
	}
	aliasW := d.aliasWidth(m)
	alias := clipAlias(it.host.Alias, aliasW)
	alias += strings.Repeat(" ", aliasW-lipgloss.Width(alias))

	row := seg(base, prefix) + glyph + seg(base, " "+alias) + seg(d.styles.PathExtra, fmt.Sprintf("  %*s", latWidth, extra))
	if d.withCount(m) {
		up := ""
		if n := d.upCount(it.host); n > 0 {
			up = fmt.Sprintf("%d up", n)
		}
		row += seg(d.styles.PathExtra, fmt.Sprintf("  %*s", upWidth, up))
	}
	fmt.Fprint(w, d.styles.Clamp.MaxWidth(m.Width()).Render(row))
}
