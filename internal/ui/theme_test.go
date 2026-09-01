package ui_test

import (
	"errors"
	"regexp"
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

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

var errTest = errors.New("disk full")

func TestThemes_Palettes(t *testing.T) {
	t.Parallel()
	if len(ui.Themes) != 4 {
		t.Fatalf("len(Themes) = %v, want 4", len(ui.Themes))
	}
	if ui.Themes[0].Name != "gruvbox" {
		t.Errorf("Themes[0].Name = %v, want gruvbox (the default)", ui.Themes[0].Name)
	}
	seenBg := make(map[string]string)
	for _, th := range ui.Themes {
		p := th.Palette
		tokens := map[string]lipgloss.Color{
			"Bg": p.Bg, "BgSubtle": p.BgSubtle, "Fg": p.Fg, "FgDim": p.FgDim,
			"Accent": p.Accent, "Accent2": p.Accent2, "Success": p.Success,
			"Warning": p.Warning, "Error": p.Error, "Selection": p.Selection,
		}
		for name, c := range tokens {
			if !hexColor.MatchString(string(c)) {
				t.Errorf("theme %s: token %s = %q, want #rrggbb", th.Name, name, c)
			}
		}
		if prev, dup := seenBg[string(p.Bg)]; dup {
			t.Errorf("themes %s and %s share Bg %q", prev, th.Name, p.Bg)
		}
		seenBg[string(p.Bg)] = th.Name
	}
}

// TestThemes_CanonicalTokens pins name -> palette wiring with exact
// upstream values: the Ascii test profile renders all themes identically,
// so a palette swapped behind the wrong name is invisible to View tests.
func TestThemes_CanonicalTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		accent string
		bg     string
	}{
		{name: "gruvbox", accent: "#fe8019", bg: "#282828"},
		{name: "mocha", accent: "#cba6f7", bg: "#1e1e2e"},
		{name: "tokyonight", accent: "#7aa2f7", bg: "#1a1b26"},
		{name: "nord", accent: "#88c0d0", bg: "#2e3440"},
	}
	if len(ui.Themes) != len(tests) {
		t.Fatalf("len(Themes) = %v, want %v", len(ui.Themes), len(tests))
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := ui.Themes[i]
			if th.Name != tt.name {
				t.Fatalf("Themes[%d].Name = %v, want %v", i, th.Name, tt.name)
			}
			if got := string(th.Palette.Accent); got != tt.accent {
				t.Errorf("%s Accent = %v, want %v", tt.name, got, tt.accent)
			}
			if got := string(th.Palette.Bg); got != tt.bg {
				t.Errorf("%s Bg = %v, want %v", tt.name, got, tt.bg)
			}
		})
	}
}

// TestModel_ApplyTheme_StylesFollowPalette pins the switch wiring at the
// style layer: the model's own styles and the help component must carry
// the selected palette after New and after every `t`.
//
// Known residual: the list delegate holds its own styles copy and bubbles
// exposes no delegate getter, so the SetDelegate line in applyTheme is
// pinned only by review.
func TestModel_ApplyTheme_StylesFollowPalette(t *testing.T) {
	t.Parallel()

	m := ui.New(nil, probe.Prober{Dialer: fakedial.New()}, fakerun.New(), ui.Config{Theme: "nord"})
	if got := ui.StylesOf(m).Title.GetForeground(); got != ui.Nord.Accent {
		t.Errorf("Title foreground with Config{nord} = %v, want %v", got, ui.Nord.Accent)
	}

	m = taggedModel(t, fakerun.New()) // default: gruvbox
	if got := ui.StylesOf(m).Title.GetForeground(); got != ui.Gruvbox.Accent {
		t.Fatalf("Title foreground at start = %v, want gruvbox %v", got, ui.Gruvbox.Accent)
	}
	m = press(t, m, "t") // -> mocha
	if got := ui.StylesOf(m).Title.GetForeground(); got != ui.Mocha.Accent {
		t.Errorf("Title foreground after t = %v, want mocha %v", got, ui.Mocha.Accent)
	}
	if got := ui.StylesOf(m).StatusUp.GetForeground(); got != ui.Mocha.Success {
		t.Errorf("StatusUp foreground after t = %v, want mocha %v", got, ui.Mocha.Success)
	}
	if got := ui.HelpStyles(m).ShortKey.GetForeground(); got != ui.Mocha.FgDim {
		t.Errorf("help ShortKey foreground after t = %v, want mocha %v", got, ui.Mocha.FgDim)
	}
	if got := ui.HelpStyles(m).FullDesc.GetForeground(); got != ui.Mocha.FgDim {
		t.Errorf("help FullDesc foreground after t = %v, want mocha %v", got, ui.Mocha.FgDim)
	}
}

