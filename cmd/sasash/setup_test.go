// Internal test package, like main_test.go: main packages cannot be
// imported, so an external _test package has no access to setupHome.
package main

import (
	"bytes"
	"os"
	"path/filepath"
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
		theme      string
		wantStdout []string
		wantLine   string // in config.toml afterwards
	}{
		{name: "fresh template", wantStdout: []string{"create ~/.config/sasash/config.toml"}, wantLine: "# theme = \"gruvbox\"\n"},
		{name: "seeded theme is announced and live", theme: "nord",
			wantStdout: []string{"create ~/.config/sasash/config.toml", `with theme = "nord" carried over from ~/.local/state/sasash/theme`},
			wantLine:   "theme = \"nord\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			env := testEnv(home)
			env.Theme = tt.theme
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
			// The rest is the commented template, and a second run has nothing to do.
			if !strings.HasPrefix(string(data), config.Header) {
				t.Errorf("config.toml does not start with the template header:\n%s", data)
			}
			stdout.Reset()
			if got := setupHome(env, true, strings.NewReader(""), &stdout, &stderr); got != 0 || !strings.Contains(stdout.String(), "already set up") {
				t.Errorf("second setupHome() = %v, %q; want 0 and already set up", got, stdout.String())
			}
		})
	}
}

func TestSetupEnv(t *testing.T) {
	// Not parallel: themeStatePath reads XDG_STATE_HOME via t.Setenv.
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	getenv := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return filepath.Join(home, "cfg")
		}
		return ""
	}
	write := func(v string) {
		t.Helper()
		p := themeStatePath(home)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	wantConfig := filepath.Join(home, "cfg", "sasash", "config.toml")
	if got := setupEnv(home, getenv); got != (setup.Env{Home: home, Config: wantConfig}) {
		t.Errorf("setupEnv() without state = %+v, want no theme and %q", got, wantConfig)
	}
	write("solarized\n")
	if got := setupEnv(home, getenv).Theme; got != "" {
		t.Errorf("setupEnv().Theme with a stale state = %q, want empty", got)
	}
	write("nord\n")
	if got := setupEnv(home, getenv).Theme; got != "nord" {
		t.Errorf("setupEnv().Theme = %q, want nord", got)
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
