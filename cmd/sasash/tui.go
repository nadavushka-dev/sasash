package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func init() {
	register("tui", "interactive host browser (the default)", cmdTUI)
}

func cmdTUI(_ []string, stdout, stderr io.Writer) int {
	out, ok := stdout.(*os.File)
	if !ok || !isTerminal(out) {
		fmt.Fprintln(stderr, "sasash: the TUI needs a terminal; try `sasash help` for scriptable subcommands")
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "sasash:", err)
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
	p := tea.NewProgram(ui.New(hosts, probe.Prober{}, run.OS{}), tea.WithAltScreen(), tea.WithOutput(out))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, "sasash:", err)
		return 1
	}
	return 0
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
