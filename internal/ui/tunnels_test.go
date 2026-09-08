package ui_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

const (
	specWeb0 = "3000:localhost:3000"
	specWeb1 = "3001:localhost:3001"
	specAPI  = "4011:localhost:4011"
	specDB   = "5432:localhost:5432"
)

// tunnelHosts is the fixture: ubuntu declares two sets, bare declares
// none, spare declares one. Paths are unstubbed, so they probe down and
// stay out of the way.
func tunnelHosts() []hostmodel.Host {
	return []hostmodel.Host{
		{Alias: "ubuntu", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}, Forwards: []hostmodel.ForwardSet{
			{Name: "web", Specs: []string{specWeb0, specWeb1}},
			{Name: "api", Specs: []string{specAPI}},
		}},
		{Alias: "bare", Paths: []probe.Path{{Host: "192.0.2.20"}}},
		{Alias: "spare", Paths: []probe.Path{{Host: "192.0.2.30"}}, Forwards: []hostmodel.ForwardSet{
			{Name: "db", Specs: []string{specDB}},
		}},
	}
}

// tunnelModel builds a sized model over tunnelHosts with a REAL engine
// over the fakes and a fresh state dir. The tunnels section is wide, so
// the frame is 100 columns.
func tunnelModel(t *testing.T, r *fakerun.Fake, d *fakedial.Fake, stateDir string) ui.Model {
	t.Helper()
	return tunnelModelSpawn(t, r, fakespawn.New().Dial(d), d, stateDir)
}

// tunnelModelSpawn is tunnelModel over a spawner the test scripts and
// inspects: a spawned process is a fake pid whose port follows it.
func tunnelModelSpawn(t *testing.T, r *fakerun.Fake, sp *fakespawn.Fake, d *fakedial.Fake, stateDir string) ui.Model {
	t.Helper()
	eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: stateDir, Timeout: 50 * time.Millisecond}
	m := ui.New(tunnelHosts(), probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	return ui.WithPreviewShown(update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30}))
}

