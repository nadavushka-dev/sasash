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

// forwardProceedMsg: the local port is free, the add can go ahead. persist
// opens the set-name prompt; ephemeral connects a session-only forward now.
type forwardProceedMsg struct{ host, spec string }

// fwdDeclaredMsg reports the persist-by-default outcome: the declaration
// write (saveErr) and the live connect (connErr).
type fwdDeclaredMsg struct {
	host, name, spec string
	saveErr, connErr error
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
	m.prompt.Placeholder = "local:host:port  (! prefix = session-only, unsaved)"
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
	spec, _ := expandSpec(raw)
	port, _ := localPort(spec)
	if port == "" {
		m.notice = "no local port in " + strconv.Quote(spec) + " — want local:host:port"
		return m, nil // stay in the prompt for correction
	}
	host, ok := m.hostByAlias(m.fwdHost)
	m.mode = promptNone
	m.prompt.Blur()
	if !ok {
		// Edited away while the prompt was open: nothing to add to.
		m.notice = "host " + m.fwdHost + " is gone — nothing added"
		return m, nil
	}
	return m, m.forwardCheckCmd(host.Alias, spec, port, ephemeral)
}

// forwardCheckCmd dials 127.0.0.1:<port> through the probe seam first (SPEC
// §4: never silently remap): held means a conflict comes back with the next
// free port offered; free means the add proceeds — to the set-name prompt
// (persist by default, SASASH.4.21) or, with the ! opt-in, straight to a
// session-only connect. The engine's own refusal still catches a port taken
// between the check and the spawn.
func (m Model) forwardCheckCmd(alias, spec, port string, ephemeral bool) tea.Cmd {
	eng, prober := m.tunnels, m.prober
	m.tinflight[alias] = true // the add writes the caches: no poll meanwhile
	return func() tea.Msg {
		cctx, ccancel := context.WithTimeout(context.Background(), forwardCheckTimeout)
		defer ccancel()
		if held(cctx, prober, port) {
			return forwardConflictMsg{host: alias, spec: spec, port: port, next: nextFreePort(cctx, prober, port), ephemeral: ephemeral}
		}
		if ephemeral {
			// The ! opt-in: one session-only process (the old inject),
			// recorded so x/X can still end it, but NOT in sasash.conf.
			actx, acancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
			defer acancel()
			done := "session-only tunnel up: " + spec + " on " + alias + " — unsaved, X forgets it"
			return tunnelDoneMsg{host: alias, label: "add", done: done, err: eng.SpawnSet(actx, alias, "", []string{spec})}
		}
		// Persist by default: on to the set-name prompt, then declare + connect.
		return forwardProceedMsg{host: alias, spec: spec}
	}
}

// openForwardName is the persist path's set-name step: the default is the
// local port, prefilled so enter accepts it. fwdset.Add then writes the
// declaration and Engine.Up connects it — persisted AND live.
func (m Model) openForwardName(host hostmodel.Host, spec string) (Model, tea.Cmd) {
	if m.saveForwardSet == nil {
		m.notice = "declaring is not available here — retry with a leading ! for a session-only tunnel"
		return m, nil
	}
	port, _ := localPort(spec)
	m.fwdHost = host.Alias
	m.fwdSpec = spec
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
	alias, spec := m.fwdHost, m.fwdSpec
	m.mode = promptNone
	m.prompt.Blur()
	if _, ok := m.hostByAlias(alias); !ok {
		m.notice = "host " + alias + " is gone — nothing added"
		return m, nil
	}
	return m, m.declareAndConnectCmd(alias, name, spec)
}

// declareAndConnectCmd is the persist-by-default action: write the
// `# @sasash forwards` line, then bring it up (SpawnSet). The declaration
// lands even if the connect fails — desire is persisted, space retries.
func (m Model) declareAndConnectCmd(alias, name, spec string) tea.Cmd {
	eng, save := m.tunnels, m.saveForwardSet
	m.tinflight[alias] = true
	return func() tea.Msg {
		if err := save(alias, name, []string{spec}); err != nil {
			return fwdDeclaredMsg{host: alias, name: name, spec: spec, saveErr: err}
		}
		actx, acancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer acancel()
		return fwdDeclaredMsg{host: alias, name: name, spec: spec, connErr: eng.SpawnSet(actx, alias, name, []string{spec})}
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
