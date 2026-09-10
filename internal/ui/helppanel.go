package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// The ? panel (SASASH.4.13): the action bindings, grouped by the surface
// they belong to, generated from the bindings themselves — never hand-
// maintained — and shown in the 4.11 popup (so it scrolls, owns the
// keyboard, closes on esc — ? closes it too — and / narrows its rows to
// the ones naming a key or a verb, 4.14). The group for the
// surface that holds the keys right now comes first. The inline
// expanding help is retired: the main view's footer is a whisper and
// this panel is where the actions are advertised.
//
// The panel lists ACTIONS, not motion (SASASH.4.35): vim movement is
// self-evident and every pane-switch key already names its pane in a
// group heading (paths (p), tunnels (l)), so the navigation rows are
// omitted here — they stay live and keep their place in the contextual
// footers. Groups still PROPOSE their full key set below; the panel
// PRESENTS the actions, dropping any row in panelOmit.
var panelOmit = map[string]bool{
	"↑/k": true, "↓/j": true, "j/k": true, // list / scroll motion
	"g": true, "G": true, "ctrl+d": true, "ctrl+u": true, // first / last / half-page
	"l/tab": true, "h": true, "l": true, // pane and directory hops
	"u/d": true, // the transfer's own two options already show the direction
}

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
		{"host list", []key.Binding{k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp, k.Connect, k.Handoff, k.Herdr, k.PickPath, k.Adhoc, k.CopyAddr, k.CopyCmd, k.Edit, k.FocusRight, k.Filter, k.ClearFilter, k.Help, k.Settings, k.Theme, k.ShowAmbient, k.ShowPreview, k.Rename, k.Quit}},
		{"paths (p)", pathsKeys{}.ShortHelp()},
		// Only the tunnel keys the pane's own footer (tunnelsKeys) does not
		// already carry: +/r are unique here; n and X come from tunnelsKeys
		// below. The pane hops (h back to the host list, l on it) are
		// proposed but panelOmit drops them — the '(l)' heading already
		// names the pane.
		{"tunnels (l)", append([]key.Binding{k.FocusLeft, k.AddFwd, k.Restart}, tunnelsKeys{k: k}.ShortHelp()...)},
		{"copy files (C)", transferHelpKeys(k)},
		{"prompts", promptGroupKeys()},
		{"settings (o)", settingsKeys{}.ShortHelp()},
		{"popups", append(popupKeys{}.ShortHelp(), key.NewBinding(key.WithKeys("enter", "esc"), key.WithHelp("enter/esc", "on a question: enter does what its title says · esc cancels")))},
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
			if h := b.Help(); !panelOmit[h.Key] {
				width = max(width, lipgloss.Width(h.Key))
			}
		}
	}
	var out []popupLine
	for _, g := range groups {
		var rows []popupLine
		for _, b := range g.keys {
			h := b.Help()
			if panelOmit[h.Key] {
				continue // motion is self-evident; the panel lists actions
			}
			rows = append(rows, popupLine{text: m.styles.BoxKey.Render(padRight(h.Key, width)) + "  " + m.styles.BoxDesc.Render(h.Desc), plain: h.Key + "  " + h.Desc})
		}
		if len(rows) == 0 {
			continue // every row was motion — no heading for an empty group
		}
		if len(out) > 0 {
			out = append(out, popupLine{})
		}
		out = append(out, popupLine{text: m.styles.BoxHeading.Render(g.name), plain: g.name, heading: true})
		out = append(out, rows...)
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
