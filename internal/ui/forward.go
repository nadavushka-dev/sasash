package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// portSearch is how far above a held port `+` looks for a free one.
const portSearch = 50

// forwardCheckTimeout bounds the conflict check: one loopback dial for
// the asked port plus, when held, the search above it. The add itself
// gets tunnelActionTimeout — `ssh -fN` may authenticate.
const forwardCheckTimeout = 5 * time.Second

// forwardConflictMsg reports that the asked local port is held; next is
// the first free port above it, "" when none within portSearch.
type forwardConflictMsg struct {
	host, spec, port, next string
	ephemeral              bool
}

// forwardProceedMsg: every local port is free, the add can go ahead. persist
// opens the set-name prompt; ephemeral connects a session-only forward now.
// specs is the whole set (SASASH.4.27: one or many, created together).
type forwardProceedMsg struct {
	host  string
	specs []string
}

// forwardPortConflict is one held local port in a multi-spec set and the
// first free port above it (next == "" when none within portSearch).
type forwardPortConflict struct{ port, next string }

// forwardSetConflictMsg: a multi-spec add where at least one local port is
// held. The whole set is refused (all-or-nothing); every conflict is named,
// and reoffer is the input to reopen so nothing typed is lost (SASASH.4.27).
type forwardSetConflictMsg struct {
	host, reoffer string
	held          []forwardPortConflict
}

// fwdDeclaredMsg reports the persist-by-default outcome: the declaration
// write (saveErr) and the live connect (connErr). specs is the whole set.
type fwdDeclaredMsg struct {
	host, name string
	specs      []string
	saveErr    error
	connErr    error
}

// expandSpec turns the two port shorthands into canonical three-part
// specs BEFORE anything downstream sees them (SASASH.4.9): "3000" is
// 3000:localhost:3000, "2000:3000" (two all-digit fields) is
// 2000:localhost:3000. Anything else passes through untouched — a
// hostname is never guessed into a shorthand, and every surface keeps
// the gate it always had (the engine's local-port rule, or the a
// prompt's two-colon rule), so nothing ssh accepts is lost: a remote
// socket target like 3000:/var/run/docker.sock is not a shorthand and
// goes through as typed.
func expandSpec(input string) (spec string, expanded bool) {
	spec = strings.TrimSpace(input)
	fields := strings.Split(spec, ":")
	allDigits := func(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }
	switch len(fields) {
	case 1:
		if allDigits(fields[0]) {
			return fields[0] + ":localhost:" + fields[0], true
		}
	case 2:
		if allDigits(fields[0]) && allDigits(fields[1]) {
			return fields[0] + ":localhost:" + fields[1], true
		}
	}
	return spec, false
}

// localPort mirrors the engine's rule for the port a spec binds locally
// (CONTRACT-tunnel §5: the first all-digit ':'-field among the first two)
// and returns which field it was, so a rewrite touches only that field.
// TestLocalPort_MatchesEngine pins the mirror to Engine.Status's Port.
func localPort(spec string) (port string, field int) {
	fields := strings.SplitN(spec, ":", 3)
	for i := 0; i < len(fields) && i < 2; i++ {
		if fields[i] != "" && strings.Trim(fields[i], "0123456789") == "" {
			return fields[i], i
		}
	}
	return "", -1
}

// withLocalPort rewrites the local-port field of spec; a spec with no
// such field (field < 0) is returned unchanged.
func withLocalPort(spec string, field int, port string) string {
	fields := strings.SplitN(spec, ":", 3)
	if field < 0 || field >= len(fields) {
		return spec
	}
	fields[field] = port
	return strings.Join(fields, ":")
}

// openForward is `+`: the prompt binds to the host under the cursor NOW
// (fwdHost, shown in the label), so a check that lands after the cursor
// moved on still adds to the host the user meant.
func (m Model) openForward(host hostmodel.Host, value string) (Model, tea.Cmd) {
	m.mode = promptForward
	m.fwdHost = m.tunnelHost(host).Alias // the tunnel is the machine's, not the variant's
	m.prompt.Reset()
	m.prompt.Placeholder = "local:host:port [more…]  (! prefix = session-only, unsaved)"
	m.prompt.SetValue(value)
	m.prompt.CursorEnd()
	return m, m.prompt.Focus()
}

