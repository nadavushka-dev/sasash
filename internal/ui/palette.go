// Package ui implements the sasash TUI: a two-pane host browser over
// internal/hostmodel with live path probing (SPEC §5).
//
// Styling rule (SPEC §5 addendum): every lipgloss style derives from a
// semantic Palette. No hex literal exists outside this file.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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

// Mocha is Catppuccin Mocha. Values vendored from the canonical palette at
// github.com/catppuccin/palette — the official catppuccin/go library would
// serve the same values, but SPEC §8 caps the dependency list.
var Mocha = Palette{
	Bg:        "#1e1e2e", // base
	BgSubtle:  "#313244", // surface0
	Fg:        "#cdd6f4", // text
	FgDim:     "#6c7086", // overlay0
	Accent:    "#cba6f7", // mauve
	Accent2:   "#89b4fa", // blue
	Success:   "#a6e3a1", // green
	Warning:   "#f9e2af", // yellow
	Error:     "#f38ba8", // red
	Selection: "#45475a", // surface1
}

// TokyoNight is the night variant, vendored from
// github.com/folke/tokyonight.nvim (extras/lua/tokyonight_night.lua).
var TokyoNight = Palette{
	Bg:        "#1a1b26", // bg
	BgSubtle:  "#292e42", // bg_highlight
	Fg:        "#c0caf5", // fg
	FgDim:     "#565f89", // comment
	Accent:    "#7aa2f7", // blue
	Accent2:   "#bb9af7", // magenta
	Success:   "#9ece6a", // green
	Warning:   "#e0af68", // yellow
	Error:     "#f7768e", // red
	Selection: "#283457", // bg_visual
}

// Nord is vendored from nordtheme.com/docs/colors-and-palettes.
var Nord = Palette{
	Bg:        "#2e3440", // nord0
	BgSubtle:  "#3b4252", // nord1
	Fg:        "#eceff4", // nord6
	FgDim:     "#4c566a", // nord3
	Accent:    "#88c0d0", // nord8
	Accent2:   "#81a1c1", // nord9
	Success:   "#a3be8c", // nord14
	Warning:   "#ebcb8b", // nord13
	Error:     "#bf616a", // nord11
	Selection: "#434c5e", // nord2
}

// Theme pairs a palette with the name users select it by.
type Theme struct {
	Name    string
	Palette Palette
}

// Themes is the shipped set, in `t`-cycle order; Themes[0] is the default.
// Custom user themes are deferred (DEC-custom-themes) — a future loader
// only needs to append here.
var Themes = []Theme{
	{Name: "gruvbox", Palette: Gruvbox},
	{Name: "mocha", Palette: Mocha},
	{Name: "tokyonight", Palette: TokyoNight},
	{Name: "nord", Palette: Nord},
}

// ThemeIndex resolves a case-insensitive theme name; ok is false for a
// name not in Themes (including "").
func ThemeIndex(name string) (int, bool) {
	for i, t := range Themes {
		if strings.EqualFold(t.Name, name) {
			return i, true
		}
	}
	return 0, false
}
