package ui_test

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// glanceHosts: alpha reachable, bravo down, and charlie whose HostName
// is its dead path while its other path is alive — the row must say
// what ssh gets (down), the detail's ← active rule, even though the
// header counts the host as reachable.
func glanceModel(t *testing.T) ui.Model {
	t.Helper()
	hosts := []hostmodel.Host{
		{Alias: "alpha", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
		{Alias: "bravo", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.20"}}},
		{Alias: "charlie", HostName: "198.51.100.30", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.30"}, {Name: "vpn", Host: "198.51.100.30"}}},
	}
	d := fakedial.New().
		Up("192.0.2.10:22", 20*time.Millisecond).
		Down("192.0.2.20:22").
		Up("192.0.2.30:22", 5*time.Millisecond).
		Down("198.51.100.30:22")
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, fakerun.New(), ui.Config{})
	return update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
}

// rowOf is the line carrying the host's row.
func rowOf(t *testing.T, view, alias string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, " "+alias+" ") {
			return line
		}
	}
	t.Fatalf("no row for %q:\n%s", alias, view)
	return ""
}

func TestModel_View_RowsCarryStatus(t *testing.T) {
	t.Parallel()
	m := glanceModel(t)
	// Before the first round every row is pending, glyph and column.
	view := m.View()
	for _, alias := range []string{"alpha", "bravo", "charlie"} {
		if row := rowOf(t, view, alias); !strings.Contains(row, "◌ "+alias) || !strings.Contains(row, "  …") {
			t.Errorf("row before the first round = %q, want ◌ and …", row)
		}
	}
	m = update(t, m, m.Init()())
	view = m.View()
	tests := []struct {
		name  string
		alias string
		want  []string
	}{
		{name: "reachable: glyph and latency", alias: "alpha", want: []string{"› ● alpha", "20ms"}},
		{name: "down: glyph and the word", alias: "bravo", want: []string{"  ○ bravo", "down"}},
		{name: "HostName path down, another alive: what ssh gets", alias: "charlie", want: []string{"  ○ charlie", "down"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			row := rowOf(t, view, tt.alias)
			for _, w := range tt.want {
				if !strings.Contains(row, w) {
					t.Errorf("row = %q, want %q", row, w)
				}
			}
		})
	}
	if !strings.Contains(view, "3 hosts · 2 reachable") {
		t.Errorf("header should still count charlie reachable by its live path:\n%s", view)
	}
	// Tabular: the alias column and the latency column's right edge line
	// up across rows, whatever the alias length or the value.
	col := func(row, tok string) int { return len([]rune(row[:strings.Index(row, tok)])) } // cells, not bytes: the glyphs are multi-byte
	edge := func(row, tok string) int { return col(row, tok) + len([]rune(tok)) }
	a, b, c := rowOf(t, view, "alpha"), rowOf(t, view, "bravo"), rowOf(t, view, "charlie")
	if col(a, "alpha") != col(b, "bravo") || col(a, "alpha") != col(c, "charlie") {
		t.Errorf("alias columns differ:\n%s\n%s\n%s", a, b, c)
	}
	if edge(a, "20ms") != edge(b, "down") || edge(a, "20ms") != edge(c, "down") {
		t.Errorf("latency columns differ:\n%s\n%s\n%s", a, b, c)
	}
}

func TestModel_View_RowsCarryTunnelCount(t *testing.T) {
	t.Parallel()
	// ubuntu's web forward is our live pid on an answering port: its row
	// says 1 up; the unpolled hosts say nothing; and the count column is
	// reserved from the start, so the detail pane never shifts when the
	// first poll lands.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41}]}`)
	m := tunnelModelSpawn(t, r, fakespawn.New().Dial(d).Live(41), d, stateDir)
	before := ui.ListPaneWidth(m)
	m = feed(t, m, m.Init())
	view := m.View()
	if row := rowOf(t, view, "ubuntu"); !strings.Contains(row, "› ○ ubuntu") || !strings.Contains(row, "1 up") {
		t.Errorf("ubuntu row = %q, want the ○ (its path is down) and 1 up", row)
	}
	for _, alias := range []string{"bare", "spare"} {
		if row := rowOf(t, view, alias); strings.Contains(row, " up") {
			t.Errorf("%s row = %q, want no count for an unpolled host", alias, row)
		}
	}
	if after := ui.ListPaneWidth(m); after != before {
		t.Errorf("list width %d after the poll, was %d: the count column must be reserved", after, before)
	}
}

func TestModel_View_LongAliasKeepsTheStatusColumns(t *testing.T) {
	t.Parallel()
	// The alias is cut with a visible ellipsis; the status column after
	// it survives, and the row stays inside the list pane.
	long := "build-eu-west-bastion-01-very-long"
	hosts := []hostmodel.Host{{Alias: long, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}}}
	m := update(t, sized(t, hosts), tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View()
	lw := ui.ListPaneWidth(m)
	for _, line := range strings.Split(view, "\n") {
		if !strings.Contains(line, "› ◌ build-eu-west") {
			continue
		}
		listPart := strings.TrimRight(string([]rune(line)[:lw]), " ")
		if !strings.Contains(listPart, "…") || !strings.HasSuffix(listPart, "  …") {
			t.Errorf("list part = %q, want the ellipsis in the alias and the pending column after it", listPart)
		}
		return
	}
	t.Errorf("View() has no selected row for the long alias:\n%s", view)
}

func TestModel_View_RowsNeverReadTheConfig(t *testing.T) {
	t.Parallel()
	// Render is per row per frame: rows read the block CACHE only. The
	// detail pane fills it for the selected host (one read); the other
	// hosts' rows must not.
	var reads []string
	m := glanceModel(t)
	m = ui.WithSwitcher(m, nil, func(alias string) ([]probe.Path, string, bool) {
		reads = append(reads, alias)
		return nil, "", false
	}, nil)
	m = update(t, m, m.Init()())
	for range 3 {
		_ = m.View()
	}
	for _, alias := range reads {
		if alias != "alpha" {
			t.Errorf("View() read the block of %q; only the selected alpha may be read (reads: %v)", alias, reads)
		}
	}
	_ = pathswitch.Result{} // the switcher hook's type, wired nil above
}
