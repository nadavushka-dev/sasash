package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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
// A running master is not touched — its socket is keyed by the OLD
// connection params — so it keeps working and the next connect uses the
// new alias; the notice says so.

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
		m.notice = alias + " is " + parent + "'s pinned variant for path " + name + " — rename " + parent + ": its variants follow"
		return m, nil
	}
	if !m.inConf(host) {
		// Managed in the list's eyes or not, the block is not in the one
		// file sasash writes.
		where := "by hand (e)"
		if host.File != "" {
			where = "in " + host.File + " yourself (e)"
		}
		m.notice = alias + " is not in sasash.conf — rename it " + where
		return m, nil
	}
	if m.switching[alias] || m.tinflight[alias] {
		// Both write files keyed by the alias (sasash.conf, the caches).
		m.notice = alias + " is busy — a path switch or tunnel action is running; try again"
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

// inConf reports a host whose block lives in sasash.conf (paths compared
// cleaned, as managed does).
func (m Model) inConf(h hostmodel.Host) bool {
	return m.confPath != "" && h.File != "" && filepath.Clean(h.File) == filepath.Clean(m.confPath)
}

// submitRename is enter in the rename prompt. The 2.3 save rules gate
// the name first — the collision against EVERY listed host, ambient
// tier included, is this side's (the writer sees only sasash.conf), for
// the alias and for each "<new>.<name>" the fan-out would create.
func (m Model) submitRename() (Model, tea.Cmd) {
	old, new := m.renaming, strings.TrimSpace(m.prompt.Value())
	if new == old {
		m.notice = "unchanged — esc cancels"
		return m, nil
	}
	if new == "" {
		m.notice = `alias "": want a single plain word`
		return m, nil
	}
	if problem := m.aliasProblem(new); problem != "" {
		m.notice = problem
		return m, nil
	}
	for _, name := range variantsOf(old, m.hosts, m.fold.variants) {
		if problem := m.aliasProblem(new + "." + name); problem != "" {
			m.notice = problem
			return m, nil
		}
	}
	m.mode = promptNone
	m.prompt.Blur()
	m.notice = "renaming " + old + " → " + new + " …"
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
// tell (blocks that did not follow, a cache that stayed) the popup
// carries it, with the live-master caveat.
func (m Model) renamed(msg renameDoneMsg) (Model, tea.Cmd) {
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
	// A master up under the old alias is not touched: its socket is
	// keyed by the old connection params. Said on the line when it is
	// known to be running — the pane keeps showing it as the old name
	// until it exits.
	ts, known := m.tstatus[msg.old]
	masterUp := known && ts.err == nil && ts.status.Master
	delete(m.blocks, msg.old)
	delete(m.tstatus, msg.old)
	cmd := m.setRows(m.listRows())
	if m.reselect == msg.old {
		m.reselect = msg.new // under a filter the match set lands later
	}
	m.notice = renameNotice(msg, masterUp)
	if len(msg.res.LeftAsIs) > 0 || msg.err != nil {
		m = m.show("renamed "+msg.old+" → "+msg.new, renameLines(msg)...)
	}
	if m.loadHosts != nil {
		cmd = tea.Batch(cmd, m.reloadCmd(false))
	}
	return m, cmd
}

// renameNotice is the one line: the rename, the fan-out count, and the
// master caveat only when a master is up.
func renameNotice(msg renameDoneMsg, masterUp bool) string {
	s := "renamed " + msg.old + " → " + msg.new
	if n := len(msg.res.Followed); n > 0 {
		s += fmt.Sprintf(" (%d %s followed)", n, plural(n, "variant"))
	}
	if masterUp {
		s += " — master still up as " + msg.old + " until it exits"
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
		lines = append(lines, cleanLine(msg.err.Error()), "  (the observed history stays under "+msg.old+")")
	}
	return append(lines, "",
		"a running master is not touched: its socket is keyed by the old",
		"connection, so it keeps working under "+msg.old+" until it exits;",
		"the next connect uses "+msg.new)
}
