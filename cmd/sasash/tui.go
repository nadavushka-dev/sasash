package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/action"
	"github.com/nadavushka-dev/sasash/internal/controlmaster"
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
	cfg := tuiConfig(home, statePath, theme, os.Getenv("EDITOR"))
	cfg.Spawn = spawnConfig(home, os.Getenv)
	m := ui.New(hosts, probe.Prober{}, run.OS{}, cfg)
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
	if v := readState(statePath); v != "" {
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
	// One state dir for the engine, the stats store and the popup that
	// names a cache file in it: they must never drift apart.
	sd := stateDir(home)
	store := stats.Store{Dir: sd}
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
		Tunnels:       tunnel.Engine{StateDir: sd, Stats: &store},
		StateDir:      sd,
		Home:          home,
		RecordHandoff: store.RecordHandoff,
		// p: the switcher on sasash.conf, the block read behind the rows
		// it offers, and the switch stamp.
		SetPath:      func(alias, name string) (pathswitch.Result, error) { return pathswitch.Set(confPath, alias, name) },
		PathBlock:    pathBlockIn(confPath),
		RecordSwitch: store.RecordSwitch,
		SaveForwardSet: func(alias, name string, specs []string) error {
			return fwdset.Add(confPath, alias, name, specs)
		},
		SaveForwardEdit: func(alias, set, old, new string) error {
			return fwdset.Update(confPath, alias, set, old, new)
		},
		// 4.18: an older managed entry without the ControlMaster trio is
		// offered the missing lines from the No-ControlPath failure.
		InspectControl: inspectControl(confPath),
		EnableControl:  enableControl(confPath),
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
// sasash.conf (SPEC §2: the one file sasash writes) — the managed-entry
// template, ControlMaster trio included (4.18: without it the tunnel
// engine's -O commands have no ControlPath and every saved host showed
// its master down). The existing content rides through the lossless
// parser untouched — appending cannot reflow it — and the write is
// atomic. A missing sasash.conf means setup has not run; the error says
// so instead of creating a file setup owns.
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
		b.WriteString(managedBlock(e.Alias, e.Host, e.User, e.Port))
		nf := sshconf.Parse(confPath, []byte(b.String()))
		nf.Perm = f.Perm
		return nf.WriteAtomic(confPath)
	}
}

// managedBlock renders a new managed entry as sasash writes it: the Host
// line, HostName, User and Port when given, then the ControlMaster trio
// from controlmaster.Trio — the one owner of the literal (SPEC §2).
func managedBlock(alias, hostName, user, port string) string {
	var b strings.Builder
	b.WriteString("Host " + alias + "\n")
	b.WriteString("  HostName " + hostName + "\n")
	if user != "" {
		b.WriteString("  User " + user + "\n")
	}
	if port != "" {
		b.WriteString("  Port " + port + "\n")
	}
	for _, d := range controlmaster.Trio {
		b.WriteString("  " + d.Keyword + " " + d.Value + "\n")
	}
	return b.String()
}

// inspectControl is the TUI's read of a managed host's trio (4.18): the
// lines the block lacks, as controlmaster.Enable would write them, and
// the trio directives it sets to another value, as written — the helper
// keeps a hand-chosen value and reports only what is missing, while the
// popup has to say when that value (ControlPath none) defeats the engine.
// Read-only; the block is the one the helper would write (first pattern).
func inspectControl(confPath string) func(alias string) (ui.ControlStatus, error) {
	return func(alias string) (ui.ControlStatus, error) {
		var st ui.ControlStatus
		f, err := sshconf.ParseFile(confPath)
		if err != nil {
			return st, err
		}
		h, ok := f.Host(alias)
		if !ok || len(h.Patterns) == 0 {
			return st, fmt.Errorf("host %q: %w", alias, ui.ErrNotManaged)
		}
		if v := pinnedVariant(f, alias); v != nil {
			return st, v
		}
		for _, d := range controlmaster.Trio {
			v, ok := h.Get(d.Keyword)
			switch {
			case !ok:
				st.Missing = append(st.Missing, d.Keyword+" "+d.Value)
			case !strings.EqualFold(v, d.Value):
				st.Other = append(st.Other, strings.TrimSpace(d.Keyword+" "+v))
			}
		}
		return st, nil
	}
}

// pinnedVariant reports whether alias is a block the path generator
// wrote — pathswitch's own question (Generated: the generated shape,
// anchored in its Header run), so the refusal and the switcher agree on
// ownership whatever a hand edit did to the parent's path names or
// addresses. A user's dotted host outside a Header run is not one.
func pinnedVariant(f *sshconf.File, alias string) *ui.VariantError {
	parent, ok := pathswitch.Generated(f, alias)
	if !ok {
		return nil
	}
	return &ui.VariantError{Parent: parent, Path: strings.TrimPrefix(alias, parent+".")}
}

