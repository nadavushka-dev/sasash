package ui_test

import (
	"context"
	"reflect"
	"strconv"
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
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestLocalPort_MatchesEngine(t *testing.T) {
	t.Parallel()
	// The UI's port rule is a mirror of the engine's: for every spec the
	// port `+` dials must be the port Status would judge the forward by.
	specs := []string{
		"8080:localhost:80",
		"127.0.0.1:8080:localhost:80",
		"*:8080:localhost:80",
		"[::1]:8080:localhost:80",
		"localhost:8080:localhost:80",
		"nonsense",
		"",
		":8080:localhost:80",
	}
	for _, spec := range specs {
		t.Run(strconv.Quote(spec), func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New()
			eng := tunnel.Engine{Runner: r, Spawner: fakespawn.New().Dial(d), Dialer: d, StateDir: t.TempDir(), Timeout: time.Millisecond}
			st, err := eng.Status(context.Background(), "ubuntu", []hostmodel.ForwardSet{{Name: "s", Specs: []string{spec}}})
			if err != nil {
				t.Fatalf("Status() = %v", err)
			}
			want := ""
			if len(st.Forwards) == 1 {
				want = st.Forwards[0].Port
			}
			if got, _ := ui.LocalPort(spec); got != want {
				t.Errorf("LocalPort(%q) = %q, want the engine's %q", spec, got, want)
			}
		})
	}
}

func TestWithLocalPort_ExactStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec string
		port string
		want string
	}{
		{name: "bare local port", spec: "8080:localhost:80", port: "8081", want: "8081:localhost:80"},
		{name: "bind address kept", spec: "127.0.0.1:8080:localhost:80", port: "8081", want: "127.0.0.1:8081:localhost:80"},
		{name: "remote port untouched", spec: "8080:localhost:8080", port: "8081", want: "8081:localhost:8080"},
		{name: "no local port leaves the spec alone", spec: "nonsense", port: "8081", want: "nonsense"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, field := ui.LocalPort(tt.spec)
			if got := ui.WithLocalPort(tt.spec, field, tt.port); got != tt.want {
				t.Errorf("WithLocalPort(%q, %q) = %q, want %q", tt.spec, tt.port, got, tt.want)
			}
		})
	}
}

