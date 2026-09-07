package ui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The settings menu is a centered popup with a vim-style / search, and
// the keys panel searches the same way (SASASH.4.14).

// wide is a probed model on a 100x24 frame.
func wide(t *testing.T) ui.Model {
	t.Helper()
	return update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 100, Height: 24})
}

// boxMargins measures a view's box: the columns left and right of it
// on its top border row, and the body rows above and below it.
func boxMargins(t *testing.T, view string, width int) (left, right, above, below int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	top, bottom := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "╭") {
			top = i
		}
		if strings.Contains(l, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatalf("View() holds no whole box (top %d, bottom %d):\n%s", top, bottom, view)
	}
	left = strings.Index(lines[top], "╭")
	right = width - lipgloss.Width(strings.TrimRight(lines[top], " "))
	// The header is two lines, the footer two.
	return left, right, top - 2, len(lines) - 2 - bottom - 1
}

func TestModel_View_SettingsIsACenteredBox(t *testing.T) {
	t.Parallel()
	m := press(t, wide(t), "o")
	view := m.View()
	if n := len(strings.Split(view, "\n")); n != 24 {
		t.Fatalf("View() is %d lines, want the frame's 24:\n%s", n, view)
	}
	// Centered both ways: as wide as its rows, not the frame, with the
	// same room on either side and above as below (within rounding).
	left, right, above, below := boxMargins(t, view, 100)
	if left < 20 || right < 20 || left-right > 1 || right-left > 1 {
		t.Errorf("box not centered horizontally: %d left, %d right:\n%s", left, right, view)
	}
	if above < 3 || below < 3 || above-below > 1 || below-above > 1 {
		t.Errorf("box not centered vertically: %d above, %d below:\n%s", above, below, view)
	}
	inner := strings.Join(boxLines(view), "\n")
	for _, want := range []string{" settings", "› create config file", "add new tunnel"} {
		if !strings.Contains(inner, want) {
			t.Errorf("box rows missing %q:\n%s", want, view)
		}
	}
	// The keys panel and the attention popup float in the same box: the
	// panel is taller than the frame here, so it is centered sideways.
	if left, right, _, _ := boxMargins(t, press(t, wide(t), "?").View(), 100); left < 10 || right < 10 || left-right > 1 || right-left > 1 {
		t.Errorf("keys panel not centered horizontally: %d left, %d right", left, right)
	}
	f, _ := failedForward(t)
	if left, right, above, below := boxMargins(t, f.View(), 100); left < 1 || right < 1 || left-right > 1 || right-left > 1 || above < 1 || below < 1 {
		t.Errorf("attention popup not centered: %d left, %d right, %d above, %d below:\n%s", left, right, above, below, f.View())
	}
}

func TestModel_View_SettingsBoxFitsATinyFrame(t *testing.T) {
	t.Parallel()
	m := press(t, update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 30, Height: 8}), "o")
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 8 {
		t.Errorf("View() is %d lines, want <= 8", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 30 {
			t.Errorf("View() line %d width = %d, want <= 30", i, w)
		}
	}
	// Both borders and the first entry are on screen: the blank under
	// the title went instead of the box's bottom.
	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") || !strings.Contains(view, "› create config file") {
		t.Errorf("View() at 30x8 should keep the box whole with its first row:\n%s", view)
	}
}