// writeState pre-writes the engine's cache for ubuntu as a previous
// sasash left it; a pid in it is alive only if the spawner says so.
func writeState(t *testing.T, stateDir, state string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// spawnArgv is the engine's exact tunnel command for ubuntu, joined.
func spawnArgv(host, spec string) string { return strings.Join(tunnel.SpawnArgv(host, spec), " ") }

// tunnelEngine is a real engine over the fakes and a fresh state dir.
func tunnelEngine(t *testing.T, r *fakerun.Fake, d *fakedial.Fake) tunnel.Engine {
	t.Helper()
	return tunnel.Engine{Runner: r, Spawner: fakespawn.New().Dial(d), Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
}

// feed executes one cmd the way the runtime would — one level deep, a
// batch's members each once — and delivers the produced msgs. Cmds the
// deliveries return are dropped (a probe result arms a real 5s tick).
func feed(t *testing.T, m ui.Model, cmd tea.Cmd) ui.Model {
	t.Helper()
	if cmd == nil {
		t.Fatalf("feed: cmd = nil, want a cmd")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return update(t, m, msg)
	}
	for _, c := range batch {
		m = update(t, m, c())
	}
	return m
}

// polled builds a model whose first status round has landed: master per
// checkExit, ports per the dialer.
func polled(t *testing.T, r *fakerun.Fake, d *fakedial.Fake) ui.Model {
	t.Helper()
	m := tunnelModel(t, r, d, t.TempDir())
	return feed(t, m, m.Init())
}

// polledSpawn is polled over the given spawner and state dir.
func polledSpawn(t *testing.T, r *fakerun.Fake, sp *fakespawn.Fake, d *fakedial.Fake, stateDir string) ui.Model {
	t.Helper()
	m := tunnelModelSpawn(t, r, sp, d, stateDir)
	return feed(t, m, m.Init())
}

func TestTunnelArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "spawn", got: ui.SpawnArgv("ubuntu", specWeb0), want: []string{"ssh", "-N", "-S", "none", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-L", specWeb0, "ubuntu"}},
		{name: "kill", got: ui.KillArgv(4242), want: []string{"kill", "-TERM", "4242"}},
		{name: "exit", got: ui.ExitArgv("ubuntu"), want: []string{"ssh", "-O", "exit", "ubuntu"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("%s argv = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

// The preview must show what the engine really runs: the spawn line is
// pinned to what Engine.Spawn hands the spawner, the exit line to what
// Engine.Exit hands the runner.
func TestTunnelArgv_MatchesEngine(t *testing.T) {
	t.Parallel()
	t.Run("spawn", func(t *testing.T) {
		t.Parallel()
		d := fakedial.New()
		sp := fakespawn.New().Dial(d)
		eng := tunnel.Engine{Runner: fakerun.New(), Spawner: sp, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
		if err := eng.Spawn(context.Background(), "ubuntu", "web", specWeb0); err != nil {
			t.Fatalf("engine Spawn = %v", err)
		}
		if got, want := sp.Spawned(), []string{strings.Join(ui.SpawnArgv("ubuntu", specWeb0), " ")}; !reflect.DeepEqual(got, want) {
			t.Errorf("engine spawned = %v, want %v", got, want)
		}
	})
	t.Run("exit", func(t *testing.T) {
		t.Parallel()
		argv := ui.ExitArgv("ubuntu")
		r := fakerun.New().StubExit(0, argv...)
		eng := tunnel.Engine{Runner: r, Spawner: fakespawn.New(), Dialer: fakedial.New(), StateDir: t.TempDir()}
		if err := eng.Exit(context.Background(), "ubuntu"); err != nil {
			t.Fatalf("engine Exit = %v", err)
		}
		if got, want := r.CallKeys(), []string{fakerun.Key(argv...)}; !reflect.DeepEqual(got, want) {
			t.Errorf("engine calls = %v, want %v", got, want)
		}
	})
}

func TestModel_View_TunnelStates(t *testing.T) {
	t.Parallel()
	// Every state renders verbatim as glyph AND word — never collapsed,
	// and readable under NO_COLOR.
	tests := []struct {
		name      string
		checkExit int
		ours      bool // a recorded process of ours is alive on the port
		portUp    bool
		wantGlyph string
		wantWord  string
	}{
		{name: "our process alive and port answers is up", checkExit: 0, ours: true, portUp: true, wantGlyph: "●", wantWord: "up"},
		{name: "port answers but no process of ours is listening", checkExit: 0, portUp: true, wantGlyph: "◐", wantWord: "listening"},
		{name: "port refused is down, whatever the master", checkExit: 255, portUp: false, wantGlyph: "○", wantWord: "down"},
		{name: "our process alive but port dead is down", checkExit: 0, ours: true, portUp: false, wantGlyph: "○", wantWord: "down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(tt.checkExit, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New()
			if tt.portUp {
				d.Up("127.0.0.1:3000", time.Millisecond)
			}
			sp := fakespawn.New().Dial(d)
			stateDir := t.TempDir()
			if tt.ours {
				writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41}]}`)
				sp.Live(41)
			}
			m := tunnelModelSpawn(t, r, sp, d, stateDir)
			// Status runs in a cmd: the frame renders before it lands,
			// and an unpolled forward is pending — never a guessed down.
			cmd := m.Init()
			if view := m.View(); !strings.Contains(view, "◌ web "+specWeb0+" …") {
				t.Fatalf("View() before status missing the pending web row:\n%s", view)
			}
			m = feed(t, m, cmd)
			view := m.View()
			row := ""
			for _, line := range strings.Split(view, "\n") {
				if strings.Contains(line, specWeb0) {
					row = line
				}
			}
			if !strings.Contains(row, tt.wantGlyph) || !strings.HasSuffix(strings.TrimSpace(row), tt.wantWord) {
				t.Errorf("web row = %q, want glyph %q and word %q", row, tt.wantGlyph, tt.wantWord)
			}
		})
	}
}

// aliveWeb0 is the common fixture: master alive, only web's first
// forward up — served by a recorded process of ours (pid 41).
func aliveWeb0(t *testing.T) (ui.Model, *fakerun.Fake) {
	t.Helper()
	m, r, _ := aliveWeb0Spawn(t)
	return m, r
}

func aliveWeb0Spawn(t *testing.T) (ui.Model, *fakerun.Fake, *fakespawn.Fake) {
	t.Helper()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	sp := fakespawn.New().Dial(d).Live(41)
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41}]}`)
	return polledSpawn(t, r, sp, d, stateDir), r, sp
}

func TestModel_Update_FocusMovesHighlightAndPreview(t *testing.T) {
	t.Parallel()
	m, _ := aliveWeb0(t)
	steps := []struct {
		key         string
		wantPreview string
		wantMore    string
	}{
		{key: "", wantPreview: "$ ssh ubuntu"},
		// l focuses the pane on the first row. web is partially up:
		// space spawns only what is not up — web's second forward.
		{key: "l", wantPreview: spawnArgv("ubuntu", specWeb1)},
		{key: "j", wantPreview: spawnArgv("ubuntu", specWeb1)},
		{key: "j", wantPreview: spawnArgv("ubuntu", specAPI)},
		{key: "j", wantPreview: spawnArgv("ubuntu", specAPI)}, // clamps at the last row
		{key: "k", wantPreview: spawnArgv("ubuntu", specWeb1)},
		{key: "G", wantPreview: spawnArgv("ubuntu", specAPI)},
		{key: "g", wantPreview: spawnArgv("ubuntu", specWeb1)},
		{key: "tab", wantPreview: spawnArgv("ubuntu", specWeb1)},
		{key: "shift+tab", wantPreview: spawnArgv("ubuntu", specWeb1)},
		{key: "h", wantPreview: "$ ssh ubuntu"},
		{key: "tab", wantPreview: spawnArgv("ubuntu", specWeb1)}, // tab from the list = l
		{key: "esc", wantPreview: "$ ssh ubuntu"},
	}
	for i, st := range steps {
		if st.key != "" {
			m = press(t, m, st.key)
		}
		view := m.View()
		if !strings.Contains(view, st.wantPreview) {
			t.Errorf("step %d (%s): View() missing preview %q:\n%s", i, st.key, st.wantPreview, view)
		}
		if st.wantMore != "" && !strings.Contains(view, st.wantMore) {
			t.Errorf("step %d (%s): View() missing %q:\n%s", i, st.key, st.wantMore, view)
		}
		if st.wantMore == "" && strings.Contains(view, "more") {
			t.Errorf("step %d (%s): View() shows a count for a single command:\n%s", i, st.key, view)
		}
	}
	// The highlighted row carries the mark; the host list never moved.
	m = press(t, m, "l", "G") // api
	if host, _ := m.Selected(); host.Alias != "ubuntu" || !ui.TunnelsFocused(m) {
		t.Fatalf("Selected() = %q, focused %v; want ubuntu under tunnels focus", host.Alias, ui.TunnelsFocused(m))
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "$ ") {
			continue // the preview line, not a row
		}
		if strings.Contains(line, specAPI) && !strings.Contains(line, "› ") {
			t.Errorf("api row unmarked while highlighted: %q", line)
		}
		if strings.Contains(line, specWeb0) && strings.Contains(line, "› ") {
			t.Errorf("web row marked while api is highlighted: %q", line)
		}
	}
}

func TestModel_Update_CopyCmdCopiesPreview(t *testing.T) {
	t.Parallel()
	// c copies the DISPLAYED command (SPEC §5): with a forward row
	// highlighted that is the space command, not enter's ssh.
	m, r := aliveWeb0(t)
	want := spawnArgv("ubuntu", specAPI)
	calls := yankStubs(r, want, "pbcopy")
	m = press(t, m, "l", "G")
	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatalf("Update(c) cmd = nil, want clipboard cmd")
	}
	cmd()
	if got, want := r.CallKeys()[len(r.CallKeys())-2:], calls; !reflect.DeepEqual(got, want) {
		t.Errorf("last calls = %q, want %q", got, want)
	}
}

func TestModel_Update_SpaceBringsSetUp(t *testing.T) {
	t.Parallel()
	// Master dead, everything down: space on web spawns one process per
	// spec, in order, runs nothing through the runner, then re-polls.
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	m := polledSpawn(t, r, sp, d, t.TempDir())
	m = press(t, m, "l")
	if view := m.View(); !strings.Contains(view, "$ "+spawnArgv("ubuntu", specWeb0)) || !strings.Contains(view, "+1 more") {
		t.Fatalf("View() with web down should preview the first spawn:\n%s", view)
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg(" "))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(space) cmd = nil, want action cmd")
	}
	next, refresh := m.Update(cmd())
	m = next.(ui.Model)
	want := []string{spawnArgv("ubuntu", specWeb0), spawnArgv("ubuntu", specWeb1)}
	if got := sp.Spawned(); !reflect.DeepEqual(got, want) {
		t.Errorf("space spawned = %v, want %v", got, want)
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("space ran %v through the runner, want nothing", got)
	}
	if view := m.View(); !strings.Contains(view, "web up") {
		t.Errorf("View() missing the success notice:\n%s", view)
	}
	if refresh == nil {
		t.Errorf("Update(tunnelDone) cmd = nil, want a status re-poll")
	}
}

func TestModel_Update_SpaceTakesSetDown(t *testing.T) {
	t.Parallel()
	// Every forward of web up, ours: space kills each process.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond).Up("127.0.0.1:3001", time.Millisecond)
	sp := fakespawn.New().Dial(d).Live(41).Live(42)
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41},{"set":"web","spec":"`+specWeb1+`","pid":42}]}`)
	m := polledSpawn(t, r, sp, d, stateDir)
	m = press(t, m, "l")
	if view := m.View(); !strings.Contains(view, "$ kill -TERM 41") || !strings.Contains(view, "+1 more") {
		t.Fatalf("View() with web fully up should preview the kill:\n%s", view)
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg(" "))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(space) cmd = nil, want action cmd")
	}
	m = update(t, m, cmd())
	if got := sp.Killed(); !reflect.DeepEqual(got, []int{41, 42}) {
		t.Errorf("space killed = %v, want [41 42]", got)
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("space ran %v through the runner, want nothing", got)
	}
	if view := m.View(); !strings.Contains(view, "web down") {
		t.Errorf("View() missing the success notice:\n%s", view)
	}
}

// blockedStateFile returns a state dir whose ubuntu.json is a DIRECTORY:
// the engine can spawn (its log dir exists) and every record then fails
// with ErrStateWrite while the process side succeeded.
func blockedStateFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ubuntu.json"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

// readOnlyState returns a state dir whose ubuntu.json records web's two
// forwards as pids 41/42 and cannot be rewritten: every record update
// then fails with ErrStateWrite while the process side succeeded.
func readOnlyState(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("a read-only file does not stop root")
	}
	dir := t.TempDir()
	writeState(t, dir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41},{"set":"web","spec":"`+specWeb1+`","pid":42}]}`)
	if err := os.Chmod(filepath.Join(dir, "ubuntu.json"), 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return dir
}

