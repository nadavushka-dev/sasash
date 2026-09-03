package ui_test

import (
	"errors"
	"os/exec"
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
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestSSHCommand_Argv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		alias string
		want  []string
	}{
		{name: "plain alias", alias: "ubuntu", want: []string{"ssh", "ubuntu"}},
		{name: "dotted alias", alias: "ubuntu.lan", want: []string{"ssh", "ubuntu.lan"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.SSHCommand(tt.alias); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SSHCommand(%q) = %v, want %v", tt.alias, got, tt.want)
			}
		})
	}
}

func TestYankArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	// Both legs are exact argv through the run seam: the OSC 52 leg carries
	// the base64 payload built in Go, the tool leg the text as $1 — never
	// spliced into the script.
	tests := []struct {
		name string
		text string
		osc  string // base64 of text
	}{
		{name: "address", text: "192.0.2.10", osc: "MTkyLjAuMi4xMA=="},
		{name: "command with spaces travels as one argument", text: "ssh ubuntu", osc: "c3NoIHVidW50dQ=="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, want := ui.Osc52Argv(tt.text), []string{"sh", "-c", `printf '\033]52;c;%s\a' "$1" > /dev/tty`, "sh", tt.osc}; !reflect.DeepEqual(got, want) {
				t.Errorf("Osc52Argv(%q) = %v, want %v", tt.text, got, want)
			}
			got := ui.ClipToolArgv(tt.text)
			if len(got) != 5 || got[0] != "sh" || got[1] != "-c" || got[3] != "sh" || got[4] != tt.text {
				t.Errorf("ClipToolArgv(%q) = %v, want sh -c <script> sh %q", tt.text, got, tt.text)
			}
			for _, tool := range []string{"pbcopy", "wl-copy", "xclip -selection clipboard", "clip.exe", "command -v", "exit 3"} {
				if !strings.Contains(got[2], tool) {
					t.Errorf("ClipToolArgv script missing %q:\n%s", tool, got[2])
				}
			}
		})
	}
}

// yankStubs stubs both legs of a yank of text as succeeding, the tool leg
// naming the tool, and returns the calls a yank makes in order.
func yankStubs(r *fakerun.Fake, text, tool string) []string {
	r.StubExit(0, ui.Osc52Argv(text)...).StubOut(tool, ui.ClipToolArgv(text)...)
	return []string{fakerun.Key(ui.Osc52Argv(text)...), fakerun.Key(ui.ClipToolArgv(text)...)}
}

// probedModel builds a sized model over web (lan down, ts up) + db, with
// the first probe already applied, wired to the given runner.
func probedModel(t *testing.T, runner *fakerun.Fake) ui.Model {
	t.Helper()
	hosts := []hostmodel.Host{
		{Alias: "web", Paths: []probe.Path{
			{Name: "lan", Host: "192.0.2.10"},
			{Name: "ts", Host: "198.51.100.5"},
		}},
		{Alias: "db", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.30"}}},
	}
	dialer := fakedial.New().
		Down("192.0.2.10:22").
		Up("198.51.100.5:22", 20*time.Millisecond).
		Up("192.0.2.30:22", 5*time.Millisecond)
	m := ui.New(hosts, probe.Prober{Dialer: dialer, Timeout: 50 * time.Millisecond}, runner, ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return update(t, m, m.Init()())
}

func TestModel_Update_CopyAddr(t *testing.T) {
	t.Parallel()
	// y copies the bare active host — the clipboard twin of `sasash addr` —
	// which for web is the ts path, the first alive one in config order.
	runner := fakerun.New()
	want := yankStubs(runner, "198.51.100.5", "pbcopy")
	m := probedModel(t, runner)

	_, cmd := m.Update(keyMsg("y"))
	if cmd == nil {
		t.Fatalf("Update(y) cmd = nil, want clipboard cmd")
	}
	done := cmd()
	if got := runner.CallKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("CallKeys() = %v, want %v", got, want)
	}
	m = update(t, m, done)
	if view := m.View(); !strings.Contains(view, "copied via terminal (OSC 52) + pbcopy") || ui.PopupOpen(m) {
		t.Errorf("View() after a copy should name both legs on the line:\n%s", view)
	}
}

func TestModel_Update_CopyAddr_NoAlivePath(t *testing.T) {
	t.Parallel()
	runner := fakerun.New()
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}}}
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New().Down("192.0.2.10:22"), Timeout: 50 * time.Millisecond}, runner, ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = update(t, m, m.Init()())

	m2, cmd := m.Update(keyMsg("y"))
	m = m2.(ui.Model)
	if cmd != nil {
		t.Errorf("Update(y) cmd = %v, want nil when nothing is alive", cmd)
	}
	if len(runner.Calls()) != 0 {
		t.Errorf("Calls() = %v, want none", runner.Calls())
	}
	// The pin requires y with no alive path to copy nothing AND say so.
	if view := m.View(); !strings.Contains(view, "nothing copied") {
		t.Errorf("View() = %q, want a nothing-copied notice", view)
	}
	// The notice is transient: the next keypress clears it.
	m = update(t, m, keyMsg("j"))
	if view := m.View(); strings.Contains(view, "nothing copied") {
		t.Errorf("View() after next key still shows the notice:\n%s", view)
	}
}

func TestModel_Update_CopyCmd(t *testing.T) {
	t.Parallel()
	runner := fakerun.New()
	want := yankStubs(runner, "ssh web", "wl-copy")
	m := probedModel(t, runner)

	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatalf("Update(c) cmd = nil, want clipboard cmd")
	}
	cmd()
	if got := runner.CallKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("CallKeys() = %v, want %v", got, want)
	}
}

