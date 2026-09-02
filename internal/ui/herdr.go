package ui

import (
	"os/exec"
	"strings"
)

// herdrCommand is the argv `H` hands the terminal to (SPEC §5/§6): herdr
// reads ssh_config itself, so it needs no actions.toml template.
func herdrCommand(alias string) []string {
	return []string{"herdr", "--remote", alias}
}

// herdrExec is the ONE construction site of the herdr handoff process.
func herdrExec(alias string) *exec.Cmd {
	return execCommand(herdrCommand(alias))
}

// herdrExitMsg arrives when the terminal comes back from herdr. err
// reports only a failure to start; herdr's own exit code is not news.
type herdrExitMsg struct{ err error }

// actionsLine is the full-help line naming the actions.toml entries
// (SPEC §6): they are listed so the file is discoverable, and honest
// about not being runnable from the TUI yet.
func actionsLine(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " actions.toml: " + strings.Join(names, ", ") + " — not runnable from the TUI yet"
}
