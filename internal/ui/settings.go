package ui

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/setup"
)

// settingsPane says which settings screen, if any, owns the body.
type settingsPane int

const (
	settingsOff   settingsPane = iota
	settingsMenu               // the entry list
	settingsSetup              // setup (create files): what will be written
	settingsTheme              // the theme picker
	settingsSpawn              // the spawn target picker
)

// Settings entries, in menu order.
const (
	entryCreate = iota
	entryEdit
	entryTheme
	entrySpawn
	entryConnection
	entryTunnel
	entryCount
)

var entryLabels = [entryCount]string{
	entryCreate:     "setup (create files)",
	entryEdit:       "edit connections file",
	entryTheme:      "theme",
	entrySpawn:      "spawn target",
	entryConnection: "add new connection",
	entryTunnel:     "add new tunnel",
}

// settingsState is the `o` panel: a discoverable surface over machinery
// that already exists (setup, the editor handoff, themes, the ad-hoc
// save). It never holds logic of its own.
type settingsState struct {
	pane   settingsPane
	cursor int    // menu entry, or theme index in the picker
	find   finder // the / search over the menu entries (4.14)

	// create config: the last setup.Check, nil until it lands.
	status   *setup.Status
	checkErr error
}

// setupStatusMsg carries a setup.Check result; setupDoneMsg a setup.Ensure
// outcome.
type setupStatusMsg struct {
	status setup.Status
	err    error
}
type setupDoneMsg struct{ err error }

// setupCheckCmd inspects env off the UI thread; nil without a home.
func setupCheckCmd(env setup.Env) tea.Cmd {
	if env.Home == "" {
		return nil
	}
	return func() tea.Msg {
		st, err := setup.Check(env)
		return setupStatusMsg{status: st, err: err}
	}
}

// setupEnsureCmd runs the one-time bootstrap off the UI thread.
func setupEnsureCmd(env setup.Env) tea.Cmd {
	return func() tea.Msg { return setupDoneMsg{err: setup.Ensure(env)} }
}

// openSettings is `o`. The panel supplies its own help, so an expanded
// `?` is folded away; a save-first flag from an earlier run cannot
// outlive this point.
func (m Model) openSettings() Model {
	m.settings = settingsState{pane: settingsMenu}
	m.saveThenConnect = false
	return m
}

// settingsKey routes a key while the panel owns the keyboard: vim
// navigation, enter selects, esc backs out one level, / searches the
// menu. Every action key is inert here, as while filtering.
func (m Model) settingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.settings
	if s.pane == settingsMenu {
		if s.find.typing {
			return m.settingsFindKey(msg)
		}
		if key.Matches(msg, m.keys.Filter) {
			s.find = finder{typing: true}
			return m, nil
		}
	}
	switch {
	case key.Matches(msg, m.keys.ClearFilter):
		if s.pane == settingsMenu {
			s.pane = settingsOff
		} else {
			s.cursor = m.settingsEntry(s.pane)
			s.pane = settingsMenu
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		s.cursor = min(s.cursor+1, m.settingsRows()-1)
		return m, nil
	case key.Matches(msg, m.keys.Up):
		s.cursor = max(s.cursor-1, 0)
		return m, nil
	case key.Matches(msg, m.keys.Top):
		s.cursor = 0
		return m, nil
	case key.Matches(msg, m.keys.Bottom):
		s.cursor = m.settingsRows() - 1
		return m, nil
	case key.Matches(msg, m.keys.Help):
		return m.openHelp(), nil
	case msg.Type == tea.KeyEnter:
		return m.settingsEnter()
	}
	return m, nil
}

