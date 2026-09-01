package ui

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// RefreshTick lets the external test package drive the live-refresh loop
// without waiting out the real tick interval.
func RefreshTick() tea.Msg { return refreshTickMsg{} }

// ProbeChunk exposes the refresh fan-out bound for the regression test.
const ProbeChunk = probeChunk

// SSHExit lets tests replay the terminal returning from an ssh handoff.
func SSHExit(err error) tea.Msg { return sshExitMsg{err: err} }

// SSHCommand and ClipArgv expose the argv builders for exact-string tests.
func SSHCommand(alias string) []string { return sshCommand(alias) }
func ClipArgv(text string) []string    { return clipArgv(text) }

// SSHExec exposes the handoff process builder so tests can assert the
// built argv actually reaches the *exec.Cmd.
var SSHExec = sshExec

// FilterState exposes the list's filter state for the filter-flow tests.
func FilterState(m Model) list.FilterState { return m.list.FilterState() }

// StaticCursor turns off the filter input's cursor blink so the key-path
// tests never produce blink tick cmds (which sleep when executed).
func StaticCursor(m Model) Model {
	m.list.FilterInput.Cursor.SetMode(cursor.CursorStatic)
	return m
}
