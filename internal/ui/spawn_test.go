package ui_test

import (
	"errors"
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
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestDetectMux_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		env      map[string]string
		wantMux  ui.Mux
		wantPane string
	}{
		{name: "nothing", env: map[string]string{}, wantMux: ui.MuxNone},
		{name: "herdr", env: map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w24:p16"}, wantMux: ui.MuxHerdr, wantPane: "w24:p16"},
		{name: "herdr without a pane id", env: map[string]string{"HERDR_ENV": "1"}, wantMux: ui.MuxHerdr},
		{name: "tmux", env: map[string]string{"TMUX": "/tmp/tmux-501/default,123,0"}, wantMux: ui.MuxTmux},
		{name: "tmux inside herdr is tmux: innermost wins", env: map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p2", "TMUX": "/tmp/tmux-501/default,123,0"}, wantMux: ui.MuxTmux},
		{name: "any value counts as set", env: map[string]string{"HERDR_ENV": "0"}, wantMux: ui.MuxHerdr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mux, pane := ui.DetectMux(func(k string) string { return tt.env[k] })
			if mux != tt.wantMux || pane != tt.wantPane {
				t.Errorf("DetectMux() = %v, %q; want %v, %q", mux, pane, tt.wantMux, tt.wantPane)
			}
		})
	}
}

const darwinWindow = `osascript -e 'tell application "Terminal" to do script "ssh {host}"'`

func TestSpawnArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	// These strings are the product: a typo spawns nothing, silently.
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{name: "herdr pane from a known pane", got: ui.HerdrCreateArgv("pane", "web", "w24:p16"), want: []string{"herdr", "pane", "split", "--pane", "w24:p16", "--direction", "right", "--no-focus"}},
		{name: "herdr pane without an id splits the current", got: ui.HerdrCreateArgv("pane", "web", ""), want: []string{"herdr", "pane", "split", "--current", "--direction", "right", "--no-focus"}},
		{name: "herdr tab", got: ui.HerdrCreateArgv("tab", "web", "w24:p16"), want: []string{"herdr", "tab", "create", "--label", "web", "--no-focus"}},
		{name: "herdr workspace", got: ui.HerdrCreateArgv("workspace", "web", ""), want: []string{"herdr", "workspace", "create", "--label", "web", "--no-focus"}},
		{name: "herdr run into the created pane", got: ui.HerdrRunArgv("w24:p9", "web"), want: []string{"herdr", "pane", "run", "w24:p9", "ssh", "web"}},
		{name: "tmux pane", got: ui.TmuxArgv("pane", "web"), want: []string{"tmux", "split-window", "-h", "-d", "ssh", "web"}},
		{name: "tmux tab", got: ui.TmuxArgv("tab", "web"), want: []string{"tmux", "new-window", "-d", "-n", "web", "ssh", "web"}},
		{name: "tmux workspace names the window, not the session", got: ui.TmuxArgv("workspace", "web"), want: []string{"tmux", "new-session", "-d", "-n", "web", "ssh", "web"}},
		// tmux refuses '.' and ':' in names; only the name changes.
		{name: "tmux tab with a dotted alias", got: ui.TmuxArgv("tab", "web.office"), want: []string{"tmux", "new-window", "-d", "-n", "web_office", "ssh", "web.office"}},
		{name: "tmux workspace with a port-like alias", got: ui.TmuxArgv("workspace", "web:1"), want: []string{"tmux", "new-session", "-d", "-n", "web_1", "ssh", "web:1"}},
		{name: "tmux pane keeps the alias untouched", got: ui.TmuxArgv("pane", "web.office"), want: []string{"tmux", "split-window", "-h", "-d", "ssh", "web.office"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("%s argv = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
	t.Run("window template expands per token", func(t *testing.T) {
		t.Parallel()
		got, err := ui.WindowArgv(darwinWindow, "web")
		want := []string{"osascript", "-e", `tell application "Terminal" to do script "ssh web"`}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("WindowArgv() = %q, %v; want %q", got, err, want)
		}
		if _, err := ui.WindowArgv("kitty {nope}", "web"); err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
			t.Errorf("WindowArgv(bad template) err = %v, want an unknown-placeholder error", err)
		}
	})
}

