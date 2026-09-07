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
	if !strings.Contains(view, "pinned .office .nat (v shows them)") {
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
	if strings.Contains(view, "  web.office") || !strings.Contains(view, "pinned .office .nat") || !strings.Contains(view, "1 host ·") {
		t.Errorf("View() after the reload should fold the new variants:\n%s", view)
	}
}

func TestModel_View_HelpListsVariants(t *testing.T) {
	t.Parallel()
	// Tall enough for the whole host-list group (it grew a row in 2.11).
	m := update(t, sized(t, nil), tea.WindowSizeMsg{Width: 100, Height: 40})
	if view := press(t, m, "?").View(); !strings.Contains(view, "pinned variants") {
		t.Errorf("View() full help missing v:\n%s", view)
	}
}

func TestComputeVariants_Witnesses(t *testing.T) {
	t.Parallel()
	web := hostmodel.Host{Alias: "web", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "office.2", Host: "192.0.2.12"}}}
	tests := []struct {
		name    string
		host    hostmodel.Host
		variant bool
	}{
		{name: "generated block", host: hostmodel.Host{Alias: "web.office", Paths: []probe.Path{{Host: "192.0.2.10"}}}, variant: true},
		{name: "host compared case-insensitively", host: hostmodel.Host{Alias: "web.office", Paths: []probe.Path{{Host: "192.0.2.10"}}}, variant: true},
		{name: "dotted path name", host: hostmodel.Host{Alias: "web.office.2", Paths: []probe.Path{{Host: "192.0.2.12"}}}, variant: true},
		{name: "user block with its own meta stays", host: hostmodel.Host{Alias: "web.office", Paths: []probe.Path{{Host: "192.0.2.10"}}, Tags: []string{"prod"}}, variant: false},
		{name: "user block with forwards stays", host: hostmodel.Host{Alias: "web.office", Paths: []probe.Path{{Host: "192.0.2.10"}}, Forwards: []hostmodel.ForwardSet{{Name: "pg", Specs: []string{"5432:localhost:5432"}}}}, variant: false},
		{name: "another address stays", host: hostmodel.Host{Alias: "web.office", Paths: []probe.Path{{Host: "203.0.113.5"}}}, variant: false},
		{name: "two paths stays", host: hostmodel.Host{Alias: "web.office", Paths: []probe.Path{{Host: "192.0.2.10"}, {Host: "192.0.2.11"}}}, variant: false},
		{name: "no such path name stays", host: hostmodel.Host{Alias: "web.staging", Paths: []probe.Path{{Host: "192.0.2.10"}}}, variant: false},
		{name: "no such parent stays", host: hostmodel.Host{Alias: "db.office", Paths: []probe.Path{{Host: "192.0.2.10"}}}, variant: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The candidate first: config order must not matter.
			m := ui.New([]hostmodel.Host{tt.host, web}, probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{})
			m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
			view := m.View()
			listedRow := strings.Contains(view, "  "+tt.host.Alias) || strings.Contains(view, "› "+tt.host.Alias)
			if listedRow == tt.variant {
				t.Errorf("%s listed = %v, want variant %v:\n%s", tt.host.Alias, listedRow, tt.variant, view)
			}
		})
	}
}

