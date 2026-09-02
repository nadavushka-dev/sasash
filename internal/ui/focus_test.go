package ui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestModel_Update_FocusEscOrder(t *testing.T) {
	t.Parallel()
	// esc folds an expanded ? first, then returns focus to the list, and
	// only then clears an applied filter.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "/", "u", "b", "u", "enter") // filter applied
	if got := ui.FilterState(m); got.String() != "filter applied" {
		t.Fatalf("FilterState() = %v, want filter applied", got)
	}
	m = press(t, m, "l", "?")
	if !ui.TunnelsFocused(m) || !strings.Contains(m.View(), "host list") {
		t.Fatalf("l then ?: focused %v, full help shown %v", ui.TunnelsFocused(m), strings.Contains(m.View(), "host list"))
	}
	m = press(t, m, "esc")
	if !ui.TunnelsFocused(m) || strings.Contains(m.View(), "host list") {
		t.Errorf("first esc should fold the help and keep focus: focused %v:\n%s", ui.TunnelsFocused(m), m.View())
	}
	m = press(t, m, "esc")
	if ui.TunnelsFocused(m) || ui.FilterState(m).String() != "filter applied" {
		t.Errorf("second esc should unfocus and keep the filter: focused %v, filter %v", ui.TunnelsFocused(m), ui.FilterState(m))
	}
	m = press(t, m, "esc")
	if ui.FilterState(m).String() != "unfiltered" {
		t.Errorf("third esc should clear the filter, got %v", ui.FilterState(m))
	}
}

func TestModel_Update_FocusNeedsRows(t *testing.T) {
	t.Parallel()
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(255, "ssh", "-O", "check", "bare")
	m := polled(t, r, fakedial.New())
	// h on the list is a no-op; l on a host without tunnels says so.
	m = press(t, m, "h")
	if ui.TunnelsFocused(m) {
		t.Errorf("h on the list focused the tunnels pane")
	}
	m = press(t, m, "j") // bare
	m = feed(t, m, m.Init())
	m = press(t, m, "l")
	if ui.TunnelsFocused(m) || !strings.Contains(m.View(), "no tunnels on bare") {
		t.Errorf("l on a host without tunnels: focused %v:\n%s", ui.TunnelsFocused(m), m.View())
	}
}

func TestModel_Update_FocusKeepsActionKeys(t *testing.T) {
	t.Parallel()
	// Under tunnels focus enter is still ssh to the host, q still quits,
	// space acts on the highlighted row, and t still cycles the theme
	// without dropping the focus.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", specWeb0, "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", specWeb1, "ubuntu")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l")
	if _, cmd := m.Update(keyMsg("enter")); cmd == nil {
		t.Errorf("enter under tunnels focus cmd = nil, want the ssh handoff")
	}
	if _, cmd := m.Update(keyMsg("q")); cmd == nil {
		t.Errorf("q under tunnels focus cmd = nil, want quit")
	}
	next, cmd := m.Update(keyMsg(" "))
	if cmd == nil {
		t.Fatalf("space under tunnels focus cmd = nil, want the set action")
	}
	m = update(t, next.(ui.Model), cmd())
	if view := m.View(); !strings.Contains(view, "web up") {
		t.Errorf("View() after space missing the outcome:\n%s", view)
	}
	m = press(t, m, "t")
	if !ui.TunnelsFocused(m) || ui.ThemeName(m) == "gruvbox" {
		t.Errorf("t under tunnels focus: focused %v, theme %q", ui.TunnelsFocused(m), ui.ThemeName(m))
	}
}

func TestModel_Update_FocusDropsWhenRowsVanish(t *testing.T) {
	t.Parallel()
	// A reload can remove every forward under the focus: the next key
	// finds nothing to move over and hands the list back.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l")
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
		return []hostmodel.Host{{Alias: "ubuntu", Paths: []probe.Path{{Host: "192.0.2.10"}}}}, nil, nil
	})
	next, reload := m.Update(ui.EditorExit(nil))
	next, after := next.(ui.Model).Update(reload())
	m = feed(t, next.(ui.Model), after) // the reload's re-poll lands: no rows
	m = press(t, m, "j")
	if ui.TunnelsFocused(m) {
		t.Errorf("focus stayed on an empty tunnels pane")
	}
	if host, ok := m.Selected(); !ok || host.Alias != "ubuntu" {
		t.Errorf("Selected() = %q, %v; want ubuntu (j was consumed by the unfocus)", host.Alias, ok)
	}
}

func TestStyles_ItemSelectedDim_DerivesFromPalette(t *testing.T) {
	t.Parallel()
	// The dimmed selection is a palette role, never a literal: for every
	// theme it takes fg-dim over the subtle fill.
	for _, th := range ui.Themes {
		m := ui.New(testHosts(1), probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{Theme: th.Name})
		st := ui.StylesOf(m)
		if got, want := st.ItemSelectedDim.GetForeground(), th.Palette.FgDim; got != want {
			t.Errorf("%s: ItemSelectedDim foreground = %v, want FgDim %v", th.Name, got, want)
		}
		if got, want := st.ItemSelectedDim.GetBackground(), th.Palette.BgSubtle; got != want {
			t.Errorf("%s: ItemSelectedDim background = %v, want BgSubtle %v", th.Name, got, want)
		}
		if st.ItemSelected.GetForeground() == st.ItemSelectedDim.GetForeground() {
			t.Errorf("%s: lit and dim selections share a foreground — focus would be invisible", th.Name)
		}
	}
}
