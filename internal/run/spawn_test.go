package run_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/run"
)

// waitUntil polls cond for up to two seconds; this is a real-process tier
// and a bounded wait is the honest tool.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOSSpawner_Lifecycle(t *testing.T) {
	t.Parallel()
	s := run.OSSpawner{}
	stderr := filepath.Join(t.TempDir(), "proc.log")

	pid, err := s.Spawn([]string{"/bin/sh", "-c", "echo started >&2; exec sleep 30"}, stderr)
	if err != nil {
		t.Fatalf("Spawn() err = %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if !s.Alive(pid) {
		t.Fatal("Alive() = false right after Spawn, want true")
	}
	waitUntil(t, "stderr to land in the file", func() bool {
		b, _ := os.ReadFile(stderr)
		return string(b) == "started\n"
	})
	info, err := os.Stat(stderr)
	if err != nil {
		t.Fatalf("Stat() err = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("stderr file mode = %o, want 600", got)
	}

	if err := s.Kill(pid); err != nil {
		t.Fatalf("Kill() err = %v", err)
	}
	waitUntil(t, "the process to die", func() bool { return !s.Alive(pid) })
	// A reaped child must stay dead on the next look — no zombie reading
	// as alive, and no error on re-checking a pid that is gone.
	if s.Alive(pid) {
		t.Error("Alive() after death = true, want false (zombie not reaped?)")
	}
	if err := s.Kill(pid); err != nil {
		t.Errorf("Kill() of a dead pid err = %v, want nil", err)
	}
}

// A second Spawn into the same path truncates: the file carries only the
// latest process's stderr.
func TestOSSpawner_Spawn_TruncatesStderr(t *testing.T) {
	t.Parallel()
	s := run.OSSpawner{}
	stderr := filepath.Join(t.TempDir(), "proc.log")
	for _, msg := range []string{"first", "second"} {
		pid, err := s.Spawn([]string{"/bin/sh", "-c", "echo " + msg + " >&2"}, stderr)
		if err != nil {
			t.Fatalf("Spawn(%s) err = %v", msg, err)
		}
		waitUntil(t, msg+" to exit", func() bool { return !s.Alive(pid) })
	}
	b, err := os.ReadFile(stderr)
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if string(b) != "second\n" {
		t.Errorf("stderr file = %q, want %q", b, "second\n")
	}
}

func TestOSSpawner_Errors(t *testing.T) {
	t.Parallel()
	s := run.OSSpawner{}
	tests := []struct {
		name   string
		argv   []string
		stderr string
	}{
		{name: "empty argv", argv: nil, stderr: filepath.Join(t.TempDir(), "x.log")},
		{name: "missing binary", argv: []string{"/nonexistent/sasash-test-binary"}, stderr: filepath.Join(t.TempDir(), "x.log")},
		{name: "unwritable stderr path", argv: []string{"/bin/echo"}, stderr: filepath.Join(t.TempDir(), "missing", "x.log")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := s.Spawn(tt.argv, tt.stderr); err == nil {
				t.Errorf("Spawn(%q) err = nil, want error", tt.argv)
			}
		})
	}

	t.Run("alive of nonpositive pid is false", func(t *testing.T) {
		t.Parallel()
		if s.Alive(0) || s.Alive(-1) {
			t.Error("Alive(0/-1) = true, want false")
		}
	})
	t.Run("kill of nonpositive pid is an error", func(t *testing.T) {
		t.Parallel()
		if err := s.Kill(0); err == nil {
			t.Error("Kill(0) err = nil, want error (would signal the process group)")
		}
	})
	t.Run("a non-child that exists is alive", func(t *testing.T) {
		t.Parallel()
		if !s.Alive(os.Getppid()) {
			t.Error("Alive(parent) = false, want true")
		}
	})
}
