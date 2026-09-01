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

func TestClipArgv_Argv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "address",
			text: "192.0.2.10",
			want: []string{"sh", "-c", `printf %s "$1" | pbcopy`, "sh", "192.0.2.10"},
		},
		{
			name: "command with spaces travels as one argument",
			text: "ssh ubuntu",
			want: []string{"sh", "-c", `printf %s "$1" | pbcopy`, "sh", "ssh ubuntu"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.ClipArgv(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ClipArgv(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
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
	wantArgv := ui.ClipArgv("198.51.100.5")
	runner := fakerun.New().StubExit(0, wantArgv...)
	m := probedModel(t, runner)

	_, cmd := m.Update(keyMsg("y"))
	if cmd == nil {
		t.Fatalf("Update(y) cmd = nil, want clipboard cmd")
	}
	done := cmd()
	if got := runner.Calls(); !reflect.DeepEqual(got, [][]string{wantArgv}) {
		t.Errorf("Calls() = %v, want %v", got, [][]string{wantArgv})
	}
	m = update(t, m, done)
	if view := m.View(); strings.Contains(view, "copy failed") {
		t.Errorf("View() after successful copy shows failure notice:\n%s", view)
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
	wantArgv := ui.ClipArgv("ssh web")
	runner := fakerun.New().StubExit(0, wantArgv...)
	m := probedModel(t, runner)

	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatalf("Update(c) cmd = nil, want clipboard cmd")
	}
	cmd()
	if got := runner.Calls(); !reflect.DeepEqual(got, [][]string{wantArgv}) {
		t.Errorf("Calls() = %v, want %v", got, [][]string{wantArgv})
	}
}

func TestModel_Update_CopyFailure(t *testing.T) {
	t.Parallel()
	runner := fakerun.New().StubExit(1, ui.ClipArgv("ssh web")...)
	m := probedModel(t, runner)

	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil {
		t.Fatalf("Update(c) cmd = nil, want clipboard cmd")
	}
	m = update(t, m, cmd())
	if view := m.View(); !strings.Contains(view, "copy failed") {
		t.Errorf("View() = %q, want a copy-failed notice", view)
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
