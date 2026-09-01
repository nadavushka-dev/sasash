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

func writeStatusConfig(t *testing.T, config string) (root, home string) {
	t.Helper()
	home = t.TempDir()
	root = filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	if err := os.WriteFile(root, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	return root, home
}

func TestRunStatus(t *testing.T) {
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
		dialer     probe.Dialer
		wantStdout string // exact, whole output: field order is the contract
	}{
		{
			name: "all paths alive",
			dialer: fakedial.New().
				Up("192.0.2.10:22", 20*time.Millisecond).
				Up("198.51.100.20:22", 5*time.Millisecond).
				Up("192.0.2.5:2222", 12*time.Millisecond),
			wantStdout: "ubuntu lan 192.0.2.10 20ms\n" +
				"plain - 192.0.2.5 12ms\n",
		},
		{
			name: "active falls to second path in config order",
			dialer: fakedial.New().
				Down("192.0.2.10:22").
				Up("198.51.100.20:22", 70*time.Millisecond).
				Up("192.0.2.5:2222", 12*time.Millisecond),
			wantStdout: "ubuntu ts 198.51.100.20 70ms down:lan\n" +
				"plain - 192.0.2.5 12ms\n",
		},
		{
			name: "config order beats latency for the active path",
			dialer: fakedial.New().
				Up("192.0.2.10:22", 50*time.Millisecond).
				Up("198.51.100.20:22", 5*time.Millisecond).
				Up("192.0.2.5:2222", 12*time.Millisecond),
			wantStdout: "ubuntu lan 192.0.2.10 50ms\n" +
				"plain - 192.0.2.5 12ms\n",
		},
		{
			name: "every host down still reports, unnamed dead path shown by address",
			dialer: fakedial.New().
				Down("192.0.2.10:22").
				Down("198.51.100.20:22").
				Down("192.0.2.5:2222"),
			wantStdout: "ubuntu - - down down:lan,ts\n" +
				"plain - - down down:192.0.2.5\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root, home := writeStatusConfig(t, config)
			var stdout, stderr bytes.Buffer
			got := runStatus(nil, &stdout, &stderr, root, home, tt.dialer)
			if got != 0 {
				t.Errorf("runStatus() = %v, want 0 (status reports, it does not judge)", got)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("runStatus() stdout =\n%q, want\n%q", stdout.String(), tt.wantStdout)
			}
		})
	}
}

func TestRunStatus_EmptyConfig(t *testing.T) {
	t.Parallel()

	root, home := writeStatusConfig(t, "")
	var stdout, stderr bytes.Buffer
	if got := runStatus(nil, &stdout, &stderr, root, home, fakedial.New()); got != 0 {
		t.Errorf("runStatus() = %v, want 0", got)
	}
	if stdout.String() != "" {
		t.Errorf("runStatus() stdout = %q, want empty", stdout.String())
	}
}

func TestRunStatus_UsageOnArgs(t *testing.T) {
	t.Parallel()

	root, home := writeStatusConfig(t, "Host a\n")
	var stdout, stderr bytes.Buffer
	if got := runStatus([]string{"ubuntu"}, &stdout, &stderr, root, home, fakedial.New()); got != 2 {
		t.Errorf("runStatus() = %v, want 2", got)
	}
	if !strings.Contains(stderr.String(), "usage: sasash status") {
		t.Errorf("runStatus() stderr = %q, want usage message", stderr.String())
	}
}

func TestRunStatus_MissingConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	got := runStatus(nil, &stdout, &stderr, filepath.Join(home, ".ssh", "config"), home, fakedial.New())
	if got != 3 {
		t.Errorf("runStatus() = %v, want 3", got)
	}
	if !strings.Contains(stderr.String(), "no such file") {
		t.Errorf("runStatus() stderr = %q, want the missing-config error", stderr.String())
	}
}

func TestRunStatus_WarningsToStderrExitStillZero(t *testing.T) {
	t.Parallel()

	config := `# @sasash path lan
Host ubuntu
  HostName 192.0.2.10
`
	root, home := writeStatusConfig(t, config)
	var stdout, stderr bytes.Buffer
	got := runStatus(nil, &stdout, &stderr, root, home, fakedial.New().Up("192.0.2.10:22", 20*time.Millisecond))
	if got != 0 {
		t.Fatalf("runStatus() = %v, want 0", got)
	}
	if stdout.String() != "ubuntu - 192.0.2.10 20ms\n" {
		t.Errorf("runStatus() stdout = %q, want the fallback line", stdout.String())
	}
	if !strings.Contains(stderr.String(), "missing addr") {
		t.Errorf("runStatus() stderr = %q, want the malformed-meta warning", stderr.String())
	}
}
