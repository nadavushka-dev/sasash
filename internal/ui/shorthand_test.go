package ui_test

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestExpandSpec_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		want     string
		expanded bool
	}{
		{name: "port only", in: "3000", want: "3000:localhost:3000", expanded: true},
		{name: "local:port", in: "2000:3000", want: "2000:localhost:3000", expanded: true},
		{name: "three parts unchanged", in: "3000:localhost:3000", want: "3000:localhost:3000"},
		{name: "three parts with a host unchanged", in: "3000:db.invalid:5432", want: "3000:db.invalid:5432"},
		{name: "bind address kept", in: "127.0.0.1:3000:localhost:3000", want: "127.0.0.1:3000:localhost:3000"},
		{name: "surrounding space trimmed", in: "  3000 ", want: "3000:localhost:3000", expanded: true},
		// Not shorthands: through as typed, no hostname guessed, the
		// surface's own gate decides.
		{name: "two fields, second not all digits", in: "3000:web", want: "3000:web"},
		{name: "two fields, first not all digits", in: "web:3000", want: "web:3000"},
		{name: "remote unix socket target", in: "3000:/var/run/docker.sock", want: "3000:/var/run/docker.sock"},
		{name: "one field not all digits", in: "nonsense", want: "nonsense"},
		{name: "empty", in: "", want: ""},
		{name: "empty fields are not digits", in: "3000::", want: "3000::"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, expanded := ui.ExpandSpec(tt.in)
			if got != tt.want || expanded != tt.expanded {
				t.Errorf("ExpandSpec(%q) = %q, %v; want %q, %v", tt.in, got, expanded, tt.want, tt.expanded)
			}
		})
	}
}

