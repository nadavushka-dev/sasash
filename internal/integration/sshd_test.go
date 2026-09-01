//go:build integration

// Package integration is the real-sshd test tier from SPEC §12: a throwaway
// sshd on localhost proving that -O forward / -O cancel / -O check / -O exit
// behave the way §4 claims against real OpenSSH. It re-proves, in CI shape,
// what a human verified live on a real box (2026-09-01: OpenSSH 10.2p1,
// 8/8 checks — master start, -O check, live forward inject, cancel, exit).
//
// Excluded from `go test ./...` by the build tag; run via `make integration`,
// locally before a release, not on every save. Sleeps and retries are fine
// here — the no-sleep rule is for the unit tiers.
//
// The seams still hold: every process runs through run.OS (sshd -D is held as
// a context-cancelled run.OS child; ssh -f backgrounds itself), and every
// listener check dials through probe.Net.
package integration_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run"
)

const sshBin = "/usr/bin/ssh" // SPEC §1: shell out to /usr/bin/ssh, always

// findSshd returns an sshd binary, preferring a homebrew OpenSSH over the
// system one. No sshd at all is an environment gap, not a failure.
func findSshd(t *testing.T) string {
	t.Helper()
	for _, p := range []string{
		"/opt/homebrew/opt/openssh/sbin/sshd",
		"/usr/local/opt/openssh/sbin/sshd",
		"/usr/sbin/sshd",
	} {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	t.Skip("no sshd binary found; brew install openssh to run the integration tier")
	return ""
}

// freePort reserves an ephemeral localhost port and returns it. The port is
// released before use, so a collision is possible but wildly unlikely within
// one test run.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%q) err = %v", l.Addr(), err)
	}
	return port
}

// keygen writes a fresh passphrase-less ed25519 key pair at path.
func keygen(t *testing.T, r run.Runner, path string) {
	t.Helper()
	res, err := r.Run(context.Background(), "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path)
	if err != nil || !res.OK() {
		t.Fatalf("ssh-keygen(%q) = %v, %v\n%s", path, res.Code, err, res.Stderr)
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// listening reports whether 127.0.0.1:port accepts a TCP connection.
func listening(port string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := probe.Net{}.Dial(ctx, net.JoinHostPort("127.0.0.1", port))
	return err == nil
}

// startSshd spins the throwaway daemon: fresh host key, the current user
// authorized by a fresh client key, everything under t.TempDir(). It returns
// the client key path, the sshd port, and the login user, and tears the
// daemon down in cleanup.
func startSshd(t *testing.T, r run.Runner) (userKey, port, login string) {
	t.Helper()
	sshd := findSshd(t)
	dir := t.TempDir()

	hostKey := filepath.Join(dir, "host_key")
	userKey = filepath.Join(dir, "user_key")
	keygen(t, r, hostKey)
	keygen(t, r, userKey)

	pub, err := os.ReadFile(userKey + ".pub")
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	authKeys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authKeys, pub, 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	// LogLevel VERBOSE + the -e flag below put auth failures on stderr,
	// which the cleanup dumps on test failure — otherwise sshd logs the
	// reason to syslog and the tier can only say "it broke".
	// Deliberately absent: "PerSourcePenalties no" (OpenSSH >= 9.8 penalises
	// a source IP after failed auth, which would flake retry loops or a
	// second test in this package) — the keyword is a fatal parse error on
	// older sshds, so add it only together with a version probe.
	port = freePort(t)
	conf := fmt.Sprintf(`ListenAddress 127.0.0.1
Port %s
HostKey %s
PidFile %s
AuthorizedKeysFile %s
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
StrictModes no
LogLevel VERBOSE
`, port, hostKey, filepath.Join(dir, "sshd.pid"), authKeys)
	confPath := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	// sshd -D stays in the foreground, so hold it as a run.OS child and kill
	// it by cancelling its context (CommandContext delivers SIGKILL, fine
	// for a throwaway daemon). The goroutine closes done rather than sending
	// on it: both the readiness poll and the cleanup wait on it, and a send
	// would satisfy only one of them — the other would hang the whole run.
	ctx, cancel := context.WithCancel(context.Background())
	var sshdRes run.Result
	var sshdErr error
	done := make(chan struct{})
	go func() {
		sshdRes, sshdErr = r.Run(ctx, sshd, "-D", "-e", "-f", confPath)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("sshd (%s) err=%v stderr:\n%s", sshd, sshdErr, sshdRes.Stderr)
		}
	})

	waitFor(t, "sshd to accept on "+port, 10*time.Second, func() bool {
		select {
		case <-done:
			t.Fatalf("sshd %s exited before accepting connections: code %d, err %v\n%s",
				sshd, sshdRes.Code, sshdErr, sshdRes.Stderr)
		default:
		}
		return listening(port)
	})

	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() err = %v", err)
	}
	return userKey, port, u.Username
}

