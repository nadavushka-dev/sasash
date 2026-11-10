package ui_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// Reverse forwards in the pane (SASASH.4.41): an `R…` row toggles like any
// other, is marked as reverse with a glyph, and never meets a local-port
// conflict check.

const (
	specRev = "R2222:localhost:22"
	// specRevBind names a remote bind address: read as -L, its second
	// field would pass for local port 2222 — the mangling the pane must
	// never do.
	specRevBind = "Rlocalhost:2222:localhost:22"
)

// reverseModel is a polled model over a managed ubuntu declaring one
// reverse set, with a foreign listener on local 2222 — which must not
// matter to the reverse row.
func reverseModel(t *testing.T, saveEdit func(alias, set, old, new string) error) (ui.Model, *fakespawn.Fake, *fakedial.Fake) {
	t.Helper()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:2222", time.Millisecond)
	sp := fakespawn.New().Dial(d)
	hosts := []hostmodel.Host{{Alias: "ubuntu", HasMeta: true, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}},
		Forwards: []hostmodel.ForwardSet{{Name: "mac", Specs: []string{specRev}}}}}
	eng := tunnel.Engine{Runner: r, Spawner: sp, Dialer: d, StateDir: t.TempDir(), Timeout: 10 * time.Millisecond}
	m := ui.New(hosts, probe.Prober{Dialer: d, Timeout: 50 * time.Millisecond}, r, ui.Config{Tunnels: eng, SaveForwardEdit: saveEdit})
	m = ui.WithPreviewShown(update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}))
	return feed(t, m, m.Init()), sp, d
}

func TestModel_Update_ReverseRowSpaceUpDown(t *testing.T) {
	t.Parallel()
	m, sp, _ := reverseModel(t, nil)
	view := m.View()
	if !strings.Contains(view, "○ mac "+specRev+" down") || !strings.Contains(view, "⇐ remote :2222") {
		t.Fatalf("a reverse row should read down (never listening) and be marked remote:\n%s", view)
	}
	m = press(t, m, "l")
	if view := m.View(); !strings.Contains(view, spawnArgv("ubuntu", specRev)) || !strings.Contains(view, "-R 2222:localhost:22") {
		t.Errorf("the preview should be the -R spawn:\n%s", view)
	}
	space := func(m ui.Model) ui.Model {
		t.Helper()
		next, cmd := m.Update(keyMsg(" "))
		if cmd == nil {
			t.Fatalf("Update(space) cmd = nil, want action cmd")
		}
		next, refresh := next.(ui.Model).Update(cmd())
		return feed(t, next.(ui.Model), refresh)
	}
	m = space(m)
	if got, want := sp.Spawned(), []string{spawnArgv("ubuntu", specRev)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("space spawned = %v, want %v", got, want)
	}
	if view := m.View(); !strings.Contains(view, "› ● mac "+specRev+" up") {
		t.Fatalf("after space the reverse row should be up:\n%s", view)
	}
	m = space(m)
	if got := sp.Killed(); !reflect.DeepEqual(got, []int{1000}) {
		t.Errorf("second space killed = %v, want [1000]", got)
	}
	if view := m.View(); !strings.Contains(view, "› ○ mac "+specRev+" down") {
		t.Errorf("after the second space the reverse row should be down:\n%s", view)
	}
}

func TestModel_Update_ReverseRowEdit(t *testing.T) {
	t.Parallel()
	var edits [][]string
	m, _, _ := reverseModel(t, func(alias, set, old, new string) error {
		edits = append(edits, []string{alias, set, old, new})
		return nil
	})
	m = press(t, m, "l", "E")
	if got := ui.PromptValue(m); got != specRev {
		t.Fatalf("E prefill = %q, want %q", got, specRev)
	}
	m = press(t, m, "backspace")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	next, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Fatalf("Update(enter) cmd = nil, want the edit cmd:\n%s", next.(ui.Model).View())
	}
	_ = update(t, next.(ui.Model), cmd())
	if want := [][]string{{"ubuntu", "mac", specRev, "R2222:localhost:23"}}; !reflect.DeepEqual(edits, want) {
		t.Errorf("saveForwardEdit = %v, want %v", edits, want)
	}
}

