package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
)

// foldState is shared by pointer between the model and the list's filter
// function (a closure made once in New): which aliases are generated
// variants (alias → path name). It is mutated in place on reload —
// deliberately, so the closure sees fresh data; like the model's other
// maps, the mutation is shared across Model copies. Generated variants
// always fold: they are an ssh-alias detail for external tools, never TUI
// rows, and a filter term that names one is the only way to surface it
// (SASASH.4.23 retired the v toggle and the pinned-variants line).
type foldState struct {
	variants map[string]string
	hidden   map[string]bool // "# @sasash hide" hosts: out of the rows unless named
}

// hiddenSet collects the hosts that asked to be left out of listings.
func hiddenSet(hosts []hostmodel.Host) map[string]bool {
	out := make(map[string]bool)
	for _, h := range hosts {
		if h.Hidden {
			out[h.Alias] = true
		}
	}
	return out
}

// computeVariants classifies generated pinned variants with four
// witnesses, display-only: alias == "<parent>.<name>" for a listed parent
// and one of ITS path names (matched by prefix, so a dotted path name
// works and a multi-dot parent too); the block has exactly one path,
// carries no @sasash meta of its own (a generator never writes any), and
// that path's host is the parent's path host. A user host that merely
// looks dotted, or that says anything a generator would not, stays listed.
func computeVariants(hosts []hostmodel.Host) map[string]string {
	out := make(map[string]string)
	for _, h := range hosts {
		if len(h.Paths) != 1 || len(h.Tags) > 0 || len(h.Forwards) > 0 {
			continue
		}
		for _, parent := range hosts {
			if parent.Alias == h.Alias || !strings.HasPrefix(h.Alias, parent.Alias+".") {
				continue
			}
			name := h.Alias[len(parent.Alias)+1:]
			for _, p := range parent.Paths {
				if p.Name == name && strings.EqualFold(p.Host, h.Paths[0].Host) {
					out[h.Alias] = name
				}
			}
		}
	}
	return out
}

// termNamesVariant reports whether a filter term explicitly asks for the
// variant: it contains the whole alias, or it is a substring of the
// ".<name>" suffix at least as specific as the name itself — so a bare
// parent term does not bring the clutter back.
func termNamesVariant(term, alias, name string) bool {
	term, alias, name = strings.ToLower(term), strings.ToLower(alias), strings.ToLower(name)
	if strings.Contains(term, alias) {
		return true
	}
	suffix := "." + name
	return strings.Contains(suffix, term) && strings.Contains(term, name)
}

// foldFilter wraps the default fuzzy filter: while a filter is active the
// list holds every host, and folded variants are kept out of the matches
// unless the term names them — so the rows never change under a filter,
// the cursor stays where it is, and each keystroke yields one match set.
func foldFilter(fold *foldState) list.FilterFunc {
	return func(term string, targets []string) []list.Rank {
		ranks := list.DefaultFilter(term, targets)
		out := ranks[:0]
		for _, r := range ranks {
			fields := strings.Fields(targets[r.Index]) // FilterValue starts with the alias
			if len(fields) == 0 {
				continue // the divider, which no term names
			}
			alias := fields[0]
			if fold.hidden[alias] && !strings.Contains(strings.ToLower(term), strings.ToLower(alias)) {
				continue // hidden: only a term naming the whole alias reveals it
			}
			if name, variant := fold.variants[alias]; !variant || termNamesVariant(term, alias, name) {
				out = append(out, r)
			}
		}
		return out
	}
}

// listedHosts are the rows outside a filter: the non-variant hosts
// (generated variants always fold). The header tally counts these.
func (m Model) listedHosts() []hostmodel.Host {
	var out []hostmodel.Host
	for _, h := range m.hosts {
		if m.fold.hidden[h.Alias] {
			continue
		}
		if _, variant := m.fold.variants[h.Alias]; variant {
			continue
		}
		out = append(out, h)
	}
	return out
}

// listRows are the items the list holds right now: every host while a
// filter TERM stands (the filter function hides folded variants the term
// does not name), else the listed ones. An empty term is special: bubbles
// never calls the filter function for it and would show every item, so
// the rows stay folded until a character is typed.
func (m Model) listRows() []list.Item {
	if m.list.FilterState() != list.Unfiltered && strings.TrimSpace(m.list.FilterValue()) != "" {
		items := make([]list.Item, len(m.hosts))
		for i, h := range m.hosts {
			items[i] = hostItem{host: h, ambient: !m.managed(h)}
		}
		return items
	}
	return m.tierRows(m.listedHosts())
}

// setRows hands the list its rows, keeping the selection on its alias.
// Outside a filter the index is item space and Select is exact; under a
// filter the alias is stashed and re-selected when the match set lands
// (Select indexes filter space, which SetItems just emptied).
func (m *Model) setRows(items []list.Item) tea.Cmd {
	selected := ""
	if it, ok := m.list.SelectedItem().(hostItem); ok {
		selected = it.host.Alias
	}
	keep := -1
	for i, it := range items {
		if rowKey(it) == selected {
			keep = i
		}
	}
	cmd := m.list.SetItems(items)
	if m.list.FilterState() == list.Unfiltered {
		if keep >= 0 {
			m.list.Select(keep)
		}
		clampCursor(&m.list)
	} else {
		m.reselect = selected
	}
	return cmd
}

// rowsDiffer reports whether the list's items are not rows.
func (m Model) rowsDiffer(rows []list.Item) bool {
	items := m.list.Items()
	if len(items) != len(rows) {
		return true
	}
	for i, it := range items {
		if rowKey(it) != rowKey(rows[i]) {
			return true
		}
	}
	return false
}