func TestModel_Update_SettingsSearch(t *testing.T) {
	t.Parallel()
	all := []string{"create config file", "edit config file", "theme", "spawn target", "add new connection", "add new tunnel"}
	t.Run("typing narrows and highlights the first match", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "o", "/", "t", "h")
		view := m.View()
		if !ui.SettingsOpen(m) || !strings.Contains(view, "› theme") || strings.Contains(view, "create config file") || !strings.Contains(view, "/th▏") {
			t.Errorf("View() after /th should show only theme, highlighted, with the query:\n%s", view)
		}
	})
	t.Run("the match ignores case", func(t *testing.T) {
		t.Parallel()
		if view := press(t, wide(t), "o", "/", "T", "H").View(); !strings.Contains(view, "› theme") || strings.Contains(view, "no match") {
			t.Errorf("View() after /TH should still find theme:\n%s", view)
		}
	})
	t.Run("enter activates the highlighted match", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "o", "/", "t", "h", "enter")
		if view := m.View(); !ui.SettingsOpen(m) || !strings.Contains(view, "theme — enter applies and persists") || strings.Contains(view, "/th") {
			t.Errorf("View() after /th enter should open the theme picker without the query:\n%s", view)
		}
	})
	t.Run("arrows move among the matches; j is text", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "o", "/", "a", "d", "d")
		if view := m.View(); !strings.Contains(view, "› add new connection") || !strings.Contains(view, "  add new tunnel") || strings.Contains(view, "theme") {
			t.Fatalf("View() after /add should list the two matches with the first highlighted:\n%s", view)
		}
		m = press(t, m, "down")
		if view := m.View(); !strings.Contains(view, "› add new tunnel") || strings.Contains(view, "› add new connection") {
			t.Errorf("View() after ↓ should highlight the second match:\n%s", view)
		}
		m = press(t, m, "up", "up")
		if view := m.View(); !strings.Contains(view, "› add new connection") {
			t.Errorf("View() after ↑ ↑ should stop at the first match:\n%s", view)
		}
		m = press(t, m, "j")
		if view := m.View(); !strings.Contains(view, "no match for /addj — esc clears") {
			t.Errorf("View() after j should have typed it:\n%s", view)
		}
		m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
		if view := m.View(); !strings.Contains(view, "› add new connection") || !strings.Contains(view, "/add▏") {
			t.Errorf("View() after backspace should widen back to the two matches:\n%s", view)
		}
		// enter on the highlighted match opens the connection prompt: the
		// popup closes, the prompt owns the keys.
		m = press(t, m, "enter")
		if ui.SettingsOpen(m) || !ui.Prompting(m) || !strings.Contains(m.View(), "new connection:") {
			t.Errorf("enter on add new connection should open its prompt: settings %v, prompting %v:\n%s", ui.SettingsOpen(m), ui.Prompting(m), m.View())
		}
	})
	t.Run("esc clears back to the full menu, highlight kept; a second esc closes", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "o", "/", "t", "h", "esc")
		view := m.View()
		for _, want := range all {
			if !strings.Contains(view, want) {
				t.Errorf("View() after esc missing %q:\n%s", want, view)
			}
		}
		if !ui.SettingsOpen(m) || !strings.Contains(view, "› theme") || strings.Contains(view, "/th") {
			t.Errorf("View() after esc should keep theme highlighted without the query:\n%s", view)
		}
		if m = press(t, m, "esc"); ui.SettingsOpen(m) {
			t.Errorf("SettingsOpen() = true after the second esc, want closed")
		}
	})
	t.Run("no match: enter is inert, esc recovers", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "o", "/", "z", "z")
		if view := m.View(); !strings.Contains(view, "no match for /zz — esc clears") || strings.Contains(view, "create config file") {
			t.Fatalf("View() after /zz should say no match:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd != nil || !ui.SettingsOpen(m) || !strings.Contains(m.View(), "no match for /zz") {
			t.Errorf("enter with no match should do nothing: cmd %v, open %v:\n%s", cmd != nil, ui.SettingsOpen(m), m.View())
		}
		if view := press(t, m, "esc").View(); !strings.Contains(view, "› create config file") || !strings.Contains(view, "add new tunnel") {
			t.Errorf("View() after esc should show the full menu:\n%s", view)
		}
	})
	t.Run("the search is the menu's: / is inert in a picker", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "o", "j", "j", "enter", "/")
		if view := m.View(); !strings.Contains(view, "theme — enter applies and persists") || strings.Contains(view, "/▏") {
			t.Errorf("View() after / in the theme picker should not start a search:\n%s", view)
		}
	})
	t.Run("the setup footer stays on the bottom line of a short box", func(t *testing.T) {
		t.Parallel()
		short := ui.WithHome(update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 80, Height: 10}), t.TempDir())
		next, check := press(t, short, "o").Update(keyMsg("enter"))
		short = update(t, next.(ui.Model), check())
		view := short.View()
		if !strings.Contains(view, "enter writes") || !strings.Contains(view, "╰") || len(strings.Split(view, "\n")) > 10 {
			t.Errorf("View() at 80x10 should keep the footer and the box's bottom:\n%s", view)
		}
	})
}

