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

	hosts, warnings, err := hostmodel.Load(filepath.Join(home, ".ssh", "config"), home)
	if err != nil {
		fmt.Fprintln(stderr, "sasash:", err)
		return 1
	}
	// Warnings land in the scrollback: visible after the alt screen exits.
	for _, w := range warnings {
		fmt.Fprintln(stderr, "sasash:", w)
	}
	m := ui.New(hosts, probe.Prober{}, run.OS{}, ui.Config{Theme: theme, SaveTheme: saveThemeTo(statePath)})
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
