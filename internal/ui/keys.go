package ui

import "github.com/charmbracelet/bubbles/key"

// KeyMap names every binding so the help line is generated from the
// bindings themselves (SPEC §5 addendum), never hand-maintained.
type KeyMap struct {
	Up          key.Binding
	Down        key.Binding
	Top         key.Binding
	Bottom      key.Binding
	HalfDown    key.Binding
	HalfUp      key.Binding
	Connect     key.Binding
	CopyAddr    key.Binding
	CopyCmd     key.Binding
	Adhoc       key.Binding
	Edit        key.Binding
	Filter      key.Binding
	ClearFilter key.Binding
	Theme       key.Binding
	Help        key.Binding
	Quit        key.Binding
	ForceQuit   key.Binding
}

// DefaultKeyMap is the vim-style layout. Bare h/l/b/f/u/d stay unbound:
// they page in the bubbles list defaults and would collide with the
// single-letter action keys of later slices.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:          key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("↑/k", "up")),
		Down:        key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("↓/j", "down")),
		Top:         key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "first")),
		Bottom:      key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "last")),
		HalfDown:    key.NewBinding(key.WithKeys("ctrl+d", "pgdown"), key.WithHelp("ctrl+d", "½ page down")),
		HalfUp:      key.NewBinding(key.WithKeys("ctrl+u", "pgup"), key.WithHelp("ctrl+u", "½ page up")),
		Connect:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "ssh")),
		CopyAddr:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yank host")),
		CopyCmd:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "yank command")),
		Adhoc:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "ad-hoc ssh")),
		Edit:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit config")),
		Filter:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		ClearFilter: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter / back")),
		Theme:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:        key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit:   key.NewBinding(key.WithKeys("ctrl+c")), // no help text: always-on escape hatch
	}
}

// ShortHelp implements help.KeyMap; the help bar truncates by width, so
// the most-reached-for keys come first.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Connect, k.Filter, k.Adhoc, k.Help, k.CopyAddr, k.CopyCmd, k.Theme, k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp, k.Quit}
}

// FullHelp implements help.KeyMap, grouped general / navigation / actions;
// `?` expands the help bar into these columns. bubbles drops whole columns
// right-to-left when the terminal is narrow, so the group holding filter,
// esc, help, and quit — the keys the view exists to document — comes first.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Filter, k.ClearFilter, k.Help, k.Quit},
		{k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp},
		{k.Connect, k.Adhoc, k.CopyAddr, k.CopyCmd, k.Edit, k.Theme},
	}
}

// promptKeys is the help shown while a prompt owns the keyboard.
type promptKeys struct{ save bool }

func (p promptKeys) ShortHelp() []key.Binding {
	enter, esc := "connect", "cancel"
	if p.save {
		enter, esc = "save", "skip"
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", enter)),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", esc)),
	}
}

func (p promptKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}

// filterKeys is the help shown while the filter input is focused: only
// the keys that do anything there.
type filterKeys struct{}

func (filterKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply filter")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
}

func (k filterKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}
