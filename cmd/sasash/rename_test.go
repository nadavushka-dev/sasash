package main

import (
	"errors"
	"github.com/nadavushka-dev/sasash/internal/config"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The TUI's rename hook (4.16) over the merged writer: sasash.conf first,
// then the state caches follow the host.

func TestRenameHost_Cases(t *testing.T) {
	t.Parallel()
	const conf = "Host old\n  HostName 192.0.2.7\nHost taken\n  HostName 192.0.2.8\n"
	variants := "# @sasash path lan 192.0.2.10\n# @sasash path wan 203.0.113.10\nHost ubuntu\n  HostName 192.0.2.10\n\n" + pathswitch.Header + "\nHost ubuntu.lan\n  HostName 192.0.2.10\nHost ubuntu.wan\n  HostName 203.0.113.10\n"
	tests := []struct {
		name      string
		conf      string
		files     []string // state files present before
		old, new  string
		wantConf  string
		wantFiles []string
		wantRes   pathswitch.RenameResult
		wantErr   string
	}{
		{name: "conf and both caches follow", conf: conf, files: []string{"old.json", "old.stats.json", "taken.json"}, old: "old", new: "new",
			wantConf: "Host new\n  HostName 192.0.2.7\nHost taken\n  HostName 192.0.2.8\n", wantFiles: []string{"new.json", "new.stats.json", "taken.json"}},
		{name: "missing caches are skipped", conf: conf, files: []string{"old.stats.json"}, old: "old", new: "new",
			wantConf: "Host new\n  HostName 192.0.2.7\nHost taken\n  HostName 192.0.2.8\n", wantFiles: []string{"new.stats.json"}},
		{name: "variants follow", conf: variants, old: "ubuntu", new: "box",
			wantConf: strings.ReplaceAll(variants, "ubuntu", "box"), wantRes: pathswitch.RenameResult{Followed: []string{"lan", "wan"}}},
		{name: "writer refusal moves nothing", conf: conf, files: []string{"old.json"}, old: "old", new: "taken",
			wantConf: conf, wantFiles: []string{"old.json"}, wantErr: `alias "taken" already exists`},
		{name: "a pinned variant is refused", conf: variants, files: []string{"ubuntu.lan.json"}, old: "ubuntu.lan", new: "x",
			wantConf: variants, wantFiles: []string{"ubuntu.lan.json"}, wantErr: "ubuntu.lan is a generated pinned variant of ubuntu"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			confPath := filepath.Join(dir, "sasash.conf")
			if err := os.WriteFile(confPath, []byte(tt.conf), 0o600); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(dir, "state")
			if err := os.Mkdir(state, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(state, f), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			res, err := renameHost(confPath, state)(tt.old, tt.new)
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("renameHost() err = %v, want %q", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(res, tt.wantRes) {
				t.Errorf("renameHost() = %+v, want %+v", res, tt.wantRes)
			}
			if got, _ := os.ReadFile(confPath); string(got) != tt.wantConf {
				t.Errorf("sasash.conf after = %q, want %q", got, tt.wantConf)
			}
			entries, _ := os.ReadDir(state)
			var files []string
			for _, e := range entries {
				files = append(files, e.Name())
			}
			slices.Sort(files)
			want := slices.Clone(tt.wantFiles)
			slices.Sort(want)
			if !reflect.DeepEqual(files, want) {
				t.Errorf("state files after = %v, want %v", files, want)
			}
		})
	}
}

func TestRenameHost_KeptCache(t *testing.T) {
	t.Parallel()
	// A cache that cannot move (a non-empty directory sits where it
	// goes) is reported under ui.ErrCacheKept, the host renamed all the
	// same: the host is the file's; the cache is history.
	dir := t.TempDir()
	confPath := filepath.Join(dir, "sasash.conf")
	if err := os.WriteFile(confPath, []byte("Host old\n  HostName 192.0.2.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The stats file after it still moves: one failure never orphans
	// the next cache under the old name.
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(state, "new.json", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"old.json", "old.stats.json"} {
		if err := os.WriteFile(filepath.Join(state, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := renameHost(confPath, state)("old", "new")
	if !errors.Is(err, ui.ErrCacheKept) || !strings.Contains(err.Error(), "old.json") {
		t.Errorf("renameHost() err = %v, want ErrCacheKept naming old.json", err)
	}
	if _, err := os.Stat(filepath.Join(state, "new.stats.json")); err != nil {
		t.Errorf("new.stats.json should have moved despite the earlier failure: %v", err)
	}
	if got, _ := os.ReadFile(confPath); string(got) != "Host new\n  HostName 192.0.2.7\n" {
		t.Errorf("sasash.conf after = %q, want the host renamed", got)
	}
	if _, err := os.Stat(filepath.Join(state, "old.json")); err != nil {
		t.Errorf("old.json should stay where the move failed: %v", err)
	}
}

func TestRenameHost_LeftoverUnderTheNewName(t *testing.T) {
	t.Parallel()
	// An earlier host's history under the new name is not overwritten:
	// both files stay, and the report names them.
	dir := t.TempDir()
	confPath := filepath.Join(dir, "sasash.conf")
	if err := os.WriteFile(confPath, []byte("Host old\n  HostName 192.0.2.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"old.stats.json", "new.stats.json"} {
		if err := os.WriteFile(filepath.Join(state, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := renameHost(confPath, state)("old", "new")
	if !errors.Is(err, ui.ErrCacheKept) || !strings.Contains(err.Error(), "new.stats.json exists: ") || !strings.Contains(err.Error(), "old.stats.json left in place") {
		t.Errorf("renameHost() err = %v, want ErrCacheKept naming both files", err)
	}
	for _, f := range []string{"old.stats.json", "new.stats.json"} {
		if got, _ := os.ReadFile(filepath.Join(state, f)); string(got) != f {
			t.Errorf("%s = %q, want untouched", f, got)
		}
	}
}

func TestRenameHost_UnreadableConf(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := renameHost(filepath.Join(dir, "missing.conf"), dir)("old", "new"); err == nil {
		t.Error("renameHost() on a missing file = nil error, want one")
	}
}

func TestTuiConfig_RenameHostWired(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if cfg := tuiConfig(home, filepath.Join(home, "config.toml"), "", config.Config{}, ""); cfg.RenameHost == nil {
		t.Error("tuiConfig().RenameHost = nil, want renameHost wired")
	}
}
