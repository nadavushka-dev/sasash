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

func TestActiveIndex_Rule(t *testing.T) {
	t.Parallel()
	three := []probe.Path{pathLAN, pathTS, pathWAN}
	tests := []struct {
		name     string
		rows     []probe.Path
		hostName string
		want     int
	}{
		{name: "the matching path", rows: three, hostName: "203.0.113.5", want: 2},
		{name: "case-insensitive host match", rows: three, hostName: "198.51.100.5", want: 1},
		{name: "no match is -1 (hand-edited HostName)", rows: three, hostName: "192.0.2.99", want: -1},
		{name: "hostnames match case-insensitively", rows: []probe.Path{pathLAN, {Name: "dns", Host: "Db.Example.Invalid"}}, hostName: "db.example.invalid", want: 1},
		{name: "last match wins when two share a host", rows: []probe.Path{pathLAN, {Name: "lan2", Host: "192.0.2.10"}, pathTS}, hostName: "192.0.2.10", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.ActiveIndex(tt.rows, tt.hostName); got != tt.want {
				t.Errorf("ActiveIndex() = %d, want %d", got, tt.want)
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
	// dual's HOST MODEL renders the same named paths its sasash.conf
	// block declares (the usual case); TestModel_View_SwitchPlan covers
	// a pane that renders another block's paths.
	hosts := append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS}})
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
		func(alias, name string) (pathswitch.Result, error) {
			switched = append(switched, alias+" "+name)
			return res, swErr
		},
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
	return ui.WithPreviewShown(m), &switched, &stamped, &reads
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
	m = press(t, m, "G") // dual: the pane's lan/ts are the block's too
	if view := m.View(); !strings.Contains(view, "p: pick lan / ts") {
		t.Errorf("View() on dual missing the pick plan:\n%s", view)
	}
	// A pane rendering another block's paths (a Host above the sasash
	// Include shadows it): nothing to offer, said on the line and at p.
	other := reloadAndProbe(t, ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf"), append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{{Name: "office", Host: "203.0.113.9"}, {Name: "vpn", Host: "203.0.113.10"}}}))
	other = press(t, other, "G")
	if view := other.View(); !strings.Contains(view, "p: pane rows are not sasash.conf's") {
		t.Errorf("View() with a shadowed block should say so:\n%s", view)
	}
	next, cmd := other.Update(keyMsg("p"))
	if cmd != nil || ui.PathsFocused(next.(ui.Model)) || !strings.Contains(next.(ui.Model).View(), "not sasash.conf's — e reloads") {
		t.Errorf("p with a shadowed block: cmd %v, focused %v:\n%s", cmd != nil, ui.PathsFocused(next.(ui.Model)), next.(ui.Model).View())
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
	if view := m.View(); strings.Contains(view, "p: pick") || strings.Contains(view, "p: only") {
		t.Errorf("View() with a forward highlighted still previews p:\n%s", view)
	}
	// No hooks: no segment, and p does nothing.
	plain := polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New())
	if view := plain.View(); strings.Contains(view, "p: ") {
		t.Errorf("View() without a switcher previews p:\n%s", view)
	}
	if _, cmd := plain.Update(keyMsg("p")); cmd != nil {
		t.Errorf("Update(p) without a switcher cmd = %v, want nil", cmd)
	}
}

// pick presses p and enter: the paths pane takes focus on the active
// row and enter makes it (or the row moved to) active through the hook.
func pick(t *testing.T, m ui.Model, moves ...string) (ui.Model, tea.Cmd) {
	t.Helper()
	m = press(t, m, "p")
	if !ui.PathsFocused(m) {
		t.Fatalf("Update(p) should focus the paths pane:\n%s", m.View())
	}
	m = press(t, m, moves...)
	next, cmd := m.Update(keyMsg("enter"))
	return next.(ui.Model), cmd
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
			m, cmd := pick(t, m, "j") // ts
			if cmd == nil {
				t.Fatalf("enter in the picker cmd = nil, want the switch")
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
			if !reflect.DeepEqual(*switched, []string{"dual ts"}) {
				t.Errorf("switch hook calls = %v, want [dual ts]", *switched)
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
	if view := press(t, m, "?").View(); !strings.Contains(view, "pick path") {
		t.Errorf("View() full help missing p:\n%s", view)
	}
}

func TestModel_Update_PickPathWithoutAChoice(t *testing.T) {
	t.Parallel()
	// No picker without two named paths: the cycle's own no-op wording
	// is the notice, the hook is never asked, nothing is written.
	tests := []struct {
		name       string
		moves      []string
		wantNotice string
	}{
		{name: "unmanaged host", moves: []string{"j", "j"}, wantNotice: "p: not managed by sasash"},
		{name: "one unnamed path", moves: nil, wantNotice: "no named paths — nothing to switch"},
		{name: "one named path", moves: []string{"j"}, wantNotice: "only one named path — nothing to switch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, switched, _ := switcherModel(t, fakerun.New(), pathswitch.Result{}, nil)
			m = press(t, m, tt.moves...)
			next, cmd := m.Update(keyMsg("p"))
			m = next.(ui.Model)
			if cmd != nil || ui.PathsFocused(m) || len(*switched) != 0 || !strings.Contains(m.View(), tt.wantNotice) {
				t.Errorf("p: cmd %v, picker %v, switched %v:\n%s", cmd != nil, ui.PathsFocused(m), *switched, m.View())
			}
		})
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
	m, sw := pick(t, m, "j")
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
	m, first := pick(t, m)
	next, second := m.Update(keyMsg("p"))
	m = next.(ui.Model)
	if second != nil || ui.PathsFocused(m) || !strings.Contains(m.View(), "already running") {
		t.Errorf("second p while a switch runs: cmd %v, picker %v, view:\n%s", second != nil, ui.PathsFocused(m), m.View())
	}
	first()
	if !reflect.DeepEqual(*switched, []string{"dual lan"}) {
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
	m, sw := pick(t, m, "j")
	next, after := m.Update(sw()) // switch stamp queued too
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
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, hostName, true }, nil)
	m = press(t, m, "G")
	// p marks the block's active row: lan.
	if view := press(t, m, "p").View(); !strings.Contains(view, "› ○ lan 192.0.2.10:22 down") {
		t.Fatalf("View() before the edit should mark lan:\n%s", view)
	}
	hostName = "198.51.100.5" // a hand-edit switched it
	// p reads the block against disk, not the render cache: the mark
	// lands on what is really active now.
	if view := press(t, m, "p").View(); !strings.Contains(view, "› ○ ts  198.51.100.5:2222 down") {
		t.Errorf("View() after a hand-edit should mark the fresh active row:\n%s", view)
	}
	// The switch itself reads the block fresh: its notice names what was
	// really there, not the cached HostName.
	hostName = "203.0.113.99" // hand-edited to match no path
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) {
		return pathswitch.Result{Alias: "dual", To: pathLAN, Changed: true}, nil
	}, func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, hostName, true }, nil)
	picked, sw := pick(t, m)
	next, _ := picked.Update(sw())
	if view := next.(ui.Model).View(); !strings.Contains(view, "HostName: 203.0.113.99 (no path) → lan") {
		t.Errorf("the notice should name the fresh pre-switch HostName:\n%s", view)
	}
	m = ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = reloadAndProbe(t, m, hosts)
	// Re-read: the hand-edited HostName matches no row, so the mark
	// starts on the first row and the plan does not call it active.
	if view := press(t, m, "p").View(); !strings.Contains(view, "› ○ lan") || strings.Contains(view, "already active") {
		t.Errorf("View() after the reload should re-read the block:\n%s", view)
	}
}

