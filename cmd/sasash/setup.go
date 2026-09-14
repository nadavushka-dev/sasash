// The setup subcommand: one-time bootstrap per SPEC.md §2. The work lives in
// internal/setup; this file owns the confirmation prompt.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
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
	env, problems := setupEnv(home, os.Getenv)
	for _, p := range problems {
		fmt.Fprintln(stdout, p)
	}
	return setupHome(env, *yes, stdin, stdout, stderr)
}

// setupEnv resolves what setup writes under home: config.toml where XDG
// puts it, and the three settings files that predate it (SASASH.4.36),
// each read here — the cmd layer knows their formats — for setup to
// move in and remove. problems names old files setup will not touch
// (unreadable, or a directory), for the caller to say so.
func setupEnv(home string, getenv func(string) string) (env setup.Env, problems []string) {
	files, problems := legacyFiles(home, getenv)
	return setup.Env{Home: home, Config: config.Path(home, getenv), Legacy: files}, problems
}

// legacyFiles lists the pre-config settings files with the value each
// carries: the theme and the spawn target only when they name a shipped
// choice (a stale file is removed, never carried), the window template
// verbatim — an empty file meant "no window command" and still does. A
// file that cannot be read is left out — and out of setup's hands —
// with a problem line, never mistaken for one with nothing to keep.
func legacyFiles(home string, getenv func(string) string) (files []setup.Legacy, problems []string) {
	add := func(path, key string, keep func(string) any) {
		v, present, err := readLegacy(path)
		if err != nil {
			problems = append(problems, "cannot read "+tilde(home, path)+": "+err.Error()+" — left as is")
			return
		}
		l := setup.Legacy{Path: path, Key: key}
		if present {
			l.Value = keep(v)
		}
		files = append(files, l)
	}
	add(themeStatePath(home, getenv), "theme", func(v string) any {
		if _, ok := ui.ThemeIndex(v); ok {
			return v
		}
		return nil
	})
	add(spawnStatePath(home, getenv), "spawn_target", func(v string) any {
		if _, ok := ui.SpawnTargetIndex(v); ok {
			return v
		}
		return nil
	})
	add(spawnWindowPath(home, getenv), "spawn_window", func(v string) any { return v })
	return files, problems
}

// readLegacy reads one legacy settings file: its trimmed content and
// whether it exists. A directory or an unreadable file is an error, not
// "nothing to keep" — that would have setup delete it.
func readLegacy(path string) (string, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.IsDir() {
		return "", false, errors.New("it is a directory")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(b)), true, nil
}

// moveLine says what setup does with one old settings file — the move
// setup.Check resolved, so the line and the action cannot disagree.
func moveLine(home string, m setup.Move) string {
	if m.Value == nil {
		return fmt.Sprintf("  remove %s (%s)", tilde(home, m.Path), m.Reason)
	}
	return fmt.Sprintf("  move %s into config.toml as %s = %q", tilde(home, m.Path), m.Key, fmt.Sprint(m.Value))
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
	for _, m := range st.Legacy {
		fmt.Fprintln(stdout, moveLine(env.Home, m))
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
