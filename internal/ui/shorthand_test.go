package ui_test

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestExpandSpec_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "port only", in: "3000", want: "3000:localhost:3000"},
		{name: "local:port", in: "2000:3000", want: "2000:localhost:3000"},
		{name: "three parts unchanged", in: "3000:localhost:3000", want: "3000:localhost:3000"},
		{name: "three parts with a host unchanged", in: "3000:db.invalid:5432", want: "3000:db.invalid:5432"},
		{name: "bind address kept", in: "127.0.0.1:3000:localhost:3000", want: "127.0.0.1:3000:localhost:3000"},
		{name: "surrounding space trimmed", in: "  3000 ", want: "3000:localhost:3000"},
		{name: "two fields, second not all digits: no guessing", in: "3000:web", wantErr: true},
		{name: "two fields, first not all digits", in: "web:3000", wantErr: true},
		{name: "one field not all digits", in: "nonsense", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ui.ExpandSpec(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("ExpandSpec(%q) = %q, %v; want %q, err %v", tt.in, got, err, tt.want, tt.wantErr)
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
		r := fakerun.New().
			StubExit(0, "ssh", "-O", "check", "ubuntu").
			StubExit(0, "ssh", "-O", "forward", "-L", "8080:localhost:8080", "ubuntu")
		m := polled(t, r, fakedial.New())
		m = press(t, m, "+")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("8080")})
		if view := m.View(); !strings.Contains(view, "→ 8080:localhost:8080") {
			t.Errorf("View() missing the expansion beside the field:\n%s", view)
		}
		before := len(r.CallKeys())
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"ssh -O check ubuntu", "ssh -O forward -L 8080:localhost:8080 ubuntu"}) {
			t.Errorf("calls = %v", got)
		}
		if view := m.View(); !strings.Contains(view, "added 8080:localhost:8080 on ubuntu") {
			t.Errorf("View() outcome:\n%s", view)
		}
	})
	t.Run("E prompt", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		r := fakerun.New().
			StubExit(0, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu").
			StubExit(0, "ssh", "-O", "forward", "-L", "3000:localhost:9000", "ubuntu")
		m := editModel(t, r, &calls, nil)
		m = press(t, m, "l", "E")
		for range specWeb0 {
			m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
		}
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3000:9000")})
		if view := m.View(); !strings.Contains(view, "→ 3000:localhost:9000") || !strings.Contains(view, "ssh -O forward -L 3000:localhost:9000 ubuntu") {
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
	t.Run("disambiguation stays an error", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		m := polled(t, r, fakedial.New())
		m = press(t, m, "+")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3000:web")})
		if view := m.View(); strings.Contains(view, "→ ") {
			t.Errorf("View() offers an expansion for an ambiguous input:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd != nil || !ui.Prompting(m) || !strings.Contains(m.View(), `spec "3000:web": want local:host:port, port, or local:port`) {
			t.Errorf("enter on 3000:web: cmd %v, prompting %v:\n%s", cmd != nil, ui.Prompting(m), m.View())
		}
	})
}
