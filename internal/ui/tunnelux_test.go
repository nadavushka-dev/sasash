package ui_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestParseAdhocTunnelArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		want       []string // the -fN argv
		tunnelOnly bool
		wantErr    string
	}{
		{name: "-N before the target", input: "-N -L 8080:localhost:80 root@192.0.2.99", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "8080:localhost:80", "root@192.0.2.99"}, tunnelOnly: true},
		{name: "-N after the target", input: "192.0.2.99 -N", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "192.0.2.99"}, tunnelOnly: true},
		{name: "pasted ssh command with -p", input: "ssh -L 8080:localhost:80 -p 2222 root@192.0.2.99", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "8080:localhost:80", "-p", "2222", "root@192.0.2.99"}},
		{name: "pasted -N -p glued", input: "ssh -N -p2222 -L5432:127.0.0.1:5432 db.invalid", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "5432:127.0.0.1:5432", "-p", "2222", "db.invalid"}, tunnelOnly: true},
		{name: "same port both ways", input: "192.0.2.99:2222 -p 2222", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-p", "2222", "192.0.2.99"}},
		{name: "-N twice is once", input: "-N h -N", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "h"}, tunnelOnly: true},
		{name: "port given twice differently", input: "192.0.2.99:2222 -p 2223", wantErr: "port given twice"},
		{name: "bad -p", input: "-p x h", wantErr: "bad port"},
		{name: "dangling -p", input: "h -p", wantErr: "-p needs a value"},
		{name: "two targets", input: "h1 h2", wantErr: `unexpected "h2" after the target "h1"`},
		{name: "ssh alone", input: "ssh", wantErr: "type [user@]host"},
		{name: "unknown option", input: "ssh -T h", wantErr: `unexpected "-T"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, tunnelOnly, err := ui.ParseAdhocTunnelArgv(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ParseAdhocTunnelArgv(%q) err = %v, want containing %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAdhocTunnelArgv(%q) = %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) || tunnelOnly != tt.tunnelOnly {
				t.Errorf("ParseAdhocTunnelArgv(%q) = %v, %v; want %v, %v", tt.input, got, tunnelOnly, tt.want, tt.tunnelOnly)
			}
		})
	}
	// The interactive argv drops only the -fN: a pasted command connects
	// exactly as pasted.
	if got, err := ui.ParseAdhocArgv("ssh -p 2222 root@192.0.2.99"); err != nil || !reflect.DeepEqual(got, []string{"ssh", "-p", "2222", "root@192.0.2.99"}) {
		t.Errorf("ParseAdhocArgv(pasted) = %v, %v", got, err)
	}
}

func TestModel_Update_AdhocTunnelOnly(t *testing.T) {
	t.Parallel()
	// SASASH.4.17: an unsaved -N target saves first. Declining the save
	// offers the unmanaged run up front; proceeding runs the raw ssh -fN
	// and its outcome names the cleanup — never a silent, invisible tunnel.
	const input = "-N -L 8080:localhost:80 root@192.0.2.99"
	argv := []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "8080:localhost:80", "root@192.0.2.99"}
	tests := []struct {
		name      string
		reply     fakerun.Reply
		wantNote  []string
		wantPopup bool
	}{
		{name: "up, unmanaged", reply: fakerun.Reply{Result: run.Result{Code: 0}}, wantNote: []string{"unmanaged tunnel up", "pgrep -fl 'ssh -fN'", "x/X cannot stop it"}, wantPopup: true},
		{name: "auth needed a prompt", reply: fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("root@192.0.2.99: Permission denied (publickey,password).\n")}}, wantNote: []string{"ssh -fN failed: Permission denied (publickey,password).", "keys/agent", "connect once"}, wantPopup: true},
		{name: "ssh never ran", reply: fakerun.Reply{Err: errors.New("no ssh binary")}, wantNote: []string{"ssh: no ssh binary"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().Stub(tt.reply, argv...)
			m := ui.WithSaveHost(probedModel(t, r), func(ui.AdhocEntry) error { return nil })
			m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
			m = press(t, m, "a")
			m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(input)})
			m = press(t, m, "enter")
			// Save first: the alias prompt, saying why.
			if !ui.PromptingSave(m) || !strings.Contains(m.View(), "shown and stoppable") || len(r.CallKeys()) != 0 {
				t.Fatalf("enter on an unsaved -N should open the save prompt first (prompting %v, calls %v):\n%s", ui.PromptingSave(m), r.CallKeys(), m.View())
			}
			// Declined: the honest question, nothing run yet.
			m = press(t, m, "esc")
			if view := m.View(); !ui.PopupOpen(m) || ui.Prompting(m) || !strings.Contains(view, "run an unmanaged tunnel?") || !strings.Contains(view, strings.Join(argv, " ")) || !strings.Contains(view, "pgrep -fl 'ssh -fN'") || !strings.Contains(view, "cannot stop it") || len(r.CallKeys()) != 0 {
				t.Fatalf("declining the save should offer the unmanaged run (popup %v, calls %v):\n%s", ui.PopupOpen(m), r.CallKeys(), view)
			}
			// esc there runs nothing.
			if c := press(t, m, "esc"); ui.PopupOpen(c) || len(r.CallKeys()) != 0 || !strings.Contains(c.View(), "cancelled — nothing started") {
				t.Errorf("esc on the question should run nothing (popup %v, calls %v):\n%s", ui.PopupOpen(c), r.CallKeys(), c.View())
			}
			// enter proceeds: the raw path, through the runner, no handoff.
			next, cmd := m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if cmd == nil || ui.PopupOpen(m) || !strings.Contains(m.View(), "starting unmanaged ssh -fN") {
				t.Fatalf("enter on the question: cmd %v, popup %v:\n%s", cmd != nil, ui.PopupOpen(m), m.View())
			}
			msg := cmd()
			if got, want := r.CallKeys(), []string{fakerun.Key(argv...)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("runner calls = %v, want %v", got, want)
			}
			m = update(t, m, msg)
			view := m.View()
			for _, w := range tt.wantNote {
				if !strings.Contains(view, w) {
					t.Errorf("View() missing %q:\n%s", w, view)
				}
			}
			// The save was declined: no offer follows either outcome.
			if ui.PopupOpen(m) != tt.wantPopup || ui.PromptingSave(m) {
				t.Errorf("after the run: popup %v (want %v), save prompt %v (want none):\n%s", ui.PopupOpen(m), tt.wantPopup, ui.PromptingSave(m), view)
			}
		})
	}
}

func TestModel_Update_AdhocTunnelSaveFirst(t *testing.T) {
	t.Parallel()
	t.Run("a saved alias carries the tunnel through the engine", func(t *testing.T) {
		t.Parallel()
		// The block is on disk after the save: the engine starts the master
		// through the new alias and adds the forward — recorded, shown.
		const spec = "8080:localhost:80"
		r := fakerun.New().StubExit(1, "ssh", "-O", "check", "edge")
		d := fakedial.New()
		sp := fakespawn.New().Dial(d)
		var saved []ui.AdhocEntry
		m := ui.WithSaveHost(polledSpawn(t, r, sp, d, t.TempDir()), func(e ui.AdhocEntry) error { saved = append(saved, e); return nil })
		m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L " + spec + " root@192.0.2.99")})
		m = press(t, m, "enter")
		if !ui.PromptingSave(m) {
			t.Fatalf("enter should open the save prompt first:\n%s", m.View())
		}
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
		next, save := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if save == nil {
			t.Fatalf("enter on the alias should save")
		}
		next, start := m.Update(save())
		m = next.(ui.Model)
		if len(saved) != 1 || saved[0].Alias != "edge" || saved[0].Host != "192.0.2.99" || saved[0].User != "root" || !reflect.DeepEqual(saved[0].Forwards, []string{spec}) {
			t.Errorf("saved = %+v, want the typed target under edge", saved)
		}
		if start == nil || !strings.Contains(m.View(), "starting "+spawnArgv("edge", spec)) {
			t.Fatalf("the save should start the engine flow (cmd %v):\n%s", start != nil, m.View())
		}
		before := len(r.CallKeys())
		m = feed(t, m, start)
		if got, want := sp.Spawned(), []string{spawnArgv("edge", spec)}; !reflect.DeepEqual(got, want) {
			t.Errorf("spawned = %v, want the engine's flow %v", got, want)
		}
		if got := r.CallKeys()[before:]; len(got) != 0 {
			t.Errorf("the flow ran %v through the runner, want nothing", got)
		}
		if view := m.View(); !strings.Contains(view, "added "+spec+" on edge") || ui.PopupOpen(m) || ui.Prompting(m) {
			t.Errorf("View() after the engine flow (popup %v, prompting %v):\n%s", ui.PopupOpen(m), ui.Prompting(m), view)
		}
	})
	t.Run("an empty alias declines too", func(t *testing.T) {
		t.Parallel()
		m := ui.WithSaveHost(probedModel(t, fakerun.New()), func(ui.AdhocEntry) error { return nil })
		m = press(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24}), "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N root@192.0.2.99")})
		m = press(t, m, "enter", "enter")
		if !ui.PopupOpen(m) || !strings.Contains(m.View(), "run an unmanaged tunnel?") {
			t.Errorf("an empty alias should offer the unmanaged run:\n%s", m.View())
		}
	})
	t.Run("declining a plain connect's save is still just not saved", func(t *testing.T) {
		t.Parallel()
		// The question belongs to -N alone: a declined save after an
		// interactive connect changes nothing and asks nothing.
		m := ui.WithSaveHost(probedModel(t, fakerun.New()), func(ui.AdhocEntry) error { return nil })
		m = press(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24}), "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("root@192.0.2.99")})
		next, _ := m.Update(keyMsg("enter"))
		next, _ = next.(ui.Model).Update(ui.AdhocExit(nil))
		m = next.(ui.Model)
		if !ui.PromptingSave(m) {
			t.Fatalf("the save offer should follow the connect:\n%s", m.View())
		}
		m = press(t, m, "esc")
		if ui.PopupOpen(m) || ui.Prompting(m) || !strings.Contains(m.View(), "not saved") {
			t.Errorf("esc should decline quietly (popup %v):\n%s", ui.PopupOpen(m), m.View())
		}
	})
	t.Run("nowhere to save: the question comes at once", func(t *testing.T) {
		t.Parallel()
		m := press(t, update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 100, Height: 24}), "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N root@192.0.2.99")})
		m = press(t, m, "enter")
		if !ui.PopupOpen(m) || ui.Prompting(m) || !strings.Contains(m.View(), "run an unmanaged tunnel?") {
			t.Errorf("without a save hook enter should ask at once:\n%s", m.View())
		}
	})
	t.Run("a typed saved alias goes through the engine; a user or port beside it is refused; an address saves first", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name    string
			input   string
			preview string // "" when nothing would run
			route   string // engine, nothing, refused, save
		}{
			{name: "bare alias", input: "-N ubuntu -L 8080:localhost:80", preview: spawnArgv("ubuntu", "8080:localhost:80"), route: "engine"},
			{name: "alias with no specs has nothing to start", input: "ubuntu -N", route: "nothing"},
			{name: "alias with a user", input: "-N root@ubuntu", route: "refused"},
			{name: "alias with a port", input: "-N ubuntu:2222", route: "refused"},
			{name: "not a host", input: "-N 192.0.2.99", route: "save"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
				d := fakedial.New()
				sp := fakespawn.New().Dial(d)
				m := ui.WithSaveHost(polledSpawn(t, r, sp, d, t.TempDir()), func(ui.AdhocEntry) error { return nil })
				m = press(t, update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}), "a")
				m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.input)})
				if view := m.View(); (tt.preview != "" && !strings.Contains(view, "→ "+tt.preview)) || strings.Contains(view, "-fN") {
					t.Errorf("View() should preview exactly the engine's steps (%q):\n%s", tt.preview, view)
				}
				before := len(r.CallKeys())
				next, cmd := m.Update(keyMsg("enter"))
				m = next.(ui.Model)
				switch tt.route {
				case "engine":
					if cmd == nil || ui.Prompting(m) {
						t.Fatalf("enter: cmd %v, prompting %v; want the engine action", cmd != nil, ui.Prompting(m))
					}
					m = update(t, m, cmd())
					if got := sp.Spawned(); !reflect.DeepEqual(got, []string{spawnArgv("ubuntu", "8080:localhost:80")}) || len(r.CallKeys()) != before {
						t.Errorf("spawned = %v, runner %v; want the engine's one process, never the raw ssh -fN", got, r.CallKeys()[before:])
					}
				case "nothing":
					if cmd != nil || len(sp.Spawned()) != 0 || len(r.CallKeys()) != before || !strings.Contains(m.View(), "nothing to start") {
						t.Errorf("enter on -N without -L: cmd %v, spawned %v, calls %v; want nothing started and the notice:\n%s", cmd != nil, sp.Spawned(), r.CallKeys()[before:], m.View())
					}
				case "refused":
					if cmd != nil || !ui.Prompting(m) || ui.PromptingSave(m) || len(r.CallKeys()) != before || !strings.Contains(m.View(), "ubuntu is saved: -N ubuntu alone, or its address") {
						t.Errorf("enter should refuse in the prompt with the reason (cmd %v, prompting %v, calls %v):\n%s", cmd != nil, ui.Prompting(m), r.CallKeys()[before:], m.View())
					}
				case "save":
					if !ui.PromptingSave(m) || len(r.CallKeys()) != before {
						t.Errorf("enter should save first (prompting %v, calls %v)", ui.PromptingSave(m), r.CallKeys()[before:])
					}
				}
			})
		}
	})
	t.Run("the refusal fits beside the input at 80 columns, and the line says it before enter", func(t *testing.T) {
		t.Parallel()
		m := ui.StaticCursor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()))
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -p 2222 -L 8080:localhost:80")}) // the selection plus a port
		if view := m.View(); !strings.Contains(view, "→ ubuntu is saved: -N ubuntu alone, or its address") || strings.Contains(view, "BatchMode") {
			t.Errorf("View() before enter should say the refusal, not promise a command:\n%s", view)
		}
		// At 80 columns, beside a short input, the whole reason is on the line.
		m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		m = press(t, m, "esc", "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -p 2222")})
		m = press(t, m, "enter")
		for _, l := range strings.Split(m.View(), "\n") {
			if strings.Contains(l, "connect:") && !strings.Contains(l, "ubuntu is saved: -N ubuntu alone, or its address") {
				t.Errorf("the whole refusal should fit the 80-column line: %q", l)
			}
		}
	})
	t.Run("a held port comes back as the remap offer, as with +", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New().Up("127.0.0.1:8080", time.Millisecond)
		m := ui.StaticCursor(polled(t, r, d))
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L 8080:localhost:80")})
		next, cmd := m.Update(keyMsg("enter"))
		before := len(r.CallKeys())
		next, _ = next.(ui.Model).Update(cmd()) // the conflict lands
		m = next.(ui.Model)
		if got := r.CallKeys()[before:]; len(got) != 0 {
			t.Errorf("calls after the conflict = %v, want none", got)
		}
		if !ui.Prompting(m) || !strings.Contains(m.View(), "8080 is in use") || !strings.Contains(m.View(), "8081 is free") {
			t.Errorf("a held port should come back as the remap offer:\n%s", m.View())
		}
	})
	t.Run("the save carries its own target: a later prompt cannot change what starts", func(t *testing.T) {
		t.Parallel()
		const spec = "8080:localhost:80"
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(1, "ssh", "-O", "check", "edge")
		d := fakedial.New()
		sp := fakespawn.New().Dial(d)
		m := ui.WithSaveHost(polledSpawn(t, r, sp, d, t.TempDir()), func(ui.AdhocEntry) error { return nil })
		m = press(t, update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}), "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L " + spec + " root@192.0.2.99")})
		m = press(t, m, "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
		next, save := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		// Before the save lands, another -N is typed AND submitted for
		// another host: the pending slot has moved on.
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N ubuntu -L 9999:localhost:99")})
		next, second := m.Update(keyMsg("enter"))
		m = feed(t, next.(ui.Model), second)
		next, start := m.Update(save())
		m = next.(ui.Model)
		if start == nil {
			t.Fatalf("the save should start the engine flow")
		}
		n := len(sp.Spawned())
		feed(t, m, start)
		got := strings.Join(sp.Spawned()[n:], " | ")
		if !strings.Contains(got, spawnArgv("edge", spec)) || strings.Contains(got, "9999") {
			t.Errorf("spawned = %q, want the first -N's spec on edge and nothing of the later prompt", got)
		}
	})
	t.Run("a plain connect saved after a save-first -N starts nothing", func(t *testing.T) {
		t.Parallel()
		// The tunnel flag is spent by the save it belonged to.
		r := fakerun.New().StubExit(1, "ssh", "-O", "check", "edge").StubExit(0, "ssh", "-fN", "edge").StubExit(0, "ssh", "-O", "forward", "-L", "8080:localhost:80", "edge")
		m := ui.WithSaveHost(polled(t, r, fakedial.New()), func(ui.AdhocEntry) error { return nil })
		m = press(t, update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}), "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L 8080:localhost:80 root@192.0.2.99")})
		m = press(t, m, "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
		next, save := m.Update(keyMsg("enter"))
		next, start := next.(ui.Model).Update(save())
		m = feed(t, next.(ui.Model), start)
		// Now an ordinary connect, returned from, and saved.
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("root@192.0.2.50")})
		next, _ = m.Update(keyMsg("enter"))
		next, _ = next.(ui.Model).Update(ui.AdhocExit(nil))
		m = next.(ui.Model)
		if !ui.PromptingSave(m) {
			t.Fatalf("the connect should offer the save:\n%s", m.View())
		}
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("other")})
		next, save = m.Update(keyMsg("enter"))
		before := len(r.CallKeys())
		next, after := next.(ui.Model).Update(save())
		m = next.(ui.Model)
		if after != nil || len(r.CallKeys()) != before || strings.Contains(m.View(), "starting") || !strings.Contains(m.View(), "saved other") {
			t.Errorf("a plain save should start no tunnel (cmd %v, calls %v):\n%s", after != nil, r.CallKeys()[before:], m.View())
		}
	})
	t.Run("a failed save keeps the choice: the question follows", func(t *testing.T) {
		t.Parallel()
		m := ui.WithSaveHost(probedModel(t, fakerun.New()), func(ui.AdhocEntry) error { return errors.New("no sasash.conf — run `sasash setup` first") })
		m = press(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24}), "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N root@192.0.2.99")})
		m = press(t, m, "enter")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
		next, save := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), save())
		if !ui.PopupOpen(m) || !strings.Contains(m.View(), "run an unmanaged tunnel?") || !strings.Contains(m.View(), "not saved: no sasash.conf") {
			t.Errorf("a failed save should say so and ask about the unmanaged run:\n%s", m.View())
		}
	})
	t.Run("the question's footer says enter runs it; no search inside", func(t *testing.T) {
		t.Parallel()
		m := press(t, update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 100, Height: 24}), "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N root@192.0.2.99")})
		m = press(t, m, "enter")
		lines := strings.Split(m.View(), "\n")
		if foot := lines[len(lines)-1]; !strings.Contains(foot, "enter run it") || !strings.Contains(foot, "esc cancel") || strings.Contains(foot, "close") {
			t.Errorf("footer under the question = %q, want run it / cancel", foot)
		}
		if view := press(t, m, "/", "z", "z").View(); strings.Contains(view, "/zz") || strings.Contains(view, "no match") || !strings.Contains(view, "pgrep") {
			t.Errorf("a question should not search its own terms:\n%s", view)
		}
	})
}

// editModel is the tunnels fixture with the edit hook recording calls:
// master alive, web's first forward up as our pid 41, everything else
// down.
func editModel(t *testing.T, r *fakerun.Fake, calls *[][]string, hookErr error) (ui.Model, *fakespawn.Fake) {
	t.Helper()
	r.StubExit(0, "ssh", "-O", "check", "ubuntu")
	// The cache also records web's second spec as a dead pid: taking the
	// whole set down would touch it too, so only per-spec surgery passes.
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41},{"set":"web","spec":"`+specWeb1+`","pid":42}]}`)
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	sp := fakespawn.New().Dial(d).LiveOn(41, "3000")
	m := tunnelModelSpawn(t, r, sp, d, stateDir)
	m = feed(t, m, m.Init())
	return ui.WithSaveForwardEdit(m, func(alias, set, old, new string) error {
		*calls = append(*calls, []string{alias, set, old, new})
		return hookErr
	}), sp
}

