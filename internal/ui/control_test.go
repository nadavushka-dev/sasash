package ui_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The ControlMaster trio offer (SASASH.4.18): a tunnel action failing
// with ssh's "No ControlPath specified" on a managed host opens a
// question that writes exactly the missing lines; an unmanaged host
// gets the e hint.

const noControlPath = "No ControlPath specified for \"-O\" command\r\n"

// controlModel is a polled model whose ubuntu is managed (HasMeta) and
// whose + on 8080:localhost:80 fails with the No-ControlPath reason.
func controlModel(t *testing.T, managed bool, missing func(string) ([]string, error), enable func(string) ([]string, error)) (ui.Model, *fakerun.Fake) {
	t.Helper()
	hosts := tunnelHosts()
	hosts[0].HasMeta = managed
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").
		Stub(fakerun.Reply{Result: run.Result{Stderr: []byte(noControlPath), Code: 255}}, "ssh", "-O", "forward", "-L", "8080:localhost:80", "ubuntu")
	d := fakedial.New()
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d), MissingControl: missing, EnableControl: enable})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	m = ui.StaticCursor(m)
	m, cmd := typeSpec(t, m, "8080:localhost:80")
	return update(t, m, cmd()), r
}

func TestModel_Update_NoControlPathOffersTheTrio(t *testing.T) {
	t.Parallel()
	trio := []string{"ControlMaster auto", "ControlPath ~/.ssh/cm/%C", "ControlPersist 30m"}
	t.Run("managed host: the missing lines, enter writes them", func(t *testing.T) {
		t.Parallel()
		var enabled []string
		m, r := controlModel(t, true,
			func(alias string) ([]string, error) { return trio, nil },
			func(alias string) ([]string, error) { enabled = append(enabled, alias); return trio, nil })
		view := m.View()
		for _, want := range []string{"add failed on ubuntu — no ControlPath: enable tunnels? enter writes · esc closes", "No ControlPath specified", "enable tunnels for ubuntu", "  ControlMaster auto", "  ControlPath ~/.ssh/cm/%C", "  ControlPersist 30m", "nothing else is touched"} {
			if !strings.Contains(view, want) {
				t.Errorf("View() missing %q:\n%s", want, view)
			}
		}
		lines := strings.Split(view, "\n")
		if foot := lines[len(lines)-1]; !strings.Contains(foot, "enter write it") || !strings.Contains(foot, "esc cancel") {
			t.Errorf("footer = %q, want write it / cancel", foot)
		}
		// esc writes nothing.
		if c := press(t, m, "esc"); ui.PopupOpen(c) || len(enabled) != 0 {
			t.Errorf("esc should close and write nothing (popup %v, enabled %v)", ui.PopupOpen(c), enabled)
		}
		// enter writes, and the outcome says what was written.
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd == nil || ui.PopupOpen(m) || !strings.Contains(m.View(), "enabling tunnels on ubuntu") {
			t.Fatalf("enter should start the write (cmd %v, popup %v):\n%s", cmd != nil, ui.PopupOpen(m), m.View())
		}
		before := len(r.CallKeys())
		m = update(t, m, cmd())
		if len(enabled) != 1 || enabled[0] != "ubuntu" || len(r.CallKeys()) != before {
			t.Errorf("enter should call the writer once for ubuntu and run no ssh (enabled %v)", enabled)
		}
		if view := m.View(); !strings.Contains(view, "tunnels enabled on ubuntu — ControlMaster auto, ControlPath ~/.ssh/cm/%C, ControlPersist 30m written; space or + again") {
			t.Errorf("View() after the write should name the lines:\n%s", view)
		}
	})
	t.Run("only what is missing is offered", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true,
			func(string) ([]string, error) { return []string{"ControlPersist 30m"}, nil },
			func(string) ([]string, error) { return nil, nil })
		if view := m.View(); !strings.Contains(view, "  ControlPersist 30m") || strings.Contains(view, "  ControlMaster auto") {
			t.Errorf("View() should offer only the missing line:\n%s", view)
		}
	})
	t.Run("a complete block is not a question", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true,
			func(string) ([]string, error) { return nil, nil },
			func(string) ([]string, error) { return nil, nil })
		lines := strings.Split(m.View(), "\n")
		if !ui.PopupOpen(m) || !strings.Contains(m.View(), "already carries the ControlMaster trio") || !strings.Contains(lines[len(lines)-1], "esc/enter close") {
			t.Errorf("a complete block should explain, not ask:\n%s", m.View())
		}
	})
	t.Run("a write failure is said", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true,
			func(string) ([]string, error) { return trio, nil },
			func(string) ([]string, error) { return nil, errors.New("disk full") })
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		if !strings.Contains(m.View(), "tunnels not enabled on ubuntu: disk full") {
			t.Errorf("View() should report the failed write:\n%s", m.View())
		}
	})
	t.Run("an unmanaged host gets the e hint, no question", func(t *testing.T) {
		t.Parallel()
		var enabled int
		m, _ := controlModel(t, false,
			func(string) ([]string, error) { return trio, nil },
			func(string) ([]string, error) { enabled++; return trio, nil })
		view := m.View()
		lines := strings.Split(view, "\n")
		if !ui.PopupOpen(m) || !strings.Contains(view, "not managed") || !strings.Contains(view, "e to add them by hand") || strings.Contains(view, "enter writes") || !strings.Contains(lines[len(lines)-1], "esc/enter close") {
			t.Errorf("an unmanaged host should get the hint:\n%s", view)
		}
		if c := press(t, m, "enter"); ui.PopupOpen(c) || enabled != 0 {
			t.Errorf("enter should just close (popup %v, writes %d)", ui.PopupOpen(c), enabled)
		}
	})
	t.Run("without the hooks the hint stands in", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true, nil, nil)
		if !strings.Contains(m.View(), "e to add them by hand") || strings.Contains(m.View(), "enter writes") {
			t.Errorf("no hooks: the hint, never a question:\n%s", m.View())
		}
	})
	t.Run("another failure is the plain popup", func(t *testing.T) {
		t.Parallel()
		hosts := tunnelHosts()
		hosts[0].HasMeta = true
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("bind: Address already in use\n"), Code: 255}}, "ssh", "-O", "forward", "-L", "8080:localhost:80", "ubuntu")
		d := fakedial.New()
		m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d), MissingControl: func(string) ([]string, error) { return []string{"x"}, nil }})
		m = ui.StaticCursor(feed(t, update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}), m.Init()))
		m, cmd := typeSpec(t, m, "8080:localhost:80")
		m = update(t, m, cmd())
		if !ui.PopupOpen(m) || strings.Contains(m.View(), "enable tunnels") || !strings.Contains(m.View(), "add failed on ubuntu") {
			t.Errorf("an unrelated failure should not offer the trio:\n%s", m.View())
		}
	})
}

