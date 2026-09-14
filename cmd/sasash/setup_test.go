// Internal test package, like main_test.go: main packages cannot be
// imported, so an external _test package has no access to setupHome.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/config"
	"github.com/nadavushka-dev/sasash/internal/setup"
)

// testEnv is the Env the tests use: config.toml under home's .config,
// no seeded theme.
func testEnv(home string) setup.Env {
	return setup.Env{Home: home, Config: filepath.Join(home, ".config", "sasash", "config.toml")}
}

func TestSetupHome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		yes        bool
		stdin      string
		preSetup   bool // run the bootstrap before the command under test
		wantCode   int
		wantStdout string // substring
		wantConfig bool   // ~/.ssh/config must exist afterwards
	}{
		{
			name:       "confirmed with y",
			stdin:      "y\n",
			wantCode:   0,
			wantStdout: "done",
			wantConfig: true,
		},
		{
			name:       "confirmed with yes",
			stdin:      "YES\n",
			wantCode:   0,
			wantStdout: "done",
			wantConfig: true,
		},
		{
			name:       "declined with n",
			stdin:      "n\n",
			wantCode:   1,
			wantStdout: "aborted; nothing changed",
		},
		{
			name:       "declined by empty stdin",
			stdin:      "",
			wantCode:   1,
			wantStdout: "aborted; nothing changed",
		},
		{
			name:       "dash y skips the prompt",
			yes:        true,
			stdin:      "",
			wantCode:   0,
			wantStdout: "done",
			wantConfig: true,
		},
		{
			name:       "already set up asks nothing",
			preSetup:   true,
			stdin:      "",
			wantCode:   0,
			wantStdout: "already set up",
			wantConfig: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if tt.preSetup {
				if got := setupHome(testEnv(home), true, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); got != 0 {
					t.Fatalf("pre-setup setupHome() = %v, want 0", got)
				}
			}
			var stdout, stderr bytes.Buffer
			got := setupHome(testEnv(home), tt.yes, strings.NewReader(tt.stdin), &stdout, &stderr)
			if got != tt.wantCode {
				t.Errorf("setupHome() = %v, want %v (stderr %q)", got, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("setupHome() stdout = %q, want substring %q", stdout.String(), tt.wantStdout)
			}
			_, err := os.Stat(filepath.Join(home, ".ssh", "config"))
			if gotConfig := err == nil; gotConfig != tt.wantConfig {
				t.Errorf("config exists = %v, want %v", gotConfig, tt.wantConfig)
			}
			_, err = os.Stat(testEnv(home).Config)
			if gotToml := err == nil; gotToml != tt.wantConfig {
				t.Errorf("config.toml exists = %v, want %v", gotToml, tt.wantConfig)
			}
		})
	}
}

func TestSetupHome_CheckError(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// A config.d that is a regular file makes Check fail.
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) failed: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("Host *\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.d"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(config.d) failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := setupHome(testEnv(home), true, strings.NewReader(""), &stdout, &stderr); got != 1 {
		t.Errorf("setupHome() = %v, want 1", got)
	}
	if !strings.Contains(stderr.String(), "sasash setup:") {
		t.Errorf("setupHome() stderr = %q, want an error report", stderr.String())
	}
}

func TestRunSetup_BadFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if got := runSetup([]string{"-bogus"}, strings.NewReader(""), &stdout, &stderr); got != 2 {
		t.Errorf("runSetup(-bogus) = %v, want 2", got)
	}
}

