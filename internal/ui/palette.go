// Package ui implements the sasash TUI: a two-pane host browser over
// internal/hostmodel with live path probing (SPEC §5).
//
// Styling rule (SPEC §5 addendum): every lipgloss style derives from a
// semantic Palette. No hex literal exists outside this file.
package ui

import "github.com/charmbracelet/lipgloss"

// Palette is the semantic color set a theme provides — roles, not widgets.
// NewStyles materializes every widget style from these ten tokens, so a new
// theme is just another Palette value.
type Palette struct {
	Bg        lipgloss.Color // terminal background the theme assumes
	BgSubtle  lipgloss.Color // subtle fills, dimmest chrome
	Fg        lipgloss.Color // primary text
	FgDim     lipgloss.Color // metadata, help text
	Accent    lipgloss.Color // selection, title — used sparingly
	Accent2   lipgloss.Color // secondary accent (active-path marker)
	Success   lipgloss.Color // path up
	Warning   lipgloss.Color // probe pending
	Error     lipgloss.Color // path down
	Selection lipgloss.Color // selected-row fill
}

// Gruvbox is the default theme. Dark-mode palette from
// github.com/morhetz/gruvbox (README palette table, bright fg variants).
var Gruvbox = Palette{
	Bg:        "#282828", // bg0
	BgSubtle:  "#3c3836", // bg1
	Fg:        "#ebdbb2", // fg1
	FgDim:     "#928374", // gray
	Accent:    "#fe8019", // orange
	Accent2:   "#83a598", // blue
	Success:   "#b8bb26", // green
	Warning:   "#fabd2f", // yellow
	Error:     "#fb4934", // red
	Selection: "#504945", // bg2
}
