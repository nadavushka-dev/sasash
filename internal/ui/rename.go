package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
)

// Rename a managed host from the TUI (SASASH.4.16): R opens the prompt
// PREFILLED with the alias; enter renames through the RenameHost hook
// — pathswitch.Rename on sasash.conf, then the state-cache files, both
// wired by the cmd layer — and esc cancels. Only a block in sasash.conf
// is sasash's to rename: an ambient host is pointed at e, a pinned
// variant at its parent (a parent's rename carries its variants along).
// A running master is not touched — and with sasash's ControlPath
// (~/.ssh/cm/%C, a hash of HostName, port and user, none of which a
// rename changes) the same socket answers under the new alias at the
// next poll; the notice says so.

// ErrCacheKept marks a rename that reached sasash.conf but could not
// move a state-cache file: the host IS renamed (the list reloads), and
// the notice says the observed history stayed under the old name. The
// cmd layer wraps its os.Rename failure in it.
var ErrCacheKept = errors.New("state cache not renamed")

// renameDoneMsg reports the hook's outcome.
type renameDoneMsg struct {
	old, new string
	res      pathswitch.RenameResult
	err      error
}

// openRename is R on the selected host: the prompt, or the reason there
// is none.
func (m Model) openRename() (Model, tea.Cmd) {
	host, ok := m.Selected()
	if !ok {
		return m, nil
	}
	if m.renameHost == nil {
		m.notice = "renaming is not available"
		return m, nil
	}
	alias := host.Alias
	if name, ok := m.fold.variants[alias]; ok {
		parent := strings.TrimSuffix(alias, "."+name)
		// Listed as a variant by the fold's display witnesses; whether
		// the writer carries it along is the writer's call (LeftAsIs).
		m.notice = alias + " is listed as " + parent + "'s pinned variant for path " + name + " — rename " + parent + " instead"
		return m, nil
	}
	if !m.inConf(host) {
		// Managed in the list's eyes or not, the block is not in the one
		// file sasash writes. e is named only when it works.
		where := "by hand"
		if host.File != "" {
			where = "in " + host.File + " yourself"
		}
		if len(m.editor) > 0 && m.confPath != "" {
			where += " (e)"
		}
		m.notice = alias + " is not in sasash.conf — rename it " + where
		return m, nil
	}
	if m.switching[alias] || m.tinflight[alias] {
		// Both write files keyed by the alias (sasash.conf, the caches).
		m.notice = busyNotice(alias)
		return m, nil
	}
	m.renaming = alias
	m.mode = promptRename
	m.prompt.Reset()
	m.prompt.Placeholder = "new alias — esc cancels"
	m.prompt.SetValue(alias)
	m.prompt.CursorEnd()
	return m, m.prompt.Focus()
}

// busyNotice is the one-writer-per-host refusal.
func busyNotice(alias string) string {
	// tinflight also marks a plain status check (issued on every cursor
	// move): named, so the user is not sent chasing an action.
	return alias + " is busy — a status check or an action is running; try again"
}

// inConf reports a host whose block lives in sasash.conf (paths compared
// cleaned, as managed does).
func (m Model) inConf(h hostmodel.Host) bool {
	return m.confPath != "" && h.File != "" && filepath.Clean(h.File) == filepath.Clean(m.confPath)
}

// submitRename is enter in the rename prompt. The 2.3 save rules gate
// the name first — the collision against EVERY listed host, ambient
// tier included, is this side's (the writer sees only sasash.conf), for
// the alias and for each "<new>.<x>" the fan-out could create: every
// "<old>.<x>" block, not just the ones the fold lists as variants — the
// writer's ownership test is its own, and a hand-edited address must not
// let a generated block shadow a user's host in another file.
func (m Model) submitRename() (Model, tea.Cmd) {
	old, new := m.renaming, strings.TrimSpace(m.prompt.Value())
	if new == old {
		m.notice = "unchanged — esc cancels"
		return m, nil
	}
	// A slash or a dot-name would make a cache file the store refuses
	// to write (CacheRenames moves nothing then): the history would be
	// silently left behind.
	if new == "" || new == "." || new == ".." || strings.Contains(new, "/") {
		m.notice = "alias " + strconv.Quote(new) + ": want a single plain word"
		return m, nil
	}
	if problem := m.aliasProblem(new); problem != "" {
		m.notice = problem
		return m, nil
	}
	for _, h := range m.hosts {
		if suffix, dotted := strings.CutPrefix(h.Alias, old+"."); dotted {
			if problem := m.aliasProblem(new + "." + suffix); problem != "" {
				m.notice = problem
				return m, nil
			}
		}
	}
	// The gate read at R, re-read at enter (a poll may have started
	// while the prompt was open) and then HELD until the outcome lands:
	// the rename moves the files a poll writes.
	if m.switching[old] || m.tinflight[old] {
		m.mode = promptNone
		m.prompt.Blur()
		m.notice = busyNotice(old)
		return m, nil
	}
	m.mode = promptNone
	m.prompt.Blur()
	m.notice = "renaming " + old + " → " + new + " …"
	m.tinflight[old] = true
	rename := m.renameHost
	return m, func() tea.Msg {
		res, err := rename(old, new)
		return renameDoneMsg{old: old, new: new, res: res, err: err}
	}
}