func TestHerdrPaneID_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reply   string
		want    string
		wantErr bool
	}{
		{name: "tab or workspace create", reply: `{"id":"cli:tab:create","result":{"root_pane":{"pane_id":"w24:p9","tab_id":"w24:tX"},"tab":{"tab_id":"w24:tX"},"type":"tab_created"}}`, want: "w24:p9"},
		{name: "pane split", reply: `{"id":"cli:pane:split","result":{"pane":{"pane_id":"w24:p10"},"type":"pane_info"}}`, want: "w24:p10"},
		{name: "no pane in the reply", reply: `{"id":"x","result":{"type":"ok"}}`, wantErr: true},
		{name: "not json", reply: `error: no server`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ui.HerdrPaneID([]byte(tt.reply))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("HerdrPaneID() = %q, %v; want %q, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

const tabReply = `{"id":"cli:tab:create","result":{"root_pane":{"pane_id":"w24:p9"},"tab":{"tab_id":"w24:tX"},"type":"tab_created"}}`

// spawnModel is probedModel's host set (web selected) with the spawn
// wiring, a real tunnel engine so a spawn's stamp has a poll to ride,
// and a wide frame so the two-step preview fits.
func spawnModel(t *testing.T, r *fakerun.Fake, cfg ui.SpawnConfig, rec func(string) error) ui.Model {
	t.Helper()
	hosts := []hostmodel.Host{
		{Alias: "web", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
		{Alias: "db", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.30"}}},
	}
	d := fakedial.New().Up("192.0.2.10:22", 5*time.Millisecond).Up("192.0.2.30:22", 5*time.Millisecond)
	eng := tunnel.Engine{Runner: r, Spawner: fakespawn.New().Dial(d), Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Spawn: cfg, Tunnels: eng, RecordHandoff: rec})
	m = update(t, m, tea.WindowSizeMsg{Width: 140, Height: 24})
	return feed(t, m, m.Init())
}

func herdrCfg(target string) ui.SpawnConfig {
	return ui.SpawnConfig{Mux: ui.MuxHerdr, HerdrPane: "w24:p16", Target: target}
}

func TestModel_Update_EnterSpawnsHerdr(t *testing.T) {
	t.Parallel()
	var stamped []string
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "web").
		StubExit(0, "ssh", "-O", "check", "db").
		StubOut(tabReply, "herdr", "tab", "create", "--label", "web", "--no-focus").
		StubExit(0, "herdr", "pane", "run", "w24:p9", "ssh", "web")
	m := spawnModel(t, r, herdrCfg("tab"), func(a string) error { stamped = append(stamped, a); return nil })
	// The line previews both steps, the pane still a placeholder, and
	// names S as the plain handoff.
	view := m.View()
	for _, want := range []string{"$ herdr tab create --label web --no-focus ; herdr pane run <pane> ssh web", "S ssh web", "H herdr --remote web"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q:\n%s", want, view)
		}
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(enter) cmd = nil, want the spawn")
	}
	done := cmd()
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"herdr tab create --label web --no-focus", "herdr pane run w24:p9 ssh web"}) {
		t.Errorf("calls = %v", got)
	}
	next, cmd = m.Update(done)
	m = next.(ui.Model)
	if !strings.Contains(m.View(), "spawned ssh web → herdr tab") {
		t.Errorf("View() missing the outcome:\n%s", m.View())
	}
	// A spawn is a connection: stamped on the ride-along poll.
	if cmd == nil {
		t.Fatalf("Update(spawnDone) cmd = nil, want the stamped poll")
	}
	cmd()
	if !reflect.DeepEqual(stamped, []string{"web"}) {
		t.Errorf("RecordHandoff calls = %v, want [web]", stamped)
	}
}

