package ui_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/transfer"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The C key (SASASH.4.33): copy files with scp, both ends browsed. Smoke
// tier: the flow's steps and the messages it sends, never full frames.

// xferHome is a local tree the filepicker browses: a directory first
// (the picker lists directories before files), then a file.
func xferHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"proj/a.txt", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// xferModel is a sized model over host0 with the runner wired for the
// remote listings and the filepicker rooted at home.
func xferModel(t *testing.T, home string, f *fakerun.Fake) ui.Model {
	t.Helper()
	m := ui.New(testHosts(1), probe.Prober{Dialer: fakedial.New(), Timeout: 50 * time.Millisecond}, f, ui.Config{Home: home})
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return ui.StaticCursor(m)
}

// drive presses keys, feeding every message each key's cmd produces
// (a directory read, a remote listing) back into the model, so the
// pickers fill as they would under the real loop.
func drive(t *testing.T, m ui.Model, keys ...string) ui.Model {
	t.Helper()
	for _, k := range keys {
		next, cmd := m.Update(keyMsg(k))
		m = next.(ui.Model)
		for _, msg := range drain(cmd) {
			m = update(t, m, msg)
		}
	}
	return m
}

// homeListing is the stub for host0's login directory.
func homeListing() *fakerun.Fake {
	return fakerun.New().StubOut("docs/\nreadme\n", "ssh", "-o", "BatchMode=yes", "host0", "ls", "-1Ap", "--", ".")
}

func TestModel_Update_CopyOpensDirectionStep(t *testing.T) {
	t.Parallel()
	m := drive(t, xferModel(t, xferHome(t), fakerun.New()), "C")
	if !ui.TransferActive(m) || ui.TransferStep(m) != "direction" {
		t.Fatalf("after C: active = %v, step = %q, want the direction step", ui.TransferActive(m), ui.TransferStep(m))
	}
	view := m.View()
	for _, want := range []string{"copy files (scp)", "host0", "upload", "download", "u/d"} {
		if !strings.Contains(view, want) {
			t.Errorf("direction view missing %q:\n%s", want, view)
		}
	}
	if got := ui.TransferPreview(m); got != nil {
		t.Errorf("TransferPreview() = %q before a direction, want none", got)
	}
}

// Single-letter actions must not fire while the filter is focused (SPEC
// §5 addendum): C is text there.
func TestModel_Update_CopyInertWhileFiltering(t *testing.T) {
	t.Parallel()
	m := press(t, xferModel(t, xferHome(t), fakerun.New()), "/", "C")
	if ui.TransferActive(m) {
		t.Errorf("C opened the copy flow while filtering")
	}
	if got := ui.FilterText(m); got != "C" {
		t.Errorf("FilterText() = %q, want C typed into the filter", got)
	}
}

func TestModel_Update_CopyEscBacksOutOneStep(t *testing.T) {
	t.Parallel()
	m := xferModel(t, xferHome(t), homeListing())
	// direction → esc closes.
	m1 := drive(t, m, "C", "esc")
	if ui.TransferActive(m1) {
		t.Errorf("esc on the direction step left the flow open")
	}
	if !strings.Contains(m1.View(), "copy cancelled") {
		t.Errorf("esc on the direction step: no notice")
	}
	// source picker → esc returns to the direction, the choice kept.
	m2 := drive(t, m, "C", "d", "esc")
	if got := ui.TransferStep(m2); got != "direction" {
		t.Errorf("esc from the source picker: step = %q, want direction", got)
	}
	if !strings.Contains(m2.View(), "› download") {
		t.Errorf("esc from the source picker lost the highlighted direction:\n%s", m2.View())
	}
	// dest picker → esc returns to the source picker, the source forgotten.
	m3 := drive(t, m, "C", "u", "enter", "esc")
	if _, _, picked := ui.TransferSource(m3); picked || ui.TransferStep(m3) != "local" {
		t.Errorf("esc from the dest picker: picked = %v, step = %q, want the local source picker again", picked, ui.TransferStep(m3))
	}
}

