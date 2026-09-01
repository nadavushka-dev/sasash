package ui_test

import (
	"errors"
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
	// A start failure (no such editor) reports and does not reload.
	loaded := false
	m := editorModel(t)
	m = ui.WithLoadHosts(m, func() ([]hostmodel.Host, error) {
		loaded = true
		return nil, nil
	})
	next, cmd := m.Update(ui.EditorExit(errors.New(`exec: "vi": not found`)))
	m = next.(ui.Model)
	if cmd == nil {
		t.Errorf("Update(editorExit err) cmd = nil, want re-probe")
	}
	if loaded {
		t.Errorf("LoadHosts called on editor start failure, want no reload")
	}
	if view := m.View(); !strings.Contains(view, "editor: ") {
		t.Errorf("View() missing the editor failure notice:\n%s", view)
	}
}
