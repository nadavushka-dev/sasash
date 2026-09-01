package ui

import tea "github.com/charmbracelet/bubbletea"

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