func TestModel_Update_CyclePathNeedsBothHooks(t *testing.T) {
	t.Parallel()
	// SwitchPath without PathBlock is half-wired: p is disabled rather
	// than a nil dereference on the key.
	m := polled(t, fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu"), fakedial.New())
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil }, nil, nil)
	if next, cmd := m.Update(keyMsg("p")); cmd != nil || ui.PathsFocused(next.(ui.Model)) {
		t.Errorf("Update(p) with only SetPath wired: cmd %v, picker %v; want neither", cmd != nil, ui.PathsFocused(next.(ui.Model)))
	}
}

// pickerModel is dual with its host model and block in agreement (lan
// up at 5ms, ts down), so the picker rows carry live probes.
func pickerModel(t *testing.T, r *fakerun.Fake) (ui.Model, *[]string) {
	t.Helper()
	hosts := append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS, {Host: "203.0.113.7"}}})
	r.StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "dual")
	d := fakedial.New().Up("192.0.2.10:22", 5*time.Millisecond).Down("198.51.100.5:2222").Down("203.0.113.7:22")
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
			if alias == "dual" {
				return []probe.Path{pathLAN, pathTS, {Host: "203.0.113.7"}}, "192.0.2.10", true
			}
			return nil, "", false
		}, nil)
	return ui.WithPreviewShown(press(t, m, "G")), &switched
}

