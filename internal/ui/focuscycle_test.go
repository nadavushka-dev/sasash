package ui_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The pane-focus cycle (SASASH.2.11): l/tab from the list reach the paths
// pane when the host has a path to choose, the tunnels pane otherwise;
// l under paths moves on to tunnels, l under tunnels returns to the
// list; enter on a path is p's flow.

// cycleModel is the picker fixture with a host, "both", that has two
// named paths AND a declared forward set, so every stop of the cycle
// exists on one host; the switcher hooks record and answer as p's do.
func cycleModel(t *testing.T) (ui.Model, *[]string) {
	t.Helper()
	both := hostmodel.Host{Alias: "both", Paths: []probe.Path{pathLAN, pathTS}, Forwards: []hostmodel.ForwardSet{{Name: "web", Specs: []string{"3000:localhost:3000"}}}}
	hosts := append(tunnelHosts(), both)
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "both")
	d := fakedial.New().Up("192.0.2.10:22", 5*time.Millisecond).Down("198.51.100.5:2222")
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d)})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	var switched []string
	m = ui.WithSwitcher(m,
		func(alias, name string) (pathswitch.Result, error) {
			switched = append(switched, alias+" "+name)
			return pathswitch.Result{Alias: alias, From: pathLAN, To: pathTS, Changed: true}, nil
		},
		func(alias string) ([]probe.Path, string, bool) {
			if alias == "both" {
				return []probe.Path{pathLAN, pathTS}, "192.0.2.10", true
			}
			return nil, "", false
		}, nil)
	return press(t, m, "G"), &switched // both is last in the list
}