// typeSpec opens the forward prompt and submits spec; the returned cmd
// is the check-and-add, not yet run.
func typeSpec(t *testing.T, m ui.Model, spec string) (ui.Model, tea.Cmd) {
	t.Helper()
	m = press(t, m, "+")
	if !ui.Prompting(m) {
		t.Fatalf("Prompting() = false after +, want the forward prompt")
	}
	if view := m.View(); !strings.Contains(view, "forward ubuntu:") || !strings.Contains(view, "enter add") {
		t.Fatalf("View() missing the forward prompt or its help:\n%s", view)
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!" + spec)})
	next, cmd := m.Update(keyMsg("enter"))
	return next.(ui.Model), cmd
}

func TestModel_Update_AddForwardFree(t *testing.T) {
	t.Parallel()
	// Port free: the spec goes in as an ad-hoc forward, one spawned process.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	m := polledSpawn(t, r, sp, d, t.TempDir())
	m, cmd := typeSpec(t, m, "8080:localhost:80")
	if cmd == nil || ui.Prompting(m) {
		t.Fatalf("enter: cmd %v, prompting %v; want the add cmd and the prompt closed", cmd != nil, ui.Prompting(m))
	}
	before := len(r.CallKeys())
	m = update(t, m, cmd())
	if got, want := sp.Spawned(), []string{spawnArgv("ubuntu", "8080:localhost:80")}; !reflect.DeepEqual(got, want) {
		t.Errorf("add spawned = %v, want %v", got, want)
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("add ran %v through the runner, want nothing", got)
	}
	if view := m.View(); !strings.Contains(view, "session-only tunnel up: 8080:localhost:80") {
		t.Errorf("View() missing the added notice:\n%s", view)
	}
}

func TestModel_Update_AddForwardConflictOffersNext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		heldUpTo int    // every port 8080..heldUpTo answers
		wantSpec string // prompt value after the conflict
		wantNote []string
	}{
		{name: "next port offered, visibly", heldUpTo: 8081, wantSpec: "8082:localhost:80", wantNote: []string{"8080 is in use", "8082 is free"}},
		{name: "nothing free within reach", heldUpTo: 8080 + 50, wantSpec: "8080:localhost:80", wantNote: []string{"8080 is in use", "no free port"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New()
			for p := 8080; p <= tt.heldUpTo; p++ {
				d.Up("127.0.0.1:"+strconv.Itoa(p), time.Millisecond)
			}
			sp := fakespawn.New().Dial(d)
			m := polledSpawn(t, r, sp, d, t.TempDir())
			m, cmd := typeSpec(t, m, "8080:localhost:80")
			before := len(r.CallKeys())
			next, focus := m.Update(cmd()) // the conflict lands
			m = next.(ui.Model)
			if focus != nil {
				focus() // cursor focus, no msgs of interest
			}
			// Nothing ran: the remap is on screen, not applied.
			if got := r.CallKeys()[before:]; len(got) != 0 || len(sp.Spawned()) != 0 {
				t.Fatalf("calls after a conflict = %v, spawned %v, want none", got, sp.Spawned())
			}
			if !ui.Prompting(m) {
				t.Fatalf("Prompting() = false after a conflict, want the prompt reopened")
			}
			view := m.View()
			if !strings.Contains(view, "forward ubuntu: !"+tt.wantSpec) {
				t.Errorf("View() prompt should hold %q:\n%s", tt.wantSpec, view)
			}
			for _, w := range tt.wantNote {
				if !strings.Contains(view, w) {
					t.Errorf("View() missing %q:\n%s", w, view)
				}
			}
			if tt.heldUpTo > 8081 {
				return
			}
			// enter accepts the offered port — and re-checks it first.
			m = ui.StaticCursor(m)
			next, cmd = m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("enter on the offered spec cmd = nil, want the add cmd")
			}
			m = update(t, m, cmd())
			if got, want := sp.Spawned(), []string{spawnArgv("ubuntu", tt.wantSpec)}; !reflect.DeepEqual(got, want) {
				t.Errorf("add spawned = %v, want %v", got, want)
			}
			if view := m.View(); !strings.Contains(view, "session-only tunnel up: "+tt.wantSpec) {
				t.Errorf("View() missing the added notice:\n%s", view)
			}
		})
	}
}

func TestModel_Update_AddForwardRaceReported(t *testing.T) {
	t.Parallel()
	// Free at the check, taken by the add: ssh's bind failure is the
	// notice, never swallowed into "added".
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d).Exits("bind [127.0.0.1]:8080: Address already in use", tunnel.SpawnArgv("ubuntu", "8080:localhost:80")...)
	m := polledSpawn(t, r, sp, d, t.TempDir())
	m, cmd := typeSpec(t, m, "8080:localhost:80")
	m = update(t, m, cmd())
	view := m.View()
	if !strings.Contains(view, "add: ") || !strings.Contains(view, "Address already in use") {
		t.Errorf("View() missing ssh's bind failure:\n%s", view)
	}
	if strings.Contains(view, "added") {
		t.Errorf("View() claims the add succeeded:\n%s", view)
	}
}

