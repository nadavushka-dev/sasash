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
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
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
	eng := tunnel.Engine{Runner: r, Dialer: d, StateDir: stateDir, Timeout: 50 * time.Millisecond}
	m := ui.New(tunnelHosts(), probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng})
	return update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
}

// tunnelEngine is a real engine over the fakes and a fresh state dir.
func tunnelEngine(t *testing.T, r *fakerun.Fake, d *fakedial.Fake) tunnel.Engine {
	t.Helper()
	return tunnel.Engine{Runner: r, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
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

func TestTunnelArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "start", got: ui.StartArgv("ubuntu"), want: []string{"ssh", "-fN", "ubuntu"}},
		{name: "forward", got: ui.ForwardArgv("ubuntu", specWeb0), want: []string{"ssh", "-O", "forward", "-L", specWeb0, "ubuntu"}},
		{name: "cancel", got: ui.CancelArgv("ubuntu", specWeb0), want: []string{"ssh", "-O", "cancel", "-L", specWeb0, "ubuntu"}},
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

func TestTunnelArgv_MatchesEngine(t *testing.T) {
	t.Parallel()
	// The preview must show what the engine really runs: each builder is
	// pinned to the argv the engine hands the runner for the same action.
	tests := []struct {
		name string
		argv []string
		act  func(context.Context, tunnel.Engine) error
	}{
		{name: "start", argv: ui.StartArgv("ubuntu"), act: func(ctx context.Context, e tunnel.Engine) error { return e.Start(ctx, "ubuntu") }},
		{name: "forward", argv: ui.ForwardArgv("ubuntu", specWeb0), act: func(ctx context.Context, e tunnel.Engine) error { return e.Forward(ctx, "ubuntu", "web", specWeb0) }},
		{name: "cancel", argv: ui.CancelArgv("ubuntu", specWeb0), act: func(ctx context.Context, e tunnel.Engine) error { return e.Cancel(ctx, "ubuntu", specWeb0) }},
		{name: "exit", argv: ui.ExitArgv("ubuntu"), act: func(ctx context.Context, e tunnel.Engine) error { return e.Exit(ctx, "ubuntu") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, tt.argv...)
			eng := tunnel.Engine{Runner: r, Dialer: fakedial.New(), StateDir: t.TempDir()}
			if err := tt.act(context.Background(), eng); err != nil {
				t.Fatalf("engine %s = %v", tt.name, err)
			}
			if got, want := r.CallKeys(), []string{fakerun.Key(tt.argv...)}; !reflect.DeepEqual(got, want) {
				t.Errorf("engine calls = %v, want %v", got, want)
			}
		})
	}
}

func TestModel_View_TunnelStates(t *testing.T) {
	t.Parallel()
	// Every state renders verbatim as glyph AND word — never collapsed,
	// and readable under NO_COLOR.
	tests := []struct {
		name      string
		checkExit int
		portUp    bool
		wantGlyph string
		wantWord  string
	}{
		{name: "master alive and port answers is up", checkExit: 0, portUp: true, wantGlyph: "●", wantWord: "up"},
		{name: "port answers but master dead is listening", checkExit: 255, portUp: true, wantGlyph: "◐", wantWord: "listening"},
		{name: "port refused is down", checkExit: 0, portUp: false, wantGlyph: "○", wantWord: "down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(tt.checkExit, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New()
			if tt.portUp {
				d.Up("127.0.0.1:3000", time.Millisecond)
			}
			m := tunnelModel(t, r, d, t.TempDir())
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
			wantMaster := "○ master down"
			if tt.checkExit == 0 {
				wantMaster = "● master up"
			}
			if !strings.Contains(view, wantMaster) {
				t.Errorf("View() missing %q:\n%s", wantMaster, view)
			}
		})
	}
}

// aliveWeb0 is the common fixture: master alive, only web's first port up.
func aliveWeb0(t *testing.T) (ui.Model, *fakerun.Fake) {
	t.Helper()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	return polled(t, r, d), r
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
		// space goes through Up, one -L per spec.
		{key: "l", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "j", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "j", wantPreview: "ssh -O forward -L " + specAPI + " ubuntu"},
		{key: "j", wantPreview: "ssh -O forward -L " + specAPI + " ubuntu"}, // clamps at the last row
		{key: "k", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "G", wantPreview: "ssh -O forward -L " + specAPI + " ubuntu"},
		{key: "g", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "tab", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "shift+tab", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "h", wantPreview: "$ ssh ubuntu"},
		{key: "tab", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"}, // tab from the list = l
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
	want := "ssh -O forward -L " + specAPI + " ubuntu"
	r.StubExit(0, ui.ClipArgv(want)...)
	m = press(t, m, "l", "G")
	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatalf("Update(c) cmd = nil, want clipboard cmd")
	}
	cmd()
	if got, want := r.CallKeys()[len(r.CallKeys())-1], fakerun.Key(ui.ClipArgv(want)...); got != want {
		t.Errorf("last call = %q, want %q", got, want)
	}
}

