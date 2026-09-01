package ui_test

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

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

func TestModel_Update_EditLaunchesEditor(t *testing.T) {
	t.Parallel()
	m := editorModel(t)
	_, cmd := m.Update(keyMsg("e"))
	if cmd == nil {
		t.Errorf("Update(e) cmd = nil, want editor handoff cmd")
	}

	// Without an editor configured the key is a no-op.
	plain := probedModel(t, fakerun.New())
	if _, cmd := plain.Update(keyMsg("e")); cmd != nil {
		t.Errorf("Update(e) without an editor cmd = %v, want nil", cmd)
	}
}

func TestModel_Update_EditorExitReloads(t *testing.T) {
	t.Parallel()
	m := editorModel(t)
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, error) {
		return []hostmodel.Host{
			{Alias: "handmade", Paths: []probe.Path{{Host: "192.0.2.77"}}},
		}, nil
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
			m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, error) {
				loaded = true
				return nil, nil
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

func TestModel_Update_EditorGuards(t *testing.T) {
	t.Parallel()
	// Empty ConfPath disables the key even with an editor set.
	m := probedModel(t, fakerun.New())
	m = ui.WithEditor(m, []string{"vi"}, "", "")
	if _, cmd := m.Update(keyMsg("e")); cmd != nil {
		t.Errorf("Update(e) with empty ConfPath cmd = %v, want nil", cmd)
	}

	// While filtering, e is typed text even with an editor configured.
	m2 := editorModel(t)
	m2 = press(t, m2, "/")
	m3, cmd := m2.Update(keyMsg("e"))
	m2 = m3.(ui.Model)
	if got := ui.FilterState(m2); got.String() != "filtering" {
		t.Errorf("FilterState() after e = %v, want still filtering", got)
	}
	// Any cmd here is filter machinery, never a handoff: the filter must
	// now contain the rune.
	_ = cmd
	if view := m2.View(); !strings.Contains(view, "apply filter") {
		t.Errorf("View() lost the filter prompt after e:\n%s", view)
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
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, error) {
		loaded = true
		return nil, nil
	})
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