// renamed is the hook's outcome. On success the rows follow at once —
// the cursor stays on its row, and the reload then keeps the selection
// by the NEW alias — and the config is re-read so the panes show the
// file as written. The line says the rename; when there is more to
// tell (blocks that did not follow, a cache that stayed, a master up)
// the popup carries it, with why the master follows.
func (m Model) renamed(msg renameDoneMsg) (Model, tea.Cmd) {
	delete(m.tinflight, msg.old) // the gate held since enter, whatever the outcome
	if msg.err != nil && !errors.Is(msg.err, ErrCacheKept) {
		// The writer's words verbatim: it can name a dotted alias the
		// user did not type ("alias "web.lan" already exists").
		m.notice = "rename failed: " + cleanLine(msg.err.Error())
		return m, nil
	}
	hosts := slices.Clone(m.hosts)
	for i, h := range hosts {
		switch name, variant := strings.CutPrefix(h.Alias, msg.old+"."); {
		case h.Alias == msg.old:
			hosts[i].Alias = msg.new
		case variant && slices.Contains(msg.res.Followed, name):
			hosts[i].Alias = msg.new + "." + name
		}
	}
	m.hosts = hosts
	m.fold.variants = computeVariants(m.hosts)
	m.fold.hidden = hiddenSet(m.hosts)
	// A master up under the old alias is not touched: its socket is
	// keyed by the connection (%C), which the rename leaves as it was,
	// so the next poll finds it under the new alias. Said on the line
	// when it is known to be running.
	ts, known := m.tstatus[msg.old]
	masterUp := known && ts.err == nil && ts.status.Master
	delete(m.blocks, msg.old)
	delete(m.tstatus, msg.old)
	cmd := m.setRows(m.listRows())
	if m.reselect == msg.old {
		m.reselect = msg.new // under a filter the match set lands later
	}
	m.notice = renameNotice(msg, masterUp)
	if len(msg.res.LeftAsIs) > 0 || msg.err != nil || masterUp {
		m = m.show("renamed "+msg.old+" → "+msg.new, renameLines(msg)...)
	}
	if m.loadHosts != nil {
		cmd = tea.Batch(cmd, m.reloadCmd(false))
	}
	return m, cmd
}

// renameNotice is the one line: the rename, the fan-out count, and the
// master's fate only when one is up.
func renameNotice(msg renameDoneMsg, masterUp bool) string {
	s := "renamed " + msg.old + " → " + msg.new
	if n := len(msg.res.Followed); n > 0 {
		s += fmt.Sprintf(" (%d %s followed)", n, plural(n, "variant"))
	}
	if masterUp {
		s += " — the master follows"
	}
	return s
}

// renameLines is the popup's story when the line cannot hold it — in
// lines the box shows whole (it cuts, never wraps).
func renameLines(msg renameDoneMsg) []string {
	var lines []string
	if len(msg.res.Followed) > 0 {
		lines = append(lines, "pinned variants followed: "+strings.Join(msg.res.Followed, ", "))
	}
	if len(msg.res.LeftAsIs) > 0 {
		lines = append(lines, "dotted blocks left as-is: "+strings.Join(msg.res.LeftAsIs, ", "),
			"  (not written by sasash — rename them by hand, e)")
	}
	if msg.err != nil {
		// errors.Join separates the kept caches with a newline, which
		// cleanLine would delete: one line per file, so each is named.
		for _, l := range strings.Split(msg.err.Error(), "\n") {
			lines = append(lines, cleanLine(l))
		}
		lines = append(lines, "  (what did not move stays under "+msg.old+")")
	}
	return append(lines, "",
		"a running master is not restarted; with sasash's ControlPath (~/.ssh/cm/%C)",
		"its socket is keyed by the connection, so the same master answers under",
		msg.new+" at the next poll — an alias-keyed ControlPath would need a reconnect")
}