func TestModel_Update_SpaceOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// webUp records both web forwards as ours and answering, so
		// space goes down.
		webUp    bool
		script   func(sp *fakespawn.Fake)
		stateDir func(t *testing.T) string
		want     []string // all present in the notice
		wantNot  []string
	}{
		{
			name: "a spawn failure is reported with ssh's message",
			script: func(sp *fakespawn.Fake) {
				sp.Exits("bind: Address already in use", tunnel.SpawnArgv("ubuntu", specWeb0)...)
			},
			stateDir: func(t *testing.T) string { return t.TempDir() },
			want:     []string{"web: ", "Address already in use"},
			wantNot:  []string{"web up"},
		},
		{
			name:     "state write failures warn on one line and the tunnels are real",
			script:   func(*fakespawn.Fake) {},
			stateDir: blockedStateFile,
			// Two specs => SpawnSet joins two state errors: both must stay
			// on the notice line, not push the frame off the terminal.
			want:    []string{"warning: ", "not recorded"},
			wantNot: []string{"web: "},
		},
		{
			name:  "a refused kill joined with a state failure is a failure",
			webUp: true,
			script: func(sp *fakespawn.Fake) {
				sp.KillErr = errors.New("operation not permitted")
			},
			stateDir: readOnlyState,
			// KillSet joins the kill failure and the cache failure: the
			// kill's reason must win the label, never "warning".
			want:    []string{"web: ", "operation not permitted"},
			wantNot: []string{"warning: "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New()
			sp := fakespawn.New().Dial(d)
			tt.script(sp)
			stateDir := tt.stateDir(t)
			if tt.webUp {
				d.Up("127.0.0.1:3000", time.Millisecond).Up("127.0.0.1:3001", time.Millisecond)
				sp.LiveOn(41, "3000").LiveOn(42, "3001")
			}
			m := tunnelModelSpawn(t, r, sp, d, stateDir)
			m = feed(t, m, m.Init())
			// An unreadable state file opens the cache popup once at the
			// first poll (SASASH.4.11): dismissed, the keys are the
			// pane's again.
			if ui.PopupOpen(m) {
				m = press(t, m, "esc")
			}
			m = press(t, m, "l") // web
			next, cmd := m.Update(keyMsg(" "))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(space) cmd = nil, want action cmd")
			}
			m = update(t, m, cmd())
			// A failure also opens the popup; closing it leaves the
			// one-line outcome on the bottom line, which is what these
			// cases pin.
			if ui.PopupOpen(m) {
				m = press(t, m, "esc")
			}
			view := m.View()
			for _, w := range tt.want {
				if !strings.Contains(view, w) {
					t.Errorf("View() missing %q:\n%s", w, view)
				}
			}
			for _, w := range tt.wantNot {
				if strings.Contains(view, w) {
					t.Errorf("View() shows %q, want it absent:\n%s", w, view)
				}
			}
			if h := strings.Count(view, "\n") + 1; h > 30 {
				t.Errorf("View() height = %d, want <= 30 (a notice must never add a line)", h)
			}
			// ssh's reason must survive an 80-column terminal too: the
			// argv the engine echoes ahead of it was on the preview line.
			narrow := update(t, m, tea.WindowSizeMsg{Width: 80, Height: 30}).View()
			for _, w := range tt.want {
				if !strings.Contains(narrow, w) {
					t.Errorf("View() at 80 cols missing %q:\n%s", w, narrow)
				}
			}
		})
	}
}

