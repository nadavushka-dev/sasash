// Internal test package, like main_test.go: main cannot be imported.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/action"
	"github.com/nadavushka-dev/sasash/internal/config"
	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestResolveTheme(t *testing.T) {
	t.Parallel()
	inFile := config.Config{Present: true, Theme: "mocha"}
	badFile := config.Config{Present: true, Theme: "solarized"}

	tests := []struct {
		name      string
		flagVal   string
		envVal    string
		uc        config.Config
		want      string
		wantWarn  string // substring; "" for no warning
		wantFatal bool
	}{
		{name: "flag wins over everything", flagVal: "tokyonight", envVal: "nord", uc: inFile, want: "tokyonight"},
		{name: "unknown flag is fatal", flagVal: "solarized", wantWarn: "unknown theme", wantFatal: true},
		{name: "env wins over config", envVal: "tokyonight", uc: inFile, want: "tokyonight"},
		{name: "config alone", uc: inFile, want: "mocha"},
		{name: "bad env warns and falls back to config", envVal: "solarized", uc: inFile, want: "mocha", wantWarn: "SASASH_THEME"},
		{name: "bad config warns and falls back to default", uc: badFile, want: "", wantWarn: "config.toml: theme"},
		{name: "bad env and bad config both named", envVal: "x", uc: badFile, want: "", wantWarn: "SASASH_THEME=\"x\" is not a theme (themes: gruvbox, mocha, tokyonight, nord); config.toml: theme \"solarized\""},
		{name: "a present file with no theme is the default", uc: config.Config{Present: true}, want: ""},
		{name: "nothing set", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warn, fatal := resolveTheme(tt.flagVal, tt.envVal, tt.uc)
			if got != tt.want {
				t.Errorf("resolveTheme() name = %q, want %q", got, tt.want)
			}
			if (warn != "") != (tt.wantWarn != "") || !strings.Contains(warn, tt.wantWarn) {
				t.Errorf("resolveTheme() warn = %q, want %q", warn, tt.wantWarn)
			}
			if fatal != tt.wantFatal {
				t.Errorf("resolveTheme() fatal = %v, want %v", fatal, tt.wantFatal)
			}
		})
	}
}