func TestModel_Update_FilterKeepsSelectionWhenAVariantSurfaces(t *testing.T) {
	t.Parallel()
	// Typing the character that makes the term name a variant must not
	// move the cursor to another host: the rows never change under a
	// filter, only the match set does.
	var hosts []hostmodel.Host
	for i := 1; i <= 4; i++ {
		hosts = append(hosts,
			hostmodel.Host{Alias: "zzz" + string(rune('0'+i)), Paths: []probe.Path{{Host: "192.0.2.1"}}},
			hostmodel.Host{Alias: "office" + string(rune('0'+i)), Paths: []probe.Path{{Host: "192.0.2.2"}}})
	}
	hosts = append(hosts,
		hostmodel.Host{Alias: "web", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "nat", Host: "198.51.100.5"}}},
		hostmodel.Host{Alias: "web.office", Paths: []probe.Path{{Host: "192.0.2.10"}}})
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(t, m, "/", "o", "f", "f", "i", "c")
	if host, _ := m.Selected(); host.Alias != "office1" {
		t.Fatalf("Selected() after /offic = %q, want office1", host.Alias)
	}
	m = press(t, m, "e")
	if host, _ := m.Selected(); host.Alias != "office1" {
		t.Errorf("Selected() after the surfacing keystroke = %q, want office1 kept", host.Alias)
	}
	if view := m.View(); !strings.Contains(view, "web.office") {
		t.Errorf("View() should now list web.office:\n%s", view)
	}
}

func TestModel_Update_OneMatchSetPerKeystroke(t *testing.T) {
	t.Parallel()
	// Under a filter each key must yield exactly one match round: two
	// would race, and the stale one could land last and empty the list.
	m := ui.StaticCursor(foldModel(t))
	m = press(t, m, "/")
	for _, k := range []string{"o", "f", "f", "i", "c", "e"} {
		next, cmd := m.Update(keyMsg(k))
		m = next.(ui.Model)
		msgs := filterMatches(cmd)
		if len(msgs) != 1 {
			t.Fatalf("key %q produced %d match rounds, want exactly 1", k, len(msgs))
		}
		m = update(t, m, msgs[0])
	}
	if view := m.View(); !strings.Contains(view, "web.office") || strings.Contains(view, "no hosts match") {
		t.Errorf("View() after /office:\n%s", view)
	}
}

func TestModel_Update_VariantsToggleRefreshesStatus(t *testing.T) {
	t.Parallel()
	// Folding under the cursor moves the selection to another host: that
	// host's status is refreshed like any selection change.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "ubuntu.lan")
	d := fakedial.New()
	hosts := []hostmodel.Host{
		{Alias: "ubuntu", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
		{Alias: "ubuntu.lan", Paths: []probe.Path{{Host: "192.0.2.10"}}},
	}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d)})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = feed(t, m, m.Init())
	m = press(t, m, "v")
	next, sel := m.Update(keyMsg("j")) // ubuntu.lan
	m = feed(t, next.(ui.Model), sel)
	if host, _ := m.Selected(); host.Alias != "ubuntu.lan" {
		t.Fatalf("Selected() = %q, want ubuntu.lan", host.Alias)
	}
	next, cmd := m.Update(keyMsg("v")) // folds under the cursor: selection clamps to ubuntu
	m = next.(ui.Model)
	if host, _ := m.Selected(); host.Alias != "ubuntu" {
		t.Fatalf("Selected() after folding = %q, want ubuntu", host.Alias)
	}
	if cmd == nil || !strings.Contains(m.View(), "◌ master") {
		t.Errorf("folding onto ubuntu: cmd %v, view:\n%s", cmd != nil, m.View())
	}
}

func TestModel_View_PinnedLineCountsWhenLong(t *testing.T) {
	t.Parallel()
	parent := hostmodel.Host{Alias: "dc"}
	hosts := []hostmodel.Host{parent}
	for i := 0; i < 12; i++ {
		name := "datacenter" + string(rune('0'+i%10)) + string(rune('a'+i))
		hosts[0].Paths = append(hosts[0].Paths, probe.Path{Name: name, Host: "192.0.2.1"})
		hosts = append(hosts, hostmodel.Host{Alias: "dc." + name, Paths: []probe.Path{{Host: "192.0.2.1"}}})
	}
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	if view := m.View(); !strings.Contains(view, "pinned 12 variants (v shows them)") {
		t.Errorf("View() at 80 cols should count the variants:\n%s", view)
	}
	m = update(t, m, tea.WindowSizeMsg{Width: 250, Height: 30})
	if view := m.View(); !strings.Contains(view, ".datacenter0a") || !strings.Contains(view, "(v shows them)") {
		t.Errorf("View() at 250 cols should name them:\n%s", view)
	}
}

