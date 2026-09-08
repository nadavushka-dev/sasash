package ui_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// H spawns beside the TUI (SASASH.4.22): herdr's remote session through
// the same road as enter, per surrounding multiplexer — and never nests
// herdr inside herdr.

func TestHerdrPlan_ExactArgv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cfg       ui.SpawnConfig
		wantSteps [][]string
		wantWhere string
	}{
		// Inside herdr, --remote would nest: the tab runs plain ssh, and the plan says so.
		{name: "herdr tab", cfg: herdrCfg("tab"),
			wantSteps: [][]string{{"herdr", "tab", "create", "--label", "web", "--no-focus"}, {"herdr", "pane", "run", "<pane>", "ssh", "web"}},
			wantWhere: "herdr tab"},
		{name: "herdr pane", cfg: herdrCfg("pane"),
			wantSteps: [][]string{{"herdr", "pane", "split", "--pane", "w24:p16", "--direction", "right", "--no-focus"}, {"herdr", "pane", "run", "<pane>", "ssh", "web"}},
			wantWhere: "herdr pane"},
		{name: "herdr workspace", cfg: herdrCfg("workspace"),
			wantSteps: [][]string{{"herdr", "workspace", "create", "--label", "web", "--no-focus"}, {"herdr", "pane", "run", "<pane>", "ssh", "web"}},
			wantWhere: "herdr workspace"},
		// Under tmux the window runs herdr --remote: not nested.
		{name: "tmux window", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab"},
			wantSteps: [][]string{{"tmux", "new-window", "-d", "-n", "web", "herdr", "--remote", "web"}}, wantWhere: "tmux window"},
		{name: "tmux split", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "pane"},
			wantSteps: [][]string{{"tmux", "split-window", "-h", "-d", "herdr", "--remote", "web"}}, wantWhere: "tmux split"},
		{name: "tmux session", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "workspace"},
			wantSteps: [][]string{{"tmux", "new-session", "-d", "-n", "web", "herdr", "--remote", "web"}}, wantWhere: "tmux session"},
		// A tmux started inside a herdr pane carries HERDR_ENV: --remote
		// would nest there too, so plain ssh, as inside herdr.
		{name: "tmux inside herdr", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab", HerdrEnv: true},
			wantSteps: [][]string{{"tmux", "new-window", "-d", "-n", "web", "ssh", "web"}}, wantWhere: "tmux window"},
		// No multiplexer: nothing to open beside the TUI — a window template
		// bakes its own ssh in and cannot carry H's command.
		{name: "no multiplexer", cfg: ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: darwinWindow}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			steps, where := ui.HerdrPlan(tt.cfg, "web")
			if !reflect.DeepEqual(steps, tt.wantSteps) || where != tt.wantWhere {
				t.Errorf("HerdrPlan(%s) = %v, %q; want %v, %q", tt.name, steps, where, tt.wantSteps, tt.wantWhere)
			}
		})
	}
}

func TestModel_Update_HSpawnsBesideTheTUI(t *testing.T) {
	t.Parallel()
	t.Run("inside herdr: a new tab with ssh, the TUI stays, the handoff is stamped", func(t *testing.T) {
		t.Parallel()
		var stamped []string
		r := fakerun.New().
			StubExit(0, "ssh", "-O", "check", "web").
			StubExit(0, "ssh", "-O", "check", "db").
			StubOut(tabReply, "herdr", "tab", "create", "--label", "web", "--no-focus").
			StubExit(0, "herdr", "pane", "run", "w24:p9", "ssh", "web")
		m := spawnModel(t, r, herdrCfg("tab"), func(a string) error { stamped = append(stamped, a); return nil })
		if view := m.View(); !strings.Contains(view, "H ssh web (herdr tab; --remote nests)") {
			t.Errorf("View() should preview H's step and the reason:\n%s", view)
		}
		before := len(r.CallKeys())
		next, cmd := m.Update(keyMsg("H"))
		m = next.(ui.Model)
		if cmd == nil {
			t.Fatalf("Update(H) cmd = nil, want the spawn")
		}
		next, stamp := m.Update(cmd())
		m = next.(ui.Model)
		if got, want := r.CallKeys()[before:], []string{"herdr tab create --label web --no-focus", "herdr pane run w24:p9 ssh web"}; !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %v, want %v — never herdr --remote inside herdr", got, want)
		}
		// The notice, and the TUI still on screen (no handoff).
		if view := m.View(); !strings.Contains(view, "spawned ssh web → herdr tab — herdr --remote would nest inside herdr") || !strings.Contains(view, "paths") || ui.HandoffHost(m) != "" {
			t.Errorf("View() should report the spawn and keep the TUI (handoff %q):\n%s", ui.HandoffHost(m), view)
		}
		if stamp == nil {
			t.Fatalf("a spawn is a connection: it should stamp the handoff")
		}
		feed(t, m, stamp)
		if !reflect.DeepEqual(stamped, []string{"web"}) {
			t.Errorf("stamped = %v, want [web]", stamped)
		}
	})
	t.Run("under tmux: a window running herdr --remote", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
			StubExit(0, "tmux", "new-window", "-d", "-n", "web", "herdr", "--remote", "web")
		m := spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab"}, nil)
		if view := m.View(); !strings.Contains(view, "H herdr --remote web (tmux window)") {
			t.Errorf("View() should preview the tmux step:\n%s", view)
		}
		before := len(r.CallKeys())
		next, cmd := m.Update(keyMsg("H"))
		m = update(t, next.(ui.Model), cmd())
		if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, []string{"tmux new-window -d -n web herdr --remote web"}) {
			t.Errorf("calls = %v", got)
		}
		if view := m.View(); !strings.Contains(view, "spawned herdr --remote web → tmux window") {
			t.Errorf("View() should report the spawn:\n%s", view)
		}
	})
	t.Run("a failed step opens the spawn popup, nothing hands off", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("no server running"), Code: 1}}, "tmux", "new-window", "-d", "-n", "web", "herdr", "--remote", "web")
		m := spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab"}, nil)
		next, cmd := m.Update(keyMsg("H"))
		m = update(t, next.(ui.Model), cmd())
		if !ui.PopupOpen(m) || !strings.Contains(m.View(), "spawn failed for web") || !strings.Contains(m.View(), "no server running") {
			t.Errorf("a failed H spawn should open the popup:\n%s", m.View())
		}
	})
}
