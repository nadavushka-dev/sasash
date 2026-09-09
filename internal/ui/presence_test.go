package ui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// SASASH.4.29/4.32 presence: the wordmark band shows only when the
// terminal has the rows and columns for it, and the frame still fits
// the terminal exactly — every line within the width, exactly height
// lines — at every size, band or not.
func TestModel_View_WordmarkFitsOrPlainTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		wantLogo      bool
	}{
		{name: "full screen", width: 120, height: 40, wantLogo: true},
		{name: "ultrawide", width: 240, height: 60, wantLogo: true},
		{name: "at the threshold", width: 100, height: ui.MinLogoHeight, wantLogo: true},
		{name: "one row short", width: 100, height: ui.MinLogoHeight - 1, wantLogo: false},
		{name: "classic 80x24", width: 80, height: 24, wantLogo: false},
		{name: "half of a 60-row screen", width: 200, height: 30, wantLogo: false},
		{name: "tall but narrow", width: 40, height: 40, wantLogo: false},
		// The face fits with nothing to spare; the tally has its own line,
		// so nothing competes with it (4.32).
		{name: "just the face's width", width: 48, height: 40, wantLogo: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := update(t, taggedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: tt.width, Height: tt.height})
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) != tt.height {
				t.Errorf("View() is %d lines, want the frame's %d:\n%s", len(lines), tt.height, view)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > tt.width {
					t.Errorf("line %d width = %v, want <= %v", i, w, tt.width)
				}
			}
			logo := ui.RenderLogo(ui.DefaultLogoFace(), tt.width, tt.height)
			hh := ui.HeaderHeight(m)
			if (hh > 2) != tt.wantLogo {
				t.Fatalf("HeaderHeight() = %d (wordmark shown %v), want shown %v", hh, hh > 2, tt.wantLogo)
			}
			if !tt.wantLogo {
				if hh != 2 || !strings.Contains(lines[0], "sasash") || !strings.Contains(lines[0], "2 hosts") {
					t.Errorf("plain header: height %d, line 0 %q; want 2 and the title with its tally", hh, lines[0])
				}
				return
			}
			// The band (4.32): the face's rows centered across the frame,
			// then the wake leading the tally on its own centered line,
			// then the blank. The face rows carry nothing else.
			if hh != len(logo)+2 {
				t.Errorf("HeaderHeight() = %d, want the %d face rows, the wake line and a blank", hh, len(logo)+2)
			}
			if strings.Contains(lines[0], "sasash") {
				t.Errorf("line 0 = %q, want the wordmark's first row, not the plain title", lines[0])
			}
			face := lines[:len(logo)]
			if joined := strings.Join(face, "\n"); strings.Contains(joined, "hosts") || strings.Contains(joined, "»") {
				t.Errorf("face rows carry the tally or the wake:\n%s", joined)
			}
			band := lines[hh-2]
			if !strings.Contains(band, "»»»»") || !strings.Contains(band, "2 hosts") || strings.Index(band, "»") > strings.Index(band, "2 hosts") {
				t.Errorf("wake line = %q, want the wake leading the tally", band)
			}
			if strings.TrimSpace(lines[hh-1]) != "" {
				t.Errorf("line %d = %q, want the blank under the header", hh-1, lines[hh-1])
			}
			// Centered within the frame (plus the frame's own indent on
			// an ultrawide terminal): the face block and the wake line
			// each sit at half their slack.
			frame := min(tt.width, ui.MaxFrameWidth)
			framePad := (tt.width - frame) / 2
			faceW, lead := 0, tt.width
			for _, r := range face {
				faceW = max(faceW, lipgloss.Width(strings.TrimSpace(r)))
				lead = min(lead, len(r)-len(strings.TrimLeft(r, " ")))
			}
			if want := framePad + (frame-faceW)/2; lead != want {
				t.Errorf("face block starts at column %d, want %d (frame %d, face %d):\n%s", lead, want, frame, faceW, strings.Join(face, "\n"))
			}
			bandLead := len(band) - len(strings.TrimLeft(band, " "))
			if want := framePad + (frame-lipgloss.Width(strings.TrimSpace(band)))/2; bandLead != want {
				t.Errorf("wake line starts at column %d, want %d: %q", bandLead, want, band)
			}
		})
	}
}

// The default face is a product choice no other test can see (4.32:
// the solid ANSI Shadow face).
func TestRenderLogo_DefaultFaceIsANSIShadow(t *testing.T) {
	t.Parallel()
	if got := ui.DefaultLogoFace(); got != "ansi-shadow" {
		t.Errorf("DefaultLogoFace() = %q, want ansi-shadow", got)
	}
}