// settingsFindKey is a key while the menu search is being typed: text
// narrows the entries and the highlight lands on the first match, the
// arrows move it among the matches (j/k are text here), enter activates
// the highlighted entry and drops the search, esc drops it and leaves
// the full menu with the highlight where it was.
func (m Model) settingsFindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.settings
	if msg.Type == tea.KeyEnter {
		if !slices.Contains(m.settingsMatches(), s.cursor) {
			return m, nil // nothing highlighted: nothing to activate
		}
		s.find = finder{}
		return m.settingsEnter()
	}
	if s.find.key(msg) {
		if hits := m.settingsMatches(); len(hits) > 0 && s.find.typing {
			s.cursor = hits[0]
		}
		return m, nil
	}
	hits := m.settingsMatches()
	at := slices.Index(hits, s.cursor)
	switch {
	case key.Matches(msg, m.keys.Down) && at >= 0:
		s.cursor = hits[min(at+1, len(hits)-1)]
	case key.Matches(msg, m.keys.Up) && at >= 0:
		s.cursor = hits[max(at-1, 0)]
	}
	return m, nil
}

// settingsMatches is the menu entries the search leaves, in menu order;
// all of them without a query.
func (m Model) settingsMatches() []int {
	var hits []int
	for i, label := range entryLabels {
		if m.settings.find.matches(label) {
			hits = append(hits, i)
		}
	}
	return hits
}

// settingsRows is how many rows the current pane's cursor ranges over.
func (m Model) settingsRows() int {
	switch m.settings.pane {
	case settingsTheme:
		return len(Themes)
	case settingsSpawn:
		return len(SpawnTargets)
	case settingsMenu:
		return entryCount
	case settingsSetup:
		_, rows, _, _ := m.settingsContent()
		return len(rows)
	}
	return 1
}

// settingsEntry is the menu entry a sub-pane was opened from, so esc
// lands the cursor back on it.
func (m Model) settingsEntry(pane settingsPane) int {
	switch pane {
	case settingsSetup:
		return entryCreate
	case settingsTheme:
		return entryTheme
	case settingsSpawn:
		return entrySpawn
	}
	return 0
}

// settingsEnter acts on the highlighted row.
func (m Model) settingsEnter() (tea.Model, tea.Cmd) {
	s := &m.settings
	switch s.pane {
	case settingsMenu:
		switch s.cursor {
		case entryCreate:
			if m.setupEnv.Home == "" {
				m.notice = "no home directory known — cannot set up"
				return m, nil
			}
			s.pane, s.status, s.checkErr = settingsSetup, nil, nil
			return m, setupCheckCmd(m.setupEnv)
		case entryEdit:
			if len(m.editor) == 0 || m.confPath == "" {
				m.notice = "no editor configured"
				return m, nil
			}
			s.pane = settingsOff
			m.editing = m.confPath
			return m, tea.ExecProcess(settingsEditExec(m), func(err error) tea.Msg {
				return editorExitMsg{err: err}
			})
		case entryTheme:
			s.pane, s.cursor = settingsTheme, m.themeIdx
			return m, nil
		case entrySpawn:
			s.pane = settingsSpawn
			s.cursor, _ = SpawnTargetIndex(m.spawnTarget())
			return m, nil
		case entryConnection:
			s.pane = settingsOff
			m.mode = promptNewConn
			m.prompt.Reset()
			m.prompt.Placeholder = "[user@]host[:port] [-L local:host:port]"
			return m, m.prompt.Focus()
		case entryTunnel:
			if m.saveForwardSet == nil {
				m.notice = "declaring tunnels is not available"
				return m, nil
			}
			s.pane = settingsOff
			return m.openTunnelPrompt()
		}
	case settingsSetup:
		if s.checkErr != nil {
			// Unknown state is not a state to write from.
			m.notice = "cannot inspect ~/.ssh: " + s.checkErr.Error()
			return m, nil
		}
		if s.status == nil {
			return m, nil // still checking
		}
		if s.status.Done() {
			m.notice = "already set up — nothing to write"
			return m, nil
		}
		return m, setupEnsureCmd(m.setupEnv)
	case settingsTheme:
		return m.setTheme(s.cursor)
	case settingsSpawn:
		return m.setSpawnTarget(s.cursor)
	}
	return m, nil
}

