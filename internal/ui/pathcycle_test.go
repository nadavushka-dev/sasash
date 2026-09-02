package ui_test

import (
	"errors"
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

var (
	pathLAN = probe.Path{Name: "lan", Host: "192.0.2.10"}
	pathTS  = probe.Path{Name: "ts", Host: "198.51.100.5", Port: "2222"}
	pathWAN = probe.Path{Name: "wan", Host: "203.0.113.5"}
)

func TestNextPath_Rule(t *testing.T) {
	t.Parallel()
	three := hostmodel.Host{Alias: "web", Paths: []probe.Path{pathLAN, pathTS, pathWAN}}
	tests := []struct {
		name     string
		host     hostmodel.Host
		hostName string
		want     probe.Path
		ok       bool
	}{
		{name: "after the current, wrapping", host: three, hostName: "203.0.113.5", want: pathLAN, ok: true},
		{name: "case-insensitive host match", host: three, hostName: "198.51.100.5", want: pathWAN, ok: true},
		{name: "no match starts at the first", host: three, hostName: "192.0.2.99", want: pathLAN, ok: true},
		{name: "last match wins when two share a host", host: hostmodel.Host{Paths: []probe.Path{pathLAN, {Name: "lan2", Host: "192.0.2.10"}, pathTS}}, hostName: "192.0.2.10", want: pathTS, ok: true},
		{name: "unnamed paths do not count", host: hostmodel.Host{Paths: []probe.Path{pathLAN, {Host: "192.0.2.77"}}}, hostName: "192.0.2.10"},
		{name: "one named path", host: hostmodel.Host{Paths: []probe.Path{pathLAN}}, hostName: "192.0.2.10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ui.NextPath(tt.host, tt.hostName)
			if ok != tt.ok || (ok && !reflect.DeepEqual(got, tt.want)) {
				t.Errorf("NextPath() = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSwitchNotice_Renders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		res  pathswitch.Result
		want string
	}{
		{name: "unchanged", res: pathswitch.Result{Alias: "web"}, want: "only one named path — nothing to switch"},
		{name: "from to", res: pathswitch.Result{Alias: "web", From: pathLAN, To: pathTS, Changed: true}, want: "HostName: lan 192.0.2.10:22 → ts 198.51.100.5:2222"},
		{name: "no prior match", res: pathswitch.Result{Alias: "web", To: pathLAN, Changed: true}, want: "HostName: ? → lan 192.0.2.10:22"},
		{name: "unmanaged variants named", res: pathswitch.Result{Alias: "web", From: pathLAN, To: pathTS, Changed: true, Unmanaged: []string{"wan"}}, want: "HostName: lan 192.0.2.10:22 → ts 198.51.100.5:2222 · 1 pinned variant left as-is (wan)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.SwitchNotice(tt.res); got != tt.want {
				t.Errorf("SwitchNotice() = %q, want %q", got, tt.want)
			}
		})
	}
}

// switcherModel is the tunnels fixture (ubuntu declares one unnamed path)
// with a second host, dual, that has two named paths, and the p hooks
// wired to fakes: hostName answers per alias, switch records calls.
func switcherModel(t *testing.T, r *fakerun.Fake, res pathswitch.Result, swErr error) (ui.Model, *[]string, *[][]string) {
	t.Helper()
	hosts := append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS}})
	r.StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "dual")
	d := fakedial.New()
	eng := tunnelEngine(t, r, d)
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	var switched []string
	var stamped [][]string
	m = ui.WithSwitcher(m,
		func(alias string) (pathswitch.Result, error) { switched = append(switched, alias); return res, swErr },
		func(alias string) (string, bool) {
			switch alias {
			case "dual":
				return "192.0.2.10", true
			case "ubuntu", "bare", "spare":
				return "192.0.2.10", true
			}
			return "", false
		},
		func(alias, path string) error { stamped = append(stamped, []string{alias, path}); return nil })
	return m, &switched, &stamped
}

