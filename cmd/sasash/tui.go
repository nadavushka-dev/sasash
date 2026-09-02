package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/action"
	"github.com/nadavushka-dev/sasash/internal/fwdset"
	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/setup"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
	"github.com/nadavushka-dev/sasash/internal/stats"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func init() {
	register("tui", "interactive host browser (the default)", cmdTUI)
}

func cmdTUI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	themeFlag := fs.String("theme", "", "color theme: "+themeNames())
	if err := fs.Parse(args); err != nil {
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "sasash:", err)
		return 1
	}
	// Flag validation precedes the terminal guard: a typoed -theme should
	// report usage whether or not stdout is a tty.
	statePath := themeStatePath(home)
	theme, warn, fatal := resolveTheme(*themeFlag, os.Getenv("SASASH_THEME"), statePath)
	if warn != "" {
		fmt.Fprintln(stderr, "sasash:", warn)
	}
	if fatal {
		return 2
	}
	out, ok := stdout.(*os.File)
	if !ok || !isTerminal(out) {
		fmt.Fprintln(stderr, "sasash: the TUI needs a terminal; try `sasash help` for scriptable subcommands")
		return 1
	}

	hosts, warnings, err := hostmodel.Load(sshConfigPath(home), home)
	if err != nil {
		fmt.Fprintln(stderr, "sasash:", err)
		return 1
	}
	// Warnings land in the scrollback: visible after the alt screen exits.
	for _, w := range warnings {
		fmt.Fprintln(stderr, "sasash:", w)
	}
	m := ui.New(hosts, probe.Prober{}, run.OS{}, tuiConfig(home, statePath, theme, os.Getenv("EDITOR")))
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(out))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, "sasash:", err)
		return 1
	}
	return 0
}

// resolveTheme picks the theme name: flag > SASASH_THEME > persisted state
// > default. A flag naming an unknown theme is fatal (typos should fail
// loudly); a bad env or state value degrades to the next source with a
// warning, so a stale file can never keep the TUI from starting.
func resolveTheme(flagVal, envVal, statePath string) (name, warn string, fatal bool) {
	if flagVal != "" {
		if _, ok := ui.ThemeIndex(flagVal); !ok {
			return "", fmt.Sprintf("unknown theme %q (themes: %s)", flagVal, themeNames()), true
		}
		return flagVal, "", false
	}
	if envVal != "" {
		if _, ok := ui.ThemeIndex(envVal); ok {
			return envVal, "", false
		}
		warn = fmt.Sprintf("SASASH_THEME=%q is not a theme (themes: %s); using the default", envVal, themeNames())
	}
	if v := readTheme(statePath); v != "" {
		if _, ok := ui.ThemeIndex(v); ok {
			return v, warn, false
		}
	}
	return "", warn, false
}

// tuiConfig wires the model's hooks to the user's home: every path and
// every `e` setting is decided here, in one testable place. sasash.conf
// is the ONE file sasash writes (SPEC §2) — never ~/.ssh/config.
func tuiConfig(home, statePath, theme, editorEnv string) ui.Config {
	root := sshConfigPath(home)
	confPath := filepath.Join(home, ".ssh", "config.d", "sasash.conf")
	store := stats.Store{Dir: stateDir(home)}
	editor, editorHint := resolveEditor(editorEnv)
	// An edit ssh cannot see (the Include is not in ~/.ssh/config) must
	// come back with an explanation, not a silently unchanged list. A
	// missing sasash.conf alone is fine: ssh ignores an Include of a
	// missing file, and the editor creates it.
	editorWarn := ""
	if st, err := setup.Check(home); err == nil && !st.IncludePresent {
		editorWarn = "sasash.conf is not wired into ssh yet — o → create config file, or `sasash setup`"
	}
	cfg := ui.Config{
		Theme:      theme,
		SaveTheme:  saveThemeTo(statePath),
		SaveHost:   saveHostTo(confPath),
		Editor:     editor,
		EditorHint: editorHint,
		EditorWarn: editorWarn,
		ConfPath:   confPath,
		LoadHosts: func() ([]hostmodel.Host, []error, error) {
			return hostmodel.Load(root, home)
		},
		// The same state dir as `up`/`down`, or the cache silently splits;
		// the stats store lives beside it and stamps handoffs.
		Tunnels:       tunnel.Engine{StateDir: stateDir(home), Stats: &store},
		Home:          home,
		RecordHandoff: store.RecordHandoff,
		// p: the switcher on sasash.conf, the HostName read behind its
		// preview, and the switch stamp.
		SwitchPath:   func(alias string) (pathswitch.Result, error) { return pathswitch.Cycle(confPath, alias) },
		PathBlock:    pathBlockIn(confPath),
		RecordSwitch: store.RecordSwitch,
		SaveForwardSet: func(alias, name string, specs []string) error {
			return fwdset.Add(confPath, alias, name, specs)
		},
		SaveForwardEdit: func(alias, set, old, new string) error {
			return fwdset.Update(confPath, alias, set, old, new)
		},
	}
	cfg.Actions, cfg.ActionsWarn = loadActionNames(home)
	return cfg
}