func TestModel_Update_SaveSaysWhenTheControlDirIsMissing(t *testing.T) {
	t.Parallel()
	save := func(ui.AdhocEntry) error { return nil }
	saveAs := func(t *testing.T, m ui.Model) ui.Model {
		t.Helper()
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("root@192.0.2.99")})
		next, _ := m.Update(keyMsg("enter"))
		next, _ = next.(ui.Model).Update(ui.AdhocExit(nil))
		m = update(t, next.(ui.Model), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
		next, cmd := m.Update(keyMsg("enter"))
		return update(t, next.(ui.Model), cmd())
	}
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		home := homeWith(t, true, true, false)
		m := saveAs(t, ui.WithHome(ui.WithSaveHost(update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 120, Height: 24}), save), home))
		if view := m.View(); !strings.Contains(view, "saved edge — ~/.ssh/cm is missing: o → create config file") {
			t.Errorf("View() should say the control directory is missing:\n%s", view)
		}
	})
	t.Run("present", func(t *testing.T) {
		t.Parallel()
		home := homeWith(t, true, true, true)
		if err := os.MkdirAll(filepath.Join(home, ".ssh", "cm"), 0o700); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
		m := saveAs(t, ui.WithHome(ui.WithSaveHost(update(t, probedModel(t, fakerun.New()), tea.WindowSizeMsg{Width: 120, Height: 24}), save), home))
		if view := m.View(); !strings.Contains(view, "saved edge") || strings.Contains(view, "cm is missing") {
			t.Errorf("View() should not warn when the directory exists:\n%s", view)
		}
	})
}