func TestModel_Update_SpaceNeedsHighlight(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg(" "))
	m = next.(ui.Model)
	if cmd != nil || len(r.CallKeys()) != before {
		t.Errorf("Update(space) without a highlight ran something: cmd %v, calls %v", cmd != nil, r.CallKeys()[before:])
	}
	if view := m.View(); !strings.Contains(view, "l cycles") {
		t.Errorf("View() missing the tab hint:\n%s", view)
	}
}

func TestModel_Update_RestartMaster(t *testing.T) {
	t.Parallel()
	// r exits the interactive control master; the next connect starts a
	// fresh one, and the tunnels — their own processes — are not touched.
	// X is delete now (SASASH.4.25), not kill-all: it never reaches here.
	refused := fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("Control socket connect: No such file")}}
	tests := []struct {
		name       string
		exitReply  fakerun.Reply
		wantNotice string
	}{
		{
			name:       "r exits the interactive master",
			exitReply:  fakerun.Reply{Result: run.Result{Code: 0}},
			wantNotice: "master exited — a fresh one starts on the next connect",
		},
		{
			name:       "r on a dead master says so",
			exitReply:  refused,
			wantNotice: "restart: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().
				Stub(tt.exitReply, "ssh", "-O", "exit", "ubuntu").
				StubExit(0, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
			sp := fakespawn.New().Dial(d).Live(41)
			stateDir := t.TempDir()
			writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41}]}`)
			m := polledSpawn(t, r, sp, d, stateDir)
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("r"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(r) cmd = nil, want the master-exit cmd")
			}
			m = update(t, m, cmd())
			if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"ssh -O exit ubuntu"}) {
				t.Errorf("r calls = %v, want the master exit", got)
			}
			// r never ends a tunnel process.
			if got := sp.Killed(); got != nil {
				t.Errorf("r killed %v, want nothing (tunnels are not the master)", got)
			}
			view := m.View()
			if !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
			if strings.HasSuffix(tt.wantNotice, ": ") && !strings.Contains(view, "No such file") {
				t.Errorf("View() hides ssh's message:\n%s", view)
			}
		})
	}
}

func TestModel_Update_HighlightResetsOnHostChange(t *testing.T) {
	t.Parallel()
	// A highlight is per host: moving to another host that also has
	// forwards must not leave an unrelated row armed for space.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "check", "spare")
	m := polled(t, r, fakedial.New())
	// Under tunnels focus G moves the highlight, never the host.
	m = press(t, m, "l", "G")
	if host, _ := m.Selected(); host.Alias != "ubuntu" {
		t.Fatalf("Selected() under tunnels focus after G = %q, want ubuntu", host.Alias)
	}
	// Back on the list, G changes host and no highlight comes along.
	m = press(t, m, "h", "G")
	if view := m.View(); !strings.Contains(view, "$ ssh spare") || strings.Contains(view, "› ○ db") || ui.TunnelsFocused(m) {
		t.Errorf("View() after h G carries ubuntu's highlight onto spare:\n%s", view)
	}
}

func TestModel_Update_TunnelKeysInertWhileFiltering(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	// With a row highlighted — the realistic state — every key is text.
	m = press(t, m, "l", "/", " ", "X", "r")
	if got := ui.FilterText(m); got != " Xr" {
		t.Errorf("FilterText() = %q, want %q", got, " Xr")
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("calls while filtering = %v, want none", got)
	}
}

func TestModel_Update_SelectionChangeFetchesStatus(t *testing.T) {
	t.Parallel()
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(255, "ssh", "-O", "check", "bare")
	m := polled(t, r, fakedial.New().Up("127.0.0.1:3000", time.Millisecond))
	next, cmd := m.Update(keyMsg("j"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(j) onto an unpolled host cmd = nil, want status cmd")
	}
	// Until it lands the status is pending, never a guessed state.
	if ui.TunnelStatusKnown(m, "bare") {
		t.Errorf("bare's status should be pending before its poll lands")
	}
	m = feed(t, m, cmd)
	view := m.View()
	if !ui.TunnelStatusKnown(m, "bare") {
		t.Errorf("bare's status should have landed after the poll")
	}
	if !strings.Contains(view, "$ ssh bare") {
		t.Errorf("View() preview should be enter's ssh for the new host:\n%s", view)
	}
	// Back to a host polled earlier: its status is stale — the master
	// may have died meanwhile — so it reads pending and is re-polled,
	// never shown as live from memory.
	next, cmd = m.Update(keyMsg("k"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(k) back onto ubuntu cmd = nil, want a re-poll")
	}
	if ui.TunnelStatusKnown(m, "ubuntu") || strings.Contains(m.View(), "● web") {
		t.Errorf("View() shows ubuntu's stale status as live:\n%s", m.View())
	}
}

func TestModel_Update_OnePollInFlightPerHost(t *testing.T) {
	t.Parallel()
	// Moving the cursor issues a poll for the new host; moving onto it
	// again while that poll is still out must not spawn another `-O
	// check` — one process per host at a time, however fast j/k go.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(255, "ssh", "-O", "check", "bare")
	m := polled(t, r, fakedial.New())
	next, first := m.Update(keyMsg("j")) // bare: poll issued, not delivered
	m = next.(ui.Model)
	if first == nil {
		t.Fatalf("Update(j) cmd = nil, want bare's poll")
	}
	m = press(t, m, "k") // back to ubuntu: its own re-poll, left in flight
	next, again := m.Update(keyMsg("j"))
	m = next.(ui.Model)
	if again != nil {
		t.Errorf("Update(j) with bare's poll in flight cmd = %v, want nil", again)
	}
	// The tick skips it too; only the path probe runs.
	before := len(r.CallKeys())
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("tick calls with a poll in flight = %v, want none", got)
	}
	// Once it lands the host polls again.
	m = feed(t, m, first)
	if !ui.TunnelStatusKnown(m, "bare") {
		t.Errorf("bare's status should have landed after its poll")
	}
	if _, cmd := m.Update(ui.RefreshTick()); cmd == nil {
		t.Errorf("Update(tick) after the poll landed cmd = nil, want a re-poll")
	}
}

