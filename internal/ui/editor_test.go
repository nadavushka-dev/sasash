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
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestEditorArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		editor []string
		path   string
		want   []string
	}{
		{name: "plain vi", editor: []string{"vi"}, path: "/tmp/x/sasash.conf", want: []string{"vi", "/tmp/x/sasash.conf"}},
		{name: "editor with args", editor: []string{"code", "-w"}, path: "c.conf", want: []string{"code", "-w", "c.conf"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ui.EditorArgv(tt.editor, tt.path); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EditorArgv(%v, %q) = %v, want %v", tt.editor, tt.path, got, tt.want)
			}
		})
	}
}

// editorModel is a probed model with the editor configured.
func editorModel(t *testing.T) ui.Model {
	t.Helper()
	m := probedModel(t, fakerun.New())
	return ui.WithEditor(m, []string{"vi"}, "opened with vi — set $EDITOR to change", "/tmp/x/sasash.conf")
}

func TestEditorExec_ExactArgs(t *testing.T) {
	t.Parallel()
	// The process `e` hands the terminal to must carry the file: an
	// editor opened on nothing would look like it worked.
	got := ui.EditorExec(editorModel(t)).Args
	if want := []string{"vi", "/tmp/x/sasash.conf"}; !reflect.DeepEqual(got, want) {
		t.Errorf("EditorExec().Args = %v, want %v", got, want)
	}
}

func TestModel_Update_EditGuards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		editor   []string
		confPath string
		wantCmd  bool
	}{
		{name: "editor and path launch", editor: []string{"vi"}, confPath: "/tmp/x/sasash.conf", wantCmd: true},
		{name: "no editor is a no-op", editor: nil, confPath: "/tmp/x/sasash.conf"},
		{name: "no path is a no-op", editor: []string{"vi"}, confPath: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := probedModel(t, fakerun.New())
			m = ui.WithEditor(m, tt.editor, "", tt.confPath)
			if _, cmd := m.Update(keyMsg("e")); (cmd != nil) != tt.wantCmd {
				t.Errorf("Update(e) cmd != nil = %v, want %v", cmd != nil, tt.wantCmd)
			}
		})
	}
}

func TestModel_Update_EditWhileFilteringIsText(t *testing.T) {
	t.Parallel()
	m := press(t, editorModel(t), "/", "e")
	if got := ui.FilterState(m); got.String() != "filtering" {
		t.Errorf("FilterState() after e = %v, want still filtering", got)
	}
	// The rune went into the filter, not to the editor.
	if got := ui.FilterText(m); got != "e" {
		t.Errorf("FilterText() = %q, want %q", got, "e")
	}
	if view := m.View(); !strings.Contains(view, "apply filter") {
		t.Errorf("View() lost the filter prompt after e:\n%s", view)
	}
}

func TestModel_Update_EditorExitReloads(t *testing.T) {
	t.Parallel()
	m := editorModel(t)
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
		return []hostmodel.Host{
			{Alias: "handmade", Paths: []probe.Path{{Host: "192.0.2.77"}}},
		}, nil, nil
	})

	next, reload := m.Update(ui.EditorExit(nil))
	m = next.(ui.Model)
	if reload == nil {
		t.Fatalf("Update(editorExit) cmd = nil, want reload cmd")
	}
	m = update(t, m, reload())
	view := m.View()
	// Hand-edits win: the edited config IS the list now.
	if !strings.Contains(view, "handmade") {
		t.Errorf("View() after editor missing the hand-added host:\n%s", view)
	}
	if strings.Contains(view, "web") {
		t.Errorf("View() after editor still shows the pre-edit list:\n%s", view)
	}
	// The $EDITOR-unset hint surfaces on return.
	if !strings.Contains(view, "set $EDITOR") {
		t.Errorf("View() missing the editor hint:\n%s", view)
	}
}

