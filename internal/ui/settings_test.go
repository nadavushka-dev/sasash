package ui_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/setup"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// settingsModel is a probed model with the panel open.
func settingsModel(t *testing.T, r *fakerun.Fake) ui.Model {
	t.Helper()
	m := press(t, probedModel(t, r), "o")
	if !ui.SettingsOpen(m) {
		t.Fatalf("SettingsOpen() = false after o, want the panel")
	}
	return m
}

func TestModel_Update_SettingsOpensAndCloses(t *testing.T) {
	t.Parallel()
	r := fakerun.New()
	m := settingsModel(t, r)
	view := m.View()
	for _, want := range []string{"settings", "› create config file", "edit config file", "theme", "add new connection", "esc"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() with settings open missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "$ ssh") || strings.Contains(view, "paths") {
		t.Errorf("View() with settings open still shows the host panes:\n%s", view)
	}
	// Action keys are inert while the panel is open: nothing runs.
	before := len(r.CallKeys())
	for _, k := range []string{"y", "c", "e", "H", "a", "+", "t", "q", "/"} {
		next, cmd := m.Update(keyMsg(k))
		m = next.(ui.Model)
		if cmd != nil || !ui.SettingsOpen(m) || ui.Prompting(m) {
			t.Errorf("key %q while settings open: cmd %v, open %v, prompting %v", k, cmd != nil, ui.SettingsOpen(m), ui.Prompting(m))
		}
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("calls while settings open = %v, want none", got)
	}
	if got := ui.ThemeName(m); got != "gruvbox" {
		t.Errorf("t while settings open changed the theme to %q", got)
	}
	// esc closes; the host view is back.
	m = press(t, m, "esc")
	if ui.SettingsOpen(m) {
		t.Errorf("SettingsOpen() = true after esc, want closed")
	}
	if view := m.View(); !strings.Contains(view, "$ ssh web") {
		t.Errorf("View() after closing settings lost the host view:\n%s", view)
	}
}

func TestModel_Update_SettingsCursor(t *testing.T) {
	t.Parallel()
	m := settingsModel(t, fakerun.New())
	m = press(t, m, "j", "j")
	if view := m.View(); !strings.Contains(view, "› theme") {
		t.Errorf("View() after j j should highlight theme:\n%s", view)
	}
	m = press(t, m, "G", "j", "j") // clamps at the last entry
	if view := m.View(); !strings.Contains(view, "› add new connection") {
		t.Errorf("View() after G should highlight the last entry:\n%s", view)
	}
	m = press(t, m, "g", "k")
	if view := m.View(); !strings.Contains(view, "› create config file") {
		t.Errorf("View() after g k should stay on the first entry:\n%s", view)
	}
}