// controlSocketDir returns a short-lived directory with a short absolute
// path. t.TempDir() is too deep for a unix socket on macOS (sun_path caps at
// 104 bytes — the same limit that makes SPEC §2 mandate ControlPath %C).
func controlSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sasash-it-*")
	if err != nil {
		t.Fatalf("MkdirTemp() err = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestControlMaster_LiveForwardLifecycle walks the exact command table from
// SPEC §4 against a real OpenSSH: start a master, verify it, inject a
// forward into the live connection, verify the listener, cancel it, verify
// the listener is gone and the master survived, then tear down. One linear
// test because the states are strictly ordered.
func TestControlMaster_LiveForwardLifecycle(t *testing.T) {
	t.Parallel() // safe: every invocation owns its own sshd, port, and socket

	r := run.OS{}
	userKey, sshdPort, login := startSshd(t, r)
	sock := filepath.Join(controlSocketDir(t), "cm.sock")
	target := login + "@127.0.0.1"

	// Isolate the client completely from the user's real ssh world.
	base := []string{
		sshBin,
		"-F", "/dev/null",
		"-o", "IdentityFile=" + userKey,
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		"-o", "ControlPath=" + sock,
		"-p", sshdPort,
	}
	ssh := func(extra ...string) (run.Result, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		argv := append(append(append([]string{}, base...), extra...), target)
		return r.Run(ctx, argv...)
	}

	// SPEC §4: start master. -f backgrounds ssh after auth, so Run returns.
	if res, err := ssh("-f", "-N", "-M"); err != nil || !res.OK() {
		t.Fatalf("ssh -fNM = %v, %v\n%s", res.Code, err, res.Stderr)
	}
	// The master survives this test's process tree; always tear it down.
	t.Cleanup(func() { _, _ = ssh("-O", "exit") })

	// SPEC §4: is it alive?
	res, err := ssh("-O", "check")
	if err != nil || !res.OK() {
		t.Fatalf("ssh -O check = %v, %v\n%s", res.Code, err, res.Stderr)
	}
	if !strings.Contains(string(res.Stderr), "Master running") {
		t.Errorf("ssh -O check stderr = %q, want %q", res.Stderr, "Master running")
	}

	// SPEC §4: add a forward, live — the reason the whole design works.
	fwdPort := freePort(t)
	spec := fwdPort + ":127.0.0.1:" + freePort(t)
	if res, err := ssh("-O", "forward", "-L", spec); err != nil || !res.OK() {
		t.Fatalf("ssh -O forward -L %s = %v, %v\n%s", spec, res.Code, err, res.Stderr)
	}
	waitFor(t, "forward listener on "+fwdPort, 5*time.Second, func() bool { return listening(fwdPort) })

	// SPEC §4: remove it, live; the listener must go away…
	if res, err := ssh("-O", "cancel", "-L", spec); err != nil || !res.OK() {
		t.Fatalf("ssh -O cancel -L %s = %v, %v\n%s", spec, res.Code, err, res.Stderr)
	}
	waitFor(t, "forward listener on "+fwdPort+" to close", 5*time.Second, func() bool { return !listening(fwdPort) })

	// …and the master must not.
	res, err = ssh("-O", "check")
	if err != nil || !res.OK() {
		t.Fatalf("ssh -O check after cancel = %v, %v\n%s: forward+cancel must not kill the master", res.Code, err, res.Stderr)
	}

	// SPEC §4: tear down, and verify it is really gone.
	if res, err := ssh("-O", "exit"); err != nil || !res.OK() {
		t.Fatalf("ssh -O exit = %v, %v\n%s", res.Code, err, res.Stderr)
	}
	waitFor(t, "master to exit", 5*time.Second, func() bool {
		res, err := ssh("-O", "check")
		return err == nil && !res.OK()
	})
}
