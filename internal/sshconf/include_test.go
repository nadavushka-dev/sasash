package sshconf_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// writeTree writes files (paths relative to home) into a fake home directory.
func writeTree(t *testing.T, home string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll(%q) err = %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) err = %v", path, err)
		}
	}
}

func hostNames(hosts []sshconf.Host) []string {
	out := make([]string, len(hosts))
	for i := range hosts {
		out[i] = hosts[i].Name()
	}
	return out
}

func TestIncludeHosts_Order(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string // relative to the fake home
		want  []string          // aliases in ssh evaluation order
	}{
		{
			"no includes",
			map[string]string{
				".ssh/config": "Host a\n  HostName 192.0.2.1\nHost b\n",
			},
			[]string{"a", "b"},
		},
		{
			"include spliced in place",
			map[string]string{
				".ssh/config":   "Host a\n  HostName 192.0.2.1\nInclude sub.conf\nHost b\n",
				".ssh/sub.conf": "Host s1\nHost s2\n",
			},
			[]string{"a", "s1", "s2", "b"},
		},
		{
			"tilde pattern",
			map[string]string{
				".ssh/config": "Include ~/extra.conf\nHost a\n",
				"extra.conf":  "Host e\n",
			},
			[]string{"e", "a"},
		},
		{
			"glob expands sorted and skips directories",
			map[string]string{
				".ssh/config":                    "Include config.d/*\n",
				".ssh/config.d/10-first.conf":    "Host first\n",
				".ssh/config.d/20-second.conf":   "Host second\n",
				".ssh/config.d/subdir/.gitkeep":  "",
				".ssh/config.d/subdir/deep.conf": "Host unreachable\n",
			},
			[]string{"first", "second"},
		},
		{
			"missing file and empty glob silently skipped",
			map[string]string{
				".ssh/config": "Include nope.conf\nInclude empty.d/*.conf\nHost a\n",
			},
			[]string{"a"},
		},
		{
			"several patterns on one line",
			map[string]string{
				".ssh/config":   "Include one.conf two.conf\n",
				".ssh/one.conf": "Host one\n",
				".ssh/two.conf": "Host two\n",
			},
			[]string{"one", "two"},
		},
		{
			"cycle loads each file once",
			map[string]string{
				".ssh/config":    "Host a\nInclude loop.conf\n",
				".ssh/loop.conf": "Host loop\nInclude config\n",
			},
			[]string{"a", "loop"},
		},
		{
			"file included twice loads once",
			map[string]string{
				".ssh/config":      "Include shared.conf\nInclude shared.conf\n",
				".ssh/shared.conf": "Host shared\n",
			},
			[]string{"shared"},
		},
		{
			"nested includes depth first",
			map[string]string{
				".ssh/config":    "Include mid.conf\nHost root\n",
				".ssh/mid.conf":  "Host mid\nInclude leaf.conf\n",
				".ssh/leaf.conf": "Host leaf\n",
			},
			[]string{"mid", "leaf", "root"},
		},
		{
			"keyword is case-insensitive",
			map[string]string{
				".ssh/config":     "INCLUDE upper.conf\n",
				".ssh/upper.conf": "Host upper\n",
			},
			[]string{"upper"},
		},
		{
			"malformed glob treated as no match",
			map[string]string{
				".ssh/config": "Include conf[.d/*\nHost a\n",
			},
			[]string{"a"},
		},
		{
			"quoted pattern",
			map[string]string{
				".ssh/config":      "Include \"quoted.conf\"\n",
				".ssh/quoted.conf": "Host quoted\n",
			},
			[]string{"quoted"},
		},
		{
			"equals separator",
			map[string]string{
				".ssh/config":   "Include=sub.conf\n",
				".ssh/sub.conf": "Host sub\n",
			},
			[]string{"sub"},
		},
		{
			"duplicate alias kept in order for first-value-wins",
			map[string]string{
				".ssh/config":     "Include first.conf\nHost dup\n  HostName 192.0.2.9\n",
				".ssh/first.conf": "Host dup\n  HostName 192.0.2.1\n",
			},
			[]string{"dup", "dup"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			writeTree(t, home, tt.files)

			hosts, err := sshconf.IncludeHosts(filepath.Join(home, ".ssh", "config"), home)
			if err != nil {
				t.Fatalf("IncludeHosts() err = %v", err)
			}
			if got := hostNames(hosts); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("IncludeHosts() hosts = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIncludeHosts_HostsKeepTheirFileLink(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeTree(t, home, map[string]string{
		".ssh/config":   "Include sub.conf\nHost a\n  HostName 192.0.2.1\n",
		".ssh/sub.conf": "Host s\n  HostName 198.51.100.7\n",
	})

	hosts, err := sshconf.IncludeHosts(filepath.Join(home, ".ssh", "config"), home)
	if err != nil {
		t.Fatalf("IncludeHosts() err = %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("IncludeHosts() returned %d hosts, want 2", len(hosts))
	}
	if got, _ := hosts[0].Get("HostName"); got != "198.51.100.7" {
		t.Errorf("included host HostName = %q, want %q", got, "198.51.100.7")
	}
	if got, _ := hosts[1].Get("HostName"); got != "192.0.2.1" {
		t.Errorf("root host HostName = %q, want %q", got, "192.0.2.1")
	}
}

func TestIncludeHosts_EmptyHomeUsesUserHomeDir(t *testing.T) {
	t.Parallel()

	// The root path is absolute and has no Include patterns, so home is only
	// consulted for the default lookup — which must succeed and not affect
	// the result.
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"config": "Host a\n"})

	hosts, err := sshconf.IncludeHosts(filepath.Join(dir, "config"), "")
	if err != nil {
		t.Fatalf("IncludeHosts() err = %v", err)
	}
	if got := hostNames(hosts); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("IncludeHosts() hosts = %v, want %v", got, []string{"a"})
	}
}

func TestIncludeHosts_MissingRootErrors(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if _, err := sshconf.IncludeHosts(filepath.Join(home, ".ssh", "config"), home); err == nil {
		t.Error("IncludeHosts() err = nil, want error for missing root")
	}
}

func TestIncludeHosts_AbsolutePattern(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	elsewhere := t.TempDir()
	writeTree(t, elsewhere, map[string]string{"abs.conf": "Host abs\n"})
	writeTree(t, home, map[string]string{
		".ssh/config": "Include " + filepath.Join(elsewhere, "abs.conf") + "\n",
	})

	hosts, err := sshconf.IncludeHosts(filepath.Join(home, ".ssh", "config"), home)
	if err != nil {
		t.Fatalf("IncludeHosts() err = %v", err)
	}
	if got := hostNames(hosts); !reflect.DeepEqual(got, []string{"abs"}) {
		t.Errorf("IncludeHosts() hosts = %v, want %v", got, []string{"abs"})
	}
}

func TestIncludeHosts_UnreadableIncludeErrors(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}

	home := t.TempDir()
	writeTree(t, home, map[string]string{
		".ssh/config":      "Include locked.conf\n",
		".ssh/locked.conf": "Host locked\n",
	})
	if err := os.Chmod(filepath.Join(home, ".ssh", "locked.conf"), 0o000); err != nil {
		t.Fatalf("Chmod() err = %v", err)
	}

	if _, err := sshconf.IncludeHosts(filepath.Join(home, ".ssh", "config"), home); err == nil {
		t.Error("IncludeHosts() err = nil, want error for unreadable include")
	}
}
