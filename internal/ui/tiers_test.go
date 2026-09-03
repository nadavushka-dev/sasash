package ui_test

import (
	"reflect"
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

const confPath = "/home/u/.ssh/config.d/sasash.conf"

// tierHosts is the fact fixture: one host per classification, in an
// order that puts ambient ones first so the tiers visibly reorder.
func tierHosts() []hostmodel.Host {
	path := func(host string) []probe.Path { return []probe.Path{{Host: host}} }
	return []hostmodel.Host{
		{Alias: "plain", Paths: path("192.0.2.4"), File: "/home/u/.ssh/config"},     // ambient: root config, no meta
		{Alias: "web", Paths: path("192.0.2.1"), File: confPath, HasMeta: true},     // managed by file (and meta)
		{Alias: "office", Paths: path("192.0.2.6"), File: "/home/u/.ssh/work.conf"}, // ambient: an Included file
		{Alias: "secret", Paths: path("192.0.2.2"), File: "/home/u/.ssh/work.conf", HasMeta: true, Hidden: true},
		{Alias: "bare", Paths: path("192.0.2.5"), File: "/home/u/.ssh/config.d/./sasash.conf"},     // managed by file, spelled unclean
		{Alias: "described", Paths: path("192.0.2.3"), File: "/home/u/.ssh/config", HasMeta: true}, // managed by meta, outside sasash.conf
	}
}

func tierModel(t *testing.T, hosts []hostmodel.Host) ui.Model {
	t.Helper()
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{ConfPath: confPath, Editor: []string{"vi"}})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return update(t, m, m.Init()())
}

