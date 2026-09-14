package setup_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/config"
	"github.com/nadavushka-dev/sasash/internal/setup"
)

// writeConfig creates ~/.ssh/config under home with the given content.
func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) failed: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(config) failed: %v", err)
	}
}

// env is the Env the tests use: config.toml under home's .config, where
// the cmd layer puts it without XDG_CONFIG_HOME.
func env(home string) setup.Env {
	return setup.Env{Home: home, Config: filepath.Join(home, ".config", "sasash", "config.toml")}
}

func readConfig(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		t.Fatalf("ReadFile(config) failed: %v", err)
	}
	return string(data)
}

func TestEnsure_Include(t *testing.T) {
	t.Parallel()
	// "@HOME@" in config/want is replaced by the temp home dir.
	tests := []struct {
		name     string
		config   string // "" with noConfig means ~/.ssh does not exist
		noConfig bool
		want     string
	}{
		{
			name:     "missing ssh dir gets fresh config",
			noConfig: true,
			want:     "Include ~/.ssh/config.d/sasash.conf\n",
		},
		{
			name: "empty config",
			want: "Include ~/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "host star first gets include above it",
			config: "Host *\n  User dev\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\nHost *\n  User dev\n",
		},
		{
			name:   "comments and hosts preserved byte for byte",
			config: "# my config\n\nHost web\n  HostName 192.0.2.10\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\n# my config\n\nHost web\n  HostName 192.0.2.10\n",
		},
		{
			name:   "already included tilde form",
			config: "Include ~/.ssh/config.d/sasash.conf\nHost web\n  HostName 192.0.2.10\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\nHost web\n  HostName 192.0.2.10\n",
		},
		{
			name:   "already included via glob",
			config: "Include ~/.ssh/config.d/*.conf\nHost *\n",
			want:   "Include ~/.ssh/config.d/*.conf\nHost *\n",
		},
		{
			name:   "already included percent d form",
			config: "Include %d/.ssh/config.d/sasash.conf\n",
			want:   "Include %d/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "include inside host block does not count",
			config: "Host web\n  HostName 192.0.2.10\n  Include ~/.ssh/config.d/sasash.conf\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\nHost web\n  HostName 192.0.2.10\n  Include ~/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "include inside match block does not count",
			config: "Match all\nInclude ~/.ssh/config.d/sasash.conf\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\nMatch all\nInclude ~/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "already included relative form",
			config: "Include config.d/sasash.conf\nHost *\n",
			want:   "Include config.d/sasash.conf\nHost *\n",
		},
		{
			name:   "already included absolute form",
			config: "Include @HOME@/.ssh/config.d/sasash.conf\n",
			want:   "Include @HOME@/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "already included lowercase keyword",
			config: "include ~/.ssh/config.d/sasash.conf\n",
			want:   "include ~/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "already included quoted path",
			config: "Include \"~/.ssh/config.d/sasash.conf\"\n",
			want:   "Include \"~/.ssh/config.d/sasash.conf\"\n",
		},
		{
			name:   "already included among several files",
			config: "Include ~/.ssh/other.conf ~/.ssh/config.d/sasash.conf\n",
			want:   "Include ~/.ssh/other.conf ~/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "bare tilde include does not count",
			config: "Include ~\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\nInclude ~\n",
		},
		{
			name:   "include of another file does not count",
			config: "Include ~/.ssh/other.conf\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\nInclude ~/.ssh/other.conf\n",
		},
		{
			name:   "sasash conf mentioned in comment does not count",
			config: "# Include ~/.ssh/config.d/sasash.conf\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\n# Include ~/.ssh/config.d/sasash.conf\n",
		},
		{
			name:   "crlf file keeps crlf",
			config: "Host *\r\n  User dev\r\n",
			want:   "Include ~/.ssh/config.d/sasash.conf\r\nHost *\r\n  User dev\r\n",
		},
		{
			name:   "no trailing newline preserved",
			config: "Host *\n  User dev",
			want:   "Include ~/.ssh/config.d/sasash.conf\nHost *\n  User dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if !tt.noConfig {
				writeConfig(t, home, strings.ReplaceAll(tt.config, "@HOME@", home))
			}
			if err := setup.Ensure(env(home)); err != nil {
				t.Fatalf("Ensure(%q) failed: %v", home, err)
			}
			want := strings.ReplaceAll(tt.want, "@HOME@", home)
			if got := readConfig(t, home); got != want {
				t.Errorf("Ensure() config = %q, want %q", got, want)
			}
		})
	}
}

