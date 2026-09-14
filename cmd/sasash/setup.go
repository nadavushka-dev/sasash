// The setup subcommand: one-time bootstrap per SPEC.md §2. The work lives in
// internal/setup; this file owns the confirmation prompt.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/config"
	"github.com/nadavushka-dev/sasash/internal/setup"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func init() {
	register("setup", "one-time bootstrap: Include line, the connections file (sasash.conf), ControlMaster socket dir (~/.ssh/cm), config.toml", func(args []string, stdout, stderr io.Writer) int {
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
// puts it, and the three settings files that predate it (SASASH.4.36),
// each read here — the cmd layer knows their formats — for setup to
// move in and remove.
func setupEnv(home string, getenv func(string) string) setup.Env {
	return setup.Env{Home: home, Config: config.Path(home, getenv), Legacy: legacyFiles(home, getenv)}
}

// legacyFiles lists the pre-config settings files with the value each
// carries: the theme and the spawn target only when they name a shipped
// choice (a stale file is removed, never carried), the window template
// verbatim — an empty file meant "no window command" and still does.
func legacyFiles(home string, getenv func(string) string) []setup.Legacy {
	var theme, target any
	if v := readState(themeStatePath(home)); v != "" {
		if _, ok := ui.ThemeIndex(v); ok {
			theme = v
		}
	}
	if v := readState(spawnStatePath(home)); v != "" {
		if _, ok := ui.SpawnTargetIndex(v); ok {
			target = v
		}
	}
	var window any
	if v, present := readStateFound(spawnWindowPath(home, getenv)); present {
		window = v
	}
	return []setup.Legacy{
		{Path: themeStatePath(home), Key: "theme", Value: theme},
		{Path: spawnStatePath(home), Key: "spawn_target", Value: target},
		{Path: spawnWindowPath(home, getenv), Key: "spawn_window", Value: window},
	}
}

// legacyLine says what setup does with one old settings file.
func legacyLine(home string, l setup.Legacy) string {
	if l.Value == nil {
		return fmt.Sprintf("  remove %s (it names nothing sasash can keep)", tilde(home, l.Path))
	}
	return fmt.Sprintf("  move %s into config.toml as %s = %q", tilde(home, l.Path), l.Key, fmt.Sprint(l.Value))
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
		fmt.Fprintln(stdout, "  create ~/.ssh/config.d/sasash.conf, the connections file")
	}
	if !st.CMDirPresent {
		fmt.Fprintln(stdout, "  create ~/.ssh/cm/, where ssh keeps its ControlMaster sockets")
	}
	if !st.ConfigPresent {
		fmt.Fprintf(stdout, "  create %s, every option commented out with its default\n", tilde(env.Home, env.Config))
	}
	for _, l := range env.Legacy {
		if slices.Contains(st.Legacy, l.Path) {
			fmt.Fprintln(stdout, legacyLine(env.Home, l))
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
