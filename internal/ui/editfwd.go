package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// editFwdState is the row E is editing: its host, set ("" = ad-hoc),
// old spec, and whether it is live — the row up, served by a process of
// ours (pid) — which decides whether saving also re-plumbs the forward:
// kill the old process, spawn the new spec.
type editFwdState struct {
	host, set, old string
	live           bool
	pid            int
}

// editFwdDoneMsg reports the edit: done is the success notice.
type editFwdDoneMsg struct {
	host, done string
	err        error
}

// openEditFwd is E on a highlighted row: the prompt opens PREFILLED with
// the spec, bound to the row.
func (m Model) openEditFwd(host hostmodel.Host, row tunnel.Forward) (tea.Model, tea.Cmd) {
	host = m.tunnelHost(host)
	ts, known := m.tstatus[host.Alias]
	live := known && ts.err == nil && row.State == tunnel.Up && row.Pid > 0
	if row.Set == "" && !live {
		m.notice = "ad-hoc forward is not live — x it and + a new one"
		return m, nil
	}
	if row.Set != "" && m.saveForwardEdit == nil && !live {
		m.notice = "editing declared forwards is not available"
		return m, nil
	}
	m.editFwd = editFwdState{host: host.Alias, set: row.Set, old: row.Spec, live: live, pid: row.Pid}
	m.mode = promptEditFwd
	m.prompt.Reset()
	m.prompt.Placeholder = "local:host:port"
	m.prompt.SetValue(row.Spec)
	m.prompt.CursorEnd()
	return m, m.prompt.Focus()
}

// editFwdPlan lists exactly what enter will run for the typed spec: the
// config rewrite (one meta line) and, when the forward is live, the
// per-process surgery — kill the old process, spawn the new spec, every
// other forward untouched.
func (m Model) editFwdPlan(newSpec string) []string {
	e := m.editFwd
	var plan []string
	if e.set != "" && m.saveForwardEdit != nil {
		plan = append(plan, fmt.Sprintf("sasash.conf: forwards %s: %s → %s", e.set, e.old, newSpec))
	}
	if e.live {
		plan = append(plan,
			strings.Join(killArgv(e.pid), " "),
			strings.Join(spawnArgv(e.host, newSpec), " "))
	}
	if len(plan) == 0 {
		plan = append(plan, "nothing to change")
	}
	return plan
}

// submitEditFwd is enter in the edit prompt.
func (m Model) submitEditFwd() (tea.Model, tea.Cmd) {
	newSpec, _ := expandSpec(m.prompt.Value())
	if port, _ := localPort(newSpec); port == "" {
		m.notice = "spec " + strconv.Quote(newSpec) + ": no local port — want local:host:port"
		return m, nil
	}
	e := m.editFwd
	if newSpec == e.old {
		m.notice = "unchanged"
		return m, nil
	}
	m.mode = promptNone
	m.prompt.Blur()
	save, eng := m.saveForwardEdit, m.tunnels
	configEdit := e.set != "" && save != nil
	m.tinflight[e.host] = true // the surgery writes the caches: no poll meanwhile
	return m, func() tea.Msg {
		if configEdit {
			if err := save(e.host, e.set, e.old, newSpec); err != nil {
				return editFwdDoneMsg{host: e.host, err: fmt.Errorf("not declared: %v", err)}
			}
		}
		done := "declared " + newSpec + " in " + setLabel(e.set)
		if e.live {
			ctx, cancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
			defer cancel()
			// ssh's reason, without the argv it echoes (shown on the row).
			if err := eng.Kill(ctx, e.host, e.old); err != nil {
				msg := fmt.Sprintf("kill of %s failed: %s", e.old, oneLine(err, e.host))
				if configEdit {
					msg += " (config updated)"
				}
				return editFwdDoneMsg{host: e.host, err: errors.New(msg)}
			}
			if err := eng.Spawn(ctx, e.host, e.set, newSpec); err != nil {
				return editFwdDoneMsg{host: e.host, err: fmt.Errorf("killed %s but spawn of %s failed: %s", e.old, newSpec, oneLine(err, e.host))}
			}
			// An ad-hoc record is its own declaration: without the purge
			// the old spec lingers as a dead row (CONTRACT-tunnel C1).
			if e.set == "" {
				if err := eng.Forget(e.host, e.old); err != nil {
					return editFwdDoneMsg{host: e.host, err: fmt.Errorf("re-plumbed %s → %s, but the old record stays: %s", e.old, newSpec, oneLine(err, e.host))}
				}
			}
			done = "re-plumbed " + e.old + " → " + newSpec
			if configEdit {
				done += " (and declared)"
			}
		}
		return editFwdDoneMsg{host: e.host, done: done}
	}
}