func TestConfigWarn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		err   error
		warns []string
		want  string
	}{
		{name: "nothing", want: ""},
		{name: "skipped keys joined", warns: []string{"a", "b"}, want: "a; b"},
		{name: "rejected file names defaults", err: errors.New("config.toml:3: x"), warns: []string{"ignored"}, want: "config.toml:3: x — using defaults"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := configWarn(tt.err, tt.warns); got != tt.want {
				t.Errorf("configWarn() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTuiConfig_SaveHooksWriteConfig pins the write-back: t's theme and
// the picker's H land in config.toml as live lines, everything else in
// the file untouched.
func TestTuiConfig_SaveHooksWriteConfig(t *testing.T) {
	t.Parallel()
	home := wiredHome(t, "")
	cfgPath := filepath.Join(home, ".config", "sasash", "config.toml")
	cfg := tuiConfig(home, cfgPath, "", config.Config{}, "", func(string) string { return "" })
	if err := cfg.SaveTheme("nord"); err != nil {
		t.Fatalf("SaveTheme() = %v", err)
	}
	if err := cfg.SaveShowHidden(true); err != nil {
		t.Fatalf("SaveShowHidden() = %v", err)
	}
	got, warns, err := config.Load(cfgPath)
	if err != nil || warns != nil {
		t.Fatalf("Load() = %+v, %v, %v", got, warns, err)
	}
	if got.Theme != "nord" || !got.ShowHidden {
		t.Errorf("Load() = %+v, want theme nord and show_hidden true", got)
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "# editor = \"\"\n") {
		t.Errorf("config.toml lost the template's other lines:\n%s", data)
	}
	if cfg.Setup.Home != home || cfg.Setup.Config != cfgPath || len(cfg.Setup.Legacy) != 3 {
		t.Errorf("tuiConfig().Setup = %+v, want the home, the config path and the three legacy files", cfg.Setup)
	}
	if cfg.SetupWarn != "" {
		t.Errorf("tuiConfig().SetupWarn = %q, want none without old files", cfg.SetupWarn)
	}
}

func TestTuiConfig_SetupWarnNamesOldFiles(t *testing.T) {
	t.Parallel()
	home := wiredHome(t, "")
	getenv := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return filepath.Join(home, "xdg")
		}
		return ""
	}
	old := filepath.Join(home, "xdg", "sasash", "spawn-window")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("kitty ssh {host}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := tuiConfig(home, filepath.Join(home, "xdg", "sasash", "config.toml"), "", config.Config{}, "", getenv)
	if !strings.Contains(cfg.SetupWarn, "old settings files: ~/xdg/sasash/spawn-window") || !strings.Contains(cfg.SetupWarn, "o → setup") {
		t.Errorf("tuiConfig().SetupWarn = %q, want the old file named and the way to move it", cfg.SetupWarn)
	}
	// Named, never read: the TUI's window template is still the platform default.
	if _, err := os.Stat(old); err != nil {
		t.Errorf("tuiConfig() touched the old file: %v", err)
	}
}

func TestThemeStatePath(t *testing.T) {
	// Not parallel: mutates XDG_STATE_HOME via t.Setenv.
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	if got, want := themeStatePath("/home/x"), filepath.Join("/tmp/xdg-state", "sasash", "theme"); got != want {
		t.Errorf("themeStatePath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	if got, want := themeStatePath("/home/x"), filepath.Join("/home/x", ".local", "state", "sasash", "theme"); got != want {
		t.Errorf("themeStatePath() = %q, want %q", got, want)
	}
}

func TestResolveEditor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      string
		env      string
		want     []string
		wantHint bool
	}{
		{name: "plain editor", env: "nvim", want: []string{"nvim"}},
		{name: "editor with args", env: "code -w", want: []string{"code", "-w"}},
		{name: "config beats the environment", cfg: "hx", env: "nvim", want: []string{"hx"}},
		{name: "config with args", cfg: "code -w", want: []string{"code", "-w"}},
		{name: "blank config falls through to the environment", cfg: " ", env: "nvim", want: []string{"nvim"}},
		{name: "unset falls back to vi with a hint", env: "", want: []string{"vi"}, wantHint: true},
		{name: "whitespace only is unset", env: "   ", want: []string{"vi"}, wantHint: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, hint := resolveEditor(tt.cfg, tt.env)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveEditor(%q) = %v, want %v", tt.env, got, tt.want)
			}
			if (hint != "") != tt.wantHint {
				t.Errorf("resolveEditor(%q) hint = %q, want hint %v", tt.env, hint, tt.wantHint)
			}
		})
	}
}

// wiredHome builds a home where setup has run: ~/.ssh/config includes
// sasash.conf, and sasash.conf holds conf.
func wiredHome(t *testing.T, conf string) string {
	t.Helper()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "config.d"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte("Include ~/.ssh/config.d/sasash.conf\n"), 0o600); err != nil {
		t.Fatalf("write root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config.d", "sasash.conf"), []byte(conf), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	return home
}

