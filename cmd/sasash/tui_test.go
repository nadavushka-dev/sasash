// Internal test package, like main_test.go: main cannot be imported.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