func TestModel_Update_SpaceBringsSetUp(t *testing.T) {
	t.Parallel()
	// Master dead, everything down: space on web starts the master and
	// forwards each spec, in that order, then re-polls.
	r := fakerun.New().
		StubExit(255, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-fN", "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", specWeb0, "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", specWeb1, "ubuntu")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "l")
	if view := m.View(); !strings.Contains(view, "$ ssh -fN ubuntu") || !strings.Contains(view, "+2 more") {
		t.Fatalf("View() with a dead master should preview the start:\n%s", view)
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg(" "))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(space) cmd = nil, want action cmd")
	}
	next, refresh := m.Update(cmd())
	m = next.(ui.Model)
	want := []string{
		"ssh -O check ubuntu",
		"ssh -fN ubuntu",
		"ssh -O forward -L " + specWeb0 + " ubuntu",
		"ssh -O forward -L " + specWeb1 + " ubuntu",
	}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("space calls = %v, want %v", got, want)
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
	// Every forward of web up: space cancels each spec.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(0, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu").
		StubExit(0, "ssh", "-O", "cancel", "-L", specWeb1, "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond).Up("127.0.0.1:3001", time.Millisecond)
	m := polled(t, r, d)
	m = press(t, m, "l")
	if view := m.View(); !strings.Contains(view, "$ ssh -O cancel -L "+specWeb0+" ubuntu") || !strings.Contains(view, "+1 more") {
		t.Fatalf("View() with web fully up should preview the cancel:\n%s", view)
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg(" "))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(space) cmd = nil, want action cmd")
	}
	m = update(t, m, cmd())
	want := []string{
		"ssh -O cancel -L " + specWeb0 + " ubuntu",
		"ssh -O cancel -L " + specWeb1 + " ubuntu",
	}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("space calls = %v, want %v", got, want)
	}
	if view := m.View(); !strings.Contains(view, "web down") {
		t.Errorf("View() missing the success notice:\n%s", view)
	}
}

