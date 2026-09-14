package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/action"
	"github.com/nadavushka-dev/sasash/internal/run"
)

// Spawn (SASASH.4.10): enter opens the connection BESIDE the TUI through
// the surrounding multiplexer's own CLI — a previewed command the user
// could type themselves — so the switchboard never leaves the screen.
// Detection order is ratified, innermost first: tmux, herdr, an
// OS-window template, then the classic handoff. Every spawn goes through the run seam.

// Mux is the multiplexer sasash runs inside, as detected from the
// environment.
type Mux int

const (
	MuxNone  Mux = iota // no multiplexer: the OS-window template, else handoff
	MuxHerdr            // HERDR_ENV set
	MuxTmux             // TMUX set
)

// SpawnTargets are the settings choices, in picker order; each maps per
// multiplexer (herdr pane · tab · workspace; tmux split · window · session).
var SpawnTargets = [...]string{"pane", "tab", "workspace"}

// DefaultSpawnTarget is used when nothing is persisted.
const DefaultSpawnTarget = "tab"

// SpawnConfig is the cmd layer's wiring for enter (SASASH.4.10).
type SpawnConfig struct {
	// Mux is what DetectMux found; HerdrPane is HERDR_PANE_ID, the pane a
	// herdr split is made from ("" splits the current pane). HerdrEnv is
	// HERDR_ENV being set at all: herdr's own nesting guard reads it, and
	// a tmux started inside a herdr pane inherits it, so H under that tmux
	// must not run herdr --remote either (SASASH.4.22).
	Mux       Mux
	HerdrPane string
	HerdrEnv  bool
	// Emulator is the outer terminal emulator DetectEmulator found: where
	// H opens its tab (SASASH.4.22), since a tab of the emulator is a
	// fresh top-level process outside herdr.
	Emulator Emulator
	// WindowTemplate is the OS-window command outside any multiplexer,
	// with actions.toml placeholders ({host} is the alias); "" means
	// nothing to spawn into, so enter hands off with a notice.
	// WindowPath is the config.toml it is read from (spawn_window),
	// named when it fails.
	WindowTemplate string
	WindowPath     string
	// Target is the persisted spawn target (one of SpawnTargets; unknown
	// or "" is DefaultSpawnTarget); SaveTarget persists a pick from the
	// settings picker, nil disables persistence.
	Target     string
	SaveTarget func(string) error
}

// DetectMux reads the ratified detection order from the environment,
// innermost first: tmux before herdr, because a tmux started inside a
// herdr pane sets both (and carries the pane's HERDR_* forever, even
// once that pane is gone) while the user is in the tmux; the reverse
// nesting does not exist. Both need only be set, whatever the value.
func DetectMux(getenv func(string) string) (mux Mux, herdrPane string) {
	switch {
	case getenv("TMUX") != "":
		return MuxTmux, ""
	case getenv("HERDR_ENV") != "":
		return MuxHerdr, getenv("HERDR_PANE_ID")
	}
	return MuxNone, ""
}

// SpawnTargetIndex maps a target name to its SpawnTargets index.
func SpawnTargetIndex(name string) (int, bool) {
	for i, t := range SpawnTargets {
		if t == name {
			return i, true
		}
	}
	return 0, false
}

// spawnState is the model's slice of SpawnConfig plus the live target.
type spawnState struct {
	mux        Mux
	herdrPane  string
	herdrEnv   bool
	emulator   Emulator
	window     string
	windowPath string
	target     string
	saveTarget func(string) error
}

// spawnPlan is what enter will run for an alias: the argv steps in order.
// herdr is two steps — create, then `pane run` into the created pane,
// whose id only the create reply knows — so step two carries the
// panePlaceholder until it runs. nil means nothing to spawn into.
type spawnPlan struct {
	where string     // "herdr tab", "tmux window", "window" — for the notice
	steps [][]string // exact argv, in order
	what  []string   // the command the steps open (ssh, or herdr --remote), for the notice
	err   error      // a template that cannot expand; steps is nil then
}