func TestEnsure_Idempotent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// The nested Include does not count as bootstrapped, so the first run
	// inserts at the top; the second must then recognize its own line.
	writeConfig(t, home, "Host web\n  HostName 192.0.2.10\n  Include ~/.ssh/config.d/sasash.conf\n")

	if err := setup.Ensure(env(home)); err != nil {
		t.Fatalf("first Ensure() failed: %v", err)
	}
	first := readConfig(t, home)
	if err := setup.Ensure(env(home)); err != nil {
		t.Fatalf("second Ensure() failed: %v", err)
	}
	if got := readConfig(t, home); got != first {
		t.Errorf("second Ensure() config = %q, want unchanged %q", got, first)
	}
	st, err := setup.Check(env(home))
	if err != nil {
		t.Fatalf("Check() failed: %v", err)
	}
	if !st.Done() {
		t.Errorf("Check() after Ensure() = %+v, want Done", st)
	}
}

func TestEnsure_ConfCreated(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	if err := setup.Ensure(env(home)); err != nil {
		t.Fatalf("Ensure(%q) failed: %v", home, err)
	}

	path := filepath.Join(home, ".ssh", "config.d", "sasash.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(sasash.conf) failed: %v", err)
	}
	if got, want := string(data), setup.Header+"\n"; got != want {
		t.Errorf("sasash.conf = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(sasash.conf) failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("sasash.conf perm = %o, want %o", got, 0o600)
	}
}

func TestEnsure_ConfNeverOverwritten(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh", "config.d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) failed: %v", dir, err)
	}
	existing := "Host web\n  HostName 192.0.2.10\n"
	path := filepath.Join(dir, "sasash.conf")
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("WriteFile(sasash.conf) failed: %v", err)
	}

	if err := setup.Ensure(env(home)); err != nil {
		t.Fatalf("Ensure(%q) failed: %v", home, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(sasash.conf) failed: %v", err)
	}
	if got := string(data); got != existing {
		t.Errorf("sasash.conf = %q, want untouched %q", got, existing)
	}
}

func TestEnsure_CMDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	if err := setup.Ensure(env(home)); err != nil {
		t.Fatalf("Ensure(%q) failed: %v", home, err)
	}

	info, err := os.Stat(filepath.Join(home, ".ssh", "cm"))
	if err != nil {
		t.Fatalf("Stat(cm) failed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("cm is not a directory")
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("cm perm = %o, want %o", got, 0o700)
	}
}

func TestEnsure_Perms(t *testing.T) {
	t.Parallel()

	t.Run("fresh config is 0600 and ssh dir 0700", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		if err := setup.Ensure(env(home)); err != nil {
			t.Fatalf("Ensure(%q) failed: %v", home, err)
		}
		info, err := os.Stat(filepath.Join(home, ".ssh", "config"))
		if err != nil {
			t.Fatalf("Stat(config) failed: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("config perm = %o, want %o", got, 0o600)
		}
		info, err = os.Stat(filepath.Join(home, ".ssh"))
		if err != nil {
			t.Fatalf("Stat(.ssh) failed: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf(".ssh perm = %o, want %o", got, 0o700)
		}
	})

	t.Run("existing config perm preserved", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeConfig(t, home, "Host *\n")
		path := filepath.Join(home, ".ssh", "config")
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("Chmod(config) failed: %v", err)
		}
		if err := setup.Ensure(env(home)); err != nil {
			t.Fatalf("Ensure(%q) failed: %v", home, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(config) failed: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("config perm = %o, want %o", got, 0o644)
		}
	})
}

func TestEnsure_SymlinkedConfig(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// A config symlinked into a dotfiles repo must stay a symlink; the
	// inserted line must land in the link's target.
	dotfiles := filepath.Join(home, "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) failed: %v", dotfiles, err)
	}
	real := filepath.Join(dotfiles, "ssh_config")
	if err := os.WriteFile(real, []byte("Host *\n  User dev\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) failed: %v", real, err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) failed: %v", sshDir, err)
	}
	link := filepath.Join(sshDir, "config")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink(%q) failed: %v", link, err)
	}

	if err := setup.Ensure(env(home)); err != nil {
		t.Fatalf("Ensure(%q) failed: %v", home, err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat(config) failed: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("config mode = %v, want still a symlink", info.Mode())
	}
	data, err := os.ReadFile(real)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", real, err)
	}
	want := setup.IncludeLine + "\nHost *\n  User dev\n"
	if got := string(data); got != want {
		t.Errorf("Ensure() link target = %q, want %q", got, want)
	}
}

