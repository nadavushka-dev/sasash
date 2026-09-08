package ui_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// H spawns beside the TUI (SASASH.4.22): `herdr --remote` through the
// spawn road — and inside herdr, up and out into a tab of the OUTER
// terminal emulator, so it never nests herdr inside herdr.

// weztermTab is H's exact wezterm argv: herdr's pane variables scrubbed
// (wezterm's CLI forwards its environment into the new pane), wezterm's
// own stale ones dropped, no mux server auto-started.
var weztermTab = []string{"env", "-u", "HERDR_ENV", "-u", "HERDR_PANE_ID", "-u", "HERDR_TAB_ID", "-u", "HERDR_WORKSPACE_ID", "-u", "HERDR_SOCKET_PATH",
	"-u", "WEZTERM_UNIX_SOCKET", "-u", "WEZTERM_PANE", "wezterm", "cli", "--no-auto-start", "spawn", "--", "herdr", "--remote", "web"}

func TestDetectEmulator_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want ui.Emulator
	}{
		{name: "wezterm by pane", env: map[string]string{"WEZTERM_PANE": "0", "TERM_PROGRAM": "tmux"}, want: ui.EmuWezTerm},
		{name: "wezterm by program", env: map[string]string{"TERM_PROGRAM": "WezTerm"}, want: ui.EmuWezTerm},
		{name: "kitty", env: map[string]string{"KITTY_WINDOW_ID": "1"}, want: ui.EmuKitty},
		{name: "iTerm2", env: map[string]string{"TERM_PROGRAM": "iTerm.app"}, want: ui.EmuITerm},
		{name: "Terminal.app", env: map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, want: ui.EmuTerminal},
		{name: "tmux alone", env: map[string]string{"TERM_PROGRAM": "tmux"}, want: ui.EmuNone},
		{name: "nothing", env: map[string]string{}, want: ui.EmuNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.DetectEmulator(func(k string) string { return tt.env[k] }); got != tt.want {
				t.Errorf("DetectEmulator(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestHerdrPlan_ExactArgv(t *testing.T) {
	t.Parallel()
	inHerdr := func(emu ui.Emulator) ui.SpawnConfig {
		return ui.SpawnConfig{Mux: ui.MuxHerdr, HerdrPane: "w24:p16", HerdrEnv: true, Target: "tab", Emulator: emu}
	}
	tests := []struct {
		name      string
		cfg       ui.SpawnConfig
		alias     string
		wantSteps [][]string
		wantWhere string
	}{
		// Inside herdr: never a herdr tab, never plain ssh — the outer emulator's tab.
		{name: "herdr in wezterm", cfg: inHerdr(ui.EmuWezTerm), wantSteps: [][]string{weztermTab}, wantWhere: "wezterm tab"},
		{name: "herdr in kitty", cfg: inHerdr(ui.EmuKitty),
			wantSteps: [][]string{{"kitten", "@", "launch", "--type=tab", "--tab-title", "web", "herdr", "--remote", "web"}}, wantWhere: "kitty tab"},
		{name: "herdr in iTerm2", cfg: inHerdr(ui.EmuITerm),
			wantSteps: [][]string{{"osascript", "-e", "on run argv", "-e", `tell application "iTerm2"`, "-e", "tell current window",
				"-e", "create tab with default profile", "-e", "tell current session to write text (item 1 of argv)",
				"-e", "end tell", "-e", "end tell", "-e", "end run", "herdr --remote web"}},
			wantWhere: "iTerm2 tab"},
		{name: "herdr in Terminal.app", cfg: inHerdr(ui.EmuTerminal),
			wantSteps: [][]string{{"osascript", "-e", "on run argv", "-e", `tell application "Terminal" to do script (item 1 of argv)`, "-e", "end run", "herdr --remote web"}},
			wantWhere: "Terminal window"},
		// The shell line for the scripted apps is quoted per word, never spliced into the script.
		{name: "iTerm2 quotes the alias", cfg: inHerdr(ui.EmuITerm), alias: "we'b x",
			wantSteps: [][]string{{"osascript", "-e", "on run argv", "-e", `tell application "iTerm2"`, "-e", "tell current window",
				"-e", "create tab with default profile", "-e", "tell current session to write text (item 1 of argv)",
				"-e", "end tell", "-e", "end tell", "-e", "end run", `herdr --remote 'we'\''b x'`}},
			wantWhere: "iTerm2 tab"},
		// The spawn target is herdr's business; H's tab ignores it.
		{name: "herdr pane target still a wezterm tab", cfg: ui.SpawnConfig{Mux: ui.MuxHerdr, HerdrEnv: true, Target: "pane", Emulator: ui.EmuWezTerm},
			wantSteps: [][]string{weztermTab}, wantWhere: "wezterm tab"},
		// The mux alone is the herdr signal too.
		{name: "herdr mux without the env flag", cfg: ui.SpawnConfig{Mux: ui.MuxHerdr, Target: "tab", Emulator: ui.EmuWezTerm},
			wantSteps: [][]string{weztermTab}, wantWhere: "wezterm tab"},
		// A tmux started inside a herdr pane carries HERDR_ENV: its window would nest too, so the emulator tab.
		{name: "tmux inside herdr", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, HerdrEnv: true, Target: "tab", Emulator: ui.EmuWezTerm},
			wantSteps: [][]string{weztermTab}, wantWhere: "wezterm tab"},
		// tmux with no herdr above: a tmux window is not nested.
		{name: "tmux window", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab", Emulator: ui.EmuWezTerm},
			wantSteps: [][]string{{"tmux", "new-window", "-d", "-n", "web", "herdr", "--remote", "web"}}, wantWhere: "tmux window"},
		{name: "tmux split", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "pane"},
			wantSteps: [][]string{{"tmux", "split-window", "-h", "-d", "herdr", "--remote", "web"}}, wantWhere: "tmux split"},
		{name: "tmux session", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "workspace"},
			wantSteps: [][]string{{"tmux", "new-session", "-d", "-n", "web", "herdr", "--remote", "web"}}, wantWhere: "tmux session"},
		// No multiplexer: the emulator tab; a window template cannot carry H's command.
		{name: "no multiplexer, wezterm", cfg: ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: darwinWindow, Emulator: ui.EmuWezTerm},
			wantSteps: [][]string{weztermTab}, wantWhere: "wezterm tab"},
		// No emulator to drive: nil — the caller shows the command. Never ssh, never a herdr tab.
		{name: "herdr, no emulator", cfg: ui.SpawnConfig{Mux: ui.MuxHerdr, HerdrEnv: true, Target: "tab"}},
		{name: "tmux inside herdr, no emulator", cfg: ui.SpawnConfig{Mux: ui.MuxTmux, HerdrEnv: true, Target: "tab"}},
		{name: "nothing at all", cfg: ui.SpawnConfig{Mux: ui.MuxNone, WindowTemplate: darwinWindow}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			alias := tt.alias
			if alias == "" {
				alias = "web"
			}
			steps, where := ui.HerdrPlan(tt.cfg, alias)
			if !reflect.DeepEqual(steps, tt.wantSteps) || where != tt.wantWhere {
				t.Errorf("HerdrPlan(%s) = %v, %q; want %v, %q", tt.name, steps, where, tt.wantSteps, tt.wantWhere)
			}
		})
	}
}