const panePlaceholder = "<pane>"

// where names the multiplexer-side thing a target becomes — or the
// handoff, when there is nothing to spawn into.
func (s spawnState) where(target string) string {
	switch s.mux {
	case MuxHerdr:
		return "herdr " + target
	case MuxTmux:
		return "tmux " + tmuxWord(target)
	}
	if s.window == "" {
		return "handoff"
	}
	return "window"
}

// tmuxWord is the tmux name of a target.
func tmuxWord(target string) string {
	switch target {
	case "pane":
		return "split"
	case "workspace":
		return "session"
	}
	return "window"
}

// herdrCreateArgv is step one of a herdr spawn: the exact create call per
// target. --no-focus keeps the switchboard on screen; --label names the
// tab/workspace after the alias.
func herdrCreateArgv(target, alias, fromPane string) []string {
	switch target {
	case "pane":
		argv := []string{"herdr", "pane", "split"}
		if fromPane != "" {
			argv = append(argv, "--pane", fromPane)
		} else {
			argv = append(argv, "--current")
		}
		return append(argv, "--direction", "right", "--no-focus")
	case "workspace":
		return []string{"herdr", "workspace", "create", "--label", alias, "--no-focus"}
	}
	return []string{"herdr", "tab", "create", "--label", alias, "--no-focus"}
}

// herdrRunArgv is step two: the command — ssh, or H's — into the
// created pane.
func herdrRunArgv(paneID string, command []string) []string {
	return append([]string{"herdr", "pane", "run", paneID}, command...)
}

// tmuxArgv is the one-step tmux spawn per target. -d keeps the current
// pane/window/client where it is, as --no-focus does for herdr; tmux 3
// takes the command as argv, so no shell sits between sasash and ssh.
// The window carries the alias as its name — tmux refuses '.' and ':'
// there, so those become '_' in the NAME only; the ssh argv keeps the
// real alias. A session is named by its window rather than -s: session
// names must be unique, and a second shell to the same host is normal.
func tmuxArgv(target, alias string, command []string) []string {
	var head []string
	switch target {
	case "pane":
		head = []string{"tmux", "split-window", "-h", "-d"}
	case "workspace":
		head = []string{"tmux", "new-session", "-d", "-n", tmuxName(alias)}
	default:
		head = []string{"tmux", "new-window", "-d", "-n", tmuxName(alias)}
	}
	return append(head, command...)
}

// tmuxName is an alias as a tmux window name.
func tmuxName(alias string) string { return tmuxNamer.Replace(alias) }

var tmuxNamer = strings.NewReplacer(".", "_", ":", "_")

// windowArgv expands the OS-window template with the alias as {host}
// (actions.toml discipline: tokenize, then substitute per token).
func windowArgv(template, alias string) ([]string, error) {
	inv, err := action.Action{Name: "spawn-window", Cmd: template}.Expand(action.Subst{Host: alias})
	if err != nil {
		return nil, err
	}
	return inv.Argv, nil
}

// windowHint closes the OS-window failure popup: the command that failed
// is the user's to change, and this is the key and the file — the one
// the cmd layer read (XDG_CONFIG_HOME moves it), named exactly.
func (s spawnState) windowHint() []string {
	return []string{"the window command is spawn_window in " + s.windowPath, "({host} is the alias; \"\" turns it off, unset means the platform default)"}
}

