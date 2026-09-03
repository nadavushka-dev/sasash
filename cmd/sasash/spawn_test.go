package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestSpawnConfig_Wiring(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w24:p16"}
	getenv := func(k string) string { return env[k] }
	t.Run("detects, defaults, and persists beside the theme", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		cfg := spawnConfig(home, getenv)
		if cfg.Mux != ui.MuxHerdr || cfg.HerdrPane != "w24:p16" || cfg.Target != ui.DefaultSpawnTarget {
			t.Errorf("spawnConfig() = mux %v, pane %q, target %q", cfg.Mux, cfg.HerdrPane, cfg.Target)
		}
		if cfg.WindowTemplate != defaultWindowTemplate(runtime.GOOS) {
			t.Errorf("WindowTemplate without a file = %q, want this OS's default", cfg.WindowTemplate)
		}
		if cfg.SaveTarget == nil {
			t.Fatalf("SaveTarget = nil, want the persistence hook")
		}
		if err := cfg.SaveTarget("workspace"); err != nil {
			t.Fatalf("SaveTarget(workspace) = %v", err)
		}
		if got := spawnConfig(home, getenv).Target; got != "workspace" {
			t.Errorf("Target after save = %q, want workspace", got)
		}
		if want := filepath.Join(home, ".local", "state", "sasash", "spawn-target"); spawnStatePath(home) != want {
			t.Errorf("spawnStatePath() = %q, want %q", spawnStatePath(home), want)
		}
	})
	t.Run("a stale target file falls back to the default", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		if err := saveStateTo(spawnStatePath(home))("floating"); err != nil {
			t.Fatal(err)
		}
		if got := spawnConfig(home, getenv).Target; got != ui.DefaultSpawnTarget {
			t.Errorf("Target = %q, want %q", got, ui.DefaultSpawnTarget)
		}
	})
	t.Run("the window template file overrides the default", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		path := filepath.Join(home, ".config", "sasash", "spawn-window")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("kitty ssh {host}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := spawnConfig(home, getenv).WindowTemplate; got != "kitty ssh {host}" {
			t.Errorf("WindowTemplate = %q, want the file's line", got)
		}
		// Present but empty means no template: the handoff, not the default.
		if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := spawnConfig(home, getenv).WindowTemplate; got != "" {
			t.Errorf("WindowTemplate with an empty file = %q, want none", got)
		}
	})
	t.Run("XDG_CONFIG_HOME relocates the template", func(t *testing.T) {
		t.Parallel()
		home, xdg := t.TempDir(), t.TempDir()
		env := map[string]string{"XDG_CONFIG_HOME": xdg}
		path := filepath.Join(xdg, "sasash", "spawn-window")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("wezterm start -- ssh {host}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := spawnConfig(home, func(k string) string { return env[k] }).WindowTemplate; got != "wezterm start -- ssh {host}" {
			t.Errorf("WindowTemplate = %q, want the XDG file's line", got)
		}
	})
}

func TestDefaultWindowTemplate_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		goos string
		want string
	}{
		{name: "darwin opens Terminal", goos: "darwin", want: `osascript -e 'tell application "Terminal" to do script "ssh {host}"'`},
		{name: "linux has no default", goos: "linux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := defaultWindowTemplate(tt.goos); got != tt.want {
				t.Errorf("defaultWindowTemplate(%s) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}