func TestModel_Update_UploadDirectoryFlow(t *testing.T) {
	t.Parallel()
	home := xferHome(t)
	f := homeListing()
	m := drive(t, xferModel(t, home, f), "C", "u")
	if got := ui.TransferStep(m); got != "local" {
		t.Fatalf("after u: step = %q, want local", got)
	}
	if got := ui.TransferLocalDir(m); got != home {
		t.Errorf("TransferLocalDir() = %q, want the home %q", got, home)
	}
	if want := []string{"scp", "<local>", "host0:<remote>"}; !reflect.DeepEqual(ui.TransferPreview(m), want) {
		t.Errorf("TransferPreview() = %q, want %q", ui.TransferPreview(m), want)
	}
	if view := m.View(); !strings.Contains(view, "proj") || !strings.Contains(view, "notes.txt") {
		t.Fatalf("local picker did not list the home:\n%s", view)
	}
	// enter on the first row — the directory — picks it as the source.
	m = drive(t, m, "enter")
	src, isDir, picked := ui.TransferSource(m)
	if !picked || !isDir || src != filepath.Join(home, "proj") {
		t.Fatalf("TransferSource() = %q, %v, %v; want proj as a directory", src, isDir, picked)
	}
	if got := ui.TransferStep(m); got != "remote" {
		t.Fatalf("after the source: step = %q, want remote", got)
	}
	if got := f.CallKeys(); !reflect.DeepEqual(got, []string{"ssh -o BatchMode=yes host0 ls -1Ap -- ."}) {
		t.Errorf("CallKeys() = %q, want the home listing", got)
	}
	// The line shows the scp as it stands: -r, the dest still a placeholder.
	if want := []string{"scp", "-r", filepath.Join(home, "proj"), "host0:<remote dir>"}; !reflect.DeepEqual(ui.TransferPreview(m), want) {
		t.Errorf("TransferPreview() = %q, want %q", ui.TransferPreview(m), want)
	}
	if view := m.View(); !strings.Contains(view, "docs/") || !strings.Contains(view, "readme") || !strings.Contains(view, "../") {
		t.Fatalf("remote browser did not list the home:\n%s", view)
	}
	// j to docs/, enter picks it as the dest: the confirm names the scp.
	m = drive(t, m, "j", "enter")
	if !ui.PopupOpen(m) || ui.PopupTitle(m) != "copy files: upload to host0" {
		t.Fatalf("no confirm popup: open = %v, title = %q", ui.PopupOpen(m), ui.PopupTitle(m))
	}
	if want := "scp -r " + filepath.Join(home, "proj") + " host0:docs"; !boxSays(m.View(), want) {
		t.Errorf("confirm popup does not show %q:\n%s", want, m.View())
	}
	// enter runs it: the flow closes and a process cmd comes back.
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if ui.TransferActive(m) || ui.PopupOpen(m) {
		t.Errorf("after the confirm: active = %v, popup = %v, want both closed", ui.TransferActive(m), ui.PopupOpen(m))
	}
	if cmd == nil {
		t.Errorf("confirm returned no cmd, want the scp handoff")
	}
}

