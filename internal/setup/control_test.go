package setup_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/setup"
)

func TestManagedBlock_Template(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		alias, hostName, user, port string
		want                        string
	}{
		{name: "full", alias: "edge", hostName: "192.0.2.99", user: "root", port: "2222",
			want: "Host edge\n  HostName 192.0.2.99\n  User root\n  Port 2222\n  ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n"},
		{name: "minimal", alias: "a1", hostName: "192.0.2.5",
			want: "Host a1\n  HostName 192.0.2.5\n  ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := setup.ManagedBlock(tt.alias, tt.hostName, tt.user, tt.port); got != tt.want {
				t.Errorf("ManagedBlock() = %q, want %q", got, tt.want)
			}
		})
	}
}

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sasash.conf")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingControl_Cases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		conf    string
		alias   string
		want    []string
		wantErr error
	}{
		{name: "all missing", conf: "Host old\n  HostName 192.0.2.7\n", alias: "old",
			want: []string{"ControlMaster auto", "ControlPath ~/.ssh/cm/%C", "ControlPersist 30m"}},
		{name: "one missing, another value kept", conf: "Host old\n  HostName 192.0.2.7\n  controlmaster no\n  ControlPath ~/.ssh/other/%C\n", alias: "old",
			want: []string{"ControlPersist 30m"}},
		{name: "complete", conf: "Host old\n  HostName 192.0.2.7\n  ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n", alias: "old"},
		{name: "not managed", conf: "Host old\n  HostName 192.0.2.7\n", alias: "other", wantErr: setup.ErrNotManaged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := setup.MissingControl(writeConf(t, tt.conf), tt.alias)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("MissingControl() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MissingControl() = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		if _, err := setup.MissingControl(filepath.Join(t.TempDir(), "none.conf"), "x"); err == nil {
			t.Errorf("MissingControl() on a missing file = nil, want an error")
		}
	})
}

func TestEnsureControl_WritesOnlyTheMissingLines(t *testing.T) {
	t.Parallel()
	// Tab indent, CRLF-free, an unknown directive, a second block after:
	// every other byte survives, the added lines take the block's indent
	// and sit under the Host line in the template's order.
	before := "# header\nHost old\n\tHostName 192.0.2.7\n\tUnknownDirective kept\n\tControlPath ~/.ssh/other/%C\n\nHost next\n  HostName 192.0.2.8\n"
	path := writeConf(t, before)
	added, err := setup.EnsureControl(path, "old")
	if err != nil {
		t.Fatalf("EnsureControl() = %v", err)
	}
	if want := []string{"ControlMaster auto", "ControlPersist 30m"}; !reflect.DeepEqual(added, want) {
		t.Errorf("EnsureControl() added = %v, want %v", added, want)
	}
	got, _ := os.ReadFile(path)
	want := "# header\nHost old\n\tControlMaster auto\n\tControlPersist 30m\n\tHostName 192.0.2.7\n\tUnknownDirective kept\n\tControlPath ~/.ssh/other/%C\n\nHost next\n  HostName 192.0.2.8\n"
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("perm = %v, want the file's 0640 kept", fi.Mode().Perm())
	}
	// Idempotent: a complete block writes nothing.
	fi1, _ := os.Stat(path)
	added, err = setup.EnsureControl(path, "old")
	if err != nil || added != nil {
		t.Errorf("second EnsureControl() = %v, %v; want nothing added", added, err)
	}
	fi2, _ := os.Stat(path)
	if fi1.ModTime() != fi2.ModTime() {
		t.Errorf("a complete block should not be rewritten")
	}
}

func TestEnsureControl_Errors(t *testing.T) {
	t.Parallel()
	if _, err := setup.EnsureControl(writeConf(t, "Host old\n  HostName 192.0.2.7\n"), "other"); !errors.Is(err, setup.ErrNotManaged) {
		t.Errorf("EnsureControl() on an unknown alias = %v, want ErrNotManaged", err)
	}
	if _, err := setup.EnsureControl(filepath.Join(t.TempDir(), "none.conf"), "x"); err == nil {
		t.Errorf("EnsureControl() on a missing file = nil, want an error")
	}
	// The added lines are exactly the trio's spellings.
	path := writeConf(t, "Host old\n  HostName 192.0.2.7\n")
	added, _ := setup.EnsureControl(path, "old")
	got, _ := os.ReadFile(path)
	for _, l := range added {
		if !strings.Contains(string(got), "  "+l+"\n") {
			t.Errorf("file lacks the added line %q:\n%s", l, got)
		}
	}
}