func TestModel_Update_AddForwardRejectsBadSpec(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	m, cmd := typeSpec(t, m, "nonsense")
	if cmd != nil || !ui.Prompting(m) {
		t.Errorf("enter on a bad spec: cmd %v, prompting %v; want no cmd and the prompt kept", cmd != nil, ui.Prompting(m))
	}
	if view := m.View(); !strings.Contains(view, `spec "nonsense": no local port — want local:host:port`) {
		t.Errorf("View() missing the rejection:\n%s", view)
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
	// esc leaves without running anything.
	m = press(t, m, "esc")
	if ui.Prompting(m) {
		t.Errorf("Prompting() = true after esc, want the prompt closed")
	}
}

func TestModel_Update_AddForwardNeedsEngine(t *testing.T) {
	t.Parallel()
	m := sized(t, tunnelHosts())
	m = press(t, m, "+")
	if ui.Prompting(m) {
		t.Errorf("Prompting() = true after + with no engine, want no prompt")
	}
}

func TestModel_Update_CancelHighlighted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ours       bool // web0 served by our pid 41, port answering
		wantKilled []int
		wantNotice string
	}{
		{name: "x kills the one row's process", ours: true, wantKilled: []int{41}, wantNotice: "killed " + specWeb0 + " — its connections dropped with it"},
		{name: "a row without a process of ours has nothing to kill", wantNotice: specWeb0 + " has no process of ours to kill"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// web has two specs and the cache records its second: x on
			// the first row must end that one only — never the set
			// (KillSet would also end the recorded spec's process).
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New()
			sp := fakespawn.New().Dial(d).Live(42)
			stateDir := t.TempDir()
			state := `{"forwards":[{"set":"web","spec":"` + specWeb1 + `","pid":42}]}`
			if tt.ours {
				d.Up("127.0.0.1:3000", time.Millisecond)
				sp.Live(41)
				state = `{"forwards":[{"set":"web","spec":"` + specWeb0 + `","pid":41},{"set":"web","spec":"` + specWeb1 + `","pid":42}]}`
			}
			writeState(t, stateDir, state)
			m := tunnelModelSpawn(t, r, sp, d, stateDir)
			m = feed(t, m, m.Init())
			m = press(t, m, "l") // web0
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("x"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(x) cmd = nil, want kill cmd")
			}
			m = update(t, m, cmd())
			if got := sp.Killed(); !reflect.DeepEqual(got, tt.wantKilled) {
				t.Errorf("x killed = %v, want %v", got, tt.wantKilled)
			}
			if got := r.CallKeys()[before:]; len(got) != 0 {
				t.Errorf("x ran %v through the runner, want nothing", got)
			}
			if view := m.View(); !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
		})
	}
}

func TestModel_Update_CancelNeedsHighlight(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("x"))
	m = next.(ui.Model)
	if cmd != nil || len(r.CallKeys()) != before {
		t.Errorf("Update(x) without a highlight ran something")
	}
	if view := m.View(); !strings.Contains(view, "l cycles") {
		t.Errorf("View() missing the tab hint:\n%s", view)
	}
}

func TestModel_View_SharedPortDemoted(t *testing.T) {
	t.Parallel()
	// A stale recorded spec on web's local port: the port answers for
	// web's own process, so the stale twin reads listening — the port
	// answers but no process of ours serves THAT spec — and says whose
	// port it shares.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	stateDir := t.TempDir()
	writeState(t, stateDir, `{"forwards":[{"set":"web","spec":"`+specWeb0+`","pid":41},{"set":"","spec":"3000:localhost:9999"},{"set":"","spec":"4011:localhost:1"}]}`)
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	m := tunnelModelSpawn(t, r, fakespawn.New().Dial(d).Live(41), d, stateDir)
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 24}) // the note beside a status-bearing list row
	m = feed(t, m, m.Init())
	var webRow, staleRow string
	for _, line := range strings.Split(m.View(), "\n") {
		switch {
		case strings.Contains(line, specWeb0):
			webRow = line
		case strings.Contains(line, "3000:localhost:9999"):
			staleRow = line
		}
	}
	if !strings.Contains(webRow, "● ") || !strings.Contains(webRow, " up") {
		t.Errorf("web row = %q, want up", webRow)
	}
	if !strings.Contains(staleRow, "◐ ") || !strings.Contains(staleRow, "listening") || !strings.Contains(staleRow, "unverified: :3000 claimed by web") {
		t.Errorf("stale row = %q, want listening with the shared-port note", staleRow)
	}
	// api's port has no listener: its recorded twin is plainly down and
	// carries no note — nobody is claiming anything there.
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "4011:localhost:1") && strings.Contains(line, "claimed") {
			t.Errorf("dead row carries a shared-port note: %q", line)
		}
	}
	// A listening row is not up (no process of ours), so space previews
	// its spawn, not a kill.
	m = press(t, m, "l", "G", "k")
	if view := m.View(); !strings.Contains(view, "$ "+spawnArgv("ubuntu", "3000:localhost:9999")) {
		t.Errorf("View() preview on the listening row should be a spawn:\n%s", view)
	}
}

