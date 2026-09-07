package ui_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The popup family's color pass and fixed frame (SASASH.4.15).

// TestNewStyles_BoxRolesFollowPalette pins every box role to its palette
// token in every theme: the reference box is the accent, the alert box
// the warning color, keys and live values the secondary accent, a
// picker's ○ dim — no reuse by accident.
func TestNewStyles_BoxRolesFollowPalette(t *testing.T) {
	t.Parallel()
	for _, th := range ui.Themes {
		t.Run(th.Name, func(t *testing.T) {
			t.Parallel()
			p := th.Palette
			s := ui.NewStyles(p)
			tests := []struct {
				name string
				got  lipgloss.TerminalColor
				want lipgloss.Color
			}{
				{name: "Popup border", got: s.Popup.GetBorderTopForeground(), want: p.Accent},
				{name: "PopupAlert border", got: s.PopupAlert.GetBorderTopForeground(), want: p.Warning},
				{name: "BoxTitle", got: s.BoxTitle.GetForeground(), want: p.Accent},
				{name: "BoxTitleWarn", got: s.BoxTitleWarn.GetForeground(), want: p.Warning},
				{name: "BoxHeading", got: s.BoxHeading.GetForeground(), want: p.Accent},
				{name: "BoxKey", got: s.BoxKey.GetForeground(), want: p.Accent2},
				{name: "BoxDesc", got: s.BoxDesc.GetForeground(), want: p.Fg},
				{name: "BoxValue", got: s.BoxValue.GetForeground(), want: p.Accent2},
				{name: "BoxQuery", got: s.BoxQuery.GetForeground(), want: p.Accent2},
				{name: "BoxOff", got: s.BoxOff.GetForeground(), want: p.FgDim},
			}
			for _, tt := range tests {
				if tt.got != tt.want {
					t.Errorf("%s foreground = %v, want %v", tt.name, tt.got, tt.want)
				}
			}
			if !s.BoxTitle.GetBold() || !s.BoxTitleWarn.GetBold() || !s.BoxKey.GetBold() {
				t.Errorf("BoxTitle, BoxTitleWarn and BoxKey should be bold")
			}
			// The two box kinds differ by color in every theme, as they
			// do by their glyph.
			if p.Accent == p.Warning {
				t.Errorf("Accent and Warning coincide (%v): the box kinds would not read apart", p.Accent)
			}
			// The box's text sits on the theme's background: the roles
			// used for text are never the background itself.
			for _, c := range []lipgloss.Color{p.Accent, p.Accent2, p.Fg, p.FgDim, p.Warning} {
				if c == p.Bg || c == p.Selection {
					t.Errorf("token %v equals a fill: text in it would vanish", c)
				}
			}
		})
	}
}

// TestModel_ApplyTheme_BoxStylesFollowSwitch pins that the model's box
// styles change with t like the rest.
func TestModel_ApplyTheme_BoxStylesFollowSwitch(t *testing.T) {
	t.Parallel()
	m := press(t, taggedModel(t, fakerun.New()), "t") // gruvbox -> mocha
	if got := ui.StylesOf(m).Popup.GetBorderTopForeground(); got != ui.Mocha.Accent {
		t.Errorf("Popup border after t = %v, want mocha %v", got, ui.Mocha.Accent)
	}
	if got := ui.StylesOf(m).BoxKey.GetForeground(); got != ui.Mocha.Accent2 {
		t.Errorf("BoxKey after t = %v, want mocha %v", got, ui.Mocha.Accent2)
	}
}

// TestUI_NoColorLiteralsOutsidePalette pins the styling rule by reading
// the package's sources: a #rrggbb anywhere but palette.go is a leak.
func TestUI_NoColorLiteralsOutsidePalette(t *testing.T) {
	t.Parallel()
	hex := regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("Glob(*.go) = %v, %v", files, err)
	}
	for _, f := range files {
		if f == "palette.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := hex.Find(src); m != nil {
			t.Errorf("%s carries a color literal %q; colors live in palette.go", f, m)
		}
	}
}

// boxSize is a view's box as columns and rows, borders included.
func boxSize(t *testing.T, view string) (w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	top, bottom := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "╭") {
			top = i
			w = lipgloss.Width(strings.TrimSpace(l))
		}
		if strings.Contains(l, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatalf("View() holds no whole box:\n%s", view)
	}
	return w, bottom - top + 1
}

func TestModel_View_SettingsBoxHasAFixedFrame(t *testing.T) {
	t.Parallel()
	// The menu's frame is the same whatever is inside it: the full menu,
	// a search down to one entry, no match at all, a picker; and a
	// second visit is the first's.
	base := press(t, wide(t), "o")
	w0, h0 := boxSize(t, base.View())
	if w0 < 40 || h0 != 6+boxChromeRows {
		t.Fatalf("settings box = %dx%d, want the menu's six rows in a fixed frame:\n%s", w0, h0, base.View())
	}
	tests := []struct {
		name string
		keys []string
	}{
		{name: "search to one entry", keys: []string{"o", "/", "t", "h"}},
		{name: "no match", keys: []string{"o", "/", "z", "z"}},
		{name: "theme picker", keys: []string{"o", "j", "j", "enter"}},
		{name: "spawn picker", keys: []string{"o", "j", "j", "j", "enter"}},
		{name: "second visit", keys: []string{"o", "esc", "o"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := press(t, wide(t), tt.keys...)
			if w, h := boxSize(t, m.View()); w != w0 || h != h0 {
				t.Errorf("settings box = %dx%d, want the menu's %dx%d:\n%s", w, h, w0, h0, m.View())
			}
		})
	}
	// A short frame clamps the fixed height as the popup's room does.
	short := press(t, update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 80, Height: 12}), "o")
	if _, h := boxSize(t, short.View()); h >= h0 || !strings.Contains(short.View(), "of 6 · j/k scroll") {
		t.Errorf("settings box at height 12 = %d rows, want fewer than %d with the scroll hint:\n%s", h, h0, short.View())
	}
}

// boxChromeRows is the frame around the rows: two border rows, the
// title, its blank and the bottom line.
const boxChromeRows = 5

func TestModel_View_PopupFrameIsItsWholeContent(t *testing.T) {
	t.Parallel()
	// The keys panel fits whole on a tall frame; a search narrows the
	// rows inside the same frame, and the attention popup does the same.
	tall := update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 120, Height: 80})
	full := press(t, tall, "?")
	w0, h0 := boxSize(t, full.View())
	if w, h := boxSize(t, press(t, full, "/", "yank").View()); w != w0 || h != h0 {
		t.Errorf("keys panel narrowed = %dx%d, want the whole panel's %dx%d", w, h, w0, h0)
	}
	if w, h := boxSize(t, press(t, full, "/", "zzz").View()); w != w0 || h != h0 {
		t.Errorf("keys panel with no match = %dx%d, want the whole panel's %dx%d", w, h, w0, h0)
	}
	f, _ := failedForward(t)
	w1, h1 := boxSize(t, f.View())
	if w, h := boxSize(t, press(t, f, "/", "Banner").View()); w != w1 || h != h1 {
		t.Errorf("attention popup narrowed = %dx%d, want its whole %dx%d", w, h, w1, h1)
	}
}
