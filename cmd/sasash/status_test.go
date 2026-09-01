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

# @sasash path a 203.0.113.1
# @sasash path b 203.0.113.2
# @sasash path c 203.0.113.3
Host edge
`
	tests := []struct {
		name       string
		dialer     probe.Dialer
		wantStdout string // exact, whole output: the five-field format is the contract
	}{
		{
			name: "all paths alive",
			dialer: fakedial.New().
				Up("192.0.2.10:22", 20*time.Millisecond).
				Up("198.51.100.20:22", 5*time.Millisecond).
				Up("192.0.2.5:2222", 12*time.Millisecond).
				Up("203.0.113.1:22", 7*time.Millisecond).
				Up("203.0.113.2:22", 8*time.Millisecond).
				Up("203.0.113.3:22", 9*time.Millisecond),
			wantStdout: "ubuntu lan 192.0.2.10:22 20ms down:-\n" +
				"plain - 192.0.2.5:2222 12ms down:-\n" +
				"edge a 203.0.113.1:22 7ms down:-\n",
		},
		{
			name: "dead paths marked on both sides of the active one",
			dialer: fakedial.New().
				Down("192.0.2.10:22").
				Up("198.51.100.20:22", 70*time.Millisecond).
				Up("192.0.2.5:2222", 12*time.Millisecond).
				Up("203.0.113.1:22", 7*time.Millisecond).
				Down("203.0.113.2:22").
				Down("203.0.113.3:22"),
			wantStdout: "ubuntu ts 198.51.100.20:22 70ms down:lan\n" +
				"plain - 192.0.2.5:2222 12ms down:-\n" +
				"edge a 203.0.113.1:22 7ms down:b,c\n",
		},
		{
			name: "config order beats latency for the active path",
			dialer: fakedial.New().
				Up("192.0.2.10:22", 50*time.Millisecond).
				Up("198.51.100.20:22", 5*time.Millisecond).
				Up("192.0.2.5:2222", 12*time.Millisecond).
				Up("203.0.113.1:22", 90*time.Millisecond).
				Up("203.0.113.2:22", 5*time.Millisecond).
				Up("203.0.113.3:22", 5*time.Millisecond),
			wantStdout: "ubuntu lan 192.0.2.10:22 50ms down:-\n" +
				"plain - 192.0.2.5:2222 12ms down:-\n" +
				"edge a 203.0.113.1:22 90ms down:-\n",
		},
		{
			name: "every host down still reports, unnamed dead path shown by dial address",
			dialer: fakedial.New().
				Down("192.0.2.10:22").
				Down("198.51.100.20:22").
				Down("192.0.2.5:2222").
				Down("203.0.113.1:22").
				Down("203.0.113.2:22").
				Down("203.0.113.3:22"),
			wantStdout: "ubuntu - - down down:lan,ts\n" +
				"plain - - down down:192.0.2.5:2222\n" +
				"edge - - down down:a,b,c\n",
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
			if stderr.String() != "" {
				t.Errorf("runStatus() stderr = %q, want empty for a well-formed config", stderr.String())
			}
		})
	}
}

// The flattened ProbeAll is the design decision that keeps status fast: every
// path across every host probes concurrently, so slow paths cost one timeout
// for the whole listing, not one per host.
func TestRunStatus_ProbesAllHostsConcurrently(t *testing.T) {
	t.Parallel()

	config := "Host a\n  HostName 192.0.2.1\nHost b\n  HostName 192.0.2.2\n"
	root, home := writeStatusConfig(t, config)

	delay := 250 * time.Millisecond
	dialer := fakedial.New().
		Stub("192.0.2.1:22", fakedial.Reply{Latency: time.Millisecond, Delay: delay}).
		Stub("192.0.2.2:22", fakedial.Reply{Latency: time.Millisecond, Delay: delay})

	var stdout, stderr bytes.Buffer
	start := time.Now()
	got := runStatus(nil, &stdout, &stderr, root, home, dialer)
	elapsed := time.Since(start)

	if got != 0 {
		t.Fatalf("runStatus() = %v, want 0", got)
	}
	if want := "a - 192.0.2.1:22 1ms down:-\nb - 192.0.2.2:22 1ms down:-\n"; stdout.String() != want {
		t.Errorf("runStatus() stdout = %q, want %q", stdout.String(), want)
	}
	// Sequential probing would take 2*delay; leave headroom for -race.
	if elapsed >= 2*delay {
		t.Errorf("runStatus() took %v, want < %v: hosts must probe concurrently", elapsed, 2*delay)
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
	if stdout.String() != "ubuntu - 192.0.2.10:22 20ms down:-\n" {
		t.Errorf("runStatus() stdout = %q, want the fallback line", stdout.String())
	}
	if !strings.Contains(stderr.String(), "missing addr") {
		t.Errorf("runStatus() stderr = %q, want the malformed-meta warning", stderr.String())
	}
}
