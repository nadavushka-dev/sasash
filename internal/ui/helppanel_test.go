package ui_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// tallHelp opens the keys panel at a size where it fits whole.
func tallHelp(t *testing.T) ui.Model {
	t.Helper()
	m := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 120, Height: 80})
	return press(t, m, "?")
}

func TestModel_View_HelpPanelListsEveryBinding(t *testing.T) {
	t.Parallel()
	// Generated, never hand-maintained: every named binding of the
	// KeyMap that carries help text appears with its key and description.
	m := tallHelp(t)
	view := m.View()
	if !ui.HelpOpen(m) || !strings.Contains(view, "? keys — ? q esc closes") {
		t.Fatalf("? should open the keys panel:\n%s", view)
	}
	// A row is the key, padding, then the description on ONE line: a
	// key and a description found apart do not count.
	row := func(h key.Help) bool {
		for _, l := range strings.Split(view, "\n") {
			if rest, ok := strings.CutPrefix(l, "   "+h.Key+" "); ok && strings.Contains(rest, " "+h.Desc) {
				return true
			}
		}
		return false
	}
	km := reflect.ValueOf(ui.DefaultKeyMap())
	listed := 0
	for i := 0; i < km.NumField(); i++ {
		b, ok := km.Field(i).Interface().(key.Binding)
		if !ok || b.Help().Key == "" {
			continue // ctrl+c carries no help text on purpose
		}
		listed++
		if h := b.Help(); !row(h) {
			t.Errorf("panel missing %s as a row: %q %q", km.Type().Field(i).Name, h.Key, h.Desc)
		}
	}
	if listed < 25 {
		t.Errorf("only %d bindings carry help text; the reflection walk found too few", listed)
	}
	// The context keymaps too: every hint a surface's footer can show is
	// a row here (the prompts' verbs are unioned into one row).
	for _, km := range ui.ContextKeymaps() {
		for _, b := range km.ShortHelp() {
			h := b.Help()
			if h.Key == "enter" || h.Key == "esc" {
				if !strings.Contains(view, h.Desc) {
					t.Errorf("panel missing the prompt verb %q", h.Desc)
				}
				continue
			}
			if !row(h) {
				t.Errorf("panel missing the context row %q %q", h.Key, h.Desc)
			}
		}
	}
	// Grouped by surface, in the base order when nothing holds the keys.
	if !order(view, "   host list", "   paths (p)", "   tunnels (l)", "   prompts", "   settings (o)", "   popups") {
		t.Errorf("panel groups out of order:\n%s", view)
	}
	// The prompts row is honest about enter meaning different things.
	if !strings.Contains(view, "connect / save / add / next / declare") || !strings.Contains(view, "cancel / skip") || !strings.Contains(view, "the prompt's own line says which") {
		t.Errorf("panel prompts row should union the verbs:\n%s", view)
	}
	// Cells, not bytes: the arrow rows' descriptions start in the same
	// column as everyone else's.
	col := -1
	for _, l := range strings.Split(view, "\n") {
		for _, k := range []string{"↑/k ", "ctrl+d ", "g "} {
			if strings.HasPrefix(l, "   "+k) {
				rest := strings.TrimLeft(l[3+len(k):], " ")
				c := lipgloss.Width(l[:len(l)-len(rest)])
				if col >= 0 && c != col {
					t.Errorf("description column drifts at %q: %d vs %d", k, c, col)
				}
				col = c
			}
		}
	}
}

