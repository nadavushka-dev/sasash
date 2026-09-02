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
// old spec, and whether it is live — master alive AND the row up — which
// decides whether saving also re-plumbs the forward on the master.
type editFwdState struct {
	host, set, old string
	live           bool
}

// editFwdDoneMsg reports the edit: done is the success notice.
type editFwdDoneMsg struct {
	host, done string
	err        error
}

// openEditFwd is E on a highlighted row: the prompt opens PREFILLED with
// the spec, bound to the row.
func (m Model) openEditFwd(host hostmodel.Host, row tunnel.Forward) (tea.Model, tea.Cmd) {
	ts, known := m.tstatus[host.Alias]
	live := known && ts.err == nil && ts.status.Master && row.State == tunnel.Up
	if row.Set == "" && !live {
		m.notice = "ad-hoc forward is not live — x it and + a new one"
		return m, nil
	}
	if row.Set != "" && m.saveForwardEdit == nil && !live {
		m.notice = "editing declared forwards is not available"
		return m, nil
	}
	m.editFwd = editFwdState{host: host.Alias, set: row.Set, old: row.Spec, live: live}
	m.mode = promptEditFwd
	m.prompt.Reset()
	m.prompt.Placeholder = "local:host:port"
	m.prompt.SetValue(row.Spec)
	m.prompt.CursorEnd()
	return m, m.prompt.Focus()
}

// editFwdPlan lists exactly what enter will run for the typed spec: the
// config rewrite (one meta line) and, when the forward is live, the
// per-spec surgery on the master — cancel the old, forward the new,
// every other forward untouched.
func (m Model) editFwdPlan(newSpec string) []string {
	e := m.editFwd
	var plan []string
	if e.set != "" && m.saveForwardEdit != nil {
		plan = append(plan, fmt.Sprintf("sasash.conf: forwards %s: %s → %s", e.set, e.old, newSpec))
	}
	if e.live {
		plan = append(plan,
			strings.Join(cancelArgv(e.host, e.old), " "),
			strings.Join(forwardArgv(e.host, newSpec), " "))
	}
	if len(plan) == 0 {
		plan = append(plan, "nothing to change")
	}
	return plan
}

// submitEditFwd is enter in the edit prompt.
func (m Model) submitEditFwd() (tea.Model, tea.Cmd) {
	newSpec := strings.TrimSpace(m.prompt.Value())
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
			if err := eng.Cancel(ctx, e.host, e.old); err != nil {
				msg := fmt.Sprintf("cancel of %s failed: %s", e.old, oneLine(err, e.host))
				if configEdit {
					msg += " (config updated)"
				}
				return editFwdDoneMsg{host: e.host, err: errors.New(msg)}
			}
			if err := eng.Forward(ctx, e.host, e.set, newSpec); err != nil {
				return editFwdDoneMsg{host: e.host, err: fmt.Errorf("cancelled %s but forward %s failed: %s", e.old, newSpec, oneLine(err, e.host))}
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
