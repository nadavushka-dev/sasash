// Internal test package: main packages cannot be imported, so an
// external _test package has no access to dispatch.
package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func init() {
	register("argcount", "test-only: exit with len(args)", func(args []string, _, _ io.Writer) int {
		return len(args)
	})
}

func TestDispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // substring; "" skips the check
		wantStderr string // substring; "" skips the check
	}{
		{
			name:       "no args points at help",
			args:       nil,
			wantCode:   1,
			wantStderr: "TUI not built yet",
		},
		{
			name:       "unknown subcommand",
			args:       []string{"bogus"},
			wantCode:   1,
			wantStderr: `unknown subcommand "bogus"`,
		},
		{
			name:       "help lists registered subcommands",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: "argcount",
		},
		{
			name:       "dash h prints usage",
			args:       []string{"-h"},
			wantCode:   0,
			wantStdout: "usage: sasash",
		},
		{
			name:     "subcommand receives remaining args",
			args:     []string{"argcount", "a", "b"},
			wantCode: 2,
		},
		{
			name:       "subcommand stdout reaches the caller",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			got := dispatch(tt.args, &stdout, &stderr)
			if got != tt.wantCode {
				t.Errorf("dispatch(%v) = %v, want %v", tt.args, got, tt.wantCode)
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("dispatch(%v) stdout = %q, want substring %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("dispatch(%v) stderr = %q, want substring %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}