func TestTuiConfig(t *testing.T) {
	t.Parallel()
	wired := wiredHome(t, "# managed\n")
	bare := t.TempDir()
	// Include present, sasash.conf not yet: the editor will create the
	// file and ssh already includes it — no warning.
	included := wiredHome(t, "")
	if err := os.Remove(filepath.Join(included, ".ssh", "config.d", "sasash.conf")); err != nil {
		t.Fatalf("remove conf: %v", err)
	}
	tests := []struct {
		name       string
		home       string
		editorEnv  string
		wantEditor []string
		wantHint   bool
		wantWarn   bool
	}{
		{name: "wired home with editor", home: wired, editorEnv: "code -w", wantEditor: []string{"code", "-w"}},
		{name: "bare home falls back and warns", home: bare, editorEnv: "", wantEditor: []string{"vi"}, wantHint: true, wantWarn: true},
		{name: "include without conf does not warn", home: included, editorEnv: "vi", wantEditor: []string{"vi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tuiConfig(tt.home, filepath.Join(tt.home, "config.toml"), "nord", config.Config{}, tt.editorEnv, func(string) string { return "" })
			// SPEC §2: `e` edits sasash.conf, the one file sasash writes —
			// never ~/.ssh/config.
			if want := filepath.Join(tt.home, ".ssh", "config.d", "sasash.conf"); cfg.ConfPath != want {
				t.Errorf("tuiConfig().ConfPath = %q, want %q", cfg.ConfPath, want)
			}
			if cfg.Theme != "nord" {
				t.Errorf("tuiConfig().Theme = %q, want %q", cfg.Theme, "nord")
			}
			if !reflect.DeepEqual(cfg.Editor, tt.wantEditor) {
				t.Errorf("tuiConfig().Editor = %v, want %v", cfg.Editor, tt.wantEditor)
			}
			if (cfg.EditorHint != "") != tt.wantHint {
				t.Errorf("tuiConfig().EditorHint = %q, want hint %v", cfg.EditorHint, tt.wantHint)
			}
			if (cfg.EditorWarn != "") != tt.wantWarn || (tt.wantWarn && !strings.Contains(cfg.EditorWarn, "sasash setup")) {
				t.Errorf("tuiConfig().EditorWarn = %q, want setup warning %v", cfg.EditorWarn, tt.wantWarn)
			}
			if cfg.SaveShowHidden == nil || cfg.Setup.Config != filepath.Join(tt.home, "config.toml") {
				t.Errorf("tuiConfig() SaveShowHidden wired = %v, Setup.Config = %q; want wired and the config path", cfg.SaveShowHidden != nil, cfg.Setup.Config)
			}
			if cfg.SaveTheme == nil || cfg.SaveHost == nil || cfg.LoadHosts == nil {
				t.Errorf("tuiConfig() hooks = %v %v %v, want all wired", cfg.SaveTheme != nil, cfg.SaveHost != nil, cfg.LoadHosts != nil)
			}
			if cfg.SaveForwardEdit == nil {
				t.Errorf("tuiConfig().SaveForwardEdit = nil, want fwdset.Update wired")
			}
			if cfg.SaveForwardSet == nil || cfg.Home != tt.home {
				t.Errorf("tuiConfig() SaveForwardSet wired = %v, Home = %q; want wired and %q", cfg.SaveForwardSet != nil, cfg.Home, tt.home)
			}
			// The TUI and `up`/`down` must share one state cache; the stats
			// store sits beside it and stamps handoffs.
			eng, ok := cfg.Tunnels.(tunnel.Engine)
			if !ok || eng.StateDir != stateDir(tt.home) {
				t.Errorf("tuiConfig().Tunnels = %#v, want tunnel.Engine with StateDir %q", cfg.Tunnels, stateDir(tt.home))
			}
			if cfg.SetPath == nil || cfg.PathBlock == nil || cfg.RecordSwitch == nil {
				t.Errorf("tuiConfig() p hooks: set %v, block %v, record %v; want all wired", cfg.SetPath != nil, cfg.PathBlock != nil, cfg.RecordSwitch != nil)
			}
			if eng.Stats == nil || eng.Stats.Dir != stateDir(tt.home) || cfg.RecordHandoff == nil {
				t.Errorf("tuiConfig() stats: engine store %+v, RecordHandoff wired %v; want the store on %q and the hook", eng.Stats, cfg.RecordHandoff != nil, stateDir(tt.home))
			}
		})
	}
}

func TestTuiConfig_LoadHostsReportsWarnings(t *testing.T) {
	t.Parallel()
	// A malformed hand-edit must come back as a warning, not vanish: under
	// the alt screen the TUI notice is the only place it can surface.
	home := wiredHome(t, "# @sasash path lan\nHost edge\n  HostName 192.0.2.9\n")
	hosts, warnings, err := tuiConfig(home, filepath.Join(home, "config.toml"), "", config.Config{}, "vi", func(string) string { return "" }).LoadHosts()
	if err != nil {
		t.Fatalf("LoadHosts() = %v", err)
	}
	if len(hosts) != 1 || hosts[0].Alias != "edge" {
		t.Errorf("LoadHosts() hosts = %+v, want the edge host", hosts)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "missing addr") {
		t.Errorf("LoadHosts() warnings = %v, want one missing-addr warning", warnings)
	}
}

