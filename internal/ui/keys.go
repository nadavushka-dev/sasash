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
	Rename      key.Binding
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
		Herdr:       key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "herdr session beside")),
		PickPath:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pick path")),
		CopyAddr:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yank host")),
		CopyCmd:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "yank command")),
		Adhoc:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "ad-hoc ssh")),
		Edit:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit config")),
		FocusRight:  key.NewBinding(key.WithKeys("l", "tab"), key.WithHelp("l/tab", "paths / tunnels pane")),
		FocusLeft:   key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "host list")),
		Toggle:      key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle set")),
		AddFwd:      key.NewBinding(key.WithKeys("+"), key.WithHelp("+", "add forward")),
		CancelFwd:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "kill forward")),
		EditFwd:     key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "edit forward")),
		Restart:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart master")),
		Exit:        key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "kill all + exit master")),
		Filter:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		ClearFilter: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter / back")),
		Theme:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "theme")),
		Settings:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "settings")),
		Rename:      key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rename host")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys")),
		Quit:        key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		ForceQuit:   key.NewBinding(key.WithKeys("ctrl+c")), // no help text: always-on escape hatch
	}
}

// The KeyMap renders nowhere as a strip (SASASH.4.13): the main view's
// footer is a whisper and `?` opens the keys panel, which helpGroups
// builds from these bindings. The context keymaps below are what the
// footer shows while a surface holds the keys.

// moveKeys is the j/k row every focus and panel shares (paths, tunnels,
// settings, the popup).
var moveKeys = key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "move"))

// actionsLine is the keys panel's last section, naming the actions.toml
// entries (SPEC §6): listed so the file is discoverable, and honest about
// not being runnable from the TUI yet.
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
	case promptRename:
		enter = "rename"
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", enter)),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", esc)),
	}
}

func (p promptKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}

// tunnelsKeys is the context help shown while the tunnels pane holds
// focus: what the pane's keys do there, spelled by the same bindings
// the keys panel lists (action keys keep their meanings).
type tunnelsKeys struct{ k KeyMap }

func (t tunnelsKeys) ShortHelp() []key.Binding {
	// The way out comes first: a narrow footer cuts the tail, and this
	// is the one line whose job is to say how to leave the focus.
	return []key.Binding{
		key.NewBinding(key.WithKeys("esc", "h"), key.WithHelp("esc/h", "host list")),
		moveKeys,
		t.k.Toggle,
		t.k.CancelFwd,
		t.k.EditFwd,
		key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "host list (the cycle closes)")),
	}
}

func (k tunnelsKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

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