// spawnPlanFor builds enter's plan for an alias, or nil when the fallback
// is the classic handoff.
func (s spawnState) planFor(alias string) *spawnPlan {
	target := s.target
	if _, ok := SpawnTargetIndex(target); !ok {
		target = DefaultSpawnTarget
	}
	switch s.mux {
	case MuxHerdr:
		return &spawnPlan{where: s.where(target), what: sshCommand(alias), steps: [][]string{
			herdrCreateArgv(target, alias, s.herdrPane),
			herdrRunArgv(panePlaceholder, sshCommand(alias)),
		}}
	case MuxTmux:
		return &spawnPlan{where: s.where(target), what: sshCommand(alias), steps: [][]string{tmuxArgv(target, alias, sshCommand(alias))}}
	}
	if s.window == "" {
		return nil
	}
	argv, err := windowArgv(s.window, alias)
	if err != nil {
		return &spawnPlan{where: "window", err: err}
	}
	return &spawnPlan{where: "window", what: sshCommand(alias), steps: [][]string{argv}}
}

// Emulator is the outer terminal emulator sasash runs inside — the
// window around herdr or tmux — as detected from the environment.
type Emulator int

const (
	EmuNone     Emulator = iota // none sasash can drive: H shows the command instead
	EmuWezTerm                  // WEZTERM_PANE set, or TERM_PROGRAM=WezTerm
	EmuKitty                    // KITTY_WINDOW_ID set
	EmuITerm                    // TERM_PROGRAM=iTerm.app
	EmuTerminal                 // TERM_PROGRAM=Apple_Terminal
)

// DetectEmulator reads the outer terminal emulator off the environment.
// The emulators' own variables come first: tmux resets TERM_PROGRAM to
// "tmux", but WEZTERM_PANE / KITTY_WINDOW_ID survive it and herdr alike
// (both pass the environment down).
func DetectEmulator(getenv func(string) string) Emulator {
	switch {
	case getenv("WEZTERM_PANE") != "":
		return EmuWezTerm
	case getenv("KITTY_WINDOW_ID") != "":
		return EmuKitty
	}
	switch getenv("TERM_PROGRAM") {
	case "WezTerm":
		return EmuWezTerm
	case "iTerm.app":
		return EmuITerm
	case "Apple_Terminal":
		return EmuTerminal
	}
	return EmuNone
}

// where names what an emulator opens for H, as the notice and footer
// say it: a tab everywhere but Terminal.app, whose scripting can open a
// window but not a tab.
func (e Emulator) where() string {
	switch e {
	case EmuWezTerm:
		return "wezterm tab"
	case EmuKitty:
		return "kitty tab"
	case EmuITerm:
		return "iTerm2 tab"
	case EmuTerminal:
		return "Terminal window"
	}
	return ""
}

// herdrPaneEnv is what herdr sets in every pane it runs. wezterm's CLI
// forwards its own environment into the pane it spawns, so left in place
// these would follow H's command into the new tab and make herdr's
// nesting guard fire on a process that is not nested at all; the
// scrubbed tab is what cmd-t gives. HERDR_ENV is the guard's variable.
var herdrPaneEnv = []string{"HERDR_ENV", "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_SOCKET_PATH"}

// weztermStaleEnv are wezterm's own per-pane variables, also dropped:
// herdr snapshots its environment for life, so under a GUI restarted
// since they name a socket and a pane that are gone. Without them the
// CLI finds the running GUI through its default socket link and spawns
// into the active window. TMUX and TMUX_PANE go the same way: a tmux
// started inside herdr lands here too, and a cmd-t tab has neither.
var weztermStaleEnv = []string{"WEZTERM_UNIX_SOCKET", "WEZTERM_PANE", "TMUX", "TMUX_PANE"}

