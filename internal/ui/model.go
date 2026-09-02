package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	// os/exec appears here ONLY to build the *exec.Cmd that tea.ExecProcess
	// requires for the ssh handoff (SPEC §5): tea must own the tty itself,
	// so this one invocation cannot go through run.Runner. Every other
	// process start stays behind the run seam.
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run"
)

// refreshEvery is how often paths are re-probed. One refresh dials every
// path, bounded by probeChunk and the prober timeout, so this stays cheap.
const refreshEvery = 5 * time.Second

// probeChunk caps how many paths one refresh dials at once: a large config
// must not open hundreds of sockets per round (ProbeAll itself starts one
// goroutine per path). Chunks run sequentially inside the tea.Cmd, off the
// UI thread, so a big fleet costs refresh latency, never responsiveness.
const probeChunk = 64

// hostItem adapts hostmodel.Host to the bubbles list.
type hostItem struct{ host hostmodel.Host }

// FilterValue is what `/` filters on: alias and @sasash tags.
func (i hostItem) FilterValue() string {
	return i.host.Alias + " " + strings.Join(i.host.Tags, " ")
}

// hostDelegate renders one single-line row: "› alias" selected, "  alias"
// otherwise. The prefix + bold keep the selection visible under NO_COLOR.
type hostDelegate struct{ styles Styles }

func (hostDelegate) Height() int                               { return 1 }
func (hostDelegate) Spacing() int                              { return 0 }
func (hostDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }

func (d hostDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(hostItem)
	if !ok {
		return
	}
	if index == m.Index() {
		fmt.Fprint(w, d.styles.ItemSelected.MaxWidth(m.Width()).Render("› "+it.host.Alias))
		return
	}
	fmt.Fprint(w, d.styles.Item.MaxWidth(m.Width()).Render(it.host.Alias))
}

// probeDoneMsg carries one full refresh: statuses per host alias, each
// slice index-aligned with that host's Paths.
type probeDoneMsg struct{ statuses map[string][]probe.Status }

// refreshTickMsg asks for the next probe round.
type refreshTickMsg struct{}

// sshExitMsg arrives when the terminal comes back from an ssh handoff.
// err reports only a failure to start; ssh's own exit code is not news.
type sshExitMsg struct{ err error }

// copyDoneMsg reports the outcome of a clipboard write.
type copyDoneMsg struct{ err error }

// themeSavedMsg reports the outcome of persisting a theme choice.
type themeSavedMsg struct{ err error }

// sshCommand is the argv `enter` runs and the command line previews; one
// builder so the preview can never drift from the handoff (SPEC §5: the
// bottom line always shows what the highlighted action will run).
func sshCommand(alias string) []string {
	return []string{"ssh", alias}
}

// sshExec builds the process the handoff gives the terminal to.
func sshExec(alias string) *exec.Cmd {
	return execCommand(sshCommand(alias))
}

// editorArgv is the exact invocation `e` hands the terminal to.
func editorArgv(editor []string, confPath string) []string {
	return append(append([]string{}, editor...), confPath)
}

// editorExec is the ONE place the `e` handoff process is built, so a test
// can pin the exact args that reach the *exec.Cmd.
func (m Model) editorExec() *exec.Cmd {
	return execCommand(editorArgv(m.editor, m.confPath))
}

// editorExitMsg arrives when the terminal returns from the editor.
type editorExitMsg struct{ err error }

// execCommand adapts a built argv to the *exec.Cmd tea.ExecProcess needs.
func execCommand(argv []string) *exec.Cmd {
	return exec.Command(argv[0], argv[1:]...)
}

// clipTimeout bounds a clipboard write: a wedged pasteboard server must
// surface as a "copy failed" notice, not as silence.
const clipTimeout = 2 * time.Second

// clipArgv builds the pbcopy invocation (decision R2). run.Runner has no
// stdin, so the text travels as a positional shell argument — never spliced
// into the script, so no quoting is needed and no injection is possible.
func clipArgv(text string) []string {
	return []string{"sh", "-c", `printf %s "$1" | pbcopy`, "sh", text}
}

// clipCmd writes text to the clipboard through the run seam, off the UI
// thread.
func clipCmd(r run.Runner, text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), clipTimeout)
		defer cancel()
		res, err := r.Run(ctx, clipArgv(text)...)
		if err == nil && !res.OK() {
			err = fmt.Errorf("pbcopy exited %d: %s", res.Code, strings.TrimSpace(string(res.Stderr)))
		}
		return copyDoneMsg{err: err}
	}
}