func TestModel_Update_EnterSpawnTargets(t *testing.T) {
	t.Parallel()
	splitReply := `{"id":"cli:pane:split","result":{"pane":{"pane_id":"w24:p10"},"type":"pane_info"}}`
	tests := []struct {
		name      string
		cfg       ui.SpawnConfig
		create    []string
		reply     string
		wantCalls []string
		wantWhere string
	}{
		{name: "herdr pane splits from the TUI's pane", cfg: herdrCfg("pane"),
			create: []string{"herdr", "pane", "split", "--pane", "w24:p16", "--direction", "right", "--no-focus"}, reply: splitReply,
			wantCalls: []string{"herdr pane split --pane w24:p16 --direction right --no-focus", "herdr pane run w24:p10 ssh web"}, wantWhere: "herdr pane"},
		{name: "herdr workspace", cfg: herdrCfg("workspace"),
			create: []string{"herdr", "workspace", "create", "--label", "web", "--no-focus"}, reply: tabReply,
			wantCalls: []string{"herdr workspace create --label web --no-focus", "herdr pane run w24:p9 ssh web"}, wantWhere: "herdr workspace"},
		{name: "unknown persisted target is the default tab", cfg: herdrCfg("stale"),
			create: []string{"herdr", "tab", "create", "--label", "web", "--no-focus"}, reply: tabReply,
			wantCalls: []string{"herdr tab create --label web --no-focus", "herdr pane run w24:p9 ssh web"}, wantWhere: "herdr tab"},
		{name: "tmux window is one step", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab"},
			create:    []string{"tmux", "new-window", "-d", "-n", "web", "ssh", "web"},
			wantCalls: []string{"tmux new-window -d -n web ssh web"}, wantWhere: "tmux window"},
		{name: "tmux session", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "workspace"},
			create:    []string{"tmux", "new-session", "-d", "-n", "web", "ssh", "web"},
			wantCalls: []string{"tmux new-session -d -n web ssh web"}, wantWhere: "tmux session"},
		{name: "no multiplexer runs the window template", cfg: ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: darwinWindow},
			create:    []string{"osascript", "-e", `tell application "Terminal" to do script "ssh web"`},
			wantCalls: []string{`osascript -e tell application "Terminal" to do script "ssh web"`}, wantWhere: "window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db")
			if tt.reply != "" {
				r.StubOut(tt.reply, tt.create...)
				id := "w24:p9"
				if strings.Contains(tt.reply, "w24:p10") {
					id = "w24:p10"
				}
				r.StubExit(0, "herdr", "pane", "run", id, "ssh", "web")
			} else {
				r.StubExit(0, tt.create...)
			}
			m := spawnModel(t, r, tt.cfg, nil)
			if view := m.View(); !strings.Contains(view, "$ "+strings.Join(tt.create, " ")) {
				t.Errorf("View() should preview the create step:\n%s", view)
			}
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("enter"))
			if cmd == nil {
				t.Fatalf("Update(enter) cmd = nil, want the spawn")
			}
			m = update(t, next.(ui.Model), cmd())
			if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", got, tt.wantCalls)
			}
			if !strings.Contains(m.View(), "spawned ssh web → "+tt.wantWhere) {
				t.Errorf("View() missing the outcome for %s:\n%s", tt.wantWhere, m.View())
			}
		})
	}
}

func TestModel_Update_EnterSpawnFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		stub       func(*fakerun.Fake)
		wantNotice string
		wantRun    bool // the second step ran
	}{
		{name: "create exits non-zero", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run1("herdr: no server\n")}, "herdr", "tab", "create", "--label", "web", "--no-focus")
		}, wantNotice: "herdr tab create failed: herdr: no server"},
		{name: "herdr's error envelope gives its message", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run1(`{"error":{"code":"workspace_not_found","message":"workspace w99 not found"},"id":"cli:tab:create"}` + "\n")}, "herdr", "tab", "create", "--label", "web", "--no-focus")
		}, wantNotice: "herdr tab create failed: workspace w99 not found"},
		{name: "valid json with no message falls through to the text", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run1(`{"id":"x","result":{"type":"ok"}}` + "\n")}, "herdr", "tab", "create", "--label", "web", "--no-focus")
		}, wantNotice: `herdr tab create failed: {"id":"x","result":{"type":"ok"}}`},
		{name: "a tool that speaks on stdout", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Stdout: []byte("no display\n"), Code: 1}}, "herdr", "tab", "create", "--label", "web", "--no-focus")
		}, wantNotice: "herdr tab create failed: no display"},
		{name: "a silent failure names the exit code", stub: func(r *fakerun.Fake) {
			r.StubExit(3, "herdr", "tab", "create", "--label", "web", "--no-focus")
		}, wantNotice: "herdr tab create failed: exit 3"},
		{name: "herdr cannot run", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Err: errors.New("exec: \"herdr\": executable file not found in $PATH")}, "herdr", "tab", "create", "--label", "web", "--no-focus")
		}, wantNotice: `herdr: exec: "herdr": executable file not found`},
		{name: "reply names no pane", stub: func(r *fakerun.Fake) {
			r.StubOut(`{"id":"x","result":{"type":"ok"}}`, "herdr", "tab", "create", "--label", "web", "--no-focus")
		}, wantNotice: "herdr reply names no pane"},
		{name: "pane run fails", stub: func(r *fakerun.Fake) {
			r.StubOut(tabReply, "herdr", "tab", "create", "--label", "web", "--no-focus")
			r.Stub(fakerun.Reply{Result: run1("pane not found\n")}, "herdr", "pane", "run", "w24:p9", "ssh", "web")
		}, wantNotice: "herdr pane run failed: pane not found", wantRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stamped int
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db")
			tt.stub(r)
			m := spawnModel(t, r, herdrCfg("tab"), func(string) error { stamped++; return nil })
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("enter"))
			next, cmd = next.(ui.Model).Update(cmd())
			m = next.(ui.Model)
			if !strings.Contains(m.View(), tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, m.View())
			}
			ran := false
			for _, k := range r.CallKeys()[before:] {
				ran = ran || strings.HasPrefix(k, "herdr pane run")
			}
			if ran != tt.wantRun {
				t.Errorf("pane run ran = %v, want %v (calls %v)", ran, tt.wantRun, r.CallKeys()[before:])
			}
			if cmd != nil || stamped != 0 {
				t.Errorf("a failed spawn: cmd %v, stamps %d; want none", cmd != nil, stamped)
			}
		})
	}
}

