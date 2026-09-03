package ui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
)

// variantOf classifies alias as a generated pinned variant of another
// listed host: "<parent>.<name>", split at the LAST dot, where parent is
// a listed host AND name is one of that parent's path names. Both
// witnesses are required (the two-witness caution the generator itself
// applies): a user host that merely looks dotted — web.staging with no
// such parent, or web.office whose parent has no "office" path — is not
// a variant and stays listed. Display-only: nothing is ever written.
func variantOf(alias string, hosts []hostmodel.Host) (parent, name string, ok bool) {
	dot := strings.LastIndex(alias, ".")
	if dot <= 0 || dot == len(alias)-1 {
		return "", "", false
	}
	parent, name = alias[:dot], alias[dot+1:]
	for _, h := range hosts {
		if h.Alias != parent {
			continue
		}
		for _, p := range h.Paths {
			if p.Name == name {
				return parent, name, true
			}
		}
		return "", "", false
	}
	return "", "", false
}

// variantsOf lists the pinned variant names of parent, in list order.
func variantsOf(parent string, hosts []hostmodel.Host) []string {
	var out []string
	for _, h := range hosts {
		if p, name, ok := variantOf(h.Alias, hosts); ok && p == parent {
			out = append(out, name)
		}
	}
	return out
}

// visibleHosts are the rows the list shows: every host when variants are
// shown, otherwise the non-variants plus any variant the filter term
// names explicitly — the term matches the variant's ".<name>" suffix
// (office, .office, web.office), so a bare parent term does not bring
// the clutter back.
func (m Model) visibleHosts() []hostmodel.Host {
	term := strings.ToLower(strings.TrimSpace(m.list.FilterValue()))
	var out []hostmodel.Host
	for _, h := range m.hosts {
		_, name, ok := variantOf(h.Alias, m.hosts)
		if !ok || m.showVariants || (term != "" && termNamesVariant(term, h.Alias, name)) {
			out = append(out, h)
		}
	}
	return out
}

// termNamesVariant reports whether a filter term explicitly asks for the
// variant: it contains the whole alias, or it is a substring of the
// ".<name>" suffix at least as specific as the name itself.
func termNamesVariant(term, alias, name string) bool {
	alias, name = strings.ToLower(alias), strings.ToLower(name)
	if strings.Contains(term, alias) {
		return true
	}
	suffix := "." + name
	return strings.Contains(suffix, term) && strings.Contains(term, name)
}

// rebuildItems hands the list the visible rows when they changed,
// keeping the selection on the same alias when it is still visible.
// Returns the list's own cmd (a filter re-run while one is active).
func (m *Model) rebuildItems() (changed bool, cmd tea.Cmd) {
	visible := m.visibleHosts()
	current := make([]string, 0, len(m.list.Items()))
	for _, it := range m.list.Items() {
		current = append(current, it.(hostItem).host.Alias)
	}
	want := make([]string, 0, len(visible))
	for _, h := range visible {
		want = append(want, h.Alias)
	}
	if strings.Join(current, "\x00") == strings.Join(want, "\x00") {
		return false, nil
	}
	selected := ""
	if it, ok := m.list.SelectedItem().(hostItem); ok {
		selected = it.host.Alias
	}
	items := make([]list.Item, len(visible))
	keep := -1
	for i, h := range visible {
		items[i] = hostItem{host: h}
		if h.Alias == selected {
			keep = i
		}
	}
	c := m.list.SetItems(items)
	if keep >= 0 {
		m.list.Select(keep)
	}
	clampCursor(&m.list)
	return true, c
}

// pinnedLine renders a parent's folded variants in its detail pane.
func (m Model) pinnedLine(host hostmodel.Host) string {
	names := variantsOf(host.Alias, m.hosts)
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	for i, n := range names {
		names[i] = "." + n
	}
	hint := "v shows them"
	if m.showVariants {
		hint = "listed"
	}
	return "\n " + m.styles.SectionLabel.Render("pinned") + " " + m.styles.PathName.Render(strings.Join(names, " ")) + " " + m.styles.PathExtra.Render("("+hint+")")
}

// forceRebuild rebuilds the rows even when the aliases are unchanged (a
// reload replaces the host values behind the same aliases).
func (m *Model) forceRebuild() (bool, tea.Cmd) {
	visible := m.visibleHosts()
	selected := ""
	if it, ok := m.list.SelectedItem().(hostItem); ok {
		selected = it.host.Alias
	}
	items := make([]list.Item, len(visible))
	keep := -1
	for i, h := range visible {
		items[i] = hostItem{host: h}
		if h.Alias == selected {
			keep = i
		}
	}
	c := m.list.SetItems(items)
	if keep >= 0 {
		m.list.Select(keep)
	}
	clampCursor(&m.list)
	return true, c
}