func TestLoadActionNames(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, body string) string {
		t.Helper()
		home := t.TempDir()
		if body == "" {
			return home
		}
		if err := os.MkdirAll(filepath.Join(home, ".config", "sasash"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(action.Path(home), []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return home
	}
	tests := []struct {
		name      string
		body      string
		wantNames []string
		wantWarn  string
	}{
		{name: "no file is no actions and no warning", body: ""},
		{name: "entries in file order", body: "[vnc]\ncmd = \"vncviewer {addr}\"\n[rdp]\ncmd = \"xfreerdp /v:{addr}\"\n", wantNames: []string{"vnc", "rdp"}},
		{name: "malformed file warns with the line", body: "[rdp]\ncmd = \"unterminated\n", wantWarn: "actions.toml:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			names, warn := loadActionNames(write(t, tt.body))
			if !reflect.DeepEqual(names, tt.wantNames) {
				t.Errorf("loadActionNames() names = %v, want %v", names, tt.wantNames)
			}
			if (warn != "") != (tt.wantWarn != "") || !strings.Contains(warn, tt.wantWarn) {
				t.Errorf("loadActionNames() warn = %q, want containing %q", warn, tt.wantWarn)
			}
		})
	}
}

func TestTuiConfig_PathCycleHooks(t *testing.T) {
	t.Parallel()
	// Two named paths in sasash.conf: PathBlock reads that block's paths
	// and HostName — even when ~/.ssh/config's own earlier block for the
	// alias would win the host model — and SwitchPath really cycles the
	// file through pathswitch.
	conf := "# @sasash path lan 192.0.2.10\n# @sasash path ts 198.51.100.5\nHost web\n  HostName 192.0.2.10\n"
	home := wiredHome(t, conf)
	root := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(root, []byte("# @sasash path office 203.0.113.9\nHost web\n  HostName 203.0.113.9\nInclude ~/.ssh/config.d/sasash.conf\n"), 0o600); err != nil {
		t.Fatalf("write root: %v", err)
	}
	cfg := tuiConfig(home, filepath.Join(home, "config.toml"), "", config.Config{}, "vi", func(string) string { return "" })
	paths, hostName, ok := cfg.PathBlock("web")
	if !ok || hostName != "192.0.2.10" {
		t.Errorf("PathBlock(web) = %v, %q, %v; want the sasash.conf block", paths, hostName, ok)
	}
	if len(paths) != 2 || paths[0].Name != "lan" || paths[1].Name != "ts" {
		t.Errorf("PathBlock(web) paths = %+v, want lan, ts from sasash.conf (not office from the root)", paths)
	}
	if _, _, ok := cfg.PathBlock("nope"); ok {
		t.Errorf("PathBlock(nope) ok = true, want false for an unmanaged alias")
	}
	res, err := cfg.SetPath("web", "ts")
	if err != nil || !res.Changed || res.To.Name != "ts" {
		t.Fatalf("SetPath(web, ts) = %+v, %v; want a switch to ts", res, err)
	}
	if _, got, _ := cfg.PathBlock("web"); got != "198.51.100.5" {
		t.Errorf("HostName after the switch = %q, want 198.51.100.5", got)
	}
	if err := cfg.RecordSwitch("web", "ts"); err != nil {
		t.Errorf("RecordSwitch = %v", err)
	}
}

func TestCmdTUI_UnknownThemeFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	if got := dispatch([]string{"tui", "-theme", "solarized"}, &stdout, &stderr); got != 2 {
		t.Errorf("dispatch(tui -theme solarized) = %v, want 2", got)
	}
	if !strings.Contains(stderr.String(), "gruvbox") {
		t.Errorf("stderr = %q, want the valid theme list", stderr.String())
	}
}