// oneLineText folds a multi-line ssh message onto the notice line.
func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// expandedOrTyped is the canonical form of a typed spec for display: the
// expansion, or the text as typed when it is not a shorthand.
func expandedOrTyped(input string) string {
	full, _ := expandSpec(input)
	return full
}

// promptExpansion is the full expansion the prompt shows under a
// shorthand: what enter will really use. Empty when nothing typed is a
// shorthand (already canonical, or not parseable).
func (m Model) promptExpansion() string {
	v := strings.TrimSpace(m.prompt.Value())
	if v == "" {
		return ""
	}
	switch m.mode {
	case promptForward:
		if full, expanded := expandSpec(strings.TrimSpace(strings.TrimPrefix(v, "!"))); expanded {
			return full
		}
	case promptEditFwd:
		if full, expanded := expandSpec(v); expanded {
			return full
		}
	case promptTunnelSpecs:
		var out []string
		changed := false
		for _, s := range strings.Fields(v) {
			full, expanded := expandSpec(s)
			if port, _ := localPort(full); port == "" {
				return "" // enter will refuse this token; promise nothing
			}
			changed = changed || expanded
			out = append(out, full)
		}
		if changed {
			return strings.Join(out, " ")
		}
	case promptConnect, promptNewConn:
		// Token by token: any -L value that is a shorthand means the
		// command ssh gets differs from what was typed — as does a target
		// the selection supplied (the a prompt only; a new connection is
		// a new host, never the selected one).
		selected := ""
		if m.mode == promptConnect {
			selected = m.selectedAlias()
		}
		if t, err := parseAdhocFor(v, selected); err == nil {
			// A -N on a saved host runs through the engine (4.17): its
			// flow is what the line shows, always — it is never what
			// was typed.
			saved := ""
			if t.TunnelOnly && m.mode == promptConnect {
				// What enter will refuse is said here, not promised.
				if why := m.tunnelRefusal(t); why != "" {
					return why
				}
				saved, _ = m.savedTunnelTarget(t)
			}
			if saved == "" && !anyForwardShorthand(v) && !t.FromSelection {
				return ""
			}
			argv := adhocArgv(t)
			if t.TunnelOnly {
				if m.mode == promptNewConn {
					return "" // enter refuses -N there; no command to show
				}
				argv = adhocTunnelArgv(t)
			}
			line := strings.Join(argv, " ")
			if saved != "" {
				steps := m.engineTunnelPreview(saved, t.Forwards)
				if len(steps) == 0 {
					return "" // a live master and nothing to add: nothing would run
				}
				line = strings.Join(steps, " ; ")
			}
			if t.FromSelection {
				// The disclosure leads: the line's tail is what a narrow
				// frame cuts (4.9's ellipsis), and the tail is the part
				// the user typed and can reconstruct.
				line = "target: " + t.Host + " (selected) · " + line
			}
			return line
		}
	}
	return ""
}

// anyForwardShorthand reports whether any -L value in the input (bare or
// glued) is a shorthand.
func anyForwardShorthand(input string) bool {
	fields := strings.Fields(input)
	for i, f := range fields {
		spec := ""
		switch {
		case f == "-L" && i+1 < len(fields):
			spec = fields[i+1]
		case strings.HasPrefix(f, "-L") && len(f) > 2:
			spec = f[2:]
		}
		if spec != "" {
			if _, expanded := expandSpec(spec); expanded {
				return true
			}
		}
	}
	return false
}