func TestModel_Update_ConflictBindsToItsHost(t *testing.T) {
	t.Parallel()
	// The check runs off the UI thread; the cursor moves on meanwhile.
	// The offer must come back for the host + was pressed on — and the
	// add must go there, never to whatever is under the cursor now.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "check", "spare")
	d := fakedial.New().Up("127.0.0.1:8080", time.Millisecond)
	sp := fakespawn.New().Dial(d)
	m := polledSpawn(t, r, sp, d, t.TempDir())
	m, check := typeSpec(t, m, "8080:localhost:80")
	m = press(t, m, "G") // spare
	next, _ := m.Update(check())
	m = next.(ui.Model)
	view := m.View()
	if !ui.Prompting(m) || !strings.Contains(view, "forward ubuntu: !8081:localhost:80") {
		t.Fatalf("View() should reopen the prompt bound to ubuntu:\n%s", view)
	}
	before := len(r.CallKeys())
	next, add := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if add == nil {
		t.Fatalf("enter cmd = nil, want the add")
	}
	m = update(t, m, add())
	if got, want := sp.Spawned(), []string{spawnArgv("ubuntu", "8081:localhost:80")}; !reflect.DeepEqual(got, want) {
		t.Errorf("add spawned = %v, want %v (never spare)", got, want)
	}
	if got := r.CallKeys()[before:]; len(got) != 0 {
		t.Errorf("add ran %v through the runner, want nothing", got)
	}
	if view := m.View(); !strings.Contains(view, "session-only tunnel up: 8081:localhost:80 on ubuntu") {
		t.Errorf("View() notice should name the host:\n%s", view)
	}
}

func TestModel_Update_ConflictYieldsToKeyboardOwner(t *testing.T) {
	t.Parallel()
	// A conflict landing while another prompt or the filter owns the
	// keyboard must not seize it: the offer becomes a notice, nothing
	// runs, nothing typed is lost.
	tests := []struct {
		name  string
		setup func(t *testing.T, m ui.Model) ui.Model
		check func(t *testing.T, m ui.Model)
	}{
		{
			name: "save prompt keeps its alias",
			setup: func(t *testing.T, m ui.Model) ui.Model {
				m = ui.WithSaveHost(m, func(ui.AdhocEntry) error { return nil })
				m = connectPending(t, m, "192.0.2.99")
				return update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
			},
			check: func(t *testing.T, m ui.Model) {
				if !ui.PromptingSave(m) || !strings.Contains(m.View(), "save as: edge") {
					t.Errorf("save prompt lost:\n%s", m.View())
				}
			},
		},
		{
			name:  "settings panel keeps the keyboard",
			setup: func(t *testing.T, m ui.Model) ui.Model { return press(t, m, "o") },
			check: func(t *testing.T, m ui.Model) {
				if !ui.SettingsOpen(m) || ui.Prompting(m) {
					t.Errorf("settings lost: open %v, prompting %v", ui.SettingsOpen(m), ui.Prompting(m))
				}
			},
		},
		{
			name: "paths focus keeps the keyboard",
			setup: func(t *testing.T, m ui.Model) ui.Model {
				m = ui.WithSwitcher(m, func(string, string) (pathswitch.Result, error) { return pathswitch.Result{}, nil },
					func(string) ([]probe.Path, string, bool) {
						return []probe.Path{{Name: "lan", Host: "192.0.2.10"}, {Name: "ts", Host: "198.51.100.5"}}, "192.0.2.10", true
					}, nil)
				// ubuntu renders both of the block's paths, so p has rows.
				hosts := tunnelHosts()
				hosts[0].Paths = []probe.Path{{Name: "lan", Host: "192.0.2.10"}, {Name: "ts", Host: "198.51.100.5"}}
				m = reloadAndProbe(t, ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf"), hosts)
				return press(t, m, "p")
			},
			check: func(t *testing.T, m ui.Model) {
				if !ui.PathsFocused(m) || ui.Prompting(m) {
					t.Errorf("paths focus lost: focused %v, prompting %v", ui.PathsFocused(m), ui.Prompting(m))
				}
			},
		},
		{
			name: "popup keeps the keyboard",
			setup: func(t *testing.T, m ui.Model) ui.Model {
				return update(t, m, ui.TunnelStatus("ubuntu", tunnel.HostStatus{Host: "ubuntu", CorruptState: true}, nil))
			},
			check: func(t *testing.T, m ui.Model) {
				if !ui.PopupOpen(m) || ui.Prompting(m) {
					t.Errorf("popup lost: open %v, prompting %v", ui.PopupOpen(m), ui.Prompting(m))
				}
			},
		},
		{
			name:  "filter stays focused",
			setup: func(t *testing.T, m ui.Model) ui.Model { return press(t, m, "/", "u") },
			check: func(t *testing.T, m ui.Model) {
				if ui.Prompting(m) || ui.FilterState(m).String() != "filtering" || ui.FilterText(m) != "u" {
					t.Errorf("filter lost: prompting %v, state %v, text %q", ui.Prompting(m), ui.FilterState(m), ui.FilterText(m))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
			m := polled(t, r, fakedial.New().Up("127.0.0.1:8080", time.Millisecond))
			m, check := typeSpec(t, m, "8080:localhost:80")
			m = tt.setup(t, m)
			before := len(r.CallKeys())
			next, cmd := m.Update(check())
			m = next.(ui.Model)
			if cmd != nil {
				t.Errorf("yield returned a cmd: nothing may run while another input owns the keyboard")
			}
			tt.check(t, m)
			view := m.View()
			if !strings.Contains(view, "8080 is in use on ubuntu") || !strings.Contains(view, "8081:localhost:80") {
				t.Errorf("View() missing the offer as a notice:\n%s", view)
			}
			if got := r.CallKeys()[before:]; len(got) != 0 {
				t.Errorf("calls = %v, want none", got)
			}
		})
	}
}

func TestModel_Update_ForwardHostEditedAway(t *testing.T) {
	t.Parallel()
	// An `e` reload can drop the bound host while the prompt is open or
	// the check is out: both paths close with a notice and add nothing.
	tests := []struct {
		name string
		run  func(t *testing.T, m ui.Model) ui.Model
	}{
		{
			name: "before enter",
			run: func(t *testing.T, m ui.Model) ui.Model {
				m = press(t, m, "+")
				m = editReload(t, m, tunnelHosts()[1:]) // ubuntu gone
				m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("8080:localhost:80")})
				next, cmd := m.Update(keyMsg("enter"))
				if cmd != nil {
					t.Errorf("enter on a gone host returned a cmd, want none")
				}
				return next.(ui.Model)
			},
		},
		{
			name: "while the check is out",
			run: func(t *testing.T, m ui.Model) ui.Model {
				m, check := typeSpec(t, m, "8080:localhost:80")
				m = editReload(t, m, tunnelHosts()[1:])
				next, cmd := m.Update(check())
				if cmd != nil {
					t.Errorf("conflict for a gone host returned a cmd, want none")
				}
				return next.(ui.Model)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().
				StubExit(0, "ssh", "-O", "check", "ubuntu").
				StubExit(0, "ssh", "-O", "check", "bare")
			m := polled(t, r, fakedial.New().Up("127.0.0.1:8080", time.Millisecond))
			m = tt.run(t, m)
			if ui.Prompting(m) {
				t.Errorf("Prompting() = true, want the prompt closed")
			}
			if view := m.View(); !strings.Contains(view, "host ubuntu is gone") {
				t.Errorf("View() missing the gone notice:\n%s", view)
			}
		})
	}
}

