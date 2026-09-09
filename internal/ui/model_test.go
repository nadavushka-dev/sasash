package ui_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// testHosts builds n hosts host0..host{n-1} on RFC 5737 addresses.
func testHosts(n int) []hostmodel.Host {
	hosts := make([]hostmodel.Host, n)
	for i := range hosts {
		hosts[i] = hostmodel.Host{
			Alias: fmt.Sprintf("host%d", i),
			Paths: []probe.Path{{Name: "lan", Host: fmt.Sprintf("192.0.2.%d", i+1)}},
		}
	}
	return hosts
}

// sized builds a model over hosts, with fake dialer and runner so nothing
// ever dials or executes, and delivers the initial window size.
func sized(t *testing.T, hosts []hostmodel.Host) ui.Model {
	t.Helper()
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New(), Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
	return ui.WithPreviewShown(update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24}))
}

// update runs one Update and asserts the model type is preserved.
func update(t *testing.T, m ui.Model, msg tea.Msg) ui.Model {
	t.Helper()
	next, _ := m.Update(msg)
	nm, ok := next.(ui.Model)
	if !ok {
		t.Fatalf("Update() returned %T, want ui.Model", next)
	}
	return nm
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestModel_Update_KeyDispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		keys      []string
		wantAlias string
	}{
		{name: "j moves down", keys: []string{"j"}, wantAlias: "host1"},
		{name: "arrow down moves down", keys: []string{"down"}, wantAlias: "host1"},
		{name: "k clamps at top", keys: []string{"k"}, wantAlias: "host0"},
		{name: "arrow up after j returns", keys: []string{"j", "up"}, wantAlias: "host0"},
		{name: "G jumps to bottom", keys: []string{"G"}, wantAlias: "host4"},
		{name: "g returns to top", keys: []string{"G", "g"}, wantAlias: "host0"},
		{name: "ctrl+d half-pages down clamped to last", keys: []string{"ctrl+d"}, wantAlias: "host4"},
		{name: "ctrl+u half-pages back up", keys: []string{"G", "ctrl+u"}, wantAlias: "host0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := sized(t, testHosts(5))
			for _, k := range tt.keys {
				m = update(t, m, keyMsg(k))
			}
			got, ok := m.Selected()
			if !ok {
				t.Fatalf("Selected() ok = false, want true")
			}
			if got.Alias != tt.wantAlias {
				t.Errorf("Selected().Alias = %v, want %v", got.Alias, tt.wantAlias)
			}
		})
	}
}

func TestModel_Update_Quit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
	}{
		{name: "q quits", key: "q"},
		{name: "ctrl+c force-quits", key: "ctrl+c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := sized(t, testHosts(2))
			_, cmd := m.Update(keyMsg(tt.key))
			if cmd == nil {
				t.Fatalf("Update(%q) cmd = nil, want tea.Quit", tt.key)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("Update(%q) cmd() = %T, want tea.QuitMsg", tt.key, cmd())
			}
		})
	}
}