func TestModel_View_PathsFocus(t *testing.T) {
	t.Parallel()
	m, _ := pickerModel(t, fakerun.New())
	plain := m.View()
	m = press(t, m, "p")
	view := m.View()
	// The rows are the ones already rendered — glyph, latency, active
	// marker in place — with the mark on the active row; the unnamed
	// path keeps its line but is never marked; the plan says so; the
	// list dims; the help is the pane's.
	for _, want := range []string{"› ● lan 192.0.2.10:22 5ms ← active", "  ○ ts  198.51.100.5:2222 down", "203.0.113.7:22 down", "p → lan 192.0.2.10  (already active — variants regenerate)", "enter make active", "esc/h keep current"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() paths focus missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "$ ssh dual") || strings.Contains(view, "› dual") {
		t.Errorf("View() paths focus should replace the ssh line and dim the list's mark:\n%s", view)
	}
	if !strings.Contains(plain, "$ ssh dual") || !strings.Contains(plain, "› dual") {
		t.Errorf("View() before p:\n%s", plain)
	}
	// Moving changes the plan; the port is not guessed.
	m = press(t, m, "j")
	if view := m.View(); !strings.Contains(view, "› ○ ts") || !strings.Contains(view, "p → ts 198.51.100.5") || strings.Contains(view, "p → ts 198.51.100.5:") || strings.Contains(view, "already active") {
		t.Errorf("View() after j:\n%s", view)
	}
	// The active marker follows the block's HostName, not the mark.
	if view := m.View(); !strings.Contains(view, "● lan 192.0.2.10:22 5ms ← active") || strings.Count(view, "← active") != 1 {
		t.Errorf("View() after j should keep ← active on lan only:\n%s", view)
	}
	// j clamps at the last named row (the unnamed path is not a stop);
	// g/G/k bound the mark.
	if view := press(t, m, "j", "j").View(); !strings.Contains(view, "› ○ ts") {
		t.Errorf("View() after j past the end should stay on ts:\n%s", view)
	}
	if view := press(t, m, "j", "j", "k", "G", "g").View(); !strings.Contains(view, "› ● lan") {
		t.Errorf("View() after g:\n%s", view)
	}
	if view := press(t, m, "g", "k", "k").View(); !strings.Contains(view, "› ● lan") || !strings.Contains(view, "p → lan") {
		t.Errorf("View() after k past the top should stay on lan:\n%s", view)
	}
	// A rendered row is offered when the block declares its NAME — the
	// hosts may differ (Set works by name; the block's wins at write).
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) {
			return []probe.Path{pathLAN, {Name: "ts", Host: "203.0.113.50"}}, "192.0.2.10", true
		}, nil)
	// The pane keeps its own address on the row; the plan names the
	// block's, which is what enter writes.
	if view := press(t, m, "esc", "p", "j").View(); !strings.Contains(view, "› ○ ts  198.51.100.5:2222") || !strings.Contains(view, "p → ts 203.0.113.50") {
		t.Errorf("View() should offer ts by name and plan the block's address:\n%s", view)
	}
	// With the block's HostName on that address, ts is the active row.
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) {
			return []probe.Path{pathLAN, {Name: "ts", Host: "203.0.113.50"}}, "203.0.113.50", true
		}, nil)
	if view := press(t, m, "esc", "p").View(); !strings.Contains(view, "› ○ ts  198.51.100.5:2222 down ← active") || !strings.Contains(view, "already active") {
		t.Errorf("View() should judge the active row by the block's address:\n%s", view)
	}
	// Exactly one rendered row the block declares is not a choice.
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, "192.0.2.10", true }, nil)
	one := reloadAndProbe(t, press(t, m, "esc"), append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathWAN}}))
	next, cmd := press(t, one, "G").Update(keyMsg("p"))
	if cmd != nil || ui.PathsFocused(next.(ui.Model)) || !strings.Contains(next.(ui.Model).View(), "not sasash.conf's") {
		t.Errorf("p with one declared row rendered: cmd %v, focused %v:\n%s", cmd != nil, ui.PathsFocused(next.(ui.Model)), next.(ui.Model).View())
	}
	// The mark starts on the active row even when it is not the first.
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, "198.51.100.5", true }, nil)
	m = press(t, m, "esc")
	m = reloadAndProbe(t, m, append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS, pathWAN}}))
	view = press(t, m, "G", "p").View()
	for _, want := range []string{"› ○ ts  198.51.100.5:2222 down", "  ○ wan 203.0.113.5:22 down"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() paths focus missing %q:\n%s", want, view)
		}
	}
	// An unnamed path ahead of the named ones keeps its line and is
	// never a stop: the mark lands on the rendered row, not the nth line.
	m = reloadAndProbe(t, m, append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{{Host: "203.0.113.7"}, pathLAN, pathTS}}))
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, "198.51.100.5", true }, nil)
	view = press(t, m, "G", "p").View()
	if !strings.Contains(view, "  ○     203.0.113.7:22 down") || !strings.Contains(view, "› ○ ts  198.51.100.5:2222 down") {
		t.Errorf("View() with an unnamed path first should mark ts on its own line:\n%s", view)
	}
	// Probe-pending rows keep their pending glyph under the mark: before
	// the first round lands, the mark sits on a "◌ …" row.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-O", "check", "dual")
	d := fakedial.New()
	fresh := ui.New(append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS}}), probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d)})
	fresh = update(t, fresh, tea.WindowSizeMsg{Width: 120, Height: 30})
	fresh = ui.WithSwitcher(fresh, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, "192.0.2.10", true }, nil)
	if view := press(t, fresh, "G", "p").View(); !strings.Contains(view, "› ◌ lan 192.0.2.10:22 …") {
		t.Errorf("View() paths focus before the first probe should mark a pending row:\n%s", view)
	}
}

