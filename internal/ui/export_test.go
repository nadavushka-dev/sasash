package ui

import (
	"os/exec"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
)

// RefreshTick lets the external test package drive the live-refresh loop
// without waiting out the real tick interval.
func RefreshTick() tea.Msg { return refreshTickMsg{} }

// ProbeChunk exposes the refresh fan-out bound for the regression test.
const ProbeChunk = probeChunk

// SSHExit lets tests replay the terminal returning from an ssh handoff.
func SSHExit(err error) tea.Msg { return sshExitMsg{err: err} }

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

// Prompting and PromptingSave expose which prompt, if any, owns the keys.
func Prompting(m Model) bool     { return m.mode != promptNone }
func PromptingSave(m Model) bool { return m.mode == promptAlias }