func TestModel_Update_HelpPanelSearch(t *testing.T) {
	t.Parallel()
	t.Run("typing narrows the rows under their headings", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "?", "/", "y", "a", "n", "k")
		view := m.View()
		inner := strings.Join(boxLines(view), "\n")
		for _, want := range []string{"host list", "yank host", "yank command", "/yank▏"} {
			if !strings.Contains(inner, want) {
				t.Errorf("box rows missing %q:\n%s", want, view)
			}
		}
		for _, not := range []string{"↑/k", "paths (p)", "tunnels (l)", "popups", "j/k scroll"} {
			if strings.Contains(inner, not) {
				t.Errorf("box rows should not carry %q:\n%s", not, view)
			}
		}
		if !ui.HelpOpen(m) {
			t.Errorf("HelpOpen() = false while searching, want the panel")
		}
	})
	t.Run("a description matches too", func(t *testing.T) {
		t.Parallel()
		inner := strings.Join(boxLines(press(t, wide(t), "?", "/", "page").View()), "\n")
		if !strings.Contains(inner, "½ page down") || !strings.Contains(inner, "½ page up") || strings.Contains(inner, "spawn ssh") {
			t.Errorf("box rows after /page should be the two half pages:\n%s", inner)
		}
	})
	t.Run("enter keeps the rows, esc clears, esc closes", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "?", "/", "yank", "enter")
		inner := strings.Join(boxLines(m.View()), "\n")
		if !ui.HelpOpen(m) || !strings.Contains(inner, "yank host") || strings.Contains(inner, "↑/k") || !strings.Contains(inner, "/yank") || strings.Contains(inner, "▏") {
			t.Errorf("View() after enter should keep the narrowed rows and show the query without a cursor:\n%s", m.View())
		}
		// The keys are back: j scrolls (nothing to scroll here) rather
		// than typing.
		if inner := strings.Join(boxLines(press(t, m, "j").View()), "\n"); strings.Contains(inner, "/yankj") {
			t.Errorf("j after enter should not type:\n%s", inner)
		}
		m = press(t, m, "esc")
		if inner := strings.Join(boxLines(m.View()), "\n"); !ui.HelpOpen(m) || !strings.Contains(inner, "↑/k") || !strings.Contains(inner, "j/k scroll") || strings.Contains(inner, "/yank") {
			t.Errorf("View() after esc should show every row again (scrolling):\n%s", m.View())
		}
		if m = press(t, m, "esc"); ui.HelpOpen(m) {
			t.Errorf("HelpOpen() = true after the second esc, want closed")
		}
	})
	t.Run("? and q are text while typing", func(t *testing.T) {
		t.Parallel()
		m := press(t, wide(t), "?", "/", "q", "?")
		if inner := strings.Join(boxLines(m.View()), "\n"); !ui.HelpOpen(m) || !strings.Contains(inner, "/q?▏") {
			t.Errorf("View() after /q? should still be the panel with the query typed:\n%s", m.View())
		}
	})
	t.Run("no match is said", func(t *testing.T) {
		t.Parallel()
		if inner := strings.Join(boxLines(press(t, wide(t), "?", "/", "zzz").View()), "\n"); !strings.Contains(inner, "no match for /zzz — esc clears") || strings.Contains(inner, "host list") {
			t.Errorf("View() after /zzz should say no match:\n%s", inner)
		}
	})
	t.Run("an attention popup's rows narrow the same way", func(t *testing.T) {
		t.Parallel()
		m, _ := failedForward(t)
		inner := strings.Join(boxLines(press(t, m, "/", "Banner").View()), "\n")
		if !strings.Contains(inner, "Banner line three") || strings.Contains(inner, "kex_exchange_identification") {
			t.Errorf("View() after /Banner should keep only the matching reason line:\n%s", inner)
		}
	})
}
