package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

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
	Handoff     key.Binding
	Herdr       key.Binding
	PickPath    key.Binding
	CopyAddr    key.Binding
	CopyCmd     key.Binding
	Adhoc       key.Binding
	Edit        key.Binding
	FocusRight  key.Binding
	FocusLeft   key.Binding
	Toggle      key.Binding
	AddFwd      key.Binding
	CancelFwd   key.Binding
	EditFwd     key.Binding
	Restart     key.Binding
	Exit        key.Binding
	Filter      key.Binding
	ClearFilter key.Binding
	Theme       key.Binding
	Settings    key.Binding
	Variants    key.Binding
	Help        key.Binding
	Quit        key.Binding
	ForceQuit   key.Binding
}

// DefaultKeyMap is the vim-style layout. Bare b/f/u/d stay unbound: they
// page in the bubbles list defaults and would collide with the
// single-letter action keys. h/l, reserved since 1.3, move pane focus.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:          key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("↑/k", "up")),
		Down:        key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("↓/j", "down")),
		Top:         key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "first")),
		Bottom:      key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "last")),
		HalfDown:    key.NewBinding(key.WithKeys("ctrl+d", "pgdown"), key.WithHelp("ctrl+d", "½ page down")),
		HalfUp:      key.NewBinding(key.WithKeys("ctrl+u", "pgup"), key.WithHelp("ctrl+u", "½ page up")),
		Connect:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "spawn ssh")),
		Handoff:     key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "ssh here")),
		Herdr:       key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "herdr")),
		PickPath:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pick path")),
		CopyAddr:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yank host")),
		CopyCmd:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "yank command")),
		Adhoc:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "ad-hoc ssh")),
		Edit:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit config")),
		FocusRight:  key.NewBinding(key.WithKeys("l", "tab"), key.WithHelp("l/tab", "tunnels pane")),
		FocusLeft:   key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "host list")),
		Toggle:      key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle set")),
		AddFwd:      key.NewBinding(key.WithKeys("+"), key.WithHelp("+", "add forward")),
		CancelFwd:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "cancel forward")),
		EditFwd:     key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "edit forward")),
		Restart:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart master")),
		Exit:        key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "exit master")),
		Filter:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		ClearFilter: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter / back")),
		Theme:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Settings:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "settings")),
		Variants:    key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "pinned variants")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:        key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit:   key.NewBinding(key.WithKeys("ctrl+c")), // no help text: always-on escape hatch
	}
}

// ShortHelp implements help.KeyMap; the help bar truncates by width, so
// the most-reached-for keys come first.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Connect, k.Filter, k.Adhoc, k.Help, k.Settings, k.CopyAddr, k.CopyCmd, k.Edit, k.Herdr, k.Handoff, k.PickPath, k.FocusRight, k.Toggle, k.AddFwd, k.CancelFwd, k.EditFwd, k.Restart, k.Exit, k.Theme, k.Variants, k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp, k.Quit}
}

// FullHelp implements help.KeyMap, grouped general / navigation / actions /
// tunnels; `?` expands the help bar into these columns. bubbles drops whole
// columns right-to-left when the terminal is narrow, so the group holding
// filter, esc, help, and quit — the keys the view exists to document —
// comes first. e and t keep working but are not listed here: settings
// (o) is their discoverable home (SASASH.4.5).
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Filter, k.ClearFilter, k.Help, k.Settings, k.Variants, k.Quit},
		{k.Up, k.Down, k.Top, k.Bottom, k.HalfDown, k.HalfUp, k.FocusRight, k.FocusLeft},
		{k.Connect, k.Handoff, k.Herdr, k.PickPath, k.Adhoc, k.CopyAddr, k.CopyCmd},
		{k.Toggle, k.AddFwd, k.CancelFwd, k.EditFwd, k.Restart, k.Exit},
	}
}

// actionsLine is the full-help line naming the actions.toml entries
// (SPEC §6): listed so the file is discoverable, and honest about not
// being runnable from the TUI yet.
func actionsLine(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " actions.toml: " + strings.Join(names, ", ") + " — not runnable from the TUI yet"
}

// promptKeys is the help shown while a prompt owns the keyboard.
type promptKeys struct{ mode promptMode }

func (p promptKeys) ShortHelp() []key.Binding {
	enter, esc := "connect", "cancel"
	switch p.mode {
	case promptAlias:
		enter, esc = "save", "skip"
	case promptForward:
		enter = "add"
	case promptNewConn:
		enter = "next"
	case promptOffer:
		enter, esc = "connect", "skip"
	case promptTunnelHost, promptTunnelName:
		enter = "next"
	case promptTunnelSpecs:
		enter = "declare"
	case promptEditFwd:
		enter = "save"
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
