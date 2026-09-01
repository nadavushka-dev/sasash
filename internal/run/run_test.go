package run_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/run"
)

func TestOS_Run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		argv       []string
		wantStdout string
		wantStderr string
		wantCode   int
		wantErr    bool
	}{
		{
			name:       "success captures stdout",
			argv:       []string{"/bin/echo", "hello"},
			wantStdout: "hello\n",
		},
		{
			name:     "non-zero exit is data, not an error",
			argv:     []string{"/bin/sh", "-c", "exit 3"},
			wantCode: 3,
		},
		{
			name:       "stdout and stderr are kept apart",
			argv:       []string{"/bin/sh", "-c", "echo out; echo err >&2"},
			wantStdout: "out\n",
			wantStderr: "err\n",
		},
		{
			name:     "output is captured even on failure",
			argv:     []string{"/bin/sh", "-c", "echo partial; exit 1"},
			wantCode: 1, wantStdout: "partial\n",
		},
		{
			name:    "missing binary is an error",
			argv:    []string{"/nonexistent/sasash-test-binary"},
			wantErr: true,
		},
		{
			name:    "empty argv is an error",
			argv:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := run.OS{}.Run(context.Background(), tt.argv...)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Run(%q) err = nil, want non-nil", tt.argv)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run(%q) err = %v, want nil", tt.argv, err)
			}
			if got.Code != tt.wantCode {
				t.Errorf("Code = %d, want %d", got.Code, tt.wantCode)
			}
			if string(got.Stdout) != tt.wantStdout {
				t.Errorf("Stdout = %q, want %q", got.Stdout, tt.wantStdout)
			}
			if string(got.Stderr) != tt.wantStderr {
				t.Errorf("Stderr = %q, want %q", got.Stderr, tt.wantStderr)
			}
			if got.OK() != (tt.wantCode == 0) {
				t.Errorf("OK() = %v, want %v", got.OK(), tt.wantCode == 0)
			}
		})
	}
}

// A cancelled context must surface as an error rather than a silent exit code,
// so a hung probe can never be mistaken for a host that answered.
func TestOS_Run_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := run.OS{}.Run(ctx, "/bin/sh", "-c", "sleep 5")
	if err == nil {
		t.Fatal("Run() err = nil, want context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run() err = %v, want context.DeadlineExceeded", err)
	}
}

func TestOS_Run_AlreadyCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (run.OS{}).Run(ctx, "/bin/echo", "hi"); err == nil {
		t.Fatal("Run() err = nil, want context error")
	}
}