func TestModel_Update_ProbeFlow(t *testing.T) {
	t.Parallel()
	hosts := []hostmodel.Host{
		{Alias: "web", HostName: "198.51.100.5", Paths: []probe.Path{
			{Name: "lan", Host: "192.0.2.10"},
			{Name: "ts", Host: "198.51.100.5"},
		}},
		{Alias: "db", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.30"}}},
	}
	dialer := fakedial.New().
		Down("192.0.2.10:22").
		Up("198.51.100.5:22", 20*time.Millisecond).
		Up("192.0.2.30:22", 5*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: dialer, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
	m = ui.WithPreviewShown(update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24}))

	if view := m.View(); !strings.Contains(view, "◌") {
		t.Errorf("View() before probe = %q, want pending glyph ◌", view)
	}

	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("Init() = nil, want probe cmd")
	}
	next, tick := m.Update(cmd()) // run the probe through the fake dialer
	m = next.(ui.Model)
	if tick == nil {
		t.Errorf("Update(probeDoneMsg) cmd = nil, want refresh tick")
	}

	view := m.View()
	// Selected host is web: HostName is ts's address, so ts is the active
	// path (SASASH.7.2 — ssh's actual target), and it is also up at 20ms.
	// Addresses render as the full dial target, port included.
	for _, want := range []string{"web", "db", "○", "down", "●", "20ms", "← active", "$", "ssh web", "198.51.100.5:22"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() after probe missing %q\nview:\n%s", want, view)
		}
	}

	// db's status must be its own, not an offset into web's: the per-host
	// slicing of the flat probe result is the only index math in the package.
	m = update(t, m, keyMsg("j"))
	view = m.View()
	for _, want := range []string{"5ms", "ssh db"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() with db selected missing %q\nview:\n%s", want, view)
		}
	}
	// (web's own row carries its 20ms since SASASH.4.29; the selected
	// line — db's row and its first path — must not.)
	if lineHas(view, "› ", "20ms") {
		t.Errorf("View() with db selected still shows web's 20ms latency\nview:\n%s", view)
	}
}

func TestModel_Update_RefreshTick(t *testing.T) {
	t.Parallel()
	hosts := testHosts(1)
	addr := hosts[0].Paths[0].Addr()
	dialer := fakedial.New().Down(addr)
	m := ui.New(hosts, probe.Prober{Dialer: dialer, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
	m = ui.WithPreviewShown(update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24}))

	next, rearm := m.Update(m.Init()())
	m = next.(ui.Model)
	if rearm == nil {
		t.Errorf("Update(probeDoneMsg) cmd = nil, want re-arming tick")
	}
	if view := m.View(); !strings.Contains(view, "down") {
		t.Fatalf("View() after first probe = %q, want down", view)
	}

	// The host comes back up; the next tick must re-probe and repaint.
	dialer.Up(addr, 7*time.Millisecond)
	next, cmd := m.Update(ui.RefreshTick())
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(refreshTick) cmd = nil, want probe cmd")
	}
	next, rearm2 := m.Update(cmd())
	m = next.(ui.Model)
	if rearm2 == nil {
		t.Errorf("Update(probeDoneMsg) after a tick cmd = nil, want the refresh chain re-armed")
	}
	if view := m.View(); !strings.Contains(view, "7ms") {
		t.Errorf("View() after refresh = %q, want recovered 7ms status", view)
	}
}

func TestModel_View_Smoke(t *testing.T) {
	t.Parallel()
	m := sized(t, testHosts(3))
	view := m.View()
	for _, want := range []string{"sasash", "host0", "host1", "host2", "paths", "ssh host0"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q", want)
		}
	}

	// The list look is a SPEC §5 invariant the delegate provides: the
	// selected row carries the › prefix and unselected rows indent two
	// columns to align under it. A zero-valued delegate (a lost
	// SetDelegate) drops both.
	var selected, unselected bool
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(line, "› ◌ host0") {
			selected = true
		}
		if strings.HasPrefix(line, "  ◌ host1") {
			unselected = true
		}
	}
	if !selected {
		t.Errorf("View() has no ›-prefixed selected row:\n%s", view)
	}
	if !unselected {
		t.Errorf("View() has no two-column-indented unselected row:\n%s", view)
	}
}

// peakDialer records the highest number of concurrent dials it has seen.
// The short block gives dials in the same round time to overlap, so an
// unbounded fan-out reliably exceeds the cap.
type peakDialer struct {
	mu        sync.Mutex
	cur, peak int
}

func (d *peakDialer) Dial(ctx context.Context, addr string) (time.Duration, error) {
	d.mu.Lock()
	d.cur++
	if d.cur > d.peak {
		d.peak = d.cur
	}
	d.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	d.mu.Lock()
	d.cur--
	d.mu.Unlock()
	return time.Millisecond, nil
}