// noStateDir returns a path where the state dir cannot be created: a
// file. Every record then fails with ErrStateWrite while ssh succeeded.
func noStateDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestModel_Update_SpaceOutcomes(t *testing.T) {
	t.Parallel()
	refused := fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("Control socket connect: No such file")}}
	tests := []struct {
		name string
		// webUp stubs every web port answering so space goes down.
		webUp    bool
		stubs    func(r *fakerun.Fake)
		stateDir func(t *testing.T) string
		want     []string // all present in the notice
		wantNot  []string
	}{
		{
			name: "ssh failure is reported with its message",
			stubs: func(r *fakerun.Fake) {
				r.Stub(fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("bind: Address already in use")}}, "ssh", "-O", "forward", "-L", specWeb0, "ubuntu")
			},
			stateDir: func(t *testing.T) string { return t.TempDir() },
			want:     []string{"web: ", "Address already in use"},
			wantNot:  []string{"web up"},
		},
		{
			name: "state write failures warn on one line and the tunnel is real",
			stubs: func(r *fakerun.Fake) {
				r.StubExit(0, "ssh", "-O", "forward", "-L", specWeb0, "ubuntu").
					StubExit(0, "ssh", "-O", "forward", "-L", specWeb1, "ubuntu")
			},
			stateDir: noStateDir,
			// Two specs => Up joins two state errors: both must stay on
			// the notice line, not push the frame off the terminal.
			want:    []string{"warning: ", "not recorded"},
			wantNot: []string{"web: "},
		},
		{
			name:  "a refused cancel joined with a state failure is a failure",
			webUp: true,
			stubs: func(r *fakerun.Fake) {
				r.Stub(refused, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu").
					StubExit(0, "ssh", "-O", "cancel", "-L", specWeb1, "ubuntu")
			},
			stateDir: noStateDir,
			// DownSet joins the cancel failure and the cache failure:
			// ssh's message must win the label, never "warning".
			want:    []string{"web: ", "No such file"},
			wantNot: []string{"warning: "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
			tt.stubs(r)
			d := fakedial.New()
			if tt.webUp {
				d.Up("127.0.0.1:3000", time.Millisecond).Up("127.0.0.1:3001", time.Millisecond)
			}
			m := tunnelModel(t, r, d, tt.stateDir(t))
			m = feed(t, m, m.Init())
			// An unreadable state dir opens the cache popup once at the
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
	if view := m.View(); !strings.Contains(view, "l focuses") {
		t.Errorf("View() missing the tab hint:\n%s", view)
	}
}

func TestModel_Update_RestartAndExit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		key        string
		exitReply  fakerun.Reply
		wantCalls  []string
		wantNotice string
	}{
		{
			// SPEC §5 "restart master": exit, then a fresh master and
			// nothing else — no set the user did not ask for gets bound.
			name:       "r exits then starts a fresh master only",
			key:        "r",
			exitReply:  fakerun.Reply{Result: run.Result{Code: 0}},
			wantCalls:  []string{"ssh -O exit ubuntu", "ssh -O check ubuntu", "ssh -fN ubuntu"},
			wantNotice: "master restarted",
		},
		{
			name:       "r on a dead master is just a start",
			key:        "r",
			exitReply:  fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("Control socket connect: No such file")}},
			wantCalls:  []string{"ssh -O exit ubuntu", "ssh -O check ubuntu", "ssh -fN ubuntu"},
			wantNotice: "master restarted",
		},
		{
			name:       "X exits the master",
			key:        "X",
			exitReply:  fakerun.Reply{Result: run.Result{Code: 0}},
			wantCalls:  []string{"ssh -O exit ubuntu"},
			wantNotice: "master exited",
		},
		{
			name:       "X failure shows ssh's message",
			key:        "X",
			exitReply:  fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("Control socket connect: No such file")}},
			wantCalls:  []string{"ssh -O exit ubuntu"},
			wantNotice: "exit: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().
				Stub(tt.exitReply, "ssh", "-O", "exit", "ubuntu").
				StubExit(0, "ssh", "-fN", "ubuntu")
			// The poll sees a live master; after the exit the restart's
			// own check finds none and starts one.
			r.StubExit(0, "ssh", "-O", "check", "ubuntu")
			m := polled(t, r, fakedial.New())
			r.StubExit(255, "ssh", "-O", "check", "ubuntu")
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg(tt.key))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(%s) cmd = nil, want action cmd", tt.key)
			}
			m = update(t, m, cmd())
			if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, tt.wantCalls) {
				t.Errorf("%s calls = %v, want %v", tt.key, got, tt.wantCalls)
			}
			view := m.View()
			if !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
			if tt.wantNotice == "exit: " && !strings.Contains(view, "No such file") {
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
	// Until it lands the master row is pending, never a guessed state.
	if view := m.View(); !strings.Contains(view, "◌ master …") {
		t.Errorf("View() before bare's status missing the pending master row:\n%s", view)
	}
	m = feed(t, m, cmd)
	view := m.View()
	if !strings.Contains(view, "○ master down") {
		t.Errorf("View() after bare's status missing its master row:\n%s", view)
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
	if view := m.View(); !strings.Contains(view, "◌ master …") || strings.Contains(view, "● web") {
		t.Errorf("View() shows ubuntu's stale status as live:\n%s", view)
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
	if view := m.View(); !strings.Contains(view, "○ master down") {
		t.Errorf("View() after bare's poll landed missing its master row:\n%s", view)
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
	if !strings.Contains(row, "ad-hoc") || !strings.HasSuffix(strings.TrimSpace(row), "down") {
		t.Errorf("recorded ad-hoc row = %q, want an ad-hoc row marked down", row)
	}
	// It is the last row: l then G reaches it, and with the master
	// alive space would re-forward it.
	m = press(t, m, "l", "G")
	if view := m.View(); !strings.Contains(view, "$ ssh -O forward -L 5000:localhost:5000 ubuntu") {
		t.Errorf("View() after shift+tab should preview the ad-hoc row's forward:\n%s", view)
	}
}

func TestModel_View_HelpListsTunnelKeys(t *testing.T) {
	t.Parallel()
	// Presence only at a width where the tunnels column fits.
	m := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(t, m, "?")
	view := m.View()
	for _, want := range []string{"toggle set", "restart master", "exit master", "tunnels pane", "host list"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() full help missing %q:\n%s", want, view)
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