// Every shipped face renders on an 80-column full-screen terminal, its
// rows one width (the block centers as a unit). (Color is not asserted:
// go test renders in the ascii profile, so the text here is the
// NO_COLOR text — the same solid glyphs.)
func TestRenderLogo_EveryFace(t *testing.T) {
	t.Parallel()
	names := ui.LogoFaceNames()
	if len(names) < 2 {
		t.Fatalf("LogoFaceNames() = %v, want the candidates", names)
	}
	found := false
	for _, name := range names {
		found = found || name == ui.DefaultLogoFace()
		rows := ui.RenderLogo(name, 80, 40)
		if len(rows) < 4 {
			t.Errorf("RenderLogo(%q) at 80 cols = %d rows, want the face", name, len(rows))
			continue
		}
		for i, r := range rows {
			if w := lipgloss.Width(r); w > 80 || w != lipgloss.Width(rows[0]) {
				t.Errorf("RenderLogo(%q) row %d width = %v, want <= 80 and equal to row 0 (%d)", name, i, w, lipgloss.Width(rows[0]))
			}
		}
	}
	if !found {
		t.Errorf("DefaultLogoFace() = %q, not among %v", ui.DefaultLogoFace(), names)
	}
}

// The frame is capped and centered on a wide terminal: every row moves
// right by half the slack, the top stays the top (lists grow down), and
// the cap places without cutting — a line may still run past the frame
// to the terminal's edge.
func TestModel_View_WideTerminalCentersTheFrame(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		width   int
		wantPad int
	}{
		{name: "80 cols", width: 80, wantPad: 0},
		{name: "at the cap", width: ui.MaxFrameWidth, wantPad: 0},
		{name: "one past the cap", width: ui.MaxFrameWidth + 1, wantPad: 0},
		{name: "wide", width: 200, wantPad: (200 - ui.MaxFrameWidth) / 2},
		{name: "one past a one-cell slack", width: ui.MaxFrameWidth + 2, wantPad: 1},
		{name: "ultrawide", width: 300, wantPad: (300 - ui.MaxFrameWidth) / 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := update(t, taggedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: tt.width, Height: 24})
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) != 24 || strings.TrimSpace(lines[0]) == "" {
				t.Fatalf("View() is %d lines, first %q; want 24 with the header on top:\n%s", len(lines), lines[0], view)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > tt.width {
					t.Errorf("line %d width = %v, want <= %v", i, w, tt.width)
				}
				if strings.TrimSpace(line) == "" {
					continue
				}
				// The plain title and the footer carry their own one
				// leading space; the selected row starts with the mark.
				lead := len(line) - len(strings.TrimLeft(line, " "))
				if strings.Contains(line, "› ") && lead != tt.wantPad {
					t.Errorf("selected row starts at column %d, want %d: %q", lead, tt.wantPad, line)
				}
				if i == 0 && lead != tt.wantPad+1 {
					t.Errorf("header starts at column %d, want %d: %q", lead, tt.wantPad+1, line)
				}
			}
		})
	}
}

func TestModel_View_FrameCapPlacesWithoutCutting(t *testing.T) {
	t.Parallel()
	// A preview long enough to need more than the frame: on a wide
	// terminal it runs past the frame's right edge whole instead of
	// losing its tail to the cap (the honesty surface is not cosmetic).
	hosts := []hostmodel.Host{{Alias: "build-eu-west-bastion-01", Paths: []probe.Path{{Host: "192.0.2.10"}}}}
	m := ui.WithPreviewShown(ui.New(hosts, probe.Prober{Dialer: fakedial.New()}, fakerun.New(), ui.Config{Spawn: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab"}}))
	width := 240
	m = update(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
	var preview string
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "$ tmux") {
			preview = line
		}
	}
	// Whole — every segment main showed at this width — inside the
	// terminal, and the frame shifted left to make the room: the line's
	// indent is smaller than the cap's own centering would give.
	lead := len(preview) - len(strings.TrimLeft(preview, " "))
	if w := lipgloss.Width(preview); w > width || !strings.HasSuffix(preview, "(tmux window)") || w-lead <= ui.MaxFrameWidth || lead >= (width-ui.MaxFrameWidth)/2 {
		t.Errorf("preview width = %d, lead %d (cap %d), line %q; want it whole within the terminal, longer than the cap, with the frame shifted left", w, lead, ui.MaxFrameWidth, preview)
	}
}