// spawnTarget is the live target, defaulted.
func (m Model) spawnTarget() string {
	if _, ok := SpawnTargetIndex(m.spawn.target); ok {
		return m.spawn.target
	}
	return DefaultSpawnTarget
}

// setTheme applies and persists a theme choice: the `t` cycle and the
// picker share it.
func (m Model) setTheme(idx int) (tea.Model, tea.Cmd) {
	m = m.applyTheme(idx)
	m.notice = "theme: " + Themes[idx].Name
	if m.saveTheme == nil {
		return m, nil
	}
	save, name := m.saveTheme, Themes[idx].Name
	return m, func() tea.Msg { return themeSavedMsg{err: save(name)} }
}

// settingsEditExec is the ONE construction site of settings' editor
// process: always sasash.conf, whatever host is selected.
func settingsEditExec(m Model) *exec.Cmd { return editorExecOn(m.editor, m.confPath) }

// settingsView renders the panel in the popup box (4.14): the pane's
// title, its rows with the highlighted one kept in view, and a bottom
// line — the search while it is typed, else the pane's own footer.
func (m Model) settingsView() string {
	title, rows, cursor, foot := m.settingsContent()
	// The window is the fixed height, or what the frame leaves of it.
	height := min(settingsBoxRows, m.popupRoom())
	offset := max(cursor-height+1, 0)
	end := min(offset+height, len(rows))
	// The bottom line: the search while typed, else the pane's footer;
	// when the rows outgrow the box the scroll hint leads, so what is
	// hidden is never silent (the footer's tail yields on a narrow frame).
	var bottom []string
	if len(rows) > height {
		bottom = append(bottom, m.styles.PathExtra.Render(scrollHint(offset, end, len(rows), m.settings.find.typing)))
	}
	switch h := m.settings.find.hint(); {
	case h != "":
		bottom = append(bottom, m.styles.BoxQuery.Render(h))
	case foot != "":
		bottom = append(bottom, foot)
	}
	// The bottom line is always there, so the frame's height does not
	// change when a search or a footer appears.
	line := strings.Join(bottom, helpSeparator)
	if line == "" {
		line = " "
	}
	width := settingsBoxWidth
	if m.settings.pane == settingsSetup {
		width = 0 // the confirmation is as wide as the pieces it lists
	}
	return m.box(m.styles.BoxTitle.Render(title), rows[offset:end], line, false, width, height)
}

// The settings box's fixed frame (4.15): as wide as the menu's longest
// row with room to spare, as tall as the menu, clamped to the frame —
// so the box does not move as a search narrows the entries, a picker
// opens or between visits; the rows scroll or pad inside it. The setup
// confirmation alone takes the width its pieces need.
const (
	settingsBoxWidth = 60
	settingsBoxRows  = entryCount
)

