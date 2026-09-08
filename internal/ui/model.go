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
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/setup"
	"github.com/nadavushka-dev/sasash/internal/stats"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
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
type hostItem struct {
	host    hostmodel.Host
	ambient bool // below the divider: not declared in sasash.conf, no meta
}

// FilterValue is what `/` filters on: alias and @sasash tags.
func (i hostItem) FilterValue() string {
	return i.host.Alias + " " + strings.Join(i.host.Tags, " ")
}

// hostDelegate renders one single-line row: "› alias" selected, "  alias"
// otherwise — the prefix is what keeps the selection visible under
// NO_COLOR (bold does not survive it). While the tunnels pane holds focus
// (dim) the row keeps its fill but loses the prefix: the one "›" on
// screen then marks the focused pane's selection, in colour or not.
type hostDelegate struct {
	styles Styles
	dim    bool
}

func (hostDelegate) Height() int                               { return 1 }
func (hostDelegate) Spacing() int                              { return 0 }
func (hostDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }

func (d hostDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	it, ok := item.(hostItem)
	if !ok {
		if _, divider := item.(dividerItem); divider {
			renderDivider(w, d.styles, m.Width())
		}
		return // the spacer renders as its blank line
	}
	if index == m.Index() {
		if d.dim {
			fmt.Fprint(w, d.styles.ItemSelectedDim.MaxWidth(m.Width()).Render("  "+it.host.Alias))
			return
		}
		fmt.Fprint(w, d.styles.ItemSelected.MaxWidth(m.Width()).Render("› "+it.host.Alias))
		return
	}
	if it.ambient {
		fmt.Fprint(w, d.styles.ItemAmbient.MaxWidth(m.Width()).Render(it.host.Alias))
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

// contextHelp is the footer renderer for a surface's hints, spelled with
// the same separator as the whisper.
func contextHelp() help.Model {
	h := help.New()
	h.ShortSeparator = helpSeparator
	return h
}

// handoff is the classic full-terminal ssh (S, and enter's last resort).
func (m Model) handoff(alias string) (tea.Model, tea.Cmd) {
	m.handoffHost = alias
	return m, tea.ExecProcess(sshExec(alias), func(err error) tea.Msg {
		return sshExitMsg{err: err}
	})
}

// hNoTabTitle heads the popup H shows when no terminal emulator tab can
// be opened for it.
const hNoTabTitle = "H: no terminal tab to open here"

// herdrCommand is the argv `H` opens beside the TUI (SPEC §5/§6): herdr
// reads ssh_config itself, so it needs no actions.toml template. Inside
// herdr it runs in a tab of the outer emulator, never in herdr's own
// (see herdrPlanFor).
func herdrCommand(alias string) []string {
	return []string{"herdr", "--remote", alias}
}

// editorArgv is the exact invocation `e` hands the terminal to.
func editorArgv(editor []string, confPath string) []string {
	return append(append([]string{}, editor...), confPath)
}

// editTarget is the file `e` opens: the one that DECLARES the selected
// host — sasash.conf, ~/.ssh/config, or an Included file — falling back
// to sasash.conf when no host is selected or the host carries no path.
func (m Model) editTarget() string {
	if host, ok := m.Selected(); ok && host.File != "" {
		return host.File
	}
	return m.confPath
}

// editorExec is the ONE place the `e` handoff process is built, so a test
// can pin the exact args that reach the *exec.Cmd.
func (m Model) editorExec() *exec.Cmd {
	return execCommand(editorArgv(m.editor, m.editTarget()))
}

// editorExecOn builds the same process on an explicit file (settings'
// "edit config file" always means sasash.conf).
func editorExecOn(editor []string, path string) *exec.Cmd {
	return execCommand(editorArgv(editor, path))
}

// editorExitMsg arrives when the terminal returns from the editor.
type editorExitMsg struct{ err error }

// execCommand adapts a built argv to the *exec.Cmd tea.ExecProcess needs.
func execCommand(argv []string) *exec.Cmd {
	return exec.Command(argv[0], argv[1:]...)
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
	mode           promptMode
	prompt         textinput.Model
	pending        adhocTarget
	saveHost       func(AdhocEntry) error // nil -> no save offer after connect
	inspectControl func(alias string) (ControlStatus, error)
	enableControl  func(alias string) ([]string, error)
	loadHosts      func() ([]hostmodel.Host, []error, error)

	// Tunnels section (SPEC §5): tunnels is the engine seam, nil = off.
	// tstatus is the last Status per alias; fwdIdx is the highlighted
	// forward row while the tunnels pane holds focus, -1 otherwise (then
	// the preview is enter's ssh).
	focus     paneFocus
	focusHost string // the alias the tunnels focus was taken on
	tunnels   Tunneler
	tstatus   map[string]tunnelState
	tinflight map[string]bool // a Status poll is running for the alias
	fwdIdx    int
	fwdHost   string // the host the forward prompt is bound to

	actions []string // actions.toml entry names, listed in the full help

	// Settings (`o`): the panel state and the home it inspects/bootstraps
	// through internal/setup; "" disables the create-config entry.
	settings settingsState
	home     string
	// saveThenConnect marks the settings "add new connection" flow: the
	// save prompt is entered first, and a successful save offers a
	// connect instead of the ad-hoc flow's connect-first order.
	saveThenConnect bool
	offerAlias      string
	// tunnelFirst marks a -N whose target is not saved (SASASH.4.17): the
	// alias prompt runs first, and the saved alias carries the tunnel
	// through the engine; a declined save offers the unmanaged run.
	tunnelFirst bool
	// add new tunnel: the hook that writes the meta line, and the flow's
	// collected answers.
	saveForwardSet func(alias, name string, specs []string) error
	tunnel         newTunnel
	// edit forward in place (E): the hook that rewrites the meta line,
	// and the row being edited.
	saveForwardEdit func(alias, set, old, new string) error
	editFwd         editFwdState
	// rename (R): the hook, and the alias the open prompt renames.
	renameHost func(old, new string) (pathswitch.RenameResult, error)
	renaming   string

	// Stats (SASASH.4.8): every duration the UI renders comes from clock;
	// hist is the per-path session memory behind the sparkline and the
	// stability count; recordHandoff stamps enter/H returns.
	clock         stats.Clock
	hist          map[string]*pathHist
	recordHandoff func(alias string) error
	// handoffHost is the managed alias the running handoff (enter/H) was
	// made to, stamped when the terminal comes back; pendingStamp holds
	// stamps waiting for an in-flight poll of that host to land.
	handoffHost  string
	pendingStamp map[string]func() error // stamp to run on the host's next poll

	// Spawn (SASASH.4.10): enter opens beside the TUI through the
	// multiplexer; S is the classic handoff.
	spawn spawnState

	// Path selection (SASASH.2.10): the highlighted named path row while
	// the paths pane holds focus.
	pathIdx int

	// editing is the file the running editor handoff was opened on.
	editing string

	// Popup (SASASH.4.11): the attention tier, open or zero; corruptSeen
	// remembers which hosts' cache problems were already shown so the
	// popup opens once per host per session, not on every poll.
	popup       popup
	corruptSeen map[string]bool
	stateDir    string

	// p (SASASH.2.6): the cycle hook, the block read behind its preview
	// (cached per alias in blocks), the switch stamp, and the hosts with
	// a switch running — a marker of its own, so finishing a switch can
	// never release a poll's gate.
	setPath      func(alias, name string) (pathswitch.Result, error)
	pathBlock    func(alias string) (paths []probe.Path, hostName string, managed bool)
	recordSwitch func(alias, pathName string) error
	blocks       map[string]pathBlock
	switching    map[string]bool

	// fold (SASASH.2.7): generated <alias>.<path> variants fold into
	// their parent's detail pane unless shown; reselect is an alias to
	// put the cursor back on once a filter's match set lands.
	fold     *foldState
	reselect string

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
	// InspectControl reads what a managed host's block has of the
	// ControlMaster trio; EnableControl writes the missing lines through
	// controlmaster.Enable (4.18). Both nil disables the offer that
	// follows a No-ControlPath failure.
	InspectControl func(alias string) (ControlStatus, error)
	EnableControl  func(alias string) ([]string, error)
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
	// Tunnels drives the tunnels section (status polling, space/r/X);
	// nil leaves the section out. The cmd layer passes a tunnel.Engine.
	Tunnels Tunneler
	// Actions are the actions.toml entry names (action.Load, file order),
	// listed in the full help; ActionsWarn is a load problem to show at
	// startup — under the alt screen stderr is invisible.
	Actions     []string
	ActionsWarn string
	// Home is the directory setup.Check/Ensure inspect and bootstrap from
	// the settings panel; "" disables that entry.
	Home string
	// SaveForwardSet declares a named forward set for a host (the settings
	// "add new tunnel" entry) — desire only, nothing is started; nil
	// disables the entry. The cmd layer wires fwdset.Add on sasash.conf.
	SaveForwardSet func(alias, name string, specs []string) error
	// SaveForwardEdit rewrites one spec of a declared set in place (the E
	// key); nil makes E live-only. The cmd layer wires fwdset.Update.
	SaveForwardEdit func(alias, set, old, new string) error
	// RenameHost renames a managed host (the R key, SASASH.4.16): the
	// cmd layer wires pathswitch.Rename on sasash.conf and then moves the
	// state-cache files pathswitch.CacheRenames lists, wrapping a failed
	// move in ErrCacheKept; nil disables R.
	RenameHost func(old, new string) (pathswitch.RenameResult, error)
	// Clock is the time source for every duration rendered (uptime,
	// down-since, ago); nil is wall time. Tests inject a fake and never
	// sleep.
	Clock stats.Clock
	// SetPath makes a named path active (the p picker's enter): the cmd
	// layer wires pathswitch.Set on sasash.conf; nil disables p.
	// PathBlock reads the alias's sasash.conf block — its paths and
	// HostName, the exact inputs the switcher uses; managed is false when
	// there is no block — so p offers only rows the switcher knows.
	// RecordSwitch stamps a successful switch through stats.Store.
	SetPath      func(alias, name string) (pathswitch.Result, error)
	PathBlock    func(alias string) (paths []probe.Path, hostName string, managed bool)
	RecordSwitch func(alias, pathName string) error
	// RecordHandoff stamps the last interactive handoff (enter/H) for a
	// managed host after the terminal comes back; nil disables. The cmd
	// layer wires stats.Store.RecordHandoff. The stamp rides the host's
	// next status poll (one writer per host), so it needs Tunnels too.
	// Best-effort: a failed stamp is a notice, never a blocked action. A
	// handoff that never started (ssh missing) is not stamped.
	RecordHandoff func(alias string) error
	// Spawn is enter's multiplexer wiring (SASASH.4.10): what was
	// detected, the OS-window template, and the persisted target.
	Spawn SpawnConfig
	// StateDir is where the tunnel engine keeps its caches, named in the
	// popup when one is unreadable; "" leaves the path out.
	StateDir string
}

// paneFocus says which pane the navigation keys move (SPEC §5: l/tab
// and h/esc switch it; the focused pane's selection is lit, the other's
// dim).
type paneFocus int

const (
	focusList    paneFocus = iota
	focusTunnels           // j/k move the forward highlight
	focusPaths             // j/k move the path highlight (p)
)

// promptMode says which prompt, if any, owns the keyboard.
type promptMode int

const (
	promptNone promptMode = iota
	promptConnect
	promptAlias
	promptForward // `+`: an ad-hoc forward spec for the selected host
	promptNewConn // settings: a target to save first, then offer to connect
	promptOffer   // settings: "connect now?" after the save
	promptTunnelHost
	promptTunnelName
	promptTunnelSpecs
	promptEditFwd // E: a highlighted forward's spec, prefilled
	promptRename  // R: the selected host's alias, prefilled

	promptModeCount // sentinel: every mode above is walked by the keys panel
)

// New builds the root model. The prober's zero value probes for real and
// runner should be run.OS{} outside tests; tests inject fakes.
func New(hosts []hostmodel.Host, prober probe.Prober, runner run.Runner, cfg Config) Model {
	keys := DefaultKeyMap()

	// Generated pinned variants fold into their parent by default;
	// hidden hosts leave the rows. The rows themselves are built once
	// the model can classify tiers (below).
	fold := &foldState{variants: computeVariants(hosts), hidden: hiddenSet(hosts)}
	l := list.New(nil, hostDelegate{}, 0, 0)
	l.Filter = foldFilter(fold)
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
		hosts:          hosts,
		fold:           fold,
		list:           l,
		help:           contextHelp(),
		keys:           keys,
		prober:         prober,
		runner:         runner,
		saveTheme:      cfg.SaveTheme,
		prompt:         prompt,
		saveHost:       cfg.SaveHost,
		inspectControl: cfg.InspectControl,
		enableControl:  cfg.EnableControl,
		loadHosts:      cfg.LoadHosts,
		editor:         cfg.Editor,
		editorHint:     cfg.EditorHint,
		editorWarn:     cfg.EditorWarn,
		confPath:       cfg.ConfPath,
		tunnels:        cfg.Tunnels,
		tstatus:        make(map[string]tunnelState),
		tinflight:      make(map[string]bool),
		actions:        cfg.Actions,
		home:           cfg.Home,

		saveForwardSet:  cfg.SaveForwardSet,
		saveForwardEdit: cfg.SaveForwardEdit,
		renameHost:      cfg.RenameHost,
		clock:           clockOr(cfg.Clock),
		hist:            make(map[string]*pathHist),
		recordHandoff:   cfg.RecordHandoff,
		pendingStamp:    make(map[string]func() error),
		setPath:         cfg.SetPath,
		pathBlock:       cfg.PathBlock,
		recordSwitch:    cfg.RecordSwitch,
		blocks:          make(map[string]pathBlock),
		switching:       make(map[string]bool),
		corruptSeen:     make(map[string]bool),
		stateDir:        cfg.StateDir,
		spawn: spawnState{
			mux:        cfg.Spawn.Mux,
			herdrPane:  cfg.Spawn.HerdrPane,
			herdrEnv:   cfg.Spawn.HerdrEnv,
			emulator:   cfg.Spawn.Emulator,
			window:     cfg.Spawn.WindowTemplate,
			windowPath: cfg.Spawn.WindowPath,
			target:     cfg.Spawn.Target,
			saveTarget: cfg.Spawn.SaveTarget,
		},
		notice: cfg.ActionsWarn,
		fwdIdx: -1,
	}
	idx, _ := ThemeIndex(cfg.Theme) // unknown or "" -> 0, the default
	m = m.applyTheme(idx)
	// The rows need the model to classify tiers, so they are seeded here
	// — Unfiltered at construction, so SetItems' cmd is nil and nothing
	// is dropped (under a filter that cmd is the only refill: see v).
	m.list.SetItems(m.listRows())
	return m
}

// applyTheme is the whole theme switch (crush pattern): rebuild Styles
// from the palette and hand them to every component that holds any.
func (m Model) applyTheme(idx int) Model {
	m.themeIdx = idx
	m.styles = NewStyles(Themes[idx].Palette)
	m.list.SetDelegate(hostDelegate{styles: m.styles, dim: m.focus != focusList})
	m.help.Styles.ShortKey = m.styles.HelpKey
	m.help.Styles.ShortDesc = m.styles.HelpDesc
	m.help.Styles.ShortSeparator = m.styles.HelpSep
	m.help.Styles.FullKey = m.styles.HelpKey
	m.help.Styles.FullDesc = m.styles.HelpDesc
	m.help.Styles.FullSeparator = m.styles.HelpSep
	return m
}

// setFocus moves pane focus: the list selection lights or dims with it,
// and the forward highlight exists only under tunnels focus, bound to
// the host it was taken on.
func (m Model) setFocus(f paneFocus) Model {
	m.focus = f
	m.focusHost = ""
	m.fwdIdx, m.pathIdx = -1, -1
	if f != focusList {
		if host, ok := m.Selected(); ok {
			m.focusHost = host.Alias
		}
	}
	m.list.SetDelegate(hostDelegate{styles: m.styles, dim: f != focusList})
	return m
}

// settleFocus runs after every Update: the tunnels focus is only ever
// valid on the host it was taken on, over rows that still exist. Any
// path that changes the selection (a key, a filter accept, a reload
// clamp) or removes the rows (a reload, a status shrink) hands the list
// back here, so no highlight can migrate to a host the user never
// focused and no frame shows a focus with nothing marked. A shrink
// clamps the highlight onto the last remaining row.
func (m Model) settleFocus() Model {
	host, ok := m.Selected()
	switch m.focus {
	case focusTunnels:
		rows, _ := m.forwardRows(host)
		if !ok || host.Alias != m.focusHost || len(rows) == 0 {
			return m.setFocus(focusList)
		}
		m.fwdIdx = min(m.fwdIdx, len(rows)-1)
	case focusPaths:
		if !ok || host.Alias != m.focusHost {
			return m.setFocus(focusList)
		}
		rows, _ := pathRows(host, m.block(host.Alias))
		if len(rows) == 0 {
			return m.setFocus(focusList)
		}
		m.pathIdx = min(m.pathIdx, len(rows)-1)
	}
	return m
}

// focusTunnelsOn is the tunnels focus on host when it has forward rows,
// else a notice — the way l (from the list or the paths pane) lands there.
func (m Model) focusTunnelsOn(host hostmodel.Host) Model {
	if m.tunnels == nil {
		return m
	}
	if rows, _ := m.forwardRows(host); len(rows) == 0 {
		m.notice = "no tunnels on " + host.Alias + " — + adds one"
		return m
	}
	m = m.setFocus(focusTunnels)
	m.fwdIdx = 0
	return m
}

// tunnelsFocusKey routes navigation while the tunnels pane holds focus:
// j/k (tab/shift+tab) move the highlight, clamped; g/G jump; h/esc go
// back to the list, and so does l — the cycle closes (2.11). Everything
// else is not handled here — action keys keep their meanings in both
// foci. settleFocus guarantees rows exist whenever this runs.
func (m Model) tunnelsFocusKey(msg tea.KeyMsg) (Model, bool) {
	host, _ := m.Selected()
	rows, _ := m.forwardRows(host)
	move := func(d int) Model {
		m.fwdIdx = min(max(m.fwdIdx+d, 0), len(rows)-1)
		return m
	}
	switch {
	case key.Matches(msg, m.keys.Down) || msg.String() == "tab":
		return move(1), true
	case key.Matches(msg, m.keys.Up) || msg.String() == "shift+tab":
		return move(-1), true
	case key.Matches(msg, m.keys.HalfDown):
		return move(m.halfPage()), true
	case key.Matches(msg, m.keys.HalfUp):
		return move(-m.halfPage()), true
	case key.Matches(msg, m.keys.Top):
		m.fwdIdx = 0
		return m, true
	case key.Matches(msg, m.keys.Bottom):
		m.fwdIdx = len(rows) - 1
		return m, true
	case key.Matches(msg, m.keys.FocusLeft), key.Matches(msg, m.keys.ClearFilter), m.cycleKey(msg):
		return m.setFocus(focusList), true
	}
	return m, false
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

// Init kicks off the first probe (and the selected host's tunnel status);
// rendering never waits on either.
func (m Model) Init() tea.Cmd {
	return tea.Batch(probeCmd(m.prober, m.hosts), m.selectedStatusCmd())
}

// Update is update plus settleFocus, so every return path — and there
// are many — leaves the tunnels focus valid or gone. update always
// returns a Model; the unconditional assertion keeps it that way, so a
// return of another type can never quietly skip the settle.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := m.list.Index()
	next, cmd := m.update(msg)
	n := next.(Model)
	// Backstop for the early-return paths (half pages, reloads, a
	// filter clearing): the key path corrects itself before its
	// selection tail, so this is a no-op there.
	skipDivider(&n.list, before)
	return n.settleFocus(), cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before, _ := m.Selected()
	beforeIdx := m.list.Index()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		if m.popup.open() {
			// The room changed (help width included): a parked offset
			// would eat the next key.
			m.popup.offset = min(m.popup.offset, max(len(m.popupBody())-m.popupRoom(), 0))
		}
		// The prompt needs an explicit width to get textinput's horizontal
		// viewport: without one, long input silently loses its tail and
		// cursor past the terminal edge.
		m.prompt.Width = max(10, msg.Width-14)
		m.list.SetSize(m.listWidth(), m.contentHeight())
		return m, nil

	case probeDoneMsg:
		m.statuses = msg.statuses
		m = m.observeProbes(msg.statuses)
		if m.tickArmed {
			return m, nil
		}
		m.tickArmed = true
		return m, tea.Tick(refreshEvery, func(time.Time) tea.Msg { return refreshTickMsg{} })

	case refreshTickMsg:
		m.tickArmed = false
		return m, tea.Batch(probeCmd(m.prober, m.hosts), m.selectedStatusCmd())

	case tunnelStatusMsg:
		delete(m.tinflight, msg.host)
		m.tstatus[msg.host] = tunnelState{status: msg.status, err: msg.err}
		m = m.corruptCaches(msg)
		if msg.stampErr != nil {
			m.notice = "stats not recorded: " + msg.stampErr.Error()
		}
		// A handoff stamp that waited for this poll rides the next one.
		return m.stampedPollCmd(msg.host)

	case forwardConflictMsg:
		return m.forwardConflict(msg)

	case setupStatusMsg:
		if m.settings.pane == settingsSetup {
			m.settings.checkErr = msg.err
			if msg.err == nil {
				st := msg.status
				m.settings.status = &st
			}
		}
		return m, nil

	case tunnelSavedMsg:
		if msg.err != nil {
			m.notice = "not declared: " + msg.err.Error()
			return m, nil
		}
		m.notice = "declared " + msg.name + " on " + msg.host + " — space starts it"
		if m.loadHosts == nil {
			return m, nil
		}
		return m, m.reloadCmd(false)

	case setupDoneMsg:
		if msg.err != nil {
			m.notice = "setup: " + msg.err.Error()
			return m, nil
		}
		m.notice = "set up — sasash.conf is wired into ssh"
		m.editorWarn = "" // the reason for that warning is gone
		// The include may now bring hosts in; and re-check for the panel.
		cmds := []tea.Cmd{setupCheckCmd(m.home)}
		if m.loadHosts != nil {
			cmds = append(cmds, m.reloadCmd(false))
		}
		return m, tea.Batch(cmds...)

	case tunnelDoneMsg:
		delete(m.tinflight, msg.host) // the action is over: polls may run
		switch {
		case msg.err == nil:
			m.notice = msg.done
		case tunnel.StateWriteOnly(msg.err):
			// The ssh side succeeded; only the cache write failed.
			m.notice = "warning: " + oneLine(msg.err, msg.host)
		case noControlPath(msg.err):
			// The block has no ControlMaster trio (4.18): the reason, then
			// the offer to write exactly the missing lines — or, for a host
			// sasash does not manage, where to add them by hand.
			m.notice = msg.label + ": " + oneLine(msg.err, msg.host)
			m = m.offerControl(msg.host, msg.label, reasonLines(msg.err))
		default:
			// The whole reason, not one line of it: ssh's stderr is the
			// diagnosis, and the argv it ran is the first line.
			m.notice = msg.label + ": " + oneLine(msg.err, msg.host)
			m = m.show(msg.label+" failed on "+msg.host, reasonLines(msg.err)...)
		}
		// Reality may have changed either way: re-derive it.
		if host, ok := m.hostByAlias(msg.host); ok {
			return m, m.tunnelStatusCmd(host)
		}
		return m, nil

	case spawnDoneMsg:
		if msg.err != nil {
			m.notice = cleanLine(msg.err.Error())
			lines := []string{}
			if len(msg.argv) > 0 {
				// The argv comes from the spawn-window file: cleaned.
				lines = append(lines, "ran: "+cleanLine(strings.Join(msg.argv, " ")))
			}
			lines = append(lines, reasonLines(msg.err)...)
			if msg.where == "window" {
				// A broken default is self-serviceable: name the file.
				lines = append(lines, "")
				lines = append(lines, m.spawn.windowHint()...)
			}
			m = m.show("spawn failed for "+msg.alias, lines...)
			return m, nil
		}
		m.notice = "spawned " + msg.what + " → " + msg.where
		// A spawn is a connection: it counts as the last one made.
		m, stamp := m.stampHandoff(msg.alias)
		return m, stamp

	case spawnTargetSavedMsg:
		if msg.err != nil {
			m.notice = "spawn target not saved: " + msg.err.Error()
		}
		return m, nil

	case sshExitMsg:
		// A non-zero ssh exit is not news — ssh printed its own diagnosis
		// to the real terminal during the handoff. Only a failure to start
		// (missing binary) needs surfacing here.
		var exitErr *exec.ExitError
		started := msg.err == nil || errors.As(msg.err, &exitErr)
		if !started {
			m.notice = "ssh: " + msg.err.Error()
		}
		return m.handoffReturned(started)

	case copyDoneMsg:
		return m.copied(msg), nil

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
		// The setup warning is about sasash.conf not being wired into
		// ssh: an edit to ~/.ssh/config or an Included file is seen fine.
		if m.editorWarn != "" && filepath.Clean(m.editing) == filepath.Clean(m.confPath) {
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
		return m.offerSave()

	case controlEnabledMsg:
		if msg.err != nil {
			m.notice = "master reuse not enabled on " + msg.host + ": " + msg.err.Error()
			return m, nil
		}
		// The instruction leads: it is what an 80-column line must keep
		// (the popup already listed the lines).
		m.notice = "reuse enabled on " + msg.host + " — connect, then r (" + strings.Join(msg.added, ", ") + " written)"
		if m.loadHosts != nil {
			return m, m.reloadCmd(false)
		}
		return m, nil

	case adhocTunnelDoneMsg:
		// The unmanaged run (4.17): the user declined the save, so no
		// offer follows; a success is not a one-liner — the popup says
		// again what was run outside sasash and how to end it.
		if msg.err != nil {
			m.notice = "ssh: " + msg.err.Error()
			return m, probeCmd(m.prober, m.hosts)
		}
		argv := strings.Join(adhocTunnelArgv(m.pending), " ")
		if !msg.res.OK() {
			// -f cannot answer a prompt: the reason is ssh's, the hint ours.
			// ssh echoes the destination ahead of the reason; the user
			// just typed it, so it goes.
			reason := oneLineText(string(msg.res.Stderr))
			reason = strings.TrimPrefix(reason, adhocDest(m.pending)+": ")
			m.notice = "ssh -fN failed: " + cleanLine(reason) + " — keys/agent, or connect once"
			m = m.show("ssh -fN failed", append([]string{argv, "auth must be keys or an agent (BatchMode) — or connect once without -N"}, reasonLines(errors.New(strings.TrimSpace(string(msg.res.Stderr))))...)...)
		} else {
			m.notice = "unmanaged tunnel up — not in the tunnels pane; " + unmanagedCleanup
			m = m.show("unmanaged tunnel up", argv, "it runs outside sasash: not in the tunnels pane, and x/X cannot stop it", unmanagedCleanup)
		}
		return m, probeCmd(m.prober, m.hosts)

	case switchDoneMsg:
		delete(m.switching, msg.host)
		delete(m.blocks, msg.host) // the block changed: re-read on the next frame
		if msg.err != nil {
			m.notice = "path: " + msg.err.Error()
			return m, nil
		}
		m.notice = switchNotice(msg.res, len(namedPaths(msg.before.paths)), msg.before.hostName)
		if !msg.res.Changed {
			return m, nil
		}
		// The switch is on disk: remember it on the host's next poll (one
		// writer per host), and re-read the config so the pane follows.
		var stamp tea.Cmd
		if m.recordSwitch != nil {
			rec, alias, name := m.recordSwitch, msg.host, msg.res.To.Name
			m, stamp = m.stampVia(alias, func() error { return rec(alias, name) })
		}
		if m.loadHosts != nil {
			return m, tea.Batch(m.reloadCmd(false), stamp)
		}
		return m, tea.Batch(probeCmd(m.prober, m.hosts), stamp)

	case renameDoneMsg:
		return m.renamed(msg)

	case editFwdDoneMsg:
		delete(m.tinflight, msg.host)
		if msg.err != nil {
			m.notice = msg.err.Error() // built with its own context and ssh's reason
			m = m.show("edit forward failed on "+msg.host, reasonLines(msg.err)...)
		} else {
			m.notice = msg.done
		}
		cmds := []tea.Cmd{}
		if host, ok := m.hostByAlias(msg.host); ok {
			cmds = append(cmds, m.tunnelStatusCmd(host))
		}
		if m.loadHosts != nil {
			cmds = append(cmds, m.reloadCmd(false))
		}
		return m, tea.Batch(cmds...)

	case adhocSavedMsg:
		offer := m.saveThenConnect
		m.saveThenConnect = false
		if msg.err != nil {
			m.notice = "not saved: " + msg.err.Error()
			if msg.tunnel {
				// The tunnel is not dropped with the save: the choice the
				// user was one esc away from is put up (4.17), under the
				// save's own reason.
				m.pending = msg.target
				m = m.offerUnmanagedTunnel()
				m.notice = "not saved: " + msg.err.Error() + " — run it unmanaged? enter · esc cancels"
			}
			return m, nil
		}
		m.notice = "saved " + msg.alias
		if m.home != "" {
			// The saved block's ControlPath points under ~/.ssh/cm: setup's
			// directory, said here when it is not there yet (4.18). One
			// read of ~/.ssh/config per save; an unreadable config is the
			// setup pane's story, so its error is not repeated here.
			if st, err := setup.Check(m.home); err == nil && !st.CMDirPresent {
				m.notice += " — ~/.ssh/cm is missing: o → create config file, or `sasash setup`"
			}
		}
		var reload tea.Cmd
		if m.loadHosts != nil {
			reload = m.reloadCmd(false)
		}
		if msg.tunnel {
			// Save-first tunnel (4.17): the block is on disk, so ssh
			// resolves the alias now; the engine brings the tunnel up
			// through it while the list reloads.
			next, start := m.engineTunnel(msg.alias, msg.target.Forwards)
			return next, tea.Batch(reload, start)
		}
		if offer {
			// Save-first flow: the entry is on disk; now offer the ssh.
			m.mode, m.offerAlias = promptOffer, msg.alias
			m.prompt.Reset()
			m.prompt.Blur()
		}
		return m, reload

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
			if len(msg.warnings) > 1 {
				lines := make([]string, len(msg.warnings))
				for i, w := range msg.warnings {
					lines[i] = cleanLine(w.Error())
				}
				m = m.show("config: "+fmt.Sprint(len(msg.warnings))+" warnings", lines...)
			}
		}
		before := pathList(m.hosts)
		m.hosts = msg.hosts
		m = m.pruneHist()
		clear(m.blocks) // sasash.conf may have changed under the preview
		// The last probe round is index-aligned with the OLD paths: when
		// they changed in any way — order included — a host would read
		// another path's state until the batched re-probe lands, so
		// pending is the honest state. Unchanged paths keep their round
		// (the reloads tunnel actions trigger must not blank the pane).
		if !samePaths(before, pathList(m.hosts)) {
			m.statuses = nil
		}
		// The rows replace the list (variants re-folded from the new
		// hosts); the selection stays on its alias when still listed,
		// else clamped — SetItems never moves the cursor by itself. Under
		// a filter the match set is empty until the FilterMatchesMsg
		// lands — that arm re-selects and clamps.
		m.fold.variants = computeVariants(m.hosts)
		m.fold.hidden = hiddenSet(m.hosts)
		cmd := m.setRows(m.listRows())
		// The clamp can land on the divider when the selected host went
		// away: off it before the status poll reads the selection.
		skipDivider(&m.list, beforeIdx)
		return m, tea.Batch(cmd, probeCmd(m.prober, m.hosts), m.selectedStatusCmd())

	case list.FilterMatchesMsg:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		if m.reselect != "" {
			for i, it := range m.list.VisibleItems() {
				if rowKey(it) == m.reselect {
					m.list.Select(i)
				}
			}
			m.reselect = ""
		}
		clampCursor(&m.list)
		return m, cmd

	case tea.KeyMsg:
		if key.Matches(msg, m.keys.ForceQuit) {
			return m, tea.Quit
		}
		// The popup owns the keyboard while open (SPEC §5: action keys
		// inert, like the filter) — and its keys do not clear the
		// one-line notice: closing it leaves the outcome on the line.
		if m.popup.open() {
			return m.popupKey(msg)
		}
		m.notice = ""
		// The settings panel owns the keyboard while open.
		if m.settings.pane != settingsOff {
			return m.settingsKey(msg)
		}
		// A focused prompt owns the keyboard the way the filter input
		// does: esc cancels (declining the save writes nothing), enter
		// submits, everything else is typed text.
		if m.mode != promptNone {
			switch {
			case key.Matches(msg, m.keys.ClearFilter):
				if m.mode == promptAlias {
					return m.declineSave()
				}
				m.mode = promptNone
				m.saveThenConnect = false
				m.prompt.Blur()
				return m, nil
			case msg.Type == tea.KeyEnter:
				switch m.mode {
				case promptForward:
					return m.submitForward()
				case promptNewConn:
					return m.submitNewConn()
				case promptOffer:
					m.mode = promptNone
					m.handoffHost = m.offerAlias
					return m, tea.ExecProcess(sshExec(m.offerAlias), func(err error) tea.Msg {
						return sshExitMsg{err: err}
					})
				case promptTunnelHost, promptTunnelName, promptTunnelSpecs:
					return m.submitTunnel()
				case promptEditFwd:
					return m.submitEditFwd()
				case promptRename:
					return m.submitRename()
				}
				return m.promptSubmit()
			}
			if m.mode == promptOffer {
				return m, nil // enter or esc only; nothing to type
			}
			var cmd tea.Cmd
			m.prompt, cmd = m.prompt.Update(msg)
			return m, cmd
		}
		// Action keys must never fire while the filter input is focused
		// (SPEC §5 addendum): everything below is typed text then, and
		// only the list — which owns the input — sees the key.
		if m.list.FilterState() != list.Filtering {
			if m.focus == focusTunnels {
				if next, handled := m.tunnelsFocusKey(msg); handled {
					return next, nil
				} else {
					m = next
				}
			}
			if m.focus == focusPaths {
				if next, cmd, handled := m.pathsFocusKey(msg); handled {
					return next, cmd
				} else {
					m = next
				}
			}
			switch {
			case key.Matches(msg, m.keys.Quit):
				return m, tea.Quit
			case key.Matches(msg, m.keys.Connect):
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				plan := m.spawn.planFor(host.Alias)
				if plan == nil {
					// Nothing to spawn into: the classic handoff, and the
					// notice waits on the line for the terminal's return.
					m.notice = "nothing to spawn into — handed off; S does this on purpose"
					return m.handoff(host.Alias)
				}
				if plan.err != nil {
					m.notice = "spawn: " + plan.err.Error()
					return m, nil
				}
				return m, spawnCmd(m.runner, host.Alias, plan)
			case key.Matches(msg, m.keys.Handoff):
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				return m.handoff(host.Alias)
			case key.Matches(msg, m.keys.PickPath):
				host, ok := m.Selected()
				if !ok || m.setPath == nil || m.pathBlock == nil {
					return m, nil // both hooks or neither: nil disables p
				}
				if m.switching[host.Alias] {
					m.notice = "a switch is already running for " + host.Alias
					return m, nil
				}
				if m.focus == focusPaths {
					return m, nil // already there: p is idempotent
				}
				return m.focusPathsOn(host), nil
			case key.Matches(msg, m.keys.Herdr):
				// H spawns beside the TUI like enter (4.22), never a
				// handoff: herdr's remote session in a new tab/pane/window,
				// or — inside herdr, where --remote would nest — the same
				// tab with plain ssh.
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				plan := m.spawn.herdrPlanFor(host.Alias)
				if plan == nil {
					// No emulator to drive: the command is the user's to
					// run, in a tab outside herdr — never a nesting or
					// handoff fallback.
					return m.show(hNoTabTitle,
						"H runs herdr --remote in a new tab of the outer terminal emulator,",
						"a fresh process outside herdr, so it never nests. None sasash can",
						"drive is around (wezterm, kitty, iTerm2, Terminal.app).",
						"",
						"run it yourself, in a new tab outside herdr:",
						"  "+strings.Join(herdrCommand(host.Alias), " ")), nil
				}
				return m, spawnCmd(m.runner, host.Alias, plan)
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
				argv, _ := m.actionPreview()
				if argv == nil {
					return m, nil
				}
				// A spawn line is not a shell command (two steps, a pane
				// id still unknown, or a template error): the connection
				// it makes is, so that is what the clipboard gets.
				if host, ok := m.Selected(); ok {
					if _, hl := m.highlighted(host); !hl && m.spawn.planFor(host.Alias) != nil {
						argv = sshCommand(host.Alias)
					}
				}
				return m, clipCmd(m.runner, strings.Join(argv, " "))
			case key.Matches(msg, m.keys.FocusRight):
				// The pane-focus cycle (SASASH.2.11): list → paths → tunnels
				// → list. From the list, the paths pane first when the host
				// has a path to choose — the same focus p gives — else on
				// to the tunnels.
				host, ok := m.Selected()
				if !ok {
					return m, nil
				}
				b, choice := m.pathChoice(host)
				switch {
				case choice == choiceOK && m.switching[host.Alias]:
					m.notice = "a switch is already running for " + host.Alias
					return m, nil
				case choice == choiceOK:
					return m.focusPathsWith(host, b), nil
				}
				m = m.focusTunnelsOn(host)
				if choice == choiceShadowed {
					// The diagnostic p would give is kept on the way past —
					// set last, so a host without tunnels does not talk over it.
					m.notice = shadowedPaths
				}
				return m, nil
			case key.Matches(msg, m.keys.FocusLeft):
				return m, nil // already on the list
			case key.Matches(msg, m.keys.Toggle):
				host, ok := m.Selected()
				if !ok || m.tunnels == nil {
					return m, nil
				}
				row, ok := m.highlighted(host)
				if !ok {
					m.notice = "no forward highlighted — l cycles to the tunnels pane"
					return m, nil
				}
				return m, m.toggleCmd(host, row)
			case key.Matches(msg, m.keys.AddFwd):
				host, ok := m.Selected()
				if !ok || m.tunnels == nil {
					return m, nil
				}
				return m.openForward(host, "")
			case key.Matches(msg, m.keys.CancelFwd):
				host, ok := m.Selected()
				if !ok || m.tunnels == nil {
					return m, nil
				}
				row, ok := m.highlighted(host)
				if !ok {
					m.notice = "no forward highlighted — l cycles to the tunnels pane"
					return m, nil
				}
				return m, m.cancelCmd(host, row)
			case key.Matches(msg, m.keys.EditFwd):
				host, ok := m.Selected()
				if !ok || m.tunnels == nil {
					return m, nil
				}
				row, ok := m.highlighted(host)
				if !ok {
					m.notice = "no forward highlighted — l cycles to the tunnels pane"
					return m, nil
				}
				return m.openEditFwd(host, row)
			case key.Matches(msg, m.keys.Restart):
				host, ok := m.Selected()
				if !ok || m.tunnels == nil {
					return m, nil
				}
				return m, m.restartCmd(host)
			case key.Matches(msg, m.keys.Exit):
				host, ok := m.Selected()
				if !ok || m.tunnels == nil {
					return m, nil
				}
				return m, m.exitCmd(host)
			case key.Matches(msg, m.keys.Edit):
				if len(m.editor) == 0 || m.confPath == "" {
					return m, nil
				}
				m.editing = m.editTarget()
				return m, tea.ExecProcess(m.editorExec(), func(err error) tea.Msg {
					return editorExitMsg{err: err}
				})
			case key.Matches(msg, m.keys.Adhoc):
				m.saveThenConnect = false // the ad-hoc flow connects first
				m.mode = promptConnect
				m.prompt.Reset()
				m.prompt.Placeholder = "[user@]host[:port] [-L local:host:port] [-p port] [-N]  or a pasted ssh command"
				return m, m.prompt.Focus()
			case key.Matches(msg, m.keys.Theme):
				return m.setTheme((m.themeIdx + 1) % len(Themes))
			case key.Matches(msg, m.keys.Settings):
				return m.openSettings(), nil
			case key.Matches(msg, m.keys.Rename):
				return m.openRename()
			case key.Matches(msg, m.keys.Help):
				return m.openHelp(), nil
			case key.Matches(msg, m.keys.ClearFilter):
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
	// The rows follow the filter: every host while a term stands (the
	// filter function folds the rest), the listed hosts otherwise. Under
	// a filter the list's own match round was computed over the old
	// rows, so SetItems' round — the fresher one — REPLACES it whenever
	// the rows change there: one round per key, structurally. (The
	// replaced cmd also carried the input's blink; the cursor sits solid
	// until the next key. The right trade against a racing round.) Back
	// out of a filter (esc) and the rows are item space again, so the
	// selection is kept exactly.
	if m.rowsDiffer(m.listRows()) {
		if m.list.FilterState() != list.Unfiltered {
			cmd = m.setRows(m.listRows())
		} else {
			cmd = tea.Batch(cmd, m.setRows(m.listRows()))
		}
	}
	// Off the divider BEFORE the selection tail below, so crossing it
	// counts as the host change it is: the new host's status is fetched,
	// never the old host's shown as its own.
	skipDivider(&m.list, beforeIdx)
	// A new host under the cursor: whatever status it had is stale — the
	// tick refreshes only the selected host — so its rows go back to
	// pending while a fresh poll runs. Never show a stale state as live.
	// (The focus, if it was the tunnels pane's, is settled by Update.)
	if after, ok := m.Selected(); ok && after.Alias != before.Alias {
		delete(m.tstatus, after.Alias)
		cmd = tea.Batch(cmd, m.tunnelStatusCmd(after))
	}
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
// line, and the footer — one line always: the whisper, or a surface's
// context hint (SASASH.4.13).
func (m Model) contentHeight() int {
	h := m.height - 3 - lipgloss.Height(m.helpView())
	if m.expansionLine() != "" {
		h-- // the expansion line under the prompt
	}
	if h < 1 {
		h = 1
	}
	return h
}

// helpView renders the footer: a surface's one-line context hint,
// generated from its key bindings, while one holds the keys, else the
// whisper. help.Width makes bubbles truncate with an ellipsis, but its
// truncation has an off-by-a-few edge (an item landing within two cells
// of the limit is added anyway, and everything after it too), so the
// line gets the same hard clamp as the rest of the chrome, and a height
// clamp for a very short terminal.
func (m Model) helpView() string {
	var k help.KeyMap
	// A keyboard owner (a panel, a prompt, the filter) describes enter
	// before a pane focus does: the line must never promise a path switch
	// while a prompt has the key.
	switch {
	case m.popup.open():
		k = popupKeys{confirm: m.popup.confirm != nil, verb: m.popup.verb}
	case m.settings.pane != settingsOff:
		k = settingsKeys{}
	case m.mode != promptNone:
		k = promptKeys{mode: m.mode}
	case m.list.FilterState() == list.Filtering:
		k = filterKeys{}
	case m.focus == focusPaths:
		k = pathsKeys{}
	case m.focus == focusTunnels:
		k = tunnelsKeys{k: m.keys}
	}
	// The main view's footer is a whisper (SASASH.4.13): every key lives
	// in the ? panel. A keyboard owner or a pane focus keeps its one-line
	// context help, because that line says what the keys do there.
	if k == nil {
		return m.styles.Clamp.MaxWidth(m.width).Render(" " + m.whisper())
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
	// The tally is decoration next to the filter term, which explains
	// why the list is a subset: it is appended only when both fit whole.
	if sum := m.summaryLine(); sum != "" && m.settings.pane == settingsOff {
		if with := header + "   " + m.styles.PathExtra.Render(sum); lipgloss.Width(with) <= m.width {
			header = with
		}
	}
	// The applied-filter term is user content: clamp like all chrome.
	header = m.styles.Clamp.MaxWidth(m.width).Render(header)

	var body string
	if m.popup.open() {
		// The box floats centered over the body's place (4.14).
		panel := m.styles.Clamp.MaxWidth(m.width).Render(m.popupView())
		body = lipgloss.Place(m.width, m.contentHeight(), lipgloss.Center, lipgloss.Center, panel)
	} else if m.settings.pane != settingsOff {
		header += "   " + m.styles.CmdPreview.Render("settings")
		header = m.styles.Clamp.MaxWidth(m.width).Render(header)
		panel := m.styles.Clamp.MaxWidth(m.width).Render(m.settingsView())
		body = lipgloss.Place(m.width, m.contentHeight(), lipgloss.Center, lipgloss.Center, panel)
	} else if len(m.hosts) == 0 {
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
	if m.settings.pane != settingsOff {
		preview = "" // the panel's own footer says what enter does
	}
	// Under the popup the bottom line is its one-line partner: every
	// popup sets one, and a notice replaces the preview below.
	if m.mode != promptNone {
		label := "connect:"
		switch m.mode {
		case promptAlias:
			label = "save as:"
		case promptForward:
			label = "forward " + m.fwdHost + ":"
		case promptNewConn:
			label = "new connection:"
		case promptOffer:
			label = "connect to " + m.offerAlias + " now? enter · esc skips"
		case promptTunnelHost:
			label = "tunnel host:"
		case promptTunnelName:
			label = "set name:"
		case promptTunnelSpecs:
			label = "specs:"
		case promptEditFwd:
			label = "edit " + setLabel(m.editFwd.set) + " on " + m.editFwd.host + ":"
		case promptRename:
			label = "rename " + m.renaming + " to:"
		}
		// textinput pads its value to Width; trim the raw padding so a
		// notice (a rejection message, typically) still fits the line.
		field := strings.TrimRight(m.prompt.View(), " ")
		if m.mode == promptOffer || (m.notice != "" && m.prompt.Value() == "") {
			// A yes/no line has nothing to type; an empty field under a
			// notice yields its placeholder so the notice — a reason,
			// typically — gets the width. Typing clears the notice.
			field = ""
		}
		preview = " " + m.styles.CmdPrompt.Render(label) + " " + field
	}
	// A notice takes the line on its own: appended after a long preview
	// it would be clamped away, and the notice carries ssh's own reason.
	// The next keypress clears it and the preview returns.
	if m.notice != "" && m.mode == promptNone {
		preview = " " + m.styles.Notice.Render(m.notice)
	} else if m.notice != "" {
		preview += "  " + m.styles.Notice.Render(m.notice)
	}
	// Same width clamp as the panes: a long alias (or notice) must not
	// wrap the preview line and push the alt screen around.
	preview = m.styles.Clamp.MaxWidth(m.width).Render(preview)
	// A shorthand's full expansion gets a line of its own under the
	// prompt — what enter will really use is never hidden — cut with a
	// visible ellipsis rather than silently when even that is too long.
	if line := m.expansionLine(); line != "" {
		preview += "\n" + line
	}
	return strings.Join([]string{header, "", body, preview, m.helpView()}, "\n")
}

// expansionLine renders promptExpansion for the frame, "" when none.
func (m Model) expansionLine() string {
	full := m.promptExpansion()
	if full == "" || m.height-3-lipgloss.Height(m.helpView()) < 2 {
		return "" // no row to give up on a tiny terminal
	}
	line := " " + m.styles.PathExtra.Render("→ "+full)
	if lipgloss.Width(line) > m.width {
		r := []rune(" → " + full)
		keep := max(m.width-1, 1)
		if keep < len(r) {
			r = append(r[:keep], '…')
		}
		line = m.styles.PathExtra.Render(string(r))
	}
	return line
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

	// The marker names the first alive path — what ssh reaches through
	// this config — except under the paths focus, where it names the
	// block's HostName row: the thing enter changes.
	// The active marker points at the path ssh ACTUALLY dials — the one
	// whose address is the block HostName — never first-reachable, so a
	// HostName out of sync with a reachable path reads honestly (SASASH.7.2:
	// the reason tunnels to a drifted HostName silently timed out). In the
	// paths focus the block is re-read fresh.
	active := activeMark(host.Paths, m.effectiveHostName(host))
	if m.focus == focusPaths {
		active = m.activePath(host)
	}

	nameWidth := 0
	for _, p := range host.Paths {
		if len(p.Name) > nameWidth {
			nameWidth = len(p.Name)
		}
	}

	var b strings.Builder
	b.WriteString(m.styles.SectionLabel.Render("paths"))
	marked := m.highlightedPath(host)
	for i, p := range host.Paths {
		glyph := m.styles.StatusWait.Render("◌")
		extra := m.styles.PathExtra.Render("…")
		if i < len(sts) {
			if s := sts[i]; s.Alive() {
				glyph = m.styles.StatusUp.Render("●")
				// Milliseconds like `sasash status`; a sub-millisecond
				// round trip keeps its real unit rather than reading 0s.
				extra = m.styles.PathExtra.Render(spellLatency(s.Latency))
			} else {
				glyph = m.styles.StatusDown.Render("○")
				extra = m.styles.PathExtra.Render("down")
			}
		}
		// The mark column exists only while the paths pane holds focus:
		// the rows keep their width (and their sparkline room) otherwise.
		mark := ""
		if m.focus == focusPaths {
			mark = "  "
			if i == marked {
				mark = m.styles.RowMark.Render("› ")
			}
		}
		b.WriteString("\n " + mark + glyph)
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
		// Session memory: the recent latencies as a shape, their range,
		// how many rounds answered, and — for a dead path — since when
		// this session saw it down after being up.
		if h := m.hist[pathKey(host.Alias, p)]; h != nil && h.total > 0 {
			if i < len(sts) && !sts[i].Alive() {
				if d := since(m.clock.Now(), h.downSince); d != "" {
					b.WriteString(" " + m.styles.PathExtra.Render(d))
				}
			}
			// The shape is sized to what is left of the pane after the
			// range and the count, and shows the NEWEST samples: a
			// truncated ring would otherwise show the oldest.
			tail := " " + fmt.Sprintf("%d/%d", h.up, h.total)
			if r := latencyRange(h.ring); r != "" {
				tail = " " + r + tail
			}
			lineSoFar := lipgloss.Width(strings.TrimLeft(b.String()[strings.LastIndex(b.String(), "\n")+1:], "\n"))
			room := m.width - m.listWidth() - 2 - lineSoFar - lipgloss.Width(tail) - 2
			// The tail is whole or absent: a count cut by the frame clamp
			// would read as a smaller, plausible number.
			if room >= 0 {
				if shape := sparkline(newest(h.ring, room)); shape != "" {
					b.WriteString("  " + m.styles.PathExtra.Render(shape))
				}
				b.WriteString(m.styles.PathExtra.Render(tail))
			}
		}
	}
	if m.tunnels != nil {
		b.WriteString(m.tunnelsView(host))
	}
	return b.String()
}

// previewView is the bottom command line: what enter will run, or — with
// a forward row highlighted — what space will (SPEC §5, "always shows what
// the highlighted action will run"). A set is one ssh call per forward;
// the first is shown and the rest counted.
func (m Model) previewView() string {
	if host, ok := m.Selected(); ok && m.focus == focusPaths {
		return " " + m.styles.HelpDesc.Render(m.pathPlan(host))
	}
	argv, more := m.actionPreview()
	if argv == nil {
		return ""
	}
	v := " " + m.styles.CmdPrompt.Render("$") + " " + m.styles.CmdPreview.Render(strings.Join(argv, " "))
	if more > 0 {
		v += " " + m.styles.PathExtra.Render(fmt.Sprintf("+%d more", more))
	}
	// H is the other handoff on the host row: named beside enter's when
	// it fits whole — a half-rendered command would be a lie, so a long
	// alias drops it rather than clipping it.
	if host, ok := m.Selected(); ok {
		if _, hl := m.highlighted(host); !hl {
			// With a spawn on enter, S is the plain ssh handoff — named
			// first, since the spawn line is the one that hides it.
			if m.spawn.planFor(host.Alias) != nil {
				seg := "   " + m.styles.HelpKey.Render("S") + " " + m.styles.HelpDesc.Render(strings.Join(sshCommand(host.Alias), " "))
				if lipgloss.Width(v+seg) <= m.width {
					v += seg
				}
			}
			// H's command (4.22) and where it opens: the outer emulator's
			// tab inside herdr, a tmux window otherwise; the wrapper argv
			// is the failure popup's, not the footer's.
			hseg := "no terminal tab to open here (shows the command)"
			if plan := m.spawn.herdrPlanFor(host.Alias); plan != nil {
				hseg = strings.Join(plan.what, " ") + " (" + plan.where + ")"
			}
			seg := "   " + m.styles.HelpKey.Render("H") + " " + m.styles.HelpDesc.Render(hseg)
			if lipgloss.Width(v+seg) <= m.width {
				v += seg
			}
			// p's plan: the exact path it will make active, when it fits.
			if plan := m.switchPlan(host); plan != "" {
				seg := "   " + m.styles.HelpDesc.Render(plan)
				if lipgloss.Width(v+seg) <= m.width {
					v += seg
				}
			}
		}
	}
	return v
}
