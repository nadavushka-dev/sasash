package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
)

// hostsReloadedMsg carries the re-read host list after a save or an edit.
type hostsReloadedMsg struct {
	hosts    []hostmodel.Host
	warnings []error
	err      error
	edited   bool // reload follows the editor, not a save
}

// reloadCmd re-reads the config off the UI thread. edited marks a reload
// that follows the editor, whose warnings must reach the user.
func (m Model) reloadCmd(edited bool) tea.Cmd {
	load := m.loadHosts
	return func() tea.Msg {
		hosts, warnings, err := load()
		return hostsReloadedMsg{hosts: hosts, warnings: warnings, err: err, edited: edited}
	}
}

// warningNotice folds load warnings into the one notice line: the first
// verbatim, the rest as a count.
func warningNotice(warnings []error) string {
	s := "config: " + cleanLine(warnings[0].Error())
	if n := len(warnings) - 1; n > 0 {
		s += fmt.Sprintf(" (+%d more)", n)
	}
	return s
}

// clampCursor keeps the cursor on a real row after the visible items
// shrank: bubbles' SetItems never moves it, so a deleted last host would
// leave SelectedItem nil until the next j/k.
func clampCursor(l *list.Model) {
	if n := len(l.VisibleItems()); n > 0 && l.Index() >= n {
		l.Select(n - 1)
	}
}