func TestModel_Update_RefreshTickPollsSelected(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	_, cmd := m.Update(ui.RefreshTick())
	feed(t, m, cmd)
	if got, want := r.CallKeys()[before:], []string{"ssh -O check ubuntu"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tick calls = %v, want %v", got, want)
	}
}

func TestModel_View_TunnelStatusProblems(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, r *fakerun.Fake, stateDir string)
		want  string
	}{
		{
			name: "corrupt state cache is said, not hidden",
			setup: func(t *testing.T, r *fakerun.Fake, stateDir string) {
				r.StubExit(0, "ssh", "-O", "check", "ubuntu")
				if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte("{not json"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			},
			want: "state cache unreadable",
		},
		{
			name: "seam failure is a notice, not a state",
			setup: func(t *testing.T, r *fakerun.Fake, stateDir string) {
				r.Stub(fakerun.Reply{Err: errors.New("no ssh binary")}, "ssh", "-O", "check", "ubuntu")
			},
			want: "! status: no ssh binary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New()
			stateDir := t.TempDir()
			tt.setup(t, r, stateDir)
			m := tunnelModel(t, r, fakedial.New(), stateDir)
			m = feed(t, m, m.Init())
			if view := m.View(); !strings.Contains(view, tt.want) {
				t.Errorf("View() missing %q:\n%s", tt.want, view)
			}
		})
	}
}

