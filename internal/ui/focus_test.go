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
	if !ui.TunnelsFocused(m) || !ui.HelpOpen(m) {
		t.Fatalf("l then ?: focused %v, keys panel open %v", ui.TunnelsFocused(m), ui.HelpOpen(m))
	}
	m = press(t, m, "esc")
	if !ui.TunnelsFocused(m) || ui.HelpOpen(m) {
		t.Errorf("first esc should close the panel and keep focus: focused %v, open %v:\n%s", ui.TunnelsFocused(m), ui.HelpOpen(m), m.View())
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

func TestModel_Update_FocusFollowsHostChange(t *testing.T) {
	t.Parallel()
	// A host change made by the list (a filter accept) hands focus back
	// and drops the highlight: x afterwards must not act on a row of the
	// new host the user never highlighted.
	// From the FIRST host, so opening the filter does not move the
	// cursor by itself: only the accept changes the host.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "check", "spare")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l") // ubuntu's web row
	m = press(t, m, "/", "s", "p", "a")
	if host, _ := m.Selected(); host.Alias != "spare" {
		t.Fatalf("filter typed: host %q, want spare (the match)", host.Alias)
	}
	m = press(t, m, "enter")
	if host, _ := m.Selected(); host.Alias != "spare" || ui.TunnelsFocused(m) {
		t.Fatalf("after the filter accept: host %q, focused %v; want spare, list focus", host.Alias, ui.TunnelsFocused(m))
	}
	before := len(r.CallKeys())
	for _, k := range []string{"x", " ", "s"} {
		next, cmd := m.Update(keyMsg(k))
		m = next.(ui.Model)
		if cmd != nil || len(r.CallKeys()) != before || !strings.Contains(m.View(), "no forward highlighted") {
			t.Errorf("%q after a host change acted: cmd %v, calls %v", k, cmd != nil, r.CallKeys()[before:])
		}
	}
	// The list row carries the mark again; no tunnels row does.
	if view := m.View(); !strings.Contains(view, "› spare") || strings.Contains(view, "› ○") || strings.Contains(view, "› ●") {
		t.Errorf("View() after the host change should mark the list, not a row:\n%s", view)
	}
}

func TestModel_Update_FocusDropsWhenHostEditedAway(t *testing.T) {
	t.Parallel()
	// The editor deletes the focused host: the reload clamps the cursor
	// onto another host, and the focus must not ride along.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "check", "spare")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l", "G") // ubuntu's api row
	m = ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = editReload(t, m, tunnelHosts()[1:]) // ubuntu gone: cursor lands on bare
	if host, _ := m.Selected(); host.Alias != "bare" || ui.TunnelsFocused(m) {
		t.Errorf("after the reload: host %q, focused %v; want bare, list focus", host.Alias, ui.TunnelsFocused(m))
	}
	if view := m.View(); !strings.Contains(view, "› bare") {
		t.Errorf("View() should mark the list:\n%s", view)
	}
}

func TestModel_Update_FocusOverFilteredEmptyHost(t *testing.T) {
	t.Parallel()
	// Focus on ubuntu, then a filter that lands on a host with no rows:
	// the focus is gone the moment the selection moved, and exactly one
	// › is on screen — the list's.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(255, "ssh", "-O", "check", "bare")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l", "/", "b", "a", "r", "enter")
	view := m.View()
	if ui.TunnelsFocused(m) || strings.Count(view, "› ") != 1 || !strings.Contains(view, "› bare") {
		t.Errorf("focused %v, › count %d:\n%s", ui.TunnelsFocused(m), strings.Count(view, "› "), view)
	}
	// esc now means one thing: clear the filter.
	m = press(t, m, "esc")
	if ui.FilterState(m).String() != "unfiltered" {
		t.Errorf("esc after the settled focus should clear the filter, got %v", ui.FilterState(m))
	}
}

func TestModel_View_FocusMarkMovesWithFocus(t *testing.T) {
	t.Parallel()
	// The one › on screen marks the focused pane's selection — the
	// signal that survives NO_COLOR, and the pin that the delegate is
	// re-set on every focus change and theme cycle.
	m, _ := aliveWeb0(t)
	if view := m.View(); !strings.Contains(view, "› ubuntu") {
		t.Fatalf("View() with list focus missing the list mark:\n%s", view)
	}
	m = press(t, m, "l")
	view := m.View()
	if strings.Contains(view, "› ubuntu") || !strings.Contains(view, "  ubuntu") || !lineHas(view, "› ", specWeb0) {
		t.Errorf("View() with tunnels focus should move the mark to the row:\n%s", view)
	}
	m = press(t, m, "t") // a theme cycle rebuilds the delegate: focus must survive it
	if view := m.View(); strings.Contains(view, "› ubuntu") || !lineHas(view, "› ", specWeb0) {
		t.Errorf("View() after t lost the tunnels focus mark:\n%s", view)
	}
	m = press(t, m, "h")
	if view := m.View(); !strings.Contains(view, "› ubuntu") || lineHas(view, "› ", specWeb0) {
		t.Errorf("View() after h should mark the list again:\n%s", view)
	}
}

func TestModel_Update_FocusPagingKeysStayInPane(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l", "ctrl+d")
	if host, _ := m.Selected(); host.Alias != "ubuntu" {
		t.Fatalf("ctrl+d under tunnels focus moved the host list to %q", host.Alias)
	}
	if view := m.View(); !lineHas(view, "› ", specAPI) {
		t.Errorf("View() after ctrl+d should highlight the last row:\n%s", view)
	}
	m = press(t, m, "ctrl+u")
	if host, _ := m.Selected(); host.Alias != "ubuntu" {
		t.Fatalf("ctrl+u under tunnels focus moved the host list to %q", host.Alias)
	}
	if view := m.View(); !lineHas(view, "› ", specWeb0) {
		t.Errorf("View() after ctrl+u should highlight the first row:\n%s", view)
	}
	// l while focused closes the cycle (2.11): back on the list, the
	// highlight is gone; the host is unchanged.
	m = press(t, m, "G", "l")
	if host, _ := m.Selected(); host.Alias != "ubuntu" || ui.TunnelsFocused(m) || lineHas(m.View(), "› ", specAPI) {
		t.Errorf("View() after G l should be back on the list without a highlight:\n%s", m.View())
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
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
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
		t.Fatalf("space under tunnels focus cmd = nil, want the forward action")
	}
	m = update(t, next.(ui.Model), cmd())
	if view := m.View(); !strings.Contains(view, "web "+specWeb0+" up") {
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
	// The focus is dropped as soon as the rows are gone — no key needed,
	// so no key ever carries two meanings.
	if ui.TunnelsFocused(m) {
		t.Errorf("focus stayed on an empty tunnels pane")
	}
	if view := m.View(); !strings.Contains(view, "› ubuntu") {
		t.Errorf("View() should mark the list again:\n%s", view)
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
