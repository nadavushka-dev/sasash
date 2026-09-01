package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
)

func TestRunAddr(t *testing.T) {
	t.Parallel()

	config := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.20
Host ubuntu
  HostName 192.0.2.10

Host plain
  HostName 192.0.2.5
  Port 2222
`
	tests := []struct {
		name       string
		args       []string
		dialer     probe.Dialer
		wantCode   int
		wantStdout string   // exact
		wantStderr []string // substrings; nil means stderr must be EMPTY
	}{
		{
			name:       "first path alive",
			args:       []string{"ubuntu"},
			dialer:     fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond),
			wantCode:   0,
			wantStdout: "192.0.2.10\n",
		},
		{
			name:       "first dead prints second",
			args:       []string{"ubuntu"},
			dialer:     fakedial.New().Down("192.0.2.10:22").Up("198.51.100.20:22", 70*time.Millisecond),
			wantCode:   0,
			wantStdout: "198.51.100.20\n",
		},
		{
			name: "config order beats latency",
			args: []string{"ubuntu"},
			dialer: fakedial.New().
				Up("192.0.2.10:22", 50*time.Millisecond).
				Up("198.51.100.20:22", 5*time.Millisecond),
			wantCode:   0,
			wantStdout: "192.0.2.10\n",
		},
		{
			name:       "all paths dead lists each path",
			args:       []string{"ubuntu"},
			dialer:     fakedial.New().Down("192.0.2.10:22").Down("198.51.100.20:22"),
			wantCode:   1,
			wantStdout: "",
			wantStderr: []string{
				`all 2 path(s) to "ubuntu" are down`,
				"lan 192.0.2.10:22:",
				"ts 198.51.100.20:22:",
			},
		},
		{
			name:       "fallback host probes HostName with Port directive",
			args:       []string{"plain"},
			dialer:     fakedial.New().Up("192.0.2.5:2222", 20*time.Millisecond),
			wantCode:   0,
			wantStdout: "192.0.2.5\n",
		},
		{
			// The derived fallback path has no name; the diagnostic line
			// must render without a stray leading space.
			name:       "fallback path dead renders unnamed label",
			args:       []string{"plain"},
			dialer:     fakedial.New().Down("192.0.2.5:2222"),
			wantCode:   1,
			wantStdout: "",
			wantStderr: []string{
				`all 1 path(s) to "plain" are down`,
				"\n  192.0.2.5:2222:",
			},
		},
		{
			name:       "unknown host",
			args:       []string{"bogus"},
			dialer:     fakedial.New(),
			wantCode:   3,
			wantStdout: "",
			wantStderr: []string{`unknown host "bogus"`},
		},
		{
			name:       "no args",
			args:       nil,
			dialer:     fakedial.New(),
			wantCode:   2,
			wantStdout: "",
			wantStderr: []string{"usage: sasash addr"},
		},
		{
			name:       "too many args",
			args:       []string{"a", "b"},
			dialer:     fakedial.New(),
			wantCode:   2,
			wantStdout: "",
			wantStderr: []string{"usage: sasash addr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			root := filepath.Join(home, ".ssh", "config")
			if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
				t.Fatalf("MkdirAll() err = %v", err)
			}
			if err := os.WriteFile(root, []byte(config), 0o600); err != nil {
				t.Fatalf("WriteFile() err = %v", err)
			}

			var stdout, stderr bytes.Buffer
			got := runAddr(tt.args, &stdout, &stderr, root, home, tt.dialer)
			if got != tt.wantCode {
				t.Errorf("runAddr(%v) = %v, want %v (stderr: %q)", tt.args, got, tt.wantCode, stderr.String())
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("runAddr(%v) stdout = %q, want %q", tt.args, stdout.String(), tt.wantStdout)
			}
			// Quiet success is a contract: no expectations means no output.
			if len(tt.wantStderr) == 0 && stderr.String() != "" {
				t.Errorf("runAddr(%v) stderr = %q, want empty", tt.args, stderr.String())
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("runAddr(%v) stderr = %q, want substring %q", tt.args, stderr.String(), want)
				}
			}
		})
	}
}

func TestRunAddr_MissingConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	got := runAddr([]string{"ubuntu"}, &stdout, &stderr, filepath.Join(home, ".ssh", "config"), home, fakedial.New())
	if got != 3 {
		t.Errorf("runAddr() = %v, want 3", got)
	}
	if stdout.String() != "" {
		t.Errorf("runAddr() stdout = %q, want empty", stdout.String())
	}
	// Pin WHICH branch fails: the unreadable config, not "unknown host".
	if !strings.Contains(stderr.String(), "no such file") {
		t.Errorf("runAddr() stderr = %q, want the missing-config error", stderr.String())
	}
}

// Warnings about malformed meta surface only when resolution fails: the
// command lives inside $( ) in prompts and loops, so success must be silent
// on stderr too, but a failing lookup should show everything relevant.
func TestRunAddr_WarningsOnlyOnFailure(t *testing.T) {
	t.Parallel()

	config := `# @sasash path lan
# @sasash path ts 198.51.100.20
Host ubuntu
  HostName 192.0.2.10
`
	home := t.TempDir()
	root := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	if err := os.WriteFile(root, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	t.Run("success is silent", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer
		got := runAddr([]string{"ubuntu"}, &stdout, &stderr, root, home, fakedial.New().Up("198.51.100.20:22", 20*time.Millisecond))
		if got != 0 {
			t.Fatalf("runAddr() = %v, want 0 (stderr: %q)", got, stderr.String())
		}
		if stdout.String() != "198.51.100.20\n" {
			t.Errorf("runAddr() stdout = %q, want %q", stdout.String(), "198.51.100.20\n")
		}
		if stderr.String() != "" {
			t.Errorf("runAddr() stderr = %q, want empty on success", stderr.String())
		}
	})

	t.Run("failure surfaces the warning", func(t *testing.T) {
		t.Parallel()

		var stdout, stderr bytes.Buffer
		got := runAddr([]string{"ubuntu"}, &stdout, &stderr, root, home, fakedial.New().Down("198.51.100.20:22"))
		if got != 1 {
			t.Fatalf("runAddr() = %v, want 1 (stderr: %q)", got, stderr.String())
		}
		if stdout.String() != "" {
			t.Errorf("runAddr() stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), "missing addr") {
			t.Errorf("runAddr() stderr = %q, want the malformed-meta warning", stderr.String())
		}
	})
}
