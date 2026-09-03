package ui_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// failedForward is a model right after a forward action that ssh refused
// with a multi-line diagnosis: the popup is open over the tunnels pane.
func failedForward(t *testing.T) (ui.Model, *fakerun.Fake) {
	t.Helper()
	stderr := "ssh: connect to host 192.0.2.10 port 22: Connection refused\nkex_exchange_identification: read: Connection reset by peer\nBanner line three"
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").
		Stub(fakerun.Reply{Result: run.Result{Stderr: []byte(stderr), Code: 255}}, "ssh", "-O", "forward", "-L", specWeb0, "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", specWeb1, "ubuntu")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l")
	next, cmd := m.Update(keyMsg(" "))
	m = update(t, next.(ui.Model), cmd())
	return m, r
}

func TestModel_Update_PopupOwnsTheKeyboard(t *testing.T) {
	t.Parallel()
	m, r := failedForward(t)
	if !ui.PopupOpen(m) || !strings.Contains(ui.PopupTitle(m), "failed on ubuntu") {
		t.Fatalf("a refused forward should open the popup: open %v, title %q", ui.PopupOpen(m), ui.PopupTitle(m))
	}
	view := m.View()
	// The whole reason, every line, plus the argv it ran; the ssh line
	// is gone from the body and so is the tunnels pane.
	for _, want := range []string{"! ", "failed on ubuntu", "Connection refused", "kex_exchange_identification", "Banner line three", "ssh -O forward -L " + specWeb0 + " ubuntu", "esc/enter close"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() popup missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "$ ssh") {
		t.Errorf("View() popup should replace the command line:\n%s", view)
	}
	// Action keys are inert while it is open.
	before := len(r.CallKeys())
	for _, k := range []string{"S", "H", "o", "/", "q", "a", "+", "x", "l", "t", "v", "p", "y", "c", " "} {
		next, cmd := m.Update(keyMsg(k))
		if cmd != nil || !ui.PopupOpen(next.(ui.Model)) {
			t.Errorf("key %q under the popup: cmd %v, open %v", k, cmd != nil, ui.PopupOpen(next.(ui.Model)))
		}
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("calls under the popup = %v, want none", got)
	}
	// esc and enter close it; the one-line outcome is still on the line.
	for _, k := range []string{"esc", "enter"} {
		closed := press(t, m, k)
		if ui.PopupOpen(closed) || !strings.Contains(closed.View(), "web: ssh: connect to host 192.0.2.10 port 22: Connection refused") {
			t.Errorf("%s should close the popup and leave the notice: open %v:\n%s", k, ui.PopupOpen(closed), closed.View())
		}
	}
	// ctrl+c still quits.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Errorf("ctrl+c under the popup should quit")
	}
}

func TestModel_Update_PopupNewestWinsAndProbesLand(t *testing.T) {
	t.Parallel()
	m, _ := failedForward(t)
	first := ui.PopupTitle(m)
	// A status poll lands behind the popup: the pane state updates, the
	// popup stays.
	next, _ := m.Update(ui.TunnelStatus("ubuntu", tunnel.HostStatus{Host: "ubuntu", Master: true}, nil))
	m = next.(ui.Model)
	if !ui.PopupOpen(m) || ui.PopupTitle(m) != first {
		t.Errorf("a status poll should not touch the popup: open %v, title %q", ui.PopupOpen(m), ui.PopupTitle(m))
	}
	if closed := press(t, m, "esc"); !strings.Contains(closed.View(), "● master up") {
		t.Errorf("the poll that landed behind the popup should show once it closes:\n%s", closed.View())
	}
	// A second attention event replaces the open one and names it.
	m = update(t, m, ui.TunnelStatus("spare", tunnel.HostStatus{Host: "spare", CorruptState: true}, nil))
	if !ui.PopupOpen(m) || !strings.Contains(ui.PopupTitle(m), "cache unreadable for spare") || !strings.Contains(m.View(), "(replaced: "+first+")") {
		t.Errorf("the newer popup should name the one it replaced: title %q:\n%s", ui.PopupTitle(m), m.View())
	}
}