func TestModel_View_RecordedAdhocForwardRow(t *testing.T) {
	t.Parallel()
	// A forward the state file remembers outside any declared set shows
	// as its own ad-hoc row — attributed, but with a live state like the
	// rest (here down: nothing answers on its port).
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	stateDir := t.TempDir()
	state := `{"forwards":[{"set":"","spec":"5000:localhost:5000"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := tunnelModel(t, r, fakedial.New(), stateDir)
	m = feed(t, m, m.Init())
	row := ""
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "5000:localhost:5000") {
			row = line
		}
	}
	if !strings.Contains(row, "unsaved") || !strings.HasSuffix(strings.TrimSpace(row), "down") {
		t.Errorf("recorded ad-hoc row = %q, want an unsaved row marked down", row)
	}
	// It is the last row: l then G reaches it, and space would spawn it.
	m = press(t, m, "l", "G")
	if view := m.View(); !strings.Contains(view, "$ "+spawnArgv("ubuntu", "5000:localhost:5000")) {
		t.Errorf("View() after shift+tab should preview the ad-hoc row's spawn:\n%s", view)
	}
}

func TestModel_View_HelpListsTunnelKeys(t *testing.T) {
	t.Parallel()
	// Presence in the keys panel at a size where it fits whole.
	m := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 100, Height: 60})
	m = press(t, m, "?")
	view := m.View()
	for _, want := range []string{"toggle set", "restart master", "delete forward set", "tunnels pane", "host list"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() keys panel missing %q:\n%s", want, view)
		}
	}
}

func TestModel_View_NoTunnelsWithoutEngine(t *testing.T) {
	t.Parallel()
	// ui.Config{} (every other fixture) must never shell out or render
	// the section: the seam is nil, not a zero engine over run.OS{}.
	m := sized(t, tunnelHosts())
	// Scoped to the panes: the help line legitimately says "tunnels pane".
	for _, line := range strings.Split(m.View(), "\n") {
		if !strings.Contains(line, "•") && strings.Contains(line, "tunnels") {
			t.Errorf("View() renders the tunnels section with no engine: %q", line)
		}
	}
	if m = press(t, m, "l"); ui.TunnelsFocused(m) {
		t.Errorf("l focused a tunnels pane that does not exist")
	}
	if _, cmd := m.Update(keyMsg(" ")); cmd != nil {
		t.Errorf("Update(space) with no engine cmd = %v, want nil", cmd)
	}
}

// TestModel_View_FocusNeverHidesPathsOrTunnels pins SASASH.4.23: paths AND
// tunnels render in every focus state — list, paths, tunnels — so focusing
// a section only moves the highlight, it never drops the other section's
// content. (The reported bug: an added tunnel showed in the list-focus
// detail view but vanished when focused into paths.)
func TestModel_View_FocusNeverHidesPathsOrTunnels(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "dual")
	hosts := []hostmodel.Host{{
		Alias: "dual", Paths: []probe.Path{pathLAN, pathTS},
		Forwards: []hostmodel.ForwardSet{{Name: "web", Specs: []string{specWeb0}}},
	}}
	d := fakedial.New().Up("192.0.2.10:22", 5*time.Millisecond).Down("198.51.100.5:2222")
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: tunnelEngine(t, r, d)})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	m = ui.WithSwitcher(m,
		func(alias, name string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
		func(alias string) ([]probe.Path, string, bool) {
			return []probe.Path{pathLAN, pathTS}, "192.0.2.10", true
		},
		nil)

	// l cycles list -> paths -> tunnels; each state must show both sections.
	states := []struct {
		name  string
		keys  []string
		paths bool // want the paths section focused (a › on a path row)
		tuns  bool // want the tunnels section focused (a › on a forward row)
	}{
		{name: "list focus", keys: nil},
		{name: "paths focus", keys: []string{"l"}, paths: true},
		{name: "tunnels focus", keys: []string{"l", "l"}, tuns: true},
	}
	for _, st := range states {
		mm := m
		if len(st.keys) > 0 {
			mm = press(t, mm, st.keys...)
		}
		if st.paths && !ui.PathsFocused(mm) {
			t.Errorf("%s: PathsFocused = false, want the paths section focused", st.name)
		}
		if st.tuns && !ui.TunnelsFocused(mm) {
			t.Errorf("%s: TunnelsFocused = false, want the tunnels section focused", st.name)
		}
		view := mm.View()
		// The paths section: its label and the lan path row.
		if !strings.Contains(view, "paths") || !strings.Contains(view, "lan") {
			t.Errorf("%s: paths section hidden:\n%s", st.name, view)
		}
		// The tunnels section: its label and the web forward row.
		if !strings.Contains(view, "tunnels") || !strings.Contains(view, specWeb0) {
			t.Errorf("%s: tunnels section hidden:\n%s", st.name, view)
		}
	}
}

// TestModel_View_AdoptsVariantTunnelUnderTheParent pins SASASH.4.23's
// migration: a + recorded under a variant alias (ubuntu1.office) before the
// variant rows were retired surfaces under the parent machine (ubuntu1) on
// the next poll, and the variant state file is folded away.
func TestModel_View_AdoptsVariantTunnelUnderTheParent(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu1.office.json"),
		[]byte(`{"forwards":[{"set":"","spec":"8080:localhost:8080","pid":77}]}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hosts := []hostmodel.Host{
		{Alias: "ubuntu1", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "vpn", Host: "198.51.100.5"}}},
		{Alias: "ubuntu1.office", Paths: []probe.Path{{Host: "192.0.2.10"}}},
	}
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu1")
	d := fakedial.New().Up("127.0.0.1:8080", time.Millisecond)
	sp := fakespawn.New().Dial(d).Live(77)
	eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: stateDir, Timeout: 50 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())

	if view := m.View(); !strings.Contains(view, "8080:localhost:8080") {
		t.Errorf("the parent's tunnels pane should surface the adopted ad-hoc:\n%s", view)
	}
	if b, err := os.ReadFile(filepath.Join(stateDir, "ubuntu1.office.json")); err == nil {
		t.Errorf("variant state file should be removed, got %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(stateDir, "ubuntu1.json")); !strings.Contains(string(b), "8080:localhost:8080") {
		t.Errorf("parent state should hold the adopted tunnel, got %s", b)
	}
}

// TestModel_Update_AddForwardOnVariantAttributesToParent pins that pressing
// + while a filter-surfaced variant is selected binds the forward to the
// parent machine, never the path-forcing alias (SASASH.4.23).
func TestModel_Update_AddForwardOnVariantAttributesToParent(t *testing.T) {
	t.Parallel()
	hosts := []hostmodel.Host{
		{Alias: "ubuntu1", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "vpn", Host: "198.51.100.5"}}},
		{Alias: "ubuntu1.office", Paths: []probe.Path{{Host: "192.0.2.10"}}},
	}
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu1")
	d := fakedial.New()
	eng := tunnel.Engine{Runner: r, Spawner: fakespawn.New().Dial(d), Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	m = press(t, m, "/", "u", "b", "u", "n", "t", "u", "1", ".", "o", "f", "f", "i", "c", "e", "enter")
	if host, _ := m.Selected(); host.Alias != "ubuntu1.office" {
		t.Fatalf("Selected() = %q, want the surfaced variant ubuntu1.office", host.Alias)
	}
	m = press(t, m, "+")
	if view := m.View(); !strings.Contains(view, "forward ubuntu1:") || strings.Contains(view, "forward ubuntu1.office:") {
		t.Errorf("+ on a variant should bind the prompt to the parent ubuntu1:\n%s", view)
	}
}