func TestModel_Update_DownloadFileFlow(t *testing.T) {
	t.Parallel()
	home := xferHome(t)
	m := drive(t, xferModel(t, home, homeListing()), "C", "d")
	if got := ui.TransferStep(m); got != "remote" {
		t.Fatalf("after d: step = %q, want remote", got)
	}
	// rows: ../, docs/, readme — two down is the file.
	m = drive(t, m, "j", "j", "enter")
	src, isDir, picked := ui.TransferSource(m)
	if !picked || isDir || src != "readme" {
		t.Fatalf("TransferSource() = %q, %v, %v; want readme as a file", src, isDir, picked)
	}
	if got := ui.TransferStep(m); got != "local" {
		t.Fatalf("after the source: step = %q, want local", got)
	}
	if want := []string{"scp", "host0:readme", "<local dir>"}; !reflect.DeepEqual(ui.TransferPreview(m), want) {
		t.Errorf("TransferPreview() = %q, want %q", ui.TransferPreview(m), want)
	}
	// . picks the directory the picker is in.
	m = drive(t, m, ".")
	if !ui.PopupOpen(m) || ui.PopupTitle(m) != "copy files: download from host0" {
		t.Fatalf("no confirm popup: open = %v, title = %q", ui.PopupOpen(m), ui.PopupTitle(m))
	}
	if want := "scp host0:readme " + home; !boxSays(m.View(), want) {
		t.Errorf("confirm popup does not show %q:\n%s", want, m.View())
	}
	// esc on the confirm keeps the picker: nothing ran, the dest can be re-picked.
	m = drive(t, m, "esc")
	if ui.PopupOpen(m) || !ui.TransferActive(m) || ui.TransferStep(m) != "local" {
		t.Errorf("esc on the confirm: popup = %v, active = %v, step = %q; want the local picker back", ui.PopupOpen(m), ui.TransferActive(m), ui.TransferStep(m))
	}
	if !strings.Contains(m.View(), "nothing copied") {
		t.Errorf("esc on the confirm: no notice saying nothing ran")
	}
	// enter on a directory as the dest selects AND descends (bubbles'
	// picker): after a declined confirm the rows are that directory's,
	// the one the path line names, and . picks it.
	m = drive(t, m, "enter", "esc")
	if got := ui.TransferLocalDir(m); got != filepath.Join(home, "proj") {
		t.Fatalf("TransferLocalDir() = %q, want proj after enter on it", got)
	}
	if v := m.View(); !strings.Contains(v, "a.txt") || strings.Contains(v, "notes.txt") {
		t.Errorf("after enter on proj the rows are not proj's:\n%s", v)
	}
	m = drive(t, m, ".")
	if want := "scp host0:readme " + filepath.Join(home, "proj"); !boxSays(m.View(), want) {
		t.Errorf("confirm popup does not show %q:\n%s", want, m.View())
	}
}

// The remote browser descends with l and climbs with h, each a listing
// through the run seam with the exact argv.
func TestModel_Update_RemoteBrowserDescendsAndClimbs(t *testing.T) {
	t.Parallel()
	f := homeListing().StubOut("guide.md\n", "ssh", "-o", "BatchMode=yes", "host0", "ls", "-1Ap", "--", "docs")
	m := drive(t, xferModel(t, xferHome(t), f), "C", "d", "j", "l")
	if got := ui.TransferRemotePath(m); got != "docs" {
		t.Fatalf("after l on docs/: path = %q, want docs", got)
	}
	if !strings.Contains(m.View(), "guide.md") || !strings.Contains(m.View(), "host0:docs") {
		t.Errorf("browser did not show docs:\n%s", m.View())
	}
	// l on a file is nothing; enter on ../ climbs, as h does.
	m = drive(t, m, "j", "l")
	if got := ui.TransferRemotePath(m); got != "docs" {
		t.Errorf("l on a file moved to %q", got)
	}
	m = drive(t, m, "h")
	if got := ui.TransferRemotePath(m); got != "." {
		t.Errorf("after h: path = %q, want .", got)
	}
	m = drive(t, m, "j", "l", "g", "enter")
	if got := ui.TransferRemotePath(m); got != "." {
		t.Errorf("after enter on ../: path = %q, want .", got)
	}
	want := []string{"ssh -o BatchMode=yes host0 ls -1Ap -- .", "ssh -o BatchMode=yes host0 ls -1Ap -- docs", "ssh -o BatchMode=yes host0 ls -1Ap -- .", "ssh -o BatchMode=yes host0 ls -1Ap -- docs", "ssh -o BatchMode=yes host0 ls -1Ap -- ."}
	if got := f.CallKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("CallKeys() = %q, want %q", got, want)
	}
	// Vim motion over the rows: G lands on the last, ctrl+u back on the first.
	m = drive(t, m, "G")
	if !strings.Contains(m.View(), "› readme") {
		t.Errorf("G did not reach the last row:\n%s", m.View())
	}
	m = drive(t, m, "ctrl+u")
	if !strings.Contains(m.View(), "› ../") {
		t.Errorf("ctrl+u did not reach the first row:\n%s", m.View())
	}
}