func TestModel_Update_HelpPanelKeys(t *testing.T) {
	t.Parallel()
	m := tallHelp(t)
	// ?, q and esc close it; enter does too (a popup); action keys are
	// inert — the panel never lists a key that would not work under it
	// as anything but "close".
	for _, k := range []string{"?", "q", "esc", "enter"} {
		if c := press(t, m, k); ui.HelpOpen(c) || ui.PopupOpen(c) {
			t.Errorf("%s should close the panel", k)
		}
	}
	for _, k := range []string{"S", "o", "/", "a", "+", "l", "p", "t", "v"} {
		next, cmd := m.Update(keyMsg(k))
		if cmd != nil || !ui.HelpOpen(next.(ui.Model)) {
			t.Errorf("key %q under the panel: cmd %v, open %v", k, cmd != nil, ui.HelpOpen(next.(ui.Model)))
		}
	}
	// It scrolls when the frame is short.
	short := press(t, update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 100, Height: 20}), "?")
	view := short.View()
	if !strings.Contains(view, "j/k scroll") || strings.Contains(view, "popups") {
		t.Errorf("View() short frame should scroll the panel:\n%s", view)
	}
	if view := press(t, short, "G").View(); !strings.Contains(view, "popups") {
		t.Errorf("View() after G should reach the last group:\n%s", view)
	}
	// An attention popup replaces it; ? under an attention popup is inert.
	m2, _ := failedForward(t)
	if c := press(t, m2, "?"); !ui.PopupOpen(c) || ui.HelpOpen(c) {
		t.Errorf("? under an attention popup should be inert")
	}
}

func TestModel_View_HelpPanelContextGroupFirst(t *testing.T) {
	t.Parallel()
	group := func(m ui.Model) string {
		lines := strings.Split(m.View(), "\n")
		return strings.TrimSpace(lines[4])
	}
	// Tunnels focus: the tunnels group leads.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := update(t, polled(t, r, fakedial.New()), tea.WindowSizeMsg{Width: 100, Height: 60})
	if g := group(press(t, m, "l", "?")); !strings.HasPrefix(g, "tunnels (l)") {
		t.Errorf("under tunnels focus the first group = %q, want tunnels", g)
	}
	// Paths focus: the paths group leads, and closing the panel keeps
	// the focus.
	pm, _ := pickerModel(t, fakerun.New())
	pm = update(t, pm, tea.WindowSizeMsg{Width: 100, Height: 60})
	pm = press(t, pm, "p", "?")
	if g := group(pm); !strings.HasPrefix(g, "paths (p)") {
		t.Errorf("under paths focus the first group = %q, want paths", g)
	}
	if c := press(t, pm, "esc"); ui.HelpOpen(c) || !ui.PathsFocused(c) {
		t.Errorf("esc should close the panel and keep the paths focus: help %v, focused %v", ui.HelpOpen(c), ui.PathsFocused(c))
	}
	// Nothing holding the keys: the host list leads.
	if g := group(press(t, m, "?")); !strings.HasPrefix(g, "host list") {
		t.Errorf("first group = %q, want host list", g)
	}
}

func TestModel_View_FooterIsAWhisper(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	last := func(m ui.Model) string {
		lines := strings.Split(m.View(), "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	// The main view: exactly the whisper — the product string.
	if got := last(m); got != "? keys · q quit" {
		t.Errorf("footer = %q, want the whisper", got)
	}
	// The command-preview line above it is untouched.
	if !strings.Contains(m.View(), "$ ssh ubuntu") {
		t.Errorf("View() should keep the command line:\n%s", m.View())
	}
	// Owners and pane focuses keep their one-line context hints.
	tests := []struct {
		name string
		keys []string
		want string
	}{
		{name: "tunnels focus", keys: []string{"l"}, want: "esc/h host list · j/k move"}, // the way out leads, so an 80-col cut keeps it
		{name: "filter", keys: []string{"/"}, want: "apply filter"},
		{name: "prompt", keys: []string{"a"}, want: "connect · esc cancel"}, // the same separator as the whisper
		{name: "settings", keys: []string{"o"}, want: "back / close"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := last(press(t, m, tt.keys...)); !strings.Contains(got, tt.want) || strings.Contains(got, "yank") {
				t.Errorf("footer under %s = %q, want the context hint %q and no action clutter", tt.name, got, tt.want)
			}
		})
	}
}
