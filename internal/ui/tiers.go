package ui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
)

// Tiers (SASASH.2.9): the list is attention-ordered, not capability-
// ordered. Managed hosts — declared in sasash.conf, or carrying any
// @sasash meta of their own — list first; every other ssh_config entry
// follows below a divider, dimmed. Both tiers do everything. The
// classification uses facts the host model carries (the declaring file,
// meta presence), never a name heuristic. A host with "# @sasash hide"
// leaves the rows altogether unless a filter term names it, the way a
// folded variant does.

// dividerItem is the row between the tiers. It is not a host: the cursor
// never rests on it, and no filter matches it.
type dividerItem struct{}

func (dividerItem) FilterValue() string { return "" }

// dividerLabels are the divider's wordings, longest first: the pane
// takes the longest that fits whole, never a clipped one.
var dividerLabels = []string{"other ssh_config entries", "other entries", "other"}

// managed reports the facts-only classification: the block lives in
// sasash.conf (paths compared cleaned, as the include resolver spelled
// them) or it says anything to sasash.
func (m Model) managed(h hostmodel.Host) bool {
	if h.HasMeta {
		return true
	}
	return m.confPath != "" && h.File != "" && filepath.Clean(h.File) == filepath.Clean(m.confPath)
}

// tierRows are the unfiltered rows: managed hosts, then — when both
// tiers exist — the divider and the ambient hosts.
func (m Model) tierRows(hosts []hostmodel.Host) []list.Item {
	var managed, ambient []list.Item
	for _, h := range hosts {
		if m.managed(h) {
			managed = append(managed, hostItem{host: h})
		} else {
			ambient = append(ambient, hostItem{host: h, ambient: true})
		}
	}
	if len(managed) == 0 || len(ambient) == 0 {
		return append(managed, ambient...)
	}
	return append(append(managed, dividerItem{}), ambient...)
}

// rowKey identifies a row for change detection.
func rowKey(it list.Item) string {
	if h, ok := it.(hostItem); ok {
		return h.host.Alias
	}
	return "\x00divider"
}

// skipDivider moves the cursor off the divider in the direction it was
// travelling (down past it, or up), so it never rests there.
func skipDivider(l *list.Model, before int) {
	if _, divider := l.SelectedItem().(dividerItem); !divider {
		return
	}
	if l.Index() >= before {
		l.CursorDown()
	} else {
		l.CursorUp()
	}
	// Still on it (a one-row list edge): the other way is a host.
	if _, divider := l.SelectedItem().(dividerItem); divider {
		if l.Index() >= before {
			l.CursorUp()
		} else {
			l.CursorDown()
		}
	}
}

// renderDivider draws the tier boundary across the list width, the
// label whole and a rule filling the rest.
func renderDivider(w io.Writer, st Styles, width int) {
	label := dividerLabels[len(dividerLabels)-1]
	for _, l := range dividerLabels {
		if len(l)+4 <= width { // the indent, a space, and at least one rule
			label = l
			break
		}
	}
	fill := max(width-len(label)-3, 1)
	fmt.Fprint(w, st.Divider.MaxWidth(width).Render("  "+label+" "+strings.Repeat("─", fill)))
}

// tally spells the host count with the managed split when both tiers
// are listed.
func (m Model) tally(hosts []hostmodel.Host) string {
	n, managed := len(hosts), 0
	for _, h := range hosts {
		if m.managed(h) {
			managed++
		}
	}
	s := fmt.Sprintf("%d %s", n, plural(n, "host"))
	if managed > 0 && managed < n {
		s += fmt.Sprintf(" (%d managed)", managed)
	}
	return s
}
