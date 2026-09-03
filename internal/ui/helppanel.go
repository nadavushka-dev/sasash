package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

// The ? panel (SASASH.4.13): every named binding, grouped by the surface
// it belongs to, generated from the bindings themselves — never hand-
// maintained — and shown in the 4.11 popup (so it scrolls, owns the
// keyboard, and closes on esc; ? closes it too). The group for the
// surface that holds the keys right now comes first. The inline
// expanding help is retired: the main view's footer is a whisper and
// this panel is where every key is advertised.

// helpGroup is one section of the panel.
type helpGroup struct {
	name string
	keys []key.Binding
}

// helpGroups lists the panel's sections in their base order.
func (m Model) helpGroups() []helpGroup {
	k := m.keys
	groups := []helpGroup{
		{"global", []key.Binding{k.Filter, k.ClearFilter, k.Help, k.Settings, k.Theme, k.Variants, k.Quit}},
		{"hosts", []key.Binding{k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp, k.Connect, k.Handoff, k.Herdr, k.PickPath, k.Adhoc, k.CopyAddr, k.CopyCmd, k.Edit}},
		{"paths (p)", pathsKeys{}.ShortHelp()},
		{"tunnels (l)", append([]key.Binding{k.FocusRight, k.FocusLeft, k.Toggle, k.AddFwd, k.CancelFwd, k.EditFwd, k.Restart, k.Exit}, tunnelsKeys{}.ShortHelp()[0])},
		{"prompts", promptKeys{}.ShortHelp()},
		{"settings (o)", settingsKeys{}.ShortHelp()},
		{"popups", popupKeys{}.ShortHelp()},
	}
	// The surface holding the keys comes first.
	// (A focused prompt owns ? as typed text, so the panel never opens
	// from one; the prompts group keeps its base place.)
	first := ""
	switch {
	case m.settings.pane != settingsOff:
		first = "settings (o)"
	case m.focus == focusPaths:
		first = "paths (p)"
	case m.focus == focusTunnels:
		first = "tunnels (l)"
	}
	for i, g := range groups {
		if g.name == first && i > 0 {
			groups = append([]helpGroup{g}, append(groups[:i:i], groups[i+1:]...)...)
			break
		}
	}
	return groups
}

// helpLines renders the groups as the popup's lines: a heading, then
// one `key  description` row per binding, keys padded to a column.
func (m Model) helpLines() []string {
	groups := m.helpGroups()
	width := 0
	for _, g := range groups {
		for _, b := range g.keys {
			width = max(width, len(b.Help().Key))
		}
	}
	var out []string
	for i, g := range groups {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, m.styles.SectionLabel.Render(g.name))
		for _, b := range g.keys {
			h := b.Help()
			if h.Key == "" {
				continue // ctrl+c: an always-on escape hatch with no help text
			}
			out = append(out, m.styles.HelpKey.Render(padRight(h.Key, width))+"  "+m.styles.HelpDesc.Render(h.Desc))
		}
	}
	if line := actionsLine(m.actions); line != "" {
		out = append(out, "", m.styles.SectionLabel.Render("actions.toml"), strings.TrimSpace(line))
	}
	return out
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// helpTitle marks the panel: ? closes it, unlike an attention popup.
const helpTitle = "keys — ? or esc closes"

// openHelp shows the panel. (While it is open the popup owns the keys,
// and ? closes it there — see popupKey.)
func (m Model) openHelp() Model {
	m = m.show(helpTitle, m.helpLines()...)
	m.popup.glyph = "?"
	return m
}

// whisper is the main view's footer: the one key that leads everywhere.
func (m Model) whisper() string {
	return m.styles.HelpKey.Render("?") + " " + m.styles.HelpDesc.Render("keys") + m.styles.HelpSep.Render(" · ") + m.styles.HelpKey.Render("q") + " " + m.styles.HelpDesc.Render("quit")
}