// submitForward is enter in the forward prompt: the spec must name a
// local port the check can dial; then the check-and-add runs off the
// UI thread against the bound host.
func (m Model) submitForward() (Model, tea.Cmd) {
	raw := strings.TrimSpace(m.prompt.Value())
	ephemeral := strings.HasPrefix(raw, "!")
	if ephemeral {
		raw = strings.TrimSpace(raw[1:])
	}
	// Whitespace-separated specs → one set with N forwards (SASASH.4.27); a
	// single spec is the N=1 case. The whole set is validated all-or-nothing
	// BEFORE anything runs: a malformed or duplicate token names itself and
	// adds none, keeping the input in the prompt to fix.
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		m.notice = "no spec — want local:host:port [more…]"
		return m, nil
	}
	specs := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, tok := range fields {
		full, _ := expandSpec(tok)
		if port, _ := localPort(full); port == "" {
			m.notice = "spec " + strconv.Quote(tok) + ": no local port — want local:host:port"
			return m, nil // stay in the prompt; nothing added
		}
		if seen[full] {
			m.notice = "spec " + strconv.Quote(full) + " repeated — the set adds none"
			return m, nil
		}
		seen[full] = true
		specs = append(specs, full)
	}
	host, ok := m.hostByAlias(m.fwdHost)
	m.mode = promptNone
	m.prompt.Blur()
	if !ok {
		// Edited away while the prompt was open: nothing to add to.
		m.notice = "host " + m.fwdHost + " is gone — nothing added"
		return m, nil
	}
	// The persist default needs a sasash.conf block to declare into; an
	// unmanaged host has none, so + there is session-only, with a hint to
	// save the host first to persist (SASASH.4.21 — never fail after two
	// prompts).
	note := "unsaved, X forgets it"
	if !ephemeral && !m.managed(host) {
		ephemeral = true
		note = host.Alias + " not managed — save it (o) to persist"
	}
	return m, m.forwardCheckCmd(host.Alias, specs, ephemeral, note)
}

// forwardCheckCmd dials 127.0.0.1:<port> through the probe seam first (SPEC
// §4: never silently remap): held means a conflict comes back with the next
// free port offered; free means the add proceeds — to the set-name prompt
// (persist by default, SASASH.4.21) or, with the ! opt-in, straight to a
// session-only connect. The engine's own refusal still catches a port taken
// between the check and the spawn.
func (m Model) forwardCheckCmd(alias string, specs []string, ephemeral bool, sessionNote string) tea.Cmd {
	eng, prober := m.tunnels, m.prober
	m.tinflight[alias] = true // the add writes the caches: no poll meanwhile
	return func() tea.Msg {
		cctx, ccancel := context.WithTimeout(context.Background(), forwardCheckTimeout)
		defer ccancel()
		// Every spec's local port is dialled: a held one is a conflict.
		var conflicts []forwardPortConflict
		for _, spec := range specs {
			port, _ := localPort(spec)
			if held(cctx, prober, port) {
				conflicts = append(conflicts, forwardPortConflict{port: port, next: nextFreePort(cctx, prober, port)})
			}
		}
		if len(conflicts) > 0 {
			// N=1 keeps the single-port remap offer (unchanged UX); a
			// multi-spec set is refused whole and every conflict named.
			if len(specs) == 1 {
				return forwardConflictMsg{host: alias, spec: specs[0], port: conflicts[0].port, next: conflicts[0].next, ephemeral: ephemeral}
			}
			reoffer := strings.Join(specs, " ")
			if ephemeral {
				reoffer = "!" + reoffer
			}
			return forwardSetConflictMsg{host: alias, reoffer: reoffer, held: conflicts}
		}
		if ephemeral {
			// The ! opt-in: session-only processes (the old inject),
			// recorded so x/X can still end them, but NOT in sasash.conf.
			actx, acancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
			defer acancel()
			done := "session-only tunnel up: " + strings.Join(specs, " ") + " on " + alias + " — " + sessionNote
			return tunnelDoneMsg{host: alias, label: "add", done: done, err: eng.SpawnSet(actx, alias, "", specs)}
		}
		// Persist by default: on to the set-name prompt, then declare + connect.
		return forwardProceedMsg{host: alias, specs: specs}
	}
}

// openForwardName is the persist path's set-name step: the default is the
// local port, prefilled so enter accepts it. fwdset.Add then writes the
// declaration and Engine.Up connects it — persisted AND live.
func (m Model) openForwardName(host hostmodel.Host, specs []string) (Model, tea.Cmd) {
	if m.saveForwardSet == nil {
		m.notice = "declaring is not available here — retry with a leading ! for a session-only tunnel"
		return m, nil
	}
	// The default set name is the first spec's local port (SASASH.4.27: a
	// multi-spec set names itself after its first forward).
	port, _ := localPort(specs[0])
	m.fwdHost = host.Alias
	m.fwdSpecs = specs
	m.mode = promptForwardName
	m.prompt.Reset()
	m.prompt.Placeholder = "set name — enter saves as " + port
	m.prompt.SetValue(port)
	m.prompt.CursorEnd()
	return m, m.prompt.Focus()
}

// submitForwardName declares the set (fwdset.Add) and connects it live.
func (m Model) submitForwardName() (Model, tea.Cmd) {
	name := strings.TrimSpace(m.prompt.Value())
	if name == "" || strings.ContainsAny(name, " \t#") {
		m.notice = "set name: want a single word"
		return m, nil
	}
	alias, specs := m.fwdHost, m.fwdSpecs
	m.mode = promptNone
	m.prompt.Blur()
	if _, ok := m.hostByAlias(alias); !ok {
		m.notice = "host " + alias + " is gone — nothing added"
		return m, nil
	}
	return m, m.declareAndConnectCmd(alias, name, specs)
}

