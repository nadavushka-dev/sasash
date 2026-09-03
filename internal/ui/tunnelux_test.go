package ui_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestParseAdhocTunnelArgv_ExactStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		want       []string // the -fN argv
		tunnelOnly bool
		wantErr    string
	}{
		{name: "-N before the target", input: "-N -L 8080:localhost:80 root@192.0.2.99", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "8080:localhost:80", "root@192.0.2.99"}, tunnelOnly: true},
		{name: "-N after the target", input: "192.0.2.99 -N", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "192.0.2.99"}, tunnelOnly: true},
		{name: "pasted ssh command with -p", input: "ssh -L 8080:localhost:80 -p 2222 root@192.0.2.99", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "8080:localhost:80", "-p", "2222", "root@192.0.2.99"}},
		{name: "pasted -N -p glued", input: "ssh -N -p2222 -L5432:127.0.0.1:5432 db.invalid", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "5432:127.0.0.1:5432", "-p", "2222", "db.invalid"}, tunnelOnly: true},
		{name: "same port both ways", input: "192.0.2.99:2222 -p 2222", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "-p", "2222", "192.0.2.99"}},
		{name: "-N twice is once", input: "-N h -N", want: []string{"ssh", "-fN", "-o", "BatchMode=yes", "h"}, tunnelOnly: true},
		{name: "port given twice differently", input: "192.0.2.99:2222 -p 2223", wantErr: "port given twice"},
		{name: "bad -p", input: "-p x h", wantErr: "bad port"},
		{name: "dangling -p", input: "h -p", wantErr: "-p needs a value"},
		{name: "two targets", input: "h1 h2", wantErr: `unexpected "h2" after the target "h1"`},
		{name: "ssh alone", input: "ssh", wantErr: "type [user@]host"},
		{name: "unknown option", input: "ssh -T h", wantErr: `unexpected "-T"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, tunnelOnly, err := ui.ParseAdhocTunnelArgv(tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ParseAdhocTunnelArgv(%q) err = %v, want containing %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAdhocTunnelArgv(%q) = %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) || tunnelOnly != tt.tunnelOnly {
				t.Errorf("ParseAdhocTunnelArgv(%q) = %v, %v; want %v, %v", tt.input, got, tunnelOnly, tt.want, tt.tunnelOnly)
			}
		})
	}
	// The interactive argv drops only the -fN: a pasted command connects
	// exactly as pasted.
	if got, err := ui.ParseAdhocArgv("ssh -p 2222 root@192.0.2.99"); err != nil || !reflect.DeepEqual(got, []string{"ssh", "-p", "2222", "root@192.0.2.99"}) {
		t.Errorf("ParseAdhocArgv(pasted) = %v, %v", got, err)
	}
}

func TestModel_Update_AdhocTunnelOnly(t *testing.T) {
	t.Parallel()
	const input = "-N -L 8080:localhost:80 root@192.0.2.99"
	argv := []string{"ssh", "-fN", "-o", "BatchMode=yes", "-L", "8080:localhost:80", "root@192.0.2.99"}
	tests := []struct {
		name      string
		reply     fakerun.Reply
		wantNote  []string
		wantOffer bool
	}{
		{name: "up in the background", reply: fakerun.Reply{Result: run.Result{Code: 0}}, wantNote: []string{"tunnel up in the background"}, wantOffer: true},
		{name: "auth needed a prompt", reply: fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("root@192.0.2.99: Permission denied (publickey,password).\n")}}, wantNote: []string{"ssh -fN failed: Permission denied (publickey,password).", "keys/agent", "connect once"}, wantOffer: true},
		{name: "ssh never ran", reply: fakerun.Reply{Err: errors.New("no ssh binary")}, wantNote: []string{"ssh: no ssh binary"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().Stub(tt.reply, argv...)
			m := ui.WithSaveHost(probedModel(t, r), func(ui.AdhocEntry) error { return nil })
			m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24}) // the reason plus the hint plus the offer
			m = press(t, m, "a")
			m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(input)})
			next, cmd := m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if cmd == nil || ui.Prompting(m) {
				t.Fatalf("enter: cmd %v, prompting %v; want the background cmd and the prompt closed", cmd != nil, ui.Prompting(m))
			}
			if view := m.View(); !strings.Contains(view, "starting ssh -fN") {
				t.Errorf("View() while starting missing the command:\n%s", view)
			}
			// No handoff: the cmd runs ssh through the runner and returns a
			// message the model handles — the TUI never left the screen.
			msg := cmd()
			if got, want := r.CallKeys(), []string{fakerun.Key(argv...)}; !reflect.DeepEqual(got, want) {
				t.Fatalf("runner calls = %v, want %v", got, want)
			}
			m = update(t, m, msg)
			view := m.View()
			for _, w := range tt.wantNote {
				if !strings.Contains(view, w) {
					t.Errorf("View() missing %q:\n%s", w, view)
				}
			}
			if got := ui.PromptingSave(m); got != tt.wantOffer {
				t.Errorf("PromptingSave() = %v, want %v", got, tt.wantOffer)
			}
			if !tt.wantOffer {
				return
			}
			// The save offer is the ordinary one: the entry carries the
			// forwards and the host exactly as typed.
			m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("edge")})
			var saved []ui.AdhocEntry
			m = ui.WithSaveHost(m, func(e ui.AdhocEntry) error { saved = append(saved, e); return nil })
			next, save := m.Update(keyMsg("enter"))
			update(t, next.(ui.Model), save())
			want := []ui.AdhocEntry{{Alias: "edge", User: "root", Host: "192.0.2.99", Forwards: []string{"8080:localhost:80"}}}
			if !reflect.DeepEqual(saved, want) {
				t.Errorf("saved = %+v, want %+v", saved, want)
			}
		})
	}
}

// editModel is the tunnels fixture with the edit hook recording calls:
// master alive, web's first port up, everything else down.
func editModel(t *testing.T, r *fakerun.Fake, calls *[][]string, hookErr error) ui.Model {
	t.Helper()
	r.StubExit(0, "ssh", "-O", "check", "ubuntu")
	// The cache records web's second spec: taking the whole set down
	// would cancel it too (unstubbed), so only per-spec surgery passes.
	stateDir := t.TempDir()
	state := `{"forwards":[{"set":"web","spec":"` + specWeb1 + `"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := tunnelModel(t, r, fakedial.New().Up("127.0.0.1:3000", time.Millisecond), stateDir)
	m = feed(t, m, m.Init())
	return ui.WithSaveForwardEdit(m, func(alias, set, old, new string) error {
		*calls = append(*calls, []string{alias, set, old, new})
		return hookErr
	})
}

func TestModel_Update_EditForwardLive(t *testing.T) {
	t.Parallel()
	var calls [][]string
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", "3005:localhost:3000", "ubuntu")
	m := editModel(t, r, &calls, nil)
	m = press(t, m, "l", "E") // web0, live
	view := m.View()
	if !ui.Prompting(m) || !strings.Contains(view, "edit web on ubuntu: "+specWeb0) {
		t.Fatalf("E should open the prompt prefilled with the spec:\n%s", view)
	}
	// The row shows exactly what enter will run, following the typing.
	for _, w := range []string{"sasash.conf: forwards web: " + specWeb0 + " → " + specWeb0, "ssh -O cancel -L " + specWeb0 + " ubuntu", "ssh -O forward -L " + specWeb0 + " ubuntu"} {
		if !strings.Contains(view, w) {
			t.Errorf("View() while editing missing the plan line %q:\n%s", w, view)
		}
	}
	for range specWeb0 {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3005:localhost:3000")})
	if view := m.View(); !strings.Contains(view, "ssh -O forward -L 3005:localhost:3000 ubuntu") || !strings.Contains(view, "ssh -O cancel -L "+specWeb0+" ubuntu") {
		t.Errorf("View() plan did not follow the typing:\n%s", view)
	}
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd == nil || ui.Prompting(m) {
		t.Fatalf("enter: cmd %v, prompting %v", cmd != nil, ui.Prompting(m))
	}
	next, after := m.Update(cmd())
	m = next.(ui.Model)
	// Config first, then per-spec surgery only: cancel old, forward new,
	// nothing else on the master.
	if want := [][]string{{"ubuntu", "web", specWeb0, "3005:localhost:3000"}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("hook calls = %v, want %v", calls, want)
	}
	want := []string{"ssh -O cancel -L " + specWeb0 + " ubuntu", "ssh -O forward -L 3005:localhost:3000 ubuntu"}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("ssh calls = %v, want exactly %v", got, want)
	}
	if view := m.View(); !strings.Contains(view, "re-plumbed "+specWeb0+" → 3005:localhost:3000") {
		t.Errorf("View() missing the outcome:\n%s", view)
	}
	if after == nil {
		t.Errorf("Update(editFwdDone) cmd = nil, want a status re-poll and reload")
	}
}

func TestModel_Update_EditForwardOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setup     func(r *fakerun.Fake)
		hookErr   error
		row       []string // keys to reach the row after l
		masterUp  bool
		newSpec   string
		wantHook  bool
		wantCalls []string
		wantNote  []string
		wantPlan  []string
		notPlan   []string
	}{
		{
			name:     "down row is config only",
			row:      []string{"j"}, // web1: down
			masterUp: true,
			newSpec:  "3001:localhost:3009",
			wantHook: true,
			wantNote: []string{"declared 3001:localhost:3009 in web"},
			wantPlan: []string{"sasash.conf: forwards web"},
			notPlan:  []string{"-O cancel", "-O forward"},
		},
		{
			name:     "master down is config only even for an answering port",
			masterUp: false, // web0's port answers but nothing can be re-plumbed
			newSpec:  "3000:localhost:3009",
			wantHook: true,
			wantNote: []string{"declared 3000:localhost:3009 in web"},
			notPlan:  []string{"-O cancel"},
		},
		{
			name: "config write failure stops before any ssh",
			setup: func(r *fakerun.Fake) {
				r.StubExit(0, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu")
			},
			hookErr:  errors.New(`spec "3000:localhost:3009" already in set "web"`),
			masterUp: true,
			newSpec:  "3000:localhost:3009",
			wantHook: true,
			wantNote: []string{"not declared: ", "already in set"},
		},
		{
			name: "forward failure says the old one is gone",
			setup: func(r *fakerun.Fake) {
				r.StubExit(0, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu").
					Stub(fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("bind: Address already in use")}}, "ssh", "-O", "forward", "-L", "3000:localhost:3009", "ubuntu")
			},
			masterUp:  true,
			newSpec:   "3000:localhost:3009",
			wantHook:  true,
			wantCalls: []string{"ssh -O cancel -L " + specWeb0 + " ubuntu", "ssh -O forward -L 3000:localhost:3009 ubuntu"},
			wantNote:  []string{"cancelled " + specWeb0 + " but forward 3000:localhost:3009 failed", "Address already in use"},
		},
		{
			name: "cancel failure is reported and the forward is not attempted",
			setup: func(r *fakerun.Fake) {
				r.Stub(fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("unknown port forwarding")}}, "ssh", "-O", "cancel", "-L", specWeb0, "ubuntu")
			},
			masterUp:  true,
			newSpec:   "3000:localhost:3009",
			wantHook:  true,
			wantCalls: []string{"ssh -O cancel -L " + specWeb0 + " ubuntu"},
			wantNote:  []string{"cancel of " + specWeb0 + " failed: unknown port forwarding", "config updated"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls [][]string
			r := fakerun.New()
			if tt.setup != nil {
				tt.setup(r)
			}
			var m ui.Model
			if tt.masterUp {
				m = editModel(t, r, &calls, tt.hookErr)
			} else {
				r.StubExit(255, "ssh", "-O", "check", "ubuntu")
				m = polled(t, r, fakedial.New().Up("127.0.0.1:3000", time.Millisecond))
				m = ui.WithSaveForwardEdit(m, func(alias, set, old, new string) error {
					calls = append(calls, []string{alias, set, old, new})
					return tt.hookErr
				})
			}
			m = press(t, m, append([]string{"l"}, tt.row...)...)
			m = press(t, m, "E")
			view := m.View()
			for _, w := range tt.wantPlan {
				if !strings.Contains(view, w) {
					t.Errorf("plan missing %q:\n%s", w, view)
				}
			}
			for _, w := range tt.notPlan {
				if strings.Contains(view, w) {
					t.Errorf("plan shows %q, want absent:\n%s", w, view)
				}
			}
			for range 32 {
				m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
			}
			m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.newSpec)})
			before := len(r.CallKeys())
			next, cmd := m.Update(keyMsg("enter"))
			m = next.(ui.Model)
			if cmd == nil {
				t.Fatalf("enter cmd = nil, want the edit")
			}
			m = update(t, m, cmd())
			if (len(calls) == 1) != tt.wantHook {
				t.Errorf("hook calls = %v, want called %v", calls, tt.wantHook)
			}
			if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, tt.wantCalls) && !(len(got) == 0 && len(tt.wantCalls) == 0) {
				t.Errorf("ssh calls = %v, want %v", got, tt.wantCalls)
			}
			view = m.View()
			for _, w := range tt.wantNote {
				if !strings.Contains(view, w) {
					t.Errorf("View() missing %q:\n%s", w, view)
				}
			}
		})
	}
}

