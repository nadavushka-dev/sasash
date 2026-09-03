package ui

import (
	"os/exec"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// RefreshTick lets the external test package drive the live-refresh loop
// without waiting out the real tick interval.
func RefreshTick() tea.Msg { return refreshTickMsg{} }

// ProbeChunk exposes the refresh fan-out bound for the regression test.
const ProbeChunk = probeChunk

// SSHExit lets tests replay the terminal returning from an ssh handoff.
func SSHExit(err error) tea.Msg { return sshExitMsg{err: err} }

// HandoffHost exposes the managed alias the running handoff was made to
// (stamped when the terminal comes back); PathRing exposes one path's
// session ring.
func HandoffHost(m Model) string { return m.handoffHost }
func PathRing(m Model, alias string, p probe.Path) []time.Duration {
	if h := m.hist[pathKey(alias, p)]; h != nil {
		return append([]time.Duration(nil), h.ring...)
	}
	return nil
}

// Since, Sparkline, and LatencyRange expose the stats spellings for
// table tests (SASASH.4.8).
var (
	Since        = since
	Sparkline    = sparkline
	LatencyRange = latencyRange
)

// ActiveIndex and SwitchNotice expose the p rules and rendering for
// table tests; WithSwitcher wires the p hooks; PickerOpen reports the
// picker owning the keyboard.
var (
	ActiveIndex  = activeIndex
	SwitchNotice = switchNotice
)

func WithSwitcher(m Model, sw func(alias, name string) (pathswitch.Result, error), pb func(string) ([]probe.Path, string, bool), rec func(string, string) error) Model {
	m.setPath, m.pathBlock, m.recordSwitch = sw, pb, rec
	return m
}

func PickerOpen(m Model) bool { return m.picker.open() }

// WithRecordHandoff swaps the handoff stamp hook so tests can observe it.
func WithRecordHandoff(m Model, f func(string) error) Model {
	m.recordHandoff = f
	return m
}

// SSHCommand and ClipArgv expose the argv builders for exact-string tests.
func SSHCommand(alias string) []string { return sshCommand(alias) }
func ClipArgv(text string) []string    { return clipArgv(text) }

// SSHExec exposes the handoff process builder so tests can assert the
// built argv actually reaches the *exec.Cmd.
var SSHExec = sshExec

// FilterState and FilterText expose the list's filter state and typed
// term for the filter-flow tests.
func FilterState(m Model) list.FilterState { return m.list.FilterState() }
func FilterText(m Model) string            { return m.list.FilterValue() }

// ThemeName exposes the active theme for the cycle/selection tests.
func ThemeName(m Model) string { return Themes[m.themeIdx].Name }

// WithSaveTheme swaps the persistence hook so tests can observe it.
func WithSaveTheme(m Model, f func(string) error) Model {
	m.saveTheme = f
	return m
}

// StylesOf and HelpStyles expose the LIVE style state for theme-switch
// assertions: tests run under the Ascii color profile, where every theme
// renders identically, so wiring must be asserted at the style layer
// (GetForeground needs no rendering and no profile).
func StylesOf(m Model) Styles        { return m.styles }
func HelpStyles(m Model) help.Styles { return m.help.Styles }

// StaticCursor turns off the filter and prompt inputs' cursor blink so
// the key-path tests never produce blink tick cmds (which sleep when
// executed).
func StaticCursor(m Model) Model {
	m.list.FilterInput.Cursor.SetMode(cursor.CursorStatic)
	m.prompt.Cursor.SetMode(cursor.CursorStatic)
	return m
}

// ParseAdhocArgv exposes the prompt-input -> exact-ssh-argv pipeline for
// table tests (the argv strings are the product).
func ParseAdhocArgv(input string) ([]string, error) {
	t, err := parseAdhoc(input)
	if err != nil {
		return nil, err
	}
	return adhocArgv(t), nil
}

// ParseAdhocTunnelArgv exposes the tunnel-only (-N) pipeline: the exact
// `ssh -fN` argv and whether the input asked for it.
func ParseAdhocTunnelArgv(input string) (argv []string, tunnelOnly bool, err error) {
	t, err := parseAdhoc(input)
	if err != nil {
		return nil, false, err
	}
	return adhocTunnelArgv(t), t.TunnelOnly, nil
}

// WithSaveForwardEdit swaps the edit-in-place hook so tests can observe it.
func WithSaveForwardEdit(m Model, f func(alias, set, old, new string) error) Model {
	m.saveForwardEdit = f
	return m
}

// AdhocExit replays the terminal returning from an ad-hoc handoff.
func AdhocExit(err error) tea.Msg { return adhocExitMsg{err: err} }

// EditorExit replays the terminal returning from the editor; EditorArgv
// exposes the exact invocation `e` builds.
func EditorExit(err error) tea.Msg { return editorExitMsg{err: err} }

var EditorArgv = editorArgv

// EditorExec exposes the one construction site of the `e` handoff process
// so a test can pin the args that actually reach the *exec.Cmd.
func EditorExec(m Model) *exec.Cmd { return m.editorExec() }

// WithEditor configures the `e` key for tests.
func WithEditor(m Model, editor []string, hint, confPath string) Model {
	m.editor = editor
	m.editorHint = hint
	m.confPath = confPath
	return m
}

// WithEditorWarn sets the setup-incomplete warning for tests.
func WithEditorWarn(m Model, warn string) Model {
	m.editorWarn = warn
	return m
}

// WithSaveHost and WithLoadHosts swap the ad-hoc persistence hooks so
// tests can observe them.
func WithSaveHost(m Model, f func(AdhocEntry) error) Model {
	m.saveHost = f
	return m
}

func WithLoadHosts(m Model, f func() ([]hostmodel.Host, []error, error)) Model {
	m.loadHosts = f
	return m
}

// Tunnel preview argv builders, exposed so the parity test can pin them
// to the engine's real calls.
var (
	StartArgv   = startArgv
	ForwardArgv = forwardArgv
	CancelArgv  = cancelArgv
	ExitArgv    = exitArgv
)

// HerdrCommand and HerdrExec expose the H handoff: the argv and the one
// construction site of its process.
var (
	HerdrCommand = herdrCommand
	HerdrExec    = herdrExec
)

// HerdrExit replays the terminal returning from herdr.
func HerdrExit(err error) tea.Msg { return herdrExitMsg{err: err} }

// ExpandSpec exposes the shorthand expansion for its table test.
var ExpandSpec = expandSpec

// Spawn (SASASH.4.10): the argv matrix and the plan preview, for exact-
// string tests; HerdrPaneID for the reply parser.
var (
	HerdrCreateArgv = herdrCreateArgv
	HerdrRunArgv    = herdrRunArgv
	TmuxArgv        = tmuxArgv
	WindowArgv      = windowArgv
	HerdrPaneID     = herdrPaneID
)

// SpawnTargetOf reports the live spawn target.
func SpawnTargetOf(m Model) string { return m.spawnTarget() }

// LocalPort and WithLocalPort expose the forward-spec port rule so it can
// be pinned to the engine's and to exact rewrites.
var (
	LocalPort     = localPort
	WithLocalPort = withLocalPort
)

// TunnelsFocused reports whether the tunnels pane holds focus.
func TunnelsFocused(m Model) bool { return m.focus == focusTunnels }

// SettingsOpen reports whether the `o` panel owns the keyboard.
func SettingsOpen(m Model) bool { return m.settings.pane != settingsOff }

// WithSaveForwardSet swaps the tunnel-declaration hook so tests can
// observe it.
func WithSaveForwardSet(m Model, f func(alias, name string, specs []string) error) Model {
	m.saveForwardSet = f
	return m
}

// WithHome sets the directory the settings panel bootstraps.
func WithHome(m Model, home string) Model {
	m.home = home
	return m
}

// Prompting and PromptingSave expose which prompt, if any, owns the keys.
func Prompting(m Model) bool     { return m.mode != promptNone }
func PromptingSave(m Model) bool { return m.mode == promptAlias }
