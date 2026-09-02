// Internal test package, like main_test.go: main cannot be imported.
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestResolveTheme(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	persisted := filepath.Join(dir, "theme")
	if err := os.WriteFile(persisted, []byte("nord\n"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	stale := filepath.Join(dir, "stale")
	if err := os.WriteFile(stale, []byte("solarized\n"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	missing := filepath.Join(dir, "none")

	tests := []struct {
		name      string
		flagVal   string
		envVal    string
		statePath string
		want      string
		wantWarn  bool
		wantFatal bool
	}{
		{name: "flag wins over env and state", flagVal: "mocha", envVal: "nord", statePath: persisted, want: "mocha"},
		{name: "unknown flag is fatal", flagVal: "solarized", statePath: missing, wantWarn: true, wantFatal: true},
		{name: "env wins over state", envVal: "tokyonight", statePath: persisted, want: "tokyonight"},
		{name: "bad env warns and falls back to state", envVal: "solarized", statePath: persisted, want: "nord", wantWarn: true},
		{name: "state alone", statePath: persisted, want: "nord"},
		{name: "stale state degrades to default", statePath: stale, want: ""},
		{name: "nothing set", statePath: missing, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warn, fatal := resolveTheme(tt.flagVal, tt.envVal, tt.statePath)
			if got != tt.want {
				t.Errorf("resolveTheme() name = %q, want %q", got, tt.want)
			}
			if (warn != "") != tt.wantWarn {
				t.Errorf("resolveTheme() warn = %q, want warn %v", warn, tt.wantWarn)
			}
			if fatal != tt.wantFatal {
				t.Errorf("resolveTheme() fatal = %v, want %v", fatal, tt.wantFatal)
			}
		})
	}
}

func TestSaveThemeTo_RoundTrip(t *testing.T) {
	t.Parallel()
	// The directory does not exist yet: first save must create it.
	path := filepath.Join(t.TempDir(), "state", "sasash", "theme")
	if err := saveThemeTo(path)("nord"); err != nil {
		t.Fatalf("saveThemeTo() = %v", err)
	}
	if got := readTheme(path); got != "nord" {
		t.Errorf("readTheme() = %q, want %q", got, "nord")
	}
	// Overwrite sticks.
	if err := saveThemeTo(path)("mocha"); err != nil {
		t.Fatalf("saveThemeTo() overwrite = %v", err)
	}
	if got := readTheme(path); got != "mocha" {
		t.Errorf("readTheme() after overwrite = %q, want %q", got, "mocha")
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
		env      string
		want     []string
		wantHint bool
	}{
		{name: "plain editor", env: "nvim", want: []string{"nvim"}},
		{name: "editor with args", env: "code -w", want: []string{"code", "-w"}},
		{name: "unset falls back to vi with a hint", env: "", want: []string{"vi"}, wantHint: true},
		{name: "whitespace only is unset", env: "   ", want: []string{"vi"}, wantHint: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, hint := resolveEditor(tt.env)
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
			cfg := tuiConfig(tt.home, filepath.Join(tt.home, "theme"), "nord", tt.editorEnv)
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
			if cfg.SaveTheme == nil || cfg.SaveHost == nil || cfg.LoadHosts == nil {
				t.Errorf("tuiConfig() hooks = %v %v %v, want all wired", cfg.SaveTheme != nil, cfg.SaveHost != nil, cfg.LoadHosts != nil)
			}
			// The TUI and `up`/`down` must share one state cache.
			if eng, ok := cfg.Tunnels.(tunnel.Engine); !ok || eng.StateDir != stateDir(tt.home) {
				t.Errorf("tuiConfig().Tunnels = %#v, want tunnel.Engine with StateDir %q", cfg.Tunnels, stateDir(tt.home))
			}
		})
	}
}

func TestTuiConfig_LoadHostsReportsWarnings(t *testing.T) {
	t.Parallel()
	// A malformed hand-edit must come back as a warning, not vanish: under
	// the alt screen the TUI notice is the only place it can surface.
	home := wiredHome(t, "# @sasash path lan\nHost edge\n  HostName 192.0.2.9\n")
	hosts, warnings, err := tuiConfig(home, filepath.Join(home, "theme"), "", "vi").LoadHosts()
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
	want := existing + "\n\n# @sasash forwards adhoc 8080:localhost:80 5432:127.0.0.1:5432\nHost edge\n  HostName 192.0.2.99\n  User root\n  Port 2222\n"
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
	want := []hostmodel.Host{
		{Alias: "edge", Paths: []probe.Path{{Host: "192.0.2.99", Port: "2222"}}, Forwards: []hostmodel.ForwardSet{{Name: "adhoc", Specs: []string{"8080:localhost:80"}}}},
		{Alias: "spare", Paths: []probe.Path{{Host: "192.0.2.98"}}, Forwards: []hostmodel.ForwardSet{{Name: "adhoc", Specs: []string{"5432:127.0.0.1:5432"}}}},
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
	want := "# header\n\nHost a1\n  HostName 192.0.2.5\n\nHost a2\n  HostName 192.0.2.6\n"
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
