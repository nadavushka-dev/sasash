package ui_test

import (
	"errors"
	"os/exec"
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
	if got, want := ui.HerdrExec("web").Args, []string{"herdr", "--remote", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HerdrExec(web).Args = %v, want %v", got, want)
	}
}

func TestModel_Update_HerdrHandoff(t *testing.T) {
	t.Parallel()
	m := probedModel(t, fakerun.New())
	if _, cmd := m.Update(keyMsg("H")); cmd == nil {
		t.Errorf("Update(H) cmd = nil, want the herdr handoff")
	}
	// No host, no handoff.
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

func TestModel_Update_HerdrExitVariants(t *testing.T) {
	t.Parallel()
	exitErr := exec.Command("false").Run()
	if exitErr == nil {
		t.Fatalf("exec false: expected non-nil exit error for fixture")
	}
	tests := []struct {
		name       string
		err        error
		wantNotice bool
	}{
		{name: "clean exit re-probes quietly", err: nil},
		{name: "herdr's own exit code is not news", err: exitErr},
		{name: "start failure is reported", err: errors.New(`exec: "herdr": executable file not found in $PATH`), wantNotice: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := probedModel(t, fakerun.New())
			next, cmd := m.Update(ui.HerdrExit(tt.err))
			m = next.(ui.Model)
			if cmd == nil {
				t.Errorf("Update(herdrExit) cmd = nil, want a re-probe")
			}
			if got := strings.Contains(m.View(), "herdr: "); got != tt.wantNotice {
				t.Errorf("View() shows a herdr notice = %v, want %v:\n%s", got, tt.wantNotice, m.View())
			}
		})
	}
}

func TestModel_View_HerdrPreview(t *testing.T) {
	t.Parallel()
	// The host row's line names both handoffs; a highlighted forward row
	// previews space alone.
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New())
	if view := m.View(); !strings.Contains(view, "$ ssh ubuntu") || !strings.Contains(view, "H herdr --remote ubuntu") {
		t.Errorf("View() missing the herdr preview beside enter's:\n%s", view)
	}
	m = press(t, m, "tab")
	if view := m.View(); strings.Contains(view, "herdr --remote") {
		t.Errorf("View() with a forward highlighted still previews herdr:\n%s", view)
	}
}

func TestModel_View_HerdrPreviewFitsOrGoes(t *testing.T) {
	t.Parallel()
	// A long alias at a narrow width: the H segment must vanish whole,
	// never render as a clipped command.
	hosts := []hostmodel.Host{{Alias: "build-eu-west-bastion-01", Paths: []probe.Path{{Host: "192.0.2.10"}}}}
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New()}, fakerun.New(), ui.Config{})
	wide := update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24}).View()
	if !strings.Contains(wide, "H herdr --remote build-eu-west-bastion-01") {
		t.Errorf("View() at 100 cols missing the whole H segment:\n%s", wide)
	}
	narrow := update(t, m, tea.WindowSizeMsg{Width: 60, Height: 24}).View()
	if strings.Contains(narrow, "herd") {
		t.Errorf("View() at 60 cols shows a clipped H segment:\n%s", narrow)
	}
	if !strings.Contains(narrow, "$ ssh build-eu-west-bastion-01") {
		t.Errorf("View() at 60 cols lost the enter preview:\n%s", narrow)
	}
}

func TestModel_View_HelpListsHerdr(t *testing.T) {
	t.Parallel()
	// The help is generated from the bindings so H cannot drift out of
	// it: present in the short help at a width where it fits and in the
	// full help (whose key column pads "H" out from "herdr"). No hosts,
	// so no preview line can supply the word.
	m := update(t, sized(t, nil), tea.WindowSizeMsg{Width: 140, Height: 30})
	if view := m.View(); !strings.Contains(view, "H herdr") {
		t.Errorf("View() short help missing the H entry:\n%s", view)
	}
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if view := press(t, m, "?").View(); !strings.Contains(view, "herdr") {
		t.Errorf("View() full help missing the H entry:\n%s", view)
	}
}

func TestModel_View_HelpListsActions(t *testing.T) {
	t.Parallel()
	m := ui.New(testHosts(1), probe.Prober{Dialer: fakedial.New()}, fakerun.New(), ui.Config{Actions: []string{"rdp", "vnc"}})
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if view := m.View(); strings.Contains(view, "actions.toml") {
		t.Fatalf("View() lists actions in the short help:\n%s", view)
	}
	m = press(t, m, "?")
	view := m.View()
	if !strings.Contains(view, "actions.toml: rdp, vnc") || !strings.Contains(view, "not runnable") {
		t.Errorf("View() full help missing the actions line:\n%s", view)
	}
	// The line yields with the rest of the help when the filter or a
	// prompt owns the keyboard.
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
