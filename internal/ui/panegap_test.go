package ui_test

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestModel_View_PaneGapSeparatesTheColumns(t *testing.T) {
	t.Parallel()
	// SASASH.4.28: the list block is only as wide as its longest row, so
	// the band after it is the whole separation between the columns. The
	// band is wider than the two cells that read as cramped, and every
	// pane row renders exactly that band: blank, with the detail starting
	// right after it. Together the two checks pin the widening itself.
	if ui.PaneGap < 4 {
		t.Errorf("PaneGap = %v, want >= 4", ui.PaneGap)
	}
	tests := []struct {
		name  string
		width int
		alias string // "" for the two short aliases of taggedModel
	}{
		{name: "default", width: 80},
		{name: "wide", width: 120},
		{name: "tmux split", width: 60},
		// An alias past the list clamp: the block is as wide as the list
		// itself, the real geometry the sparkline budget is sized for.
		{name: "alias at the list clamp", width: 80, alias: "build-eu-west-bastion-01-long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var m ui.Model
			if tt.alias == "" {
				m = taggedModel(t, fakerun.New())
			} else {
				hosts := []hostmodel.Host{{Alias: tt.alias, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}}}
				d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond)
				m = ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
				m = update(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 24})
				m = update(t, m, m.Init()())
			}
			m = update(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 24})
			lw := ui.ListPaneWidth(m)
			// Pane rows only: the header (two lines) and the footer both
			// carry text inside the band's columns, so they are skipped by
			// position — a frame that gains a line above or below the
			// panes must move these bounds, not loosen the band check.
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