func TestModel_Update_EditorExitVariants(t *testing.T) {
	t.Parallel()
	// A genuine *exec.ExitError, as an editor exiting non-zero after a
	// successful save produces (vi :cq, code -w on window close).
	exitErr := exec.Command("false").Run()
	if exitErr == nil {
		t.Fatalf("exec false: expected non-nil exit error for fixture")
	}
	tests := []struct {
		name       string
		err        error
		wantReload bool
		wantNotice string
	}{
		{name: "clean exit reloads", err: nil, wantReload: true},
		// The slice's headline decision: hand-edits win even when the
		// editor exits non-zero — the edit may already be on disk, and
		// the cost of re-reading is nil while the cost of not doing so
		// is a silently ignored edit.
		{name: "non-zero editor exit still reloads", err: exitErr, wantReload: true},
		{name: "start failure reports without reloading", err: errors.New(`exec: "vi": not found`), wantReload: false, wantNotice: "editor: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loaded := false
			m := editorModel(t)
			m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
				loaded = true
				return nil, nil, nil
			})
			next, cmd := m.Update(ui.EditorExit(tt.err))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("Update(editorExit) cmd = nil, want a follow-up cmd")
			}
			m = update(t, m, cmd()) // reload or re-probe result
			if loaded != tt.wantReload {
				t.Errorf("LoadHosts called = %v, want %v", loaded, tt.wantReload)
			}
			view := m.View()
			if tt.wantNotice != "" && !strings.Contains(view, tt.wantNotice) {
				t.Errorf("View() missing %q:\n%s", tt.wantNotice, view)
			}
			if tt.wantNotice == "" && strings.Contains(view, "editor: ") {
				t.Errorf("View() shows a failure notice on a reload-worthy exit:\n%s", view)
			}
		})
	}
}

func TestModel_Update_EditorWarnOverridesHint(t *testing.T) {
	t.Parallel()
	// setup not run: the edit cannot show in the list — the warning must
	// come back with the editor, outranking the vi hint, and the reload
	// still runs (harmless, and correct once setup is wired).
	loaded := false
	m := editorModel(t)
	m = ui.WithEditorWarn(m, "sasash.conf is not wired into ssh yet — run `sasash setup`")
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
		loaded = true
		return nil, nil, nil
	})
	m = press(t, m, "e") // on a host without a declaring file: sasash.conf
	next, cmd := m.Update(ui.EditorExit(nil))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatalf("Update(editorExit) cmd = nil, want reload cmd")
	}
	m = update(t, m, cmd())
	if !loaded {
		t.Errorf("LoadHosts not called, want reload attempted")
	}
	view := m.View()
	if !strings.Contains(view, "sasash setup") {
		t.Errorf("View() missing the setup warning:\n%s", view)
	}
	if strings.Contains(view, "set $EDITOR") {
		t.Errorf("View() shows the hint the warning should outrank:\n%s", view)
	}
}

// editReload replays the editor returning over a model whose next load
// yields hosts (and warnings), then feeds any filter matches back in the
// way the bubbletea loop would.
func editReload(t *testing.T, m ui.Model, hosts []hostmodel.Host, warnings ...error) ui.Model {
	t.Helper()
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, []error, error) {
		return hosts, warnings, nil
	})
	next, reload := m.Update(ui.EditorExit(nil))
	m = next.(ui.Model)
	if reload == nil {
		t.Fatalf("Update(editorExit) cmd = nil, want reload cmd")
	}
	next, cmd := m.Update(reload())
	m = next.(ui.Model)
	for _, msg := range filterMatches(cmd) {
		m = update(t, m, msg)
	}
	return m
}

func TestModel_Update_EditorReloadKeepsSelection(t *testing.T) {
	t.Parallel()
	// Cursor on the last host; the editor deletes a host. The list shrank
	// under the cursor — a row must still be selected, with its detail
	// pane and $ ssh preview, not a blank right side until j/k.
	m := ui.WithEditor(sized(t, testHosts(3)), []string{"vi"}, "", "/tmp/x/sasash.conf")
	m = press(t, m, "G")
	m = editReload(t, m, testHosts(2))

	host, ok := m.Selected()
	if !ok || host.Alias != "host1" {
		t.Fatalf("Selected() after shrinking reload = %q, %v; want host1, true", host.Alias, ok)
	}
	view := m.View()
	if !strings.Contains(view, "› host1") || !strings.Contains(view, "$ ssh host1") {
		t.Errorf("View() after shrinking reload lost the selection or preview:\n%s", view)
	}
}

