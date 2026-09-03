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
// Detection order is ratified: herdr, tmux, an OS-window template, then
// the classic handoff. Every spawn goes through the run seam.

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
	// herdr split is made from ("" splits the current pane).
	Mux       Mux
	HerdrPane string
	// WindowTemplate is the OS-window command outside any multiplexer,
	// with actions.toml placeholders ({host} is the alias); "" means
	// nothing to spawn into, so enter hands off with a notice.
	WindowTemplate string
	// Target is the persisted spawn target (one of SpawnTargets; unknown
	// or "" is DefaultSpawnTarget); SaveTarget persists a pick from the
	// settings picker, nil disables persistence.
	Target     string
	SaveTarget func(string) error
}

// DetectMux reads the ratified detection order from the environment:
// herdr wins over tmux (tmux inside a herdr pane still spawns herdr
// tabs), and both need only be set, whatever the value.
func DetectMux(getenv func(string) string) (mux Mux, herdrPane string) {
	switch {
	case getenv("HERDR_ENV") != "":
		return MuxHerdr, getenv("HERDR_PANE_ID")
	case getenv("TMUX") != "":
		return MuxTmux, ""
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
	window     string
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
	err   error      // a template that cannot expand; steps is nil then
}

const panePlaceholder = "<pane>"

// muxWord names the multiplexer-side thing a target becomes.
func muxWord(mux Mux, target string) string {
	switch mux {
	case MuxHerdr:
		return "herdr " + target
	case MuxTmux:
		return "tmux " + tmuxWord(target)
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

// herdrRunArgv is step two: ssh into the created pane.
func herdrRunArgv(paneID, alias string) []string {
	return append([]string{"herdr", "pane", "run", paneID}, sshCommand(alias)...)
}

// tmuxArgv is the one-step tmux spawn per target. -d keeps the current
// pane/window/client where it is, as --no-focus does for herdr; tmux 3
// takes the command as argv, so no shell sits between sasash and ssh.
func tmuxArgv(target, alias string) []string {
	var head []string
	switch target {
	case "pane":
		head = []string{"tmux", "split-window", "-h", "-d"}
	case "workspace":
		head = []string{"tmux", "new-session", "-d", "-s", alias}
	default:
		head = []string{"tmux", "new-window", "-d", "-n", alias}
	}
	return append(head, sshCommand(alias)...)
}

// windowArgv expands the OS-window template with the alias as {host}
// (actions.toml discipline: tokenize, then substitute per token).
func windowArgv(template, alias string) ([]string, error) {
	inv, err := action.Action{Name: "spawn-window", Cmd: template}.Expand(action.Subst{Host: alias})
	if err != nil {
		return nil, err
	}
	return inv.Argv, nil
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
		return &spawnPlan{where: muxWord(s.mux, target), steps: [][]string{
			herdrCreateArgv(target, alias, s.herdrPane),
			herdrRunArgv(panePlaceholder, alias),
		}}
	case MuxTmux:
		return &spawnPlan{where: muxWord(s.mux, target), steps: [][]string{tmuxArgv(target, alias)}}
	}
	if s.window == "" {
		return nil
	}
	argv, err := windowArgv(s.window, alias)
	if err != nil {
		return &spawnPlan{where: "window", err: err}
	}
	return &spawnPlan{where: "window", steps: [][]string{argv}}
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
	err   error
}

// spawnCmd runs the plan's steps in order through the run seam, feeding
// the herdr create reply's pane id into `pane run`.
func spawnCmd(r run.Runner, alias string, p *spawnPlan) tea.Cmd {
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
				return spawnDoneMsg{alias: alias, where: p.where, err: fmt.Errorf("%s: %w", argv[0], err)}
			}
			if !res.OK() {
				return spawnDoneMsg{alias: alias, where: p.where, err: fmt.Errorf("%s failed: %s", strings.Join(argv[:min(3, len(argv))], " "), oneLineText(string(res.Stderr)))}
			}
			if argv[0] == "herdr" && paneID == "" {
				id, err := herdrPaneID(res.Stdout)
				if err != nil {
					return spawnDoneMsg{alias: alias, where: p.where, err: err}
				}
				paneID = id
			}
		}
		return spawnDoneMsg{alias: alias, where: p.where}
	}
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
