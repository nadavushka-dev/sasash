package ui

import "github.com/charmbracelet/bubbles/key"

// KeyMap names every binding so the help line is generated from the
// bindings themselves (SPEC §5 addendum), never hand-maintained.
type KeyMap struct {
	Up        key.Binding
	Down      key.Binding
	Top       key.Binding
	Bottom    key.Binding
	HalfDown  key.Binding
	HalfUp    key.Binding
	Quit      key.Binding
	ForceQuit key.Binding
}

// DefaultKeyMap is the vim-style layout. Bare h/l/b/f/u/d stay unbound:
// they page in the bubbles list defaults and would collide with the
// single-letter action keys of later slices.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:        key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("↓/j", "down")),
		Top:       key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "first")),
		Bottom:    key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "last")),
		HalfDown:  key.NewBinding(key.WithKeys("ctrl+d", "pgdown"), key.WithHelp("ctrl+d", "½ page down")),
		HalfUp:    key.NewBinding(key.WithKeys("ctrl+u", "pgup"), key.WithHelp("ctrl+u", "½ page up")),
		Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c")), // no help text: always-on escape hatch
	}
}

// ShortHelp implements help.KeyMap; the help bar truncates by width.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp, k.Quit}
}

// FullHelp implements help.KeyMap, grouped navigation / general. The full
// help view itself arrives with the help slice (SASASH.1.5).
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp},
		{k.Quit},
	}
}