func TestModel_View_PopupScrolls(t *testing.T) {
	t.Parallel()
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("warning line %02d", i+1)
	}
	m := ui.WithEditor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()), []string{"vi"}, "", "/tmp/x/sasash.conf")
	var warnings []error
	for _, l := range lines {
		warnings = append(warnings, errors.New(l))
	}
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) { return tunnelHosts(), warnings, nil })
	next, reload := m.Update(ui.EditorExit(nil))
	next, _ = next.(ui.Model).Update(reload())
	m = next.(ui.Model)
	if !ui.PopupOpen(m) || ui.PopupTitle(m) != "config: 40 warnings" {
		t.Fatalf("40 warnings should open the popup: open %v, title %q", ui.PopupOpen(m), ui.PopupTitle(m))
	}
	view := m.View()
	// The popup's rows carry a three-space indent; the bottom-line notice
	// repeats the first warning without it.
	if !strings.Contains(view, "   warning line 01") || strings.Contains(view, "   warning line 40") || !strings.Contains(view, "of 40 · j/k scroll") {
		t.Errorf("View() should show the top of a long popup with a scroll hint:\n%s", view)
	}
	if h := strings.Count(view, "\n") + 1; h != 30 {
		t.Errorf("View() is %d lines with the popup open, want the frame's 30", h)
	}
	m = press(t, m, "G")
	if view := m.View(); !strings.Contains(view, "   warning line 40") || strings.Contains(view, "   warning line 01") {
		t.Errorf("View() after G should show the end:\n%s", view)
	}
	m = press(t, m, "g", "j", "j")
	if view := m.View(); !strings.Contains(view, "   warning line 03") || strings.Contains(view, "   warning line 01") {
		t.Errorf("View() after g j j should start at line 3:\n%s", view)
	}
	if view := press(t, m, "ctrl+u", "k", "k", "k").View(); !strings.Contains(view, "   warning line 01") {
		t.Errorf("View() cannot scroll above the top:\n%s", view)
	}
}

