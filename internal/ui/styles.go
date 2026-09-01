package ui

import "github.com/charmbracelet/lipgloss"

// Styles is the single style repository: every lipgloss.Style the UI uses
// lives here, built once from a Palette. Components read Styles and never
// call lipgloss.NewStyle themselves, so switching theme is just rebuilding
// this struct from another Palette.
type Styles struct {
	Title lipgloss.Style // app name in the header line

	// Host list (left pane).
	Item         lipgloss.Style // unselected row
	ItemSelected lipgloss.Style // "› " row: accent text over selection fill

	// Detail (right pane).
	SectionLabel lipgloss.Style // "paths" heading
	PathName     lipgloss.Style
	PathAddr     lipgloss.Style
	PathExtra    lipgloss.Style // latency / "down" / pending ellipsis
	ActiveMark   lipgloss.Style // "← active"
	StatusUp     lipgloss.Style // ● glyph
	StatusDown   lipgloss.Style // ○ glyph
	StatusWait   lipgloss.Style // ◌ glyph

	// Bottom chrome.
	CmdPrompt  lipgloss.Style // the "$"
	CmdPreview lipgloss.Style // ghost text of the command to run
	HelpKey    lipgloss.Style
	HelpDesc   lipgloss.Style
	HelpSep    lipgloss.Style

	Empty lipgloss.Style // empty-state sentence
}

// NewStyles materializes the widget styles for one theme.
func NewStyles(p Palette) Styles {
	return Styles{
		Title: lipgloss.NewStyle().Foreground(p.Accent).Bold(true),

		Item:         lipgloss.NewStyle().Foreground(p.Fg).PaddingLeft(2),
		ItemSelected: lipgloss.NewStyle().Foreground(p.Accent).Background(p.Selection).Bold(true),

		SectionLabel: lipgloss.NewStyle().Foreground(p.FgDim),
		PathName:     lipgloss.NewStyle().Foreground(p.Fg),
		PathAddr:     lipgloss.NewStyle().Foreground(p.FgDim),
		PathExtra:    lipgloss.NewStyle().Foreground(p.FgDim),
		ActiveMark:   lipgloss.NewStyle().Foreground(p.Accent2),
		StatusUp:     lipgloss.NewStyle().Foreground(p.Success),
		StatusDown:   lipgloss.NewStyle().Foreground(p.Error),
		StatusWait:   lipgloss.NewStyle().Foreground(p.Warning),

		CmdPrompt:  lipgloss.NewStyle().Foreground(p.Accent),
		CmdPreview: lipgloss.NewStyle().Foreground(p.FgDim).Italic(true),
		HelpKey:    lipgloss.NewStyle().Foreground(p.FgDim),
		HelpDesc:   lipgloss.NewStyle().Foreground(p.FgDim).Faint(true),
		HelpSep:    lipgloss.NewStyle().Foreground(p.BgSubtle),

		Empty: lipgloss.NewStyle().Foreground(p.FgDim),
	}
}
