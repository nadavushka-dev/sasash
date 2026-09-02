package ui

import (
	"path/filepath"
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
	settingsSetup              // create config file: what will be written
	settingsTheme              // the theme picker
)

// Settings entries, in menu order.
const (
	entryCreate = iota
	entryEdit
	entryTheme
	entryConnection
	entryCount
)

var entryLabels = [entryCount]string{
	entryCreate:     "create config file",
	entryEdit:       "edit config file",
	entryTheme:      "theme",
	entryConnection: "add new connection",
}

// settingsState is the `o` panel: a discoverable surface over machinery
// that already exists (setup, the editor handoff, themes, the ad-hoc
// save). It never holds logic of its own.
type settingsState struct {
	pane   settingsPane
	cursor int // menu entry, or theme index in the picker

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

// setupCheckCmd inspects home off the UI thread; nil without a home.
func setupCheckCmd(home string) tea.Cmd {
	if home == "" {
		return nil
	}
	return func() tea.Msg {
		st, err := setup.Check(home)
		return setupStatusMsg{status: st, err: err}
	}
}

// setupEnsureCmd runs the one-time bootstrap off the UI thread.
func setupEnsureCmd(home string) tea.Cmd {
	return func() tea.Msg { return setupDoneMsg{err: setup.Ensure(home)} }
}

// openSettings is `o`. The panel supplies its own help, so an expanded
// `?` is folded away; a save-first flag from an earlier run cannot
// outlive this point.
func (m Model) openSettings() Model {
	m.settings = settingsState{pane: settingsMenu}
	m.saveThenConnect = false
	if m.help.ShowAll {
		m.help.ShowAll = false
		m.list.SetSize(m.listWidth(), m.contentHeight())
	}
	return m
}

// settingsKey routes a key while the panel owns the keyboard: vim
// navigation, enter selects, esc backs out one level. Every action key
// is inert here, as while filtering.
func (m Model) settingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.settings
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
		m.help.ShowAll = !m.help.ShowAll
		m.list.SetSize(m.listWidth(), m.contentHeight())
		return m, nil
	case msg.Type == tea.KeyEnter:
		return m.settingsEnter()
	}
	return m, nil
}

// settingsRows is how many rows the current pane's cursor ranges over.
func (m Model) settingsRows() int {
	switch m.settings.pane {
	case settingsTheme:
		return len(Themes)
	case settingsMenu:
		return entryCount
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
			if m.home == "" {
				m.notice = "no home directory known — cannot set up"
				return m, nil
			}
			s.pane, s.status, s.checkErr = settingsSetup, nil, nil
			return m, setupCheckCmd(m.home)
		case entryEdit:
			if len(m.editor) == 0 || m.confPath == "" {
				m.notice = "no editor configured"
				return m, nil
			}
			s.pane = settingsOff
			return m, tea.ExecProcess(m.editorExec(), func(err error) tea.Msg {
				return editorExitMsg{err: err}
			})
		case entryTheme:
			s.pane, s.cursor = settingsTheme, m.themeIdx
			return m, nil
		case entryConnection:
			s.pane = settingsOff
			m.mode = promptNewConn
			m.prompt.Reset()
			m.prompt.Placeholder = "[user@]host[:port] [-L local:host:port]"
			return m, m.prompt.Focus()
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
		return m, setupEnsureCmd(m.home)
	case settingsTheme:
		return m.setTheme(s.cursor)
	}
	return m, nil
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

// settingsView renders the panel that replaces the panes while open.
func (m Model) settingsView() string {
	s := m.settings
	var b strings.Builder
	mark := func(i, cur int) string {
		if i == cur {
			return m.styles.RowMark.Render("› ")
		}
		return "  "
	}
	switch s.pane {
	case settingsMenu:
		b.WriteString(" " + m.styles.SectionLabel.Render("settings"))
		detail := [entryCount]string{
			entryEdit:  m.confPath,
			entryTheme: Themes[m.themeIdx].Name,
		}
		for i, label := range entryLabels {
			b.WriteString("\n " + mark(i, s.cursor) + m.styles.PathName.Render(label))
			if detail[i] != "" {
				b.WriteString("  " + m.styles.PathExtra.Render(detail[i]))
			}
		}
	case settingsSetup:
		b.WriteString(" " + m.styles.SectionLabel.Render("create config file — sasash writes exactly:"))
		type piece struct {
			text    string
			present func(setup.Status) bool
		}
		pieces := []piece{
			{setup.IncludeLine + "  → top of " + m.tildePath("config"), func(st setup.Status) bool { return st.IncludePresent }},
			{m.tildePath("config.d", "sasash.conf") + "  (one managed-header comment line)", func(st setup.Status) bool { return st.ConfPresent }},
			{m.tildePath("cm") + "/  (control sockets)", func(st setup.Status) bool { return st.CMDirPresent }},
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
			b.WriteString("\n   " + glyph + " " + m.styles.PathAddr.Render(p.text) + " " + m.styles.PathExtra.Render(word))
		}
		switch {
		case s.checkErr != nil:
			b.WriteString("\n   " + m.styles.Notice.Render("! "+s.checkErr.Error()))
		case s.status != nil && s.status.Done():
			b.WriteString("\n\n " + m.styles.Empty.Render("already set up — nothing to write; esc back"))
		case s.status != nil:
			b.WriteString("\n\n " + m.styles.Empty.Render("enter writes the missing pieces (atomically, nothing else touched) · esc back"))
		}
	case settingsTheme:
		b.WriteString(" " + m.styles.SectionLabel.Render("theme — enter applies and persists"))
		for i, t := range Themes {
			glyph := m.styles.StatusDown.Render("○")
			if i == m.themeIdx {
				glyph = m.styles.StatusUp.Render("●")
			}
			b.WriteString("\n " + mark(i, s.cursor) + glyph + " " + m.styles.PathName.Render(t.Name))
		}
	}
	return b.String()
}

// tildePath spells a path under ~/.ssh the way the Include line does.
func (m Model) tildePath(parts ...string) string {
	return "~/" + filepath.Join(append([]string{".ssh"}, parts...)...)
}

// settingsKeys is the help shown while the panel owns the keyboard.
type settingsKeys struct{}

func (settingsKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "move")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back / close")),
	}
}

func (k settingsKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}