func TestModel_Init_ProbeFanOutBounded(t *testing.T) {
	t.Parallel()
	dialer := &peakDialer{}
	hosts := testHosts(2*ui.ProbeChunk + 3)
	m := ui.New(hosts, probe.Prober{Dialer: dialer, Timeout: time.Second}, fakerun.New(), ui.Config{})
	msg := m.Init()()

	if dialer.peak > ui.ProbeChunk {
		t.Errorf("peak concurrent dials = %v, want <= %v", dialer.peak, ui.ProbeChunk)
	}
	// Every host still gets its own status after the chunked rounds.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	next, _ = next.(ui.Model).Update(msg)
	m = next.(ui.Model)
	m = update(t, m, keyMsg("G"))
	if view := m.View(); !strings.Contains(view, "1ms") {
		t.Errorf("View() for last host = %q, want probed 1ms status", view)
	}
}

func TestModel_View_NeverWiderThanTerminal(t *testing.T) {
	t.Parallel()
	hosts := []hostmodel.Host{{
		Alias: "prod-web-frankfurt-teaching-cluster-01",
		Paths: []probe.Path{{Name: "lan", Host: strings.Repeat("very-long-name.", 4) + "example.com"}},
	}}
	m := sized(t, hosts)
	next, _ := m.Update(m.Init()())
	m = next.(ui.Model)
	for i, line := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("View() line %d width = %v, want <= 80", i, w)
		}
	}

	// The preview line must stay clamped with a notice appended too: the
	// only path here is down, so y produces the nothing-copied notice.
	m = update(t, m, keyMsg("y"))
	if view := m.View(); !strings.Contains(view, "nothing copied") {
		t.Fatalf("View() = %q, want a notice on the preview line", view)
	}
	for i, line := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("View() with notice, line %d width = %v, want <= 80", i, w)
		}
	}

	// At width 30 the bare "$ ssh <alias>" preview line itself overflows
	// unless clamped — the long alias makes the preview clamp load-bearing.
	m = update(t, m, tea.WindowSizeMsg{Width: 30, Height: 24})
	for i, line := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(line); w > 30 {
			t.Errorf("View() at width 30, line %d width = %v, want <= 30", i, w)
		}
	}
}

func TestModel_View_NoHosts(t *testing.T) {
	t.Parallel()
	m := sized(t, nil)
	if view := m.View(); !strings.Contains(view, "no hosts found") {
		t.Errorf("View() = %q, want empty-state sentence", view)
	}
	if _, ok := m.Selected(); ok {
		t.Errorf("Selected() ok = true, want false for empty list")
	}
	// q must still quit from the empty state.
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatalf("Update(q) cmd = nil, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Update(q) cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestModel_Update_PreviewHiddenByDefaultUntilToggled(t *testing.T) {
	t.Parallel()
	// SASASH.4.26 decision (3): the command-preview footer is off by
	// default; `$` toggles it. A raw model (no WithPreviewShown) proves
	// the default. The toggle-on notice takes the line until the next
	// keypress, so a benign k clears it and the preview returns.
	m := ui.New(testHosts(2), probe.Prober{Dialer: fakedial.New(), Timeout: time.Millisecond}, fakerun.New(), ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	if view := m.View(); strings.Contains(view, "$ ssh host0") {
		t.Errorf("the command preview must be hidden by default:\n%s", view)
	}
	m = press(t, m, "$")
	if view := m.View(); !strings.Contains(view, "command line shown") {
		t.Errorf("$ should announce the preview is shown:\n%s", view)
	}
	m = press(t, m, "k")
	if view := m.View(); !strings.Contains(view, "$ ssh host0") {
		t.Errorf("after $, the command preview should render:\n%s", view)
	}
	m = press(t, m, "$")
	m = press(t, m, "k")
	if view := m.View(); strings.Contains(view, "$ ssh host0") {
		t.Errorf("$ again should hide the command preview:\n%s", view)
	}
}
