package ui_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestHerdrCommand_ExactStrings(t *testing.T) {
	t.Parallel()
	// herdr reads ssh_config itself (SPEC §6): the alias, nothing else.
	if got, want := ui.HerdrCommand("web"), []string{"herdr", "--remote", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HerdrCommand(web) = %v, want %v", got, want)
	}
}

func TestModel_Update_HerdrKeyGuards(t *testing.T) {
	t.Parallel()
	// No emulator sasash can drive: H shows the command to run yourself
	// — it never hands off, never runs anything (4.22).
	m := probedModel(t, fakerun.New())
	next, cmd := m.Update(keyMsg("H"))
	if view := next.(ui.Model).View(); cmd != nil || !ui.PopupOpen(next.(ui.Model)) || !strings.Contains(view, "H: no terminal tab to open here") || !strings.Contains(view, "herdr --remote web") {
		t.Errorf("Update(H) without an emulator: cmd %v, view:\n%s", cmd != nil, view)
	}
	// No host, nothing.
	empty := sized(t, nil)
	if _, cmd := empty.Update(keyMsg("H")); cmd != nil {
		t.Errorf("Update(H) with no hosts cmd = %v, want nil", cmd)
	}
	// While filtering H is typed text.
	f := press(t, m, "/", "H")
	if got := ui.FilterText(f); got != "H" {
		t.Errorf("FilterText() = %q, want %q", got, "H")
	}
}

func TestModel_View_HerdrPreview(t *testing.T) {
	t.Parallel()
	// The host row's line names both handoffs; a highlighted forward row
	// previews space alone.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	// Without an emulator the H segment says H will show the command.
	if view := m.View(); !strings.Contains(view, "$ ssh ubuntu") || !strings.Contains(view, "H no terminal tab to open here (shows the command)") {
		t.Errorf("View() missing the H segment beside enter's:\n%s", view)
	}
	m = press(t, m, "l")
	if view := m.View(); strings.Contains(view, "H no terminal tab") {
		t.Errorf("View() with a forward highlighted still previews H:\n%s", view)
	}
}

func TestModel_View_HerdrPreviewFitsOrGoes(t *testing.T) {
	t.Parallel()
	// A long alias at a narrow width: the H segment must vanish whole,
	// never render as a clipped command.
	hosts := []hostmodel.Host{{Alias: "build-eu-west-bastion-01", Paths: []probe.Path{{Host: "192.0.2.10"}}}}
	m := ui.WithPreviewShown(ui.New(hosts, probe.Prober{Dialer: fakedial.New()}, fakerun.New(), ui.Config{Spawn: ui.SpawnConfig{Mux: ui.MuxTmux, Target: "tab"}}))
	wide := update(t, m, tea.WindowSizeMsg{Width: 240, Height: 24}).View()
	if !strings.Contains(wide, "H herdr --remote build-eu-west-bastion-01 (tmux window)") {
		t.Errorf("View() at 240 cols missing the whole H segment:\n%s", wide)
	}
	narrow := update(t, m, tea.WindowSizeMsg{Width: 60, Height: 24}).View()
	preview := ""
	for _, line := range strings.Split(narrow, "\n") {
		if strings.Contains(line, "$ ") {
			preview = line
		}
	}
	if strings.Contains(preview, "herd") {
		t.Errorf("preview at 60 cols shows a clipped H segment: %q", preview)
	}
	if !strings.Contains(preview, "build-eu-west-bastion-01") {
		t.Errorf("preview at 60 cols lost the enter command: %q", preview)
	}
}

