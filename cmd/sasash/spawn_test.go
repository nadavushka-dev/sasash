package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/config"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

func TestSpawnConfig_Wiring(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w24:p16"}
	getenv := func(k string) string { return env[k] }
	t.Run("detects, defaults, and persists the target in config.toml", func(t *testing.T) {
		t.Parallel()
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		cfg := spawnConfig(cfgPath, config.Config{}, getenv)
		if cfg.Mux != ui.MuxHerdr || cfg.HerdrPane != "w24:p16" || !cfg.HerdrEnv || cfg.Target != ui.DefaultSpawnTarget {
			t.Errorf("spawnConfig() = mux %v, pane %q, herdrEnv %v, target %q", cfg.Mux, cfg.HerdrPane, cfg.HerdrEnv, cfg.Target)
		}
		// tmux started inside a herdr pane: tmux wins, the herdr signal stays.
		both := func(k string) string { return map[string]string{"TMUX": "/tmp/tmux-501/x,1,0", "HERDR_ENV": "1"}[k] }
		if c := spawnConfig(cfgPath, config.Config{}, both); c.Mux != ui.MuxTmux || !c.HerdrEnv {
			t.Errorf("spawnConfig(tmux inside herdr) = mux %v, herdrEnv %v; want tmux, true", c.Mux, c.HerdrEnv)
		}
		if c := spawnConfig(cfgPath, config.Config{}, func(string) string { return "" }); c.HerdrEnv || c.Emulator != ui.EmuNone {
			t.Errorf("spawnConfig(no env) = herdrEnv %v, emulator %v; want false, none", c.HerdrEnv, c.Emulator)
		}
		// The outer emulator rides along, for H's tab.
		wez := func(k string) string { return map[string]string{"HERDR_ENV": "1", "WEZTERM_PANE": "3"}[k] }
		if c := spawnConfig(cfgPath, config.Config{}, wez); c.Emulator != ui.EmuWezTerm {
			t.Errorf("spawnConfig(herdr in wezterm).Emulator = %v, want wezterm", c.Emulator)
		}
		if cfg.WindowTemplate != defaultWindowTemplate(runtime.GOOS, getenv) {
			t.Errorf("WindowTemplate without spawn_window = %q, want this OS's default", cfg.WindowTemplate)
		}
		if cfg.WindowPath != cfgPath {
			t.Errorf("WindowPath = %q, want the config file %q", cfg.WindowPath, cfgPath)
		}
		if cfg.SaveTarget == nil {
			t.Fatalf("SaveTarget = nil, want the persistence hook")
		}
		if err := cfg.SaveTarget("workspace"); err != nil {
			t.Fatalf("SaveTarget(workspace) = %v", err)
		}
		uc, warns, err := config.Load(cfgPath)
		if err != nil || warns != nil {
			t.Fatalf("Load() after SaveTarget = %+v, %v, %v", uc, warns, err)
		}
		if got := spawnConfig(cfgPath, uc, getenv).Target; got != "workspace" {
			t.Errorf("Target after save = %q, want workspace", got)
		}
	})
	t.Run("a stale target value falls back to the default", func(t *testing.T) {
		t.Parallel()
		if got := spawnConfig("/nonexistent/config.toml", config.Config{Present: true, SpawnTarget: "floating"}, getenv).Target; got != ui.DefaultSpawnTarget {
			t.Errorf("Target = %q, want %q", got, ui.DefaultSpawnTarget)
		}
	})
	t.Run("spawn_window overrides the default, and an empty one turns it off", func(t *testing.T) {
		t.Parallel()
		set := config.Config{Present: true, SpawnWindow: "kitty ssh {host}", SpawnWindowSet: true}
		if got := spawnConfig("/nonexistent/config.toml", set, getenv).WindowTemplate; got != "kitty ssh {host}" {
			t.Errorf("WindowTemplate = %q, want the config's line", got)
		}
		off := config.Config{Present: true, SpawnWindowSet: true}
		if got := spawnConfig("/nonexistent/config.toml", off, getenv).WindowTemplate; got != "" {
			t.Errorf("WindowTemplate with a live empty spawn_window = %q, want none", got)
		}
		unset := config.Config{Present: true}
		if got := spawnConfig("/nonexistent/config.toml", unset, getenv).WindowTemplate; got != defaultWindowTemplate(runtime.GOOS, getenv) {
			t.Errorf("WindowTemplate with spawn_window unset = %q, want the platform default", got)
		}
	})
	t.Run("the legacy paths setup moves", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		if got, want := spawnStatePath(home, func(string) string { return "" }), filepath.Join(home, ".local", "state", "sasash", "spawn-target"); got != want {
			t.Errorf("spawnStatePath() = %q, want %q", got, want)
		}
		xdg := func(k string) string { return map[string]string{"XDG_CONFIG_HOME": "/x"}[k] }
		if got, want := spawnWindowPath(home, xdg), filepath.Join("/x", "sasash", "spawn-window"); got != want {
			t.Errorf("spawnWindowPath(xdg) = %q, want %q", got, want)
		}
		if got, want := spawnWindowPath(home, func(string) string { return "" }), filepath.Join(home, ".config", "sasash", "spawn-window"); got != want {
			t.Errorf("spawnWindowPath() = %q, want %q", got, want)
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
		{name: "WSL opens a Windows Terminal tab in this distro through cmd.exe", goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu 24.04", "PATH": "wsl"}, want: "cmd.exe /c wt.exe new-tab wsl.exe -d 'Ubuntu 24.04' -- ssh {host}"},
		{name: "WSL prefers Windows Terminal over a Linux terminal", goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "PATH": "alt:wsl"}, want: "cmd.exe /c wt.exe new-tab wsl.exe -d 'Ubuntu' -- ssh {host}"},
		{name: "WSL without cmd.exe on PATH (interop off) falls back to the Linux probe", goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "PATH": "alt"}, want: "x-terminal-emulator -e ssh {host}"},
		{name: "WSL without cmd.exe or a Linux terminal has no default", goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "PATH": "empty"}},
		{name: "WSL_DISTRO_NAME outside a Linux build is ignored", goos: "darwin", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "PATH": "wsl"}, want: `osascript -e 'tell application "Terminal" to do script "ssh {host}"'`},
		{name: "anything else has no default", goos: "freebsd"},
	}
	// PATH entries are names of temp dirs built here: "alt" holds an
	// executable x-terminal-emulator, "gnome" a gnome-terminal, "wsl" a
	// cmd.exe (the interop mount), "noexec" a non-executable
	// x-terminal-emulator, "empty" nothing.
	dirs := map[string]string{}
	for _, name := range []string{"alt", "gnome", "wsl", "empty", "noexec"} {
		dirs[name] = t.TempDir()
	}
	for dir, file := range map[string]string{"alt": "x-terminal-emulator", "gnome": "gnome-terminal", "wsl": "cmd.exe"} {
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
