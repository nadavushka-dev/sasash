package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/setup"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
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

	root := filepath.Join(home, ".ssh", "config")
	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil {
		fmt.Fprintln(stderr, "sasash:", err)
		return 1
	}
	// Warnings land in the scrollback: visible after the alt screen exits.
	for _, w := range warnings {
		fmt.Fprintln(stderr, "sasash:", w)
	}
	confPath := filepath.Join(home, ".ssh", "config.d", "sasash.conf")
	editor, editorHint := resolveEditor(os.Getenv("EDITOR"))
	// An edit ssh cannot see (setup has not wired sasash.conf in) must
	// come back with an explanation, not a silently unchanged list.
	editorWarn := ""
	if st, err := setup.Check(home); err == nil && !(st.IncludePresent && st.ConfPresent) {
		editorWarn = "sasash.conf is not wired into ssh yet — run `sasash setup`"
	}
	m := ui.New(hosts, probe.Prober{}, run.OS{}, ui.Config{
		Theme:      theme,
		SaveTheme:  saveThemeTo(statePath),
		SaveHost:   saveHostTo(confPath),
		Editor:     editor,
		EditorHint: editorHint,
		EditorWarn: editorWarn,
		ConfPath:   confPath,
		LoadHosts: func() ([]hostmodel.Host, error) {
			// Reload warnings are dropped: they already printed at startup
			// and stderr is invisible under the alt screen.
			hosts, _, err := hostmodel.Load(root, home)
			return hosts, err
		},
	})
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
