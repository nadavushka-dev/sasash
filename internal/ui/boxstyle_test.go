package ui_test

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
				{name: "BoxOff", got: s.BoxOff.GetForeground(), want: p.Fg},
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
			// Legible in every theme: the roles the box paints text and
			// glyphs in clear WCAG's 3:1 floor for graphics against the
			// ground they sit on — the background, and the selection fill
			// under the highlighted row's accent.
			pairs := []struct {
				name   string
				fg, bg lipgloss.Color
			}{
				{name: "Fg on Bg", fg: p.Fg, bg: p.Bg},
				{name: "Accent on Bg", fg: p.Accent, bg: p.Bg},
				{name: "Accent2 on Bg", fg: p.Accent2, bg: p.Bg},
				{name: "Warning on Bg", fg: p.Warning, bg: p.Bg},
				{name: "Success on Bg", fg: p.Success, bg: p.Bg},
				{name: "Accent on Selection", fg: p.Accent, bg: p.Selection},
			}
			for _, pr := range pairs {
				if r := contrast(t, pr.fg, pr.bg); r < 3 {
					t.Errorf("%s contrast = %.2f, want >= 3", pr.name, r)
				}
			}
		})
	}
}

// contrast is the WCAG contrast ratio of two #rrggbb colors.
func contrast(t *testing.T, a, b lipgloss.Color) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(t *testing.T, c lipgloss.Color) float64 {
	t.Helper()
	hex := strings.TrimPrefix(string(c), "#")
	if len(hex) != 6 {
		t.Fatalf("color %q is not #rrggbb", c)
	}
	var ch [3]float64
	for i := range ch {
		v, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		s := float64(v) / 255
		if s <= 0.03928 {
			ch[i] = s / 12.92
		} else {
			ch[i] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
}

// marked is the gruvbox styles with each box role wrapped so its output
// carries a marker: which role a surface reaches for becomes visible in
// any color profile, without a golden frame.
func marked() ui.Styles {
	s := ui.NewStyles(ui.Gruvbox)
	tag := func(st lipgloss.Style, name string) lipgloss.Style {
		return st.Transform(func(text string) string { return "«" + name + "»" + text })
	}
	s.Popup, s.PopupAlert = tag(s.Popup, "REF"), tag(s.PopupAlert, "ALERT")
	s.BoxTitle, s.BoxTitleWarn = tag(s.BoxTitle, "TITLE"), tag(s.BoxTitleWarn, "WARNTITLE")
	s.BoxHeading, s.BoxKey, s.BoxDesc = tag(s.BoxHeading, "HEAD"), tag(s.BoxKey, "KEY"), tag(s.BoxDesc, "DESC")
	s.BoxValue, s.BoxQuery, s.BoxOff = tag(s.BoxValue, "VALUE"), tag(s.BoxQuery, "QUERY"), tag(s.BoxOff, "OFF")
	s.ItemSelected, s.PathName = tag(s.ItemSelected, "SEL"), tag(s.PathName, "NAME")
	s.StatusUp = tag(s.StatusUp, "UP")
	return s
}

// TestModel_View_BoxSurfacesReachTheirRoles pins the color half of the
// slice past the Styles struct: each surface paints with the role meant
// for it, on a wide frame so nothing is cut.
func TestModel_View_BoxSurfacesReachTheirRoles(t *testing.T) {
	t.Parallel()
	base := ui.WithStyles(update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 160, Height: 40}), marked())
	tests := []struct {
		name    string
		keys    []string
		want    []string
		wantNot []string
	}{
		{name: "settings menu", keys: []string{"o"},
			want:    []string{"«REF»", "«TITLE»settings", "› «SEL»create config file", "  «NAME»edit config file", "«VALUE»gruvbox", "«VALUE»tab"},
			wantNot: []string{"«ALERT»", "«WARNTITLE»"}},
		{name: "settings search", keys: []string{"o", "/", "th"}, want: []string{"«QUERY»/th▏", "› «SEL»theme"}},
		{name: "theme picker", keys: []string{"o", "j", "j", "enter"},
			want: []string{"› «UP»● «SEL»gruvbox", "  «OFF»○ «NAME»mocha"}},
		{name: "spawn picker", keys: []string{"o", "j", "j", "j", "enter"},
			want: []string{"› «UP»● «SEL»tab", "  «OFF»○ «NAME»pane"}},
		{name: "keys panel", keys: []string{"?"},
			want:    []string{"«REF»", "«TITLE»? keys", "«HEAD»host list", "«KEY»↑/k", "«DESC»up"},
			wantNot: []string{"«ALERT»"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			view := press(t, base, tt.keys...).View()
			for _, want := range tt.want {
				if !strings.Contains(view, want) {
					t.Errorf("View() missing %q:\n%s", want, view)
				}
			}
			for _, not := range tt.wantNot {
				if strings.Contains(view, not) {
					t.Errorf("View() carries %q:\n%s", not, view)
				}
			}
		})
	}
	// The attention popup is the other kind.
	f, _ := failedForward(t)
	view := ui.WithStyles(f, marked()).View()
	if !strings.Contains(view, "«ALERT»") || !strings.Contains(view, "«WARNTITLE»!") || strings.Contains(view, "«REF»") || strings.Contains(view, "«TITLE»") {
		t.Errorf("attention popup should paint with the alert roles:\n%s", view)
	}
}