func TestModel_Update_ReloadUnderFilterKeepsTheSelectedAlias(t *testing.T) {
	t.Parallel()
	// Under a filter the cursor indexes the match set; a reload that
	// inserts a host before the selected one must re-select by alias
	// once the new match set lands, not keep a now-wrong index.
	mk := func(aliases ...string) []hostmodel.Host {
		var out []hostmodel.Host
		for _, a := range aliases {
			out = append(out, hostmodel.Host{Alias: a, Paths: []probe.Path{{Host: "192.0.2.1"}}})
		}
		return out
	}
	m := ui.New(mk("a1", "b1", "a2", "b2"), probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(t, m, "/", "b", "enter", "j") // matches b1, b2; select b2
	if host, _ := m.Selected(); host.Alias != "b2" {
		t.Fatalf("Selected() = %q, want b2", host.Alias)
	}
	m = ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = editReload(t, m, mk("b0", "a1", "b1", "a2", "b2")) // b0 now precedes b2 in the matches
	if host, _ := m.Selected(); host.Alias != "b2" {
		t.Errorf("Selected() after the reload = %q, want b2 re-selected by alias", host.Alias)
	}
}

func TestModel_Update_VariantsToggleUnderAppliedFilter(t *testing.T) {
	t.Parallel()
	// v under an applied filter must keep the rows (SetItems' round has
	// to reach the runtime) and surface the variants the term reaches;
	// the stashed re-select must not hijack a later filter either.
	m := ui.StaticCursor(foldModel(t))
	m = press(t, m, "/", "w", "e", "b", "enter")
	if view := m.View(); !strings.Contains(view, "› web") || strings.Contains(view, "web.office") {
		t.Fatalf("View() before v:\n%s", view)
	}
	next, cmd := m.Update(keyMsg("v"))
	m = next.(ui.Model)
	rounds := filterMatches(cmd)
	if len(rounds) != 1 {
		t.Fatalf("v under a filter produced %d match rounds, want 1", len(rounds))
	}
	m = update(t, m, rounds[0])
	view := m.View()
	if strings.Contains(view, "no hosts match") || !strings.Contains(view, "web.office") || !strings.Contains(view, "web.nat") {
		t.Errorf("View() after v under /web:\n%s", view)
	}
	if host, _ := m.Selected(); host.Alias != "web" {
		t.Errorf("Selected() after v = %q, want web kept", host.Alias)
	}
	// A later, unrelated filter lands where it should.
	m = press(t, m, "v", "esc", "/", "d", "b", ".", "l", "enter")
	if host, _ := m.Selected(); host.Alias != "db.lan" {
		t.Errorf("Selected() after /db.l = %q, want db.lan (no stale re-select)", host.Alias)
	}
}

func TestModel_Update_EmptyFilterTermKeepsTheFold(t *testing.T) {
	t.Parallel()
	// bubbles never calls the filter function for an empty term, so the
	// rows must stay folded at / and again after backspacing to empty.
	m := ui.StaticCursor(foldModel(t))
	m = press(t, m, "/")
	if view := m.View(); strings.Contains(view, "web.office") || !strings.Contains(view, "5 hosts") {
		t.Errorf("View() right after / lists variants:\n%s", view)
	}
	m = press(t, m, "w")
	if view := m.View(); strings.Contains(view, "web.office") {
		t.Errorf("View() after /w lists variants:\n%s", view)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(ui.Model)
	rounds := filterMatches(cmd)
	if len(rounds) != 1 {
		t.Fatalf("backspace to empty produced %d match rounds, want 1", len(rounds))
	}
	m = update(t, m, rounds[0])
	if view := m.View(); strings.Contains(view, "web.office") || strings.Contains(view, "no hosts match") {
		t.Errorf("View() after backspacing to an empty term:\n%s", view)
	}
}
