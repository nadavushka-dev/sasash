package ui_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The R key (SASASH.4.16): a prefilled prompt over the RenameHost hook.

const (
	renameConf  = "/home/u/.ssh/config.d/sasash.conf"
	ambientFile = "/home/u/.ssh/config"
)

// renameHook records the hook's calls and answers with a fixed result.
type renameHook struct {
	calls []string
	res   pathswitch.RenameResult
	err   error
}

func (h *renameHook) rename(old, new string) (pathswitch.RenameResult, error) {
	h.calls = append(h.calls, old+"→"+new)
	return h.res, h.err
}

// managedHost is a block in sasash.conf.
func managedHost(alias string, paths ...probe.Path) hostmodel.Host {
	if len(paths) == 0 {
		paths = []probe.Path{{Name: "lan", Host: "192.0.2.1"}}
	}
	return hostmodel.Host{Alias: alias, Paths: paths, File: renameConf}
}

// webGroup is a managed host with two pinned variants and a neighbour.
func webGroup() []hostmodel.Host {
	return []hostmodel.Host{
		managedHost("web", probe.Path{Name: "lan", Host: "192.0.2.1"}, probe.Path{Name: "wan", Host: "203.0.113.1"}),
		managedHost("web.lan", probe.Path{Name: "lan", Host: "192.0.2.1"}),
		managedHost("web.wan", probe.Path{Name: "wan", Host: "203.0.113.1"}),
		// A hand-authored dotted block: two paths, so no variant.
		managedHost("web.old", probe.Path{Name: "a", Host: "192.0.2.3"}, probe.Path{Name: "b", Host: "192.0.2.4"}),
		managedHost("api", probe.Path{Name: "lan", Host: "192.0.2.2"}),
	}
}

// renameModel is a sized model over hosts with sasash.conf known and the
// hook wired.
func renameModel(t *testing.T, hosts []hostmodel.Host, h *renameHook) ui.Model {
	t.Helper()
	m := ui.WithEditor(sized(t, hosts), []string{"vi"}, "", renameConf)
	return ui.WithRenameHost(m, h.rename)
}

// drain runs a cmd tree and returns every message it produces.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// submit is enter in the prompt: the model after the key, and the
// messages its cmd produced (the hook's outcome, the reload).
func submit(t *testing.T, m ui.Model) (ui.Model, []tea.Msg) {
	t.Helper()
	next, cmd := m.Update(keyMsg("enter"))
	return next.(ui.Model), drain(cmd)
}

func selectedAlias(t *testing.T, m ui.Model) string {
	t.Helper()
	host, ok := m.Selected()
	if !ok {
		t.Fatal("Selected() = none")
	}
	return host.Alias
}

func TestModel_Rename_PromptPrefilledAndEscCancels(t *testing.T) {
	t.Parallel()
	h := &renameHook{}
	m := press(t, renameModel(t, []hostmodel.Host{managedHost("host0"), managedHost("host1")}, h), "R")
	view := m.View()
	if !ui.Prompting(m) || ui.PromptValue(m) != "host0" || !strings.Contains(view, "rename host0 to:") || !strings.Contains(view, "enter rename") || !strings.Contains(view, "esc cancel") {
		t.Fatalf("R should open the prompt prefilled with the alias (prompting %v, value %q):\n%s", ui.Prompting(m), ui.PromptValue(m), view)
	}
	// The cursor sits at the end: typing appends.
	if m = press(t, m, "x"); ui.PromptValue(m) != "host0x" {
		t.Errorf("PromptValue() after typing = %q, want %q", ui.PromptValue(m), "host0x")
	}
	m = press(t, m, "esc")
	if ui.Prompting(m) || len(h.calls) != 0 || strings.Contains(m.View(), "rename host0") || selectedAlias(t, m) != "host0" {
		t.Errorf("esc should cancel without a write (prompting %v, calls %v):\n%s", ui.Prompting(m), h.calls, m.View())
	}
}

