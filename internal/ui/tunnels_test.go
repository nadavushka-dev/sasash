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
)

// tunnelHosts is the fixture: ubuntu declares two sets, bare declares
// none. Paths are unstubbed, so they probe down and stay out of the way.
func tunnelHosts() []hostmodel.Host {
	return []hostmodel.Host{
		{Alias: "ubuntu", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}, Forwards: []hostmodel.ForwardSet{
			{Name: "web", Specs: []string{specWeb0, specWeb1}},
			{Name: "api", Specs: []string{specAPI}},
		}},
		{Alias: "bare", Paths: []probe.Path{{Host: "192.0.2.20"}}},
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
			// Status runs in a cmd: the frame renders before it lands.
			cmd := m.Init()
			if view := m.View(); !strings.Contains(view, "tunnels") || !strings.Contains(view, specWeb0) {
				t.Fatalf("View() before status missing the pending tunnels section:\n%s", view)
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

func TestModel_Update_TabCyclesHighlightAndPreview(t *testing.T) {
	t.Parallel()
	m, _ := aliveWeb0(t)
	steps := []struct {
		key         string
		wantPreview string
		wantMore    string
	}{
		{key: "", wantPreview: "$ ssh ubuntu"},
		// web is partially up: space goes through Up, one -L per spec.
		{key: "tab", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "tab", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
		{key: "tab", wantPreview: "ssh -O forward -L " + specAPI + " ubuntu"},
		{key: "tab", wantPreview: "$ ssh ubuntu"},
		{key: "shift+tab", wantPreview: "ssh -O forward -L " + specAPI + " ubuntu"},
		{key: "shift+tab", wantPreview: "ssh -O forward -L " + specWeb0 + " ubuntu", wantMore: "+1 more"},
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
	// The highlighted row carries the mark.
	m = press(t, m, "tab") // web1 -> api
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
	m = press(t, m, "tab", "tab", "tab")
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
	m = press(t, m, "tab")
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
	m = press(t, m, "tab")
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

func TestModel_Update_SpaceOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		forward    fakerun.Reply
		stateDir   func(t *testing.T) string
		wantNotice string
		wantNot    string
	}{
		{
			name:       "ssh failure is reported with its message",
			forward:    fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("bind: Address already in use")}},
			stateDir:   func(t *testing.T) string { return t.TempDir() },
			wantNotice: "api: ",
			wantNot:    "api up",
		},
		{
			name:    "state write failure warns but the tunnel is real",
			forward: fakerun.Reply{Result: run.Result{Code: 0}},
			// A file where the state dir should be: the record cannot be
			// written, ssh already succeeded.
			stateDir: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "notadir")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				return p
			},
			wantNotice: "warning: ",
			wantNot:    "api: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().
				StubExit(0, "ssh", "-O", "check", "ubuntu").
				Stub(tt.forward, "ssh", "-O", "forward", "-L", specAPI, "ubuntu")
			m := tunnelModel(t, r, fakedial.New(), tt.stateDir(t))
			m = feed(t, m, m.Init())
			m = press(t, m, "tab", "tab", "tab") // api
			next, cmd := m.Update(keyMsg(" "))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(space) cmd = nil, want action cmd")
			}
			m = update(t, m, cmd())
			view := m.View()
			if !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
			if strings.Contains(view, tt.wantNot) {
				t.Errorf("View() shows %q, want it absent:\n%s", tt.wantNot, view)
			}
			if tt.wantNotice == "warning: " && !strings.Contains(view, "not recorded") {
				t.Errorf("View() warning does not say what failed:\n%s", view)
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
	if view := m.View(); !strings.Contains(view, "tab") {
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
			name:      "r exits then brings every declared set up",
			key:       "r",
			exitReply: fakerun.Reply{Result: run.Result{Code: 0}},
			wantCalls: []string{
				"ssh -O exit ubuntu",
				"ssh -O check ubuntu",
				"ssh -O forward -L " + specWeb0 + " ubuntu",
				"ssh -O forward -L " + specWeb1 + " ubuntu",
				"ssh -O check ubuntu",
				"ssh -O forward -L " + specAPI + " ubuntu",
			},
			wantNotice: "master restarted",
		},
		{
			name:      "r on a dead master is just an up",
			key:       "r",
			exitReply: fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("Control socket connect: No such file")}},
			wantCalls: []string{
				"ssh -O exit ubuntu",
				"ssh -O check ubuntu",
				"ssh -O forward -L " + specWeb0 + " ubuntu",
				"ssh -O forward -L " + specWeb1 + " ubuntu",
				"ssh -O check ubuntu",
				"ssh -O forward -L " + specAPI + " ubuntu",
			},
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
				StubExit(0, "ssh", "-O", "check", "ubuntu").
				Stub(tt.exitReply, "ssh", "-O", "exit", "ubuntu").
				StubExit(0, "ssh", "-O", "forward", "-L", specWeb0, "ubuntu").
				StubExit(0, "ssh", "-O", "forward", "-L", specWeb1, "ubuntu").
				StubExit(0, "ssh", "-O", "forward", "-L", specAPI, "ubuntu")
			m := polled(t, r, fakedial.New())
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

func TestModel_Update_RestartMasterOnlyHost(t *testing.T) {
	t.Parallel()
	// A host with no declared sets still gets its master ensured.
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		StubExit(255, "ssh", "-O", "exit", "bare").
		StubExit(255, "ssh", "-O", "check", "bare").
		StubExit(0, "ssh", "-fN", "bare")
	m := polled(t, r, fakedial.New())
	m = press(t, m, "j")
	before := len(r.CallKeys())
	_, cmd := m.Update(keyMsg("r"))
	if cmd == nil {
		t.Fatalf("Update(r) cmd = nil, want action cmd")
	}
	cmd()
	want := []string{"ssh -O exit bare", "ssh -O check bare", "ssh -fN bare"}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("r calls = %v, want %v", got, want)
	}
}

func TestModel_Update_TunnelKeysInertWhileFiltering(t *testing.T) {
	t.Parallel()
	m, r := aliveWeb0(t)
	before := len(r.CallKeys())
	m = press(t, m, "/", " ", "X", "r")
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
	m = press(t, m, "tab") // highlight belongs to ubuntu
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
	// The highlight did not follow the cursor to another host.
	if !strings.Contains(view, "$ ssh bare") {
		t.Errorf("View() preview should be enter's ssh for the new host:\n%s", view)
	}
	// Back to a polled host: no re-poll on navigation alone.
	if _, cmd := m.Update(keyMsg("k")); cmd != nil {
		t.Errorf("Update(k) back onto a polled host cmd = %v, want nil", cmd)
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
	// It is the fourth row: tab reaches it and space would cancel it.
	m = press(t, m, "shift+tab")
	if view := m.View(); !strings.Contains(view, "$ ssh -fN ubuntu") && !strings.Contains(view, "5000:localhost:5000 ubuntu") {
		t.Errorf("View() after shift+tab should preview the ad-hoc row's action:\n%s", view)
	}
}

func TestModel_View_HelpListsTunnelKeys(t *testing.T) {
	t.Parallel()
	// Presence only at a width where the tunnels column fits.
	m := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(t, m, "?")
	view := m.View()
	for _, want := range []string{"toggle set", "restart master", "exit master", "next forward"} {
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
	if view := m.View(); strings.Contains(view, "tunnels") {
		t.Errorf("View() renders the tunnels section with no engine:\n%s", view)
	}
	if _, cmd := m.Update(keyMsg(" ")); cmd != nil {
		t.Errorf("Update(space) with no engine cmd = %v, want nil", cmd)
	}
}
