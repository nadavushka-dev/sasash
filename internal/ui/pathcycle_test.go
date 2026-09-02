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
	three := []probe.Path{pathLAN, pathTS, pathWAN}
	tests := []struct {
		name     string
		paths    []probe.Path
		hostName string
		want     probe.Path
		ok       bool
	}{
		{name: "after the current, wrapping", paths: three, hostName: "203.0.113.5", want: pathLAN, ok: true},
		{name: "case-insensitive host match", paths: three, hostName: "198.51.100.5", want: pathWAN, ok: true},
		{name: "no match starts at the first", paths: three, hostName: "192.0.2.99", want: pathLAN, ok: true},
		{name: "last match wins when two share a host", paths: []probe.Path{pathLAN, {Name: "lan2", Host: "192.0.2.10"}, pathTS}, hostName: "192.0.2.10", want: pathTS, ok: true},
		{name: "unnamed paths do not count", paths: []probe.Path{pathLAN, {Host: "192.0.2.77"}}, hostName: "192.0.2.10"},
		{name: "one named path", paths: []probe.Path{pathLAN}, hostName: "192.0.2.10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ui.NextPath(tt.paths, tt.hostName)
			if ok != tt.ok || (ok && !reflect.DeepEqual(got, tt.want)) {
				t.Errorf("NextPath() = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSwitchNotice_Renders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		res      pathswitch.Result
		named    int
		hostName string
		want     string
	}{
		{name: "one named path", res: pathswitch.Result{Alias: "web"}, named: 1, want: "only one named path — nothing to switch"},
		{name: "no named paths", res: pathswitch.Result{Alias: "web"}, named: 0, want: "no named paths — nothing to switch"},
		{name: "from to", res: pathswitch.Result{Alias: "web", From: pathLAN, To: pathTS, Changed: true}, named: 2, want: "HostName: lan 192.0.2.10:22 → ts 198.51.100.5:2222"},
		{name: "no prior match names the hand-edited HostName", res: pathswitch.Result{Alias: "web", To: pathLAN, Changed: true}, named: 2, hostName: "192.0.2.99", want: "HostName: 192.0.2.99 (no path) → lan 192.0.2.10:22"},
		{name: "unmanaged variants named", res: pathswitch.Result{Alias: "web", From: pathLAN, To: pathTS, Changed: true, Unmanaged: []string{"wan"}}, named: 2, want: "HostName: lan 192.0.2.10:22 → ts 198.51.100.5:2222 · 1 pinned variant left as-is (wan)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.SwitchNotice(tt.res, tt.named, tt.hostName); got != tt.want {
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
	m, switched, stamped, _ := switcherModelReads(t, r, res, swErr)
	return m, switched, stamped
}

// switcherModelReads also counts the block reads, and lets the block
// hook be steered: blocks maps an alias to its sasash.conf block; an
// alias absent from it is not managed.
func switcherModelReads(t *testing.T, r *fakerun.Fake, res pathswitch.Result, swErr error) (ui.Model, *[]string, *[][]string, *int) {
	t.Helper()
	// dual's HOST MODEL carries other paths than its sasash.conf block
	// (an earlier block in another included file wins the model): the
	// preview must follow the block, never the model.
	hosts := append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{{Name: "office", Host: "203.0.113.9"}, {Name: "vpn", Host: "203.0.113.10"}}})
	r.StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "dual")
	d := fakedial.New()
	eng := tunnelEngine(t, r, d)
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	var switched []string
	var stamped [][]string
	reads := 0
	m = ui.WithSwitcher(m,
		func(alias string) (pathswitch.Result, error) { switched = append(switched, alias); return res, swErr },
		func(alias string) ([]probe.Path, string, bool) {
			reads++
			switch alias {
			case "dual":
				// The block's own paths; the host model above disagrees.
				return []probe.Path{pathLAN, pathTS}, "192.0.2.10", true
			case "ubuntu":
				return []probe.Path{{Host: "192.0.2.10"}}, "192.0.2.10", true // one unnamed path
			case "bare":
				return []probe.Path{{Name: "only", Host: "192.0.2.20"}}, "192.0.2.20", true
			}
			return nil, "", false // spare: in the host model, not in sasash.conf
		},
		func(alias, path string) error { stamped = append(stamped, []string{alias, path}); return nil })
	return m, &switched, &stamped, &reads
}

func TestModel_View_SwitchPlan(t *testing.T) {
	t.Parallel()
	m, _, _, reads := switcherModelReads(t, fakerun.New(), pathswitch.Result{}, nil)
	// ubuntu's block has one unnamed path: nothing to cycle. bare has one
	// named path. spare has no block in sasash.conf.
	if view := m.View(); !strings.Contains(view, "p: no named paths") {
		t.Errorf("View() on ubuntu missing the no-named-paths plan:\n%s", view)
	}
	m = press(t, m, "j")
	if view := m.View(); !strings.Contains(view, "p: only one path") {
		t.Errorf("View() on bare missing the single-path plan:\n%s", view)
	}
	m = press(t, m, "j")
	if view := m.View(); !strings.Contains(view, "p: not managed by sasash") {
		t.Errorf("View() on spare missing the not-managed plan:\n%s", view)
	}
	m = press(t, m, "G") // dual: lan is current, so p goes to ts — by host, no guessed port
	if view := m.View(); !strings.Contains(view, "p → ts 198.51.100.5") || strings.Contains(view, "p → ts 198.51.100.5:") {
		t.Errorf("View() on dual missing the exact target by host:\n%s", view)
	}
	// The block is read once per alias per frame set, not on every frame.
	before := *reads
	m.View()
	m.View()
	if *reads != before {
		t.Errorf("block re-read on every frame: %d reads after two more frames", *reads-before)
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

func TestModel_Update_CyclePathOnUnmanagedHostCallsTheSwitcher(t *testing.T) {
	t.Parallel()
	// spare is in the host model but has no block in sasash.conf: the
	// preview says so, and p still asks the switcher, whose refusal is
	// the notice, verbatim.
	r := fakerun.New()
	m, switched, _ := switcherModel(t, r, pathswitch.Result{}, errors.New(`host "spare" is not managed by sasash: no Host block in /x/sasash.conf`))
	m = press(t, m, "j", "j") // spare
	next, cmd := m.Update(keyMsg("p"))
	m = update(t, next.(ui.Model), cmd())
	if !reflect.DeepEqual(*switched, []string{"spare"}) || !strings.Contains(m.View(), `path: host "spare" is not managed by sasash`) {
		t.Errorf("p on an unmanaged host: switched %v, view:\n%s", *switched, m.View())
	}
}

func TestModel_Update_CyclePathNeverFreesAPollsGate(t *testing.T) {
	t.Parallel()
	// dual's selection poll is in flight when p is pressed. The switch's
	// done arm must not release that poll's gate: no second poll and no
	// stamp until the first lands; then exactly one stamped poll.
	r := fakerun.New()
	m, _, stamped := switcherModel(t, r, pathswitch.Result{Alias: "dual", From: pathLAN, To: pathTS, Changed: true}, nil)
	next, poll := m.Update(keyMsg("G")) // dual: poll issued, not delivered
	m = next.(ui.Model)
	next, sw := m.Update(keyMsg("p"))
	m = next.(ui.Model)
	before := len(r.CallKeys())
	next, after := m.Update(sw())
	m = feedAll(t, next.(ui.Model), after) // reload + re-probe; no poll (gate held by poll #1)
	if got := r.CallKeys()[before:]; len(got) != 0 || len(*stamped) != 0 {
		t.Fatalf("with a poll in flight: calls %v, stamps %v; want none yet", got, *stamped)
	}
	m = feedAll(t, m, poll) // poll #1 lands: the stamped poll runs once
	if !reflect.DeepEqual(*stamped, [][]string{{"dual", "ts"}}) {
		t.Errorf("stamps after the poll landed = %v, want one switch stamp", *stamped)
	}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"ssh -O check dual", "ssh -O check dual"}) {
		t.Errorf("calls = %v, want poll #1 then the stamped poll", got)
	}
}

func TestModel_Update_CyclePathTwiceRunsOnce(t *testing.T) {
	t.Parallel()
	r := fakerun.New()
	m, switched, _ := switcherModel(t, r, pathswitch.Result{}, nil)
	next, sel := m.Update(keyMsg("G"))
	m = feed(t, next.(ui.Model), sel)
	next, first := m.Update(keyMsg("p"))
	m = next.(ui.Model)
	next, second := m.Update(keyMsg("p"))
	m = next.(ui.Model)
	if second != nil || !strings.Contains(m.View(), "already running") {
		t.Errorf("second p while a switch runs: cmd %v, view:\n%s", second != nil, m.View())
	}
	first()
	if !reflect.DeepEqual(*switched, []string{"dual"}) {
		t.Errorf("switch hook calls = %v, want exactly one", *switched)
	}
}

func TestModel_Update_QueuedStampsCompose(t *testing.T) {
	t.Parallel()
	// A handoff stamp waiting for an in-flight poll and a switch stamp
	// that arrives meanwhile both run on that poll — neither is lost.
	r := fakerun.New()
	m, _, stamped := switcherModel(t, r, pathswitch.Result{Alias: "dual", From: pathLAN, To: pathTS, Changed: true}, nil)
	var handoffs []string
	m = ui.WithRecordHandoff(m, func(alias string) error { handoffs = append(handoffs, alias); return nil })
	next, poll := m.Update(keyMsg("G")) // dual: poll in flight
	m = next.(ui.Model)
	next, _ = m.Update(keyMsg("enter"))
	next, back := next.(ui.Model).Update(ui.SSHExit(nil)) // handoff stamp queued
	m = feedAll(t, next.(ui.Model), back)
	next, sw := m.Update(keyMsg("p"))
	next, after := next.(ui.Model).Update(sw()) // switch stamp queued too
	m = feedAll(t, next.(ui.Model), after)
	if len(handoffs) != 0 || len(*stamped) != 0 {
		t.Fatalf("stamps ran before the poll landed: handoffs %v, switches %v", handoffs, *stamped)
	}
	m = feedAll(t, m, poll)
	if !reflect.DeepEqual(handoffs, []string{"dual"}) || !reflect.DeepEqual(*stamped, [][]string{{"dual", "ts"}}) {
		t.Errorf("after the poll: handoffs %v, switches %v; want both once", handoffs, *stamped)
	}
}

func TestModel_Update_BlockCacheRefreshesOnReload(t *testing.T) {
	t.Parallel()
	// The preview is cached per alias; a reload (an edit) re-reads it.
	r := fakerun.New()
	hostName := "192.0.2.10"
	hosts := append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS}})
	r.StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "dual")
	d := fakedial.New()
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d)})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	m = ui.WithSwitcher(m, func(string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, hostName, true }, nil)
	m = press(t, m, "G")
	if view := m.View(); !strings.Contains(view, "p → ts") {
		t.Fatalf("View() before the edit:\n%s", view)
	}
	hostName = "198.51.100.5" // a hand-edit switched it
	if view := m.View(); !strings.Contains(view, "p → ts") {
		t.Errorf("View() should still show the cached plan until a reload:\n%s", view)
	}
	// The switch itself reads the block fresh: its notice names what was
	// really there, not the cached HostName.
	hostName = "203.0.113.99" // hand-edited to match no path
	m = ui.WithSwitcher(m, func(string) (pathswitch.Result, error) {
		return pathswitch.Result{Alias: "dual", To: pathLAN, Changed: true}, nil
	}, func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, hostName, true }, nil)
	next, sw := m.Update(keyMsg("p"))
	next, _ = next.(ui.Model).Update(sw())
	if view := next.(ui.Model).View(); !strings.Contains(view, "HostName: 203.0.113.99 (no path) → lan") {
		t.Errorf("the notice should name the fresh pre-switch HostName:\n%s", view)
	}
	m = ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = reloadAndProbe(t, m, hosts)
	if view := m.View(); !strings.Contains(view, "p → lan") {
		t.Errorf("View() after the reload should re-read the block:\n%s", view)
	}
}

func TestModel_Update_CyclePathNeedsBothHooks(t *testing.T) {
	t.Parallel()
	// SwitchPath without PathBlock is half-wired: p is disabled rather
	// than a nil dereference on the key.
	m := polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New())
	m = ui.WithSwitcher(m, func(string) (pathswitch.Result, error) { return pathswitch.Result{}, nil }, nil, nil)
	if _, cmd := m.Update(keyMsg("p")); cmd != nil {
		t.Errorf("Update(p) with only SwitchPath wired cmd = %v, want nil", cmd)
	}
}