// run1 is a failed result with the given stderr.
func run1(stderr string) run.Result {
	return run.Result{Stderr: []byte(stderr), Code: 1}
}

func TestModel_Update_EnterFallsBackToHandoff(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db")
	m := spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxNone}, nil)
	// Nothing to spawn into: the line is the plain ssh, S is not repeated.
	if view := m.View(); !strings.Contains(view, "$ ssh web") || strings.Contains(view, "S ssh web") {
		t.Errorf("View() without a spawn should preview the handoff alone:\n%s", view)
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd == nil || len(r.CallKeys()) != before {
		t.Errorf("Update(enter) cmd %v, calls %v; want the handoff and no spawn", cmd != nil, r.CallKeys()[before:])
	}
	if !strings.Contains(m.View(), "nothing to spawn into") {
		t.Errorf("View() missing the fallback notice:\n%s", m.View())
	}
	// A template that cannot expand is a notice, not a handoff.
	m = spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: "kitty {nope}"}, nil)
	if view := m.View(); !strings.Contains(view, "$ spawn: action spawn-window: unknown placeholder {nope}") {
		t.Errorf("View() should show the template problem on the line:\n%s", view)
	}
	next, cmd = m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !strings.Contains(m.View(), "spawn: action spawn-window: unknown placeholder") {
		t.Errorf("Update(enter) with a bad template: cmd %v:\n%s", cmd != nil, m.View())
	}
}

func TestModel_Update_CopyCmdWithSpawn(t *testing.T) {
	t.Parallel()
	// The spawn line is not a shell command; c copies the connection.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db")
	want := yankStubs(r, "ssh web", "pbcopy")
	m := spawnModel(t, r, herdrCfg("tab"), nil)
	before := len(r.CallKeys())
	if _, cmd := m.Update(keyMsg("c")); cmd == nil {
		t.Fatalf("Update(c) cmd = nil")
	} else {
		cmd()
	}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("c copied %v, want %v", got, want)
	}
	// Same for a template error: never an error message in the clipboard.
	m = spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: "kitty {nope}"}, nil)
	before = len(r.CallKeys())
	_, cmd := m.Update(keyMsg("c"))
	cmd()
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("c with a bad template copied %v, want %v", got, want)
	}
}

func TestModel_Update_WindowTemplateNamedHerdr(t *testing.T) {
	t.Parallel()
	// A one-step template that happens to be herdr needs no reply parsed:
	// success is success.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
		StubOut(`{"id":"cli:tab:create","result":{"type":"ok"}}`, "herdr", "tab", "create", "--label", "web")
	m := spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: "herdr tab create --label {host}"}, nil)
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if !strings.Contains(m.View(), "spawned ssh web → window") {
		t.Errorf("View() should report the spawn:\n%s", m.View())
	}
}