func TestSaveHostTo_AppendsPreservingBytes(t *testing.T) {
	t.Parallel()
	// Odd-but-valid existing content: mixed indent, unknown directive,
	// no trailing newline. Every byte of it must survive the append.
	existing := "# ─── managed by sasash ───\n\n# @sasash path lan 192.0.2.7\nHost old\n\tHostName 192.0.2.7\n  UnknownDirective kept\nHost plain"
	path := filepath.Join(t.TempDir(), "sasash.conf")
	// 0640 differs from sshconf.DefaultPerm, so the preserved-perm
	// assertion cannot pass by accident.
	if err := os.WriteFile(path, []byte(existing), 0o640); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	err := saveHostTo(path)(ui.AdhocEntry{
		Alias:    "edge",
		User:     "root",
		Host:     "192.0.2.99",
		Port:     "2222",
		Forwards: []string{"8080:localhost:80", "5432:127.0.0.1:5432"},
	})
	if err != nil {
		t.Fatalf("saveHostTo() = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// Full == so the missing-trailing-newline repair is load-bearing:
	// exactly one added terminator, exactly one blank separator.
	// The managed-entry template, ControlMaster trio included (4.18).
	want := existing + "\n\n# @sasash forwards adhoc 8080:localhost:80 5432:127.0.0.1:5432\nHost edge\n  HostName 192.0.2.99\n  User root\n  Port 2222\n  ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n"
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o640 {
		t.Errorf("perm after save = %v, want the fixture's 0640 preserved", perm)
	}
}

func TestSaveHostTo_RoundTripsThroughHostmodel(t *testing.T) {
	t.Parallel()
	// Two saved entries, both carrying the per-host "adhoc" forward-set
	// name, must come back through the real Include + hostmodel pipeline
	// with zero warnings — pins the decision that the name is per-host
	// and the OwnMeta attachment boundary between adjacent saved blocks.
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "config.d"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root := filepath.Join(sshDir, "config")
	conf := filepath.Join(sshDir, "config.d", "sasash.conf")
	if err := os.WriteFile(root, []byte("Include ~/.ssh/config.d/sasash.conf\n"), 0o600); err != nil {
		t.Fatalf("write root: %v", err)
	}
	if err := os.WriteFile(conf, []byte("# managed\n"), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	save := saveHostTo(conf)
	if err := save(ui.AdhocEntry{Alias: "edge", User: "root", Host: "192.0.2.99", Port: "2222", Forwards: []string{"8080:localhost:80"}}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := save(ui.AdhocEntry{Alias: "spare", Host: "192.0.2.98", Forwards: []string{"5432:127.0.0.1:5432"}}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("Load() warnings = %v, want none", warnings)
	}
	declaring := filepath.Join(home, ".ssh", "config.d", "sasash.conf")
	want := []hostmodel.Host{
		{Alias: "edge", HostName: "192.0.2.99", Paths: []probe.Path{{Host: "192.0.2.99", Port: "2222"}}, Forwards: []hostmodel.ForwardSet{{Name: "adhoc", Specs: []string{"8080:localhost:80"}}}, File: declaring, HasMeta: true},
		{Alias: "spare", HostName: "192.0.2.98", Paths: []probe.Path{{Host: "192.0.2.98"}}, Forwards: []hostmodel.ForwardSet{{Name: "adhoc", Specs: []string{"5432:127.0.0.1:5432"}}}, File: declaring, HasMeta: true},
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Errorf("Load() = %+v, want %+v", hosts, want)
	}
}

func TestSaveHostTo_MinimalEntryAndSecondAppend(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sasash.conf")
	if err := os.WriteFile(path, []byte("# header\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := saveHostTo(path)(ui.AdhocEntry{Alias: "a1", Host: "192.0.2.5"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := saveHostTo(path)(ui.AdhocEntry{Alias: "a2", Host: "192.0.2.6"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	trio := "  ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n"
	want := "# header\n\nHost a1\n  HostName 192.0.2.5\n" + trio + "\nHost a2\n  HostName 192.0.2.6\n" + trio
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestSaveHostTo_MissingConfErrors(t *testing.T) {
	t.Parallel()
	err := saveHostTo(filepath.Join(t.TempDir(), "sasash.conf"))(ui.AdhocEntry{Alias: "x", Host: "192.0.2.5"})
	if err == nil || !strings.Contains(err.Error(), "sasash setup") {
		t.Errorf("saveHostTo() on missing conf = %v, want a setup hint", err)
	}
}

func TestDispatch_LeadingFlagRoutesToTUI(t *testing.T) {
	t.Parallel()
	// `sasash -theme x` must reach the tui subcommand, not the dispatch
	// unknown-subcommand error; the typoed theme proves it got there.
	var stdout, stderr strings.Builder
	if got := dispatch([]string{"-theme", "solarized"}, &stdout, &stderr); got != 2 {
		t.Errorf("dispatch(-theme solarized) = %v, want 2 from the tui flag check", got)
	}
	if !strings.Contains(stderr.String(), "unknown theme") {
		t.Errorf("stderr = %q, want the tui unknown-theme error", stderr.String())
	}
	// -h stays help, never tui.
	stdout.Reset()
	if got := dispatch([]string{"-h"}, &stdout, &stderr); got != 0 || !strings.Contains(stdout.String(), "usage: sasash") {
		t.Errorf("dispatch(-h) = %v stdout %q, want usage", got, stdout.String())
	}
}