func TestModel_Update_PathsFocusKeys(t *testing.T) {
	t.Parallel()
	m, switched := pickerModel(t, fakerun.New())
	// esc and h return to the list: nothing written, the ssh line is back.
	for _, back := range []string{"esc", "h"} {
		m := press(t, m, "p", "j", back)
		if ui.PathsFocused(m) || len(*switched) != 0 || !strings.Contains(m.View(), "$ ssh dual") || !strings.Contains(m.View(), "› dual") {
			t.Errorf("%s: focused %v, switched %v:\n%s", back, ui.PathsFocused(m), *switched, m.View())
		}
	}
	// Navigation and enter are the pane's; action keys keep their
	// meanings, as under the tunnels focus: / filters, o opens settings.
	m = press(t, m, "p")
	for _, k := range []string{"j", "k", "g", "G", "ctrl+d", "ctrl+u"} {
		next, cmd := m.Update(keyMsg(k))
		if cmd != nil || !ui.PathsFocused(next.(ui.Model)) {
			t.Errorf("key %q under paths focus: cmd %v, focused %v", k, cmd != nil, ui.PathsFocused(next.(ui.Model)))
		}
	}
	if f := press(t, m, "/"); ui.FilterState(f).String() != "filtering" {
		t.Errorf("/ under paths focus should start filtering")
	}
	if s := press(t, m, "o"); !ui.SettingsOpen(s) {
		t.Errorf("o under paths focus should open settings")
	}
	// A host change drops the focus (settleFocus): k is the pane's key
	// here, so the change comes through the filter.
	if f := press(t, m, "/", "u", "b", "enter"); ui.PathsFocused(f) {
		t.Errorf("selecting another host should drop the paths focus:\n%s", f.View())
	}
	// enter on a row goes through the same pipeline as before: the
	// in-flight marker, then the notice, the stamp, the reload.
	m, cmd := pick(t, m, "j")
	if cmd == nil || ui.PathsFocused(m) {
		t.Fatalf("enter: cmd %v, focused %v", cmd != nil, ui.PathsFocused(m))
	}
	next, _ := m.Update(keyMsg("p"))
	if !strings.Contains(next.(ui.Model).View(), "already running") {
		t.Errorf("p during the switch should say so:\n%s", next.(ui.Model).View())
	}
	next, _ = m.Update(cmd())
	m = next.(ui.Model)
	if !reflect.DeepEqual(*switched, []string{"dual ts"}) || !strings.Contains(m.View(), "HostName: lan 192.0.2.10:22 → ts 198.51.100.5:2222") {
		t.Errorf("switched %v, view:\n%s", *switched, m.View())
	}
	// Live probes keep landing while the pane holds focus.
	m = press(t, m, "p")
	next, tick := m.Update(ui.RefreshTick())
	m = feed(t, next.(ui.Model), tick)
	if !ui.PathsFocused(m) || !strings.Contains(m.View(), "› ● lan 192.0.2.10:22 5ms") {
		t.Errorf("paths focus after a refresh: focused %v:\n%s", ui.PathsFocused(m), m.View())
	}
	// The full help renders the pane's own keys.
	if view := press(t, m, "?").View(); !strings.Contains(view, "make active") || !strings.Contains(view, "keep current") {
		t.Errorf("View() full help under paths focus:\n%s", view)
	}
	// A reload lands while focused: the rows are re-read; focus survives
	// only while the host is the same with named rows.
	m = press(t, m, "?")
	m = reloadAndProbe(t, m, append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS}}))
	if !ui.PathsFocused(m) {
		t.Errorf("paths focus should survive a reload of the same host:\n%s", m.View())
	}
	m = reloadAndProbe(t, m, append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{{Host: "192.0.2.10"}}}))
	if ui.PathsFocused(m) {
		t.Errorf("paths focus should drop when the named rows are gone:\n%s", m.View())
	}
}

