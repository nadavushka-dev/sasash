package controlmaster_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/controlmaster"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

const trio = "ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n"

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sasash.conf")
	// 0640 differs from sshconf.DefaultPerm so the preserved-perm check
	// cannot pass by accident.
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func readConf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestTrio_PinnedToSpec(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "SPEC.md"))
	if err != nil {
		t.Fatalf("ReadFile(SPEC.md) failed: %v", err)
	}
	var b strings.Builder
	for _, d := range controlmaster.Trio {
		b.WriteString("  " + d.Keyword + " " + d.Value + "\n")
	}
	if !strings.Contains(string(data), b.String()) {
		t.Errorf("Trio rendered as %q not found in SPEC.md's managed-entry template", b.String())
	}
}

func TestEnable_ExactBytes(t *testing.T) {
	t.Parallel()
	all := controlmaster.Trio[:]
	tests := []struct {
		name  string
		in    string
		alias string
		want  string
		added []controlmaster.Directive
	}{
		{
			name:  "minimal saved block",
			in:    "Host web\n  HostName 192.0.2.10\n  User alice\n",
			alias: "web",
			want:  "Host web\n  " + trio + "  HostName 192.0.2.10\n  User alice\n",
			added: all,
		},
		{
			name:  "tab indent follows the block",
			in:    "Host web\n\tHostName 192.0.2.10\n",
			alias: "web",
			want:  "Host web\n\tControlMaster auto\n\tControlPath ~/.ssh/cm/%C\n\tControlPersist 30m\n\tHostName 192.0.2.10\n",
			added: all,
		},
		{
			name:  "CRLF file gets CRLF lines",
			in:    "Host web\r\n  HostName 192.0.2.10\r\n",
			alias: "web",
			want:  "Host web\r\n  ControlMaster auto\r\n  ControlPath ~/.ssh/cm/%C\r\n  ControlPersist 30m\r\n  HostName 192.0.2.10\r\n",
			added: all,
		},
		{
			name:  "host line as unterminated last line",
			in:    "# managed\nHost web",
			alias: "web",
			want:  "# managed\nHost web\n  " + trio,
			added: all,
		},
		{
			name:  "custom ControlPath kept, the other two inserted",
			in:    "Host web\n  HostName 192.0.2.10\n  ControlPath=~/.ssh/s-%r\n",
			alias: "web",
			want:  "Host web\n  ControlMaster auto\n  ControlPersist 30m\n  HostName 192.0.2.10\n  ControlPath=~/.ssh/s-%r\n",
			added: []controlmaster.Directive{controlmaster.Trio[0], controlmaster.Trio[2]},
		},
		{
			name:  "keyword casing does not matter, a commented-out line does",
			in:    "Host web\n  controlmaster AUTO\n  # ControlPath ~/.ssh/cm/%C\n  HostName 192.0.2.10\n",
			alias: "web",
			want:  "Host web\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n  controlmaster AUTO\n  # ControlPath ~/.ssh/cm/%C\n  HostName 192.0.2.10\n",
			added: []controlmaster.Directive{controlmaster.Trio[1], controlmaster.Trio[2]},
		},
		{
			name: "host between foreign blocks, meta run and variants untouched",
			in: "Host a\n\tHostName 192.0.2.1\n  UnknownDirective kept\n\n" +
				"# @sasash path lan 192.0.2.10\n# @sasash forwards adhoc 3000:localhost:3000\nHost web web.example\n  HostName 192.0.2.10\n\n" +
				"# pinned variants, generated — for when you want to force one path\nHost web.lan\n  HostName 192.0.2.10\nHost c\n  HostName 192.0.2.3\n",
			alias: "web",
			want: "Host a\n\tHostName 192.0.2.1\n  UnknownDirective kept\n\n" +
				"# @sasash path lan 192.0.2.10\n# @sasash forwards adhoc 3000:localhost:3000\nHost web web.example\n  " + trio + "  HostName 192.0.2.10\n\n" +
				"# pinned variants, generated — for when you want to force one path\nHost web.lan\n  HostName 192.0.2.10\nHost c\n  HostName 192.0.2.3\n",
			added: all,
		},
		{
			name:  "first block with the alias wins",
			in:    "Host web\n  HostName 192.0.2.10\nHost web\n  HostName 192.0.2.11\n",
			alias: "web",
			want:  "Host web\n  " + trio + "  HostName 192.0.2.10\nHost web\n  HostName 192.0.2.11\n",
			added: all,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, tt.in)
			before, err := controlmaster.Missing(path, tt.alias)
			if err != nil {
				t.Fatalf("Missing() error = %v", err)
			}
			if !reflect.DeepEqual(before, tt.added) {
				t.Errorf("Missing() = %v, want %v", before, tt.added)
			}
			got, err := controlmaster.Enable(path, tt.alias)
			if err != nil {
				t.Fatalf("Enable() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.added) {
				t.Errorf("Enable() = %v, want %v", got, tt.added)
			}
			if out := readConf(t, path); out != tt.want {
				t.Errorf("Enable() wrote %q, want %q", out, tt.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Errorf("perm = %o, want 640", info.Mode().Perm())
			}
			// Idempotent: the block is complete now.
			after, err := controlmaster.Missing(path, tt.alias)
			if err != nil {
				t.Fatalf("Missing() after error = %v", err)
			}
			if after != nil {
				t.Errorf("Missing() after = %v, want nil", after)
			}
			again, err := controlmaster.Enable(path, tt.alias)
			if err != nil {
				t.Fatalf("Enable() again error = %v", err)
			}
			if again != nil {
				t.Errorf("Enable() again = %v, want nil", again)
			}
			if out := readConf(t, path); out != tt.want {
				t.Errorf("Enable() again wrote %q, want %q", out, tt.want)
			}
		})
	}
}