func TestEnsure_Errors(t *testing.T) {
	t.Parallel()

	t.Run("config is a directory", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		dir := filepath.Join(home, ".ssh", "config")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%q) failed: %v", dir, err)
		}
		if err := setup.Ensure(env(home)); err == nil {
			t.Error("Ensure() err = nil, want error")
		}
	})

	t.Run("config dot d is a regular file", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeConfig(t, home, setup.IncludeLine+"\n")
		blocker := filepath.Join(home, ".ssh", "config.d")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) failed: %v", blocker, err)
		}
		if err := setup.Ensure(env(home)); err == nil {
			t.Error("Ensure() err = nil, want error")
		}
	})
}

func TestCheck_Errors(t *testing.T) {
	t.Parallel()

	t.Run("ssh dir unreadable", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("chmod 000 does not stop root")
		}
		home := t.TempDir()
		writeConfig(t, home, setup.IncludeLine+"\n")
		dir := filepath.Join(home, ".ssh")
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatalf("Chmod(%q) failed: %v", dir, err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if _, err := setup.Check(env(home)); err == nil {
			t.Error("Check() err = nil, want error")
		}
	})

	t.Run("config dot d is a regular file", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeConfig(t, home, setup.IncludeLine+"\n")
		blocker := filepath.Join(home, ".ssh", "config.d")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) failed: %v", blocker, err)
		}
		if _, err := setup.Check(env(home)); err == nil {
			t.Error("Check() err = nil, want error")
		}
	})
}

func TestCheck_States(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, home string)
		want  setup.Status
	}{
		{
			name:  "fresh home",
			setup: func(t *testing.T, home string) {},
			want:  setup.Status{},
		},
		{
			name: "include only",
			setup: func(t *testing.T, home string) {
				writeConfig(t, home, setup.IncludeLine+"\n")
			},
			want: setup.Status{IncludePresent: true},
		},
		{
			name: "config without include",
			setup: func(t *testing.T, home string) {
				writeConfig(t, home, "Host *\n")
			},
			want: setup.Status{},
		},
		{
			name: "cm exists as file does not count",
			setup: func(t *testing.T, home string) {
				dir := filepath.Join(home, ".ssh")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("MkdirAll(%q) failed: %v", dir, err)
				}
				if err := os.WriteFile(filepath.Join(dir, "cm"), nil, 0o600); err != nil {
					t.Fatalf("WriteFile(cm) failed: %v", err)
				}
			},
			want: setup.Status{},
		},
		{
			name: "fully set up",
			setup: func(t *testing.T, home string) {
				cfg := env(home).Config
				if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(cfg, []byte(config.Template()), 0o644); err != nil {
					t.Fatalf("WriteFile(config.toml): %v", err)
				}
				if err := setup.Ensure(env(home)); err != nil {
					t.Fatalf("Ensure(%q) failed: %v", home, err)
				}
			},
			want: setup.Status{IncludePresent: true, ConfPresent: true, CMDirPresent: true, ConfigPresent: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			tt.setup(t, home)
			got, err := setup.Check(env(home))
			if err != nil {
				t.Fatalf("Check(%q) failed: %v", home, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Check() = %+v, want %+v", got, tt.want)
			}
			if gotDone, wantDone := got.Done(), reflect.DeepEqual(tt.want, setup.Status{IncludePresent: true, ConfPresent: true, CMDirPresent: true, ConfigPresent: true}); gotDone != wantDone {
				t.Errorf("Done() = %v, want %v", gotDone, wantDone)
			}
		})
	}
}

func TestEnsure_ConfigToml(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		existing string // pre-existing config.toml content, "" for none
		want     string
	}{
		{name: "fresh file is the commented template", want: config.Template()},
		{name: "existing file untouched", existing: "# mine\n", want: "# mine\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			e := env(home)
			if tt.existing != "" {
				writeFile(t, e.Config, tt.existing)
			}
			before, err := setup.Check(e)
			if err != nil {
				t.Fatalf("Check() before: %v", err)
			}
			if before.ConfigPresent != (tt.existing != "") {
				t.Errorf("Check().ConfigPresent before = %v, want %v", before.ConfigPresent, tt.existing != "")
			}
			if err := setup.Ensure(e); err != nil {
				t.Fatalf("Ensure() = %v", err)
			}
			got, err := os.ReadFile(e.Config)
			if err != nil {
				t.Fatalf("ReadFile(config.toml): %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("config.toml =\n%s\nwant\n%s", got, tt.want)
			}
			after, err := setup.Check(e)
			if err != nil || !after.ConfigPresent || !after.Done() {
				t.Errorf("Check() after = %+v, %v; want ConfigPresent and Done", after, err)
			}
		})
	}
}

