package ui_test

import (
	"errors"
	"fmt"
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
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The ControlMaster trio offer (SASASH.4.18, hybrid): an interactive-
// master action (r) failing with ssh's "No ControlPath specified" on a
// managed host opens a question that writes exactly the missing lines;
// an unmanaged host gets the e hint. Tunnels are their own processes and
// never raise it.

const noControlPath = "No ControlPath specified for \"-O\" command\r\n"

// controlModel is a polled model whose ubuntu is managed (HasMeta) and
// whose r (ssh -O exit) fails with the No-ControlPath reason.
func controlModel(t *testing.T, managed bool, inspect func(string) (ui.ControlStatus, error), enable func(string) ([]string, error)) (ui.Model, *fakerun.Fake) {
	t.Helper()
	hosts := tunnelHosts()
	hosts[0].HasMeta = managed
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").
		Stub(fakerun.Reply{Result: run.Result{Stderr: []byte(noControlPath), Code: 255}}, "ssh", "-O", "exit", "ubuntu")
	d := fakedial.New()
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d), InspectControl: inspect, EnableControl: enable})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	m = ui.StaticCursor(m)
	next, cmd := m.Update(keyMsg("r"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(r) cmd = nil, want the exit")
	}
	return update(t, m, cmd()), r
}

func TestModel_Update_NoControlPathOffersTheTrio(t *testing.T) {
	t.Parallel()
	trio := []string{"ControlMaster auto", "ControlPath ~/.ssh/cm/%C", "ControlPersist 30m"}
	all := func(string) (ui.ControlStatus, error) { return ui.ControlStatus{Missing: trio}, nil }
	t.Run("managed host: the missing lines, enter writes them", func(t *testing.T) {
		t.Parallel()
		var enabled []string
		m, r := controlModel(t, true, all,
			func(alias string) ([]string, error) { enabled = append(enabled, alias); return trio, nil })
		view := m.View()
		for _, want := range []string{"enable master reuse on ubuntu? enter writes · esc closes", "No ControlPath specified", "enable master reuse for ubuntu", "  ControlMaster auto", "  ControlPath ~/.ssh/cm/%C", "  ControlPersist 30m", "nothing else is touched"} {
			if !strings.Contains(view, want) {
				t.Errorf("View() missing %q:\n%s", want, view)
			}
		}
		lines := strings.Split(view, "\n")
		if foot := lines[len(lines)-1]; !strings.Contains(foot, "enter write it") || !strings.Contains(foot, "esc cancel") {
			t.Errorf("footer = %q, want write it / cancel", foot)
		}
		// esc writes nothing, and says so in the write's own words.
		if c := press(t, m, "esc"); ui.PopupOpen(c) || len(enabled) != 0 || !strings.Contains(c.View(), "cancelled — nothing written") {
			t.Errorf("esc should close and write nothing (popup %v, enabled %v):\n%s", ui.PopupOpen(c), enabled, c.View())
		}
		// enter writes, and the outcome says what was written.
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd == nil || ui.PopupOpen(m) || !strings.Contains(m.View(), "enabling master reuse on ubuntu") {
			t.Fatalf("enter should start the write (cmd %v, popup %v):\n%s", cmd != nil, ui.PopupOpen(m), m.View())
		}
		before := len(r.CallKeys())
		m = update(t, m, cmd())
		if len(enabled) != 1 || enabled[0] != "ubuntu" || len(r.CallKeys()) != before {
			t.Errorf("enter should call the writer once for ubuntu and run no ssh (enabled %v)", enabled)
		}
		if view := m.View(); !strings.Contains(view, "reuse enabled on ubuntu — connect, then r (ControlMaster auto, ControlPath ~/.ssh/cm/%C, ControlPersist 30m written)") {
			t.Errorf("View() after the write should lead with the instruction and name the lines:\n%s", view)
		}
	})
	t.Run("the question's title and the outcome fit 80 columns", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true, all, func(string) ([]string, error) { return trio, nil })
		m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
		if inner := strings.Join(boxLines(m.View()), "\n"); !strings.Contains(inner, "enable master reuse on ubuntu? enter writes · esc closes") {
			t.Errorf("title cut at 80 columns:\n%s", m.View())
		}
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		if !strings.Contains(m.View(), "reuse enabled on ubuntu — connect, then r") {
			t.Errorf("the instruction should survive 80 columns:\n%s", m.View())
		}
	})
	t.Run("a pinned variant is refused: the master rides the parent", func(t *testing.T) {
		t.Parallel()
		// The hook decides from the file (alias form + the parent's path
		// names); the UI says so and writes nothing — enter only closes.
		var writes int
		m, _ := controlModel(t, true,
			func(string) (ui.ControlStatus, error) {
				return ui.ControlStatus{}, &ui.VariantError{Parent: "ubuntu", Path: "lan"}
			},
			func(string) ([]string, error) { writes++; return trio, nil })
		view := m.View()
		text := boxText(view)
		if !ui.PopupOpen(m) || !strings.Contains(text, "ubuntu's pinned variant for path lan") || !strings.Contains(text, "the master rides the parent — use ubuntu") || strings.Contains(view, "enter writes") {
			t.Errorf("a variant should be refused and pointed at its parent:\n%s", view)
		}
		if c := press(t, m, "enter"); ui.PopupOpen(c) || writes != 0 {
			t.Errorf("enter should only close (popup %v, writes %d)", ui.PopupOpen(c), writes)
		}
	})
	t.Run("a hostile value is said, never overridden", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			ctrl  ui.ControlStatus
			want  string
			offer bool
		}{
			{name: "ControlPath none", ctrl: ui.ControlStatus{Other: []string{"ControlPath none"}}, want: "the block sets ControlPath none"},
			{name: "a valueless ControlPath", ctrl: ui.ControlStatus{Other: []string{"ControlPath"}}, want: "the block sets an empty ControlPath"},
			{name: "a quoted none is none to ssh", ctrl: ui.ControlStatus{Other: []string{"ControlPath \"none\""}}, want: "the block sets ControlPath \"none\""},
			{name: "ControlMaster no with the rest missing", ctrl: ui.ControlStatus{Missing: []string{"ControlPath ~/.ssh/cm/%C", "ControlPersist 30m"}, Other: []string{"ControlMaster no"}}, want: "the block sets ControlMaster no"},
			{name: "another socket path is fine", ctrl: ui.ControlStatus{Missing: []string{"ControlPersist 30m"}, Other: []string{"ControlPath ~/.ssh/other/%C"}}, want: "  ControlPersist 30m", offer: true},
			{name: "ask starts a master", ctrl: ui.ControlStatus{Missing: []string{"ControlPersist 30m"}, Other: []string{"ControlMaster ask"}}, want: "  ControlPersist 30m", offer: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				m, _ := controlModel(t, true, func(string) (ui.ControlStatus, error) { return tt.ctrl, nil }, func(string) ([]string, error) { return nil, nil })
				view := m.View()
				if !strings.Contains(view, tt.want) || strings.Contains(view, "enter writes") != tt.offer {
					t.Errorf("View() want %q, offer %v:\n%s", tt.want, tt.offer, view)
				}
				if !tt.offer && !strings.Contains(view, "change it by hand (e)") {
					t.Errorf("a hostile value should point at e:\n%s", view)
				}
			})
		}
	})
	t.Run("@sasash meta outside sasash.conf gets the e hint", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true,
			func(string) (ui.ControlStatus, error) {
				return ui.ControlStatus{}, errors.New("host \"ubuntu\": " + ui.ErrNotManaged.Error())
			},
			func(string) ([]string, error) { return nil, nil })
		// errors.Is needs the wrapped sentinel: mirror setup's wrapping.
		m2, _ := controlModel(t, true,
			func(string) (ui.ControlStatus, error) { return ui.ControlStatus{}, wrapNotManaged() },
			func(string) ([]string, error) { return nil, nil })
		if view := m2.View(); !strings.Contains(view, "not in sasash.conf") || !strings.Contains(view, "e to add them by hand") || strings.Contains(view, "cannot read the block") {
			t.Errorf("meta outside sasash.conf should get the e hint:\n%s", view)
		}
		if view := m.View(); !strings.Contains(view, "cannot read the block") {
			t.Errorf("an unwrapped error is still a read failure:\n%s", view)
		}
	})
	t.Run("only what is missing is offered", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true,
			func(string) (ui.ControlStatus, error) {
				return ui.ControlStatus{Missing: []string{"ControlPersist 30m"}}, nil
			},
			func(string) ([]string, error) { return nil, nil })
		if view := m.View(); !strings.Contains(view, "  ControlPersist 30m") || strings.Contains(view, "  ControlMaster auto") {
			t.Errorf("View() should offer only the missing line:\n%s", view)
		}
	})
	t.Run("a complete block is not a question", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true,
			func(string) (ui.ControlStatus, error) { return ui.ControlStatus{}, nil },
			func(string) ([]string, error) { return nil, nil })
		lines := strings.Split(m.View(), "\n")
		if !ui.PopupOpen(m) || !strings.Contains(m.View(), "already carries the ControlMaster trio") || !strings.Contains(lines[len(lines)-1], "esc/enter close") {
			t.Errorf("a complete block should explain, not ask:\n%s", m.View())
		}
	})
	t.Run("a write failure is said", func(t *testing.T) {
		t.Parallel()
		m, _ := controlModel(t, true, all,
			func(string) ([]string, error) { return nil, errors.New("disk full") })
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		if !strings.Contains(m.View(), "master reuse not enabled on ubuntu: disk full") {
			t.Errorf("View() should report the failed write:\n%s", m.View())
		}
	})
	t.Run("an unmanaged host gets the e hint, no question", func(t *testing.T) {
		t.Parallel()
		var enabled int
		m, _ := controlModel(t, false, all,
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
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New()
		sp := fakespawn.New().Dial(d).Exits("bind: Address already in use\n", tunnel.SpawnArgv("ubuntu", "8080:localhost:80")...)
		eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
		m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng, InspectControl: all})
		m = ui.StaticCursor(feed(t, update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}), m.Init()))
		m, cmd := typeSpec(t, m, "8080:localhost:80")
		m = update(t, m, cmd())
		if !ui.PopupOpen(m) || strings.Contains(m.View(), "enable master reuse") || !strings.Contains(m.View(), "add failed on ubuntu") {
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

// wrapNotManaged mirrors the cmd hook's wrapping of the sentinel.
func wrapNotManaged() error { return fmt.Errorf("host %q: %w", "ubuntu", ui.ErrNotManaged) }

// boxText is the popup's rows as one line, so a sentence the box wrapped
// can be matched whole.
func boxText(view string) string {
	var parts []string
	for _, l := range boxLines(view) {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}
