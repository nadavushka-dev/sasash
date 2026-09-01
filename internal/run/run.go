// Package run is the only place in sasash that starts an external process.
//
// Every ssh invocation goes through Runner, which makes the whole tunnel and
// path layer testable without a live SSH server. See SPEC.md §12.
package run

import (
	"context"
	"errors"
	"os/exec"
)

// Result is the outcome of a finished process. Code is the exit status; it is
// meaningful even when Run returns a nil error.
type Result struct {
	Stdout []byte
	Stderr []byte
	Code   int
}

// OK reports whether the process exited successfully.
func (r Result) OK() bool { return r.Code == 0 }

// Runner starts a process and waits for it to finish, capturing its output.
//
// A non-zero exit is not an error: it is reported through Result.Code. Run
// returns an error only when the process could not be started or was killed
// before it could report a status (a cancelled context, a missing binary).
type Runner interface {
	Run(ctx context.Context, argv ...string) (Result, error)
}

// OS is the real Runner, backed by os/exec.
type OS struct{}

var _ Runner = OS{}

// Run implements Runner.
func (OS) Run(ctx context.Context, argv ...string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("run: empty argv")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr writeBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.b, Stderr: stderr.b}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Code = 0
	case errors.As(err, &exitErr):
		// The process ran and reported a status. That is data, not a failure.
		res.Code = exitErr.ExitCode()
	default:
		return res, err
	}
	return res, ctx.Err()
}

// writeBuffer is a minimal io.Writer so we do not pull in bytes.Buffer's
// read half, which we never use.
type writeBuffer struct{ b []byte }

func (w *writeBuffer) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