func TestModel_View_HerdrPreviewAfterRowsShrink(t *testing.T) {
	t.Parallel()
	// A poll can return fewer rows than the highlight index (tab clamps
	// only at press time). The preview then falls back to the host row
	// — and the H segment must follow the same judgement, not a stale
	// index.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	stateDir := t.TempDir()
	two := `{"forwards":[{"set":"","spec":"5000:localhost:5000"},{"set":"","spec":"5001:localhost:5001"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(two), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := tunnelModel(t, r, fakedial.New(), stateDir)
	m = feed(t, m, m.Init())
	m = press(t, m, "l", "G") // last row: 5001
	if view := m.View(); strings.Contains(view, "herdr --remote") {
		t.Fatalf("View() with a row highlighted should not name H:\n%s", view)
	}
	one := `{"forwards":[{"set":"","spec":"5000:localhost:5000"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(one), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	// One row left: the highlight clamps onto it, H stays hidden.
	if view := m.View(); !lineHas(view, "› ", "5000:localhost:5000") || strings.Contains(view, "herdr --remote") {
		t.Errorf("View() after the rows shrank should clamp the highlight onto the last row:\n%s", view)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(`{"forwards":[]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, tick = m.Update(ui.RefreshTick())
	m = feed(t, m, tick)
	// The recorded rows are gone; the declared ones remain, so the
	// highlight clamps onto the last of those and H stays hidden. (Rows
	// vanishing entirely is TestModel_Update_FocusDropsWhenRowsVanish.)
	if view := m.View(); !ui.TunnelsFocused(m) || !lineHas(view, "› ", specAPI) || strings.Contains(view, "herdr --remote") {
		t.Errorf("View() after the recorded rows vanished should clamp onto the last declared row:\n%s", view)
	}
}

func TestModel_View_HelpListsHerdr(t *testing.T) {
	t.Parallel()
	// The keys panel is generated from the bindings so H cannot drift out
	// of it. No hosts, so no preview line can supply the word; the main
	// footer is a whisper that never names it.
	m := update(t, sized(t, nil), tea.WindowSizeMsg{Width: 140, Height: 60})
	if view := m.View(); strings.Contains(view, "herdr") || !strings.Contains(view, "? keys") {
		t.Errorf("View() footer should be the whisper:\n%s", view)
	}
	if view := press(t, m, "?").View(); !strings.Contains(view, "herdr") {
		t.Errorf("View() keys panel missing the H entry:\n%s", view)
	}
}

func TestModel_View_HelpListsActions(t *testing.T) {
	t.Parallel()
	m := ui.New(testHosts(1), probe.Prober{Dialer: fakedial.New()}, fakerun.New(), ui.Config{Actions: []string{"rdp", "vnc"}})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 60})
	if view := m.View(); strings.Contains(view, "actions.toml") {
		t.Fatalf("View() lists actions in the footer:\n%s", view)
	}
	m = press(t, m, "?", "G") // the actions group closes the panel
	view := m.View()
	if !strings.Contains(view, "actions.toml: rdp, vnc") || !strings.Contains(view, "not runnable") {
		t.Errorf("View() keys panel missing the actions line:\n%s", view)
	}
	// Closed, the line is gone with the rest of the panel; the filter
	// then owns the footer.
	m = press(t, m, "esc")
	if view := press(t, m, "/").View(); strings.Contains(view, "actions.toml") {
		t.Errorf("View() keeps the actions line while filtering:\n%s", view)
	}
	if view := press(t, m, "a").View(); strings.Contains(view, "actions.toml") {
		t.Errorf("View() keeps the actions line while a prompt is open:\n%s", view)
	}
	// No file, no line.
	plain := press(t, sized(t, testHosts(1)), "?")
	if strings.Contains(plain.View(), "actions.toml") {
		t.Errorf("View() lists actions with none configured:\n%s", plain.View())
	}
}

func TestModel_New_ActionsWarnIsStartupNotice(t *testing.T) {
	t.Parallel()
	m := ui.New(testHosts(1), probe.Prober{Dialer: fakedial.New()}, fakerun.New(), ui.Config{ActionsWarn: "actions.toml:3: unterminated string"})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := m.View(); !strings.Contains(view, "actions.toml:3: unterminated string") {
		t.Errorf("View() at startup missing the actions warning:\n%s", view)
	}
	if view := press(t, m, "j").View(); strings.Contains(view, "unterminated") {
		t.Errorf("View() keeps the warning past the first keypress:\n%s", view)
	}
}