func TestModel_Update_FocusCycle(t *testing.T) {
	t.Parallel()
	t.Run("l walks list → paths → tunnels → list", func(t *testing.T) {
		t.Parallel()
		m, _ := cycleModel(t)
		if h, _ := m.Selected(); h.Alias != "both" {
			t.Fatalf("Selected() = %q, want both", h.Alias)
		}
		m = press(t, m, "l")
		if !ui.PathsFocused(m) || ui.TunnelsFocused(m) {
			t.Fatalf("l from the list should focus the paths pane (paths %v, tunnels %v)", ui.PathsFocused(m), ui.TunnelsFocused(m))
		}
		if view := m.View(); !strings.Contains(view, "make active") || !strings.Contains(view, "l tunnels pane") {
			t.Errorf("View() under the paths focus should show its footer with the way on:\n%s", view)
		}
		m = press(t, m, "l")
		if !ui.TunnelsFocused(m) || ui.PathsFocused(m) {
			t.Fatalf("l under the paths focus should move on to the tunnels pane")
		}
		m = press(t, m, "l")
		if ui.TunnelsFocused(m) || ui.PathsFocused(m) {
			t.Errorf("l under the tunnels focus should return to the list")
		}
	})
	t.Run("tab from the list is the same first step", func(t *testing.T) {
		t.Parallel()
		m, _ := cycleModel(t)
		if m = press(t, m, "tab"); !ui.PathsFocused(m) {
			t.Errorf("tab from the list should focus the paths pane")
		}
		// Inside the pane tab moves the mark, never the focus.
		if m = press(t, m, "tab"); !ui.PathsFocused(m) {
			t.Errorf("tab under the paths focus should stay in the pane")
		}
	})
	t.Run("h and esc return to the list from either pane", func(t *testing.T) {
		t.Parallel()
		m, _ := cycleModel(t)
		for _, back := range []string{"h", "esc"} {
			if c := press(t, m, "l", back); ui.PathsFocused(c) || ui.TunnelsFocused(c) {
				t.Errorf("%s under the paths focus should return to the list", back)
			}
			if c := press(t, m, "l", "l", back); ui.PathsFocused(c) || ui.TunnelsFocused(c) {
				t.Errorf("%s under the tunnels focus should return to the list", back)
			}
		}
	})
	t.Run("enter on a path under l is p's flow", func(t *testing.T) {
		t.Parallel()
		m, switched := cycleModel(t)
		m = press(t, m, "l", "j")
		if view := m.View(); !strings.Contains(view, "p → ts 198.51.100.5") {
			t.Errorf("View() should plan the highlighted path as p does:\n%s", view)
		}
		next, cmd := m.Update(keyMsg("enter"))
		m = next.(ui.Model)
		if cmd == nil || ui.PathsFocused(m) {
			t.Fatalf("enter should switch and return to the list (cmd %v, paths %v)", cmd != nil, ui.PathsFocused(m))
		}
		cmd()
		if want := []string{"both ts"}; !reflect.DeepEqual(*switched, want) {
			t.Errorf("switched = %v, want %v — the same switcher call p makes", *switched, want)
		}
	})
	t.Run("p and l land on the same focus", func(t *testing.T) {
		t.Parallel()
		m, _ := cycleModel(t)
		viaP, viaL := press(t, m, "p"), press(t, m, "l")
		if !ui.PathsFocused(viaP) || !ui.PathsFocused(viaL) || viaP.View() != viaL.View() {
			t.Errorf("p and l should render the same paths focus")
		}
	})
	t.Run("a host without a path to choose goes straight to its tunnels", func(t *testing.T) {
		t.Parallel()
		m, _ := cycleModel(t)
		m = press(t, m, "g") // ubuntu: one path, declared sets
		if m = press(t, m, "l"); !ui.TunnelsFocused(m) || ui.PathsFocused(m) {
			t.Errorf("l on a single-path host should focus the tunnels pane")
		}
	})
	t.Run("a host with paths but no tunnels stays on the paths pane and says so", func(t *testing.T) {
		t.Parallel()
		m, _ := pickerModel(t, fakerun.New()) // dual: two paths, no forwards
		m = press(t, m, "l", "l")
		if !ui.PathsFocused(m) || !strings.Contains(m.View(), "no tunnels on dual — + adds one") {
			t.Errorf("l on to a host without tunnels should say so and keep the paths focus:\n%s", m.View())
		}
	})
	t.Run("a declared path the pane does not render is no choice: on to the tunnels", func(t *testing.T) {
		t.Parallel()
		// The block declares lan and ts; the pane renders only lan (a
		// shadowing Host above the Include): the paths focus could not
		// offer a switch, so l passes it by.
		shadow := hostmodel.Host{Alias: "shadow", Paths: []probe.Path{pathLAN}, Forwards: []hostmodel.ForwardSet{{Name: "web", Specs: []string{"3000:localhost:3000"}}}}
		hosts := append(tunnelHosts(), shadow)
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "shadow")
		d := fakedial.New().Up("192.0.2.10:22", 5*time.Millisecond)
		m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d)})
		m = feed(t, update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}), m.Init())
		m = ui.WithSwitcher(m,
			func(alias, name string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
			func(alias string) ([]probe.Path, string, bool) {
				return []probe.Path{pathLAN, pathTS}, "192.0.2.10", true
			}, nil)
		m = press(t, m, "G", "l")
		if !ui.TunnelsFocused(m) || ui.PathsFocused(m) {
			t.Errorf("l should pass a pane with one rendered path by (paths %v, tunnels %v):\n%s", ui.PathsFocused(m), ui.TunnelsFocused(m), m.View())
		}
	})
	t.Run("a running switch refuses l as it refuses p", func(t *testing.T) {
		t.Parallel()
		m, _ := cycleModel(t)
		m = press(t, m, "l", "j")
		next, _ := m.Update(keyMsg("enter")) // the switch is in flight, its result not delivered
		m = press(t, next.(ui.Model), "l")
		if ui.PathsFocused(m) || !strings.Contains(m.View(), "a switch is already running for both") {
			t.Errorf("l during a switch should refuse with the reason:\n%s", m.View())
		}
	})
	t.Run("no switcher wired: l is the tunnels pane, as before", func(t *testing.T) {
		t.Parallel()
		m := polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New())
		if m = press(t, m, "l"); !ui.TunnelsFocused(m) {
			t.Errorf("without a path switcher l should focus the tunnels pane")
		}
	})
	t.Run("v still shows the variant rows, and is not needed to switch", func(t *testing.T) {
		t.Parallel()
		m, switched := cycleModel(t)
		if view := press(t, m, "v").View(); !strings.Contains(view, "pinned variants") {
			t.Errorf("v should still toggle the variant rows:\n%s", view)
		}
		// The switch above went through the pane, with v untouched.
		m = press(t, m, "l", "j")
		next, cmd := m.Update(keyMsg("enter"))
		_ = next
		cmd()
		if len(*switched) != 1 {
			t.Errorf("a path switch should need no v (switched %v)", *switched)
		}
	})
}