func TestThemeIndex_Lookup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		arg    string
		want   int
		wantOk bool
	}{
		{name: "default", arg: "gruvbox", want: 0, wantOk: true},
		{name: "case insensitive", arg: "Nord", want: 3, wantOk: true},
		{name: "unknown", arg: "solarized", want: 0, wantOk: false},
		{name: "empty", arg: "", want: 0, wantOk: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ui.ThemeIndex(tt.arg)
			if got != tt.want || ok != tt.wantOk {
				t.Errorf("ThemeIndex(%q) = %v, %v, want %v, %v", tt.arg, got, ok, tt.want, tt.wantOk)
			}
		})
	}
}

func TestModel_Update_ThemeCycle(t *testing.T) {
	t.Parallel()
	var saved []string
	m := taggedModel(t, fakerun.New())
	m = ui.WithSaveTheme(m, func(name string) error {
		saved = append(saved, name)
		return nil
	})

	want := []string{"mocha", "tokyonight", "nord", "gruvbox"}
	for _, name := range want {
		next, cmd := m.Update(keyMsg("t"))
		m = next.(ui.Model)
		if got := ui.ThemeName(m); got != name {
			t.Fatalf("ThemeName() after t = %v, want %v", got, name)
		}
		if view := m.View(); !strings.Contains(view, "theme: "+name) {
			t.Errorf("View() missing the theme notice for %v", name)
		}
		if cmd == nil {
			t.Fatalf("Update(t) cmd = nil, want persistence cmd")
		}
		m = update(t, m, cmd()) // runs the save, delivers themeSavedMsg
	}
	if got := strings.Join(saved, ","); got != strings.Join(want, ",") {
		t.Errorf("saved themes = %v, want %v", saved, want)
	}
}

func TestModel_Update_ThemeCycle_NoPersistence(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New()) // Config zero value: SaveTheme nil
	next, cmd := m.Update(keyMsg("t"))
	m = next.(ui.Model)
	if got := ui.ThemeName(m); got != "mocha" {
		t.Errorf("ThemeName() after t = %v, want mocha", got)
	}
	if cmd != nil {
		t.Errorf("Update(t) cmd = %v, want nil without a SaveTheme hook", cmd)
	}
}

func TestModel_Update_ThemeSaveFailureNotice(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New())
	m = ui.WithSaveTheme(m, func(string) error { return errTest })
	next, cmd := m.Update(keyMsg("t"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(t) cmd = nil, want persistence cmd")
	}
	m = update(t, m, cmd())
	if view := m.View(); !strings.Contains(view, "theme not saved") {
		t.Errorf("View() missing the not-saved notice:\n%s", view)
	}
}

// TestModel_View_EveryThemeEveryState renders each shipped theme through
// each UI state and asserts the frame stays whole and legible (structure
// carries the state; color is additive, per NO_COLOR requirements).
func TestModel_View_EveryThemeEveryState(t *testing.T) {
	t.Parallel()
	states := []struct {
		name string
		keys []string
		want string
	}{
		{name: "list with statuses", keys: nil, want: "20ms"},
		{name: "filtering", keys: []string{"/", "a"}, want: "apply filter"},
		{name: "filter applied", keys: []string{"/", "a", "l", "p", "enter"}, want: "/alp"},
		{name: "no match", keys: []string{"/", "z", "z"}, want: "no hosts match"},
		{name: "full help", keys: []string{"?"}, want: "clear filter / back"},
		{name: "notice", keys: []string{"t"}, want: "theme:"},
	}
	for _, th := range ui.Themes {
		for _, st := range states {
			t.Run(th.Name+"/"+st.name, func(t *testing.T) {
				t.Parallel()
				hosts := []hostmodel.Host{
					{Alias: "alpha", Tags: []string{"dev"}, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
					{Alias: "bravo", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.20"}}},
				}
				dialer := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond).Down("192.0.2.20:22")
				m := ui.New(hosts, probe.Prober{Dialer: dialer, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Theme: th.Name})
				if got := ui.ThemeName(m); got != th.Name {
					t.Fatalf("ThemeName() = %v, want %v", got, th.Name)
				}
				m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
				m = update(t, m, m.Init()())
				m = press(t, m, st.keys...)
				view := m.View()
				if !strings.Contains(view, st.want) {
					t.Errorf("View() missing %q:\n%s", st.want, view)
				}
				lines := strings.Split(view, "\n")
				if len(lines) > 24 {
					t.Errorf("View() has %d lines, want <= 24", len(lines))
				}
				for i, line := range lines {
					if w := lipgloss.Width(line); w > 80 {
						t.Errorf("View() line %d width = %v, want <= 80", i, w)
					}
				}
			})
		}
	}
}