// TestModel_Update_CancelForwardOnVariantKeysTheParent pins that x on a
// forward row keys the parent machine's state even when a variant is the
// selected host (SASASH.4.23): the kill hits the parent's recorded pid, not
// an empty variant state file.
func TestModel_Update_CancelForwardOnVariantKeysTheParent(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	// A live ad-hoc recorded under the PARENT (as the migration leaves it).
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu1.json"),
		[]byte(`{"forwards":[{"set":"","spec":"18099:localhost:18099","pid":77}]}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hosts := []hostmodel.Host{
		{Alias: "ubuntu1", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "vpn", Host: "198.51.100.5"}}},
		{Alias: "ubuntu1.office", Paths: []probe.Path{{Host: "192.0.2.10"}}},
	}
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu1")
	d := fakedial.New().Up("127.0.0.1:18099", time.Millisecond)
	sp := fakespawn.New().Dial(d).Live(77)
	eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: stateDir, Timeout: 50 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	// Surface and select the variant, focus the tunnels pane on the row.
	m = press(t, m, "/", "u", "b", "u", "n", "t", "u", "1", ".", "o", "f", "f", "i", "c", "e", "enter", "l")
	if host, _ := m.Selected(); host.Alias != "ubuntu1.office" || !ui.TunnelsFocused(m) {
		t.Fatalf("want the variant selected with tunnels focus; got %q focused %v", host.Alias, ui.TunnelsFocused(m))
	}
	next, cmd := m.Update(keyMsg("x"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(x) on the variant cmd = nil, want the kill cmd")
	}
	m = update(t, m, cmd())
	// The parent's recorded pid was killed — the op keyed ubuntu1, not the
	// empty ubuntu1.office state.
	if got := sp.Killed(); !reflect.DeepEqual(got, []int{77}) {
		t.Errorf("x on the variant killed = %v, want the parent's pid [77]", got)
	}
}

// TestModel_Update_AdhocTunnelOnVariantAttributesToParent pins that an
// ad-hoc `-N -L` typed against a saved variant in the connect prompt records
// under the canonical parent, not the path-forcing alias (SASASH.4.24).
func TestModel_Update_AdhocTunnelOnVariantAttributesToParent(t *testing.T) {
	t.Parallel()
	hosts := []hostmodel.Host{
		{Alias: "ubuntu1", Paths: []probe.Path{{Name: "office", Host: "192.0.2.10"}, {Name: "vpn", Host: "198.51.100.5"}}},
		{Alias: "ubuntu1.office", Paths: []probe.Path{{Host: "192.0.2.10"}}},
	}
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu1")
	d := fakedial.New()
	eng := tunnel.Engine{Runner: r, Spawner: fakespawn.New().Dial(d), Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = feed(t, m, m.Init())
	m = press(t, m, "a")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ubuntu1.office -N -L 8080:localhost:80")})
	m = press(t, m, "enter")
	// The engine flow names the parent machine in its spawn plan, never the
	// path-variant: if it read "…8080:localhost:80 ubuntu1.office" the tunnel
	// would be keyed to the variant again.
	if view := m.View(); strings.Contains(view, "ubuntu1.office") || !strings.Contains(view, "8080:localhost:80 ubuntu1") {
		t.Errorf("ad-hoc -N on a saved variant should target the parent ubuntu1:\n%s", view)
	}
}

// --- SASASH.4.19 lifecycle: declare (n) + delete (D) ---

func TestModel_Update_DeclareForwardSet(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	m := polledSpawn(t, r, sp, d, t.TempDir())
	var saved [][]string
	m = ui.WithSaveForwardSet(m, func(alias, name string, specs []string) error {
		saved = append(saved, append([]string{alias, name}, specs...))
		return nil
	})
	m = press(t, m, "l", "n") // focus tunnels, declare
	if !ui.Prompting(m) || !strings.Contains(m.View(), "new set name") {
		t.Fatalf("n should open the set-name prompt:\n%s", m.View())
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("cache")})
	m = press(t, m, "enter")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("6379")}) // shorthand
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if want := [][]string{{"ubuntu", "cache", "6379:localhost:6379"}}; !reflect.DeepEqual(saved, want) {
		t.Errorf("saveForwardSet = %v, want %v", saved, want)
	}
	if got := sp.Spawned(); len(got) != 0 {
		t.Errorf("declare starts nothing, spawned %v", got)
	}
	if view := m.View(); !strings.Contains(view, "declared cache on ubuntu") {
		t.Errorf("notice:\n%s", view)
	}
}

func TestModel_Update_DeleteForwardSet_NotLive(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	m := polledSpawn(t, r, sp, d, t.TempDir()) // no state -> web is down
	var removed [][]string
	m = ui.WithRemoveForwardSet(m, func(alias, name string) error {
		removed = append(removed, []string{alias, name})
		return nil
	})
	m = press(t, m, "l") // web's first row highlighted
	m = press(t, m, "X")
	view := m.View()
	if !ui.PopupOpen(m) || !strings.Contains(view, "delete web on ubuntu") || !strings.Contains(view, "@sasash forwards web") || !strings.Contains(view, specWeb0) || !strings.Contains(view, specWeb1) {
		t.Fatalf("D should confirm with the exact line:\n%s", view)
	}
	// A not-live set must NOT preview a kill — the two branches are distinct.
	if strings.Contains(view, "web is live") || strings.Contains(view, "kill -TERM") {
		t.Errorf("a not-live set must not preview a down:\n%s", view)
	}
	if !strings.Contains(view, "not running") {
		t.Errorf("the not-live preview should say the tunnels are not running:\n%s", view)
	}
	if c := press(t, m, "esc"); ui.PopupOpen(c) || len(removed) != 0 || !strings.Contains(c.View(), "web kept") {
		t.Errorf("esc cancels, removes nothing:\n%s", c.View())
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if want := [][]string{{"ubuntu", "web"}}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removeForwardSet = %v, want %v", removed, want)
	}
	if got := sp.Killed(); len(got) != 0 {
		t.Errorf("a not-live delete kills nothing, killed %v", got)
	}
	if view := m.View(); !strings.Contains(view, "deleted web from ubuntu") {
		t.Errorf("notice:\n%s", view)
	}
}

func TestModel_Update_DeleteForwardSet_LiveIsTwoStep(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	sp := fakespawn.New().Dial(d).Live(41)
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41}]}`)
	m := polledSpawn(t, r, sp, d, stateDir)
	var removed [][]string
	m = ui.WithRemoveForwardSet(m, func(alias, name string) error { removed = append(removed, []string{alias, name}); return nil })
	m = press(t, m, "l") // web0 highlighted, up
	m = press(t, m, "X")
	if view := m.View(); !ui.PopupOpen(m) || !strings.Contains(view, "web is live") || !strings.Contains(view, "kill -TERM 41") || !strings.Contains(view, "@sasash forwards web") {
		t.Fatalf("D on a live set previews down-then-delete:\n%s", view)
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if got := sp.Killed(); !reflect.DeepEqual(got, []int{41}) {
		t.Errorf("live delete KillSets pid 41, killed %v", got)
	}
	if want := [][]string{{"ubuntu", "web"}}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removeForwardSet = %v, want %v", removed, want)
	}
}

