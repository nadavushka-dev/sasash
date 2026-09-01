// Internal test package, like main_test.go: main packages cannot be
// imported, so an external _test package has no access to setupHome.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
				if got := setupHome(home, true, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); got != 0 {
					t.Fatalf("pre-setup setupHome() = %v, want 0", got)
				}
			}
			var stdout, stderr bytes.Buffer
			got := setupHome(home, tt.yes, strings.NewReader(tt.stdin), &stdout, &stderr)
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
	if got := setupHome(home, true, strings.NewReader(""), &stdout, &stderr); got != 1 {
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
