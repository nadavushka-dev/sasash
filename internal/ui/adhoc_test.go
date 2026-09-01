package ui_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestParseAdhocArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{name: "bare host", input: "192.0.2.99", want: []string{"ssh", "192.0.2.99"}},
		{name: "user at host", input: "root@192.0.2.99", want: []string{"ssh", "root@192.0.2.99"}},
		{name: "host with port", input: "192.0.2.99:2222", want: []string{"ssh", "-p", "2222", "192.0.2.99"}},
		{
			name:  "everything at once",
			input: "root@192.0.2.99:2222 -L 8080:localhost:80",
			want:  []string{"ssh", "-L", "8080:localhost:80", "-p", "2222", "root@192.0.2.99"},
		},
		{
			name:  "attached and repeated forwards",
			input: "h -L8080:localhost:80 -L 5432:127.0.0.1:5432",
			want:  []string{"ssh", "-L", "8080:localhost:80", "-L", "5432:127.0.0.1:5432", "h"},
		},
		{name: "bracketed ipv6 with port", input: "[::1]:2222", want: []string{"ssh", "-p", "2222", "::1"}},
		{name: "bare ipv6", input: "::1", want: []string{"ssh", "::1"}},
		{name: "empty input", input: "   ", wantErr: true},
		{name: "empty user", input: "@192.0.2.99", wantErr: true},
		{name: "user with no host", input: "root@", wantErr: true},
		{name: "empty port", input: "192.0.2.99:", wantErr: true},
		{name: "non-numeric port", input: "192.0.2.99:ssh", wantErr: true},
		{name: "port with no host", input: ":2222", wantErr: true},
		{name: "dangling -L", input: "h -L", wantErr: true},
		{name: "forward without colons", input: "h -L 8080", wantErr: true},
		{name: "unknown token", input: "h bogus", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ui.ParseAdhocArgv(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAdhocArgv(%q) err = %v, want err %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseAdhocArgv(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestModel_Update_AdhocPromptFlow(t *testing.T) {
	t.Parallel()
	runner := fakerun.New()
	m := probedModel(t, runner)

	m = press(t, m, "a")
	if !ui.Prompting(m) {
		t.Fatalf("Prompting() = false after a, want true")
	}
	if view := m.View(); !strings.Contains(view, "connect:") {
		t.Errorf("View() missing the connect prompt:\n%s", view)
	}

	// Action keys are typed text while the prompt is focused.
	m2, cmd := m.Update(keyMsg("q"))
	m = m2.(ui.Model)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Errorf("Update(q) while prompting returned tea.Quit")
		}
	}
	m = press(t, m, "y")
	if calls := runner.Calls(); len(calls) != 0 {
		t.Errorf("Calls() after y while prompting = %v, want none", calls)
	}

	// Clear the stray text, then type a real target and connect.
	m = press(t, m, "esc", "a")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("192.0.2.99")})
	m3, cmd := m.Update(keyMsg("enter"))
	m = m3.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(enter) with a valid target cmd = nil, want handoff cmd")
	}
	if ui.Prompting(m) {
		t.Errorf("Prompting() = true after connect, want false")
	}
}

func TestModel_Update_AdhocPromptRejectsMalformed(t *testing.T) {
	t.Parallel()
	m := probedModel(t, fakerun.New())
	m = press(t, m, "a")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h bogus")})
	m2, cmd := m.Update(keyMsg("enter"))
	m = m2.(ui.Model)
	if cmd != nil {
		t.Errorf("Update(enter) with malformed input cmd = %v, want nil (never a broken exec)", cmd)
	}
	if !ui.Prompting(m) {
		t.Errorf("Prompting() = false, want the prompt kept for correction")
	}
	if view := m.View(); !strings.Contains(view, "only -L") {
		t.Errorf("View() missing the rejection message:\n%s", view)
	}
	m = press(t, m, "esc")
	if ui.Prompting(m) {
		t.Errorf("Prompting() = true after esc, want cancelled")
	}
}

// connectPending drives the model to the state right after an ad-hoc
// connect returns: target parsed and pending, alias prompt focused.
func connectPending(t *testing.T, m ui.Model, target string) ui.Model {
	t.Helper()
	m = press(t, m, "a")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(target)})
	next, _ := m.Update(keyMsg("enter")) // handoff cmd deliberately not run
	m = next.(ui.Model)
	m = update(t, m, ui.AdhocExit(nil))
	return m
}