// emulatorTabArgv is H's one-step spawn: the command in a new tab of the
// outer emulator, exact per emulator. wezterm and kitty take argv
// directly; the two AppleScript-driven apps get one shell line, passed
// as a script argument (never spliced into the script) and shell-quoted
// per word, since the new tab's shell will parse it. --no-auto-start
// keeps wezterm's CLI from daemonizing a mux server when no GUI answers.
// kitty needs allow_remote_control; a refusal comes back as the failure
// popup, which names this argv.
func emulatorTabArgv(emu Emulator, alias string, command []string) []string {
	switch emu {
	case EmuWezTerm:
		argv := []string{"env"}
		for _, v := range herdrPaneEnv {
			argv = append(argv, "-u", v)
		}
		for _, v := range weztermStaleEnv {
			argv = append(argv, "-u", v)
		}
		argv = append(argv, "wezterm", "cli", "--no-auto-start", "spawn", "--")
		return append(argv, command...)
	case EmuKitty:
		return append([]string{"kitten", "@", "launch", "--type=tab", "--tab-title", alias}, command...)
	case EmuITerm:
		// iTerm2's own scripting example: the created tab becomes current.
		return []string{"osascript",
			"-e", "on run argv",
			"-e", `tell application "iTerm2"`,
			"-e", "tell current window",
			"-e", "create tab with default profile",
			"-e", "tell current session to write text (item 1 of argv)",
			"-e", "end tell",
			"-e", "end tell",
			"-e", "end run",
			shellJoin(command)}
	case EmuTerminal:
		return []string{"osascript",
			"-e", "on run argv",
			"-e", `tell application "Terminal" to do script (item 1 of argv)`,
			"-e", "end run",
			shellJoin(command)}
	}
	return nil
}

// shellJoin renders argv as one POSIX shell line: words made only of
// safe characters stay bare, anything else is single-quoted.
func shellJoin(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && !strings.ContainsFunc(a, func(r rune) bool { return !shellSafe(r) }) {
			words[i] = a
			continue
		}
		words[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(words, " ")
}

func shellSafe(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r)
}

// herdrPlanFor builds H's plan (SASASH.4.22): `herdr --remote <alias>`
// beside the TUI, the TUI staying up — the spawn road, never a handoff.
// The HERDR_ENV gate comes FIRST (part B): set in this process's
// environment, herdr is somewhere above us — the pane itself, or a tmux
// started from one (a tmux server snapshots its env for life) — and
// neither a herdr tab nor a tmux window will do, since both inherit the
// variable herdr's nesting guard reads. So the command goes up and out,
// into a new tab of the OUTER terminal emulator: a fresh top-level
// process, not a child of herdr, where herdr --remote starts clean. Only
// a tmux with no herdr above gets a tmux window. Without any multiplexer,
// the emulator tab again. nil when no emulator sasash can drive is
// around; the caller then shows the command for the user to run — never
// plain ssh, never a herdr tab, never a handoff.
func (s spawnState) herdrPlanFor(alias string) *spawnPlan {
	command := herdrCommand(alias)
	if s.mux == MuxTmux && !s.herdrEnv {
		target := s.target
		if _, ok := SpawnTargetIndex(target); !ok {
			target = DefaultSpawnTarget
		}
		return &spawnPlan{where: s.where(target), what: command, steps: [][]string{tmuxArgv(target, alias, command)}}
	}
	argv := emulatorTabArgv(s.emulator, alias, command)
	if len(argv) == 0 {
		// EmuNone, or a value the argv switch does not know: one list.
		return nil
	}
	return &spawnPlan{where: s.emulator.where(), what: command, steps: [][]string{argv}}
}

// preview is the plan as the bottom line shows it: the steps joined the
// way a shell would take them, the pane id still a placeholder.
func (p *spawnPlan) preview() []string {
	if p == nil || len(p.steps) == 0 {
		return nil
	}
	var out []string
	for i, step := range p.steps {
		if i > 0 {
			out = append(out, ";")
		}
		out = append(out, step...)
	}
	return out
}

// spawnTimeout bounds one spawn step: a wedged multiplexer socket must
// become a notice, not a hang.
const spawnTimeout = 10 * time.Second

// spawnDoneMsg reports a spawn: err is why it failed (a seam failure, a
// non-zero step, or an unreadable herdr reply), nil on success.
type spawnDoneMsg struct {
	alias string
	where string
	what  string   // the command that was opened, for the notice
	argv  []string // the step that failed, for the popup
	err   error
}