func TestModel_Update_HSpawnsBesideTheTUI(t *testing.T) {
	t.Parallel()
	inWezterm := ui.SpawnConfig{Mux: ui.MuxHerdr, HerdrPane: "w24:p16", HerdrEnv: true, Target: "tab", Emulator: ui.EmuWezTerm}
	t.Run("inside herdr: a wezterm tab runs herdr --remote, the TUI stays, the handoff is stamped", func(t *testing.T) {
		// Through ui.New, so the Emulator/HerdrEnv wiring itself is pinned
		// — the argv table proves herdrPlanFor, not the model's spawnState.
		t.Parallel()
		var stamped []string
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").StubExit(0, weztermTab...)
		m := spawnModel(t, r, inWezterm, func(a string) error { stamped = append(stamped, a); return nil })
		if view := m.View(); !strings.Contains(view, "H herdr --remote web (wezterm tab)") {
			t.Errorf("View() should preview H's command and where:\n%s", view)
		}
		before := len(r.CallKeys())
		next, cmd := m.Update(keyMsg("H"))
		m = next.(ui.Model)
		if cmd == nil {
			t.Fatalf("Update(H) cmd = nil, want the spawn")
		}
		next, stamp := m.Update(cmd())
		m = next.(ui.Model)
		if got, want := r.CallKeys()[before:], []string{strings.Join(weztermTab, " ")}; !reflect.DeepEqual(got, want) {
			t.Errorf("calls = %v, want %v — the outer emulator's tab, never a herdr tab", got, want)
		}
		// The notice, and the TUI still on screen (no handoff).
		if view := m.View(); !strings.Contains(view, "spawned herdr --remote web → wezterm tab") || !strings.Contains(view, "paths") || ui.HandoffHost(m) != "" {
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
	t.Run("under tmux with no herdr above: a window running herdr --remote", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
			StubExit(0, "tmux", "new-window", "-d", "-n", "web", "herdr", "--remote", "web")
		m := spawnModel(t, r, ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab", Emulator: ui.EmuWezTerm}, nil)
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
	t.Run("inside herdr with no emulator: the popup names the command, nothing runs", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db")
		m := spawnModel(t, r, herdrCfg("tab"), nil)
		before := len(r.CallKeys())
		next, cmd := m.Update(keyMsg("H"))
		m = next.(ui.Model)
		view := m.View()
		if cmd != nil || len(r.CallKeys()) != before || !ui.PopupOpen(m) || !strings.Contains(view, "H: no terminal tab to open here") || !strings.Contains(view, "herdr --remote web") || !strings.Contains(view, "outside herdr") {
			t.Errorf("Update(H) without an emulator: cmd %v, calls %v, view:\n%s", cmd != nil, r.CallKeys()[before:], view)
		}
		if ui.HandoffHost(m) != "" {
			t.Errorf("HandoffHost() = %q, want none — H never hands off", ui.HandoffHost(m))
		}
	})
	t.Run("a failed step opens the spawn popup, nothing hands off", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web").StubExit(0, "ssh", "-O", "check", "db").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("failed to connect to Socket"), Code: 1}}, weztermTab...)
		m := spawnModel(t, r, inWezterm, nil)
		next, cmd := m.Update(keyMsg("H"))
		m = update(t, next.(ui.Model), cmd())
		if !ui.PopupOpen(m) || !strings.Contains(m.View(), "spawn failed for web") || !strings.Contains(m.View(), "failed to connect to Socket") || ui.HandoffHost(m) != "" {
			t.Errorf("a failed H spawn should open the popup (handoff %q):\n%s", ui.HandoffHost(m), m.View())
		}
	})
}