func TestModel_Update_AdhocSaveFlow(t *testing.T) {
	t.Parallel()
	var saved []ui.AdhocEntry
	m := probedModel(t, fakerun.New())
	m = ui.WithSaveHost(m, func(e ui.AdhocEntry) error {
		saved = append(saved, e)
		return nil
	})
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, error) {
		return []hostmodel.Host{
			{Alias: "edge", Paths: []probe.Path{{Host: "192.0.2.99", Port: "2222"}}},
		}, nil
	})

	m = connectPending(t, m, "root@192.0.2.99:2222 -L 8080:localhost:80")
	if !ui.PromptingSave(m) {
		t.Fatalf("PromptingSave() = false after ad-hoc exit, want the alias prompt")
	}
	if view := m.View(); !strings.Contains(view, "save as:") {
		t.Errorf("View() missing the save prompt:\n%s", view)
	}

	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(enter) with an alias cmd = nil, want save cmd")
	}
	m = update(t, m, cmd()) // runs the save hook, delivers adhocSavedMsg

	want := []ui.AdhocEntry{{
		Alias:    "edge",
		User:     "root",
		Host:     "192.0.2.99",
		Port:     "2222",
		Forwards: []string{"8080:localhost:80"},
	}}
	if !reflect.DeepEqual(saved, want) {
		t.Errorf("saved entries = %+v, want %+v", saved, want)
	}
	if view := m.View(); !strings.Contains(view, "saved edge") {
		t.Errorf("View() missing the saved notice:\n%s", view)
	}
}

func TestModel_Update_AdhocSaveReloadShowsEntry(t *testing.T) {
	t.Parallel()
	m := probedModel(t, fakerun.New())
	m = ui.WithSaveHost(m, func(ui.AdhocEntry) error { return nil })
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, error) {
		return []hostmodel.Host{
			{Alias: "web", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
			{Alias: "edge", Paths: []probe.Path{{Host: "192.0.2.99"}}},
		}, nil
	})
	m = connectPending(t, m, "192.0.2.99")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
	next, saveCmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if saveCmd == nil {
		t.Fatalf("Update(enter) cmd = nil, want save cmd")
	}
	next, reloadCmd := m.Update(saveCmd()) // adhocSavedMsg -> reload cmd
	m = next.(ui.Model)
	if reloadCmd == nil {
		t.Fatalf("Update(adhocSavedMsg) cmd = nil, want reload cmd")
	}
	m = update(t, m, reloadCmd())
	if view := m.View(); !strings.Contains(view, "edge") {
		t.Errorf("View() after reload missing the saved entry:\n%s", view)
	}
}

func TestModel_Update_AdhocSaveDecline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		keys []string
	}{
		{name: "esc skips", keys: []string{"esc"}},
		{name: "empty alias skips", keys: []string{"enter"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			called := false
			m := probedModel(t, fakerun.New())
			m = ui.WithSaveHost(m, func(ui.AdhocEntry) error { called = true; return nil })
			m = connectPending(t, m, "192.0.2.99")
			m = press(t, m, tt.keys...)
			if called {
				t.Errorf("SaveHost called on decline, want nothing written")
			}
			if ui.Prompting(m) {
				t.Errorf("Prompting() = true after decline, want closed")
			}
			if view := m.View(); !strings.Contains(view, "not saved") {
				t.Errorf("View() missing the not-saved notice:\n%s", view)
			}
		})
	}
}

func TestModel_Update_AdhocAliasRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		alias string
		want  string
	}{
		{name: "collision with existing host", alias: "web", want: "already exists"},
		{name: "wildcard", alias: "we*b", want: "single plain word"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			called := false
			m := probedModel(t, fakerun.New())
			m = ui.WithSaveHost(m, func(ui.AdhocEntry) error { called = true; return nil })
			m = connectPending(t, m, "192.0.2.99")
			m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.alias)})
			next, cmd := m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if cmd != nil || called {
				t.Errorf("bad alias: cmd = %v, called = %v; want rejection", cmd, called)
			}
			if !ui.PromptingSave(m) {
				t.Errorf("PromptingSave() = false, want the prompt kept")
			}
			if view := m.View(); !strings.Contains(view, tt.want) {
				t.Errorf("View() missing %q:\n%s", tt.want, view)
			}
		})
	}
}

func TestModel_Update_AdhocExitVariants(t *testing.T) {
	t.Parallel()
	// A start failure means nothing ran: no save offer, a notice instead.
	m := probedModel(t, fakerun.New())
	m = ui.WithSaveHost(m, func(ui.AdhocEntry) error { return nil })
	next, cmd := m.Update(ui.AdhocExit(errors.New(`exec: "ssh": not found`)))
	m = next.(ui.Model)
	if cmd == nil {
		t.Errorf("Update(adhocExit) cmd = nil, want re-probe")
	}
	if ui.Prompting(m) {
		t.Errorf("Prompting() = true after start failure, want no save offer")
	}
	if view := m.View(); !strings.Contains(view, "ssh: ") {
		t.Errorf("View() missing the start-failure notice:\n%s", view)
	}

	// Without a SaveHost hook there is no offer either.
	m2 := probedModel(t, fakerun.New())
	next, _ = m2.Update(ui.AdhocExit(nil))
	m2 = next.(ui.Model)
	if ui.Prompting(m2) {
		t.Errorf("Prompting() = true with no SaveHost hook, want none")
	}
}
