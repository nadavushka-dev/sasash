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
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// failedForward is a model right after a forward action that ssh refused
// with a multi-line diagnosis: the popup is open over the tunnels pane.
func failedForward(t *testing.T) (ui.Model, *fakerun.Fake) {
	t.Helper()
	stderr := "ssh: connect to host 192.0.2.10 port 22: Connection refused\nkex_exchange_identification: read: Connection reset by peer\nBanner line three"
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d).Exits(stderr, tunnel.SpawnArgv("ubuntu", specWeb0)...)
	m := polledSpawn(t, r, sp, d, t.TempDir())
	m = press(t, m, "l")
	next, cmd := m.Update(keyMsg(" "))
	m = update(t, next.(ui.Model), cmd())
	return m, r
}

// boxLines is the popup box's inner lines — what sits between its side
// borders, trailing padding dropped: the title row, the blank, the rows
// with their three-space indent, the bottom line.
func boxLines(view string) []string {
	var out []string
	for _, l := range strings.Split(view, "\n") {
		i, j := strings.Index(l, "│"), strings.LastIndex(l, "│")
		if i < 0 || j <= i {
			continue
		}
		out = append(out, strings.TrimRight(l[i+len("│"):j], " "))
	}
	return out
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
	for _, want := range []string{"! ", "failed on ubuntu", "Connection refused", "kex_exchange_identification", "Banner line three", spawnArgv("ubuntu", specWeb0), "esc/enter close"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() popup missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "$ ssh") {
		t.Errorf("View() popup should replace the command line:\n%s", view)
	}
	// Action keys are inert while it is open.
	before := len(r.CallKeys())
	for _, k := range []string{"S", "H", "o", "q", "a", "+", "x", "l", "t", "v", "p", "y", "c", " "} {
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
	if closed := press(t, m, "esc"); !ui.TunnelStatusKnown(closed, "ubuntu") {
		t.Errorf("the poll that landed behind the popup should be retained once it closes:\n%s", closed.View())
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
	// Exactly room rows of content are shown: the frame is 30 tall, the
	// help one line, so the body has 26 rows; the box's border, title,
	// blank and bottom line take five, leaving 21 of content.
	rows := 0
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "   warning line") {
			rows++
		}
	}
	if rows != 21 || !strings.Contains(view, "lines 1–21 of 40 · j/k scroll") {
		t.Errorf("View() should show 21 content rows with the exact hint (got %d):\n%s", rows, view)
	}
	m = press(t, m, "G")
	if view := m.View(); !strings.Contains(view, "   warning line 40") || !strings.Contains(view, "   warning line 20") || strings.Contains(view, "   warning line 19") || !strings.Contains(view, "lines 20–40 of 40") {
		t.Errorf("View() after G should show exactly the last 21 rows:\n%s", view)
	}
	// One k after G moves one row: the offset is clamped when written,
	// never parked past the end.
	if view := press(t, m, "G", "k").View(); !strings.Contains(view, "   warning line 19") || strings.Contains(view, "   warning line 40") {
		t.Errorf("View() after G k should move up one row:\n%s", view)
	}
	if view := press(t, m, "G", "j", "j", "j", "k").View(); !strings.Contains(view, "   warning line 19") {
		t.Errorf("View() after over-scrolling down, k should still move:\n%s", view)
	}
	// A frame that grows after G: the offset is re-clamped on the resize,
	// so the window shows a full screen and the very next k moves.
	small := press(t, update(t, m, tea.WindowSizeMsg{Width: 100, Height: 14}), "G")
	if view := small.View(); !strings.Contains(view, "lines 36–40 of 40") {
		t.Fatalf("View() at height 14 after G:\n%s", view)
	}
	grown := update(t, small, tea.WindowSizeMsg{Width: 100, Height: 30})
	if view := grown.View(); !strings.Contains(view, "lines 20–40 of 40") {
		t.Errorf("View() after growing the frame should re-clamp to a full screen:\n%s", view)
	}
	if view := press(t, grown, "k").View(); !strings.Contains(view, "lines 19–39 of 40") {
		t.Errorf("View() one k after the resize should move one row:\n%s", view)
	}
	// The mirrored edge: half pages above the top park nothing; the next
	// j moves one row.
	if view := press(t, m, "g", "ctrl+u", "ctrl+u", "ctrl+u", "j").View(); !strings.Contains(view, "lines 2–22 of 40") {
		t.Errorf("View() j after over-scrolling up should move one row:\n%s", view)
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
		// A multiplexer step is not the window file's doing: no hint.
		if strings.Contains(view, "spawn-window") {
			t.Errorf("a herdr failure should not name the window file:\n%s", view)
		}
	})
	t.Run("window spawn failure names the file and is sanitized", func(t *testing.T) {
		t.Parallel()
		// The template is the user's file: its argv is shown cleaned, and
		// the popup says where the command comes from.
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("xterm: no display\n"), Code: 1}}, "xterm\x1b[2J", "-e", "ssh", "web")
		m := spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: "xterm\x1b[2J -e ssh {host}", WindowPath: "/home/u/.dotfiles/sasash/spawn-window"}, nil)
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		view := m.View()
		if !ui.PopupOpen(m) || !strings.Contains(view, "spawn failed for web") || !strings.Contains(view, "ran: xterm[2J -e ssh web") || !strings.Contains(view, "no display") || !strings.Contains(view, "comes from /home/u/.dotfiles/sasash/spawn-window") {
			t.Errorf("a failed window spawn should show the cleaned argv, the reason and the file:\n%s", view)
		}
		if strings.Contains(view, "\x1b") {
			t.Errorf("View() carries the escape:\n%s", view)
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
		// Unsaved and nowhere to save (4.17): the unmanaged question, then
		// enter proceeds with the raw run.
		m = press(t, m, "enter")
		if !ui.PopupOpen(m) || !strings.Contains(m.View(), "run an unmanaged tunnel?") {
			t.Fatalf("enter on an unsaved -N should ask first:\n%s", m.View())
		}
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
		m, sp := editModel(t, fakerun.New(), &calls, nil)
		sp.KillErr = errors.New("mux_client_forward: forwarding request failed\nControl socket connect(/x/cm/abc): No such file or directory")
		m = press(t, m, "l", "E")
		for range specWeb0 {
			m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
		}
		m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3000:localhost:9000")})
		next, cmd := m.Update(keyMsg("enter"))
		m = update(t, next.(ui.Model), cmd())
		view := m.View()
		if !ui.PopupOpen(m) || !strings.Contains(view, "edit forward failed on ubuntu") || !strings.Contains(view, "mux_client_forward") || !strings.Contains(view, "No such file or directory") {
			t.Errorf("a refused kill during E should open the popup with the whole reason:\n%s", view)
		}
	})
	t.Run("routine one-liners stay on the line", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		m := polled(t, r, fakedial.New())
		if m = press(t, m, "t"); ui.PopupOpen(m) || !strings.Contains(m.View(), "theme: ") {
			t.Errorf("a theme change is a one-liner: popup %v:\n%s", ui.PopupOpen(m), m.View())
		}
		if m = press(t, m, "y"); ui.PopupOpen(m) || !strings.Contains(m.View(), "nothing copied") {
			t.Errorf("a yank with nothing alive is a one-liner: popup %v:\n%s", ui.PopupOpen(m), m.View())
		}
		if m = press(t, m, "p"); ui.PopupOpen(m) {
			t.Errorf("a single-path no-op is a one-liner:\n%s", m.View())
		}
		m = press(t, m, "l")
		next, cmd := m.Update(keyMsg(" "))
		m = update(t, next.(ui.Model), cmd())
		if ui.PopupOpen(m) || !strings.Contains(m.View(), "web "+specWeb0+" up") {
			t.Errorf("a successful tunnel action is a one-liner: popup %v:\n%s", ui.PopupOpen(m), m.View())
		}
		// A successful -N through the engine on the selected host too.
		a := ui.StaticCursor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()))
		a = press(t, a, "a")
		a = update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N -L 8080:localhost:80")})
		next, cmd = a.Update(keyMsg("enter"))
		a = update(t, next.(ui.Model), cmd())
		if ui.PopupOpen(a) || !strings.Contains(a.View(), "session-only tunnel up: 8080:localhost:80 on ubuntu") {
			t.Errorf("a successful engine -N is a one-liner: popup %v:\n%s", ui.PopupOpen(a), a.View())
		}
		// An unmanaged one is not: the popup names the cleanup (4.17).
		argv := []string{"ssh", "-fN", "-o", "BatchMode=yes", "root@192.0.2.99"}
		u := ui.StaticCursor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, argv...), fakedial.New()))
		u = press(t, u, "a")
		u = update(t, u, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N root@192.0.2.99")})
		next, cmd = press(t, u, "enter").Update(keyMsg("enter"))
		u = update(t, next.(ui.Model), cmd())
		if !ui.PopupOpen(u) || !strings.Contains(u.View(), "unmanaged tunnel up") || !strings.Contains(u.View(), "pgrep") {
			t.Errorf("an unmanaged -N should say so in a popup: popup %v:\n%s", ui.PopupOpen(u), u.View())
		}
	})
	t.Run("the cache popup sets the one-line form and hides the command line", func(t *testing.T) {
		t.Parallel()
		m := polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New())
		m = update(t, m, ui.TunnelStatus("ubuntu", tunnel.HostStatus{Host: "ubuntu", CorruptState: true}, nil))
		view := m.View()
		if !ui.PopupOpen(m) || strings.Contains(view, "$ ssh") || !strings.Contains(view, "cache unreadable for ubuntu — see the tunnels pane") {
			t.Errorf("View() under the cache popup:\n%s", view)
		}
		if closed := press(t, m, "esc"); !strings.Contains(closed.View(), "cache unreadable for ubuntu — see the tunnels pane") {
			t.Errorf("closing should leave the one-line form:\n%s", closed.View())
		}
	})
	t.Run("control characters in stderr never reach the frame", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New()
		sp := fakespawn.New().Dial(d).Exits("mid\rcarriage	and \x1b[2J\x1b[H wipe", tunnel.SpawnArgv("ubuntu", specWeb0)...)
		m := polledSpawn(t, r, sp, d, t.TempDir())
		m = press(t, m, "l")
		next, cmd := m.Update(keyMsg(" "))
		m = update(t, next.(ui.Model), cmd())
		view := m.View()
		if strings.ContainsAny(view, "\r\t") || strings.Contains(view, "\x1b[2J") || !strings.Contains(view, "midcarriage and [2J[H wipe") {
			t.Errorf("View() should carry the reason with control characters dropped:\n%s", view)
		}
	})
	t.Run("control characters in config warnings, spawn and -N reasons never reach the frame", func(t *testing.T) {
		t.Parallel()
		dirty := "bad\rmeta\there \x1b[2J\x1b[H"
		m := ui.WithEditor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()), []string{"vi"}, "", "/tmp/x/sasash.conf")
		m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
			return tunnelHosts(), []error{errors.New("host \"a\": " + dirty), errors.New("host \"b\": " + dirty)}, nil
		})
		next, reload := m.Update(ui.EditorExit(nil))
		next, _ = next.(ui.Model).Update(reload())
		if view := next.(ui.Model).View(); strings.ContainsAny(view, "\r\t\x1b") || !strings.Contains(view, "badmeta here [2J[H") {
			t.Errorf("the reload popup and its line should be clean:\n%s", view)
		}
		// Spawn: the one-line form is clean like the popup.
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte(dirty), Code: 1}}, "herdr", "tab", "create", "--label", "web", "--no-focus")
		sp := spawnModel(t, r, herdrCfg("tab"), nil)
		next, cmd := sp.Update(keyMsg("enter"))
		sp = update(t, next.(ui.Model), cmd())
		if view := press(t, sp, "esc").View(); strings.ContainsAny(view, "\r\t\x1b") {
			t.Errorf("the spawn one-liner should be clean:\n%s", view)
		}
		// Ad-hoc -N: same.
		argv := []string{"ssh", "-fN", "-o", "BatchMode=yes", "root@192.0.2.99"}
		r2 := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").Stub(fakerun.Reply{Result: run.Result{Stderr: []byte(dirty), Code: 255}}, argv...)
		a := ui.StaticCursor(polled(t, r2, fakedial.New()))
		a = press(t, a, "a")
		a = update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N root@192.0.2.99")})
		next, cmd = press(t, a, "enter").Update(keyMsg("enter")) // the unmanaged question, then proceed
		a = update(t, next.(ui.Model), cmd())
		if view := press(t, a, "esc").View(); strings.ContainsAny(view, "\r\t\x1b") {
			t.Errorf("the -N one-liner should be clean:\n%s", view)
		}
	})
	t.Run("a narrow frame wraps rather than cuts", func(t *testing.T) {
		t.Parallel()
		m := ui.WithEditor(polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New()), []string{"vi"}, "", "/tmp/x/sasash.conf")
		m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
			return tunnelHosts(), []error{errors.New(strings.Repeat("A", 20) + " tail"), errors.New("second")}, nil
		})
		m = update(t, m, tea.WindowSizeMsg{Width: 16, Height: 24})
		next, reload := m.Update(ui.EditorExit(nil))
		next, _ = next.(ui.Model).Update(reload())
		view := next.(ui.Model).View()
		// Count in the popup's rows only (three-space indent inside the
		// box): the bottom-line notice repeats the head of the first
		// warning.
		as := 0
		for _, l := range boxLines(view) {
			if strings.HasPrefix(l, "   ") {
				as += strings.Count(l, "A")
			}
		}
		if as < 20 {
			t.Errorf("View() at 16 cols should keep every A by wrapping (got %d):\n%s", as, view)
		}
	})
	t.Run("? stays readable under the popup", func(t *testing.T) {
		t.Parallel()
		m, _ := failedForward(t)
		if view := press(t, m, "?").View(); !strings.Contains(view, "close") || !strings.Contains(view, "scroll") {
			t.Errorf("View() help under the popup:\n%s", view)
		}
		m = press(t, m, "esc", "?")
		m = update(t, m, ui.TunnelStatus("spare", tunnel.HostStatus{Host: "spare", CorruptStats: true}, nil))
		if view := m.View(); !strings.Contains(view, "close") {
			t.Errorf("View() full help under a popup opened after ?:\n%s", view)
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