func TestModel_View_SwitchPlan(t *testing.T) {
	t.Parallel()
	m, _, _ := switcherModel(t, fakerun.New(), pathswitch.Result{}, nil)
	// ubuntu has one unnamed path: p has nothing to cycle.
	if view := m.View(); !strings.Contains(view, "p: only one path") {
		t.Errorf("View() on ubuntu missing the single-path plan:\n%s", view)
	}
	m = press(t, m, "G") // dual: lan is current, so p goes to ts
	if view := m.View(); !strings.Contains(view, "p → ts 198.51.100.5:2222") {
		t.Errorf("View() on dual missing the exact target:\n%s", view)
	}
	// Under tunnels focus the host row's segments are not shown.
	m = press(t, m, "g", "l")
	if view := m.View(); strings.Contains(view, "p → ") || strings.Contains(view, "p: only") {
		t.Errorf("View() with a forward highlighted still previews p:\n%s", view)
	}
	// No hooks: no segment, and p does nothing.
	plain := polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New())
	if view := plain.View(); strings.Contains(view, "p →") || strings.Contains(view, "p: ") {
		t.Errorf("View() without a switcher previews p:\n%s", view)
	}
	if _, cmd := plain.Update(keyMsg("p")); cmd != nil {
		t.Errorf("Update(p) without a switcher cmd = %v, want nil", cmd)
	}
}

func TestModel_Update_CyclePathOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		res        pathswitch.Result
		err        error
		wantNotice string
		wantStamp  [][]string
		wantReload bool
	}{
		{
			name:       "switched: notice, stamp, reload",
			res:        pathswitch.Result{Alias: "dual", From: pathLAN, To: pathTS, Changed: true},
			wantNotice: "HostName: lan 192.0.2.10:22 → ts 198.51.100.5:2222",
			wantStamp:  [][]string{{"dual", "ts"}},
			wantReload: true,
		},
		{
			name:       "unmanaged variants are said",
			res:        pathswitch.Result{Alias: "dual", From: pathLAN, To: pathTS, Changed: true, Unmanaged: []string{"lan"}},
			wantNotice: "1 pinned variant left as-is (lan)",
			wantStamp:  [][]string{{"dual", "ts"}},
			wantReload: true,
		},
		{
			name:       "nothing to switch: no stamp, no reload",
			res:        pathswitch.Result{Alias: "dual"},
			wantNotice: "only one named path — nothing to switch",
		},
		{
			name:       "switcher error is the notice",
			err:        errors.New(`host "dual" is not managed by sasash: no Host block in sasash.conf`),
			wantNotice: "path: host \"dual\" is not managed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New()
			m, switched, stamped := switcherModel(t, r, tt.res, tt.err)
			loaded := false
			m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
				loaded = true
				return append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS}}), nil, nil
			})
			next, sel := m.Update(keyMsg("G")) // dual: deliver its selection poll first
			m = feed(t, next.(ui.Model), sel)
			next, cmd := m.Update(keyMsg("p"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(p) cmd = nil, want the switch")
			}
			// While the switch runs, the tick must not poll dual (one
			// writer per host).
			before := len(r.CallKeys())
			_, tick := m.Update(ui.RefreshTick())
			m = feed(t, m, tick)
			if got := r.CallKeys()[before:]; len(got) != 0 {
				t.Errorf("tick polled during the switch: %v", got)
			}
			next, after := m.Update(cmd())
			m = next.(ui.Model)
			if !reflect.DeepEqual(*switched, []string{"dual"}) {
				t.Errorf("switch hook calls = %v, want [dual]", *switched)
			}
			if view := m.View(); !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
			// The stamp is queued for the host's next poll, not written
			// from the UI thread beside a poll that may be observing.
			if len(*stamped) != 0 {
				t.Errorf("RecordSwitch ran before the poll: %v", *stamped)
			}
			m = feedAll(t, m, after) // reload (+ re-probe + poll) and the stamp
			if loaded != tt.wantReload {
				t.Errorf("reload = %v, want %v", loaded, tt.wantReload)
			}
			if !reflect.DeepEqual(*stamped, tt.wantStamp) && !(len(*stamped) == 0 && len(tt.wantStamp) == 0) {
				t.Errorf("RecordSwitch calls = %v, want %v", *stamped, tt.wantStamp)
			}
		})
	}
}

func TestModel_Update_CyclePathInert(t *testing.T) {
	t.Parallel()
	m, switched, _ := switcherModel(t, fakerun.New(), pathswitch.Result{}, nil)
	// Filtering: p is text. Settings open: p is nothing.
	f := press(t, m, "/", "p")
	if ui.FilterText(f) != "p" || len(*switched) != 0 {
		t.Errorf("p while filtering: text %q, switches %v", ui.FilterText(f), *switched)
	}
	s := press(t, m, "o", "p")
	if !ui.SettingsOpen(s) || len(*switched) != 0 {
		t.Errorf("p with settings open: open %v, switches %v", ui.SettingsOpen(s), *switched)
	}
	// Help lists it.
	if view := press(t, m, "?").View(); !strings.Contains(view, "cycle path") {
		t.Errorf("View() full help missing p:\n%s", view)
	}
}