// writeFile creates path with content, directories included.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestEnsure_MovesLegacy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    any    // the Legacy entry's Value; nil means nothing to keep
		existing string // config.toml before, "" for none
		wantLine string // a line config.toml must hold afterwards
		notLine  string // a line it must not hold
	}{
		{name: "a theme is written live", value: "nord", wantLine: "theme = \"nord\"\n", notLine: "# theme ="},
		{name: "nothing to keep is removed without a write", value: nil, wantLine: "# theme = \"gruvbox\"\n", notLine: "\ntheme ="},
		{name: "a live line the user set wins", value: "nord", existing: "theme = \"mocha\"\n", wantLine: "theme = \"mocha\"\n", notLine: "nord"},
		{name: "a commented line is revived", value: "nord", existing: config.Template(), wantLine: "theme = \"nord\"\n", notLine: "# theme ="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			e := env(home)
			old := filepath.Join(home, "state", "theme")
			writeFile(t, old, "whatever\n")
			e.Legacy = []setup.Legacy{{Path: old, Key: "theme", Value: tt.value}, {Path: filepath.Join(home, "state", "absent"), Key: "editor", Value: "vi"}}
			if tt.existing != "" {
				writeFile(t, e.Config, tt.existing)
			}
			before, err := setup.Check(e)
			if err != nil {
				t.Fatalf("Check() before: %v", err)
			}
			if !reflect.DeepEqual(before.Legacy, []string{old}) || before.Done() {
				t.Errorf("Check() before = %+v; want the one present legacy file listed and not Done", before)
			}
			if err := setup.Ensure(e); err != nil {
				t.Fatalf("Ensure() = %v", err)
			}
			data, err := os.ReadFile(e.Config)
			if err != nil {
				t.Fatalf("ReadFile(config.toml): %v", err)
			}
			if !strings.Contains(string(data), tt.wantLine) || strings.Contains(string(data), tt.notLine) {
				t.Errorf("config.toml =\n%s\nwant %q and not %q", data, tt.wantLine, tt.notLine)
			}
			if _, err := os.Lstat(old); !os.IsNotExist(err) {
				t.Errorf("legacy file still exists after Ensure (err %v)", err)
			}
			after, err := setup.Check(e)
			if err != nil || len(after.Legacy) != 0 || !after.Done() {
				t.Errorf("Check() after = %+v, %v; want no legacy and Done", after, err)
			}
			// A second run has nothing to move and changes nothing.
			if err := setup.Ensure(e); err != nil {
				t.Fatalf("second Ensure() = %v", err)
			}
			if again, _ := os.ReadFile(e.Config); string(again) != string(data) {
				t.Errorf("second Ensure() changed config.toml:\n%s", again)
			}
		})
	}
}

func TestEnsure_ConfigTomlErrors(t *testing.T) {
	t.Parallel()
	t.Run("a file at the config dir blocks the write", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		e := env(home)
		writeFile(t, filepath.Dir(e.Config), "x")
		if err := setup.Ensure(e); err == nil {
			t.Errorf("Ensure() = nil, want an error")
		}
		if _, err := setup.Check(e); err == nil {
			t.Errorf("Check() = nil, want an error")
		}
	})
	t.Run("a legacy path under a file is a check error", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		e := env(home)
		blocker := filepath.Join(home, "blocker")
		writeFile(t, blocker, "x")
		e.Legacy = []setup.Legacy{{Path: filepath.Join(blocker, "theme"), Key: "theme", Value: "nord"}}
		if _, err := setup.Check(e); err == nil {
			t.Errorf("Check() = nil, want the lstat error")
		}
		if err := setup.Ensure(e); err == nil {
			t.Errorf("Ensure() = nil, want the lstat error")
		}
	})
	t.Run("a config.toml the parser rejects keeps the legacy file", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		e := env(home)
		writeFile(t, e.Config, "[ui]\n")
		old := filepath.Join(home, "state", "theme")
		writeFile(t, old, "nord\n")
		e.Legacy = []setup.Legacy{{Path: old, Key: "theme", Value: "nord"}}
		if err := setup.Ensure(e); err == nil {
			t.Fatalf("Ensure() = nil, want the parse error")
		}
		if _, err := os.Lstat(old); err != nil {
			t.Errorf("legacy file removed although its value was not written: %v", err)
		}
	})
}