func TestModel_Update_EditForwardGuards(t *testing.T) {
	t.Parallel()
	var calls [][]string
	r := fakerun.New()
	m := editModel(t, r, &calls, nil)
	// No highlight: a hint, nothing opened.
	m = press(t, m, "E")
	if ui.Prompting(m) || !strings.Contains(m.View(), "l focuses") {
		t.Errorf("E without a highlight: prompting %v:\n%s", ui.Prompting(m), m.View())
	}
	// Unchanged spec and a bad spec keep the prompt; esc leaves it.
	m = press(t, m, "l", "E")
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !ui.Prompting(m) || !strings.Contains(m.View(), "unchanged") {
		t.Errorf("enter on the unchanged spec: cmd %v, prompting %v:\n%s", cmd != nil, ui.Prompting(m), m.View())
	}
	for range specWeb0 {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("nonsense")})
	next, cmd = m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !ui.Prompting(m) || !strings.Contains(m.View(), "want local:host:port, port, or local:port") {
		t.Errorf("enter on a bad spec: cmd %v, prompting %v:\n%s", cmd != nil, ui.Prompting(m), m.View())
	}
	m = press(t, m, "esc")
	if ui.Prompting(m) || len(calls) != 0 || len(r.CallKeys()) != 1 {
		t.Errorf("esc: prompting %v, hook %v, calls %v", ui.Prompting(m), calls, r.CallKeys())
	}
	// While filtering E is text.
	f := press(t, m, "/", "E")
	if got := ui.FilterText(f); got != "E" || ui.Prompting(f) {
		t.Errorf("E while filtering: text %q, prompting %v", got, ui.Prompting(f))
	}
	// Help lists it.
	if view := press(t, m, "?").View(); !strings.Contains(view, "edit forward") {
		t.Errorf("View() full help missing E:\n%s", view)
	}
}