// declareAndConnectCmd is the persist-by-default action: write the
// `# @sasash forwards` line, then bring it up (SpawnSet). The declaration
// lands even if the connect fails — desire is persisted, space retries.
func (m Model) declareAndConnectCmd(alias, name string, specs []string) tea.Cmd {
	eng, save := m.tunnels, m.saveForwardSet
	m.tinflight[alias] = true
	return func() tea.Msg {
		if err := save(alias, name, specs); err != nil {
			return fwdDeclaredMsg{host: alias, name: name, specs: specs, saveErr: err}
		}
		actx, acancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer acancel()
		return fwdDeclaredMsg{host: alias, name: name, specs: specs, connErr: eng.SpawnSet(actx, alias, name, specs)}
	}
}

// held reports whether something accepts on 127.0.0.1:<port>.
func held(ctx context.Context, p probe.Prober, port string) bool {
	st := p.ProbeAll(ctx, []probe.Path{{Host: "127.0.0.1", Port: port}})
	return len(st) == 1 && st[0].Alive()
}

// nextFreePort is the first port above held that nothing accepts on,
// within portSearch; "" when there is none.
func nextFreePort(ctx context.Context, p probe.Prober, held string) string {
	n, err := strconv.Atoi(held)
	if err != nil {
		return ""
	}
	var paths []probe.Path
	for i := n + 1; i <= n+portSearch && i <= 65535; i++ {
		paths = append(paths, probe.Path{Host: "127.0.0.1", Port: strconv.Itoa(i)})
	}
	for _, st := range p.ProbeAll(ctx, paths) {
		if !st.Alive() {
			return st.Path.Port
		}
	}
	return ""
}

// forwardConflict answers the conflict, which lands asynchronously: the
// prompt reopens bound to the host it was raised for, with the spec
// rewritten to the offered port so the remap is on screen before it can
// run (unchanged when nothing was free). If another prompt or the
// filter owns the keyboard by then, the offer degrades to a notice —
// nothing runs, nothing typed is lost.
func (m Model) forwardConflict(msg forwardConflictMsg) (Model, tea.Cmd) {
	delete(m.tinflight, msg.host) // nothing was written: the gate opens
	offer := msg.spec
	notice := fmt.Sprintf("port %s is in use on %s — no free port within %d above it", msg.port, msg.host, portSearch)
	if msg.next != "" {
		_, field := localPort(msg.spec)
		offer = withLocalPort(msg.spec, field, msg.next)
		notice = fmt.Sprintf("port %s is in use — %s is free: enter uses it, esc cancels", msg.port, msg.next)
	}
	if m.popup.open() || m.mode != promptNone || m.settings.pane != settingsOff || m.focus == focusPaths || m.list.FilterState() == list.Filtering {
		m.notice = fmt.Sprintf("port %s is in use on %s", msg.port, msg.host)
		if msg.next != "" {
			m.notice += " — " + msg.next + " is free: + to add " + offer
		}
		return m, nil
	}
	host, ok := m.hostByAlias(msg.host)
	if !ok {
		m.notice = "host " + msg.host + " is gone — nothing added"
		return m, nil
	}
	if msg.ephemeral {
		offer = "!" + offer
	}
	m, cmd := m.openForward(host, offer)
	m.notice = notice
	return m, cmd
}

// forwardSetConflict answers a multi-spec add where at least one local port
// is held: the whole set is refused (all-or-nothing), every conflict named
// with its next-free port, and the prompt reopens with the input so nothing
// typed is lost. If the keyboard is busy, it degrades to a notice.
func (m Model) forwardSetConflict(msg forwardSetConflictMsg) (Model, tea.Cmd) {
	delete(m.tinflight, msg.host) // nothing was written: the gate opens
	parts := make([]string, 0, len(msg.held))
	for _, c := range msg.held {
		if c.next != "" {
			parts = append(parts, "port "+c.port+" ("+c.next+" free)")
		} else {
			parts = append(parts, "port "+c.port+" (none free near)")
		}
	}
	notice := "in use on " + msg.host + ": " + strings.Join(parts, ", ") + " — none added, edit and retry"
	if m.popup.open() || m.mode != promptNone || m.settings.pane != settingsOff || m.focus == focusPaths || m.list.FilterState() == list.Filtering {
		m.notice = notice
		return m, nil
	}
	host, ok := m.hostByAlias(msg.host)
	if !ok {
		m.notice = "host " + msg.host + " is gone — nothing added"
		return m, nil
	}
	m, cmd := m.openForward(host, msg.reoffer)
	m.notice = notice
	return m, cmd
}