func TestModel_Update_PathsFocusRowGoneBeforeEnter(t *testing.T) {
	t.Parallel()
	// The path marked was removed from the file between p and enter: Set
	// re-reads and refuses, and its refusal is the notice, verbatim.
	r := fakerun.New()
	m, switched := pickerModel(t, r)
	m = ui.WithSwitcher(m, func(alias, name string) (pathswitch.Result, error) {
		*switched = append(*switched, alias+" "+name)
		return pathswitch.Result{}, errors.New(`host "dual" has no path "ts" (have lan, wan)`)
	}, func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, "192.0.2.10", true }, nil)
	m, cmd := pick(t, m, "j")
	next, _ := m.Update(cmd())
	m = next.(ui.Model)
	if !reflect.DeepEqual(*switched, []string{"dual ts"}) || !strings.Contains(m.View(), `path: host "dual" has no path "ts" (have lan, wan)`) {
		t.Errorf("switched %v, view:\n%s", *switched, m.View())
	}
}

func TestModel_View_PathsFocusActiveIsTheBlocksRow(t *testing.T) {
	t.Parallel()
	// ← active names the path ssh ACTUALLY dials — the block's HostName row
	// — in AND out of the focus, even when a DIFFERENT path is the first
	// reachable one (SASASH.7.2: the marker must never claim ssh goes to a
	// reachable path it will not; that lie was why tunnels to a drifted
	// HostName silently timed out). Here HostName is ts's address, so ts is
	// active though it is DOWN and lan is the reachable path.
	m, _ := pickerModel(t, fakerun.New()) // lan up, ts down
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, "198.51.100.5", true }, nil)
	if view := m.View(); !strings.Contains(view, "○ ts  198.51.100.5:2222 down ← active") || strings.Contains(view, "lan 192.0.2.10:22 5ms ← active") || strings.Count(view, "← active") != 1 {
		t.Fatalf("View() should mark the HostName row (ts, down) active, not the reachable lan:\n%s", view)
	}
	m = press(t, m, "p")
	view := m.View()
	if !strings.Contains(view, "› ○ ts  198.51.100.5:2222 down ← active") || strings.Count(view, "← active") != 1 || !strings.Contains(view, "already active") {
		t.Errorf("View() under p should mark the block's HostName row as active:\n%s", view)
	}
	if view := press(t, m, "esc").View(); !strings.Contains(view, "○ ts  198.51.100.5:2222 down ← active") || strings.Contains(view, "lan 192.0.2.10:22 5ms ← active") {
		t.Errorf("View() after esc should still mark the HostName row (ts) active, not the reachable lan:\n%s", view)
	}
	// A HostName that matches no path: no active marker in the focus, the
	// plan says so, the mark starts on the first row. (p evicts the block
	// cache, so the fresh HostName drives it.)
	m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(string) ([]probe.Path, string, bool) { return []probe.Path{pathLAN, pathTS}, "203.0.113.99", true }, nil)
	view = press(t, m, "esc", "p").View()
	if strings.Contains(view, "← active") || !strings.Contains(view, "› ● lan") || !strings.Contains(view, "(HostName 203.0.113.99 matches no named path)") {
		t.Errorf("View() with a HostName matching no path:\n%s", view)
	}
}