// homeWith builds a home with the given setup pieces in place.
func homeWith(t *testing.T, include, conf, cm bool) string {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "config.d"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if include {
		if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(setup.IncludeLine+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if conf {
		if err := os.WriteFile(filepath.Join(sshDir, "config.d", "sasash.conf"), []byte(setup.Header+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if cm {
		if err := os.Mkdir(filepath.Join(sshDir, "cm"), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	return home
}

func TestModel_Update_SettingsCreateConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		include, cm  bool
		wantPresent  []string
		wantMissing  []string
		wantEnsure   bool   // enter writes
		wantNotice   string // after enter
		wantLoadCall bool
	}{
		{
			name:         "bare home writes all three",
			wantMissing:  []string{setup.IncludeLine, "sasash.conf", "cm/"},
			wantEnsure:   true,
			wantNotice:   "set up",
			wantLoadCall: true,
		},
		{
			name:        "include and conf present writes the socket dir",
			include:     true,
			wantPresent: []string{setup.IncludeLine, "sasash.conf"},
			wantMissing: []string{"cm/"},
			wantEnsure:  true, wantNotice: "set up", wantLoadCall: true,
		},
		{
			name:        "already set up writes nothing",
			include:     true,
			cm:          true,
			wantPresent: []string{setup.IncludeLine, "cm/"},
			wantNotice:  "already set up",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := homeWith(t, tt.include, tt.include, tt.cm) // conf follows include here
			loaded := false
			m := ui.WithHome(settingsModel(t, fakerun.New()), home)
			m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
				loaded = true
				return nil, nil, nil
			})
			// enter on "create config file": the check runs in a cmd; the
			// pieces are pending until it lands.
			next, check := m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if check == nil {
				t.Fatalf("enter on create config cmd = nil, want the setup check")
			}
			if view := m.View(); !strings.Contains(view, "◌") || !strings.Contains(view, setup.IncludeLine) {
				t.Fatalf("View() before the check lands should list the pieces as pending:\n%s", view)
			}
			m = update(t, m, check())
			view := m.View()
			for _, w := range tt.wantPresent {
				if !lineHas(view, w, "● ", "present") {
					t.Errorf("View() should mark %q present:\n%s", w, view)
				}
			}
			for _, w := range tt.wantMissing {
				if !lineHas(view, w, "○ ", "missing") {
					t.Errorf("View() should mark %q missing:\n%s", w, view)
				}
			}
			// enter again: write what is missing — or refuse to write.
			next, ensure := m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if (ensure != nil) != tt.wantEnsure {
				t.Fatalf("second enter cmd != nil = %v, want %v", ensure != nil, tt.wantEnsure)
			}
			if tt.wantEnsure {
				next, after := m.Update(ensure()) // setupDoneMsg -> re-check + reload
				m = next.(ui.Model)
				m = feed(t, m, after)
				st, err := setup.Check(home)
				if err != nil || !st.Done() {
					t.Errorf("setup.Check() after enter = %+v, %v; want done", st, err)
				}
			}
			if view := m.View(); !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
			if loaded != tt.wantLoadCall {
				t.Errorf("LoadHosts called = %v, want %v", loaded, tt.wantLoadCall)
			}
		})
	}
}

// lineHas reports whether some line of view contains all of the wants.
func lineHas(view string, wants ...string) bool {
	for _, line := range strings.Split(view, "\n") {
		ok := true
		for _, w := range wants {
			if !strings.Contains(line, w) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestModel_Update_SettingsCreateConfigNeedsHome(t *testing.T) {
	t.Parallel()
	m := settingsModel(t, fakerun.New()) // no home
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !strings.Contains(m.View(), "no home") {
		t.Errorf("enter with no home: cmd %v, view:\n%s", cmd != nil, m.View())
	}
}

func TestModel_Update_SettingsEditConfig(t *testing.T) {
	t.Parallel()
	// With an editor: the e handoff, settings closed.
	m := ui.WithEditor(settingsModel(t, fakerun.New()), []string{"vi"}, "", "/tmp/x/sasash.conf")
	next, cmd := press(t, m, "j").Update(keyMsg("enter"))
	if cmd == nil || ui.SettingsOpen(next.(ui.Model)) {
		t.Errorf("enter on edit config: cmd %v, settings open %v; want the handoff and the panel closed", cmd != nil, ui.SettingsOpen(next.(ui.Model)))
	}
	// Without one: a notice, the panel stays.
	plain := settingsModel(t, fakerun.New())
	next, cmd = press(t, plain, "j").Update(keyMsg("enter"))
	pm := next.(ui.Model)
	if cmd != nil || !ui.SettingsOpen(pm) || !strings.Contains(pm.View(), "no editor") {
		t.Errorf("enter on edit config without an editor: cmd %v, open %v, view:\n%s", cmd != nil, ui.SettingsOpen(pm), pm.View())
	}
}

func TestModel_Update_SettingsThemePicker(t *testing.T) {
	t.Parallel()
	var saved []string
	m := ui.WithSaveTheme(settingsModel(t, fakerun.New()), func(name string) error {
		saved = append(saved, name)
		return nil
	})
	m = press(t, m, "j", "j", "enter") // theme entry -> picker
	view := m.View()
	if !lineHas(view, "› ", "● ", "gruvbox") {
		t.Fatalf("View() picker should mark and highlight the current theme:\n%s", view)
	}
	for _, name := range []string{"mocha", "tokyonight", "nord"} {
		if !lineHas(view, "○ ", name) {
			t.Errorf("View() picker missing %q:\n%s", name, view)
		}
	}
	// j, enter: applied and persisted through the existing hook.
	m = press(t, m, "j")
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("enter on mocha cmd = nil, want the save")
	}
	m = update(t, m, cmd())
	if got := ui.ThemeName(m); got != "mocha" {
		t.Errorf("ThemeName() = %q, want mocha", got)
	}
	if !reflect.DeepEqual(saved, []string{"mocha"}) {
		t.Errorf("saved themes = %v, want [mocha]", saved)
	}
	if view := m.View(); !lineHas(view, "● ", "mocha") || !strings.Contains(view, "theme: mocha") {
		t.Errorf("View() after applying should mark mocha and say so:\n%s", view)
	}
	// esc backs out to the menu, on the theme entry; esc again closes.
	m = press(t, m, "esc")
	if !ui.SettingsOpen(m) || !strings.Contains(m.View(), "› theme") {
		t.Errorf("View() after esc from the picker should be the menu on theme:\n%s", m.View())
	}
	if m = press(t, m, "esc"); ui.SettingsOpen(m) {
		t.Errorf("SettingsOpen() = true after the second esc")
	}
}

func TestModel_Update_SettingsAddConnection(t *testing.T) {
	t.Parallel()
	var saved []ui.AdhocEntry
	m := ui.WithSaveHost(settingsModel(t, fakerun.New()), func(e ui.AdhocEntry) error {
		saved = append(saved, e)
		return nil
	})
	m = press(t, m, "G", "enter") // add new connection
	if ui.SettingsOpen(m) || !ui.Prompting(m) || !strings.Contains(m.View(), "new connection:") {
		t.Fatalf("enter on add new connection: open %v, prompting %v, view:\n%s", ui.SettingsOpen(m), ui.Prompting(m), m.View())
	}
	// A bad target stays in the prompt.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-x")})
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !ui.Prompting(m) || ui.PromptingSave(m) {
		t.Fatalf("enter on a bad target moved on: cmd %v, prompting %v, save %v", cmd != nil, ui.Prompting(m), ui.PromptingSave(m))
	}
	// Save FIRST: the alias prompt follows, no ssh yet.
	m = press(t, m, "esc", "o", "G", "enter")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("root@192.0.2.99:2222")})
	next, cmd = m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil && len(filterMatches(cmd)) != 0 {
		t.Fatalf("enter on the target ran something before the save")
	}
	if !ui.PromptingSave(m) || !strings.Contains(m.View(), "save as:") {
		t.Fatalf("View() after the target should be the alias prompt:\n%s", m.View())
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
	next, save := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if save == nil {
		t.Fatalf("enter on the alias cmd = nil, want the save")
	}
	m = update(t, m, save())
	want := []ui.AdhocEntry{{Alias: "edge", User: "root", Host: "192.0.2.99", Port: "2222"}}
	if !reflect.DeepEqual(saved, want) {
		t.Errorf("saved = %+v, want %+v", saved, want)
	}
	// Then the offer: enter connects, esc skips.
	view := m.View()
	if !ui.Prompting(m) || !strings.Contains(view, "connect to edge now?") || !strings.Contains(view, "saved edge") {
		t.Fatalf("View() after the save should offer the connect:\n%s", view)
	}
	skip := press(t, m, "esc")
	if ui.Prompting(skip) {
		t.Errorf("Prompting() = true after esc on the offer, want closed")
	}
	next, connect := m.Update(keyMsg("enter"))
	if connect == nil || ui.Prompting(next.(ui.Model)) {
		t.Errorf("enter on the offer: cmd %v, prompting %v; want the ssh handoff", connect != nil, ui.Prompting(next.(ui.Model)))
	}
}

func TestModel_Update_SettingsAddConnectionDeclined(t *testing.T) {
	t.Parallel()
	saved := 0
	m := ui.WithSaveHost(settingsModel(t, fakerun.New()), func(ui.AdhocEntry) error { saved++; return nil })
	m = press(t, m, "G", "enter")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("192.0.2.99")})
	m = press(t, m, "enter", "esc") // decline at the alias prompt
	if saved != 0 || ui.Prompting(m) || !strings.Contains(m.View(), "not saved") {
		t.Errorf("declining the alias: saved %d, prompting %v, view:\n%s", saved, ui.Prompting(m), m.View())
	}
	// The ad-hoc flow is unaffected afterwards: a saves after connect.
	m = press(t, m, "a")
	if view := m.View(); !strings.Contains(view, "connect:") {
		t.Errorf("View() after a should be the ad-hoc prompt:\n%s", view)
	}
}

func TestModel_View_HelpListsSettings(t *testing.T) {
	t.Parallel()
	// The short help runs out before o at any width; the full help's
	// first column (the keys the view exists to document) carries it.
	// No hosts and the panel closed, so nothing else says "settings".
	m := update(t, sized(t, nil), tea.WindowSizeMsg{Width: 100, Height: 30})
	if view := m.View(); strings.Contains(view, "settings") {
		t.Fatalf("View() says settings before ?:\n%s", view)
	}
	if view := press(t, m, "?").View(); !strings.Contains(view, "settings") {
		t.Errorf("View() full help missing the o entry:\n%s", view)
	}
}