// A complete block means no write at all, not a rewrite of the same
// bytes: a directory nothing can be created in proves it.
func TestEnable_CompleteBlockDoesNotWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sasash.conf")
	const in = "Host web\n  HostName 192.0.2.10\n  " + trio
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	got, err := controlmaster.Enable(path, "web")
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if got != nil {
		t.Errorf("Enable() = %v, want nil", got)
	}
	if out := readConf(t, path); out != in {
		t.Errorf("file = %q, want %q", out, in)
	}
}

func TestEnable_Errors(t *testing.T) {
	t.Parallel()
	const in = "Host web web.example\n  HostName 192.0.2.10\nHost web.lan\n  HostName 192.0.2.10\n"
	tests := []struct {
		name  string
		alias string
		want  string
	}{
		{name: "unknown alias", alias: "db", want: `host "db" not managed by sasash`},
		{name: "second pattern is not the alias", alias: "web.example", want: `host "web.example" not managed by sasash`},
		{name: "empty alias", alias: "", want: `host "" not managed by sasash`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, in)
			if _, err := controlmaster.Missing(path, tt.alias); err == nil || err.Error() != tt.want {
				t.Errorf("Missing() error = %v, want %q", err, tt.want)
			}
			if _, err := controlmaster.Enable(path, tt.alias); err == nil || err.Error() != tt.want {
				t.Errorf("Enable() error = %v, want %q", err, tt.want)
			}
			if out := readConf(t, path); out != in {
				t.Errorf("file = %q, want untouched %q", out, in)
			}
		})
	}
}

func TestEnable_MissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sasash.conf")
	if _, err := controlmaster.Missing(path, "web"); !os.IsNotExist(err) {
		t.Errorf("Missing() error = %v, want not-exist", err)
	}
	if _, err := controlmaster.Enable(path, "web"); !os.IsNotExist(err) {
		t.Errorf("Enable() error = %v, want not-exist", err)
	}
}

func TestEnable_WriteFailureLeavesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sasash.conf")
	const in = "Host web\n  HostName 192.0.2.10\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := controlmaster.Enable(path, "web"); err == nil {
		t.Errorf("Enable() error = nil, want a write failure")
	}
	if out := readConf(t, path); out != in {
		t.Errorf("file = %q, want untouched %q", out, in)
	}
}

// The values land where ssh reads them: a re-parse of the written file
// answers Get with the template's values.
func TestEnable_ReparseSeesTrio(t *testing.T) {
	t.Parallel()
	path := writeConf(t, "Host web\n  HostName 192.0.2.10\n")
	if _, err := controlmaster.Enable(path, "web"); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	f, err := sshconf.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	h, ok := f.Host("web")
	if !ok {
		t.Fatalf("Host(web) not found after Enable")
	}
	for _, d := range controlmaster.Trio {
		if got, _ := h.Get(d.Keyword); got != d.Value {
			t.Errorf("Get(%s) = %q, want %q", d.Keyword, got, d.Value)
		}
	}
	if got, _ := h.Get("HostName"); got != "192.0.2.10" {
		t.Errorf("Get(HostName) = %q, want 192.0.2.10", got)
	}
}