func TestModel_Update_DeleteAdhoc_ForgetsNoConfig(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:8080", time.Millisecond)
	sp := fakespawn.New().Dial(d).Live(55)
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"","spec":"8080:localhost:8080","pid":55}]}`)
	m := polledSpawn(t, r, sp, d, stateDir)
	removeCalled := false
	m = ui.WithRemoveForwardSet(m, func(alias, name string) error { removeCalled = true; return nil })
	m = press(t, m, "l", "G") // last row = the ad-hoc 8080
	m = press(t, m, "X")
	if view := m.View(); !ui.PopupOpen(m) || !strings.Contains(view, "forget unsaved on ubuntu") || !strings.Contains(view, "8080:localhost:8080") || !strings.Contains(view, "state only") {
		t.Fatalf("D on an ad-hoc row confirms a forget:\n%s", view)
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if got := sp.Killed(); !reflect.DeepEqual(got, []int{55}) {
		t.Errorf("ad-hoc forget kills pid 55, killed %v", got)
	}
	if removeCalled {
		t.Errorf("an ad-hoc forget must NOT write sasash.conf")
	}
	// The record is purged from the state cache.
	if b, _ := os.ReadFile(filepath.Join(stateDir, "ubuntu.json")); strings.Contains(string(b), "8080") {
		t.Errorf("the ad-hoc record should be forgotten, state = %s", b)
	}
	if view := m.View(); !strings.Contains(view, "forgot 8080:localhost:8080 on ubuntu") {
		t.Errorf("notice:\n%s", view)
	}
}

func TestModel_Update_LifecycleGuards(t *testing.T) {
	t.Parallel()
	t.Run("n and D need their hooks", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New()
		m := polledSpawn(t, r, fakespawn.New().Dial(d), d, t.TempDir()) // no hooks wired
		if v := press(t, m, "l", "n").View(); !strings.Contains(v, "declaring forward sets is not available") {
			t.Errorf("n without the hook:\n%s", v)
		}
		if v := press(t, m, "l", "X").View(); !strings.Contains(v, "deleting forward sets is not available") {
			t.Errorf("D without the hook:\n%s", v)
		}
	})
	t.Run("D on a recorded-but-undeclared set is not a delete", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
		d := fakedial.New().Up("127.0.0.1:9000", time.Millisecond)
		sp := fakespawn.New().Dial(d).Live(70)
		stateDir := t.TempDir()
		// A named set recorded in state but NOT in sasash.conf (host.Forwards).
		writeState(t, stateDir, `{"forwards":[{"set":"extra","spec":"9000:localhost:9000","pid":70}]}`)
		m := polledSpawn(t, r, sp, d, stateDir)
		removeCalled := false
		m = ui.WithRemoveForwardSet(m, func(alias, name string) error { removeCalled = true; return nil })
		m = press(t, m, "l", "G", "X") // last row is the recorded extra set
		if v := m.View(); ui.PopupOpen(m) || !strings.Contains(v, "extra is not declared on ubuntu") {
			t.Errorf("D on an undeclared set should refuse, not confirm:\n%s", v)
		}
		if removeCalled {
			t.Error("nothing should be removed for an undeclared set")
		}
	})
}

func TestModel_Update_DeleteAdhoc_SurvivorKeepsRecord(t *testing.T) {
	t.Parallel()
	// The ad-hoc process ignores SIGTERM (stays alive): Forget is skipped so
	// the record survives and x can still reach it — never an orphaned tunnel.
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:8080", time.Millisecond)
	sp := fakespawn.New().Dial(d).Live(55)
	sp.KillErr = errors.New("operation not permitted") // the kill fails; the process lives on
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"","spec":"8080:localhost:8080","pid":55}]}`)
	m := polledSpawn(t, r, sp, d, stateDir)
	m = press(t, m, "l", "G", "X")
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	// Forget was SKIPPED because the kill errored: the record is kept, never
	// orphaning a process sasash could not end.
	if b, _ := os.ReadFile(filepath.Join(stateDir, "ubuntu.json")); !strings.Contains(string(b), "8080") {
		t.Errorf("a kill-failed ad-hoc must keep its record (not orphan the process), state = %s", b)
	}
	if view := m.View(); !strings.Contains(view, "kept") || !strings.Contains(view, "not forgotten") {
		t.Errorf("the notice should say the record was kept, not forgotten:\n%s", view)
	}
}