// probeCmd dials every path of every host, in parallel ProbeAll rounds of
// at most probeChunk paths. It runs on bubbletea's command goroutine, so
// the UI never blocks on the network; the result comes back as a
// probeDoneMsg.
func probeCmd(p probe.Prober, hosts []hostmodel.Host) tea.Cmd {
	return func() tea.Msg {
		var flat []probe.Path
		for _, h := range hosts {
			flat = append(flat, h.Paths...)
		}
		statuses := make([]probe.Status, 0, len(flat))
		for start := 0; start < len(flat); start += probeChunk {
			end := start + probeChunk
			if end > len(flat) {
				end = len(flat)
			}
			statuses = append(statuses, p.ProbeAll(context.Background(), flat[start:end])...)
		}
		out := make(map[string][]probe.Status, len(hosts))
		for _, h := range hosts {
			out[h.Alias] = statuses[:len(h.Paths):len(h.Paths)]
			statuses = statuses[len(h.Paths):]
		}
		return probeDoneMsg{statuses: out}
	}
}

// Model is the root bubbletea model: left host list, right detail pane for
// the selected host, bottom command preview + generated help line.
type Model struct {
	hosts    []hostmodel.Host
	statuses map[string][]probe.Status // nil until the first probe lands

	list   list.Model
	help   help.Model
	keys   KeyMap
	styles Styles
	prober probe.Prober
	runner run.Runner

	themeIdx  int                     // index into Themes
	saveTheme func(name string) error // nil -> theme choice not persisted

	// Ad-hoc connect (SPEC §5 `a`): a prompt takes over the preview line;
	// while it is focused, action keys are inert exactly as while
	// filtering. pending holds the parsed target between the handoff and
	// the save offer.
	mode      promptMode
	prompt    textinput.Model
	pending   adhocTarget
	saveHost  func(AdhocEntry) error // nil -> no save offer after connect
	loadHosts func() ([]hostmodel.Host, []error, error)

	editor     []string // argv prefix for `e`; empty disables the key
	editorHint string
	editorWarn string
	confPath   string

	// notice is a transient one-line message (copy feedback, ssh start
	// failure); any next keypress clears it.
	notice string
	// tickArmed dedupes the refresh loop: extra probe rounds (an ssh
	// handoff returning) must not each arm their own tick chain.
	tickArmed bool

	width, height int
}

// Config carries the theme selection into the model. The zero value is
// valid: default theme, no persistence.
type Config struct {
	// Theme is the initial theme name; unknown or empty falls back to
	// Themes[0] (gruvbox).
	Theme string
	// SaveTheme persists a theme name when `t` cycles to it; nil disables
	// persistence.
	SaveTheme func(name string) error
	// SaveHost persists an ad-hoc connection the user chose to keep; nil
	// disables the save offer after an ad-hoc connect.
	SaveHost func(AdhocEntry) error
	// LoadHosts re-reads the config after a save or an edit so the change
	// appears in the list; nil skips the reload. Its warnings (malformed
	// @sasash lines, hostmodel.Load's second result) are shown after an
	// edit — under the alt screen they are the only feedback a hand-edit
	// gets.
	LoadHosts func() ([]hostmodel.Host, []error, error)
	// Editor is the argv prefix `e` launches on ConfPath (resolved from
	// $EDITOR by the cmd layer); empty disables the key.
	Editor []string
	// EditorHint, when non-empty, shows as a notice after the editor
	// returns (e.g. the $EDITOR-unset fallback explanation).
	EditorHint string
	// EditorWarn, when non-empty, overrides EditorHint after the editor
	// returns: the cmd layer sets it when setup has not wired sasash.conf
	// into ssh, so an edit that cannot show up in the list says why
	// instead of silently changing nothing.
	EditorWarn string
	// ConfPath is the file `e` edits: sasash.conf.
	ConfPath string
}

// promptMode says which prompt, if any, owns the keyboard.
type promptMode int

const (
	promptNone promptMode = iota
	promptConnect
	promptAlias
)