func TestModel_Update_CopyOutcomes(t *testing.T) {
	t.Parallel()
	// The two legs are independent: either alone is a copy; neither is
	// the floor — the value on the line and in the popup, never silence.
	tests := []struct {
		name       string
		stub       func(r *fakerun.Fake)
		wantNotice string
		wantPopup  bool
	}{
		{name: "terminal only (no tool on PATH)", stub: func(r *fakerun.Fake) {
			r.StubExit(0, ui.Osc52Argv("ssh web")...).StubExit(3, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "copied via terminal (OSC 52)"},
		{name: "tool only (no tty)", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("sh: /dev/tty: Device not configured\n"), Code: 1}}, ui.Osc52Argv("ssh web")...).StubOut("xclip", ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "copied via xclip"},
		{name: "floor: no tty and no tool", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("sh: /dev/tty: No such device or address\n"), Code: 1}}, ui.Osc52Argv("ssh web")...).StubExit(3, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "clipboard not available — ssh web", wantPopup: true},
		{name: "floor: the tool itself fails", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Err: errors.New("exec: \"sh\": executable file not found in $PATH")}, ui.Osc52Argv("ssh web")...).
				Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("xclip: Error: Can't open display\n"), Code: 1}}, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "clipboard not available — ssh web", wantPopup: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := fakerun.New()
			tt.stub(runner)
			m := probedModel(t, runner)
			_, cmd := m.Update(keyMsg("c"))
			if cmd == nil {
				t.Fatalf("Update(c) cmd = nil, want clipboard cmd")
			}
			m = update(t, m, cmd())
			if ui.PopupOpen(m) != tt.wantPopup {
				t.Errorf("popup open = %v, want %v:\n%s", ui.PopupOpen(m), tt.wantPopup, m.View())
			}
			view := m.View()
			if tt.wantPopup {
				// The popup shows the value whole and what was tried;
				// closed, the line still carries the value.
				if !strings.Contains(view, "clipboard not available here") || !strings.Contains(view, "  ssh web") || !strings.Contains(view, "tried: ") || (strings.Contains(tt.name, "no tool") && !strings.Contains(view, "no clipboard")) {
					t.Errorf("View() floor popup:\n%s", view)
				}
				view = press(t, m, "esc").View()
			}
			if !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
		})
	}
}

func TestSSHExec_Args(t *testing.T) {
	t.Parallel()
	// The builder's argv must actually reach the process enter runs.
	if got := ui.SSHExec("web").Args; !reflect.DeepEqual(got, []string{"ssh", "web"}) {
		t.Errorf("SSHExec(web).Args = %v, want %v", got, []string{"ssh", "web"})
	}
}

func TestModel_Update_Connect(t *testing.T) {
	t.Parallel()
	m := probedModel(t, fakerun.New())
	_, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Errorf("Update(enter) cmd = nil, want ssh handoff cmd")
	}

	empty := sized(t, nil)
	for _, k := range []string{"enter", "y", "c"} {
		if _, cmd := empty.Update(keyMsg(k)); cmd != nil {
			t.Errorf("Update(%s) on empty list cmd = %v, want nil", k, cmd)
		}
	}
}

func TestModel_Update_CopyAddr_BeforeFirstProbe(t *testing.T) {
	t.Parallel()
	runner := fakerun.New()
	hosts := []hostmodel.Host{{Alias: "web", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}}}
	m := ui.New(hosts, probe.Prober{Dialer: fakedial.New(), Timeout: 50 * time.Millisecond}, runner, ui.Config{})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// No probe result yet: the pane says pending, so y must not claim the
	// paths are down.
	m2, cmd := m.Update(keyMsg("y"))
	m = m2.(ui.Model)
	if cmd != nil {
		t.Errorf("Update(y) before first probe cmd = %v, want nil", cmd)
	}
	if len(runner.Calls()) != 0 {
		t.Errorf("Calls() = %v, want none", runner.Calls())
	}
	if view := m.View(); !strings.Contains(view, "still probing") {
		t.Errorf("View() = %q, want a still-probing notice", view)
	}
}

func TestModel_Update_SSHExit_NoticeOnlyOnStartFailure(t *testing.T) {
	t.Parallel()
	// A genuine *exec.ExitError, as bubbletea's callback delivers for any
	// non-zero remote exit — an ordinary session end, not news.
	exitErr := exec.Command("false").Run()
	if exitErr == nil {
		t.Fatalf("exec false: expected non-nil exit error for fixture")
	}
	tests := []struct {
		name       string
		err        error
		wantNotice bool
	}{
		{name: "clean exit", err: nil, wantNotice: false},
		{name: "non-zero ssh exit is not news", err: exitErr, wantNotice: false},
		{name: "start failure surfaces", err: errors.New(`exec: "ssh": executable file not found`), wantNotice: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := probedModel(t, fakerun.New())
			next, cmd := m.Update(ui.SSHExit(tt.err))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(sshExit) cmd = nil, want re-probe")
			}
			got := strings.Contains(m.View(), "ssh: ")
			if got != tt.wantNotice {
				t.Errorf("View() ssh notice = %v, want %v", got, tt.wantNotice)
			}
		})
	}
}

func TestModel_Update_SSHExit_ReprobesOnce(t *testing.T) {
	t.Parallel()
	m := probedModel(t, fakerun.New()) // first probeDone already armed the tick
	next, cmd := m.Update(ui.SSHExit(nil))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(sshExit) cmd = nil, want re-probe")
	}
	// The re-probe's done msg must NOT arm a second tick chain.
	if _, tick := m.Update(cmd()); tick != nil {
		t.Errorf("Update(probeDoneMsg) after ssh exit cmd = non-nil, want nil (tick already armed)")
	}
}
