package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// The ? panel (SASASH.4.13): every named binding, grouped by the surface
// it belongs to, generated from the bindings themselves — never hand-
// maintained — and shown in the 4.11 popup (so it scrolls, owns the
// keyboard, closes on esc — ? closes it too — and / narrows its rows to
// the ones naming a key or a verb, 4.14). The group for the
// surface that holds the keys right now comes first. The inline
// expanding help is retired: the main view's footer is a whisper and
// this panel is where every key is advertised.

// helpGroup is one section of the panel.
type helpGroup struct {
	name string
	keys []key.Binding
}

// helpGroups lists the panel's sections in their base order. Group
// MEMBERSHIP is this list (a new binding must be placed here — the
// every-binding test catches one that is not); the keys and their
// wording come from the bindings.
func (m Model) helpGroups() []helpGroup {
	k := m.keys
	groups := []helpGroup{
		{"host list", []key.Binding{k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp, k.Connect, k.Handoff, k.Herdr, k.PickPath, k.Adhoc, k.CopyAddr, k.CopyCmd, k.Edit, k.Filter, k.ClearFilter, k.Help, k.Settings, k.Theme, k.Variants, k.Quit}},
		{"paths (p)", pathsKeys{}.ShortHelp()},
		{"tunnels (l)", append([]key.Binding{k.FocusRight, k.FocusLeft, k.AddFwd, k.Restart, k.Exit}, tunnelsKeys{k: k}.ShortHelp()...)},
		{"prompts", promptGroupKeys()},
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

// promptGroupKeys is the prompts section: enter and esc mean different
// things in different prompts, so the row unions every prompt's verbs
// and points at the prompt's own footer, which says which applies.
func promptGroupKeys() []key.Binding {
	var enters, escs []string
	seen := map[string]bool{}
	for mode := promptConnect; mode < promptModeCount; mode++ {
		for _, b := range (promptKeys{mode: mode}).ShortHelp() {
			h := b.Help()
			if seen[h.Key+h.Desc] {
				continue
			}
			seen[h.Key+h.Desc] = true
			if h.Key == "enter" {
				enters = append(enters, h.Desc)
			} else {
				escs = append(escs, h.Desc)
			}
		}
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", strings.Join(enters, " / ")+" — the prompt's own line says which")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", strings.Join(escs, " / "))),
	}
}

// helpLines renders the groups as the popup's rows: a heading, then
// one `key  description` row per binding, keys padded to a column
// (measured in cells: ↑/k is three wide, not five). A row's plain
// words are the key and the description, so / finds either.
func (m Model) helpLines() []popupLine {
	groups := m.helpGroups()
	width := 0
	for _, g := range groups {
		for _, b := range g.keys {
			width = max(width, lipgloss.Width(b.Help().Key))
		}
	}
	var out []popupLine
	for i, g := range groups {
		if i > 0 {
			out = append(out, popupLine{})
		}
		out = append(out, popupLine{text: m.styles.BoxHeading.Render(g.name), plain: g.name, heading: true})
		for _, b := range g.keys {
			h := b.Help()
			out = append(out, popupLine{text: m.styles.BoxKey.Render(padRight(h.Key, width)) + "  " + m.styles.BoxDesc.Render(h.Desc), plain: h.Key + "  " + h.Desc})
		}
	}
	if line := actionsLine(m.actions); line != "" {
		line = strings.TrimSpace(line)
		out = append(out, popupLine{}, popupLine{text: m.styles.BoxHeading.Render("actions.toml"), plain: "actions.toml", heading: true}, popupLine{text: line, plain: line})
	}
	return out
}

func padRight(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// helpTitle marks the panel: ? and q close it, unlike an attention
// popup, which wants esc or enter.
const helpTitle = "keys — ? q esc closes"

// openHelp shows the panel. (While it is open the popup owns the keys,
// and ? closes it there — see popupKey.)
func (m Model) openHelp() Model {
	m = m.showRows(helpTitle, m.helpLines())
	m.popup.glyph = "?"
	return m
}

// whisper is the main view's footer: the one key that leads everywhere,
// spelled with the same separator as every context hint.
func (m Model) whisper() string {
	return m.styles.HelpKey.Render("?") + " " + m.styles.HelpDesc.Render("keys") + m.styles.HelpSep.Render(helpSeparator) + m.styles.HelpKey.Render("q") + " " + m.styles.HelpDesc.Render("quit")
}

// helpSeparator is the footer's separator between hints.
const helpSeparator = " · "