func TestModel_Update_ShorthandExpandsEverywhere(t *testing.T) {
	t.Parallel()
	// Every surface hands canonical specs downstream, and the prompt
	// line shows the expansion beside the typed shorthand.
	t.Run("+ prompt", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New()
		sp := fakespawn.New().Dial(d)
		m := polledSpawn(t, r, sp, d, t.TempDir())
		m = press(t, m, "+")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!8080")})
		if view := m.View(); !strings.Contains(view, "→ 8080:localhost:8080") {
			t.Errorf("View() missing the expansion beside the field:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		if got := sp.Spawned(); !reflect.DeepEqual(got, []string{spawnArgv("ubuntu", "8080:localhost:8080")}) {
			t.Errorf("spawned = %v", got)
		}
		if view := m.View(); !strings.Contains(view, "session-only tunnel up: 8080:localhost:8080 on ubuntu") {
			t.Errorf("View() outcome:\n%s", view)
		}
	})
	t.Run("E prompt", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		m, _ := editModel(t, fakerun.New(), &calls, nil)
		m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}) // the plan line carries the whole spawn argv
		m = press(t, m, "l", "E")
		for range specWeb0 {
			m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
		}
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3000:9000")})
		if view := m.View(); !strings.Contains(view, "→ 3000:localhost:9000") || !strings.Contains(view, spawnArgv("ubuntu", "3000:localhost:9000")) {
			t.Errorf("View() plan and expansion should show the canonical spec:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		update(t, next.(ui.Model), cmd())
		if want := [][]string{{"ubuntu", "web", specWeb0, "3000:localhost:9000"}}; !reflect.DeepEqual(calls, want) {
			t.Errorf("hook calls = %v, want %v", calls, want)
		}
	})
	t.Run("settings add-tunnel", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		m := ui.WithSaveForwardSet(settingsModel(t, fakerun.New()), func(alias, name string, specs []string) error {
			calls = append(calls, append([]string{alias, name}, specs...))
			return nil
		})
		m = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 24}) // the expanded list is long
		m = press(t, m, "G", "enter", "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("db")})
		m = press(t, m, "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5432 5433:5434 6000:localhost:6000")})
		if view := m.View(); !strings.Contains(view, "→ 5432:localhost:5432 5433:localhost:5434 6000:localhost:6000") {
			t.Errorf("View() missing the expansion of the spec list:\n%s", view)
		}
		next, save := m.Update(keyMsg("enter"))
		update(t, next.(ui.Model), save())
		if want := [][]string{{"web", "db", "5432:localhost:5432", "5433:localhost:5434", "6000:localhost:6000"}}; !reflect.DeepEqual(calls, want) {
			t.Errorf("hook calls = %v, want %v", calls, want)
		}
	})
	t.Run("-L in a", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(probedModel(t, fakerun.New()))
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 8080 root@192.0.2.99")})
		if view := m.View(); !strings.Contains(view, "→ ssh -L 8080:localhost:8080 root@192.0.2.99") {
			t.Errorf("View() missing the expanded command:\n%s", view)
		}
		// Already canonical: nothing to add beside the field.
		m = press(t, m, "esc", "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 8080:localhost:80 h")})
		if view := m.View(); strings.Contains(view, "→ ssh") {
			t.Errorf("View() shows an expansion for a canonical spec:\n%s", view)
		}
	})
	t.Run("not a shorthand: through as typed, the a rule still refuses", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(probedModel(t, fakerun.New()))
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 3000:web h")})
		if view := m.View(); strings.Contains(view, "→ ") {
			t.Errorf("View() offers an expansion for a non-shorthand:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd != nil || !ui.Prompting(m) || !strings.Contains(m.View(), `forward "3000:web": want local:host:port`) {
			t.Errorf("enter on 3000:web: cmd %v, prompting %v:\n%s", cmd != nil, ui.Prompting(m), m.View())
		}
	})
	t.Run("ssh-valid non-shorthand still reaches the engine from +", func(t *testing.T) {
		t.Parallel()
		// The engine's local-port rule is the + gate, as before 4.9: a
		// remote socket target is not a shorthand and is not refused.
		const sock = "3000:/var/run/docker.sock"
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New()
		sp := fakespawn.New().Dial(d)
		m := polledSpawn(t, r, sp, d, t.TempDir())
		m = press(t, m, "+")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!" + sock)})
		if view := m.View(); strings.Contains(view, "→ ") {
			t.Errorf("View() offers an expansion for a canonical spec:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		update(t, next.(ui.Model), cmd())
		if got := sp.Spawned(); !reflect.DeepEqual(got, []string{spawnArgv("ubuntu", sock)}) {
			t.Errorf("spawned = %v", got)
		}
	})
	t.Run("expansion too wide is cut with a visible ellipsis", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		m := ui.WithSaveForwardSet(settingsModel(t, fakerun.New()), func(alias, name string, specs []string) error {
			calls = append(calls, append([]string{alias, name}, specs...))
			return nil
		})
		m = press(t, m, "G", "enter", "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("db")})
		m = press(t, m, "enter")
		specs := "5432 5433:5434 6000:localhost:6000 7000 8000 9000"
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(specs)})
		full := "5432:localhost:5432 5433:localhost:5434 6000:localhost:6000 7000:localhost:7000 8000:localhost:8000 9000:localhost:9000"
		view := m.View()
		line := ""
		for _, l := range strings.Split(view, "\n") {
			if strings.Contains(l, "→ ") {
				line = l
			}
		}
		if line == "" || !strings.HasSuffix(line, "…") || !strings.HasPrefix(line, " → 5432:localhost:5432 5433:localhost:5434") {
			t.Errorf("View() at 80 cols should show the expansion cut with …, got %q in:\n%s", line, view)
		}
		if strings.Contains(view, full) {
			t.Errorf("View() at 80 cols cannot show the whole expansion:\n%s", view)
		}
		// The extra line never pushes the frame past the window — the
		// body gives it the row.
		if h := strings.Count(view, "\n") + 1; h != 24 {
			t.Errorf("View() is %d lines with the expansion shown, want 24", h)
		}
		for _, l := range strings.Split(view, "\n") {
			if n := len([]rune(l)); n > 80 {
				t.Errorf("View() line %d runes wide at 80 cols: %q", n, l)
			}
		}
		// Enter still uses every expanded spec.
		next, save := m.Update(keyMsg("enter"))
		update(t, next.(ui.Model), save())
		if want := [][]string{append([]string{"web", "db"}, strings.Fields(full)...)}; !reflect.DeepEqual(calls, want) {
			t.Errorf("hook calls = %v, want %v", calls, want)
		}
	})
	t.Run("settings specs preview promises nothing enter refuses", func(t *testing.T) {
		t.Parallel()
		m := ui.WithSaveForwardSet(settingsModel(t, fakerun.New()), func(string, string, []string) error { return nil })
		m = press(t, m, "G", "enter", "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("db")})
		m = press(t, m, "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5432 nonsense")})
		if view := m.View(); !strings.Contains(view, "specs:") || strings.Contains(view, "→ ") {
			t.Errorf("View() should be the specs prompt with no preview of a list enter will refuse:\n%s", view)
		}
	})
	t.Run("tiny terminal keeps the frame instead of the line", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		m := polled(t, r, fakedial.New())
		m = press(t, m, "+")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!8080")})
		for _, h := range []int{6, 5, 4} {
			v := update(t, m, tea.WindowSizeMsg{Width: 80, Height: h}).View()
			if got := strings.Count(v, "\n") + 1; got > max(h, 5) {
				t.Errorf("View() at height %d is %d lines, want at most %d", h, got, max(h, 5))
			}
			if h == 6 && !strings.Contains(v, "→ 8080:localhost:8080") {
				t.Errorf("View() at height 6 should still fit the expansion:\n%s", v)
			}
		}
	})
	t.Run("settings duplicate revealed by expansion is refused", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		m := ui.WithSaveForwardSet(settingsModel(t, fakerun.New()), func(alias, name string, specs []string) error {
			calls = append(calls, append([]string{alias, name}, specs...))
			return nil
		})
		m = press(t, m, "G", "enter", "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("db")})
		m = press(t, m, "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5432 5432:localhost:5432")})
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd != nil || !ui.Prompting(m) || len(calls) != 0 || !strings.Contains(m.View(), `spec "5432:localhost:5432" repeated`) {
			t.Errorf("enter on a duplicate: cmd %v, prompting %v, calls %v:\n%s", cmd != nil, ui.Prompting(m), calls, m.View())
		}
	})
	t.Run("settings add-connection previews the expanded command", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(settingsModel(t, fakerun.New()))
		m = press(t, m, "G", "k", "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("root@192.0.2.99 -L 8080")})
		if view := m.View(); !strings.Contains(view, "→ ssh -L 8080:localhost:8080 root@192.0.2.99") {
			t.Errorf("View() missing the expanded command:\n%s", view)
		}
		// -N is refused on enter there, so no command is promised.
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" -N")})
		if view := m.View(); strings.Contains(view, "→ ssh") {
			t.Errorf("View() previews a command the prompt will refuse:\n%s", view)
		}
	})
	t.Run("a previews per token, not by substring", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(probedModel(t, fakerun.New()))
		m = press(t, m, "a")
		// The shorthand's expansion is also typed literally: a substring
		// test would find it and hide that -L 8080 was expanded.
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 8080 -L 8080:localhost:8080 h")})
		if view := m.View(); !strings.Contains(view, "→ ssh -L 8080:localhost:8080 -L 8080:localhost:8080 h") {
			t.Errorf("View() missing the expanded command:\n%s", view)
		}
		// Glued form too.
		m = press(t, m, "esc", "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L9000:9001 h -N")})
		if view := m.View(); !strings.Contains(view, "→ ssh -fN -o BatchMode=yes -L 9000:localhost:9001 h") {
			t.Errorf("View() missing the expanded tunnel-only command:\n%s", view)
		}
	})
}