// TestModel_View_SettingsRowsFitTheFixedWidth keeps the constant honest:
// every menu row with its fixed detail fits the frame with slack, and a
// value that does not — a config path outside home — is cut on its
// row, never wrapped onto another.
func TestModel_View_SettingsRowsFitTheFixedWidth(t *testing.T) {
	t.Parallel()
	longest := 0
	for i, label := range []string{"create config file", "edit config file", "theme", "spawn target", "add new connection", "add new tunnel"} {
		row := "  " + label
		switch i {
		case 1:
			row += "  ~/.ssh/config.d/sasash.conf"
		case 2:
			for _, th := range ui.Themes {
				longest = max(longest, lipgloss.Width(row+"  "+th.Name))
			}
		case 3:
			row += "  workspace (herdr workspace)"
		}
		longest = max(longest, lipgloss.Width(row))
	}
	if longest > 60-4 {
		t.Errorf("longest menu row = %d cells, want <= 56 to leave the fixed width slack", longest)
	}
	long := "/Volumes/work-laptop/engineering/team/.ssh/config.d/sasash.conf"
	m := press(t, ui.WithEditor(update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 120, Height: 30}), []string{"vi"}, "", long), "o")
	view := m.View()
	w, h := boxSize(t, view)
	if h != 6+boxChromeRows || w != 66 {
		t.Errorf("settings box with a long path = %dx%d, want 66x%d (cut, not wrapped):\n%s", w, h, 6+boxChromeRows, view)
	}
	if strings.Contains(view, "sasash.conf") || !strings.Contains(view, "╰") {
		t.Errorf("View() should cut the long path inside the row and keep the box whole:\n%s", view)
	}
	for _, l := range boxLines(view) {
		if l != "" && !strings.HasPrefix(l, " ") {
			t.Errorf("box row lost its indent: %q", l)
		}
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
	// A hex literal, or any lipgloss color constructor: palette.go is
	// the one place either belongs.
	hex := regexp.MustCompile(`#[0-9a-fA-F]{6}\b|lipgloss\.(Color|ANSIColor|AdaptiveColor|CompleteColor|CompleteAdaptiveColor)\(`)
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
	// Every pane's title fits the fixed width whole at 80 columns: the
	// instruction is never cut.
	for _, tt := range []struct {
		name, want string
		keys       []string
	}{
		{name: "spawn", want: "applies and persists", keys: []string{"o", "j", "j", "j", "enter"}},
		{name: "theme", want: "enter applies and persists", keys: []string{"o", "j", "j", "enter"}},
	} {
		m := press(t, update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 80, Height: 24}), tt.keys...)
		if view := m.View(); !strings.Contains(view, tt.want) {
			t.Errorf("%s picker title cut at 80 columns:\n%s", tt.name, view)
		}
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