// pathBlockIn reads a host's sasash.conf block for the p preview: its
// paths (hostmodel.Build over that block alone — the switcher's own
// inputs, not the include-following model) and its HostName. The alias
// must have a Host block there, else not managed; a missing or
// unreadable file is not managed either.
func pathBlockIn(confPath string) func(alias string) ([]probe.Path, string, bool) {
	return func(alias string) ([]probe.Path, string, bool) {
		f, err := sshconf.ParseFile(confPath)
		if err != nil {
			return nil, "", false
		}
		h, ok := f.Host(alias)
		if !ok {
			return nil, "", false
		}
		model, _ := hostmodel.Build(h) // meta warnings never block a switch either
		v, _ := h.Get("HostName")
		return model.Paths, v, true
	}
}

// loadActionNames lists actions.toml entries for the help line; a load
// problem becomes the startup notice, since stderr is invisible under
// the alt screen. A missing file is neither.
func loadActionNames(home string) (names []string, warn string) {
	acts, err := action.Load(home)
	if err != nil {
		return nil, err.Error()
	}
	for _, a := range acts {
		names = append(names, a.Name)
	}
	return names, ""
}

// resolveEditor turns $EDITOR into the argv prefix `e` launches.
// Fields-splitting keeps EDITOR="code -w" working; unset falls back to vi
// with a hint the TUI shows once the editor returns.
func resolveEditor(env string) (argv []string, hint string) {
	if fields := strings.Fields(env); len(fields) > 0 {
		return fields, ""
	}
	return []string{"vi"}, "opened with vi — set $EDITOR to change"
}

// saveHostTo persists an ad-hoc connection as a Host block appended to
// sasash.conf (SPEC §2: the one file sasash writes). The existing content
// rides through the lossless parser untouched — appending cannot reflow
// it — and the write is atomic. A missing sasash.conf means setup has not
// run; the error says so instead of creating a file setup owns.
func saveHostTo(confPath string) func(ui.AdhocEntry) error {
	return func(e ui.AdhocEntry) error {
		f, err := sshconf.ParseFile(confPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("no %s — run `sasash setup` first", filepath.Base(confPath))
			}
			return err
		}
		var b strings.Builder
		text := f.String()
		b.WriteString(text)
		if text != "" && !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
		if len(e.Forwards) > 0 {
			b.WriteString("# @sasash forwards adhoc " + strings.Join(e.Forwards, " ") + "\n")
		}
		b.WriteString("Host " + e.Alias + "\n")
		b.WriteString("  HostName " + e.Host + "\n")
		if e.User != "" {
			b.WriteString("  User " + e.User + "\n")
		}
		if e.Port != "" {
			b.WriteString("  Port " + e.Port + "\n")
		}
		nf := sshconf.Parse(confPath, []byte(b.String()))
		nf.Perm = f.Perm
		return nf.WriteAtomic(confPath)
	}
}

// themeStatePath is where the `t` key persists the choice — state, not
// config (SPEC §4 puts sasash's cache-tier files under ~/.local/state).
func themeStatePath(home string) string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "sasash", "theme")
	}
	return filepath.Join(home, ".local", "state", "sasash", "theme")
}

// readTheme returns the persisted theme name, or "" when there is none.
func readTheme(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// saveThemeTo gives the model its write-through persistence hook.
func saveThemeTo(path string) func(string) error {
	return func(name string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(name+"\n"), 0o644)
	}
}

func themeNames() string {
	names := make([]string, len(ui.Themes))
	for i, t := range ui.Themes {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
