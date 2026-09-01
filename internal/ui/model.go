package ui

import (
	"context"
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

// FilterValue matches alias and tags (filtering itself lands in 1.5).
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

// sshCommand is the argv `enter` runs and the command line previews; one
// builder so the preview can never drift from the handoff (SPEC §5: the
// bottom line always shows what the highlighted action will run).
func sshCommand(alias string) []string {
	return []string{"ssh", alias}
}

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
		res, err := r.Run(context.Background(), clipArgv(text)...)
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

	// notice is a transient one-line message (copy feedback, ssh start
	// failure); any next keypress clears it.
	notice string
	// tickArmed dedupes the refresh loop: extra probe rounds (an ssh
	// handoff returning) must not each arm their own tick chain.
	tickArmed bool

	width, height int
}

// New builds the root model. The prober's zero value probes for real and
// runner should be run.OS{} outside tests; tests inject fakes.
func New(hosts []hostmodel.Host, prober probe.Prober, runner run.Runner) Model {
	styles := NewStyles(Gruvbox)
	keys := DefaultKeyMap()

	items := make([]list.Item, len(hosts))
	for i, h := range hosts {
		items[i] = hostItem{host: h}
	}
	l := list.New(items, hostDelegate{styles: styles}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false) // the filter slice is SASASH.1.5
	// Only navigation reaches the list. The remaining default bindings —
	// paging on bare b/f/u/d/h/l, esc/q quit, filtering — stay zero-valued,
	// which never matches; quit is owned at the app level.
	l.KeyMap = list.KeyMap{
		CursorUp:   keys.Up,
		CursorDown: keys.Down,
		GoToStart:  keys.Top,
		GoToEnd:    keys.Bottom,
	}

	hm := help.New()
	hm.Styles.ShortKey = styles.HelpKey
	hm.Styles.ShortDesc = styles.HelpDesc
	hm.Styles.ShortSeparator = styles.HelpSep
	hm.Styles.FullKey = styles.HelpKey
	hm.Styles.FullDesc = styles.HelpDesc
	hm.Styles.FullSeparator = styles.HelpSep

	return Model{
		hosts:  hosts,
		list:   l,
		help:   hm,
		keys:   keys,
		styles: styles,
		prober: prober,
		runner: runner,
	}
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
		if msg.err != nil {
			m.notice = "ssh: " + msg.err.Error()
		}
		// The world may have changed while the terminal was handed away.
		return m, probeCmd(m.prober, m.hosts)

	case copyDoneMsg:
		if msg.err != nil {
			m.notice = "copy failed: " + msg.err.Error()
		}
		return m, nil

	case tea.KeyMsg:
		if key.Matches(msg, m.keys.ForceQuit) {
			return m, tea.Quit
		}
		m.notice = ""
		// Action keys must never fire while the filter input is focused
		// (SPEC §5 addendum). Filtering arrives in 1.5; the guard is
		// structural from day one so keys added later inherit it.
		if m.list.FilterState() != list.Filtering {
			switch {
			case key.Matches(msg, m.keys.Quit):
				return m, tea.Quit
			case key.Matches(msg, m.keys.Connect):
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				argv := sshCommand(host.Alias)
				return m, tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg {
					return sshExitMsg{err: err}
				})
			case key.Matches(msg, m.keys.CopyAddr):
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				// The active address, bare — the clipboard twin of
				// `sasash addr`: data for other tools, not a dial target.
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

// contentHeight is the pane area: total minus header (2) and bottom
// chrome (2: command preview + help line).
func (m Model) contentHeight() int {
	h := m.height - 4
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	header := " " + m.styles.Title.Render("sasash")

	var body string
	if len(m.hosts) == 0 {
		empty := m.styles.Empty.Render("no hosts found — add a Host block to ~/.ssh/config")
		body = lipgloss.Place(m.width, m.contentHeight(), lipgloss.Center, lipgloss.Center, empty)
	} else {
		panes := lipgloss.JoinHorizontal(lipgloss.Top, m.list.View(), "  ", m.detailView())
		// A long alias + hostname can outgrow the terminal; truncate every
		// line so the frame never wraps and pushes the alt screen around.
		panes = m.styles.Clamp.MaxWidth(m.width).Render(panes)
		body = lipgloss.Place(m.width, m.contentHeight(), lipgloss.Left, lipgloss.Top, panes)
	}

	preview := m.previewView()
	if m.notice != "" {
		preview += "  " + m.styles.Notice.Render(m.notice)
	}
	// Same width clamp as the panes: a long alias (or notice) must not
	// wrap the preview line and push the alt screen around.
	preview = m.styles.Clamp.MaxWidth(m.width).Render(preview)
	return strings.Join([]string{header, "", body, preview, " " + m.help.View(m.keys)}, "\n")
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