func TestModel_Update_EditorReloadKeepsFilteredSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		filter     string
		reload     []hostmodel.Host
		wantAlias  string // "" -> no selection while the filter stands
		afterClear string // selection once esc clears the filter
	}{
		{name: "shrunk match set", filter: "host", reload: testHosts(2), wantAlias: "host1", afterClear: "host1"},
		{name: "only match deleted", filter: "host2", reload: testHosts(2), wantAlias: "", afterClear: "host0"},
		{name: "every match deleted", filter: "host", reload: []hostmodel.Host{{Alias: "other", Paths: []probe.Path{{Host: "192.0.2.9"}}}}, wantAlias: "", afterClear: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := ui.WithEditor(sized(t, testHosts(3)), []string{"vi"}, "", "/tmp/x/sasash.conf")
			m = press(t, m, append(append([]string{"/"}, strings.Split(tt.filter, "")...), "enter", "G")...)
			if got := ui.FilterState(m); got.String() != "filter applied" {
				t.Fatalf("FilterState() = %v, want filter applied", got)
			}
			m = editReload(t, m, tt.reload)

			host, ok := m.Selected()
			if ok != (tt.wantAlias != "") || host.Alias != tt.wantAlias {
				t.Errorf("Selected() under filter after reload = %q, %v; want %q, %v", host.Alias, ok, tt.wantAlias, tt.wantAlias != "")
			}
			if got := ui.FilterState(m); got.String() != "filter applied" {
				t.Errorf("FilterState() after reload = %v, want still applied", got)
			}
			view := m.View()
			if tt.wantAlias == "" && !strings.Contains(view, "no hosts match") {
				t.Errorf("View() with no match left missing the empty-state sentence:\n%s", view)
			}
			if strings.Contains(view, "No items") {
				t.Errorf("View() doubles the empty state with the list's own:\n%s", view)
			}

			// esc clears the filter: the cursor must land on a real row.
			m = press(t, m, "esc")
			host, ok = m.Selected()
			if !ok || host.Alias != tt.afterClear {
				t.Errorf("Selected() after esc = %q, %v; want %q, true", host.Alias, ok, tt.afterClear)
			}
		})
	}
}

func TestModel_Update_EditorReloadShowsWarnings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		warnings []error
		want     string
	}{
		{name: "one warning verbatim", warnings: []error{errors.New(`host "web": @sasash path: missing addr (line 3)`)}, want: `config: host "web": @sasash path: missing addr (line 3)`},
		{name: "more are counted", warnings: []error{errors.New("first"), errors.New("second"), errors.New("third")}, want: "config: first (+2 more)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Under the alt screen the warning is the ONLY feedback a
			// malformed hand-edit gets: it outranks the vi hint.
			m := editReload(t, editorModel(t), testHosts(1), tt.warnings...)
			view := m.View()
			if !strings.Contains(view, tt.want) {
				t.Errorf("View() after an edit with warnings missing %q:\n%s", tt.want, view)
			}
			if strings.Contains(view, "set $EDITOR") {
				t.Errorf("View() shows the hint the warning should outrank:\n%s", view)
			}
		})
	}
}

func TestModel_View_HelpListsEditAndTheme(t *testing.T) {
	t.Parallel()
	// Everything lives in the ? panel (SASASH.4.13, reversing 4.5's
	// omission): e and t are listed there and nowhere on the whisper
	// footer — and both keys still work.
	m := update(t, sized(t, testHosts(2)), tea.WindowSizeMsg{Width: 120, Height: 60})
	if view := m.View(); strings.Contains(view, "edit config") || strings.Contains(view, "theme") {
		t.Errorf("View() whisper footer should name no action key:\n%s", view)
	}
	if view := press(t, m, "?").View(); !strings.Contains(view, "edit config") || !strings.Contains(view, "theme") {
		t.Errorf("View() keys panel should list e and t:\n%s", view)
	}
	if _, cmd := ui.WithEditor(m, []string{"vi"}, "", "/tmp/x/sasash.conf").Update(keyMsg("e")); cmd == nil {
		t.Errorf("Update(e) cmd = nil, want the editor handoff")
	}
	if got := ui.ThemeName(press(t, m, "t")); got == "gruvbox" {
		t.Errorf("t did not cycle the theme")
	}
}
