package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		if cfg.WindowTemplate != defaultWindowTemplate(runtime.GOOS, getenv) {
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
		env  map[string]string
		want string
	}{
		{name: "darwin opens Terminal", goos: "darwin", want: `osascript -e 'tell application "Terminal" to do script "ssh {host}"'`},
		{name: "linux opens the terminal alternative when it is on PATH", goos: "linux", env: map[string]string{"PATH": "alt"}, want: "x-terminal-emulator -e ssh {host}"},
		{name: "linux falls back to gnome-terminal", goos: "linux", env: map[string]string{"PATH": "gnome"}, want: "gnome-terminal -- ssh {host}"},
		{name: "the alternative wins over gnome-terminal", goos: "linux", env: map[string]string{"PATH": "gnome:alt"}, want: "x-terminal-emulator -e ssh {host}"},
		{name: "linux without either has no default", goos: "linux", env: map[string]string{"PATH": "empty"}},
		{name: "a non-executable file does not count", goos: "linux", env: map[string]string{"PATH": "noexec"}},
		{name: "WSL opens a Windows Terminal tab in this distro through cmd.exe", goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu 24.04"}, want: "cmd.exe /c wt.exe new-tab wsl.exe -d 'Ubuntu 24.04' -- ssh {host}"},
		{name: "anything else has no default", goos: "freebsd"},
	}
	// PATH entries are names of temp dirs built here: "alt" holds an
	// executable x-terminal-emulator, "gnome" a gnome-terminal, "noexec"
	// a non-executable x-terminal-emulator, "empty" nothing.
	dirs := map[string]string{}
	for _, name := range []string{"alt", "gnome", "empty", "noexec"} {
		dirs[name] = t.TempDir()
	}
	for dir, file := range map[string]string{"alt": "x-terminal-emulator", "gnome": "gnome-terminal"} {
		if err := os.WriteFile(filepath.Join(dirs[dir], file), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dirs["noexec"], "x-terminal-emulator"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string {
				v := tt.env[k]
				if k == "PATH" && v != "" {
					var parts []string
					for _, name := range strings.Split(v, ":") {
						parts = append(parts, dirs[name])
					}
					return strings.Join(parts, string(os.PathListSeparator))
				}
				return v
			}
			if got := defaultWindowTemplate(tt.goos, getenv); got != tt.want {
				t.Errorf("defaultWindowTemplate(%s, PATH=%s) = %q, want %q", tt.goos, tt.env["PATH"], got, tt.want)
			}
		})
	}
}