func TestModel_Rename_RefusedRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		hosts    []hostmodel.Host
		hook     bool
		noEditor bool
		keys     []string
		notice   string
	}{
		{name: "no hook", hosts: testHosts(1), keys: []string{"R"}, notice: "renaming is not available"},
		{name: "without an editor e is not named", hook: true, noEditor: true,
			hosts:  []hostmodel.Host{{Alias: "other", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.9"}}, File: ambientFile}},
			keys:   []string{"R"},
			notice: "other is not in sasash.conf — rename it in " + ambientFile + " yourself\n"},
		{name: "ambient row points at e", hook: true,
			hosts:  []hostmodel.Host{managedHost("web"), {Alias: "other", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.9"}}, File: ambientFile}},
			keys:   []string{"G", "R"},
			notice: "other is not in sasash.conf — rename it in " + ambientFile + " yourself (e)"},
		{name: "meta host outside sasash.conf is not sasash's block", hook: true,
			hosts:  []hostmodel.Host{{Alias: "tagged", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.9"}}, File: ambientFile, HasMeta: true}},
			keys:   []string{"R"},
			notice: "tagged is not in sasash.conf — rename it in " + ambientFile + " yourself (e)"},
		{name: "host without a file", hook: true,
			hosts:  []hostmodel.Host{{Alias: "bare", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.9"}}}},
			keys:   []string{"R"},
			notice: "bare is not in sasash.conf — rename it by hand (e)"},
		{name: "pinned variant points at its parent", hook: true, hosts: webGroup(),
			keys:   []string{"/", "w", "e", "b", ".", "l", "a", "n", "enter", "R"},
			notice: "web.lan is listed as web's pinned variant for path lan — rename web instead"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &renameHook{}
			editor := []string{"vi"}
			if tt.noEditor {
				editor = nil
			}
			m := ui.WithEditor(sized(t, tt.hosts), editor, "", renameConf)
			if tt.hook {
				m = ui.WithRenameHost(m, h.rename)
			}
			m = press(t, m, tt.keys...)
			if ui.Prompting(m) || len(h.calls) != 0 || !strings.Contains(m.View(), tt.notice) {
				t.Errorf("R should refuse with %q (prompting %v, calls %v):\n%s", tt.notice, ui.Prompting(m), h.calls, m.View())
			}
		})
	}
}

func TestModel_Rename_BusyHostRefused(t *testing.T) {
	t.Parallel()
	const busy = "web is busy — a status check or an action is running; try again"
	tests := []struct {
		name string
		mark func(ui.Model, string) ui.Model
	}{
		{name: "path switch in flight", mark: ui.MarkSwitching},
		{name: "tunnel action or poll in flight", mark: ui.MarkInflight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &renameHook{}
			m := press(t, tt.mark(renameModel(t, webGroup(), h), "web"), "R")
			if ui.Prompting(m) || len(h.calls) != 0 || !strings.Contains(m.View(), busy) {
				t.Errorf("R on a busy host should refuse (prompting %v, calls %v):\n%s", ui.Prompting(m), h.calls, m.View())
			}
			// The gate is read again at enter: a poll that started while
			// the prompt was open wins, and the prompt closes.
			h2 := &renameHook{}
			m = press(t, renameModel(t, webGroup(), h2), "R", "ctrl+u", "srv")
			m, msgs := submit(t, tt.mark(m, "web"))
			if ui.Prompting(m) || len(h2.calls) != 0 || len(msgs) != 0 || !strings.Contains(m.View(), busy) {
				t.Errorf("enter on a host that went busy should refuse (prompting %v, calls %v, msgs %d):\n%s", ui.Prompting(m), h2.calls, len(msgs), m.View())
			}
		})
	}
}

func TestModel_Rename_HoldsTheGateUntilTheOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "renamed"},
		{name: "refused", err: errors.New("alias \"srv\" already exists")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &renameHook{err: tt.err}
			m := press(t, renameModel(t, webGroup(), h), "R", "ctrl+u", "srv")
			m, msgs := submit(t, m)
			if !ui.Inflight(m, "web") {
				t.Fatal("enter should hold the host's gate while the rename runs")
			}
			m = update(t, m, msgs[0])
			if ui.Inflight(m, "web") || ui.Inflight(m, "srv") {
				t.Errorf("the outcome should release the gate (web %v, srv %v)", ui.Inflight(m, "web"), ui.Inflight(m, "srv"))
			}
		})
	}
}

func TestModel_Rename_FanOutCollisionBeyondTheFold(t *testing.T) {
	t.Parallel()
	// web.lan sits under web's prefix but its address was hand-edited,
	// so the fold does not list it as a variant — the writer may still
	// carry it along, and srv.lan is a user's host in another file.
	hosts := []hostmodel.Host{
		managedHost("web", probe.Path{Name: "lan", Host: "192.0.2.1"}),
		managedHost("web.lan", probe.Path{Name: "lan", Host: "198.51.100.99"}),
		{Alias: "srv.lan", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.9"}}, File: ambientFile},
	}
	h := &renameHook{}
	m := press(t, renameModel(t, hosts, h), "R", "ctrl+u", "srv")
	m, msgs := submit(t, m)
	if !ui.Prompting(m) || len(h.calls) != 0 || len(msgs) != 0 || !strings.Contains(m.View(), `alias "srv.lan" already exists`) {
		t.Errorf("enter should refuse the fan-out collision (prompting %v, calls %v):\n%s", ui.Prompting(m), h.calls, m.View())
	}
}

func TestModel_Rename_SubmitRefusals(t *testing.T) {
	t.Parallel()
	hosts := append(webGroup(), hostmodel.Host{Alias: "srv.lan", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.9"}}, File: ambientFile})
	tests := []struct {
		name   string
		typed  string
		notice string
	}{
		{name: "unchanged", typed: "web", notice: "unchanged — esc cancels"},
		{name: "empty", typed: "", notice: `alias "": want a single plain word`},
		{name: "two words", typed: "a b", notice: `alias "a b": want a single plain word`},
		{name: "leading dash", typed: "-x", notice: `alias "-x": want a single plain word`},
		{name: "wildcard", typed: "w*", notice: `alias "w*": want a single plain word`},
		{name: "a path is no file name", typed: "../x", notice: `alias "../x": want a single plain word`},
		{name: "a slash is no file name", typed: "a/b", notice: `alias "a/b": want a single plain word`},
		{name: "dot", typed: ".", notice: `alias ".": want a single plain word`},
		{name: "dot dot", typed: "..", notice: `alias "..": want a single plain word`},
		{name: "collides with a managed host", typed: "api", notice: `alias "api" already exists`},
		{name: "collides with its own variant", typed: "web.lan", notice: `alias "web.lan" already exists`},
		{name: "fan-out would shadow an ambient host", typed: "srv", notice: `alias "srv.lan" already exists`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &renameHook{}
			m := press(t, renameModel(t, hosts, h), "R", "ctrl+u")
			if tt.typed != "" {
				m = press(t, m, tt.typed)
			}
			m, msgs := submit(t, m)
			if !ui.Prompting(m) || len(h.calls) != 0 || len(msgs) != 0 || !strings.Contains(m.View(), tt.notice) {
				t.Errorf("enter on %q should stay in the prompt with %q (prompting %v, calls %v, msgs %d):\n%s", tt.typed, tt.notice, ui.Prompting(m), h.calls, len(msgs), m.View())
			}
		})
	}
}

func TestModel_Rename_Renamed(t *testing.T) {
	t.Parallel()
	h := &renameHook{res: pathswitch.RenameResult{Followed: []string{"lan", "wan"}, LeftAsIs: []string{"web.old"}}}
	loads := 0
	m := ui.WithLoadHosts(renameModel(t, webGroup(), h), func() ([]hostmodel.Host, []error, error) {
		loads++
		hosts := webGroup()
		hosts[0].Alias, hosts[1].Alias, hosts[2].Alias = "srv", "srv.lan", "srv.wan" // web.old stays
		return hosts, nil, nil
	})
	m = update(t, m, ui.TunnelStatus("web", tunnel.HostStatus{}, nil))
	m = press(t, m, "R", "ctrl+u", "srv")
	m, msgs := submit(t, m)
	if ui.Prompting(m) || !strings.Contains(m.View(), "renaming web → srv …") || len(h.calls) != 1 || h.calls[0] != "web→srv" {
		t.Fatalf("enter should call the hook once (prompting %v, calls %v):\n%s", ui.Prompting(m), h.calls, m.View())
	}
	if len(msgs) != 1 {
		t.Fatalf("enter's cmd produced %d messages, want the hook's outcome", len(msgs))
	}
	next, cmd := m.Update(msgs[0])
	m = next.(ui.Model)
	view := m.View()
	// The line says the rename; the popup tells the rest, the
	// live-master caveat included.
	for _, want := range []string{"renamed web → srv (2 variants followed)", "pinned variants followed: lan, wan", "dotted blocks left as-is: web.old", "rename them by hand, e", "the same master answers under", "srv at the next poll"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if !ui.PopupOpen(m) || strings.Contains(view, "the master follows") {
		t.Errorf("the popup should carry the story (open %v); no master was up:\n%s", ui.PopupOpen(m), view)
	}
	m = press(t, m, "esc")
	// The rows follow at once — before the reload — with the cursor on
	// the renamed row: the followed variants under the new alias, the
	// hand-authored dotted block the writer left as-is under its own.
	if got := selectedAlias(t, m); got != "srv" {
		t.Errorf("Selected() after the rename = %q, want srv", got)
	}
	// The old alias's tunnel state is evicted: the reload never touches
	// tstatus, and a host reusing the name must not inherit it.
	if ui.TunnelStatusKnown(m, "web") {
		t.Error("tstatus should forget the old alias")
	}
	// web.old (a hand-authored two-path block) is no variant, so it stays
	// listed under its own name; the followed variants srv.lan/srv.wan fold
	// under the new alias and a filter surfaces them (SASASH.4.23: no v).
	if view := m.View(); !strings.Contains(view, "web.old") || strings.Contains(view, "srv.old") || strings.Contains(view, "web.lan") {
		t.Errorf("rows before the reload: web.old stays, no srv.old, web.lan renamed away:\n%s", view)
	}
	// Each followed variant surfaces under a filter naming its full alias
	// (the only way now that v is gone); both carry the new alias.
	if view := press(t, m, "/", "s", "r", "v", ".", "l", "a", "n", "enter").View(); !strings.Contains(view, "srv.lan") {
		t.Errorf("a filter should surface the followed variant srv.lan:\n%s", view)
	}
	if view := press(t, m, "/", "s", "r", "v", ".", "w", "a", "n", "enter").View(); !strings.Contains(view, "srv.wan") {
		t.Errorf("a filter should surface the followed variant srv.wan:\n%s", view)
	}
	// The reload lands with the file's hosts; the selection stays.
	for _, msg := range drain(cmd) {
		m = update(t, m, msg)
	}
	if loads != 1 || selectedAlias(t, m) != "srv" {
		t.Errorf("reload: loads = %d, selected %q; want 1 and srv", loads, selectedAlias(t, m))
	}
}

func TestModel_Rename_Outcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		selected string
		notice   string
	}{
		{name: "writer refusal is said verbatim", err: errors.New(`alias "srv.lan" already exists`), selected: "web", notice: `rename failed: alias "srv.lan" already exists`},
		{name: "a kept cache is a renamed host", err: fmt.Errorf("%w: rename web.json srv.json: permission denied", ui.ErrCacheKept), selected: "srv",
			notice: "state cache not renamed: rename web.json srv.json: permission denied"},
		{name: "two kept caches get a line each", selected: "srv",
			err:    fmt.Errorf("%w: s/srv.json exists: s/web.json left in place\ns/srv.stats.json exists: s/web.stats.json left in place", ui.ErrCacheKept),
			notice: "s/srv.stats.json exists: s/web.stats.json left in place"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &renameHook{err: tt.err}
			m := press(t, renameModel(t, webGroup(), h), "R", "ctrl+u", "srv")
			m, msgs := submit(t, m)
			if len(msgs) != 1 {
				t.Fatalf("enter's cmd produced %d messages, want 1", len(msgs))
			}
			m = update(t, m, msgs[0])
			if got := selectedAlias(t, m); got != tt.selected || !strings.Contains(m.View(), tt.notice) {
				t.Errorf("Selected() = %q, want %q; notice want %q:\n%s", got, tt.selected, tt.notice, m.View())
			}
			if strings.Contains(m.View(), "left in places/") {
				t.Errorf("the kept caches ran together on one line:\n%s", m.View())
			}
		})
	}
}

func TestModel_Rename_MasterUpIsSaid(t *testing.T) {
	t.Parallel()
	h := &renameHook{res: pathswitch.RenameResult{Followed: []string{"lan", "wan"}}}
	m := renameModel(t, webGroup(), h)
	m = update(t, m, ui.TunnelStatus("web", tunnel.HostStatus{Master: true}, nil))
	m = press(t, m, "R", "ctrl+u", "srv")
	m, msgs := submit(t, m)
	m = update(t, m, msgs[0])
	if view := m.View(); !ui.PopupOpen(m) || !strings.Contains(view, "renamed web → srv (2 variants followed) — the master follows") || !strings.Contains(view, "the same master answers under") || !strings.Contains(view, "srv at the next poll") {
		t.Errorf("a live master should be said on the line and explained in the popup (open %v):\n%s", ui.PopupOpen(m), view)
	}
}

func TestModel_Rename_UnderFilterKeepsSelection(t *testing.T) {
	t.Parallel()
	// Three matches, the cursor on the middle one: the match set is
	// recomputed after the rename, and the selection must land on the
	// renamed host by name, never on whichever row the index now hits.
	h := &renameHook{}
	hosts := []hostmodel.Host{managedHost("zeta"), managedHost("beta"), managedHost("gamma")}
	m := press(t, renameModel(t, hosts, h), "/", "a", "enter", "j")
	if ui.FilterState(m) != list.FilterApplied || selectedAlias(t, m) != "zeta" {
		t.Fatalf("filter a then j should select the middle match, zeta (state %v, selected %q)", ui.FilterState(m), selectedAlias(t, m))
	}
	m = press(t, m, "R", "ctrl+u", "aab")
	m, msgs := submit(t, m)
	next, cmd := m.Update(msgs[0])
	m = next.(ui.Model)
	for _, msg := range drain(cmd) {
		m = update(t, m, msg)
	}
	if got := selectedAlias(t, m); got != "aab" {
		t.Errorf("Selected() after a rename under a filter = %q, want aab", got)
	}
}

func TestModel_Rename_KeyIsTextWhileFiltering(t *testing.T) {
	t.Parallel()
	h := &renameHook{}
	m := press(t, renameModel(t, testHosts(2), h), "/", "R")
	if ui.FilterState(m) != list.Filtering || ui.Prompting(m) || ui.FilterText(m) != "R" {
		t.Errorf("R while filtering: state %v, prompting %v, text %q; want typed text", ui.FilterState(m), ui.Prompting(m), ui.FilterText(m))
	}
}
