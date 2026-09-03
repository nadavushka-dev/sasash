package ui_test

import (
	"context"
	"os"
	"path/filepath"
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
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
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
			eng := tunnel.Engine{Runner: r, Dialer: fakedial.New(), StateDir: t.TempDir(), Timeout: time.Millisecond}
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
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(spec)})
	next, cmd := m.Update(keyMsg("enter"))
	return next.(ui.Model), cmd
}

func TestModel_Update_AddForwardFree(t *testing.T) {
	t.Parallel()
	// Port free: the spec goes in as an ad-hoc forward through Up.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", "8080:localhost:80", "ubuntu")
	m := polled(t, r, fakedial.New())
	m, cmd := typeSpec(t, m, "8080:localhost:80")
	if cmd == nil || ui.Prompting(m) {
		t.Fatalf("enter: cmd %v, prompting %v; want the add cmd and the prompt closed", cmd != nil, ui.Prompting(m))
	}
	before := len(r.CallKeys())
	m = update(t, m, cmd())
	want := []string{"ssh -O check ubuntu", "ssh -O forward -L 8080:localhost:80 ubuntu"}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("add calls = %v, want %v", got, want)
	}
	if view := m.View(); !strings.Contains(view, "added 8080:localhost:80") {
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
			r := fakerun.New().
				StubExit(0, "ssh", "-O", "check", "ubuntu").
				StubExit(0, "ssh", "-O", "forward", "-L", tt.wantSpec, "ubuntu")
			d := fakedial.New()
			for p := 8080; p <= tt.heldUpTo; p++ {
				d.Up("127.0.0.1:"+strconv.Itoa(p), time.Millisecond)
			}
			m := polled(t, r, d)
			m, cmd := typeSpec(t, m, "8080:localhost:80")
			before := len(r.CallKeys())
			next, focus := m.Update(cmd()) // the conflict lands
			m = next.(ui.Model)
			if focus != nil {
				focus() // cursor focus, no msgs of interest
			}
			// Nothing ran: the remap is on screen, not applied.
			if got := r.CallKeys()[before:]; len(got) != 0 {
				t.Fatalf("calls after a conflict = %v, want none", got)
			}
			if !ui.Prompting(m) {
				t.Fatalf("Prompting() = false after a conflict, want the prompt reopened")
			}
			view := m.View()
			if !strings.Contains(view, "forward ubuntu: "+tt.wantSpec) {
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
			want := []string{"ssh -O check ubuntu", "ssh -O forward -L " + tt.wantSpec + " ubuntu"}
			if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
				t.Errorf("add calls = %v, want %v", got, want)
			}
			if view := m.View(); !strings.Contains(view, "added "+tt.wantSpec) {
				t.Errorf("View() missing the added notice:\n%s", view)
			}
		})
	}
}

func TestModel_Update_AddForwardRaceReported(t *testing.T) {
	t.Parallel()
	// Free at the check, taken by the add: ssh's bind failure is the
	// notice, never swallowed into "added".
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		Stub(fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("bind [127.0.0.1]:8080: Address already in use")}},
			"ssh", "-O", "forward", "-L", "8080:localhost:80", "ubuntu")
	m := polled(t, r, fakedial.New())
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
	if view := m.View(); !strings.Contains(view, `no local port in "nonsense" — want local:host:port`) {
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
		reply      fakerun.Reply
		wantNotice string
	}{
		{name: "cancel runs for the one row", reply: fakerun.Reply{Result: run.Result{Code: 0}}, wantNotice: "cancelled " + specWeb0},
		{name: "already gone reports ssh's reason", reply: fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("unknown port forwarding")}}, wantNotice: "cancel: unknown port forwarding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// web has two specs and the cache records its second: x on
			// the first row must cancel that one only — never the set
			// (DownSet would also cancel the recorded spec, unstubbed).
			r := fakerun.New().
				StubExit(0, "ssh", "-O", "check", "ubuntu").
				Stub(tt.reply, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu")
			stateDir := t.TempDir()
			state := `{"forwards":[{"set":"web","spec":"` + specWeb1 + `"}]}`
			if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(state), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			m := tunnelModel(t, r, fakedial.New(), stateDir)
			m = feed(t, m, m.Init())
			m = press(t, m, "l") // web0
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("x"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(x) cmd = nil, want cancel cmd")
			}
			m = update(t, m, cmd())
			if got, want := r.CallKeys()[before:], []string{"ssh -O cancel -L " + specWeb0 + " ubuntu"}; !reflect.DeepEqual(got, want) {
				t.Errorf("x calls = %v, want %v", got, want)
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
	if view := m.View(); !strings.Contains(view, "l focuses") {
		t.Errorf("View() missing the tab hint:\n%s", view)
	}
}

func TestModel_View_SharedPortDemoted(t *testing.T) {
	t.Parallel()
	// A stale recorded spec on web's local port: the port answers and the
	// master is alive, so the engine calls both up. Only the first
	// claimant can be the one answering — the later one is capped at
	// listening and says whose port it shares.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	stateDir := t.TempDir()
	state := `{"forwards":[{"set":"","spec":"3000:localhost:9999"},{"set":"","spec":"4011:localhost:1"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := tunnelModel(t, r, fakedial.New().Up("127.0.0.1:3000", time.Millisecond), stateDir)
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
	// The demotion reaches actions too: the ad-hoc set is not "all up",
	// so space previews a re-forward, not a cancel.
	m = press(t, m, "l", "G")
	if view := m.View(); !strings.Contains(view, "$ ssh -O forward -L 3000:localhost:9999 ubuntu") {
		t.Errorf("View() preview on the demoted row should be a forward:\n%s", view)
	}
}

func TestModel_Update_ConflictBindsToItsHost(t *testing.T) {
	t.Parallel()
	// The check runs off the UI thread; the cursor moves on meanwhile.
	// The offer must come back for the host + was pressed on — and the
	// add must go there, never to whatever is under the cursor now.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "check", "spare").
		StubExit(0, "ssh", "-O", "forward", "-L", "8081:localhost:80", "ubuntu")
	m := polled(t, r, fakedial.New().Up("127.0.0.1:8080", time.Millisecond))
	m, check := typeSpec(t, m, "8080:localhost:80")
	m = press(t, m, "G") // spare
	next, _ := m.Update(check())
	m = next.(ui.Model)
	view := m.View()
	if !ui.Prompting(m) || !strings.Contains(view, "forward ubuntu: 8081:localhost:80") {
		t.Fatalf("View() should reopen the prompt bound to ubuntu:\n%s", view)
	}
	before := len(r.CallKeys())
	next, add := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if add == nil {
		t.Fatalf("enter cmd = nil, want the add")
	}
	m = update(t, m, add())
	want := []string{"ssh -O check ubuntu", "ssh -O forward -L 8081:localhost:80 ubuntu"}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("add calls = %v, want %v (never spare)", got, want)
	}
	if view := m.View(); !strings.Contains(view, "added 8081:localhost:80 on ubuntu") {
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
	for _, want := range []string{"add forward", "cancel forward"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() keys panel missing %q:\n%s", want, view)
		}
	}
}