func TestModel_Update_PopupRouting(t *testing.T) {
	t.Parallel()
	t.Run("one reload warning stays on the line", func(t *testing.T) {
		t.Parallel()
		m := ui.WithEditor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()), []string{"vi"}, "", "/tmp/x/sasash.conf")
		m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
			return tunnelHosts(), []error{errors.New("host \"x\": bad meta")}, nil
		})
		next, reload := m.Update(ui.EditorExit(nil))
		next, _ = next.(ui.Model).Update(reload())
		m = next.(ui.Model)
		if ui.PopupOpen(m) || !strings.Contains(m.View(), "config: host \"x\": bad meta") {
			t.Errorf("one warning: popup %v:\n%s", ui.PopupOpen(m), m.View())
		}
	})
	t.Run("two reload warnings list both", func(t *testing.T) {
		t.Parallel()
		m := ui.WithEditor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()), []string{"vi"}, "", "/tmp/x/sasash.conf")
		m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
			return tunnelHosts(), []error{errors.New("host \"x\": bad meta"), errors.New("host \"y\": duplicate path")}, nil
		})
		next, reload := m.Update(ui.EditorExit(nil))
		next, _ = next.(ui.Model).Update(reload())
		m = next.(ui.Model)
		view := m.View()
		if !ui.PopupOpen(m) || !strings.Contains(view, "config: 2 warnings") || !strings.Contains(view, "bad meta") || !strings.Contains(view, "duplicate path") {
			t.Errorf("two warnings should list both in the popup:\n%s", view)
		}
	})
	t.Run("spawn failure carries the argv it ran", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte(`{"error":{"code":"no_server","message":"herdr server is not running"}}`), Code: 1}}, "herdr", "tab", "create", "--label", "web", "--no-focus")
		m := spawnModel(t, r, herdrCfg("tab"), nil)
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		view := m.View()
		if !ui.PopupOpen(m) || !strings.Contains(view, "spawn failed for web") || !strings.Contains(view, "ran: herdr tab create --label web --no-focus") || !strings.Contains(view, "herdr server is not running") {
			t.Errorf("a failed spawn should open the popup with its argv:\n%s", view)
		}
		if closed := press(t, m, "esc"); !strings.Contains(closed.View(), "herdr tab create failed: herdr server is not running") {
			t.Errorf("the one-line outcome should remain after closing:\n%s", closed.View())
		}
	})
	t.Run("ad-hoc -N failure shows the argv, the reason and the hint", func(t *testing.T) {
		t.Parallel()
		argv := []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "8080:localhost:80", "root@192.0.2.99"}
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("root@192.0.2.99: Permission denied (publickey,password).\n"), Code: 255}}, argv...)
		m := ui.StaticCursor(polled(t, r, fakedial.New()))
		m = press(t, m, "a")
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L 8080:localhost:80 root@192.0.2.99")})
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		view := m.View()
		for _, want := range []string{"! ssh -fN failed", strings.Join(argv, " "), "Permission denied", "keys or an agent"} {
			if !strings.Contains(view, want) {
				t.Errorf("View() popup missing %q:\n%s", want, view)
			}
		}
	})
	t.Run("corrupt caches open once per host with the file's place", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New()
		hosts := tunnelHosts()
		m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d), StateDir: "/home/u/.local/state/sasash"})
		m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
		m = feed(t, m, m.Init())
		m = update(t, m, ui.TunnelStatus("ubuntu", tunnel.HostStatus{Host: "ubuntu", CorruptState: true, CorruptStats: true}, nil))
		view := m.View()
		for _, want := range []string{"cache unreadable for ubuntu", "ubuntu.json under /home/u/.local/state/sasash", "ubuntu.stats.json under /home/u/.local/state/sasash", "delete the file to reset it"} {
			if !strings.Contains(view, want) {
				t.Errorf("View() popup missing %q:\n%s", want, view)
			}
		}
		// The next poll with the same problem does not reopen it; another
		// host's does.
		m = press(t, m, "esc")
		m = update(t, m, ui.TunnelStatus("ubuntu", tunnel.HostStatus{Host: "ubuntu", CorruptState: true}, nil))
		if ui.PopupOpen(m) {
			t.Errorf("the corrupt-cache popup should open once per host")
		}
		m = update(t, m, ui.TunnelStatus("spare", tunnel.HostStatus{Host: "spare", CorruptStats: true}, nil))
		if !ui.PopupOpen(m) || !strings.Contains(ui.PopupTitle(m), "spare") {
			t.Errorf("another host's corrupt cache should open the popup: open %v, title %q", ui.PopupOpen(m), ui.PopupTitle(m))
		}
		// A status error is not a cache problem.
		m = press(t, m, "esc")
		m = update(t, m, ui.TunnelStatus("bare", tunnel.HostStatus{Host: "bare", CorruptState: true}, errors.New("ssh: cannot run")))
		if ui.PopupOpen(m) {
			t.Errorf("a status error should not open the cache popup")
		}
	})
	t.Run("edit-forward failure shows the refusal whole", func(t *testing.T) {
		t.Parallel()
		var calls [][]string
		r := fakerun.New().
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("mux_client_forward: forwarding request failed\nControl socket connect(/x/cm/abc): No such file or directory"), Code: 255}}, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu")
		m := editModel(t, r, &calls, nil)
		m = press(t, m, "l", "E")
		for range specWeb0 {
			m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
		}
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3000:localhost:9000")})
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		view := m.View()
		if !ui.PopupOpen(m) || !strings.Contains(view, "edit forward failed on ubuntu") || !strings.Contains(view, "mux_client_forward") || !strings.Contains(view, "No such file or directory") {
			t.Errorf("a refused cancel during E should open the popup with the whole reason:\n%s", view)
		}
	})
	t.Run("routine one-liners stay on the line", func(t *testing.T) {
		t.Parallel()
		m := polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New())
		m = press(t, m, "t")
		if ui.PopupOpen(m) || !strings.Contains(m.View(), "theme: ") {
			t.Errorf("a theme change is a one-liner: popup %v:\n%s", ui.PopupOpen(m), m.View())
		}
	})
	t.Run("a conflict offer yields to the popup", func(t *testing.T) {
		t.Parallel()
		m, _ := failedForward(t)
		if ui.Prompting(m) {
			t.Fatalf("prompting under the popup")
		}
		// The popup is an owner: the guard's table case lives in
		// forward_test; here the help line says the popup's keys.
		if view := m.View(); !strings.Contains(view, "esc/enter close") || strings.Contains(view, "toggle set") {
			t.Errorf("View() help under the popup:\n%s", view)
		}
	})
}

func TestModel_View_PopupWrapsLongLines(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 40)
	m := ui.WithEditor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()), []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
		return tunnelHosts(), []error{errors.New(long), errors.New("second")}, nil
	})
	m = update(t, m, tea.WindowSizeMsg{Width: 60, Height: 24})
	next, reload := m.Update(ui.EditorExit(nil))
	next, _ = next.(ui.Model).Update(reload())
	m = next.(ui.Model)
	view := m.View()
	// Every word of the long line is in the popup (the bottom-line
	// notice repeats its head, so the count is at least 40).
	if strings.Count(view, "word") < 40 {
		t.Errorf("View() should wrap the long line, keeping every word (got %d):\n%s", strings.Count(view, "word"), view)
	}
	for _, l := range strings.Split(view, "\n") {
		if n := len([]rune(l)); n > 60 {
			t.Errorf("View() line %d runes wide at 60 cols: %q", n, l)
		}
	}
	if !reflect.DeepEqual([]string{"config: 2 warnings"}, []string{ui.PopupTitle(m)}) {
		t.Errorf("title = %q", ui.PopupTitle(m))
	}
}
