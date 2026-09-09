package ui

import (
	"context"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Tunnel lifecycle in the pane (SASASH.4.19), completing add/connect/
// disconnect/delete. connect is space, disconnect is x. This file adds the
// two persistence ends: n declares a new set (desire only, nothing started)
// and X deletes a set's declaration — immediately, no confirm (SASASH.4.30:
// the popup was friction), and for a live set a two-step (down then delete).
// Not silent: the notice says what went, and how many tunnels stopped. X on
// an ad-hoc row (state only, no declaration) forgets the record instead,
// killing it first if live.

// fwdDeletedMsg reports an X outcome. name is the set name, or the spec for
// an ad-hoc forget; killErr rides the two-step / ad-hoc kill, removeErr the
// config write or the record purge; stopped counts the processes the kill
// was sent to, for the notice.
type fwdDeletedMsg struct {
	host, name      string
	killErr         error
	removeErr       error
	stopped         int
	adhoc, reconfig bool
}

// openDeclareFwd is n: declare a forward set on the host, desire only. It
// reuses the settings add-tunnel flow from the set-name prompt on (the host
// is the selected one, so the host prompt is skipped), and writes through
// fwdset.Add; nothing is started (s connects it).
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

// deleteFwd is X on the highlighted row: a named set's declaration is removed
// at once (two-step when live), an ad-hoc record is forgotten. No confirm —
// so the in-flight gate is what makes a second X a no-op (a live kill can
// take seconds; a second delete would race the first and report a false
// failure), and the notice says the deed is under way meanwhile.
func (m Model) deleteFwd(host hostmodel.Host, row tunnel.Forward) (Model, tea.Cmd) {
	host = m.tunnelHost(host)
	if m.tinflight[host.Alias] {
		m.notice = busyNotice(host.Alias) // a poll or an action owns the host: say so, X is not a queue
		return m, nil
	}
	if row.Set == "" {
		m.notice = "forgetting " + row.Spec + " on " + host.Alias + " …"
		return m, m.forgetAdhocCmd(host.Alias, row.Spec, row.Pid > 0)
	}
	return m.deleteSet(host, row.Set)
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

// deleteSet deletes a declared set: a live one is downed first, then the
// declaration is removed. A set that is not declared (only recorded) is
// not X's to delete.
func (m Model) deleteSet(host hostmodel.Host, name string) (Model, tea.Cmd) {
	if m.removeForwardSet == nil {
		m.notice = "deleting forward sets is not available"
		return m, nil
	}
	specs := m.declaredSpecs(host, name)
	if len(specs) == 0 {
		m.notice = name + " is not declared on " + host.Alias + " — x kills its forwards, it is not in sasash.conf"
		return m, nil
	}
	m.notice = "deleting " + name + " on " + host.Alias + " …"
	if m.setIsLive(host, name) {
		return m, m.killThenRemoveSetCmd(host.Alias, name, specs, len(m.setPids(host, name)))
	}
	return m, m.removeSetCmd(host.Alias, name)
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
// declaration — the two-step. The declaration is removed even if a process
// outlives the kill (the survivor stays recorded and killable); the kill
// error rides along for the notice. stopped is the count of rows observed
// UP at press time — what the user saw stop — not everything KillSet may
// have signalled (it also ends recorded processes whose port never
// answered; the engine does not report a count).
func (m Model) killThenRemoveSetCmd(alias, name string, specs []string, stopped int) tea.Cmd {
	eng, remove := m.tunnels, m.removeForwardSet
	m.tinflight[alias] = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer cancel()
		killErr := eng.KillSet(ctx, alias, name, specs)
		return fwdDeletedMsg{host: alias, name: name, killErr: killErr, removeErr: remove(alias, name), stopped: stopped, reconfig: true}
	}
}

// forgetAdhocCmd kills an ad-hoc's process (if live) then forgets its record.
// No config write. A process that outlives SIGTERM keeps its record (Kill
// errors and leaves it), and Forget is SKIPPED then — never orphan a live
// ssh -N with no record, exactly as the named-live path keeps survivors
// killable via x (parity, QA round 1).
func (m Model) forgetAdhocCmd(alias, spec string, live bool) tea.Cmd {
	eng := m.tunnels
	m.tinflight[alias] = true
	stopped := 0
	if live {
		stopped = 1
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer cancel()
		if killErr := eng.Kill(ctx, alias, spec); killErr != nil {
			return fwdDeletedMsg{host: alias, name: spec, killErr: killErr, adhoc: true}
		}
		return fwdDeletedMsg{host: alias, name: spec, removeErr: eng.Forget(alias, spec), stopped: stopped, adhoc: true}
	}
}

// stoppedSuffix is the notice's tail for a delete that downed n processes:
// " — 1 tunnel stopped", " — 2 tunnels stopped", "" for none.
func stoppedSuffix(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return " — 1 tunnel stopped"
	}
	return " — " + strconv.Itoa(n) + " tunnels stopped"
}