func TestSetupHome_ConfigToml(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		legacy     []setup.Legacy
		existing   string // config.toml before, "" for none
		wantStdout []string
		wantLine   string // in config.toml afterwards
	}{
		{name: "fresh template", wantStdout: []string{"create ~/.config/sasash/config.toml"}, wantLine: "# theme = \"gruvbox\"\n"},
		{name: "an old theme is moved in and announced",
			legacy:     []setup.Legacy{{Path: "@HOME@/.local/state/sasash/theme", Key: "theme", Value: "nord"}},
			wantStdout: []string{"create ~/.config/sasash/config.toml", `move ~/.local/state/sasash/theme into config.toml as theme = "nord"`},
			wantLine:   "theme = \"nord\"\n"},
		{name: "an old file with nothing to keep is removed and announced",
			legacy:     []setup.Legacy{{Path: "@HOME@/.local/state/sasash/theme", Key: "theme"}},
			wantStdout: []string{"remove ~/.local/state/sasash/theme (it names nothing sasash can keep)"},
			wantLine:   "# theme = \"gruvbox\"\n"},
		{name: "a key already set by hand is announced as a remove, and kept",
			legacy:     []setup.Legacy{{Path: "@HOME@/.local/state/sasash/theme", Key: "theme", Value: "nord"}},
			existing:   "theme = \"mocha\"\n",
			wantStdout: []string{"remove ~/.local/state/sasash/theme (theme is already set in config.toml)"},
			wantLine:   "theme = \"mocha\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			env := testEnv(home)
			if tt.existing != "" {
				if err := os.MkdirAll(filepath.Dir(env.Config), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(env.Config, []byte(tt.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, l := range tt.legacy {
				l.Path = strings.Replace(l.Path, "@HOME@", home, 1)
				if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(l.Path, []byte("x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				env.Legacy = append(env.Legacy, l)
			}
			var stdout, stderr bytes.Buffer
			if got := setupHome(env, true, strings.NewReader(""), &stdout, &stderr); got != 0 {
				t.Fatalf("setupHome() = %v, want 0 (stderr %q)", got, stderr.String())
			}
			for _, w := range tt.wantStdout {
				if !strings.Contains(stdout.String(), w) {
					t.Errorf("stdout = %q, want substring %q", stdout.String(), w)
				}
			}
			data, err := os.ReadFile(env.Config)
			if err != nil {
				t.Fatalf("ReadFile(config.toml): %v", err)
			}
			if !strings.Contains(string(data), tt.wantLine) {
				t.Errorf("config.toml =\n%s\nwant it to contain %q", data, tt.wantLine)
			}
			if tt.existing == "" && !strings.HasPrefix(string(data), config.Header) {
				t.Errorf("config.toml does not start with the template header:\n%s", data)
			}
			for _, l := range env.Legacy {
				if _, err := os.Lstat(l.Path); !os.IsNotExist(err) {
					t.Errorf("old file %s still exists (err %v)", l.Path, err)
				}
			}
			stdout.Reset()
			if got := setupHome(env, true, strings.NewReader(""), &stdout, &stderr); got != 0 || !strings.Contains(stdout.String(), "already set up") {
				t.Errorf("second setupHome() = %v, %q; want 0 and already set up", got, stdout.String())
			}
		})
	}
}

func TestSetupEnv(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	getenv := func(k string) string {
		return map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "cfg"), "XDG_STATE_HOME": filepath.Join(home, "state")}[k]
	}
	write := func(path, v string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(v), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	wantConfig := filepath.Join(home, "cfg", "sasash", "config.toml")
	themePath := filepath.Join(home, "state", "sasash", "theme")
	targetPath := filepath.Join(home, "state", "sasash", "spawn-target")
	windowPath := filepath.Join(home, "cfg", "sasash", "spawn-window")
	values := func() ([]any, []string) {
		env, problems := setupEnv(home, getenv)
		if env.Home != home || env.Config != wantConfig {
			t.Errorf("setupEnv() = %+v, want home and %q", env, wantConfig)
		}
		var out []any
		for _, l := range env.Legacy {
			out = append(out, l.Value)
		}
		return out, problems
	}
	if got, problems := values(); !reflect.DeepEqual(got, []any{nil, nil, nil}) || problems != nil {
		t.Errorf("values without old files = %v, %v; want all nil and no problems", got, problems)
	}
	env, _ := setupEnv(home, getenv)
	for i, want := range []setup.Legacy{{Path: themePath, Key: "theme"}, {Path: targetPath, Key: "spawn_target"}, {Path: windowPath, Key: "spawn_window"}} {
		if env.Legacy[i].Path != want.Path || env.Legacy[i].Key != want.Key {
			t.Errorf("setupEnv().Legacy[%d] = %+v, want %s at %s", i, env.Legacy[i], want.Key, want.Path)
		}
	}
	write(themePath, "solarized\n")
	write(targetPath, "floating\n")
	write(windowPath, "\n")
	if got, _ := values(); !reflect.DeepEqual(got, []any{nil, nil, ""}) {
		t.Errorf("values with stale files = %v, want nil, nil and an empty window", got)
	}
	write(themePath, "nord\n")
	write(targetPath, "workspace\n")
	write(windowPath, "kitty ssh {host}\n")
	if got, _ := values(); !reflect.DeepEqual(got, []any{"nord", "workspace", "kitty ssh {host}"}) {
		t.Errorf("values with good files = %v", got)
	}
	// A directory at a legacy path is a problem, not a file to remove:
	// it leaves the list and is named.
	if err := os.Remove(themePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(themePath, 0o755); err != nil {
		t.Fatal(err)
	}
	got, problems := values()
	if len(got) != 2 || !reflect.DeepEqual(problems, []string{"cannot read ~/state/sasash/theme: it is a directory — left as is"}) {
		t.Errorf("values with a directory = %v, problems %q; want two entries and the directory named", got, problems)
	}
}

func TestTilde(t *testing.T) {
	t.Parallel()
	if got := tilde("/home/u", "/home/u/.config/sasash/config.toml"); got != "~/.config/sasash/config.toml" {
		t.Errorf("tilde() = %q", got)
	}
	if got := tilde("/home/u", "/etc/x"); got != "/etc/x" {
		t.Errorf("tilde() outside home = %q, want unchanged", got)
	}
}
