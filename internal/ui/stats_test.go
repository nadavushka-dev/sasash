package ui_test

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
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
	dir := t.TempDir()
	store := stats.Store{Dir: dir, Clock: clock.now}
	eng := tunnel.Engine{Runner: r, Dialer: d, StateDir: dir, Timeout: 50 * time.Millisecond, Stats: &store}
	m := ui.New(tunnelHosts(), probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng, Clock: clock.now})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	return m, store
}

func TestModel_View_HeaderSummary(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: t0}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond).Up("127.0.0.1:3000", time.Millisecond)
	m, _ := statsModel(t, r, d, clock)
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
	m, _ := statsModel(t, r, d, clock)
	m = feed(t, m, m.Init())
	clock.advance(12 * time.Minute)
	view := m.View()
	if !lineHas(view, "● master up 12m") || !lineHas(view, specWeb0, "up 12m") {
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
	if !lineHas(view, "○ master down 3m") || !lineHas(view, specWeb0, "down 3m") {
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
	tests := []struct {
		name     string
		exit     func(error) tea.Msg
		hookErr  error
		wantHost string
		wantNote string
	}{
		{name: "enter on a managed host", exit: func(err error) tea.Msg { return ui.SSHExitFrom("web", err) }, wantHost: "web"},
		{name: "H on a managed host", exit: func(err error) tea.Msg { return ui.HerdrExit(err) }},
		{name: "a failed stamp is a notice", exit: func(err error) tea.Msg { return ui.SSHExitFrom("web", err) }, hookErr: errors.New("read-only"), wantHost: "web", wantNote: "stats not recorded: read-only"},
		{name: "an ad-hoc target has no managed host", exit: ui.SSHExit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stamped []string
			m := ui.WithRecordHandoff(probedModel(t, fakerun.New()), func(alias string) error {
				stamped = append(stamped, alias)
				return tt.hookErr
			})
			next, cmd := m.Update(tt.exit(nil))
			m = feed(t, next.(ui.Model), cmd)
			var want []string
			if tt.wantHost != "" {
				want = []string{tt.wantHost}
			}
			if !reflect.DeepEqual(stamped, want) {
				t.Errorf("stamped = %v, want %v", stamped, want)
			}
			if tt.wantNote != "" && !strings.Contains(m.View(), tt.wantNote) {
				t.Errorf("View() missing %q:\n%s", tt.wantNote, m.View())
			}
		})
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