func TestModel_IsManaged_Facts(t *testing.T) {
	t.Parallel()
	m := tierModel(t, tierHosts())
	tests := []struct {
		name string
		host hostmodel.Host
		want bool
	}{
		{name: "declared in sasash.conf", host: hostmodel.Host{Alias: "a", File: confPath}, want: true},
		{name: "declared in sasash.conf, unclean path", host: hostmodel.Host{Alias: "a", File: "/home/u/.ssh/config.d/../config.d/sasash.conf"}, want: true},
		{name: "meta outside sasash.conf", host: hostmodel.Host{Alias: "a", File: "/home/u/.ssh/config", HasMeta: true}, want: true},
		{name: "hidden is still managed (meta)", host: hostmodel.Host{Alias: "a", File: "/x", HasMeta: true, Hidden: true}, want: true},
		{name: "root config, no meta", host: hostmodel.Host{Alias: "a", File: "/home/u/.ssh/config"}},
		{name: "included file, no meta", host: hostmodel.Host{Alias: "a", File: "/home/u/.ssh/work.conf"}},
		{name: "no file, no meta", host: hostmodel.Host{Alias: "sasash.conf"}}, // a name is not a fact
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.IsManaged(m, tt.host); got != tt.want {
				t.Errorf("IsManaged(%+v) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

// order reports whether the names appear in the view in the given order.
func order(view string, names ...string) bool {
	last := -1
	for _, n := range names {
		i := strings.Index(view, n)
		if i < 0 || i < last {
			return false
		}
		last = i
	}
	return true
}

func TestModel_View_TiersAndDivider(t *testing.T) {
	t.Parallel()
	m := tierModel(t, tierHosts())
	view := m.View()
	// Managed first in config order, the divider, then ambient in config
	// order; the hidden host is not listed; the tally says the split.
	if strings.Contains(view, "secret") || !strings.Contains(view, "5 hosts (3 managed)") {
		t.Errorf("View() tiers: hidden host listed, or tally without the split:\n%s", view)
	}
	if !order(view, "› web", "bare", "described", "other entries ─", "plain", "office") {
		t.Errorf("View() rows out of tier order:\n%s", view)
	}
	// The cursor skips the divider both ways, and g/G land on hosts.
	m = press(t, m, "j", "j", "j")
	if h, _ := m.Selected(); h.Alias != "plain" {
		t.Errorf("after three j: selected %q, want plain (divider skipped)", h.Alias)
	}
	m = press(t, m, "k")
	if h, _ := m.Selected(); h.Alias != "described" {
		t.Errorf("after k back: selected %q, want described", h.Alias)
	}
	if h, _ := press(t, m, "G").Selected(); h.Alias != "office" {
		t.Errorf("G selected %q, want office", h.Alias)
	}
	if h, _ := press(t, m, "G", "g").Selected(); h.Alias != "web" {
		t.Errorf("g selected %q, want web", h.Alias)
	}
	// Both tiers work: the ambient host previews its ssh like any other.
	if view := press(t, m, "G").View(); !strings.Contains(view, "$ ssh office") {
		t.Errorf("View() on an ambient host should preview ssh:\n%s", view)
	}
	// The ambient rows — and only they — carry the dim style, in both the
	// unfiltered and the filtered row builds.
	if got := ui.AmbientRows(m); !reflect.DeepEqual(got, []string{"plain", "office"}) {
		t.Errorf("AmbientRows() = %v, want [plain office]", got)
	}
	if got := ui.AmbientRows(press(t, m, "/", "e")); !reflect.DeepEqual(got, []string{"plain", "office"}) {
		t.Errorf("AmbientRows() under a filter = %v, want [plain office]", got)
	}
	// A single tier has no divider, no split, and nothing dimmed: there
	// is nothing to contrast with.
	one := tierModel(t, tierHosts()[:1])
	if view := one.View(); strings.Contains(view, "other entries ─") || strings.Contains(view, "managed)") {
		t.Errorf("View() with one tier should have no divider or split:\n%s", view)
	}
	if got := ui.AmbientRows(one); len(got) != 0 {
		t.Errorf("AmbientRows() with only ambient hosts = %v, want none dimmed", got)
	}
}

func TestModel_Update_TiersUnderFilter(t *testing.T) {
	t.Parallel()
	m := tierModel(t, tierHosts())
	// A term searches both tiers, drops the divider, keeps hidden out.
	f := press(t, m, "/", "e")
	view := f.View()
	if strings.Contains(view, "other entries ─") || strings.Contains(view, "secret") || !strings.Contains(view, "office") || !strings.Contains(view, "web") {
		t.Errorf("View() under a filter term:\n%s", view)
	}
	// Naming the hidden host's whole alias reveals it.
	f = press(t, m, "/", "s", "e", "c", "r", "e", "t")
	if h, ok := f.Selected(); !ok || h.Alias != "secret" {
		t.Errorf("filtering by the hidden alias should reveal it as a row: selected %q, ok %v:\n%s", h.Alias, ok, f.View())
	}
	// A prefix does not.
	f = press(t, m, "/", "s", "e", "c", "r")
	if _, ok := f.Selected(); ok {
		t.Errorf("filtering by a prefix should not reveal a hidden host:\n%s", f.View())
	}
	// Back out of the filter: the tiers and the divider return.
	f = press(t, f, "esc")
	if view := f.View(); !strings.Contains(view, "other entries ─") {
		t.Errorf("View() after esc should show the divider again:\n%s", view)
	}
	// A wide pane carries the full wording.
	wide := update(t, m, tea.WindowSizeMsg{Width: 160, Height: 24})
	if view := wide.View(); !strings.Contains(view, "other ssh_config entries ─") {
		t.Errorf("View() at 160 cols should spell the divider out:\n%s", view)
	}
}

func TestModel_View_AmbientStyleFromPalette(t *testing.T) {
	t.Parallel()
	for _, th := range ui.Themes {
		m := ui.New(tierHosts(), probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{Theme: th.Name})
		st := ui.StylesOf(m)
		if st.ItemAmbient.GetForeground() != th.Palette.FgDim || st.Divider.GetForeground() != th.Palette.FgDim {
			t.Errorf("%s: ambient/divider foreground = %v / %v, want FgDim %v", th.Name, st.ItemAmbient.GetForeground(), st.Divider.GetForeground(), th.Palette.FgDim)
		}
	}
}

func TestModel_Update_EditOpensTheDeclaringFile(t *testing.T) {
	t.Parallel()
	// Each subtest drives its own model: the list is shared state.
	warned := func(t *testing.T) ui.Model {
		t.Helper()
		return ui.WithEditorWarn(tierModel(t, tierHosts()), "sasash.conf is not wired into ssh yet — run `sasash setup`")
	}
	tests := []struct {
		name     string
		moves    []string
		wantFile string
		wantWarn bool
	}{
		{name: "managed by file opens sasash.conf", moves: nil, wantFile: confPath, wantWarn: true},
		{name: "managed by meta opens its own file", moves: []string{"j", "j"}, wantFile: "/home/u/.ssh/config"},
		{name: "ambient in the root config", moves: []string{"j", "j", "j"}, wantFile: "/home/u/.ssh/config"},
		{name: "ambient in an included file", moves: []string{"G"}, wantFile: "/home/u/.ssh/work.conf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := press(t, warned(t), tt.moves...)
			if got := ui.EditTarget(m); got != tt.wantFile {
				t.Errorf("EditTarget() = %q, want %q", got, tt.wantFile)
			}
			if got := ui.EditorExec(m).Args; !reflect.DeepEqual(got, []string{"vi", tt.wantFile}) {
				t.Errorf("EditorExec().Args = %v, want vi %s", got, tt.wantFile)
			}
			next, cmd := m.Update(keyMsg("e"))
			if cmd == nil {
				t.Fatalf("Update(e) cmd = nil, want the editor handoff")
			}
			next, _ = next.(ui.Model).Update(ui.EditorExit(nil))
			if got := strings.Contains(next.(ui.Model).View(), "sasash setup"); got != tt.wantWarn {
				t.Errorf("setup warning shown = %v, want %v:\n%s", got, tt.wantWarn, next.(ui.Model).View())
			}
		})
	}
	// Settings' "edit config file" means sasash.conf whatever is selected.
	m := warned(t)
	s := press(t, m, "G", "o", "j")
	next, cmd := s.Update(keyMsg("enter"))
	if cmd == nil || ui.Editing(next.(ui.Model)) != confPath {
		t.Errorf("settings edit: cmd %v, editing %q; want sasash.conf", cmd != nil, ui.Editing(next.(ui.Model)))
	}
	if got := ui.SettingsEditExec(s).Args; !reflect.DeepEqual(got, []string{"vi", confPath}) {
		t.Errorf("SettingsEditExec().Args on an ambient host = %v, want vi %s", got, confPath)
	}
	// No host selected, or a host without a file: sasash.conf.
	if got := ui.EditTarget(tierModel(t, nil)); got != confPath {
		t.Errorf("EditTarget() with no hosts = %q, want sasash.conf", got)
	}
	if got := ui.EditTarget(tierModel(t, []hostmodel.Host{{Alias: "x", Paths: []probe.Path{{Host: "192.0.2.9"}}}})); got != confPath {
		t.Errorf("EditTarget() for a file-less host = %q, want sasash.conf", got)
	}
}

func TestModel_Update_ThemeUnderFilterKeepsTheRows(t *testing.T) {
	t.Parallel()
	// Cycling the theme under an applied filter must not blank the match
	// set (SetItems under a filter returns the only refill cmd).
	m := press(t, tierModel(t, tierHosts()), "/", "e", "enter")
	if _, ok := m.Selected(); !ok {
		t.Fatalf("filter applied but nothing selected:\n%s", m.View())
	}
	m = press(t, m, "t")
	if h, ok := m.Selected(); !ok || strings.Contains(m.View(), "no hosts match") {
		t.Errorf("after t under a filter: selected %q ok %v:\n%s", h.Alias, ok, m.View())
	}
	m = press(t, m, "o", "j", "j", "enter", "j", "enter") // settings → theme picker → pick
	if h, ok := m.Selected(); !ok || strings.Contains(m.View(), "no hosts match") {
		t.Errorf("after the theme picker under a filter: selected %q ok %v:\n%s", h.Alias, ok, m.View())
	}
}

func TestModel_Update_CrossingTheDividerFetchesStatus(t *testing.T) {
	t.Parallel()
	// Landing on a host across the divider is a selection change like any
	// other: its status is fetched, the old host's never shown as its own.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "plain")
	d := fakedial.New()
	hosts := []hostmodel.Host{
		{Alias: "web", Paths: []probe.Path{{Host: "192.0.2.1"}}, File: confPath},
		{Alias: "plain", Paths: []probe.Path{{Host: "192.0.2.4"}}, File: "/home/u/.ssh/config"},
	}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: time.Millisecond}, r, ui.Config{ConfPath: confPath, Tunnels: tunnelEngine(t, r, d)})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = feed(t, m, m.Init())
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("j"))
	m = next.(ui.Model)
	if h, _ := m.Selected(); h.Alias != "plain" || cmd == nil {
		t.Fatalf("j across the divider: selected %q, cmd %v; want plain and its status poll", h.Alias, cmd != nil)
	}
	m = feedAll(t, m, cmd)
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"ssh -O check plain"}) {
		t.Errorf("calls after crossing down = %v, want plain's poll", got)
	}
	before = len(r.CallKeys())
	next, cmd = m.Update(keyMsg("k"))
	m = feedAll(t, next.(ui.Model), cmd)
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"ssh -O check web"}) {
		t.Errorf("calls after crossing up = %v, want web's poll", got)
	}
}

func TestModel_Update_HideTakesEffectOnReload(t *testing.T) {
	t.Parallel()
	// Adding "# @sasash hide" in the editor: the host leaves the rows on
	// the reload the editor's return triggers.
	m := ui.WithEditor(tierModel(t, tierHosts()), []string{"vi"}, "", confPath)
	edited := tierHosts()
	edited[0].Hidden = true // plain
	m = reloadAndProbe(t, m, edited)
	if view := m.View(); strings.Contains(view, "plain") {
		t.Errorf("View() after a reload that hid plain still lists it:\n%s", view)
	}
}
