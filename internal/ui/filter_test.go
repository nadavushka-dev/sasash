package ui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// taggedModel builds a sized, probed model over alpha (tags dev,tlv) and
// bravo (tags prod), wired to the given runner.
func taggedModel(t *testing.T, runner *fakerun.Fake) ui.Model {
	t.Helper()
	hosts := []hostmodel.Host{
		{Alias: "alpha", Tags: []string{"dev", "tlv"}, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
		{Alias: "bravo", Tags: []string{"prod"}, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.20"}}},
	}
	dialer := fakedial.New().
		Up("192.0.2.10:22", 20*time.Millisecond).
		Up("192.0.2.20:22", 5*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: dialer, Timeout: 50 * time.Millisecond}, runner)
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return update(t, m, m.Init()())
}

// press feeds each key through Update the way the bubbletea loop would:
// returned cmds are executed and any filter-match results are fed back in
// (the list computes matches in a cmd), while everything else — blink
// ticks and the like — is dropped so tests never sleep.
func press(t *testing.T, m ui.Model, keys ...string) ui.Model {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		next, cmd := m.Update(keyMsg(k))
		m = next.(ui.Model)
		for _, msg := range filterMatches(cmd) {
			m = update(t, m, msg)
		}
	}
	return m
}

// filterMatches runs a cmd tree and keeps only list.FilterMatchesMsg.
func filterMatches(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	var out []tea.Msg
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			out = append(out, filterMatches(c)...)
		}
	case list.FilterMatchesMsg:
		out = append(out, msg)
	}
	return out
}

func TestModel_Update_FilterByAlias(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New())
	m = press(t, m, "/")
	if got := ui.FilterState(m); got != list.Filtering {
		t.Fatalf("FilterState() after / = %v, want Filtering", got)
	}
	m = press(t, m, "a", "l", "p", "enter")
	if got := ui.FilterState(m); got != list.FilterApplied {
		t.Fatalf("FilterState() after enter = %v, want FilterApplied", got)
	}
	view := m.View()
	if !strings.Contains(view, "alpha") {
		t.Errorf("View() missing the matching host alpha:\n%s", view)
	}
	if strings.Contains(view, "bravo") {
		t.Errorf("View() still shows filtered-out host bravo:\n%s", view)
	}
}

func TestModel_Update_FilterByTag(t *testing.T) {
	t.Parallel()
	// "dev" appears only in alpha's @sasash tags, not in either alias.
	m := taggedModel(t, fakerun.New())
	m = press(t, m, "/", "d", "e", "v", "enter")
	host, ok := m.Selected()
	if !ok {
		t.Fatalf("Selected() ok = false, want the tag-matched host")
	}
	if host.Alias != "alpha" {
		t.Errorf("Selected().Alias = %v, want alpha (matched via tag)", host.Alias)
	}
	if view := m.View(); strings.Contains(view, "bravo") {
		t.Errorf("View() still shows bravo after tag filter:\n%s", view)
	}
}

func TestModel_Update_ActionKeysSuppressedWhileFiltering(t *testing.T) {
	t.Parallel()
	runner := fakerun.New()
	m := taggedModel(t, runner)
	m = press(t, m, "/")

	// q must type into the filter, not quit.
	m2, cmd := m.Update(keyMsg("q"))
	m = m2.(ui.Model)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Errorf("Update(q) while filtering returned tea.Quit")
		}
	}
	if got := ui.FilterState(m); got != list.Filtering {
		t.Errorf("FilterState() after q = %v, want still Filtering", got)
	}

	// y must not reach the clipboard action either.
	m = press(t, m, "y")
	if calls := runner.Calls(); len(calls) != 0 {
		t.Errorf("Calls() after y while filtering = %v, want none", calls)
	}

	// esc cancels filtering entirely.
	m = press(t, m, "esc")
	if got := ui.FilterState(m); got != list.Unfiltered {
		t.Errorf("FilterState() after esc = %v, want Unfiltered", got)
	}
}

func TestModel_Update_EscClearsAppliedFilter(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New())
	m = press(t, m, "/", "a", "l", "p", "enter", "esc")
	if got := ui.FilterState(m); got != list.Unfiltered {
		t.Fatalf("FilterState() after esc = %v, want Unfiltered", got)
	}
	view := m.View()
	for _, want := range []string{"alpha", "bravo"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() after clearing filter missing %q", want)
		}
	}
}

func TestModel_Update_FilterSurvivesProbeRefresh(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New())
	m = press(t, m, "/", "a", "l", "p", "enter")

	next, cmd := m.Update(ui.RefreshTick())
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(refreshTick) cmd = nil, want probe cmd")
	}
	m = update(t, m, cmd()) // fresh probeDoneMsg lands mid-filter

	if got := ui.FilterState(m); got != list.FilterApplied {
		t.Errorf("FilterState() after probe refresh = %v, want FilterApplied", got)
	}
	view := m.View()
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "20ms") {
		t.Errorf("View() after refresh lost the filtered host or its status:\n%s", view)
	}
	if strings.Contains(view, "bravo") {
		t.Errorf("View() after refresh shows filtered-out host bravo:\n%s", view)
	}
}

func TestModel_Update_ConnectAfterFilterApplied(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New())
	m = press(t, m, "/", "a", "l", "p", "enter")
	_, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Errorf("Update(enter) with filter applied cmd = nil, want ssh handoff cmd")
	}
}

func TestModel_View_NoFilterMatch(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New())
	m = press(t, m, "/", "z", "z", "z")
	view := m.View()
	if !strings.Contains(view, "no hosts match") {
		t.Errorf("View() with zero matches missing the empty-state sentence:\n%s", view)
	}
	// The filter input must stay visible so the user can fix the term.
	if !strings.Contains(view, "zzz") {
		t.Errorf("View() with zero matches lost the filter input:\n%s", view)
	}
}

func TestModel_Update_HelpToggle(t *testing.T) {
	t.Parallel()
	m := taggedModel(t, fakerun.New())

	// The full-help-only entry proves the view is generated from FullHelp.
	if view := m.View(); strings.Contains(view, "clear filter / back") {
		t.Fatalf("View() shows full help before ?:\n%s", view)
	}
	m = press(t, m, "?")
	view := m.View()
	if !strings.Contains(view, "clear filter / back") {
		t.Errorf("View() after ? missing the generated full help:\n%s", view)
	}
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("View() with full help, line %d width = %v, want <= 80", i, w)
		}
	}

	m = press(t, m, "?")
	if view := m.View(); strings.Contains(view, "clear filter / back") {
		t.Errorf("View() after second ? still shows full help:\n%s", view)
	}

	// esc closes the help view too.
	m = press(t, m, "?")
	m = press(t, m, "esc")
	if view := m.View(); strings.Contains(view, "clear filter / back") {
		t.Errorf("View() after esc still shows full help:\n%s", view)
	}
}