// A listing the host refuses is a notice with ls's own words; the
// browser stays where it was (SPEC §12: a non-zero exit is data).
func TestModel_Update_RemoteRefusalIsANotice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reply fakerun.Reply
		want  string
	}{
		{name: "permission denied", reply: fakerun.Reply{Result: runResult(2, "ls: cannot open directory 'docs': Permission denied\n")}, want: "host0:docs — ls: cannot open directory 'docs': Permission denied"},
		{name: "ssh could not run", reply: fakerun.Reply{Err: errors.New("exec: \"ssh\": executable file not found")}, want: "host0:docs — exec: \"ssh\": executable file not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := homeListing().Stub(tt.reply, "ssh", "-o", "BatchMode=yes", "host0", "ls", "-1Ap", "--", "docs")
			m := drive(t, xferModel(t, xferHome(t), f), "C", "d", "j", "l")
			if !ui.TransferActive(m) || ui.TransferRemotePath(m) != "." {
				t.Errorf("after the refusal: active = %v, path = %q; want the browser still at .", ui.TransferActive(m), ui.TransferRemotePath(m))
			}
			if !strings.Contains(m.View(), tt.want) {
				t.Errorf("view lacks the notice %q:\n%s", tt.want, m.View())
			}
		})
	}
}

// The dest is a directory: a file picked there is refused with the way
// to pick one.
func TestModel_Update_DestMustBeADirectory(t *testing.T) {
	t.Parallel()
	// Download: readme picked, then enter on notes.txt (the second local row).
	m := drive(t, xferModel(t, xferHome(t), homeListing()), "C", "d", "j", "j", "enter", "j", "enter")
	if ui.PopupOpen(m) || !ui.TransferActive(m) {
		t.Errorf("a file as dest: popup = %v, active = %v; want refused, flow open", ui.PopupOpen(m), ui.TransferActive(m))
	}
	if !strings.Contains(m.View(), "the destination is a directory") {
		t.Errorf("no refusal notice:\n%s", m.View())
	}
	// Upload: proj picked, then enter on readme in the remote browser.
	m = drive(t, xferModel(t, xferHome(t), homeListing()), "C", "u", "enter", "j", "j", "enter")
	if ui.PopupOpen(m) || !strings.Contains(m.View(), "the destination is a directory") {
		t.Errorf("a remote file as dest was not refused:\n%s", m.View())
	}
}

