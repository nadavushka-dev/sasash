package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/config"
)

func TestPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		xdg  string
		want string
	}{
		{name: "default under home", want: "/home/u/.config/sasash/config.toml"},
		{name: "XDG_CONFIG_HOME moves it", xdg: "/x/cfg", want: "/x/cfg/sasash/config.toml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string {
				if k == "XDG_CONFIG_HOME" {
					return tt.xdg
				}
				return ""
			}
			if got := config.Path("/home/u", getenv); got != tt.want {
				t.Errorf("Path() = %q, want %q", got, tt.want)
			}
		})
	}
}

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		content   string
		want      config.Config
		wantWarns []string
		wantErr   string
	}{
		{name: "template sets nothing", content: config.Template(), want: config.Config{Present: true}},
		{name: "every key", content: "theme = \"nord\"\neditor = 'code -w'\nshow_hidden = true\n",
			want: config.Config{Present: true, Theme: "nord", Editor: "code -w", ShowHidden: true}},
		{name: "comments blanks and crlf", content: "# top\r\n\r\ntheme = \"mocha\" # trailing\r\n",
			want: config.Config{Present: true, Theme: "mocha"}},
		{name: "unknown key warns and the rest applies", content: "font_size = \"12\"\ntheme = \"nord\"\n",
			want:      config.Config{Present: true, Theme: "nord"},
			wantWarns: []string{`config.toml:1: unknown key "font_size" (ignored)`}},
		{name: "wrong type warns and the rest applies", content: "show_hidden = \"yes\"\ntheme = true\neditor = \"vim\"\n",
			want: config.Config{Present: true, Editor: "vim"},
			wantWarns: []string{
				"config.toml:1: show_hidden must be true or false (ignored)",
				"config.toml:2: theme must be a quoted string (ignored)",
			}},
		{name: "table rejected", content: "[ui]\ntheme = \"nord\"\n", wantErr: "config.toml:1: tables are not used"},
		{name: "no equals", content: "theme\n", wantErr: `config.toml:1: expected key = value, got "theme"`},
		{name: "dotted key", content: "ui.theme = \"nord\"\n", wantErr: `config.toml:1: unsupported key "ui.theme"`},
		{name: "bare value", content: "theme = nord\n", wantErr: "config.toml:1: unsupported value"},
		{name: "trailing junk", content: "theme = \"nord\" extra\n", wantErr: `config.toml:1: unexpected trailing content "extra"`},
		{name: "duplicate key", content: "theme = \"nord\"\n\ntheme = \"mocha\"\n", wantErr: `config.toml:3: duplicate key "theme" (first on line 1)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warns, err := config.Load(write(t, tt.content))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() err = %v, want %q", err, tt.wantErr)
				}
				if got != (config.Config{}) {
					t.Errorf("Load() on error = %+v, want the zero Config", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() err = %v", err)
			}
			if got != tt.want {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
			if strings.Join(warns, "|") != strings.Join(tt.wantWarns, "|") {
				t.Errorf("Load() warns = %q, want %q", warns, tt.wantWarns)
			}
		})
	}
}

func TestLoad_MissingAndUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, warns, err := config.Load(filepath.Join(dir, "none.toml"))
	if err != nil || warns != nil || got != (config.Config{}) {
		t.Errorf("Load(missing) = %+v, %v, %v; want zero, nil, nil", got, warns, err)
	}
	if _, _, err := config.Load(dir); err == nil {
		t.Errorf("Load(directory) err = nil, want an error")
	}
}

func TestTemplate_ListsEveryKeyCommented(t *testing.T) {
	t.Parallel()
	tmpl := config.Template()
	if !strings.HasPrefix(tmpl, config.Header) {
		t.Errorf("Template() does not start with Header:\n%s", tmpl)
	}
	for _, k := range config.Keys {
		if !strings.Contains(tmpl, "\n# "+k.Doc+"\n# "+k.Name+" = "+k.Default+"\n") {
			t.Errorf("Template() lacks the doc line and commented default for %s:\n%s", k.Name, tmpl)
		}
		if strings.Contains(tmpl, "\n"+k.Name+" =") {
			t.Errorf("Template() has a live %s line, want every key commented out", k.Name)
		}
	}
}

func TestApply(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		data    string
		key     string
		value   any
		want    string
		wantErr string
	}{
		{name: "live line rewritten keeping indent and comment",
			data: "# hdr\n  theme = 'nord'   # mine\neditor = \"vim\"\n", key: "theme", value: "mocha",
			want: "# hdr\n  theme = \"mocha\"   # mine\neditor = \"vim\"\n"},
		{name: "crlf kept", data: "theme = \"nord\"\r\nshow_hidden = false\r\n", key: "show_hidden", value: true,
			want: "theme = \"nord\"\r\nshow_hidden = true\r\n"},
		{name: "crlf file appended in its own style", data: "editor = \"vim\"\r\n", key: "theme", value: "nord",
			want: "editor = \"vim\"\r\ntheme = \"nord\"\r\n"},
		{name: "crlf file without a final newline appended in its own style", data: "editor = \"vim\"\r\nshow_hidden = true", key: "theme", value: "nord",
			want: "editor = \"vim\"\r\nshow_hidden = true\r\ntheme = \"nord\"\r\n"},
		{name: "indented template line revived with its indent", data: "  # theme = \"gruvbox\"\n", key: "theme", value: "nord",
			want: "  theme = \"nord\"\n"},
		{name: "prose starting with the key is not revived", data: "# editor = whatever you like\n# doc\n# editor = \"\"\n", key: "editor", value: "code",
			want: "# editor = whatever you like\n# doc\neditor = \"code\"\n"},
		{name: "commented value of the wrong kind is not revived", data: "# show_hidden = \"yes\"\n", key: "show_hidden", value: true,
			want: "# show_hidden = \"yes\"\nshow_hidden = true\n"},
		{name: "commented line with a trailing comment is revived", data: "# theme = \"gruvbox\"  # default\n", key: "theme", value: "nord",
			want: "theme = \"nord\"\n"},
		{name: "template line revived in place",
			data: "# doc\n# theme = \"gruvbox\"\n\n# doc2\n# show_hidden = false\n", key: "show_hidden", value: true,
			want: "# doc\n# theme = \"gruvbox\"\n\n# doc2\nshow_hidden = true\n"},
		{name: "live line beats a commented one", data: "# theme = \"gruvbox\"\ntheme = \"nord\"\n", key: "theme", value: "mocha",
			want: "# theme = \"gruvbox\"\ntheme = \"mocha\"\n"},
		{name: "appended after a final newline", data: "editor = \"vim\"\n", key: "theme", value: "nord",
			want: "editor = \"vim\"\ntheme = \"nord\"\n"},
		{name: "appended after a missing final newline", data: "editor = \"vim\"", key: "theme", value: "nord",
			want: "editor = \"vim\"\ntheme = \"nord\"\n"},
		{name: "appended to empty", data: "", key: "theme", value: "nord", want: "theme = \"nord\"\n"},
		{name: "a plain comment mentioning the key is not revived", data: "# set theme = to taste\n", key: "theme", value: "nord",
			want: "# set theme = to taste\ntheme = \"nord\"\n"},
		{name: "string value quoted and escaped", data: "", key: "editor", value: `c:\ed "x"`,
			want: "editor = \"c:\\\\ed \\\"x\\\"\"\n"},
		{name: "false rendered", data: "show_hidden = true\n", key: "show_hidden", value: false, want: "show_hidden = false\n"},
		{name: "commented line at EOF without a newline revived as it stands", data: "# theme = \"gruvbox\"", key: "theme", value: "nord",
			want: "theme = \"nord\""},
		{name: "a commented dotted key is not revived", data: "# ui.theme = \"x\"\n", key: "theme", value: "nord",
			want: "# ui.theme = \"x\"\ntheme = \"nord\"\n"},
		{name: "a commented unknown key is not revived", data: "# font = \"x\"\n", key: "theme", value: "nord",
			want: "# font = \"x\"\ntheme = \"nord\"\n"},
		{name: "unknown key", data: "", key: "font_size", value: "12", wantErr: `unknown key "font_size"`},
		{name: "string for a bool key", data: "", key: "show_hidden", value: "yes", wantErr: "show_hidden takes true or false, not a string"},
		{name: "bool for a string key", data: "", key: "theme", value: true, wantErr: "theme takes a quoted string, not a bool"},
		{name: "unsupported value type", data: "", key: "theme", value: 3, wantErr: "unsupported value int"},
		{name: "unreadable file left alone", data: "[ui]\n", key: "theme", value: "nord", wantErr: "config.toml:1: tables are not used"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.Apply(tt.data, tt.key, tt.value)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Apply() err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply() err = %v", err)
			}
			if got != tt.want {
				t.Errorf("Apply() =\n%q\nwant\n%q", got, tt.want)
			}
			// What Apply wrote, Load reads back — the literal is the parser's.
			cfg, warns, err := config.Load(write(t, got))
			if err != nil || warns != nil {
				t.Fatalf("Load(Apply()) = %+v, %v, %v", cfg, warns, err)
			}
			switch v := tt.value.(type) {
			case string:
				if tt.key == "theme" && cfg.Theme != v || tt.key == "editor" && cfg.Editor != v {
					t.Errorf("Load(Apply()) = %+v, want %s = %q", cfg, tt.key, v)
				}
			case bool:
				if cfg.ShowHidden != v {
					t.Errorf("Load(Apply()) = %+v, want show_hidden = %v", cfg, v)
				}
			}
		})
	}
}

func TestSet(t *testing.T) {
	t.Parallel()
	t.Run("missing file starts from the template", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "sasash", "config.toml")
		if err := config.Set(path, "theme", "nord"); err != nil {
			t.Fatalf("Set() err = %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		want := strings.Replace(config.Template(), "# theme = \"gruvbox\"\n", "theme = \"nord\"\n", 1)
		if string(got) != want {
			t.Errorf("Set() on a missing file wrote\n%s\nwant the template with the theme line live:\n%s", got, want)
		}
	})
	t.Run("re-reads so a hand edit wins", func(t *testing.T) {
		t.Parallel()
		path := write(t, "theme = \"nord\"\n")
		// A hand edit lands between the TUI's load and its write.
		if err := os.WriteFile(path, []byte("editor = \"vim\"\ntheme = \"nord\"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := config.Set(path, "theme", "mocha"); err != nil {
			t.Fatalf("Set() err = %v", err)
		}
		got, _ := os.ReadFile(path)
		if want := "editor = \"vim\"\ntheme = \"mocha\"\n"; string(got) != want {
			t.Errorf("Set() wrote %q, want %q (the hand-added editor line kept)", got, want)
		}
		if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".sasash-*")); len(left) != 0 {
			t.Errorf("temp files left behind: %v", left)
		}
	})
	t.Run("unreadable file is refused and untouched", func(t *testing.T) {
		t.Parallel()
		path := write(t, "[ui]\n")
		if err := config.Set(path, "theme", "nord"); err == nil {
			t.Fatalf("Set() on a malformed file err = nil, want the parse error")
		}
		got, _ := os.ReadFile(path)
		if string(got) != "[ui]\n" {
			t.Errorf("Set() changed a file it could not read: %q", got)
		}
	})
	t.Run("directory in place of the file errors", func(t *testing.T) {
		t.Parallel()
		if err := config.Set(t.TempDir(), "theme", "nord"); err == nil {
			t.Errorf("Set(directory) err = nil, want an error")
		}
	})
}

func TestWriteAtomic_KeepsMode(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root ignores file modes")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("theme = \"nord\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := config.Set(path, "theme", "mocha"); err != nil {
		t.Fatalf("Set() err = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode after Set() = %o, want the file's own 0600", got)
	}
	fresh := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteAtomic(fresh, "x"); err != nil {
		t.Fatalf("WriteAtomic(fresh) err = %v", err)
	}
	if info, _ := os.Stat(fresh); info.Mode().Perm() != 0o644 {
		t.Errorf("mode of a new file = %o, want 0644", info.Mode().Perm())
	}
}

func TestSet_ConcurrentWritesKeepEveryKey(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); errs <- config.Set(path, "theme", "nord") }()
		go func() { defer wg.Done(); errs <- config.Set(path, "show_hidden", true) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Set() = %v", err)
		}
	}
	got, warns, err := config.Load(path)
	if err != nil || warns != nil || got.Theme != "nord" || !got.ShowHidden {
		t.Errorf("Load() after concurrent Sets = %+v, %v, %v; want both keys live", got, warns, err)
	}
}

func TestWriteAtomic_Errors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := config.WriteAtomic(filepath.Join(blocker, "config.toml"), "x"); err == nil {
		t.Errorf("WriteAtomic(under a file) err = nil, want the mkdir error")
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if err := config.WriteAtomic(filepath.Join(locked, "config.toml"), "x"); err == nil {
		t.Errorf("WriteAtomic(read-only dir) err = nil, want the create error")
	}
}

func TestWriteAtomic_ThroughSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "config.toml")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(real, []byte("theme = \"nord\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := config.Set(link, "theme", "mocha"); err != nil {
		t.Fatalf("Set() err = %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Lstat(link) = %v, %v; want the symlink kept", fi, err)
	}
	got, _ := os.ReadFile(real)
	if want := "theme = \"mocha\"\n"; string(got) != want {
		t.Errorf("target holds %q, want %q", got, want)
	}
}

// TestKeys_Documented keeps docs/configuration.md and the registry in
// step: every key has a row on the page, and the page names no key the
// registry lacks.
func TestKeys_Documented(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatalf("ReadFile(docs/configuration.md): %v", err)
	}
	page := string(data)
	for _, k := range config.Keys {
		if !strings.Contains(page, "| `"+k.Name+"` |") {
			t.Errorf("docs/configuration.md has no row for %s", k.Name)
		}
		if !strings.Contains(page, "`"+k.Default+"`") {
			t.Errorf("docs/configuration.md does not show %s's default %s", k.Name, k.Default)
		}
	}
	for _, l := range strings.Split(page, "\n") {
		if !strings.HasPrefix(l, "| `") {
			continue
		}
		name := strings.SplitN(l, "`", 3)[1]
		found := false
		for _, k := range config.Keys {
			found = found || k.Name == name
		}
		if !found {
			t.Errorf("docs/configuration.md documents %q, which config.Keys lacks", name)
		}
	}
}
