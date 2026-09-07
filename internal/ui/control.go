package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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

// offerControl is the failure popup for a No-ControlPath error: the
// reason, then — for a host sasash manages — the missing lines and a
// question whose enter writes them (one atomic insert under the Host
// line, nothing else touched); a host sasash does not manage gets the
// e hint instead, since its block is not sasash's to write.
func (m Model) offerControl(alias, label string, reason []string) Model {
	title := label + " failed on " + alias + " — no ControlPath"
	lines := append([]string{}, reason...)
	host, ok := m.hostByAlias(alias)
	if !ok || !m.managed(host) || m.missingControl == nil || m.enableControl == nil {
		lines = append(lines, "", "tunnels need ControlMaster auto, ControlPath ~/.ssh/cm/%C and ControlPersist 30m on the host's block — not managed by sasash: e to add them by hand")
		return m.show(title, lines...)
	}
	missing, err := m.missingControl(alias)
	if err != nil {
		lines = append(lines, "", "cannot read the block: "+cleanLine(err.Error()))
		return m.show(title, lines...)
	}
	if len(missing) == 0 {
		lines = append(lines, "", "the block already carries the ControlMaster trio — check ~/.ssh/cm exists (o → create config file)")
		return m.show(title, lines...)
	}
	lines = append(lines, "", "enable tunnels for "+alias+": sasash writes under its Host line in ~/.ssh/config.d/sasash.conf")
	for _, l := range missing {
		lines = append(lines, "  "+l)
	}
	lines = append(lines, "nothing else is touched; then space or + again")
	m = m.show(title+": enable tunnels? enter writes · esc closes", lines...)
	enable := m.enableControl
	m.popup.confirm = func(m Model) (Model, tea.Cmd) {
		m.notice = "enabling tunnels on " + alias + " …"
		return m, func() tea.Msg {
			added, err := enable(alias)
			return controlEnabledMsg{host: alias, added: added, err: err}
		}
	}
	m.popup.verb = "write it"
	return m
}