func TestModel_Update_ScpReturn(t *testing.T) {
	t.Parallel()
	exitErr := exec.Command("sh", "-c", "exit 1").Run()
	if exitErr == nil {
		t.Fatalf("sh -c 'exit 1': expected a non-nil exit error for the fixture")
	}
	spec := transfer.Spec{Alias: "host0", Direction: transfer.Upload, Local: "/home/u/proj", Remote: "docs", Recursive: true}
	tests := []struct {
		name       string
		err        error
		stderr     string
		wantNotice string
		wantPopup  string
	}{
		{name: "clean exit", wantNotice: "copied /home/u/proj → host0:docs"},
		{name: "non-zero exit with scp's words", err: exitErr, stderr: "scp: docs: Permission denied\n", wantNotice: "scp: scp: docs: Permission denied", wantPopup: "scp exited 1"},
		{name: "non-zero exit, silent", err: exitErr, wantNotice: "scp: exit status 1", wantPopup: "scp exited 1"},
		{name: "start failure", err: errors.New(`exec: "scp": executable file not found`), wantNotice: `scp: exec: "scp": executable file not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := update(t, xferModel(t, xferHome(t), fakerun.New()), ui.ScpExit(spec, tt.err, tt.stderr))
			if ui.TransferActive(m) {
				t.Errorf("flow open after scp returned")
			}
			if !strings.Contains(m.View(), tt.wantNotice) {
				t.Errorf("view lacks the notice %q:\n%s", tt.wantNotice, m.View())
			}
			if got := ui.PopupTitle(m); got != tt.wantPopup {
				t.Errorf("PopupTitle() = %q, want %q", got, tt.wantPopup)
			}
			if tt.wantPopup != "" && !strings.Contains(m.View(), "scp -r /home/u/proj host0:docs") {
				t.Errorf("failure popup does not name the argv:\n%s", m.View())
			}
		})
	}
}

// The process the confirm builds carries the exact argv, and its stderr
// is captured for the outcome (stdout stays the terminal's: scp's
// progress meter writes there).
func TestScpExec_ArgsAndStderrCapture(t *testing.T) {
	t.Parallel()
	spec := transfer.Spec{Alias: "host0", Direction: transfer.Download, Local: "/home/u", Remote: "etc/nginx", Recursive: true}
	cmd := ui.ScpExec(spec)
	if want := []string{"scp", "-r", "host0:etc/nginx", "/home/u"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("Args = %q, want %q", cmd.Args, want)
	}
	if cmd.Stderr == nil {
		t.Errorf("Stderr = nil, want a capture buffer")
	}
	if cmd.Stdout != nil || cmd.Stdin != nil {
		t.Errorf("Stdout/Stdin set (%v/%v), want nil so tea hands them the terminal", cmd.Stdout, cmd.Stdin)
	}
}

// Every step renders inside the terminal's width (the frame clamp), and
// the footer says what the keys do there.
func TestModel_View_TransferSmoke(t *testing.T) {
	t.Parallel()
	base := xferModel(t, xferHome(t), homeListing())
	steps := []struct {
		name string
		keys []string
		hint string
	}{
		{name: "direction", keys: []string{"C"}, hint: "upload / download"},
		{name: "local picker", keys: []string{"C", "u"}, hint: "this dir"},
		{name: "remote browser", keys: []string{"C", "d"}, hint: "this dir"},
		{name: "confirm", keys: []string{"C", "u", "enter", "."}, hint: "copy"},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			t.Parallel()
			v := drive(t, base, st.keys...).View()
			for i, line := range strings.Split(v, "\n") {
				if w := lipgloss.Width(line); w > 80 {
					t.Errorf("line %d is %d cells wide, want ≤ 80: %q", i, w, line)
				}
			}
			if !strings.Contains(v, st.hint) {
				t.Errorf("footer lacks %q:\n%s", st.hint, v)
			}
		})
	}
	t.Run("resize follows", func(t *testing.T) {
		t.Parallel()
		m := update(t, drive(t, base, "C", "u"), tea.WindowSizeMsg{Width: 60, Height: 12})
		for i, line := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(line); w > 60 {
				t.Errorf("after resize, line %d is %d cells wide, want ≤ 60: %q", i, w, line)
			}
		}
	})
}

// boxSays reports whether the popup's rows carry want — a long argv
// wraps inside the box, so the comparison ignores spacing.
func boxSays(view, want string) bool {
	squash := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	return strings.Contains(squash(strings.Join(boxLines(view), "")), squash(want))
}

// The flow owns the keys: ? is inert inside it, as in a prompt.
func TestModel_Update_HelpInertInsideFlow(t *testing.T) {
	t.Parallel()
	m := update(t, xferModel(t, xferHome(t), fakerun.New()), tea.WindowSizeMsg{Width: 120, Height: 100})
	m = drive(t, m, "C", "?")
	if ui.HelpOpen(m) {
		t.Errorf("? opened the panel from inside the copy flow")
	}
}

func runResult(code int, stderr string) run.Result {
	return run.Result{Code: code, Stderr: []byte(stderr)}
}

// A resize while the local picker is scrolled keeps its rows on screen:
// bubbles' filepicker answers a WindowSizeMsg by resetting its window end
// below a scrolled start, which blanks every row, so the resize stays
// with transferResize and never reaches the picker.
func TestModel_Update_LocalPickerSurvivesResizeWhileScrolled(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for i := 0; i < 40; i++ {
		if err := os.WriteFile(filepath.Join(home, fmt.Sprintf("file-%02d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := update(t, xferModel(t, home, fakerun.New()), tea.WindowSizeMsg{Width: 80, Height: 40})
	m = drive(t, m, "C", "u", "G")
	if !strings.Contains(m.View(), "› ") || !strings.Contains(m.View(), "file-39.txt") {
		t.Fatalf("G did not reach the last row:\n%s", m.View())
	}
	m = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 14})
	if v := m.View(); !strings.Contains(v, "file-") {
		t.Errorf("after shrinking, the picker lists no rows:\n%s", v)
	}
}

// A listing that lands after the browser moved on — another host, or
// another directory — is dropped, so enter can never copy a path the
// user did not pick.
func TestModel_Update_StaleListingIsDropped(t *testing.T) {
	t.Parallel()
	f := homeListing().
		StubOut("guide.md\n", "ssh", "-o", "BatchMode=yes", "host0", "ls", "-1Ap", "--", "docs").
		StubOut("other/\n", "ssh", "-o", "BatchMode=yes", "host1", "ls", "-1Ap", "--", ".")
	m := ui.New(testHosts(2), probe.Prober{Dialer: fakedial.New(), Timeout: 50 * time.Millisecond}, f, ui.Config{Home: xferHome(t)})
	m = ui.StaticCursor(update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24}))
	// host0's listing in flight, then out, over to host1, in again.
	next, pending := m.Update(keyMsg("C"))
	next, pending = next.(ui.Model).Update(keyMsg("d"))
	stale := drain(pending)
	m = drive(t, next.(ui.Model), "esc", "esc", "j", "C", "d")
	if got := ui.TransferRemotePath(m); got != "." || !strings.Contains(m.View(), "other/") {
		t.Fatalf("host1's browser: path = %q, view:\n%s", got, m.View())
	}
	for _, msg := range stale {
		m = update(t, m, msg)
	}
	if v := m.View(); strings.Contains(v, "docs/") || !strings.Contains(v, "other/") {
		t.Errorf("host0's stale listing replaced host1's rows:\n%s", v)
	}
	// The race the alias check alone catches: both homes are ".", so
	// host0's listing landing while host1's own is still in flight is
	// told apart by the alias, nothing else.
	m = ui.New(testHosts(2), probe.Prober{Dialer: fakedial.New(), Timeout: 50 * time.Millisecond}, f, ui.Config{Home: xferHome(t)})
	m = ui.StaticCursor(update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24}))
	next, pending = m.Update(keyMsg("C"))
	next, pending = next.(ui.Model).Update(keyMsg("d"))
	stale = drain(pending)
	m = drive(t, next.(ui.Model), "esc", "esc", "j")
	next, _ = m.Update(keyMsg("C"))
	next, _ = next.(ui.Model).Update(keyMsg("d")) // host1's listing left in flight
	m = next.(ui.Model)
	for _, msg := range stale {
		m = update(t, m, msg)
	}
	if v := m.View(); strings.Contains(v, "docs/") || strings.Contains(v, "readme") {
		t.Errorf("host0's listing landed in host1's browser while its own was in flight:\n%s", v)
	}
	// Two moves in a row: the first's listing lands after the second was asked for.
	m2 := drive(t, xferModel(t, xferHome(t), f), "C", "d", "j")
	next, pending = m2.Update(keyMsg("l")) // docs, in flight
	late := drain(pending)
	m2 = drive(t, next.(ui.Model), "h") // back to ., asked for after
	for _, msg := range late {
		m2 = update(t, m2, msg)
	}
	if got := ui.TransferRemotePath(m2); got != "." || strings.Contains(m2.View(), "guide.md") {
		t.Errorf("the late docs listing moved the browser: path = %q, view:\n%s", got, m2.View())
	}
}

// Under BatchMode a host that needs to ask (passphrase, password, host
// key) fails with ssh's 255: the notice points at S, the one connect
// that opens a master for the listing to ride.
func TestModel_Update_RemoteRefusalByConnectionHintsAtMaster(t *testing.T) {
	t.Parallel()
	f := fakerun.New().Stub(fakerun.Reply{Result: runResult(255, "host0: Permission denied (publickey).\n")}, "ssh", "-o", "BatchMode=yes", "host0", "ls", "-1Ap", "--", ".")
	m := drive(t, xferModel(t, xferHome(t), f), "C", "d")
	if got := ui.PopupTitle(m); got != "could not list host0:." {
		t.Fatalf("PopupTitle() = %q, want the refusal popup", got)
	}
	for _, want := range []string{"ssh -o BatchMode=yes host0 ls -1Ap -- .", "Permission denied (publickey)", "connect once with S"} {
		if !boxSays(m.View(), want) {
			t.Errorf("popup lacks %q:\n%s", want, m.View())
		}
	}
	// esc leaves the outcome on the line, the browser still open at .
	m = drive(t, m, "esc")
	if want := "host0: host0: Permission denied (publickey). — S once (a master), then C"; !strings.Contains(m.View(), want) {
		t.Errorf("view lacks the notice %q:\n%s", want, m.View())
	}
	if !ui.TransferActive(m) || ui.TransferRemotePath(m) != "." {
		t.Errorf("after the refusal: active = %v, path = %q", ui.TransferActive(m), ui.TransferRemotePath(m))
	}
}

// Action keys are inert inside the flow (SPEC §5): the theme, quit,
// settings, the path picker and the ambient toggle all stay put.
func TestModel_Update_ActionKeysInertInsideFlow(t *testing.T) {
	t.Parallel()
	for _, step := range []struct {
		name string
		keys []string
	}{
		{name: "local picker", keys: []string{"C", "u"}},
		{name: "remote browser", keys: []string{"C", "d"}},
	} {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			m := drive(t, xferModel(t, xferHome(t), homeListing()), step.keys...)
			step := ui.TransferStep(m)
			for _, k := range []string{"t", "q", "o", "p", "?", "$", "R", "a"} {
				next, cmd := m.Update(keyMsg(k))
				n := next.(ui.Model)
				if !ui.TransferActive(n) || ui.TransferStep(n) != step || ui.PopupOpen(n) || ui.ThemeName(n) != "gruvbox" {
					t.Errorf("%q inside the flow: active = %v, step = %q, popup = %v, theme = %q", k, ui.TransferActive(n), ui.TransferStep(n), ui.PopupOpen(n), ui.ThemeName(n))
				}
				for _, msg := range drain(cmd) {
					if _, quit := msg.(tea.QuitMsg); quit {
						t.Errorf("%q inside the flow quit the program", k)
					}
				}
			}
		})
	}
}

// tab/shift+tab move the remote browser's highlight like j/k (the pane
// convention); tab never opens a directory.
func TestModel_Update_RemoteBrowserTabMoves(t *testing.T) {
	t.Parallel()
	m := drive(t, xferModel(t, xferHome(t), homeListing()), "C", "d", "tab")
	if !strings.Contains(m.View(), "› docs/") || ui.TransferRemotePath(m) != "." {
		t.Errorf("tab: want the highlight on docs/ at .:\n%s", m.View())
	}
	m = drive(t, m, "shift+tab")
	if !strings.Contains(m.View(), "› ../") {
		t.Errorf("shift+tab: want the highlight back on ../:\n%s", m.View())
	}
}