// settingsContent is the open pane as the box shows it: title, rows,
// the row index to keep in view (-1 when none is highlighted) and the
// footer line. The setup pane's cursor is unmarked — its rows are read,
// not chosen — but it still moves, so j/k reach every piece on a frame
// too short for all three.
func (m Model) settingsContent() (title string, rows []string, cursor int, foot string) {
	s := m.settings
	cursor = -1
	// The highlighted row is the list's selected row: the mark, then the
	// label in accent over the selection fill (4.15); the others plain.
	// A picker's glyph goes before the label, outside its span: a styled
	// string nested in another ends both at its reset.
	label := func(i, cur int, glyph, text string) string {
		prefix, style := "  ", m.styles.PathName
		if i == cur {
			prefix, style = m.styles.RowMark.Render("› "), m.styles.ItemSelected
		}
		if glyph != "" {
			prefix += glyph + " "
		}
		return prefix + style.Render(text)
	}
	switch s.pane {
	case settingsMenu:
		title = "settings"
		// A live value (the theme, the target) in the secondary accent;
		// the config path is a location, dim.
		detail := [entryCount]string{
			entryEdit:  m.styles.PathExtra.Render(m.underHome(m.confPath)),
			entryTheme: m.styles.BoxValue.Render(Themes[m.themeIdx].Name),
			entrySpawn: m.styles.BoxValue.Render(m.spawnTarget()) + " " + m.styles.PathExtra.Render("("+m.spawn.where(m.spawnTarget())+")"),
		}
		hits := m.settingsMatches()
		if len(hits) == 0 {
			return title, []string{m.styles.Empty.Render(noMatch(s.find.query))}, -1, ""
		}
		for at, i := range hits {
			if i == s.cursor {
				cursor = at
			}
			row := label(i, s.cursor, "", entryLabels[i])
			if detail[i] != "" {
				row += "  " + detail[i]
			}
			rows = append(rows, row)
		}
	case settingsSetup:
		title = "setup — sasash writes exactly:"
		cursor = s.cursor
		type piece struct {
			text    string
			present func(setup.Status) bool
		}
		pieces := []piece{
			{setup.IncludeLine + "  → top of " + m.tildePath("config"), func(st setup.Status) bool { return st.IncludePresent }},
			{m.tildePath("config.d", "sasash.conf") + "  (connections file, one header line)", func(st setup.Status) bool { return st.ConfPresent }},
			{m.tildePath("cm") + "/  (control sockets)", func(st setup.Status) bool { return st.CMDirPresent }},
			{m.underHome(m.setupEnv.Config) + "  (every option commented out)", func(st setup.Status) bool { return st.ConfigPresent }},
		}
		for _, p := range pieces {
			glyph, word := m.styles.StatusWait.Render("◌"), "…"
			// An inspection failure leaves every piece unknown — never
			// "missing", which would invite a write from a guess.
			if s.status != nil && s.checkErr == nil {
				glyph, word = m.styles.StatusDown.Render("○"), "missing"
				if p.present(*s.status) {
					glyph, word = m.styles.StatusUp.Render("●"), "present"
				}
			}
			rows = append(rows, glyph+" "+m.styles.PathAddr.Render(p.text)+" "+m.styles.PathExtra.Render(word))
		}
		// Old settings files still to move into config.toml (4.36): known
		// only once the check lands, and gone once setup has run.
		if s.status != nil && s.checkErr == nil {
			for _, mv := range s.status.Legacy {
				verb := "move " + m.underHome(mv.Path) + " → " + mv.Key + " in config.toml"
				if mv.Value == nil {
					verb = "remove " + m.underHome(mv.Path) + "  (" + mv.Reason + ")"
				}
				rows = append(rows, m.styles.StatusDown.Render("○")+" "+m.styles.PathAddr.Render(verb)+" "+m.styles.PathExtra.Render("pending"))
			}
		}
		switch {
		case s.checkErr != nil:
			rows = append(rows, m.styles.Notice.Render("! "+s.checkErr.Error()))
		case s.status != nil && s.status.Done():
			foot = m.styles.Empty.Render("already set up — nothing to write; esc back")
		case s.status != nil:
			foot = m.styles.Empty.Render("enter writes the missing pieces, nothing else touched · esc back")
		}
	case settingsSpawn:
		title = "spawn target — where enter opens ssh; applies and persists"
		cursor = s.cursor
		for i, t := range SpawnTargets {
			// ● in effect, ○ not — a choice, not a failure, so no red.
			glyph := m.styles.BoxOff.Render("○")
			if t == m.spawnTarget() {
				glyph = m.styles.StatusUp.Render("●")
			}
			rows = append(rows, label(i, s.cursor, glyph, t)+"  "+m.styles.PathExtra.Render("herdr "+t+" · tmux "+tmuxWord(t)))
		}
		if m.spawn.mux == MuxNone {
			foot = m.styles.Empty.Render("no herdr or tmux here: enter uses the OS window, or hands off")
		}
	case settingsTheme:
		title = "theme — enter applies and persists"
		cursor = s.cursor
		for i, t := range Themes {
			glyph := m.styles.BoxOff.Render("○")
			if i == m.themeIdx {
				glyph = m.styles.StatusUp.Render("●")
			}
			rows = append(rows, label(i, s.cursor, glyph, t.Name))
		}
	}
	return title, rows, cursor, foot
}