// enableControl writes the missing trio lines through the one writer,
// reporting them as the popup lists them.
func enableControl(confPath string) func(alias string) ([]string, error) {
	return func(alias string) ([]string, error) {
		added, err := controlmaster.Enable(confPath, alias)
		if err != nil || len(added) == 0 {
			return nil, err
		}
		lines := make([]string, 0, len(added))
		for _, d := range added {
			lines = append(lines, d.Keyword+" "+d.Value)
		}
		return lines, nil
	}
}

// spawnConfig wires enter's spawn (SASASH.4.10): the multiplexer from
// the environment, the OS-window template from ~/.config/sasash/
// spawn-window (one line, actions.toml placeholders; the macOS default
// opens Terminal), and the persisted target beside the theme state.
func spawnConfig(home string, getenv func(string) string) ui.SpawnConfig {
	mux, pane := ui.DetectMux(getenv)
	statePath := spawnStatePath(home)
	target := readState(statePath)
	if _, ok := ui.SpawnTargetIndex(target); !ok {
		target = ui.DefaultSpawnTarget // a stale file never keeps enter from working
	}
	// No file: this OS's default. A file, even empty: exactly what it
	// says — an empty one is how a macOS user turns the Terminal default
	// off and gets the handoff instead.
	windowPath := spawnWindowPath(home, getenv)
	tmpl, present := readStateFound(windowPath)
	if !present {
		tmpl = defaultWindowTemplate(runtime.GOOS, getenv)
	}
	return ui.SpawnConfig{
		Mux:            mux,
		HerdrPane:      pane,
		WindowTemplate: tmpl,
		WindowPath:     windowPath,
		Target:         target,
		SaveTarget:     saveStateTo(statePath),
	}
}

// defaultWindowTemplate is the OS-window spawn outside any multiplexer,
// per platform (SASASH.6): under WSL (a Linux build with
// WSL_DISTRO_NAME set and cmd.exe on PATH) Windows Terminal opens a tab
// running ssh in this distro; on Linux the x-terminal-emulator
// alternative runs it; on macOS Terminal does. Anything else has no
// default, so enter hands off until ~/.config/sasash/spawn-window says
// otherwise. {host} lands in the command unescaped — an alias is a
// plain ssh_config word, never quoted (aliasProblem enforces that for
// the ones sasash writes), the same trust actions.toml extends.
func defaultWindowTemplate(goos string, getenv func(string) string) string {
	switch goos {
	case "darwin":
		return `osascript -e 'tell application "Terminal" to do script "ssh {host}"'`
	case "linux":
		// WSL is a Linux build; the env alone does not prove the Windows
		// side is reachable (interop can be off), so cmd.exe must be on
		// PATH — probed rather than wt.exe, an app execution alias that
		// does not always stat from WSL. Through cmd.exe because the
		// interop cannot exec that alias directly on every build
		// (microsoft/WSL #4969); the distro is quoted, as its name may
		// hold spaces. Without cmd.exe the Linux probe below still runs.
		if distro := getenv("WSL_DISTRO_NAME"); distro != "" && onPath(getenv("PATH"), "cmd.exe") {
			return "cmd.exe /c wt.exe new-tab wsl.exe -d '" + distro + "' -- ssh {host}"
		}
		// The Debian alternative first, then gnome-terminal by name;
		// probed on PATH once here, so the default is a terminal that
		// exists (other desktops set ~/.config/sasash/spawn-window).
		switch {
		case onPath(getenv("PATH"), "x-terminal-emulator"):
			return "x-terminal-emulator -e ssh {host}"
		case onPath(getenv("PATH"), "gnome-terminal"):
			return "gnome-terminal -- ssh {host}"
		}
	}
	return ""
}

// onPath reports whether an executable of that name sits in one of the
// PATH directories — a stat walk, so cmd/sasash never imports os/exec.
func onPath(path, name string) bool {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// spawnStatePath persists the spawn target beside the theme.
func spawnStatePath(home string) string {
	return filepath.Join(filepath.Dir(themeStatePath(home)), "spawn-target")
}

// spawnWindowPath is the user's OS-window template (config, not state).
func spawnWindowPath(home string, getenv func(string) string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "sasash", "spawn-window")
	}
	return filepath.Join(home, ".config", "sasash", "spawn-window")
}

// themeStatePath is where the `t` key persists the choice — state, not
// config (SPEC §4 puts sasash's cache-tier files under ~/.local/state).
func themeStatePath(home string) string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "sasash", "theme")
	}
	return filepath.Join(home, ".local", "state", "sasash", "theme")
}

// readState returns a one-line state file's content, or "" when there is
// none (the theme name, the spawn target, the window template).
func readState(path string) string {
	v, _ := readStateFound(path)
	return v
}

// readStateFound also says whether the file exists, for the one reader
// that treats an empty file differently from a missing one.
func readStateFound(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// saveThemeTo gives the model its write-through persistence hook.
func saveThemeTo(path string) func(string) error { return saveStateTo(path) }

// saveStateTo writes one line to a state file, creating its directory.
func saveStateTo(path string) func(string) error {
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