func TestModel_Update_EditForwardLive(t *testing.T) {
	t.Parallel()
	var calls [][]string
	r := fakerun.New()
	m, sp := editModel(t, r, &calls, nil)
	m = press(t, m, "l", "E") // web0, live
	view := m.View()
	if !ui.Prompting(m) || !strings.Contains(view, "edit web on ubuntu: "+specWeb0) {
		t.Fatalf("E should open the prompt prefilled with the spec:\n%s", view)
	}
	// The row shows exactly what enter will run, following the typing.
	for _, w := range []string{"sasash.conf: forwards web: " + specWeb0 + " → " + specWeb0, "kill -TERM 41", spawnArgv("ubuntu", specWeb0)} {
		if !strings.Contains(view, w) {
			t.Errorf("View() while editing missing the plan line %q:\n%s", w, view)
		}
	}
	for range specWeb0 {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3005:localhost:3000")})
	if view := m.View(); !strings.Contains(view, spawnArgv("ubuntu", "3005:localhost:3000")) || !strings.Contains(view, "kill -TERM 41") {
		t.Errorf("View() plan did not follow the typing:\n%s", view)
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd == nil || ui.Prompting(m) {
		t.Fatalf("enter: cmd %v, prompting %v", cmd != nil, ui.Prompting(m))
	}
	next, after := m.Update(cmd())
	m = next.(ui.Model)
	// Config first, then per-process surgery only: kill old, spawn new,
	// nothing else.
	if want := [][]string{{"ubuntu", "web", specWeb0, "3005:localhost:3000"}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("hook calls = %v, want %v", calls, want)
	}
	if got, want := sp.Killed(), []int{41}; !reflect.DeepEqual(got, want) {
		t.Errorf("killed = %v, want %v", got, want)
	}
	if got, want := sp.Spawned(), []string{spawnArgv("ubuntu", "3005:localhost:3000")}; !reflect.DeepEqual(got, want) {
		t.Errorf("spawned = %v, want exactly %v", got, want)
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("the edit ran %v through the runner, want nothing", got)
	}
	if view := m.View(); !strings.Contains(view, "re-plumbed "+specWeb0+" → 3005:localhost:3000") {
		t.Errorf("View() missing the outcome:\n%s", view)
	}
	if after == nil {
		t.Errorf("Update(editFwdDone) cmd = nil, want a status re-poll and reload")
	}
}

func TestModel_Update_EditForwardOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		script      func(sp *fakespawn.Fake)
		hookErr     error
		row         []string // keys to reach the row after l
		ours        bool     // web0 served by our pid 41 (editModel)
		newSpec     string
		wantHook    bool
		wantKilled  []int
		wantSpawned []string
		wantNote    []string
		wantPlan    []string
		notPlan     []string
	}{
		{
			name:     "down row is config only",
			row:      []string{"j"}, // web1: down
			ours:     true,
			newSpec:  "3001:localhost:3009",
			wantHook: true,
			wantNote: []string{"declared 3001:localhost:3009 in web"},
			wantPlan: []string{"sasash.conf: forwards web"},
			notPlan:  []string{"kill -TERM", "ssh -N"},
		},
		{
			name:     "a port that answers without a process of ours is config only",
			ours:     false, // web0's port answers but nothing of ours can be re-plumbed
			newSpec:  "3000:localhost:3009",
			wantHook: true,
			wantNote: []string{"declared 3000:localhost:3009 in web"},
			notPlan:  []string{"kill -TERM"},
		},
		{
			name:     "config write failure stops before any process work",
			hookErr:  errors.New(`spec "3000:localhost:3009" already in set "web"`),
			ours:     true,
			newSpec:  "3000:localhost:3009",
			wantHook: true,
			wantNote: []string{"not declared: ", "already in set"},
		},
		{
			name: "spawn failure says the old one is gone",
			script: func(sp *fakespawn.Fake) {
				sp.Exits("bind: Address already in use", tunnel.SpawnArgv("ubuntu", "3000:localhost:3009")...)
			},
			ours:        true,
			newSpec:     "3000:localhost:3009",
			wantHook:    true,
			wantKilled:  []int{41},
			wantSpawned: []string{spawnArgv("ubuntu", "3000:localhost:3009")},
			wantNote:    []string{"killed " + specWeb0 + " but spawn of 3000:localhost:3009 failed", "Address already in use"},
		},
		{
			name: "kill failure is reported and the spawn is not attempted",
			script: func(sp *fakespawn.Fake) {
				sp.KillErr = errors.New("operation not permitted")
			},
			ours:       true,
			newSpec:    "3000:localhost:3009",
			wantHook:   true,
			wantKilled: []int{41},
			wantNote:   []string{"kill of " + specWeb0 + " failed: pid 41: operation not permitted", "config updated"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls [][]string
			r := fakerun.New()
			var m ui.Model
			var sp *fakespawn.Fake
			if tt.ours {
				m, sp = editModel(t, r, &calls, tt.hookErr)
			} else {
				r.StubExit(0, "ssh", "-O", "check", "ubuntu")
				d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
				sp = fakespawn.New().Dial(d)
				m = polledSpawn(t, r, sp, d, t.TempDir())
				m = ui.WithSaveForwardEdit(m, func(alias, set, old, new string) error {
					calls = append(calls, []string{alias, set, old, new})
					return tt.hookErr
				})
			}
			if tt.script != nil {
				tt.script(sp)
			}
			m = press(t, m, append([]string{"l"}, tt.row...)...)
			m = press(t, m, "E")
			view := m.View()
			for _, w := range tt.wantPlan {
				if !strings.Contains(view, w) {
					t.Errorf("plan missing %q:\n%s", w, view)
				}
			}
			for _, w := range tt.notPlan {
				if strings.Contains(view, w) {
					t.Errorf("plan shows %q, want absent:\n%s", w, view)
				}
			}
			for range 32 {
				m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
			}
			m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.newSpec)})
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("enter cmd = nil, want the edit")
			}
			m = update(t, m, cmd())
			if (len(calls) == 1) != tt.wantHook {
				t.Errorf("hook calls = %v, want called %v", calls, tt.wantHook)
			}
			if got := sp.Killed(); !reflect.DeepEqual(got, tt.wantKilled) && !(len(got) == 0 && len(tt.wantKilled) == 0) {
				t.Errorf("killed = %v, want %v", got, tt.wantKilled)
			}
			if got := sp.Spawned(); !reflect.DeepEqual(got, tt.wantSpawned) && !(len(got) == 0 && len(tt.wantSpawned) == 0) {
				t.Errorf("spawned = %v, want %v", got, tt.wantSpawned)
			}
			if got := r.CallKeys()[before:]; len(got) != 0 {
				t.Errorf("the edit ran %v through the runner, want nothing", got)
			}
			view = m.View()
			for _, w := range tt.wantNote {
				if !strings.Contains(view, w) {
					t.Errorf("View() missing %q:\n%s", w, view)
				}
			}
		})
	}
}