func TestModel_Update_PathsFocusOwnersAndEsc(t *testing.T) {
	t.Parallel()
	m, switched := pickerModel(t, fakerun.New())
	// A keyboard owner describes enter, not the pane: settings, a prompt
	// (+ keeps its meaning under the focus) and the filter each show
	// their own help; the pane's never leaks under them.
	if view := press(t, m, "p", "o").View(); strings.Contains(view, "make active") || !strings.Contains(view, "enter select") {
		t.Errorf("View() with settings over the paths focus should show the settings help:\n%s", view)
	}
	if view := press(t, m, "p", "+").View(); strings.Contains(view, "make active") || !strings.Contains(view, "enter add") {
		t.Errorf("View() with a prompt over the paths focus should show the prompt's help:\n%s", view)
	}
	if f := press(t, m, "p", "/"); ui.PathsFocused(f) || strings.Contains(f.View(), "make active") || !strings.Contains(f.View(), "apply filter") {
		t.Errorf("/ drops the paths focus and shows the filter's help: focused %v:\n%s", ui.PathsFocused(f), f.View())
	}
	// esc folds an expanded ? first and keeps the focus; the next esc
	// returns to the list.
	m = press(t, m, "p", "?")
	m = press(t, m, "esc")
	if !ui.PathsFocused(m) || strings.Contains(m.View(), "host list") {
		t.Errorf("first esc should fold the help and keep the focus: focused %v:\n%s", ui.PathsFocused(m), m.View())
	}
	m = press(t, m, "esc")
	if ui.PathsFocused(m) || len(*switched) != 0 {
		t.Errorf("second esc should return to the list: focused %v, switched %v", ui.PathsFocused(m), *switched)
	}
	// p under the focus is idempotent: the mark stays where it was.
	m = press(t, m, "p", "j", "p")
	if view := m.View(); !ui.PathsFocused(m) || !strings.Contains(view, "› ○ ts") {
		t.Errorf("p under the focus should keep the mark:\n%s", view)
	}
}

