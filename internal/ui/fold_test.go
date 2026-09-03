package ui_test

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// foldHosts is the asymmetric fixture: web has named paths office and
// nat; web.office and web.nat are its generated variants; web.staging
// looks dotted but names no path of web; db.lan has no parent db;
// api.v2.office has parent api.v2 whose path is office.
func foldHosts() []hostmodel.Host {
	return []hostmodel.Host{
		{Alias: "web", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "nat", Host: "198.51.100.5"}}},
		{Alias: "web.office", Paths: []probe.Path{{Host: "192.0.2.10"}}},
		{Alias: "web.nat", Paths: []probe.Path{{Host: "198.51.100.5"}}},
		{Alias: "web.staging", Paths: []probe.Path{{Host: "192.0.2.11"}}},
		{Alias: "db.lan", Paths: []probe.Path{{Host: "192.0.2.30"}}},
		// db.office: other hosts have an office path, but no host db exists.
		{Alias: "db.office", Paths: []probe.Path{{Host: "192.0.2.31"}}},
		{Alias: "api.v2", Paths: []probe.Path{{Name: "office", Host: "203.0.113.9"}}},
		{Alias: "api.v2.office", Paths: []probe.Path{{Host: "203.0.113.9"}}},
	}
}

func foldModel(t *testing.T) ui.Model {
	t.Helper()
	m := ui.New(foldHosts(), probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	return feed(t, m, m.Init())
}

func TestModel_View_VariantsFoldByDefault(t *testing.T) {
	t.Parallel()
	m := foldModel(t)
	view := m.View()
	for _, hidden := range []string{"web.office", "web.nat", "api.v2.office"} {
		if strings.Contains(view, "  "+hidden) || strings.Contains(view, "› "+hidden) {
			t.Errorf("View() lists the variant %q by default:\n%s", hidden, view)
		}
	}
	for _, kept := range []string{"web.staging", "db.lan", "db.office", "api.v2"} {
		if !strings.Contains(view, kept) {
			t.Errorf("View() folded the non-variant %q:\n%s", kept, view)
		}
	}
	// The parent names them; the tally counts listed hosts only.
	if !strings.Contains(view, "pinned .nat .office (v shows them)") {
		t.Errorf("View() missing the parent's pinned line:\n%s", view)
	}
	if !strings.Contains(view, "5 hosts · 0 reachable") {
		t.Errorf("View() tally should count the 5 listed hosts:\n%s", view)
	}
}

func TestModel_Update_VariantsToggle(t *testing.T) {
	t.Parallel()
	m := foldModel(t)
	m = press(t, m, "G") // api.v2, the last listed host
	if host, _ := m.Selected(); host.Alias != "api.v2" {
		t.Fatalf("Selected() = %q, want api.v2", host.Alias)
	}
	m = press(t, m, "v")
	view := m.View()
	for _, shown := range []string{"web.office", "web.nat", "api.v2.office"} {
		if !strings.Contains(view, shown) {
			t.Errorf("View() after v missing %q:\n%s", shown, view)
		}
	}
	if !strings.Contains(view, "8 hosts") || !strings.Contains(view, "pinned variants listed") || !strings.Contains(view, "(listed)") {
		t.Errorf("View() after v: tally/notice/hint:\n%s", view)
	}
	// The selection stayed on its alias through the rebuild.
	if host, _ := m.Selected(); host.Alias != "api.v2" {
		t.Errorf("Selected() after v = %q, want api.v2 kept", host.Alias)
	}
	// A surfaced variant is a real host: ssh to it works.
	m = press(t, m, "j") // api.v2.office
	if host, _ := m.Selected(); host.Alias != "api.v2.office" {
		t.Fatalf("Selected() = %q, want api.v2.office", host.Alias)
	}
	if _, cmd := m.Update(keyMsg("enter")); cmd == nil {
		t.Errorf("enter on a surfaced variant cmd = nil, want the ssh handoff")
	}
	if view := m.View(); !strings.Contains(view, "$ ssh api.v2.office") {
		t.Errorf("View() preview for the variant:\n%s", view)
	}
	// Fold again: the cursor cannot stay on a hidden row, so it clamps.
	m = press(t, m, "v")
	view = m.View()
	if strings.Contains(view, "api.v2.office") || !strings.Contains(view, "5 hosts") {
		t.Errorf("View() after the second v should fold again:\n%s", view)
	}
	if _, ok := m.Selected(); !ok {
		t.Errorf("Selected() ok = false after folding under the cursor")
	}
}

func TestModel_Update_FilterSurfacesVariantsExplicitly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		term     string
		wantRows []string
		notRows  []string
	}{
		{name: "the name surfaces its variants", term: "office", wantRows: []string{"web.office", "api.v2.office", "db.office"}, notRows: []string{"web.nat"}},
		{name: "the dotted suffix too", term: ".nat", wantRows: []string{"web.nat"}, notRows: []string{"web.office"}},
		{name: "the full alias too", term: "web.office", wantRows: []string{"web.office"}, notRows: []string{"web.nat"}},
		{name: "a bare parent term does not", term: "web", wantRows: []string{"web", "web.staging"}, notRows: []string{"web.office", "web.nat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := foldModel(t)
			m = press(t, m, append([]string{"/"}, strings.Split(tt.term, "")...)...)
			view := m.View()
			for _, w := range tt.wantRows {
				if !strings.Contains(view, w) {
					t.Errorf("filter %q: View() missing %q:\n%s", tt.term, w, view)
				}
			}
			for _, n := range tt.notRows {
				if strings.Contains(view, "  "+n) || strings.Contains(view, "› "+n) {
					t.Errorf("filter %q: View() lists %q:\n%s", tt.term, n, view)
				}
			}
			// Clearing the filter folds them again.
			m = press(t, m, "esc")
			if view := m.View(); strings.Contains(view, "  web.office") || strings.Contains(view, "› web.office") {
				t.Errorf("View() after esc still lists a variant:\n%s", view)
			}
		})
	}
}

func TestModel_Update_ReloadRefolds(t *testing.T) {
	t.Parallel()
	// The first p press generates the variants: the reload that follows
	// must fold them straight away, never listing them once.
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "nat", Host: "198.51.100.5"}}}}
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = feed(t, m, m.Init())
	m = ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = reloadAndProbe(t, m, foldHosts()[:3]) // web + its two variants
	view := m.View()
	if strings.Contains(view, "  web.office") || !strings.Contains(view, "pinned .nat .office") || !strings.Contains(view, "1 host ·") {
		t.Errorf("View() after the reload should fold the new variants:\n%s", view)
	}
}

func TestModel_View_HelpListsVariants(t *testing.T) {
	t.Parallel()
	m := update(t, sized(t, nil), tea.WindowSizeMsg{Width: 100, Height: 30})
	if view := press(t, m, "?").View(); !strings.Contains(view, "pinned variants") {
		t.Errorf("View() full help missing v:\n%s", view)
	}
}
