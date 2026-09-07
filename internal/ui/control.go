package ui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/setup"
)

// The ControlMaster trio (SASASH.4.18). The tunnel engine's -O commands
// reach a master only through a ControlPath, so a managed entry saved
// without SPEC §2's three directives fails every tunnel action with
// "No ControlPath specified" — and showed its master down for as long
// as it existed. New saves carry the trio (setup.ManagedBlock); an older
// entry is offered exactly the lines it lacks, from the failure itself.

// noControlPath recognises ssh's reason.
func noControlPath(err error) bool {
	return err != nil && strings.Contains(err.Error(), "No ControlPath specified")
}

// controlEnabledMsg reports the write.
type controlEnabledMsg struct {
	host  string
	added []string
	err   error
}

// byHand is the hint for a block sasash will not write.
const byHand = "e to add them by hand"

// offerControl is the failure popup for a No-ControlPath error: the
// reason, then — for a host sasash manages — the missing lines and a
// question whose enter writes them (one atomic insert under the Host
// line, nothing else touched). It says instead of asking when the block
// is not sasash's to write: a host outside sasash.conf (the e hint), a
// generated pinned variant (tunnels ride the parent — a variant with
// directives of its own would fall out of the generator's hands), or a
// block that sets a hostile value (ControlPath none, ControlMaster no)
// sasash never overrides.
func (m Model) offerControl(alias, label string, reason []string) Model {
	title := label + " failed on " + alias + " — no ControlPath"
	lines := append([]string{}, reason...)
	say := func(more ...string) Model { return m.show(title, append(lines, more...)...) }
	trio := "tunnels need ControlMaster auto, ControlPath ~/.ssh/cm/%C and ControlPersist 30m on the host's block"
	if name, variant := m.fold.variants[alias]; variant {
		parent := strings.TrimSuffix(alias, "."+name)
		return say("", alias+" is a generated pinned variant: sasash keeps it to HostName/User/Port, so tunnels ride the parent — use "+parent+" (its master follows the active path)")
	}
	host, ok := m.hostByAlias(alias)
	if !ok || !m.managed(host) || m.inspectControl == nil || m.enableControl == nil {
		return say("", trio+" — not managed by sasash: "+byHand)
	}
	c, err := m.inspectControl(alias)
	switch {
	case errors.Is(err, setup.ErrNotManaged):
		// @sasash meta on a host outside sasash.conf: managed in the
		// list's eyes, but its block is not in the file sasash writes.
		return say("", trio+" — its block is not in sasash.conf: "+byHand)
	case err != nil:
		return say("", "cannot read the block: "+cleanLine(err.Error()))
	}
	if hostile := hostileControl(c.Other); hostile != "" {
		return say("", "the block sets "+hostile+", which keeps a master from being reached — sasash never overrides a hand-written value: change it by hand (e)")
	}
	if len(c.Missing) == 0 {
		return say("", "the block already carries the ControlMaster trio — check ~/.ssh/cm exists (o → create config file)")
	}
	lines = append(lines, "", "enable tunnels for "+alias+": sasash writes under its Host line in ~/.ssh/config.d/sasash.conf")
	for _, l := range c.Missing {
		lines = append(lines, "  "+l)
	}
	lines = append(lines, "nothing else is touched; then space or + again")
	m = m.show("enable tunnels on "+alias+"? enter writes · esc closes", lines...)
	enable := m.enableControl
	m.popup.confirm = func(m Model) (Model, tea.Cmd) {
		m.notice = "enabling tunnels on " + alias + " …"
		return m, func() tea.Msg {
			added, err := enable(alias)
			return controlEnabledMsg{host: alias, added: added, err: err}
		}
	}
	m.popup.verb, m.popup.declined = "write it", "nothing written"
	return m
}

// hostileControl is the first hand-written trio value that defeats the
// engine whatever else is written: a ControlMaster that will not start a
// master (anything but auto/yes/ask) or a ControlPath of none or empty.
// Other values (another socket path, another persist time) are fine.
func hostileControl(other []string) string {
	for _, o := range other {
		kw, v, _ := strings.Cut(o, " ")
		v = strings.ToLower(v)
		switch strings.ToLower(kw) {
		case "controlmaster":
			if v != "auto" && v != "yes" && v != "ask" && v != "autoask" {
				return o
			}
		case "controlpath":
			if v == "" || v == "none" {
				return o
			}
		}
	}
	return ""
}