func TestModel_Update_PathsFocusShrinkClamp(t *testing.T) {
	t.Parallel()
	// The mark sits on the third row; a reload drops that path: the mark
	// clamps onto the last remaining row and enter acts on it, never past
	// the slice.
	r := fakerun.New()
	m, switched := pickerModel(t, r)
	three := func(string) ([]probe.Path, string, bool) {
		return []probe.Path{pathLAN, pathTS, pathWAN}, "192.0.2.10", true
	}
	m = ui.WithSwitcher(m, func(alias, name string) (pathswitch.Result, error) {
		*switched = append(*switched, alias+" "+name)
		return pathswitch.Result{Alias: alias, From: pathLAN, To: pathTS, Changed: true}, nil
	}, three, nil)
	m = reloadAndProbe(t, ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf"), append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS, pathWAN}}))
	m = press(t, m, "G", "p", "G")
	if view := m.View(); !strings.Contains(view, "› ○ wan") {
		t.Fatalf("View() should mark wan:\n%s", view)
	}
	m = reloadAndProbe(t, m, append(tunnelHosts(), hostmodel.Host{Alias: "dual", Paths: []probe.Path{pathLAN, pathTS}}))
	if view := m.View(); !ui.PathsFocused(m) || !strings.Contains(view, "› ○ ts") || !strings.Contains(view, "p → ts") {
		t.Errorf("the mark should clamp onto the last remaining row:\n%s", view)
	}
	next, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Fatalf("enter after the clamp cmd = nil")
	}
	cmd()
	if !reflect.DeepEqual(*switched, []string{"dual ts"}) || ui.PathsFocused(next.(ui.Model)) {
		t.Errorf("switched %v (want [dual ts]), focused %v", *switched, ui.PathsFocused(next.(ui.Model)))
	}
}

// TestModel_View_ActiveIsHostNameNotFirstReachable is SASASH.7.2's core
// regression: ssh dials the block HostName, so the ← active marker is the
// path whose address equals it — even when that path is DOWN and another is
// reachable. The old first-reachable marker lied, and `ssh <alias>` (every
// tunnel included) then silently timed out against the drifted HostName.
func TestModel_View_ActiveIsHostNameNotFirstReachable(t *testing.T) {
	t.Parallel()
	hosts := []hostmodel.Host{{
		Alias:    "ubuntu1",
		HostName: "203.0.113.5", // the tailscale address — and it is down
		Paths: []probe.Path{
			{Name: "office", Host: "192.0.2.10"},
			{Name: "tailscale", Host: "203.0.113.5"},
		},
	}}
	d := fakedial.New().Up("192.0.2.10:22", 5*time.Millisecond).Down("203.0.113.5:22")
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = feed(t, m, m.Init())
	view := m.View()
	if !strings.Contains(view, "tailscale") || !strings.Contains(view, "down ← active") {
		t.Errorf("the down HostName path (tailscale) must be marked active:\n%s", view)
	}
	if strings.Contains(view, "5ms ← active") {
		t.Errorf("the reachable non-HostName path (office) must NOT be active:\n%s", view)
	}
	if n := strings.Count(view, "← active"); n != 1 {
		t.Errorf("exactly one active path, got %d:\n%s", n, view)
	}
	// A HostName matching no path marks NONE active — never a lie.
	hosts[0].HostName = "198.51.100.99"
	m2 := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
	m2 = update(t, m2, tea.WindowSizeMsg{Width: 120, Height: 24})
	m2 = feed(t, m2, m2.Init())
	if view := m2.View(); strings.Contains(view, "← active") {
		t.Errorf("a HostName matching no path must mark none active:\n%s", view)
	}
	// A SINGLE @sasash path whose HostName has drifted: none active, not the
	// lone path — the honest signal even though there is only one row and the
	// switcher cannot offer p (SASASH.7.2's stuck case).
	single := []hostmodel.Host{{Alias: "solo", HostName: "203.0.113.5", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}}}}
	m3 := ui.New(single, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
	m3 = update(t, m3, tea.WindowSizeMsg{Width: 120, Height: 24})
	m3 = feed(t, m3, m3.Init())
	if view := m3.View(); strings.Contains(view, "← active") {
		t.Errorf("a single named path not matching the drifted HostName must not be active:\n%s", view)
	}
}