// tildePath spells a path under ~/.ssh the way the Include line does.
func (m Model) tildePath(parts ...string) string {
	return "~/" + filepath.Join(append([]string{".ssh"}, parts...)...)
}

// underHome spells a path under the home directory with ~, the way the
// setup pane does — so the menu's box is as wide as its words, not the
// home's absolute path. Anything else is shown as it is.
func (m Model) underHome(path string) string {
	if rest, ok := strings.CutPrefix(path, m.home+"/"); ok && m.home != "" {
		return "~/" + rest
	}
	return path
}

// settingsKeys is the help shown while the panel owns the keyboard.
type settingsKeys struct{}

func (settingsKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		moveKeys,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back / close")),
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
	}
}

func (k settingsKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// newTunnel is the settings "add new tunnel" flow: three prompts —
// host (prefilled with the selection), set name, specs — then ONE write
// through the SaveForwardSet hook. It declares desire only; the set is
// started with space afterwards.
type newTunnel struct {
	host, name string
}

// tunnelSavedMsg reports the write outcome.
type tunnelSavedMsg struct {
	host, name string
	err        error
}

// openTunnelPrompt starts the flow on the host under the cursor.
func (m Model) openTunnelPrompt() (tea.Model, tea.Cmd) {
	m.mode = promptTunnelHost
	m.prompt.Reset()
	m.prompt.Placeholder = "host alias"
	if host, ok := m.Selected(); ok {
		m.prompt.SetValue(host.Alias)
		m.prompt.CursorEnd()
	}
	return m, m.prompt.Focus()
}

// submitTunnel is enter in any of the three prompts.
func (m Model) submitTunnel() (tea.Model, tea.Cmd) {
	v := strings.TrimSpace(m.prompt.Value())
	switch m.mode {
	case promptTunnelHost:
		if _, ok := m.hostByAlias(v); !ok {
			m.notice = "no host " + strconv.Quote(v) + " in the list"
			return m, nil
		}
		m.tunnel.host = v
		m.mode = promptTunnelName
		m.prompt.Reset()
		m.prompt.Placeholder = "set name, e.g. web"
		return m, m.prompt.Focus()
	case promptTunnelName:
		if v == "" || strings.ContainsAny(v, " \t#") {
			m.notice = "set name: want a single word"
			return m, nil
		}
		m.tunnel.name = v
		m.mode = promptTunnelSpecs
		m.prompt.Reset()
		m.prompt.Placeholder = "spec [spec…]  e.g. 3000:localhost:3000"
		return m, m.prompt.Focus()
	case promptTunnelSpecs:
		specs := strings.Fields(v)
		if len(specs) == 0 {
			m.notice = "at least one spec"
			return m, nil
		}
		// Shorthands expand here, before the writer sees them; the writer
		// refuses the rest too, but refusing here keeps the host and name
		// the user already typed — including a duplicate that only the
		// expansion reveals.
		seen := make(map[string]bool)
		for i, spec := range specs {
			full, _ := expandSpec(spec)
			if port, _ := localPort(full); port == "" {
				m.notice = "spec " + strconv.Quote(spec) + ": no local port — want local:host:port"
				return m, nil
			}
			if seen[full] {
				m.notice = "spec " + strconv.Quote(full) + " repeated"
				return m, nil
			}
			seen[full] = true
			specs[i] = full
		}
		m.mode = promptNone
		m.prompt.Blur()
		save, host, name := m.saveForwardSet, m.tunnel.host, m.tunnel.name
		return m, func() tea.Msg {
			return tunnelSavedMsg{host: host, name: name, err: save(host, name, specs)}
		}
	}
	return m, nil
}
