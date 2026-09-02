package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// portSearch is how far above a held port `+` looks for a free one.
const portSearch = 50

// forwardCheckTimeout bounds the conflict check: one loopback dial for
// the asked port plus, when held, the search above it.
const forwardCheckTimeout = 10 * time.Second

// forwardConflictMsg reports that the asked local port is held; next is
// the first free port above it, "" when none within portSearch.
type forwardConflictMsg struct{ host, spec, port, next string }

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

// withLocalPort rewrites the local-port field of spec.
func withLocalPort(spec string, field int, port string) string {
	fields := strings.SplitN(spec, ":", 3)
	fields[field] = port
	return strings.Join(fields, ":")
}

// submitForward is enter in the forward prompt: the spec must name a
// local port the check can dial; then the check-and-add runs off the
// UI thread.
func (m Model) submitForward() (Model, tea.Cmd) {
	spec := strings.TrimSpace(m.prompt.Value())
	port, _ := localPort(spec)
	if port == "" {
		m.notice = "no local port in " + strconv.Quote(spec) + " — want local:host:port"
		return m, nil // stay in the prompt for correction
	}
	host, ok := m.Selected()
	if !ok {
		return m, nil
	}
	m.mode = promptNone
	m.prompt.Blur()
	return m, m.addForwardCmd(host, spec, port)
}

// addForwardCmd dials 127.0.0.1:<port> through the probe seam first
// (SPEC §4: never silently remap): held means a conflict comes back with
// the next free port offered; free means Engine.Up with the spec as an
// ad-hoc forward. Something can still take the port between the check
// and the add — ssh's bind failure then comes back as the action error.
func (m Model) addForwardCmd(host hostmodel.Host, spec, port string) tea.Cmd {
	eng, prober, alias := m.tunnels, m.prober, host.Alias
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), forwardCheckTimeout)
		defer cancel()
		if held(ctx, prober, port) {
			return forwardConflictMsg{host: alias, spec: spec, port: port, next: nextFreePort(ctx, prober, port)}
		}
		return tunnelDoneMsg{host: alias, label: "add", done: "added " + spec, err: eng.Up(ctx, alias, "", []string{spec})}
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

// forwardConflict reopens the prompt on the conflict: with the spec
// rewritten to the offered port so the remap is on screen before it can
// run, or unchanged when nothing was free.
func (m Model) forwardConflict(msg forwardConflictMsg) (Model, tea.Cmd) {
	m.mode = promptForward
	m.prompt.SetValue(msg.spec)
	m.notice = fmt.Sprintf("port %s is in use — no free port within %d above it", msg.port, portSearch)
	if msg.next != "" {
		_, field := localPort(msg.spec)
		m.prompt.SetValue(withLocalPort(msg.spec, field, msg.next))
		m.notice = fmt.Sprintf("port %s is in use — %s is free: enter uses it, esc cancels", msg.port, msg.next)
	}
	m.prompt.CursorEnd()
	return m, m.prompt.Focus()
}