// spawnCmd runs the plan's steps in order through the run seam, feeding
// the herdr create reply's pane id into `pane run`.
func spawnCmd(r run.Runner, alias string, p *spawnPlan) tea.Cmd {
	// Only a plan whose later step names the placeholder needs a reply
	// parsed: a user template that happens to start with herdr does not.
	needsPane := false
	for _, step := range p.steps {
		for _, a := range step {
			needsPane = needsPane || a == panePlaceholder
		}
	}
	return func() tea.Msg {
		paneID := ""
		for _, step := range p.steps {
			argv := append([]string(nil), step...)
			for i, a := range argv {
				if a == panePlaceholder {
					argv[i] = paneID
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), spawnTimeout)
			res, err := r.Run(ctx, argv...)
			cancel()
			if err != nil {
				return spawnDoneMsg{alias: alias, where: p.where, argv: argv, err: fmt.Errorf("%s: %w", argv[0], err)}
			}
			if !res.OK() {
				return spawnDoneMsg{alias: alias, where: p.where, argv: argv, err: fmt.Errorf("%s failed: %s", argvHead(argv), failureText(res))}
			}
			if needsPane && paneID == "" {
				id, err := herdrPaneID(res.Stdout)
				if err != nil {
					return spawnDoneMsg{alias: alias, where: p.where, argv: argv, err: err}
				}
				paneID = id
			}
		}
		return spawnDoneMsg{alias: alias, where: p.where, what: strings.Join(p.what, " ")}
	}
}

// argvHead names the tool a failed step ran, for the one-line notice:
// the first three words, past a leading `env -u NAME ...` prefix, which
// is the scrub and not the tool.
func argvHead(argv []string) string {
	i := 0
	if len(argv) > 0 && argv[0] == "env" {
		i = 1
		for i+1 < len(argv) && argv[i] == "-u" {
			i += 2
		}
	}
	rest := argv[i:]
	return strings.Join(rest[:min(3, len(rest))], " ")
}

// failureText is a failed step's reason for the notice: herdr's reply
// envelope gives up its message, anything else its stderr, or stdout
// when that is where the tool spoke.
func failureText(res run.Result) string {
	var r struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	for _, out := range [][]byte{res.Stderr, res.Stdout} {
		if json.Unmarshal(out, &r) == nil && r.Error.Message != "" {
			return r.Error.Message
		}
	}
	text := oneLineText(string(res.Stderr))
	if strings.TrimSpace(text) == "" {
		text = oneLineText(string(res.Stdout))
	}
	if strings.TrimSpace(text) == "" {
		return "exit " + fmt.Sprint(res.Code)
	}
	return text
}

// herdrPaneID reads the created pane's id from a herdr CLI reply: tab and
// workspace creates carry result.root_pane, a split carries result.pane.
func herdrPaneID(reply []byte) (string, error) {
	var r struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(reply, &r); err != nil {
		return "", fmt.Errorf("herdr reply: %w", err)
	}
	switch {
	case r.Result.RootPane.PaneID != "":
		return r.Result.RootPane.PaneID, nil
	case r.Result.Pane.PaneID != "":
		return r.Result.Pane.PaneID, nil
	}
	return "", errors.New("herdr reply names no pane")
}

// spawnTargetSavedMsg reports persisting the settings pick.
type spawnTargetSavedMsg struct{ err error }

// setSpawnTarget applies and persists a picker choice.
func (m Model) setSpawnTarget(idx int) (tea.Model, tea.Cmd) {
	m.spawn.target = SpawnTargets[idx]
	m.notice = "spawn target: " + m.spawn.target
	if m.spawn.saveTarget == nil {
		return m, nil
	}
	save, name := m.spawn.saveTarget, m.spawn.target
	return m, func() tea.Msg { return spawnTargetSavedMsg{err: save(name)} }
}