// adhocRowModel is the tunnels fixture with a recorded ad-hoc forward on
// port 9000; up says whether its port answers under a live master.
func adhocRowModel(t *testing.T, r *fakerun.Fake, up bool, calls *[][]string) (ui.Model, string) {
	t.Helper()
	r.StubExit(0, "ssh", "-O", "check", "ubuntu")
	stateDir := t.TempDir()
	state := `{"forwards":[{"set":"","spec":"9000:localhost:9000"}]}`
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	d := fakedial.New()
	if up {
		d.Up("127.0.0.1:9000", time.Millisecond)
	}
	m := tunnelModel(t, r, d, stateDir)
	m = feed(t, m, m.Init())
	return ui.WithSaveForwardEdit(m, func(alias, set, old, new string) error {
		*calls = append(*calls, []string{alias, set, old, new})
		return nil
	}), stateDir
}

func TestModel_Update_EditForwardAdhocRow(t *testing.T) {
	t.Parallel()
	// A live ad-hoc row is surgery only: no meta line exists to rewrite.
	var calls [][]string
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "cancel", "-L", "9000:localhost:9000", "ubuntu").
		StubExit(0, "ssh", "-O", "forward", "-L", "9001:localhost:9000", "ubuntu")
	m, _ := adhocRowModel(t, r, true, &calls)
	m = press(t, m, "l", "G", "E") // the ad-hoc row is last
	view := m.View()
	if !strings.Contains(view, "edit ad-hoc on ubuntu: 9000:localhost:9000") || strings.Contains(view, "sasash.conf:") {
		t.Fatalf("E on a live ad-hoc row: want the prompt and a surgery-only plan:\n%s", view)
	}
	for range len("9000:localhost:9000") {
		m = update(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9001:localhost:9000")})
	before := len(r.CallKeys())
	next, cmd := m.Update(keyMsg("enter"))
	m = update(t, next.(ui.Model), cmd())
	if len(calls) != 0 {
		t.Errorf("hook called for an ad-hoc row: %v", calls)
	}
	want := []string{"ssh -O cancel -L 9000:localhost:9000 ubuntu", "ssh -O forward -L 9001:localhost:9000 ubuntu"}
	if got := r.CallKeys()[before:]; !reflect.DeepEqual(got, want) {
		t.Errorf("ssh calls = %v, want %v", got, want)
	}
	if view := m.View(); !strings.Contains(view, "re-plumbed 9000:localhost:9000 → 9001:localhost:9000") || strings.Contains(view, "declared") {
		t.Errorf("View() outcome for an ad-hoc row:\n%s", view)
	}

	// A cancel failure on an ad-hoc row must not claim a config update:
	// nothing was written.
	var none2 [][]string
	r2 := fakerun.New().Stub(fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("unknown port forwarding")}}, "ssh", "-O", "cancel", "-L", "9000:localhost:9000", "ubuntu")
	m2, _ := adhocRowModel(t, r2, true, &none2)
	m2 = press(t, m2, "l", "G", "E")
	for range len("9000:localhost:9000") {
		m2 = update(t, m2, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m2 = update(t, m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9001:localhost:9000")})
	next, cmd = m2.Update(keyMsg("enter"))
	m2 = update(t, next.(ui.Model), cmd())
	if view := m2.View(); !strings.Contains(view, "cancel of 9000:localhost:9000 failed: unknown port forwarding") || strings.Contains(view, "config updated") {
		t.Errorf("View() after a failed ad-hoc cancel:\n%s", view)
	}

	// A dead ad-hoc row has nothing to edit: x it and + a new one.
	var none [][]string
	dead, _ := adhocRowModel(t, fakerun.New(), false, &none)
	dead = press(t, dead, "l", "G", "E")
	if ui.Prompting(dead) || !strings.Contains(dead.View(), "not live") {
		t.Errorf("E on a dead ad-hoc row: prompting %v:\n%s", ui.Prompting(dead), dead.View())
	}
}

func TestModel_Update_EditForwardNeedsHookForDeclared(t *testing.T) {
	t.Parallel()
	// Without the edit hook a declared, dead row cannot be edited; a
	// live one still can (surgery only).
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	m := polled(t, r, fakedial.New().Up("127.0.0.1:3000", time.Millisecond)) // no hook
	m = press(t, m, "l", "j", "E")                                           // web1: down
	if ui.Prompting(m) || !strings.Contains(m.View(), "not available") {
		t.Errorf("E on a dead declared row without a hook: prompting %v:\n%s", ui.Prompting(m), m.View())
	}
	m = press(t, m, "k", "E") // web0: live
	if !ui.Prompting(m) || strings.Contains(m.View(), "sasash.conf:") || !strings.Contains(m.View(), "ssh -O cancel") {
		t.Errorf("E on a live declared row without a hook: want surgery-only plan:\n%s", m.View())
	}
}

func TestModel_View_EditPlanStaysOnItsRow(t *testing.T) {
	t.Parallel()
	// A poll lands while the prompt is open and the edited ad-hoc row is
	// gone: the highlight clamps onto another row, but the plan must not
	// move with it — it belongs to the row the edit is bound to, and so
	// does the action.
	var calls [][]string
	r := fakerun.New()
	m, stateDir := adhocRowModel(t, r, true, &calls)
	m = press(t, m, "l", "G", "E")
	if !strings.Contains(m.View(), "ssh -O cancel -L 9000:localhost:9000 ubuntu") {
		t.Fatalf("plan missing before the poll:\n%s", m.View())
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ubuntu.json"), []byte(`{"forwards":[]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, tick := m.Update(ui.RefreshTick())
	m = feed(t, m, tick) // the ad-hoc row is gone; the highlight clamps onto api
	view := m.View()
	if !ui.Prompting(m) || !strings.Contains(view, "edit ad-hoc on ubuntu: 9000:localhost:9000") {
		t.Fatalf("the prompt should stay bound to the ad-hoc spec:\n%s", view)
	}
	if strings.Contains(view, "-O cancel") || strings.Contains(view, "-O forward") || strings.Contains(view, "sasash.conf:") {
		t.Errorf("View() shows the plan under a row the edit is not bound to:\n%s", view)
	}
}

func TestModel_Update_SettingsNewConnRejectsTunnelOnly(t *testing.T) {
	t.Parallel()
	m := ui.WithSaveHost(settingsModel(t, fakerun.New()), func(ui.AdhocEntry) error { return nil })
	m = press(t, m, "G", "k", "enter")
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-N 192.0.2.99")})
	next, cmd := m.Update(keyMsg("enter"))
	m = next.(ui.Model)
	if cmd != nil || !ui.Prompting(m) || ui.PromptingSave(m) || !strings.Contains(m.View(), "-N belongs to the a prompt") {
		t.Errorf("-N in the settings prompt: cmd %v, prompting %v, save %v:\n%s", cmd != nil, ui.Prompting(m), ui.PromptingSave(m), m.View())
	}
}
