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

func TestParseAdhocFor_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		selected string
		want     []string
		fromSel  bool
		wantErr  string
	}{
		{name: "spec only targets the selection", input: "-L 8080", selected: "ubuntu", want: []string{"ssh", "-L", "8080:localhost:8080", "ubuntu"}, fromSel: true},
		{name: "flags only: tunnel to the selection", input: "-N -L 1234:localhost:8080", selected: "ubuntu-machine1", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "1234:localhost:8080", "ubuntu-machine1"}, fromSel: true},
		{name: "port flag only", input: "-p 2222", selected: "ubuntu", want: []string{"ssh", "-p", "2222", "ubuntu"}, fromSel: true},
		{name: "bare -N", input: "-N", selected: "ubuntu", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "ubuntu"}, fromSel: true},
		{name: "pasted ssh with flags only", input: "ssh -L 9000:localhost:9000", selected: "web.office", want: []string{"ssh", "-L", "9000:localhost:9000", "web.office"}, fromSel: true},
		{name: "explicit target untouched", input: "-L 8080 root@192.0.2.99", selected: "ubuntu", want: []string{"ssh", "-L", "8080:localhost:8080", "root@192.0.2.99"}},
		{name: "explicit target alone", input: "db", selected: "ubuntu", want: []string{"ssh", "db"}},
		{name: "no selection keeps the no-target error", input: "-L 8080", wantErr: "no target: want [user@]host[:port]"},
		{name: "empty input keeps its usage error", input: "", selected: "ubuntu", wantErr: "type [user@]host[:port]"},
		{name: "incomplete flag keeps its error", input: "-L", selected: "ubuntu", wantErr: "-L needs a value"},
		{name: "malformed spec keeps its error", input: "-L 3000:web", selected: "ubuntu", wantErr: `forward "3000:web": want local:host:port`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, fromSel, err := ui.ParseAdhocFor(tt.input, tt.selected)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ParseAdhocFor(%q, %q) err = %v, want %q", tt.input, tt.selected, err, tt.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) || fromSel != tt.fromSel {
				t.Errorf("ParseAdhocFor(%q, %q) = %v, %v, %v; want %v, %v", tt.input, tt.selected, got, fromSel, err, tt.want, tt.fromSel)
			}
		})
	}
}

func TestModel_Update_ConnectDefaultsToSelectedHost(t *testing.T) {
	t.Parallel()
	t.Run("tunnel-only names the target on the line, runs it, offers no save", func(t *testing.T) {
		t.Parallel()
		argv := []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "1234:localhost:8080", "ubuntu"}
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, argv...)
		m := ui.WithSaveHost(polled(t, r, fakedial.New()), func(ui.AdhocEntry) error { return nil })
		m = ui.StaticCursor(m)
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L 1234:localhost:8080")})
		if view := m.View(); !strings.Contains(view, "→ ssh -fN -o BatchMode=yes -L 1234:localhost:8080 ubuntu   target: ubuntu (selected)") {
			t.Errorf("View() should name the defaulted target before enter:\n%s", view)
		}
		before := len(r.CallKeys())
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd == nil || ui.Prompting(m) {
			t.Fatalf("enter: cmd %v, prompting %v", cmd != nil, ui.Prompting(m))
		}
		next, after := m.Update(cmd())
		m = next.(ui.Model)
		if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{fakerun.Key(argv...)}) {
			t.Errorf("calls = %v, want %v", got, fakerun.Key(argv...))
		}
		if ui.PromptingSave(m) || after == nil || !strings.Contains(m.View(), "tunnel up in the background") {
			t.Errorf("after the tunnel: save prompt %v (want none), view:\n%s", ui.PromptingSave(m), m.View())
		}
	})
	t.Run("handoff to the selection offers no save; a typed target still does", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		m := ui.WithSaveHost(polled(t, r, fakedial.New()), func(ui.AdhocEntry) error { return nil })
		m = ui.StaticCursor(m)
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-p 2222")})
		if view := m.View(); !strings.Contains(view, "→ ssh -p 2222 ubuntu   target: ubuntu (selected)") {
			t.Errorf("View() should name the defaulted target:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		if cmd == nil {
			t.Fatalf("enter cmd = nil, want the handoff")
		}
		next, _ = next.(ui.Model).Update(ui.AdhocExit(nil))
		if ui.PromptingSave(next.(ui.Model)) {
			t.Errorf("a selection-defaulted target must not be offered for saving:\n%s", next.(ui.Model).View())
		}
		// Explicit target: unchanged — no target note, the save offer follows.
		m = press(t, m, "esc", "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("root@192.0.2.99")})
		if view := m.View(); strings.Contains(view, "(selected)") {
			t.Errorf("View() with an explicit target should not mention the selection:\n%s", view)
		}
		next, _ = m.Update(keyMsg("enter"))
		next, _ = next.(ui.Model).Update(ui.AdhocExit(nil))
		if !ui.PromptingSave(next.(ui.Model)) {
			t.Errorf("a typed target should still be offered for saving:\n%s", next.(ui.Model).View())
		}
	})
	t.Run("ambient host is a valid default", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(press(t, tierModel(t, tierHosts()), "G")) // office: ambient tier
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 3000")})
		if view := m.View(); !strings.Contains(view, "→ ssh -L 3000:localhost:3000 office   target: office (selected)") {
			t.Errorf("View() should default to the ambient host:\n%s", view)
		}
	})
	t.Run("empty list keeps the no-target error", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(press(t, sized(t, nil), "a"))
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 8080")})
		if view := m.View(); strings.Contains(view, "(selected)") {
			t.Errorf("View() with nothing selected offers a target:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		if cmd != nil || !strings.Contains(next.(ui.Model).View(), "no target: want [user@]host[:port]") {
			t.Errorf("enter with nothing selected: cmd %v:\n%s", cmd != nil, next.(ui.Model).View())
		}
	})
	t.Run("the settings new-connection prompt never defaults", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(settingsModel(t, fakerun.New()))
		m = press(t, m, "G", "k", "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 8080")})
		if view := m.View(); strings.Contains(view, "(selected)") {
			t.Errorf("View() in the new-connection prompt should not default the target:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		if cmd != nil || !strings.Contains(next.(ui.Model).View(), "no target") {
			t.Errorf("enter in the new-connection prompt without a target: cmd %v:\n%s", cmd != nil, next.(ui.Model).View())
		}
	})
}