func TestModel_Update_ForwardKeysInertWhileFiltering(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	m = press(t, m, "l", "/", "+", "x")
	if got := ui.FilterText(m); got != "+x" {
		t.Errorf("FilterText() = %q, want %q", got, "+x")
	}
	if ui.Prompting(m) || len(r.CallKeys()) != before {
		t.Errorf("+ or x acted while filtering")
	}
}

func TestModel_View_HelpListsForwardKeys(t *testing.T) {
	t.Parallel()
	m := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 100, Height: 60})
	m = press(t, m, "?")
	view := m.View()
	for _, want := range []string{"add forward", "R… = reverse", "kill forward"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() keys panel missing %q:\n%s", want, view)
		}
	}
}

// TestModel_Update_AddForwardPersistsByDefault pins SASASH.4.21: + on a
// managed host declares the set (fwdset.Add — persisted in sasash.conf) AND
// connects it live (Engine.Up), in one flow with a set-name step defaulting
// to the local port.
func TestModel_Update_AddForwardPersistsByDefault(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	// A MANAGED host (HasMeta): + persists into its sasash.conf block.
	hosts := []hostmodel.Host{{Alias: "ubuntu", HasMeta: true, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}}}
	eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = feed(t, m, m.Init())
	var saved [][]string
	m = ui.WithSaveForwardSet(m, func(alias, name string, specs []string) error {
		saved = append(saved, append([]string{alias, name}, specs...))
		return nil
	})
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9090:localhost:90")})
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd()) // held check -> proceed -> the set-name prompt
	if !ui.Prompting(m) || !strings.Contains(m.View(), "name the set on ubuntu") {
		t.Fatalf("the persist path should open the set-name prompt:\n%s", m.View())
	}
	if got := ui.PromptValue(m); got != "9090" {
		t.Fatalf("default set name = %q, want the local port 9090", got)
	}
	next, cmd = m.Update(keyMsg("enter")) // accept the default -> declare + connect
	m = update(t, next.(ui.Model), cmd())
	if want := [][]string{{"ubuntu", "9090", "9090:localhost:90"}}; !reflect.DeepEqual(saved, want) {
		t.Errorf("saveForwardSet = %v, want the declaration %v (persisted)", saved, want)
	}
	if got := sp.Spawned(); !reflect.DeepEqual(got, []string{spawnArgv("ubuntu", "9090:localhost:90")}) {
		t.Errorf("connect spawned = %v, want the process up", got)
	}
	if view := m.View(); !strings.Contains(view, "persisted") {
		t.Errorf("the notice should say it persisted:\n%s", view)
	}
}

