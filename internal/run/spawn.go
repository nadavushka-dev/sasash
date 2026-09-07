package run

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Spawner starts and supervises detached processes (CONTRACT-tunnel §1).
// A tunnel must outlive the sasash that started it, and a later sasash
// must be able to check and stop it by pid alone — so the seam is a pid,
// not a handle.
type Spawner interface {
	// Spawn starts argv detached — its own session (no controlling tty: a
	// closing terminal cannot HUP it and ssh cannot prompt), stdin and
	// stdout from/to /dev/null, stderr truncated into stderrPath (0600) —
	// and returns the pid without waiting. Error means it could not start.
	Spawn(argv []string, stderrPath string) (pid int, err error)
	// Alive reports whether pid is a live process. An exited child of this
	// process is reaped here, so a dead tunnel never lingers as a zombie
	// that reads as alive; a non-child is probed with a null signal.
	Alive(pid int) bool
	// Kill sends SIGTERM. A process that is already gone is not an error.
	Kill(pid int) error
}

// OSSpawner is the real Spawner.
type OSSpawner struct{}

var _ Spawner = OSSpawner{}

// Spawn implements Spawner.
func (OSSpawner) Spawn(argv []string, stderrPath string) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("run: empty argv")
	}
	stderr, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer stderr.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	// Stdin and Stdout left nil are /dev/null; stderr is the file, whose
	// descriptor the child inherits and keeps after we close ours.
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// Alive implements Spawner. A child that exited is reaped by the
// non-blocking wait (and so reads dead); a running child reads alive; a
// process that is not our child is probed with kill(pid, 0), where EPERM
// still means it exists.
func (OSSpawner) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	switch {
	case err == nil && wpid == pid:
		return false
	case err == nil:
		return true
	case errors.Is(err, syscall.ECHILD):
		err := syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	default:
		return false
	}
}

// Kill implements Spawner.
func (OSSpawner) Kill(pid int) error {
	if pid <= 0 {
		return errors.New("run: no pid to kill")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