func TestModel_Update_HandoffKey(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db")
	m := spawnModel(t, r, herdrCfg("tab"), nil)
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("S"))
	if cmd == nil || len(r.CallKeys()) != before || strings.Contains(next.(ui.Model).View(), "nothing to spawn") {
		t.Errorf("Update(S) cmd %v, calls %v; want the plain handoff", cmd != nil, r.CallKeys()[before:])
	}
	if got := ui.HandoffHost(next.(ui.Model)); got != "web" {
		t.Errorf("HandoffHost() = %q, want web", got)
	}
	// No host, nothing; while filtering S is typed text.
	if _, cmd := sized(t, nil).Update(keyMsg("S")); cmd != nil {
		t.Errorf("Update(S) with no hosts cmd = %v, want nil", cmd)
	}
	if got := ui.FilterText(press(t, m, "/", "S")); got != "S" {
		t.Errorf("FilterText() = %q, want S", got)
	}
	// Both handoffs are in the keys panel.
	m = press(t, update(t, m, tea.WindowSizeMsg{Width: 140, Height: 60}), "?")
	if view := m.View(); !ui.HelpOpen(m) || !strings.Contains(view, "ssh here") || !strings.Contains(view, "spawn ssh") {
		t.Errorf("View() keys panel missing S / enter rows:\n%s", view)
	}
}

func TestModel_Update_SettingsSpawnTarget(t *testing.T) {
	t.Parallel()
	var saved []string
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db")
	cfg := herdrCfg("tab")
	cfg.SaveTarget = func(v string) error { saved = append(saved, v); return nil }
	m := spawnModel(t, r, cfg, nil)
	m = press(t, m, "o")
	if view := m.View(); !strings.Contains(view, "spawn target") || !strings.Contains(view, "tab (herdr tab)") {
		t.Errorf("View() settings missing the spawn entry and its detail:\n%s", view)
	}
	m = press(t, m, "g", "j", "j", "j", "enter") // create, edit, theme, spawn target
	if view := m.View(); !strings.Contains(view, "spawn target — where enter opens") || !strings.Contains(view, "› ● tab") {
		t.Errorf("View() should be the picker with the cursor on the current tab:\n%s", view)
	}
	next, cmd := press(t, m, "g").Update(keyMsg("enter")) // pane
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(enter) on pane cmd = nil, want the save")
	}
	m = update(t, m, cmd())
	if !reflect.DeepEqual(saved, []string{"pane"}) || ui.SpawnTargetOf(m) != "pane" || !strings.Contains(m.View(), "spawn target: pane") {
		t.Errorf("saved = %v, target %q:\n%s", saved, ui.SpawnTargetOf(m), m.View())
	}
	// esc lands back on the entry, whose detail changed; the preview too.
	m = press(t, m, "esc")
	if view := m.View(); !strings.Contains(view, "› spawn target  pane (herdr pane)") {
		t.Errorf("View() after esc should mark the entry with the new detail:\n%s", view)
	}
	m = press(t, m, "esc")
	if view := m.View(); !strings.Contains(view, "$ herdr pane split --pane w24:p16 --direction right --no-focus ; herdr pane run <pane> ssh web") {
		t.Errorf("View() preview should follow the target:\n%s", view)
	}
	// A failed save is a notice; the pick still applies.
	cfg.SaveTarget = func(string) error { return errors.New("disk full") }
	m = spawnModel(t, r, cfg, nil)
	m = press(t, m, "o", "g", "j", "j", "j", "enter", "G")
	next, cmd = m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if ui.SpawnTargetOf(m) != "workspace" || !strings.Contains(m.View(), "spawn target not saved: disk full") {
		t.Errorf("target %q, view:\n%s", ui.SpawnTargetOf(m), m.View())
	}
	// Without a multiplexer the entry says handoff (or window, with a
	// template) and the picker says what enter does instead.
	m = spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxNone}, nil)
	m = press(t, m, "o")
	if view := m.View(); !strings.Contains(view, "tab (handoff)") {
		t.Errorf("View() entry without a template should say handoff:\n%s", view)
	}
	m = press(t, m, "g", "j", "j", "j", "enter")
	if view := m.View(); !strings.Contains(view, "no herdr or tmux here") {
		t.Errorf("View() picker without a multiplexer should say so:\n%s", view)
	}
	m = spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: darwinWindow}, nil)
	if view := press(t, m, "o").View(); !strings.Contains(view, "tab (window)") {
		t.Errorf("View() entry with a template should say window:\n%s", view)
	}
}