// + with a reverse spec: no local-port dial (a foreign listener on 2222
// is irrelevant), the set name defaults to R<remote port>, declared and
// spawned as -R.
func TestModel_Update_AddReverseForward(t *testing.T) {
	t.Parallel()
	d := fakedial.New().Up("127.0.0.1:2222", time.Millisecond)
	var saved [][]string
	m, sp := managedForwardModel(t, d, &saved)
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(specRev)})
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if !ui.Prompting(m) || !strings.Contains(m.View(), "name the set on ubuntu") {
		t.Fatalf("a reverse add should reach the set-name prompt, no conflict:\n%s", m.View())
	}
	if got := ui.PromptValue(m); got != "R2222" {
		t.Fatalf("default set name = %q, want R2222", got)
	}
	next, cmd = m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if want := [][]string{{"ubuntu", "R2222", specRev}}; !reflect.DeepEqual(saved, want) {
		t.Errorf("saveForwardSet = %v, want %v", saved, want)
	}
	if got, want := sp.Spawned(), []string{spawnArgv("ubuntu", specRev)}; !reflect.DeepEqual(got, want) {
		t.Errorf("spawned = %v, want %v", got, want)
	}
	for _, addr := range d.Calls() {
		if strings.HasPrefix(addr, "127.0.0.1:") {
			t.Errorf("dialled %s, want no local-port check for a reverse spec", addr)
		}
	}
	if view := m.View(); !strings.Contains(view, "persisted") {
		t.Errorf("the notice should say it persisted:\n%s", view)
	}
}

// A mixed set's held-port refusal names only the -L ports: the reverse
// spec's number is held locally too, and that is no conflict.
func TestModel_Update_AddMixedSetConflictSkipsReverse(t *testing.T) {
	t.Parallel()
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond).Up("127.0.0.1:2222", time.Millisecond)
	var saved [][]string
	m, sp := managedForwardModel(t, d, &saved)
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3000 " + specRevBind)})
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	view := m.View()
	if !strings.Contains(view, "port 3000") || strings.Contains(view, "port 2222") {
		t.Errorf("the refusal should name port 3000 and not the reverse 2222:\n%s", view)
	}
	if len(saved) != 0 || len(sp.Spawned()) != 0 {
		t.Errorf("saved %v, spawned %v; want nothing (all-or-nothing)", saved, sp.Spawned())
	}

	// With 3000 free, the same mixed set goes through whole.
	d2 := fakedial.New().Up("127.0.0.1:2222", time.Millisecond)
	var saved2 [][]string
	m2, sp2 := managedForwardModel(t, d2, &saved2)
	m2 = press(t, m2, "+")
	m2 = update(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3000 " + specRevBind)})
	next, cmd = m2.Update(keyMsg("enter"))
	m2 = update(t, next.(ui.Model), cmd())
	if got := ui.PromptValue(m2); got != "3000" {
		t.Fatalf("default set name = %q, want the first local port 3000:\n%s", got, m2.View())
	}
	next, cmd = m2.Update(keyMsg("enter"))
	_ = update(t, next.(ui.Model), cmd())
	if want := [][]string{{"ubuntu", "3000", "3000:localhost:3000", specRevBind}}; !reflect.DeepEqual(saved2, want) {
		t.Errorf("saveForwardSet = %v, want %v", saved2, want)
	}
	if got, want := sp2.Spawned(), []string{spawnArgv("ubuntu", "3000:localhost:3000"), spawnArgv("ubuntu", specRevBind)}; !reflect.DeepEqual(got, want) {
		t.Errorf("spawned = %v, want %v", got, want)
	}
}

func TestModel_Update_AddReverseRefusesMalformed(t *testing.T) {
	t.Parallel()
	var saved [][]string
	m, sp := managedForwardModel(t, fakedial.New(), &saved)
	m = press(t, m, "+")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Rbox")})
	next, _ := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if view := m.View(); !strings.Contains(view, `spec "Rbox": no remote port`) {
		t.Errorf("a malformed reverse spec should be refused by name:\n%s", view)
	}
	if !ui.Prompting(m) || len(saved) != 0 || len(sp.Spawned()) != 0 {
		t.Errorf("a refused spec should keep the prompt and add nothing")
	}
}

// `a` does not take reverse specs: an R… value after -L is refused, never
// quietly turned into a -R by the engine.
func TestParseAdhocArgv_ReverseRefused(t *testing.T) {
	t.Parallel()
	if _, err := ui.ParseAdhocArgv("ubuntu -L " + specRev); err == nil || !strings.Contains(err.Error(), "reverse spec") {
		t.Errorf("ParseAdhocArgv(-L %s) err = %v, want the reverse refusal", specRev, err)
	}
}