// TestModel_Update_AddForwardEphemeralWritesNoConfig pins the ! opt-in:
// session-only, one process, and NO config write — never a declaration.
func TestModel_Update_AddForwardEphemeralWritesNoConfig(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	m := polledSpawn(t, r, sp, d, t.TempDir())
	saved := false
	m = ui.WithSaveForwardSet(m, func(string, string, []string) error { saved = true; return nil })
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!7070:localhost:70")})
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd()) // held check -> direct session-only spawn (no name step)
	if saved {
		t.Error("an ephemeral (!) add must NOT write config")
	}
	if ui.Prompting(m) {
		t.Errorf("ephemeral add should not open a set-name prompt:\n%s", m.View())
	}
	if got := sp.Spawned(); !reflect.DeepEqual(got, []string{spawnArgv("ubuntu", "7070:localhost:70")}) {
		t.Errorf("ephemeral spawned = %v, want the one process", got)
	}
	if view := m.View(); !strings.Contains(view, "session-only") || !strings.Contains(view, "unsaved") {
		t.Errorf("the notice should mark it session-only / unsaved:\n%s", view)
	}
}

// TestModel_Update_AddForwardUnmanagedIsSessionOnly pins that + on an
// UNMANAGED host (no sasash.conf block to declare into) is session-only with
// a save hint — never a two-prompt failure (SASASH.4.21 QA round 1).
func TestModel_Update_AddForwardUnmanagedIsSessionOnly(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	m := polledSpawn(t, r, sp, d, t.TempDir()) // tunnelHosts' ubuntu is unmanaged
	m = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	saved := false
	m = ui.WithSaveForwardSet(m, func(string, string, []string) error { saved = true; return nil })
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("6060:localhost:60")}) // no !
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if ui.Prompting(m) {
		t.Errorf("+ on an unmanaged host should not open the set-name prompt:\n%s", m.View())
	}
	if saved {
		t.Error("+ on an unmanaged host must not attempt a declaration")
	}
	if got := sp.Spawned(); !reflect.DeepEqual(got, []string{spawnArgv("ubuntu", "6060:localhost:60")}) {
		t.Errorf("unmanaged + should spawn one session-only process, got %v", got)
	}
	if view := m.View(); !strings.Contains(view, "not managed") || !strings.Contains(view, "save it") {
		t.Errorf("the notice should say it is session-only and how to persist:\n%s", view)
	}
}

// managedForwardModel builds a model on a MANAGED ubuntu (so + persists) with
// a wired spawner, and installs a saveForwardSet capture. Used by the
// SASASH.4.27 multi-spec tests.
func managedForwardModel(t *testing.T, d *fakedial.Fake, saved *[][]string) (ui.Model, *fakespawn.Fake) {
	t.Helper()
	r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	sp := fakespawn.New().Dial(d)
	hosts := []hostmodel.Host{{Alias: "ubuntu", HasMeta: true, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}}}
	eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	m = feed(t, m, m.Init())
	m = ui.WithSaveForwardSet(m, func(alias, name string, specs []string) error {
		*saved = append(*saved, append([]string{alias, name}, specs...))
		return nil
	})
	return m, sp
}

