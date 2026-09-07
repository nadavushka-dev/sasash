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
		{name: "bad port keeps its error", input: "-p abc", selected: "ubuntu", wantErr: `bad port "abc"`},
		// A hand-written alias is a word to ssh, whatever it contains.
		{name: "alias with a colon stays the alias", input: "-L 8080", selected: "web:8080", want: []string{"ssh", "-L", "8080:localhost:8080", "web:8080"}, fromSel: true},
		{name: "alias with an at-sign stays the alias", input: "-N", selected: "a@b", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "a@b"}, fromSel: true},
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
	t.Run("tunnel-only on the selection goes through the engine, shows in the pane, offers no save", func(t *testing.T) {
		t.Parallel()
		// SASASH.4.17: the selected host is saved, so -N is `+` by another
		// road — the engine's exact flow, recorded, so Status shows it.
		const spec = "1234:localhost:8080"
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "forward", "-L", spec, "ubuntu")
		m := ui.WithSaveHost(polled(t, r, fakedial.New()), func(ui.AdhocEntry) error { return nil })
		m = ui.StaticCursor(m)
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L " + spec)})
		if view := m.View(); !strings.Contains(view, "→ target: ubuntu (selected) · ssh -fN ubuntu ; ssh -O forward -L "+spec+" ubuntu") {
			t.Errorf("View() should name the defaulted target and the engine's flow before enter:\n%s", view)
		}
		before := len(r.CallKeys())
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd == nil || ui.Prompting(m) {
			t.Fatalf("enter: cmd %v, prompting %v", cmd != nil, ui.Prompting(m))
		}
		next, after := m.Update(cmd())
		m = next.(ui.Model)
		// The master was alive (check exit 0), so no start: just the forward.
		if got, want := r.CallKeys()[before:], []string{"ssh -O check ubuntu", "ssh -O forward -L " + spec + " ubuntu"}; !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %v, want %v", got, want)
		}
		if ui.PromptingSave(m) || ui.PopupOpen(m) || after == nil || !strings.Contains(m.View(), "tunnel up on ubuntu: "+spec+" — ad-hoc, in the tunnels pane") {
			t.Errorf("after the tunnel: save prompt %v (want none), popup %v, view:\n%s", ui.PromptingSave(m), ui.PopupOpen(m), m.View())
		}
		// The regression: the re-poll finds the recorded ad-hoc forward,
		// and the tunnels pane shows it as an ad-hoc row with a live state
		// (the notice on the bottom line carries the spec too: the row is
		// the one that ends in a state).
		m = feed(t, m, after)
		row := ""
		for _, l := range strings.Split(m.View(), "\n") {
			if strings.Contains(l, spec) && strings.HasSuffix(strings.TrimSpace(l), "down") {
				row = l
			}
		}
		if row == "" || !strings.Contains(row, "ad-hoc") {
			t.Errorf("View() after the re-poll should list the forward as an ad-hoc row (got %q):\n%s", row, m.View())
		}
	})
	t.Run("handoff to the selection offers no save; a typed target still does", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		m := ui.WithSaveHost(polled(t, r, fakedial.New()), func(ui.AdhocEntry) error { return nil })
		m = ui.StaticCursor(m)
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-p 2222")})
		if view := m.View(); !strings.Contains(view, "→ target: ubuntu (selected) · ssh -p 2222 ubuntu") {
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
		if view := m.View(); !strings.Contains(view, "→ target: office (selected) · ssh -L 3000:localhost:3000 office") {
			t.Errorf("View() should default to the ambient host:\n%s", view)
		}
	})
	t.Run("the disclosure survives a narrow frame", func(t *testing.T) {
		t.Parallel()
		// At 80 columns the line's tail is cut with an ellipsis (4.9): the
		// target note leads, so it is never the part that goes.
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		m := ui.StaticCursor(update(t, polled(t, r, fakedial.New()), tea.WindowSizeMsg{Width: 80, Height: 24}))
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-L 8080 -L 9090 -L 7070 -L 6060")})
		view := m.View()
		if !strings.Contains(view, "→ target: ubuntu (selected) · ssh -L 8080:localhost:8080") || !strings.Contains(view, "…") {
			t.Errorf("View() at 80 cols should lead with the target and cut the tail:\n%s", view)
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
