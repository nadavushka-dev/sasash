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

// clipScript is the fallback script as the product: pinned whole, like
// every other argv, so a changed line is a deliberate change.
const clipScript = `failed=
for t in pbcopy wl-copy xclip clip.exe; do
  command -v "$t" >/dev/null 2>&1 || continue
  if case "$t" in xclip) printf %s "$1" | xclip -selection clipboard ;; *) printf %s "$1" | "$t" ;; esac >/dev/null 2>&1; then
    printf %s "$t"; exit 0
  else
    failed="$failed${failed:+; }$t exited $?"
  fi
done
if [ -n "$failed" ]; then printf %s "$failed"; exit 98; fi
exit 97`

func TestYankArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	// Both legs are exact argv through the run seam: the OSC 52 leg carries
	// the base64 payload built in Go (standard alphabet, padding kept), the
	// tool leg the whole script with the text as $1 — never spliced in.
	tests := []struct {
		name string
		text string
		osc  string // base64 of text
	}{
		{name: "address", text: "192.0.2.10", osc: "MTkyLjAuMi4xMA=="},
		{name: "command with spaces travels as one argument", text: "ssh ubuntu", osc: "c3NoIHVidW50dQ=="},
		{name: "standard alphabet, not URL-safe", text: "a?>", osc: "YT8+"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got, want := ui.Osc52Argv(tt.text), []string{"sh", "-c", `printf '\033]52;c;%s\a' "$1" > /dev/tty`, "sh", tt.osc}; !reflect.DeepEqual(got, want) {
				t.Errorf("Osc52Argv(%q) = %v, want %v", tt.text, got, want)
			}
			if got, want := ui.ClipToolArgv(tt.text), []string{"sh", "-c", clipScript, "sh", tt.text}; !reflect.DeepEqual(got, want) {
				t.Errorf("ClipToolArgv(%q) = %q, want %q", tt.text, got, want)
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
		wantTried  string // in the floor popup
		wantNot    string // never on screen, either tier
	}{
		{name: "terminal only (no tool on PATH) says it cannot be confirmed", stub: func(r *fakerun.Fake) {
			r.StubExit(0, ui.Osc52Argv("ssh web")...).StubExit(97, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "copied via terminal (OSC 52) only — unconfirmed; no clipboard tool here"},
		{name: "both legs name both, no doubt", stub: func(r *fakerun.Fake) {
			r.StubExit(0, ui.Osc52Argv("ssh web")...).StubOut("wl-copy", ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "copied via terminal (OSC 52) + wl-copy", wantNot: "unconfirmed"},
		{name: "floor: the terminal leg's stderr is sanitized", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("no\rtty \x1b[2Jgone\n"), Code: 1}}, ui.Osc52Argv("ssh web")...).StubExit(97, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "clipboard not available — ssh web", wantPopup: true, wantTried: "terminal: no tty [2Jgone", wantNot: "\x1b"},
		{name: "tool only (no tty)", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("sh: /dev/tty: Device not configured\n"), Code: 1}}, ui.Osc52Argv("ssh web")...).StubOut("xclip", ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "copied via xclip", wantNot: "unconfirmed"},
		{name: "floor: no tty and no tool", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("sh: /dev/tty: No such device or address\n"), Code: 1}}, ui.Osc52Argv("ssh web")...).StubExit(97, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "clipboard not available — ssh web", wantPopup: true, wantTried: "no clipboard"},
		{name: "floor: every tool found failed, named with its status", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("no tty\n"), Code: 1}}, ui.Osc52Argv("ssh web")...).
				Stub(fakerun.Reply{Result: run.Result{Stdout: []byte("wl-copy exited 1; xclip exited 1"), Code: 98}}, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "clipboard not available — ssh web", wantPopup: true, wantTried: "tried: terminal: no tty; wl-copy exited 1; xclip exited 1"},
		{name: "floor: sh itself cannot run", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Err: errors.New("exec: \"sh\": executable file not found in $PATH")}, ui.Osc52Argv("ssh web")...).
				Stub(fakerun.Reply{Err: errors.New("exec: \"sh\": executable file not found in $PATH")}, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "clipboard not available — ssh web", wantPopup: true, wantTried: "executable file not found"},
		{name: "floor: a tool's own exit 3 is not 'no tool'", stub: func(r *fakerun.Fake) {
			r.Stub(fakerun.Reply{Result: run.Result{Code: 1}}, ui.Osc52Argv("ssh web")...).
				Stub(fakerun.Reply{Result: run.Result{Stdout: []byte("xclip exited 3"), Code: 98}}, ui.ClipToolArgv("ssh web")...)
		}, wantNotice: "clipboard not available — ssh web", wantPopup: true, wantTried: "xclip exited 3"},
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
			if tt.wantNot != "" && (strings.Contains(view, tt.wantNot) || strings.Contains(press(t, m, "esc").View(), tt.wantNot)) {
				t.Errorf("View() carries %q on a tier:\n%s", tt.wantNot, view)
			}
			if tt.wantPopup {
				// The popup shows the value whole and what was tried;
				// closed, the line still carries the value.
				if !strings.Contains(view, "clipboard not available here") || !strings.Contains(view, "  ssh web") || !strings.Contains(view, "tried: ") || !strings.Contains(view, tt.wantTried) {
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
