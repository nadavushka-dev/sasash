package ui

import (
	"context"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Tunnel lifecycle in the pane (SASASH.4.19), completing add/connect/
// disconnect/delete. connect is space, disconnect is x. This file adds the
// two persistence ends: n declares a new set (desire only, nothing started)
// and D deletes a set's declaration — a confirmed, and for a live set
// two-step (down then delete), destructive action. D on an ad-hoc row (state
// only, no declaration) forgets the record instead, killing it first if live.

// fwdDeletedMsg reports a D outcome. name is the set name, or the spec for an
// ad-hoc forget; killErr rides the two-step / ad-hoc kill, removeErr the
// config write or the record purge.
type fwdDeletedMsg struct {
	host, name      string
	killErr         error
	removeErr       error
	adhoc, reconfig bool
}

// openDeclareFwd is n: declare a forward set on the host, desire only. It
// reuses the settings add-tunnel flow from the set-name prompt on (the host
// is the selected one, so the host prompt is skipped), and writes through
// fwdset.Add; nothing is started (space connects it).
func (m Model) openDeclareFwd(host hostmodel.Host) (Model, tea.Cmd) {
	if m.saveForwardSet == nil {
		m.notice = "declaring forward sets is not available"
		return m, nil
	}
	m.tunnel.host = host.Alias
	m.mode = promptTunnelName
	m.prompt.Reset()
	m.prompt.Placeholder = "new set name, e.g. web"
	return m, m.prompt.Focus()
}

// deleteFwd is D on the highlighted row: a named set's declaration is removed
// (confirmed; two-step when live), an ad-hoc record is forgotten.
func (m Model) deleteFwd(host hostmodel.Host, row tunnel.Forward) (Model, tea.Cmd) {
	host = m.tunnelHost(host)
	if row.Set == "" {
		return m.confirmForgetAdhoc(host, row), nil
	}
	return m.confirmDeleteSet(host, row.Set), nil
}

// declaredSpecs are the set's specs as sasash.conf declares them, nil when
// the set is not declared (only recorded).
func (m Model) declaredSpecs(host hostmodel.Host, name string) []string {
	for _, fs := range host.Forwards {
		if fs.Name == name {
			return fs.Specs
		}
	}
	return nil
}

// setIsLive reports whether any of the set's forwards is up (its process
// alive and its port answering).
func (m Model) setIsLive(host hostmodel.Host, name string) bool {
	rows, pending := m.forwardRows(host)
	if pending {
		return false
	}
	for _, f := range rows {
		if f.Set == name && f.State == tunnel.Up {
			return true
		}
	}
	return false
}

// declLine reconstructs the meta line D removes, exactly as fwdset writes it.
func declLine(name string, specs []string) string {
	return "# @sasash forwards " + name + " " + strings.Join(specs, " ")
}

// confirmDeleteSet opens the delete confirmation for a declared set: the exact
// line to be removed, and — when the set is live — the down-then-delete
// two-step. A set that is not declared (only recorded) is not D's to delete.
func (m Model) confirmDeleteSet(host hostmodel.Host, name string) Model {
	if m.removeForwardSet == nil {
		m.notice = "deleting forward sets is not available"
		return m
	}
	specs := m.declaredSpecs(host, name)
	if len(specs) == 0 {
		m.notice = name + " is not declared on " + host.Alias + " — x kills its forwards, it is not in sasash.conf"
		return m
	}
	alias := host.Alias
	line := declLine(name, specs)
	if m.setIsLive(host, name) {
		pids := m.setPids(host, name)
		lines := []string{
			name + " is live — delete downs it first, then removes the declaration:",
			"  " + strings.Join(killArgv(firstPid(pids)), " ") + more(len(pids)),
			"  removes  " + line,
		}
		return m.ask("delete "+name+" on "+alias, lines, "downed and deleted", name+" kept",
			func(m Model) (Model, tea.Cmd) { return m, m.killThenRemoveSetCmd(alias, name, specs) })
	}
	lines := []string{
		"remove this declaration from sasash.conf:",
		"  " + line,
		"the tunnels are not running — nothing is disconnected.",
	}
	return m.ask("delete "+name+" on "+alias, lines, "deleted", name+" kept",
		func(m Model) (Model, tea.Cmd) { return m, m.removeSetCmd(alias, name) })
}

// confirmForgetAdhoc opens the confirmation for D on an ad-hoc row: the record
// is purged (killed first when live). No sasash.conf change — an ad-hoc is
// state only.
func (m Model) confirmForgetAdhoc(host hostmodel.Host, row tunnel.Forward) Model {
	alias, spec := host.Alias, row.Spec
	lines := []string{"forget this ad-hoc forward:", "  " + spec}
	verb := "forgot"
	if row.Pid > 0 {
		lines = append(lines, "it is live — its process is killed first, and its connections drop.")
		verb = "killed and forgot"
	}
	lines = append(lines, "nothing in sasash.conf changes — an ad-hoc is state only.")
	return m.ask("forget ad-hoc on "+alias, lines, verb, spec+" kept",
		func(m Model) (Model, tea.Cmd) { return m, m.forgetAdhocCmd(alias, spec) })
}

// removeSetCmd deletes a declared (not-live) set's line through fwdset.Remove.
func (m Model) removeSetCmd(alias, name string) tea.Cmd {
	remove := m.removeForwardSet
	m.tinflight[alias] = true // the write and the reload own the host: no poll meanwhile
	return func() tea.Msg {
		return fwdDeletedMsg{host: alias, name: name, removeErr: remove(alias, name), reconfig: true}
	}
}

// killThenRemoveSetCmd downs a live set (KillSet) then removes its
// declaration — the two-step the confirm previewed. The declaration is
// removed even if a process outlives the kill (the survivor stays recorded
// and killable); the kill error rides along for the notice.
func (m Model) killThenRemoveSetCmd(alias, name string, specs []string) tea.Cmd {
	eng, remove := m.tunnels, m.removeForwardSet
	m.tinflight[alias] = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer cancel()
		killErr := eng.KillSet(ctx, alias, name, specs)
		return fwdDeletedMsg{host: alias, name: name, killErr: killErr, removeErr: remove(alias, name), reconfig: true}
	}
}

// forgetAdhocCmd kills an ad-hoc's process (if live) then forgets its record.
// No config write. A process that outlives SIGTERM keeps its record (Kill
// errors and leaves it), and Forget is SKIPPED then — never orphan a live
// ssh -N with no record, exactly as the named-live path keeps survivors
// killable via x (parity, QA round 1).
func (m Model) forgetAdhocCmd(alias, spec string) tea.Cmd {
	eng := m.tunnels
	m.tinflight[alias] = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer cancel()
		if killErr := eng.Kill(ctx, alias, spec); killErr != nil {
			return fwdDeletedMsg{host: alias, name: spec, killErr: killErr, adhoc: true}
		}
		return fwdDeletedMsg{host: alias, name: spec, removeErr: eng.Forget(alias, spec), adhoc: true}
	}
}

// firstPid is pids[0] or 0.
func firstPid(pids []int) int {
	if len(pids) == 0 {
		return 0
	}
	return pids[0]
}

// more renders "  +N more" for n pids past the first, "" for one or none.
func more(n int) string {
	if n <= 1 {
		return ""
	}
	return "  +" + strconv.Itoa(n-1) + " more"
}
