package ui_test

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/stats"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// fakeClock is a settable time source shared by the ui and the store, so
// every duration is exact and no test sleeps.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

var t0 = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func TestSince_Spellings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{name: "seconds", ago: 42 * time.Second, want: "42s"},
		{name: "minutes", ago: 12*time.Minute + 30*time.Second, want: "12m"},
		{name: "hours and minutes", ago: 63 * time.Minute, want: "1h03m"},
		{name: "days and hours", ago: 52 * time.Hour, want: "2d04h"},
		{name: "future is unknown", ago: -time.Minute, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.Since(t0, t0.Add(-tt.ago)); got != tt.want {
				t.Errorf("Since(%v ago) = %q, want %q", tt.ago, got, tt.want)
			}
		})
	}
	// The zero time is unknown, never "0s".
	if got := ui.Since(t0, time.Time{}); got != "" {
		t.Errorf("Since(zero) = %q, want empty", got)
	}
}

func TestSparkline_Shapes(t *testing.T) {
	t.Parallel()
	ms := func(vs ...int) []time.Duration {
		out := make([]time.Duration, len(vs))
		for i, v := range vs {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	tests := []struct {
		name      string
		ring      []time.Duration
		want      string
		wantRange string
	}{
		{name: "rising", ring: ms(10, 20, 30, 40, 50), want: "▁▂▄▆█", wantRange: "10ms–50ms"},
		{name: "flat sits low", ring: ms(20, 20, 20), want: "▁▁▁", wantRange: "20ms"},
		{name: "misses are dots", ring: append(ms(10), -1, time.Duration(50)*time.Millisecond), want: "▁·█", wantRange: "10ms–50ms"},
		{name: "all misses", ring: []time.Duration{-1, -1}, want: "··", wantRange: ""},
		{name: "empty", ring: nil, want: "", wantRange: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.Sparkline(tt.ring); got != tt.want {
				t.Errorf("Sparkline() = %q, want %q", got, tt.want)
			}
			if got := ui.LatencyRange(tt.ring); got != tt.wantRange {
				t.Errorf("LatencyRange() = %q, want %q", got, tt.wantRange)
			}
		})
	}
}

// statsModel builds the tunnels fixture over a real engine whose Stats
// store and the ui share one fake clock.
func statsModel(t *testing.T, r *fakerun.Fake, d *fakedial.Fake, clock *fakeClock) (ui.Model, stats.Store) {
	t.Helper()
	return statsModelOurs(t, r, d, clock, false)
}

// statsModelOurs is statsModel with, when ours, web's first forward
// recorded as our live pid 41 (its port must be up on d to read up).
func statsModelOurs(t *testing.T, r *fakerun.Fake, d *fakedial.Fake, clock *fakeClock, ours bool) (ui.Model, stats.Store) {
	t.Helper()
	dir := t.TempDir()
	store := stats.Store{Dir: dir, Clock: clock.now}
	sp := fakespawn.New().Dial(d)
	if ours {
		writeState(t, dir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41}]}`)
		sp.Live(41)
	}
	eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: dir, Timeout: 50 * time.Millisecond, Stats: &store}
	m := ui.New(tunnelHosts(), probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng, Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	return m, store
}

func TestModel_View_HeaderSummary(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond).Up("127.0.0.1:3000", time.Millisecond)
	m, _ := statsModelOurs(t, r, d, clock, true)
	// Before the first round the tally is unknown.
	if view := m.View(); !strings.Contains(view, "3 hosts · probing…") {
		t.Errorf("View() before probing missing the pending tally:\n%s", view)
	}
	m = feed(t, m, m.Init())
	if view := m.View(); !strings.Contains(view, "3 hosts · 1 reachable · 1 tunnel up (1 polled)") {
		t.Errorf("View() missing the tally:\n%s", view)
	}
	// No engine: the tunnels part is absent, never a guessed zero.
	plain := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 100, Height: 24})
	plain = feed(t, plain, plain.Init())
	if view := plain.View(); !strings.Contains(view, "2 hosts · 0 reachable") || strings.Contains(view, "tunnel") {
		t.Errorf("View() without an engine should tally hosts only:\n%s", view)
	}
}

func TestModel_View_PathSessionMemory(t *testing.T) {
	t.Parallel()
	// Three rounds: 20ms, 40ms, then a miss. The path line shows the
	// shape, the range, the stability count, and — once down after being
	// up — since when, from the injected clock.
	clock := &fakeClock{t: t0}
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}}}
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = feed(t, m, m.Init())
	if view := m.View(); !lineHas(view, "● lan", "▁", "20ms", "1/1") {
		t.Errorf("View() after one round:\n%s", view)
	}
	d.Up("192.0.2.10:22", 40*time.Millisecond)
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	if view := m.View(); !lineHas(view, "● lan", "▁█", "20ms–40ms", "2/2") {
		t.Errorf("View() after two rounds:\n%s", view)
	}
	d.Down("192.0.2.10:22")
	clock.advance(time.Minute)
	_, tick = m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	clock.advance(3 * time.Minute)
	if view := m.View(); !lineHas(view, "○ lan", "down 3m", "▁█·", "20ms–40ms", "2/3") {
		t.Errorf("View() after the miss:\n%s", view)
	}
	// A path never seen up this session carries no down-since.
	fresh := ui.New(hosts, probe.Prober{Dialer: fakedial.New(), Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	fresh = update(t, fresh, tea.WindowSizeMsg{Width: 100, Height: 24})
	fresh = feed(t, fresh, fresh.Init())
	if view := fresh.View(); !lineHas(view, "○ lan", "down", "·", "0/1") || strings.Contains(view, "down 0s") {
		t.Errorf("View() for a never-up path:\n%s", view)
	}
}

func TestModel_View_RunWords(t *testing.T) {
	t.Parallel()
	// The store observes the first alive render at t0; twelve minutes
	// later the rows say so. Then the master dies: down-since from the
	// observation, forwards likewise; a never-observed entity says nothing.
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	m, _ := statsModelOurs(t, r, d, clock, true)
	m = feed(t, m, m.Init())
	clock.advance(12 * time.Minute)
	view := m.View()
	if !lineHas(view, specWeb0, "up 12m") {
		t.Errorf("View() after 12 minutes up:\n%s", view)
	}
	if lineHas(view, specWeb1, "down 12m") || lineHas(view, specWeb1, "down 0s") {
		t.Errorf("View() invents a down-since for a forward never seen up:\n%s", view)
	}
	// The master dies; the next poll observes the transition.
	r.StubExit(255, "ssh", "-O", "check", "ubuntu")
	d.Down("127.0.0.1:3000")
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	clock.advance(3 * time.Minute)
	view = m.View()
	if !lineHas(view, specWeb0, "down 3m") {
		t.Errorf("View() after the master died:\n%s", view)
	}
}

func TestModel_View_LastSeenLine(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m, store := statsModel(t, r, fakedial.New(), clock)
	m = feed(t, m, m.Init())
	if view := m.View(); strings.Contains(view, "last connected") || strings.Contains(view, "last switch") {
		t.Errorf("View() shows last-seen with nothing observed:\n%s", view)
	}
	if err := store.RecordHandoff("ubuntu"); err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}
	clock.advance(5 * time.Minute)
	if err := store.RecordSwitch("ubuntu", "ts"); err != nil {
		t.Fatalf("RecordSwitch: %v", err)
	}
	clock.advance(2 * time.Hour)
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	if view := m.View(); !strings.Contains(view, "last connected 2h05m ago · last switch → ts 2h00m ago") {
		t.Errorf("View() missing the last-seen line:\n%s", view)
	}
}

func TestModel_View_CorruptStats(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m, store := statsModel(t, r, fakedial.New(), clock)
	if err := writeFile(store.Dir+"/ubuntu.stats.json", "{not json"); err != nil {
		t.Fatalf("write: %v", err)
	}
	m = feed(t, m, m.Init())
	if view := m.View(); !strings.Contains(view, "stats cache unreadable") {
		t.Errorf("View() missing the corrupt-stats row:\n%s", view)
	}
}

func TestModel_Update_HandoffIsRecorded(t *testing.T) {
	t.Parallel()
	// enter remembers the managed alias it hands off to (H spawns, 4.22,
	// and stamps through the spawn's own outcome); when the
	// terminal comes back the stamp rides a fresh status poll (one writer
	// per host) and the render that follows can show it.
	tests := []struct {
		name     string
		key      string
		exit     func(error) tea.Msg
		hookErr  error
		wantNote string
	}{
		{name: "enter", key: "enter", exit: ui.SSHExit},
		{name: "a failed stamp is a notice", key: "enter", exit: ui.SSHExit, hookErr: errors.New("read-only"), wantNote: "stats not recorded: read-only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stamped []string
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
			m := polled(t, r, fakedial.New())
			m = ui.WithRecordHandoff(m, func(alias string) error {
				stamped = append(stamped, alias)
				return tt.hookErr
			})
			next, handoff := m.Update(keyMsg(tt.key))
			m = next.(ui.Model)
			if handoff == nil || ui.HandoffHost(m) != "ubuntu" {
				t.Fatalf("%s: cmd %v, HandoffHost %q; want the handoff bound to ubuntu", tt.key, handoff != nil, ui.HandoffHost(m))
			}
			before := len(r.CallKeys())
			next, cmd := m.Update(tt.exit(nil))
			m = next.(ui.Model)
			if ui.HandoffHost(m) != "" {
				t.Errorf("HandoffHost() after the return = %q, want cleared", ui.HandoffHost(m))
			}
			m = feed(t, m, cmd) // probe + the stamp-then-poll
			if !reflect.DeepEqual(stamped, []string{"ubuntu"}) {
				t.Errorf("stamped = %v, want [ubuntu]", stamped)
			}
			if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"ssh -O check ubuntu"}) {
				t.Errorf("calls after the return = %v, want the re-poll", got)
			}
			if tt.wantNote != "" && !strings.Contains(m.View(), tt.wantNote) {
				t.Errorf("View() missing %q:\n%s", tt.wantNote, m.View())
			}
		})
	}
}

func TestModel_Update_HandoffStampWaitsForInFlightPoll(t *testing.T) {
	t.Parallel()
	// A poll is out when the terminal returns: the stamp must not race
	// it — it rides the poll issued when that one lands.
	var stamped []string
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	m = ui.WithRecordHandoff(m, func(alias string) error { stamped = append(stamped, alias); return nil })
	next, poll := m.Update(ui.RefreshTick()) // a poll in flight
	m = next.(ui.Model)
	next, _ = m.Update(keyMsg("enter"))
	next, back := next.(ui.Model).Update(ui.SSHExit(nil))
	m = feed(t, next.(ui.Model), back)
	if len(stamped) != 0 {
		t.Fatalf("stamped while a poll was in flight: %v", stamped)
	}
	m = feedAll(t, m, poll) // the in-flight poll lands: now the stamped poll runs
	if !reflect.DeepEqual(stamped, []string{"ubuntu"}) {
		t.Errorf("stamped after the poll landed = %v, want [ubuntu]", stamped)
	}
}

func TestModel_Update_AdhocHandoffNotStamped(t *testing.T) {
	t.Parallel()
	var stamped []string
	m := ui.WithRecordHandoff(probedModel(t, fakerun.New()), func(alias string) error { stamped = append(stamped, alias); return nil })
	m = connectPending(t, m, "192.0.2.50") // ad-hoc: no managed alias
	if len(stamped) != 0 || ui.HandoffHost(m) != "" {
		t.Errorf("ad-hoc handoff stamped %v, HandoffHost %q", stamped, ui.HandoffHost(m))
	}
}

func TestModel_Update_PathHistoryFollowsThePath(t *testing.T) {
	t.Parallel()
	// History is keyed by what a path is, never by its slot: a reload
	// that reorders the paths keeps each path's own ring, and a removed
	// path's history is forgotten, not shown under its successor.
	clock := &fakeClock{t: t0}
	lan := probe.Path{Name: "lan", Host: "192.0.2.10"}
	wan := probe.Path{Name: "wan", Host: "203.0.113.5"}
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{lan, wan}}}
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond).Down("203.0.113.5:22")
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = feed(t, m, m.Init())
	m = feed(t, m, m.Init())
	// A reload with the paths reordered; its batched re-probe lands.
	m = reloadAndProbe(t, m, []hostmodel.Host{{Alias: "web", Paths: []probe.Path{wan, lan}}})
	view := m.View()
	// Two rounds before the reload plus the reload's own re-probe.
	// (web's list row shares wan's line and carries lan's 20ms since
	// SASASH.4.29; the check reads the path row itself.)
	if wan := afterMarker(view, "○ wan"); !strings.Contains(wan, "···") || !strings.Contains(wan, "0/3") || strings.Contains(wan, "down 0s") || strings.Contains(wan, "20ms") {
		t.Errorf("wan inherited lan's history after the reorder:\n%s", view)
	}
	if !lineHas(view, "● lan", "▁▁▁", "20ms", "3/3") {
		t.Errorf("lan lost its history after the reorder:\n%s", view)
	}
	// Dropping wan forgets it; a later re-add starts fresh.
	m = reloadAndProbe(t, m, []hostmodel.Host{{Alias: "web", Paths: []probe.Path{lan}}})
	if ring := ui.PathRing(m, "web", wan); ring != nil {
		t.Errorf("removed path still remembered: %v", ring)
	}
	if got := len(ui.PathRing(m, "web", lan)); got != 4 {
		t.Errorf("lan ring after the prune = %d samples, want 4 (two rounds + two reload re-probes)", got)
	}
}

// reloadAndProbe replays the editor returning with hosts and delivers
// the reload's batched re-probe and status poll, the way the runtime
// would, so the frame after a reload is settled.
func reloadAndProbe(t *testing.T, m ui.Model, hosts []hostmodel.Host) ui.Model {
	t.Helper()
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) { return hosts, nil, nil })
	next, reload := m.Update(ui.EditorExit(nil))
	next, after := next.(ui.Model).Update(reload())
	return feedAll(t, next.(ui.Model), after)
}

// feedAll runs a cmd tree one level deep and, unlike feed, also runs the
// cmds the deliveries return once more — enough for a stamp that rides
// the poll after the one in flight.
func feedAll(t *testing.T, m ui.Model, cmd tea.Cmd) ui.Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msgs := []tea.Msg{cmd()}
	if batch, ok := msgs[0].(tea.BatchMsg); ok {
		msgs = nil
		for _, c := range batch {
			msgs = append(msgs, c())
		}
	}
	for _, msg := range msgs {
		next, again := m.Update(msg)
		m = next.(ui.Model)
		if again != nil {
			if follow := again(); follow != nil {
				if _, isBatch := follow.(tea.BatchMsg); !isBatch {
					next, _ = m.Update(follow) // a single follow-up: deliver it too
					m = next.(ui.Model)
				}
			}
		}
	}
	return m
}

func TestModel_View_UnnamedPathsKeepSeparateHistory(t *testing.T) {
	t.Parallel()
	// Two unnamed paths on one host are told apart by address: each
	// keeps its own ring and count.
	clock := &fakeClock{t: t0}
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{{Host: "192.0.2.10"}, {Host: "203.0.113.5"}}}}
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond).Down("203.0.113.5:22")
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = feed(t, m, m.Init())
	view := m.View()
	if !lineHas(view, "● 192.0.2.10:22", "1/1") || !lineHas(view, "○ 203.0.113.5:22", "0/1") {
		t.Errorf("unnamed paths share a history:\n%s", view)
	}
}

func TestModel_View_ReloadReadsPendingUntilReprobed(t *testing.T) {
	t.Parallel()
	// The round before a reload is aligned with the OLD paths: until the
	// re-probe lands, rows are pending — never another path's state.
	clock := &fakeClock{t: t0}
	lan := probe.Path{Name: "lan", Host: "192.0.2.10"}
	wan := probe.Path{Name: "wan", Host: "203.0.113.5"}
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond).Down("203.0.113.5:22")
	m := ui.New([]hostmodel.Host{{Alias: "web", Paths: []probe.Path{lan, wan}}}, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = feed(t, m, m.Init())
	m = ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = editReload(t, m, []hostmodel.Host{{Alias: "web", Paths: []probe.Path{wan, lan}}}) // re-probe NOT delivered
	view := m.View()
	if lineHas(view, "● wan") || lineHas(view, "○ lan") || !lineHas(view, "◌ wan") || !lineHas(view, "◌ lan") {
		t.Errorf("rows after a reload should be pending, not the old round's states:\n%s", view)
	}
	if !strings.Contains(view, "probing…") {
		t.Errorf("the tally should be unknown until the re-probe:\n%s", view)
	}
}

func TestModel_Update_PathRingIsBounded(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: t0}
	lan := probe.Path{Name: "lan", Host: "192.0.2.10"}
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{lan}}}
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	for i := 0; i < 35; i++ {
		m = feed(t, m, m.Init())
	}
	if got := len(ui.PathRing(m, "web", lan)); got != 30 {
		t.Errorf("ring after 35 rounds = %d samples, want the 30 newest", got)
	}
	if view := m.View(); !lineHas(view, "● lan", "35/35") {
		t.Errorf("stability should count every round:\n%s", view)
	}
}

func TestModel_View_SparklineFitsThePane(t *testing.T) {
	t.Parallel()
	// The range and the count are the honest part and always stay; the
	// shape takes what is left of the pane and shows the NEWEST samples —
	// never the oldest — so a narrow pane may show none at all.
	clock := &fakeClock{t: t0}
	lan := probe.Path{Name: "lan", Host: "192.0.2.10"}
	hosts := []hostmodel.Host{{Alias: "build-eu-west-bastion-01", Paths: []probe.Path{lan}}}
	d := fakedial.New().Up("192.0.2.10:22", 10*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := 0; i < 29; i++ {
		m = feed(t, m, m.Init())
	}
	d.Up("192.0.2.10:22", 90*time.Millisecond) // the newest sample is the tallest
	m = feed(t, m, m.Init())
	view := m.View()
	if !lineHas(view, "● lan", "10ms–90ms", "30/30") {
		t.Errorf("range and count must survive at 80 cols:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line wider than the frame (%d): %q", w, line)
		}
	}
	// Wider: part of the ring fits, and it is the newest part.
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	view = m.View()
	if !lineHas(view, "● lan", "█", "10ms–90ms", "30/30") {
		t.Errorf("the newest (tallest) sample must be drawn at 100 cols:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "● lan") && strings.Count(line, "▁") >= 29 {
			t.Errorf("the whole ring was drawn instead of the newest part: %q", line)
		}
	}
}

func TestModel_View_HeaderKeepsTheFilterTerm(t *testing.T) {
	t.Parallel()
	// The filter term explains a subset list; at a width where the tally
	// cannot sit beside it whole, the tally goes, not the term.
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m, _ := statsModel(t, r, fakedial.New(), clock)
	m = feed(t, m, m.Init())
	m = update(t, m, tea.WindowSizeMsg{Width: 60, Height: 24})
	m = press(t, m, "/", "u", "b", "u", "n", "t", "u", "enter")
	view := m.View()
	if !strings.Contains(view, "/ubuntu") || strings.Contains(view, "reachable") {
		t.Errorf("View() at 60 cols should keep the term and drop the tally:\n%s", view)
	}
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	if view := m.View(); !strings.Contains(view, "/ubuntu") || !strings.Contains(view, "reachable") {
		t.Errorf("View() at 120 cols should show both:\n%s", view)
	}
}

func TestModel_View_TallySkipsErroredPolls(t *testing.T) {
	t.Parallel()
	// A host whose poll failed (the seam, not ssh) is not "polled": the
	// tunnels clause never counts it, and with no polled host at all it
	// is absent.
	clock := &fakeClock{t: t0}
	r := fakerun.New().Stub(fakerun.Reply{Err: errors.New("no ssh binary")}, "ssh", "-O", "check", "ubuntu")
	m, _ := statsModel(t, r, fakedial.New(), clock)
	m = feed(t, m, m.Init())
	header := strings.SplitN(m.View(), "\n", 2)[0]
	if !strings.Contains(header, "3 hosts · 0 reachable") || strings.Contains(header, "tunnel") {
		t.Errorf("header should tally hosts only when no poll succeeded: %q", header)
	}
}

func TestModel_View_LastSeenAfterEditPlan(t *testing.T) {
	t.Parallel()
	// The last-seen line comes after the rows and any edit plan under
	// them, so the plan reads as the row's, not as the line's.
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m, store := statsModel(t, r, fakedial.New(), clock)
	if err := store.RecordHandoff("ubuntu"); err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}
	m = feed(t, m, m.Init())
	m = ui.WithSaveForwardEdit(m, func(string, string, string, string) error { return nil })
	m = press(t, m, "l", "G", "E")
	view := m.View()
	// The last-seen line is host history above the tunnels section now
	// (SASASH.4.26), so it precedes the edit plan; both are present.
	plan, seen := strings.Index(view, "sasash.conf: forwards api"), strings.Index(view, "last connected")
	if plan < 0 || seen < 0 || seen > plan {
		t.Errorf("the last-seen line (%d) is host history above the edit plan (%d):\n%s", seen, plan, view)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestModel_Update_UnchangedReloadKeepsTheRound(t *testing.T) {
	t.Parallel()
	// A reload that leaves every path where it was (the reloads tunnel
	// actions trigger) keeps the last round: no blank pane, no probing….
	clock := &fakeClock{t: t0}
	lan := probe.Path{Name: "lan", Host: "192.0.2.10"}
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{lan}}}
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = feed(t, m, m.Init())
	m = ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = editReload(t, m, hosts) // same paths; re-probe not delivered
	if view := m.View(); !lineHas(view, "● lan", "20ms") || strings.Contains(view, "probing…") {
		t.Errorf("an unchanged reload blanked the round:\n%s", view)
	}
}

func TestModel_Update_ActionHoldsThePollGate(t *testing.T) {
	t.Parallel()
	// While an action runs (it writes the state and stats caches), the
	// tick must not poll the same host: one writer per host. The done
	// arm releases the gate and re-polls.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l")
	next, action := m.Update(keyMsg(" ")) // out, not delivered
	m = next.(ui.Model)
	before := len(r.CallKeys())
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("tick polled a host with an action in flight: %v", got)
	}
	next, after := m.Update(action())
	m = next.(ui.Model)
	before = len(r.CallKeys())
	m = feed(t, m, after)
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"ssh -O check ubuntu"}) {
		t.Errorf("after the action: calls %v, want the re-poll", got)
	}
}

func TestModel_Update_FailedStartIsNotAHandoff(t *testing.T) {
	t.Parallel()
	// ssh (or herdr) that never started is not a handoff: no stamp, and
	// no "last connected" that never happened.
	var stamped []string
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	m = ui.WithRecordHandoff(m, func(alias string) error { stamped = append(stamped, alias); return nil })
	next, _ := m.Update(keyMsg("enter"))
	next, cmd := next.(ui.Model).Update(ui.SSHExit(errors.New(`exec: "ssh": executable file not found in $PATH`)))
	m = feedAll(t, next.(ui.Model), cmd)
	if len(stamped) != 0 || ui.HandoffHost(m) != "" {
		t.Errorf("a failed start was stamped: %v (handoffHost %q)", stamped, ui.HandoffHost(m))
	}
}

func TestModel_View_PathTailIsWholeOrAbsent(t *testing.T) {
	t.Parallel()
	// A count cut by the frame clamp would read as a smaller, plausible
	// number: at a width where the tail cannot fit whole, it is absent.
	clock := &fakeClock{t: t0}
	lan := probe.Path{Name: "lan", Host: "192.0.2.10"}
	hosts := []hostmodel.Host{{Alias: "production-bastion-eu-west", Paths: []probe.Path{lan}}}
	d := fakedial.New().Up("192.0.2.10:22", 48*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 60, Height: 24})
	for i := 0; i < 35; i++ {
		m = feed(t, m, m.Init())
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if !strings.Contains(line, "● lan") {
			continue
		}
		if strings.Contains(line, "/") && !strings.Contains(line, "35/35") {
			t.Errorf("the count was cut mid-token: %q", line)
		}
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("line wider than the frame (%d): %q", w, line)
		}
	}
}

func TestModel_View_LastSeenAboveTunnelsSection(t *testing.T) {
	t.Parallel()
	// SASASH.4.26 decision (1): the last-connected/last-switch line is host
	// history, not a tunnel — it renders under the paths, ABOVE the tunnels
	// section, never inside it.
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m, store := statsModelOurs(t, r, fakedial.New(), clock, true)
	if err := store.RecordHandoff("ubuntu"); err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}
	m = feed(t, m, m.Init())
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	view := m.View()
	// The panes join horizontally, so anchor on the first tunnel row: the
	// last-seen line must render above it, i.e. outside the tunnels section.
	seen, row := strings.Index(view, "last connected"), strings.Index(view, "web 3000:localhost:3000")
	if seen < 0 || row < 0 || seen > row {
		t.Errorf("last-seen (%d) must render above the tunnel rows (%d):\n%s", seen, row, view)
	}
}
