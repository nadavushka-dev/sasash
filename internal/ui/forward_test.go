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
	if view := m.View(); !strings.Contains(view, "forward:") || !strings.Contains(view, "enter add") {
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
			if !strings.Contains(view, "forward: "+tt.wantSpec) {
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
	if view := m.View(); !strings.Contains(view, "no local port") {
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
		{name: "cancel runs for the one row", reply: fakerun.Reply{Result: run.Result{Code: 0}}, wantNotice: "cancelled " + specAPI},
		{name: "already gone reports ssh's reason", reply: fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("unknown port forwarding")}}, wantNotice: "cancel: unknown port forwarding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().
				StubExit(0, "ssh", "-O", "check", "ubuntu").
				Stub(tt.reply, "ssh", "-O", "cancel", "-L", specAPI, "ubuntu")
			m := polled(t, r, fakedial.New())
			m = press(t, m, "tab", "tab", "tab") // api
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("x"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(x) cmd = nil, want cancel cmd")
			}
			m = update(t, m, cmd())
			if got, want := r.CallKeys()[before:], []string{"ssh -O cancel -L " + specAPI + " ubuntu"}; !reflect.DeepEqual(got, want) {
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
	if view := m.View(); !strings.Contains(view, "tab") {
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
	state := `{"forwards":[{"set":"","spec":"3000:localhost:9999"}]}`
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
	if !strings.Contains(staleRow, "◐ ") || !strings.Contains(staleRow, "listening") || !strings.Contains(staleRow, "shares :3000 with web") {
		t.Errorf("stale row = %q, want listening with the shared-port note", staleRow)
	}
	// The demotion reaches actions too: the ad-hoc set is not "all up",
	// so space previews a re-forward, not a cancel.
	m = press(t, m, "shift+tab")
	if view := m.View(); !strings.Contains(view, "$ ssh -O forward -L 3000:localhost:9999 ubuntu") {
		t.Errorf("View() preview on the demoted row should be a forward:\n%s", view)
	}
}

func TestModel_Update_ForwardKeysInertWhileFiltering(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	m = press(t, m, "tab", "/", "+", "x")
	if got := ui.FilterText(m); got != "+x" {
		t.Errorf("FilterText() = %q, want %q", got, "+x")
	}
	if ui.Prompting(m) || len(r.CallKeys()) != before {
		t.Errorf("+ or x acted while filtering")
	}
}

func TestModel_View_HelpListsForwardKeys(t *testing.T) {
	t.Parallel()
	m := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(t, m, "?")
	view := m.View()
	for _, want := range []string{"add forward", "cancel forward"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() full help missing %q:\n%s", want, view)
		}
	}
}