// TestModel_Update_AddForwardMultiSpecPersists pins SASASH.4.27: whitespace-
// separated specs become ONE set with N forwards, each expanded via the 4.9
// shorthand, declared and connected together. The default set name is the
// first forward's local port.
func TestModel_Update_AddForwardMultiSpecPersists(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string // expanded specs, in order
	}{
		{name: "three shorthand ports", input: "4011 4012 4013", want: []string{"4011:localhost:4011", "4012:localhost:4012", "4013:localhost:4013"}},
		{name: "mixed shorthand and full", input: "3000 2000:3000 5000:localhost:50", want: []string{"3000:localhost:3000", "2000:localhost:3000", "5000:localhost:50"}},
		{name: "tabs separate too", input: "7001\t7002", want: []string{"7001:localhost:7001", "7002:localhost:7002"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var saved [][]string
			m, sp := managedForwardModel(t, fakedial.New(), &saved)
			m = press(t, m, "+")
			m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.input)})
			next, cmd := m.Update(keyMsg("enter"))
			m = update(t, next.(ui.Model), cmd()) // every port free -> set-name prompt
			if !ui.Prompting(m) || !strings.Contains(m.View(), "name the set on ubuntu") {
				t.Fatalf("a multi-spec add should open the set-name prompt:\n%s", m.View())
			}
			firstPort := strings.SplitN(tt.want[0], ":", 2)[0]
			if got := ui.PromptValue(m); got != firstPort {
				t.Fatalf("default set name = %q, want the first local port %q", got, firstPort)
			}
			next, cmd = m.Update(keyMsg("enter")) // accept -> declare + connect the whole set
			m = update(t, next.(ui.Model), cmd())
			wantSaved := [][]string{append([]string{"ubuntu", firstPort}, tt.want...)}
			if !reflect.DeepEqual(saved, wantSaved) {
				t.Errorf("saveForwardSet = %v, want one set with every spec %v", saved, wantSaved)
			}
			var wantSpawn []string
			for _, s := range tt.want {
				wantSpawn = append(wantSpawn, spawnArgv("ubuntu", s))
			}
			if got := sp.Spawned(); !reflect.DeepEqual(got, wantSpawn) {
				t.Errorf("spawned = %v, want one process per spec %v", got, wantSpawn)
			}
		})
	}
}

// TestModel_Update_AddForwardMultiSpecRejectsWholeSet pins all-or-nothing: a
// single malformed token names itself and adds NONE — the prompt stays open.
func TestModel_Update_AddForwardMultiSpecRejectsWholeSet(t *testing.T) {
	t.Parallel()
	var saved [][]string
	m, sp := managedForwardModel(t, fakedial.New(), &saved)
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4011 nonsense 4012")})
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil {
		t.Errorf("a malformed token must not run any check cmd (nothing async should start)")
	}
	if !ui.Prompting(m) {
		t.Errorf("the prompt should stay open to fix the bad token:\n%s", m.View())
	}
	if len(saved) != 0 || len(sp.Spawned()) != 0 {
		t.Errorf("all-or-nothing: nothing saved (%v) or spawned (%v)", saved, sp.Spawned())
	}
	if view := m.View(); !strings.Contains(view, `spec "nonsense"`) {
		t.Errorf("the rejection should name the bad token:\n%s", view)
	}
}

// TestModel_Update_AddForwardMultiSpecConflictRefusesSet pins the per-spec
// conflict check: one held port refuses the WHOLE set (never a partial add),
// names it with its next-free port, and reopens the prompt with the set.
func TestModel_Update_AddForwardMultiSpecConflictRefusesSet(t *testing.T) {
	t.Parallel()
	var saved [][]string
	d := fakedial.New().Up("127.0.0.1:4012", time.Millisecond) // 4012 is held
	m, sp := managedForwardModel(t, d, &saved)
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4011 4012 4013")})
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd()) // held check -> set conflict, nothing runs
	if len(saved) != 0 || len(sp.Spawned()) != 0 {
		t.Errorf("all-or-nothing on conflict: saved %v, spawned %v, want none", saved, sp.Spawned())
	}
	if !ui.Prompting(m) {
		t.Fatalf("the prompt should reopen with the set to edit:\n%s", m.View())
	}
	view := m.View()
	if !strings.Contains(view, "4012") || !strings.Contains(view, "none added") {
		t.Errorf("the notice should name the held port and refuse the set:\n%s", view)
	}
	if !strings.Contains(view, "4011:localhost:4011") {
		t.Errorf("the prompt should reoffer the expanded set:\n%s", view)
	}
}
