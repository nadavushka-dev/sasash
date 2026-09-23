package ui

import (
	"os/exec"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/setup"
	"github.com/nadavushka-dev/sasash/internal/transfer"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
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
// table tests; WithSwitcher wires the p hooks; PathsFocused reports the
// paths pane holding focus.
var (
	ActiveIndex  = activeIndex
	SwitchNotice = switchNotice
)

func WithSwitcher(m Model, sw func(alias, name string) (pathswitch.Result, error), pb func(string) ([]probe.Path, string, bool), rec func(string, string) error) Model {
	m.setPath, m.pathBlock, m.recordSwitch = sw, pb, rec
	return m
}

func PathsFocused(m Model) bool { return m.focus == focusPaths }

// IsManaged and EditTarget expose the tier classification and e's file
// (SASASH.2.9) for table tests.
func IsManaged(m Model, h hostmodel.Host) bool { return m.managed(h) }
func EditTarget(m Model) string                { return m.editTarget() }

// PopupOpen and PopupTitle expose the attention tier (SASASH.4.11);
// TunnelStatus replays a status poll result.
func PopupOpen(m Model) bool { return m.popup.open() }
func HelpOpen(m Model) bool  { return m.popup.open() && m.popup.title == helpTitle }

// ContextKeymaps lists every footer keymap a surface can show, so the
// keys-panel test can walk them (the prompt modes each contribute).
func ContextKeymaps() []help.KeyMap {
	// (filterKeys stays out: SPEC §5 keeps the filter's two keys to its
	// own footer.)
	// (A question's footer names enter's deed per question — "run it",
	// "write it" — and the panel carries one row for all of them.)
	out := []help.KeyMap{pathsKeys{}, tunnelsKeys{k: DefaultKeyMap()}, settingsKeys{}, popupKeys{}, transferKeys{step: xferDirection}, transferKeys{step: xferLocal, source: true}}
	for mode := promptConnect; mode < promptModeCount; mode++ {
		out = append(out, promptKeys{mode: mode})
	}
	return out
}
func PopupTitle(m Model) string { return m.popup.title }
func TunnelStatus(host string, st tunnel.HostStatus, err error) tea.Msg {
	return tunnelStatusMsg{host: host, status: st, err: err}
}

// Editing reports the file the running editor handoff was opened on;
// SettingsEditExec is the process settings' "edit connections file" builds;
// AmbientRows lists the rows the list currently holds below the tier
// line (styled dim), in order.
func Editing(m Model) string             { return m.editing }
func SettingsEditExec(m Model) *exec.Cmd { return settingsEditExec(m) }
func AmbientRows(m Model) []string {
	var out []string
	for _, it := range m.list.Items() {
		if h, ok := it.(hostItem); ok && h.ambient {
			out = append(out, h.host.Alias)
		}
	}
	return out
}

// WithRecordHandoff swaps the handoff stamp hook so tests can observe it.
func WithRecordHandoff(m Model, f func(string) error) Model {
	m.recordHandoff = f
	return m
}

// SSHCommand, Osc52Argv and ClipToolArgv expose the argv builders for
// exact-string tests.
func SSHCommand(alias string) []string  { return sshCommand(alias) }
func Osc52Argv(text string) []string    { return osc52Argv(text) }
func ClipToolArgv(text string) []string { return clipToolArgv(text) }

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
func StylesOf(m Model) Styles            { return m.styles }
func WithStyles(m Model, s Styles) Model { m.styles = s; return m }

// EntryLabels is the settings menu's labels, in menu order.
func EntryLabels() []string          { return entryLabels[:] }
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

// ParseAdhocFor exposes the selection-defaulted parse: the argv (the
// tunnel form when -N), whether the selection supplied the target, or
// the error.
func ParseAdhocFor(input, selected string) (argv []string, fromSelection bool, err error) {
	t, err := parseAdhocFor(input, selected)
	if err != nil {
		return nil, false, err
	}
	if t.TunnelOnly {
		return adhocTunnelArgv(t), t.FromSelection, nil
	}
	return adhocArgv(t), t.FromSelection, nil
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

// WithControlHooks swaps the ControlMaster-trio hooks (4.18).
func WithControlHooks(m Model, inspect func(string) (ControlStatus, error), enable func(string) ([]string, error)) Model {
	m.inspectControl, m.enableControl = inspect, enable
	return m
}

func WithLoadHosts(m Model, f func() ([]hostmodel.Host, []error, error)) Model {
	m.loadHosts = f
	return m
}

// Tunnel preview argv builders, exposed so the parity test can pin them
// to the engine's real calls.
var (
	SpawnArgv = spawnArgv
	KillArgv  = killArgv
	ExitArgv  = exitArgv
)

// HerdrCommand and HerdrExec expose the H handoff: the argv and the one
// construction site of its process.
var (
	HerdrCommand = herdrCommand
)

// PaneGap and ListPaneWidth expose the column geometry for the gap pin.
const PaneGap = paneGap

// Presence hooks (SASASH.4.29): the header's rows, the frame cap, the
// wordmark threshold, and the faces rendered by name (the global stays
// untouched so parallel tests never race on it).
const (
	MaxFrameWidth = maxFrameWidth
	MinLogoHeight = minLogoHeight
)

func HeaderHeight(m Model) int  { return m.headerHeight() }
func ContentHeight(m Model) int { return m.contentHeight() }
func ListHeight(m Model) int    { return m.list.Height() }
func LogoFaceNames() []string {
	var out []string
	for _, f := range logoFaces {
		out = append(out, f.name)
	}
	return out
}
func RenderLogo(name string, width, height int) []string {
	for _, f := range logoFaces {
		if f.name == name && logoFits(f, width, height) {
			return renderLogo(NewStyles(Themes[0].Palette), f, width)
		}
	}
	return nil
}
func DefaultLogoFace() string { return currentLogo().name }

// LogoRows is a face's vendored table, unstyled and uncentered.
func LogoRows(name string) []string {
	for _, f := range logoFaces {
		if f.name == name {
			return append([]string(nil), f.rows...)
		}
	}
	return nil
}

func ListPaneWidth(m Model) int { return lipgloss.Width(m.list.View()) }

// ExpandSpec exposes the shorthand expansion for its table test.
var ExpandSpec = expandSpec

// Spawn (SASASH.4.10): the argv matrix and the plan preview, for exact-
// string tests; HerdrPaneID for the reply parser.
var (
	HerdrCreateArgv = herdrCreateArgv
	WindowArgv      = windowArgv
	HerdrPaneID     = herdrPaneID
)

// SpawnTargetOf reports the live spawn target.
func SpawnTargetOf(m Model) string { return m.spawnTarget() }

// HerdrPlan exposes H's spawn plan for exact-argv tests: the steps and
// where they open; nil steps when H has nothing to open beside the TUI.
func HerdrPlan(cfg SpawnConfig, alias string) (steps [][]string, where string) {
	s := spawnState{mux: cfg.Mux, herdrPane: cfg.HerdrPane, herdrEnv: cfg.HerdrEnv, emulator: cfg.Emulator, window: cfg.WindowTemplate, target: cfg.Target}
	p := s.herdrPlanFor(alias)
	if p == nil {
		return nil, ""
	}
	return p.steps, p.where
}

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

// WithRemoveForwardSet swaps the set-delete hook so tests can observe it.
// WithPreviewShown reveals the command-preview footer (the `$` toggle) so a
// test can assert its content; it is off by default (SASASH.4.26).
func WithPreviewShown(m Model) Model {
	m.showPreview = true
	return m
}

// WithAmbientShown reveals the other-ssh_config-entries tier (the `.` toggle)
// so a test can select an ambient host; hidden by default (SASASH.4.26).
func WithAmbientShown(m Model) Model {
	m.fold.showAmbient = true
	m.setRows(m.listRows()) // rebuild the list so the ambient tier appears now
	return m
}

func WithRemoveForwardSet(m Model, f func(alias, name string) error) Model {
	m.removeForwardSet = f
	return m
}

// WithSaveForwardSet swaps the tunnel-declaration hook so tests can
// observe it.
func WithSaveForwardSet(m Model, f func(alias, name string, specs []string) error) Model {
	m.saveForwardSet = f
	return m
}

// WithHome sets the directory the settings panel bootstraps.
func WithHome(m Model, home string) Model {
	m.home = home
	m.setupEnv = setup.Env{Home: home, Config: filepath.Join(home, ".config", "sasash", "config.toml")}
	return m
}

// WithSetupEnv gives the settings panel's setup entry its environment.
func WithSetupEnv(m Model, env setup.Env) Model {
	m.setupEnv = env
	return m
}

// WithShowHidden seeds config.toml's show_hidden and the H toggle's
// write-back hook.
func WithShowHidden(m Model, on bool, save func(bool) error) Model {
	m.showHidden, m.saveShowHidden = on, save
	return m
}

// LocalPickerShowsHidden reports whether C's local browser lists dotfiles.
func LocalPickerShowsHidden(m Model) bool { return m.showHidden }

// Prompting and PromptingSave expose which prompt, if any, owns the keys.
func Prompting(m Model) bool     { return m.mode != promptNone }
func PromptingSave(m Model) bool { return m.mode == promptAlias }

// WithRenameHost swaps the R hook (4.16); PromptValue is the prompt's
// text, so a test can see the prefill.
func WithRenameHost(m Model, f func(old, new string) (pathswitch.RenameResult, error)) Model {
	m.renameHost = f
	return m
}
func PromptValue(m Model) string { return m.prompt.Value() }

// MarkSwitching flags a path switch in flight on alias, as p does;
// MarkInflight a tunnel action or poll, as the engine calls do; Inflight
// reads that gate.
func MarkSwitching(m Model, alias string) Model {
	m.switching[alias] = true
	return m
}
func MarkInflight(m Model, alias string) Model {
	m.tinflight[alias] = true
	return m
}
func Inflight(m Model, alias string) bool { return m.tinflight[alias] }

// TunnelStatusKnown reports a tstatus entry for alias.
func TunnelStatusKnown(m Model, alias string) bool { _, ok := m.tstatus[alias]; return ok }

// TmuxArgv and HerdrRunArgv are enter's builders as the argv tests knew
// them: the ssh command in the created window/pane (H passes its own).
func TmuxArgv(target, alias string) []string     { return tmuxArgv(target, alias, sshCommand(alias)) }
func HerdrRunArgv(paneID, alias string) []string { return herdrRunArgv(paneID, sshCommand(alias)) }

// The copy flow (SASASH.4.33): TransferActive reports C's flow open;
// TransferStep names its step; TransferSource is the picked source;
// TransferRemotePath is the remote browser's directory; TransferPreview
// is the bottom line's argv; ScpExec is the process the confirm builds;
// ScpExit replays the terminal coming back from scp.
func TransferActive(m Model) bool { return m.xfer.active() }
func TransferStep(m Model) string {
	return [...]string{"off", "direction", "local", "remote"}[m.xfer.step]
}
func TransferSource(m Model) (path string, isDir, picked bool) {
	if len(m.xfer.srcs) == 0 {
		return "", false, m.xfer.srcPicked
	}
	return m.xfer.srcs[0].path, m.xfer.srcs[0].isDir, m.xfer.srcPicked
}
func TransferRemotePath(m Model) string { return m.xfer.br.path }
func TransferLocalDir(m Model) string   { return m.xfer.br.path }
func TransferPreview(m Model) []string  { return m.transferPreview() }

// TransferHighlighted is the browser row under the cursor (4.40).
func TransferHighlighted(m Model) string { return m.xfer.br.highlighted().Name }

// TransferMarks lists the basket's paths in mark order (4.37).
func TransferMarks(m Model) []string {
	var out []string
	for _, mk := range m.xfer.marks {
		out = append(out, mk.path)
	}
	return out
}

// TransferSources lists the picked sources in order.
func TransferSources(m Model) []string {
	var out []string
	for _, mk := range m.xfer.srcs {
		out = append(out, mk.path)
	}
	return out
}
func ScpExec(spec transfer.Spec) *exec.Cmd {
	cmd, _ := scpExec(spec)
	return cmd
}
func ScpExit(spec transfer.Spec, err error, stderr string) tea.Msg {
	return scpExitMsg{spec: spec, err: err, stderr: stderr}
}
