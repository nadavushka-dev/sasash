package ui

import tea "github.com/charmbracelet/bubbletea"

// RefreshTick lets the external test package drive the live-refresh loop
// without waiting out the real tick interval.
func RefreshTick() tea.Msg { return refreshTickMsg{} }