// New builds the root model. The prober's zero value probes for real and
// runner should be run.OS{} outside tests; tests inject fakes.
func New(hosts []hostmodel.Host, prober probe.Prober, runner run.Runner, cfg Config) Model {
	keys := DefaultKeyMap()

	items := make([]list.Item, len(hosts))
	for i, h := range hosts {
		items[i] = hostItem{host: h}
	}
	l := list.New(items, hostDelegate{}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	// '/' filters what FilterValue exposes: alias + tags. Enabling this
	// makes bubbles reserve a permanent title/filter row above the list —
	// a deliberate trade: the selected host lines up with its path rows,
	// at the cost of one row of list capacity.
	l.SetFilteringEnabled(true)
	// With a filter applied and nothing matching, the detail pane already
	// says so and names the fix key; the list's own "No items." would
	// double up beside it.
	l.Styles.NoItems = lipgloss.NewStyle().Transform(func(string) string { return "" })
	// Only navigation and filtering reach the list. The remaining default
	// bindings — paging on bare b/f/u/d/h/l, esc/q quit, the full-help
	// toggle — stay zero-valued, which never matches; quit and help are
	// owned at the app level.
	l.KeyMap = list.KeyMap{
		CursorUp:             keys.Up,
		CursorDown:           keys.Down,
		GoToStart:            keys.Top,
		GoToEnd:              keys.Bottom,
		Filter:               keys.Filter,
		ClearFilter:          keys.ClearFilter,
		CancelWhileFiltering: key.NewBinding(key.WithKeys("esc")),
		AcceptWhileFiltering: key.NewBinding(key.WithKeys("enter")),
	}

	prompt := textinput.New()
	prompt.Prompt = "" // the label renders separately, palette-styled
	prompt.CharLimit = 128

	m := Model{
		hosts:      hosts,
		list:       l,
		help:       help.New(),
		keys:       keys,
		prober:     prober,
		runner:     runner,
		saveTheme:  cfg.SaveTheme,
		prompt:     prompt,
		saveHost:   cfg.SaveHost,
		loadHosts:  cfg.LoadHosts,
		editor:     cfg.Editor,
		editorHint: cfg.EditorHint,
		editorWarn: cfg.EditorWarn,
		confPath:   cfg.ConfPath,
	}
	idx, _ := ThemeIndex(cfg.Theme) // unknown or "" -> 0, the default
	return m.applyTheme(idx)
}

// applyTheme is the whole theme switch (crush pattern): rebuild Styles
// from the palette and hand them to every component that holds any.
func (m Model) applyTheme(idx int) Model {
	m.themeIdx = idx
	m.styles = NewStyles(Themes[idx].Palette)
	m.list.SetDelegate(hostDelegate{styles: m.styles})
	m.help.Styles.ShortKey = m.styles.HelpKey
	m.help.Styles.ShortDesc = m.styles.HelpDesc
	m.help.Styles.ShortSeparator = m.styles.HelpSep
	m.help.Styles.FullKey = m.styles.HelpKey
	m.help.Styles.FullDesc = m.styles.HelpDesc
	m.help.Styles.FullSeparator = m.styles.HelpSep
	return m
}

// Selected returns the host under the cursor; ok is false when the list is
// empty.
func (m Model) Selected() (hostmodel.Host, bool) {
	it, ok := m.list.SelectedItem().(hostItem)
	if !ok {
		return hostmodel.Host{}, false
	}
	return it.host, true
}

// Init kicks off the first probe; rendering never waits on it.
func (m Model) Init() tea.Cmd {
	return probeCmd(m.prober, m.hosts)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		// The prompt needs an explicit width to get textinput's horizontal
		// viewport: without one, long input silently loses its tail and
		// cursor past the terminal edge.
		m.prompt.Width = max(10, msg.Width-14)
		m.list.SetSize(m.listWidth(), m.contentHeight())
		return m, nil

	case probeDoneMsg:
		m.statuses = msg.statuses
		if m.tickArmed {
			return m, nil
		}
		m.tickArmed = true
		return m, tea.Tick(refreshEvery, func(time.Time) tea.Msg { return refreshTickMsg{} })

	case refreshTickMsg:
		m.tickArmed = false
		return m, probeCmd(m.prober, m.hosts)

	case sshExitMsg:
		// A non-zero ssh exit is not news — ssh printed its own diagnosis
		// to the real terminal during the handoff. Only a failure to start
		// (missing binary) needs surfacing here.
		var exitErr *exec.ExitError
		if msg.err != nil && !errors.As(msg.err, &exitErr) {
			m.notice = "ssh: " + msg.err.Error()
		}
		// The world may have changed while the terminal was handed away.
		return m, probeCmd(m.prober, m.hosts)

	case copyDoneMsg:
		if msg.err != nil {
			m.notice = "copy failed: " + msg.err.Error()
		}
		return m, nil

	case themeSavedMsg:
		if msg.err != nil {
			m.notice = "theme not saved: " + msg.err.Error()
		}
		return m, nil

	case editorExitMsg:
		var exitErr *exec.ExitError
		if msg.err != nil && !errors.As(msg.err, &exitErr) {
			m.notice = "editor: " + msg.err.Error()
			return m, probeCmd(m.prober, m.hosts)
		}
		// Hand-edits win (SPEC §2): re-read and re-render, never fight.
		// The setup warning outranks the fallback hint — an edit ssh
		// cannot see must say why, not silently change nothing.
		m.notice = m.editorHint
		if m.editorWarn != "" {
			m.notice = m.editorWarn
		}
		if m.loadHosts == nil {
			return m, probeCmd(m.prober, m.hosts)
		}
		return m, m.reloadCmd(true)

	case adhocExitMsg:
		var exitErr *exec.ExitError
		if msg.err != nil && !errors.As(msg.err, &exitErr) {
			// ssh never started; there is nothing worth saving.
			m.notice = "ssh: " + msg.err.Error()
			return m, probeCmd(m.prober, m.hosts)
		}
		if m.saveHost == nil {
			return m, probeCmd(m.prober, m.hosts)
		}
		// Offer to keep the connection: the alias prompt takes the line.
		m.mode = promptAlias
		m.prompt.Reset()
		m.prompt.Placeholder = "alias to save as — esc to skip"
		return m, tea.Batch(m.prompt.Focus(), probeCmd(m.prober, m.hosts))

	case adhocSavedMsg:
		if msg.err != nil {
			m.notice = "not saved: " + msg.err.Error()
			return m, nil
		}
		m.notice = "saved " + msg.alias
		if m.loadHosts == nil {
			return m, nil
		}
		return m, m.reloadCmd(false)

	case hostsReloadedMsg:
		if msg.err != nil {
			m.notice = "reload failed: " + msg.err.Error()
			return m, nil
		}
		// A load warning after an edit outranks the editor hint: it is the
		// only feedback a malformed hand-edit gets under the alt screen.
		// After a save the "saved" notice stays — the saved block cannot
		// be what is malformed.
		if msg.edited && len(msg.warnings) > 0 {
			m.notice = warningNotice(msg.warnings)
		}
		m.hosts = msg.hosts
		items := make([]list.Item, len(msg.hosts))
		for i, h := range msg.hosts {
			items[i] = hostItem{host: h}
		}
		cmd := m.list.SetItems(items)
		// SetItems never moves the cursor: a list that shrank under it
		// (the editor deleted the selected host) would otherwise show no
		// selection at all. Under an applied filter the visible items are
		// empty until the FilterMatchesMsg lands — that arm clamps too.
		clampCursor(&m.list)
		return m, tea.Batch(cmd, probeCmd(m.prober, m.hosts))

	case list.FilterMatchesMsg:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		clampCursor(&m.list)
		return m, cmd

	case tea.KeyMsg:
		if key.Matches(msg, m.keys.ForceQuit) {
			return m, tea.Quit
		}
		m.notice = ""
		// A focused prompt owns the keyboard the way the filter input
		// does: esc cancels (declining the save writes nothing), enter
		// submits, everything else is typed text.
		if m.mode != promptNone {
			switch {
			case key.Matches(msg, m.keys.ClearFilter):
				declined := m.mode == promptAlias
				m.mode = promptNone
				m.prompt.Blur()
				if declined {
					m.notice = "not saved"
				}
				return m, nil
			case msg.Type == tea.KeyEnter:
				return m.promptSubmit()
			}
			var cmd tea.Cmd
			m.prompt, cmd = m.prompt.Update(msg)
			return m, cmd
		}
		// Action keys must never fire while the filter input is focused
		// (SPEC §5 addendum): everything below is typed text then, and
		// only the list — which owns the input — sees the key.
		if m.list.FilterState() != list.Filtering {
			switch {
			case key.Matches(msg, m.keys.Quit):
				return m, tea.Quit
			case key.Matches(msg, m.keys.Connect):
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				return m, tea.ExecProcess(sshExec(host.Alias), func(err error) tea.Msg {
					return sshExitMsg{err: err}
				})
			case key.Matches(msg, m.keys.CopyAddr):
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				// The active address, bare — the clipboard twin of
				// `sasash addr`: data for other tools, not a dial target.
				if m.statuses == nil {
					m.notice = "still probing — nothing copied"
					return m, nil
				}
				s, ok := probe.Select(m.statuses[host.Alias])
				if !ok {
					m.notice = "no alive path — nothing copied"
					return m, nil
				}
				return m, clipCmd(m.runner, s.Path.Host)
			case key.Matches(msg, m.keys.CopyCmd):
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				return m, clipCmd(m.runner, strings.Join(sshCommand(host.Alias), " "))
			case key.Matches(msg, m.keys.Edit):
				if len(m.editor) == 0 || m.confPath == "" {
					return m, nil
				}
				return m, tea.ExecProcess(m.editorExec(), func(err error) tea.Msg {
					return editorExitMsg{err: err}
				})
			case key.Matches(msg, m.keys.Adhoc):
				m.mode = promptConnect
				m.prompt.Reset()
				m.prompt.Placeholder = "[user@]host[:port] [-L local:host:port]"
				return m, m.prompt.Focus()
			case key.Matches(msg, m.keys.Theme):
				m = m.applyTheme((m.themeIdx + 1) % len(Themes))
				m.notice = "theme: " + Themes[m.themeIdx].Name
				if m.saveTheme == nil {
					return m, nil
				}
				save, name := m.saveTheme, Themes[m.themeIdx].Name
				return m, func() tea.Msg { return themeSavedMsg{err: save(name)} }
			case key.Matches(msg, m.keys.Help):
				m.help.ShowAll = !m.help.ShowAll
				m.list.SetSize(m.listWidth(), m.contentHeight())
				return m, nil
			case key.Matches(msg, m.keys.ClearFilter):
				if m.help.ShowAll {
					m.help.ShowAll = false
					m.list.SetSize(m.listWidth(), m.contentHeight())
					return m, nil
				}
				// Not consumed: esc falls through to the list, which
				// clears an applied filter (and is a no-op otherwise).
			case key.Matches(msg, m.keys.HalfDown):
				for i := 0; i < m.halfPage(); i++ {
					m.list.CursorDown()
				}
				return m, nil
			case key.Matches(msg, m.keys.HalfUp):
				for i := 0; i < m.halfPage(); i++ {
					m.list.CursorUp()
				}
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) halfPage() int {
	if n := m.list.Paginator.PerPage / 2; n > 1 {
		return n
	}
	return 1
}

// listWidth gives the left pane roughly a third of the terminal, clamped
// so short lists stay narrow and tiny terminals stay usable.
func (m Model) listWidth() int {
	w := m.width / 3
	if w > 28 {
		w = 28
	}
	if w < 16 {
		w = 16
	}
	if w > m.width {
		w = m.width
	}
	return w
}

// contentHeight is the pane area: total minus header (2), the preview
// line, and the help block — one line normally, taller while `?` has it
// expanded.
func (m Model) contentHeight() int {
	h := m.height - 3 - lipgloss.Height(m.helpView())
	if h < 1 {
		h = 1
	}
	return h
}

// helpView renders the bottom help block, generated from the key
// bindings; while the filter input is focused it shows the two keys that
// actually do anything there. help.Width makes bubbles truncate with an
// ellipsis, but its truncation has an off-by-a-few edge (an item landing
// within two cells of the limit is added anyway, and everything after it
// too), so the block gets the same hard clamp as the rest of the chrome —
// and a height clamp, so `?` on a short terminal degrades instead of
// pushing the alt screen around.
func (m Model) helpView() string {
	var k help.KeyMap = m.keys
	switch {
	case m.mode != promptNone:
		k = promptKeys{save: m.mode == promptAlias}
	case m.list.FilterState() == list.Filtering:
		k = filterKeys{}
	}
	v := m.styles.Clamp.MaxWidth(m.width).Render(" " + m.help.View(k))
	return clampHeight(v, m.height-4)
}

// clampHeight truncates s to at most maxLines lines (minimum one).
// lipgloss.Place pads content to a height but never truncates it, so
// every block that can outgrow its slot passes through here.
func clampHeight(s string, maxLines int) string {
	if maxLines < 1 {
		maxLines = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	header := " " + m.styles.Title.Render("sasash")
	if m.list.FilterState() == list.FilterApplied {
		header += "   " + m.styles.CmdPreview.Render("/"+m.list.FilterValue())
	}
	// The applied-filter term is user content: clamp like all chrome.
	header = m.styles.Clamp.MaxWidth(m.width).Render(header)

	var body string
	if len(m.hosts) == 0 {
		empty := m.styles.Empty.Render("no hosts found — add a Host block to ~/.ssh/config")
		body = lipgloss.Place(m.width, m.contentHeight(), lipgloss.Center, lipgloss.Center, empty)
	} else {
		right := m.detailView()
		if len(m.list.VisibleItems()) == 0 {
			// The list pane stays (it holds the filter input); only the
			// detail side names the fix key.
			right = m.styles.Empty.Render("no hosts match — esc to clear the filter")
		}
		panes := lipgloss.JoinHorizontal(lipgloss.Top, m.list.View(), "  ", right)
		// A long alias + hostname can outgrow the terminal; truncate every
		// line so the frame never wraps and pushes the alt screen around.
		panes = m.styles.Clamp.MaxWidth(m.width).Render(panes)
		body = lipgloss.Place(m.width, m.contentHeight(), lipgloss.Left, lipgloss.Top, panes)
	}
	// On a very short terminal the panes can outgrow their slot.
	body = clampHeight(body, m.contentHeight())

	preview := m.previewView()
	if m.mode != promptNone {
		label := "connect:"
		if m.mode == promptAlias {
			label = "save as:"
		}
		// textinput pads its value to Width; trim the raw padding so a
		// notice (a rejection message, typically) still fits the line.
		field := strings.TrimRight(m.prompt.View(), " ")
		preview = " " + m.styles.CmdPrompt.Render(label) + " " + field
	}
	if m.notice != "" {
		preview += "  " + m.styles.Notice.Render(m.notice)
	}
	// Same width clamp as the panes: a long alias (or notice) must not
	// wrap the preview line and push the alt screen around.
	preview = m.styles.Clamp.MaxWidth(m.width).Render(preview)
	return strings.Join([]string{header, "", body, preview, m.helpView()}, "\n")
}

// detailView renders the right pane for the selected host: one line per
// path, glyph + name + address + latency/state, first alive path marked
// active (probe.Select semantics: config order is preference).
func (m Model) detailView() string {
	host, ok := m.Selected()
	if !ok {
		return ""
	}
	sts := m.statuses[host.Alias]

	active := -1
	for i, s := range sts {
		if s.Alive() {
			active = i
			break
		}
	}

	nameWidth := 0
	for _, p := range host.Paths {
		if len(p.Name) > nameWidth {
			nameWidth = len(p.Name)
		}
	}

	var b strings.Builder
	b.WriteString(m.styles.SectionLabel.Render("paths"))
	for i, p := range host.Paths {
		glyph := m.styles.StatusWait.Render("◌")
		extra := m.styles.PathExtra.Render("…")
		if i < len(sts) {
			if s := sts[i]; s.Alive() {
				glyph = m.styles.StatusUp.Render("●")
				// Same latency spelling as `sasash status`: 20ms, 1.005s.
				extra = m.styles.PathExtra.Render(s.Latency.Round(time.Millisecond).String())
			} else {
				glyph = m.styles.StatusDown.Render("○")
				extra = m.styles.PathExtra.Render("down")
			}
		}
		b.WriteString("\n " + glyph)
		if nameWidth > 0 {
			b.WriteString(" " + m.styles.PathName.Render(fmt.Sprintf("%-*s", nameWidth, p.Name)))
		}
		// Full dial target, port included: the pane reports what was probed
		// (ratified: diagnostic surfaces match `sasash status`; only `addr`
		// stays bare, as data for scripting).
		b.WriteString(" " + m.styles.PathAddr.Render(p.Addr()) + " " + extra)
		if i == active {
			b.WriteString(" " + m.styles.ActiveMark.Render("← active"))
		}
	}
	return b.String()
}

// previewView is the bottom command line: what enter will run (SPEC §5,
// "always shows what the highlighted action will run").
func (m Model) previewView() string {
	host, ok := m.Selected()
	if !ok {
		return ""
	}
	return " " + m.styles.CmdPrompt.Render("$") + " " + m.styles.CmdPreview.Render(strings.Join(sshCommand(host.Alias), " "))
}
