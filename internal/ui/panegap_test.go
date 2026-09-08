package ui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestModel_View_PaneGapSeparatesTheColumns(t *testing.T) {
	t.Parallel()
	// SASASH.4.28: the list block is only as wide as its longest alias, so
	// the band after it is the whole separation between the columns. Every
	// pane row keeps that band blank, and the detail starts right after it
	// — no narrower (cramped) and no wider (the gap is the constant).
	tests := []struct {
		name  string
		width int
	}{
		{name: "default", width: 80},
		{name: "wide", width: 120},
		{name: "tmux split", width: 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := taggedModel(t, fakerun.New())
			m = update(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 24})
			lw := ui.ListPaneWidth(m)
			// The pane rows sit between the header (two lines) and the
			// footer; a row shorter than the band is padding, not a pane.
			lines := strings.Split(m.View(), "\n")
			body := lines[2 : len(lines)-2]
			detailStarts := false
			for i, line := range body {
				runes := []rune(line)
				if len(runes) <= lw+ui.PaneGap {
					continue
				}
				if band := string(runes[lw : lw+ui.PaneGap]); strings.TrimSpace(band) != "" {
					t.Errorf("row %d: gap band %q is not blank: %q", i, band, line)
				}
				if runes[lw+ui.PaneGap] != ' ' {
					detailStarts = true
				}
			}
			if !detailStarts {
				t.Errorf("no row starts the detail right after the gap (list width %d, gap %d):\n%s", lw, ui.PaneGap, m.View())
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > tt.width {
					t.Errorf("line %d width = %v, want <= %v", i, w, tt.width)
				}
			}
		})
	}
}