func TestModel_Update_EditForwardGuards(t *testing.T) {
	t.Parallel()
	var calls [][]string
	r := fakerun.New()
	m, _ := editModel(t, r, &calls, nil)
	// No highlight: a hint, nothing opened.
	m = press(t, m, "E")
	if ui.Prompting(m) || !strings.Contains(m.View(), "l cycles") {
		t.Errorf("E without a highlight: prompting %v:\n%s", ui.Prompting(m), m.View())
	}
	// Unchanged spec and a bad spec keep the prompt; esc leaves it.
	m = press(t, m, "l", "E")
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !ui.Prompting(m) || !strings.Contains(m.View(), "unchanged") {
		t.Errorf("enter on the unchanged spec: cmd %v, prompting %v:\n%s", cmd != nil, ui.Prompting(m), m.View())
	}
	for range specWeb0 {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("nonsense")})
	next, cmd = m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !ui.Prompting(m) || !strings.Contains(m.View(), "want local:host:port") {
		t.Errorf("enter on a bad spec: cmd %v, prompting %v:\n%s", cmd != nil, ui.Prompting(m), m.View())
	}
	m = press(t, m, "esc")
	if ui.Prompting(m) || len(calls) != 0 || len(r.CallKeys()) != 1 {
		t.Errorf("esc: prompting %v, hook %v, calls %v", ui.Prompting(m), calls, r.CallKeys())
	}
	// While filtering E is text.
	f := press(t, m, "/", "E")
	if got := ui.FilterText(f); got != "E" || ui.Prompting(f) {
		t.Errorf("E while filtering: text %q, prompting %v", got, ui.Prompting(f))
	}
	// Help lists it.
	if view := press(t, m, "?").View(); !strings.Contains(view, "edit forward") {
		t.Errorf("View() full help missing E:\n%s", view)
	}
}

