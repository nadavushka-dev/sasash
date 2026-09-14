// The setup subcommand: one-time bootstrap per SPEC.md §2. The work lives in
// internal/setup; this file owns the confirmation prompt.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/config"
	"github.com/nadavushka-dev/sasash/internal/setup"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func init() {
	register("setup", "one-time bootstrap: Include line, sasash.conf, ControlMaster socket dir (~/.ssh/cm), config.toml", func(args []string, stdout, stderr io.Writer) int {
		return runSetup(args, os.Stdin, stdout, stderr)
	})
}

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	yes := flags.Bool("y", false, "answer yes to the confirmation prompt")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "sasash setup: %v\n", err)
		return 1
	}
	return setupHome(setupEnv(home, os.Getenv), *yes, stdin, stdout, stderr)
}

// setupEnv resolves what setup writes under home: config.toml where XDG
// puts it, seeded with the theme the pre-config state file holds when
// that names a shipped theme — a stale file must not seed a warning
// into the new one.
func setupEnv(home string, getenv func(string) string) setup.Env {
	return setup.Env{Home: home, Config: config.Path(home, getenv), Theme: legacyTheme(home)}
}

// legacyTheme is the theme `t` persisted before config.toml existed
// (SASASH.4.36), "" when there is none or it names no shipped theme.
func legacyTheme(home string) string {
	v := readState(themeStatePath(home))
	if _, ok := ui.ThemeIndex(v); !ok {
		return ""
	}
	return v
}

// tilde spells a path under home with ~, for the setup listing.
func tilde(home, path string) string {
	if rest, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rest
	}
	return path
}

// setupHome takes the environment explicitly so tests run against a
// t.TempDir instead of the real ~/.ssh and ~/.config.
func setupHome(env setup.Env, yes bool, stdin io.Reader, stdout, stderr io.Writer) int {
	st, err := setup.Check(env)
	if err != nil {
		fmt.Fprintf(stderr, "sasash setup: %v\n", err)
		return 1
	}
	if st.Done() {
		fmt.Fprintln(stdout, "already set up")
		return 0
	}

	fmt.Fprintln(stdout, "sasash setup will:")
	if !st.IncludePresent {
		fmt.Fprintf(stdout, "  add %q at the top of ~/.ssh/config\n", setup.IncludeLine)
	}
	if !st.ConfPresent {
		fmt.Fprintln(stdout, "  create ~/.ssh/config.d/sasash.conf")
	}
	if !st.CMDirPresent {
		fmt.Fprintln(stdout, "  create ~/.ssh/cm/, where ssh keeps its ControlMaster sockets")
	}
	if !st.ConfigPresent {
		fmt.Fprintf(stdout, "  create %s, every option commented out with its default\n", tilde(env.Home, env.Config))
		if env.Theme != "" {
			fmt.Fprintf(stdout, "    with theme = %q carried over from %s\n", env.Theme, tilde(env.Home, themeStatePath(env.Home)))
		}
	}

	if !yes && !confirm(stdin, stdout) {
		fmt.Fprintln(stdout, "aborted; nothing changed")
		return 1
	}

	if err := setup.Ensure(env); err != nil {
		fmt.Fprintf(stderr, "sasash setup: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "done")
	return 0
}

// confirm asks y/N on stdin. Anything but an explicit yes — including EOF
// from a non-interactive stdin — declines.
func confirm(stdin io.Reader, stdout io.Writer) bool {
	fmt.Fprint(stdout, "proceed? [y/N] ")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