// adhocRowModel is the tunnels fixture with a recorded ad-hoc forward on
// port 9000; up says whether our pid 41 serves it and its port answers.
func adhocRowModel(t *testing.T, r *fakerun.Fake, up bool, calls *[][]string) (ui.Model, string, *fakespawn.Fake) {
	t.Helper()
	r.StubExit(0, "ssh", "-O", "check", "ubuntu")
	stateDir := t.TempDir()
	state := `{"forwards":[{"set":"","spec":"9000:localhost:9000"}]}`
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	if up {
		state = `{"forwards":[{"set":"","spec":"9000:localhost:9000","pid":41}]}`
		d.Up("127.0.0.1:9000", time.Millisecond)
		sp.LiveOn(41, "9000")
	}
	writeState(t, stateDir, state)
	m := tunnelModelSpawn(t, r, sp, d, stateDir)
	m = feed(t, m, m.Init())
	return ui.WithSaveForwardEdit(m, func(alias, set, old, new string) error {
		*calls = append(*calls, []string{alias, set, old, new})
		return nil
	}), stateDir, sp
}

func TestModel_Update_EditForwardAdhocRow(t *testing.T) {
	t.Parallel()
	// A live ad-hoc row is surgery only: no meta line exists to rewrite.
	var calls [][]string
	r := fakerun.New()
	m, _, sp := adhocRowModel(t, r, true, &calls)
	m = press(t, m, "l", "G", "E") // the ad-hoc row is last
	view := m.View()
	if !strings.Contains(view, "edit ad-hoc on ubuntu: 9000:localhost:9000") || strings.Contains(view, "sasash.conf:") {
		t.Fatalf("E on a live ad-hoc row: want the prompt and a surgery-only plan:\n%s", view)
	}
	for range len("9000:localhost:9000") {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9001:localhost:9000")})
	next, cmd := m.Update(keyMsg("enter"))
	next, after := next.(ui.Model).Update(cmd())
	m = next.(ui.Model)
	if len(calls) != 0 {
		t.Errorf("hook called for an ad-hoc row: %v", calls)
	}
	if got := sp.Killed(); !reflect.DeepEqual(got, []int{41}) {
		t.Errorf("killed = %v, want [41]", got)
	}
	if got, want := sp.Spawned(), []string{spawnArgv("ubuntu", "9001:localhost:9000")}; !reflect.DeepEqual(got, want) {
		t.Errorf("spawned = %v, want %v", got, want)
	}
	if view := m.View(); !strings.Contains(view, "re-plumbed 9000:localhost:9000 → 9001:localhost:9000") || strings.Contains(view, "declared") {
		t.Errorf("View() outcome for an ad-hoc row:\n%s", view)
	}
	// The old spec's record is purged with the re-plumb (C1): the re-poll
	// the edit arms shows the new spec only.
	if after == nil {
		t.Fatalf("Update(editFwdDone) cmd = nil, want the re-poll")
	}
	m = feed(t, m, after)
	var rows []string
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, "ad-hoc") {
			rows = append(rows, l)
		}
	}
	if joined := strings.Join(rows, "\n"); strings.Contains(joined, "9000:localhost:9000") || !strings.Contains(joined, "9001:localhost:9000") {
		t.Errorf("rows after the re-poll should carry only the new spec:\n%s", m.View())
	}

	// A kill failure on an ad-hoc row must not claim a config update:
	// nothing was written.
	var none2 [][]string
	m2, _, sp2 := adhocRowModel(t, fakerun.New(), true, &none2)
	sp2.KillErr = errors.New("operation not permitted")
	m2 = press(t, m2, "l", "G", "E")
	for range len("9000:localhost:9000") {
		m2 = update(t, m2, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m2 = update(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9001:localhost:9000")})
	next, cmd = m2.Update(keyMsg("enter"))
	m2 = update(t, next.(ui.Model), cmd())
	if view := m2.View(); !strings.Contains(view, "kill of 9000:localhost:9000 failed: pid 41: operation not permitted") || strings.Contains(view, "config updated") {
		t.Errorf("View() after a failed ad-hoc kill:\n%s", view)
	}

	// A dead ad-hoc row has nothing to edit: x it and + a new one.
	var none [][]string
	dead, _, _ := adhocRowModel(t, fakerun.New(), false, &none)
	dead = press(t, dead, "l", "G", "E")
	if ui.Prompting(dead) || !strings.Contains(dead.View(), "not live") {
		t.Errorf("E on a dead ad-hoc row: prompting %v:\n%s", ui.Prompting(dead), dead.View())
	}
}

func TestModel_Update_EditForwardNeedsHookForDeclared(t *testing.T) {
	t.Parallel()
	// Without the edit hook a declared, dead row cannot be edited; a
	// live one still can (surgery only).
	var none [][]string
	m, _ := editModel(t, fakerun.New(), &none, nil)
	m = ui.WithSaveForwardEdit(m, nil) // no hook
	m = press(t, m, "l", "j", "E")     // web1: down
	if ui.Prompting(m) || !strings.Contains(m.View(), "not available") {
		t.Errorf("E on a dead declared row without a hook: prompting %v:\n%s", ui.Prompting(m), m.View())
	}
	m = press(t, m, "k", "E") // web0: live
	if !ui.Prompting(m) || strings.Contains(m.View(), "sasash.conf:") || !strings.Contains(m.View(), "kill -TERM 41") {
		t.Errorf("E on a live declared row without a hook: want surgery-only plan:\n%s", m.View())
	}
}

func TestModel_View_EditPlanStaysOnItsRow(t *testing.T) {
	t.Parallel()
	// A poll lands while the prompt is open and the edited ad-hoc row is
	// gone: the highlight clamps onto another row, but the plan must not
	// move with it — it belongs to the row the edit is bound to, and so
	// does the action.
	var calls [][]string
	r := fakerun.New()
	m, stateDir, _ := adhocRowModel(t, r, true, &calls)
	m = press(t, m, "l", "G", "E")
	if !strings.Contains(m.View(), "kill -TERM 41") {
		t.Fatalf("plan missing before the poll:\n%s", m.View())
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(`{"forwards":[]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick) // the ad-hoc row is gone; the highlight clamps onto api
	view := m.View()
	if !ui.Prompting(m) || !strings.Contains(view, "edit ad-hoc on ubuntu: 9000:localhost:9000") {
		t.Fatalf("the prompt should stay bound to the ad-hoc spec:\n%s", view)
	}
	if strings.Contains(view, "-O cancel") || strings.Contains(view, "-O forward") || strings.Contains(view, "sasash.conf:") {
		t.Errorf("View() shows the plan under a row the edit is not bound to:\n%s", view)
	}
}

func TestModel_Update_SettingsNewConnRejectsTunnelOnly(t *testing.T) {
	t.Parallel()
	m := ui.WithSaveHost(settingsModel(t, fakerun.New()), func(ui.AdhocEntry) error { return nil })
	m = press(t, m, "G", "k", "enter")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N 192.0.2.99")})
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !ui.Prompting(m) || ui.PromptingSave(m) || !strings.Contains(m.View(), "-N belongs to the a prompt") {
		t.Errorf("-N in the settings prompt: cmd %v, prompting %v, save %v:\n%s", cmd != nil, ui.Prompting(m), ui.PromptingSave(m), m.View())
	}
}
