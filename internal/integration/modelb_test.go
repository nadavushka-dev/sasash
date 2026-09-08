//go:build integration

package integration_test

// Model B (SASASH.7) against a real OpenSSH: a forward is one detached
// `ssh -N -L` process, and killing it drops its listener AND its established
// connections together while the other processes of the set carry on.
// Everything runs through the real seams (run.OSSpawner, probe.Net, run.OS).
//
// The engine execs `ssh` by name with a host alias; the alias needs a config
// the user's real ~/.ssh must never provide, so PATH gets a wrapper that is
// exactly `/usr/bin/ssh -F <throwaway conf>`. t.Setenv makes these tests
// sequential; the parallel tests in this package are paused while they run.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/controlmaster"
	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

const alias = "it"

// modelBEngine starts a throwaway sshd, writes a Host block for it (plus
// any extra directive lines, as a managed block would carry), puts an ssh
// wrapper pinned to that config on PATH, and returns a real engine.
func modelBEngine(t *testing.T, extra ...string) tunnel.Engine {
	t.Helper()
	userKey, sshdPort, login := startSshd(t, run.OS{})
	dir := t.TempDir()
	conf := fmt.Sprintf(`Host %s
  HostName 127.0.0.1
  User %s
  Port %s
  IdentityFile %s
  IdentitiesOnly yes
  IdentityAgent none
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
`, alias, login, sshdPort, userKey)
	for _, line := range extra {
		conf += "  " + line + "\n"
	}
	confPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	wrapper := "#!/bin/sh\nexec " + sshBin + " -F " + confPath + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(wrapper), 0o700); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	eng := tunnel.Engine{StateDir: filepath.Join(dir, "state"), Timeout: time.Second}
	// Whatever happens, no tunnel process outlives the test.
	t.Cleanup(func() { _ = eng.KillAll(context.Background(), alias) })
	return eng
}

// startEcho runs a TCP echo server on an ephemeral port — the remote end
// of every forward here.
func startEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() err = %v", err)
	}
	return port
}

// roundTrip proves a connection is live end to end: a message must come
// back through the tunnel.
func roundTrip(t *testing.T, conn net.Conn, msg string) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline() err = %v", err)
	}
	if _, err := io.WriteString(conn, msg); err != nil {
		t.Fatalf("Write(%q) err = %v", msg, err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("ReadFull() err = %v, want %q echoed back", err, msg)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

func dialThrough(t *testing.T, port string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 5*time.Second)
	if err != nil {
		t.Fatalf("Dial(%s) err = %v", port, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// forward returns the reconciled view of spec from a Status call.
func forward(t *testing.T, eng tunnel.Engine, spec string) tunnel.Forward {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := eng.Status(ctx, alias, []hostmodel.ForwardSet{{Name: "web", Specs: []string{spec}}})
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	for _, f := range st.Forwards {
		if f.Spec == spec {
			return f
		}
	}
	t.Fatalf("Status() has no row for %s: %+v", spec, st.Forwards)
	return tunnel.Forward{}
}

// TestEngine_Kill_DropsEstablishedConnections is the reason for Model B: a
// connection established through a forward dies with the forward's process
// — no listener-only close, no survivor.
func TestEngine_Kill_DropsEstablishedConnections(t *testing.T) {
	eng := modelBEngine(t)
	echo := startEcho(t)
	fwdPort := freePort(t)
	spec := fwdPort + ":127.0.0.1:" + echo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := eng.Spawn(ctx, alias, "web", spec); err != nil {
		t.Fatalf("Spawn(%s) err = %v", spec, err)
	}
	if f := forward(t, eng, spec); f.State != tunnel.Up || f.Pid <= 0 {
		t.Fatalf("after Spawn: %+v, want up with a pid", f)
	}

	conn := dialThrough(t, fwdPort)
	roundTrip(t, conn, "ping")

	if err := eng.Kill(ctx, alias, spec); err != nil {
		t.Fatalf("Kill(%s) err = %v", spec, err)
	}
	waitFor(t, "listener on "+fwdPort+" to close", 5*time.Second, func() bool { return !listening(fwdPort) })

	// The established connection must be gone too: a read fails (EOF or
	// reset), it does not sit there until the deadline.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() err = %v", err)
	}
	_, err := conn.Read(make([]byte, 1))
	var nerr net.Error
	switch {
	case err == nil:
		t.Fatal("Read() after Kill succeeded, want the connection dropped")
	case errors.As(err, &nerr) && nerr.Timeout():
		t.Fatalf("Read() after Kill timed out: the established connection SURVIVED the kill")
	}

	if f := forward(t, eng, spec); f.State != tunnel.Down || f.Pid != 0 {
		t.Errorf("after Kill: %+v, want down with no pid", f)
	}
}

// TestEngine_SpawnSet_AddPortLeavesOthersUndisturbed: growing a set spawns
// one more process and touches nothing else — a connection through the
// first forward keeps working across the add and across the removal of the
// second, and the first process keeps its pid throughout.
func TestEngine_SpawnSet_AddPortLeavesOthersUndisturbed(t *testing.T) {
	eng := modelBEngine(t)
	echo := startEcho(t)
	portA, portB := freePort(t), freePort(t)
	specA := portA + ":127.0.0.1:" + echo
	specB := portB + ":127.0.0.1:" + echo
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := eng.SpawnSet(ctx, alias, "web", []string{specA}); err != nil {
		t.Fatalf("SpawnSet(A) err = %v", err)
	}
	connA := dialThrough(t, portA)
	roundTrip(t, connA, "one")
	pidA := forward(t, eng, specA).Pid
	if pidA <= 0 {
		t.Fatalf("A has no pid after SpawnSet")
	}

	// Add B: A's process and A's connection are untouched.
	if err := eng.SpawnSet(ctx, alias, "web", []string{specA, specB}); err != nil {
		t.Fatalf("SpawnSet(A,B) err = %v", err)
	}
	if f := forward(t, eng, specB); f.State != tunnel.Up {
		t.Fatalf("B after add: %+v, want up", f)
	}
	if f := forward(t, eng, specA); f.State != tunnel.Up || f.Pid != pidA {
		t.Fatalf("A after adding B: %+v, want up with the same pid %d", f, pidA)
	}
	roundTrip(t, connA, "two")
	connB := dialThrough(t, portB)
	roundTrip(t, connB, "three")

	// Remove B: only B dies.
	if err := eng.Kill(ctx, alias, specB); err != nil {
		t.Fatalf("Kill(B) err = %v", err)
	}
	waitFor(t, "listener on "+portB+" to close", 5*time.Second, func() bool { return !listening(portB) })
	if f := forward(t, eng, specA); f.State != tunnel.Up || f.Pid != pidA {
		t.Fatalf("A after killing B: %+v, want up with the same pid %d", f, pidA)
	}
	roundTrip(t, connA, "four")

	// Everything off ends A too.
	if err := eng.KillAll(ctx, alias); err != nil {
		t.Fatalf("KillAll() err = %v", err)
	}
	waitFor(t, "listener on "+portA+" to close", 5*time.Second, func() bool { return !listening(portA) })
	if f := forward(t, eng, specA); f.State != tunnel.Down {
		t.Errorf("A after KillAll: %+v, want down", f)
	}
}

// A held port is refused before anything spawns: never a live process that
// serves nothing, never a silent remap, and no ssh run for it.
func TestEngine_Spawn_RefusesHeldPort(t *testing.T) {
	eng := modelBEngine(t)
	echo := startEcho(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	defer held.Close()
	_, port, _ := net.SplitHostPort(held.Addr().String())
	spec := port + ":127.0.0.1:" + echo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = eng.Spawn(ctx, alias, "web", spec)
	if err == nil || !strings.Contains(err.Error(), "already answers") {
		t.Fatalf("Spawn() on a held port err = %v, want the pre-check refusal", err)
	}
	if f := forward(t, eng, spec); f.Pid != 0 {
		t.Errorf("a refused spawn left a pid: %+v", f)
	}
}

// TestEngine_KillAll_SurvivorStaysRecorded: a process that does not die on
// SIGTERM within the caller's budget (here: stopped with SIGSTOP, so the
// signal cannot land) keeps its record, pid and all, and the next KillAll
// still reaches it once it can die. Nothing sasash started is ever left
// alive and unrecorded.
func TestEngine_KillAll_SurvivorStaysRecorded(t *testing.T) {
	eng := modelBEngine(t)
	echo := startEcho(t)
	port := freePort(t)
	spec := port + ":127.0.0.1:" + echo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := eng.Spawn(ctx, alias, "web", spec); err != nil {
		t.Fatalf("Spawn() err = %v", err)
	}
	pid := forward(t, eng, spec).Pid
	if pid <= 0 {
		t.Fatal("no pid after Spawn")
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP %d: %v", pid, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })

	short, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()
	err := eng.KillAll(short, alias)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("pid %d still running", pid)) {
		t.Fatalf("KillAll() on a stopped process err = %v, want the survivor named", err)
	}
	if f := forward(t, eng, spec); f.Pid != pid {
		t.Fatalf("after the failed KillAll: %+v, want the survivor still recorded as pid %d", f, pid)
	}

	// Let the pending SIGTERM land; the record is what makes the retry possible.
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		t.Fatalf("SIGCONT %d: %v", pid, err)
	}
	if err := eng.KillAll(ctx, alias); err != nil {
		t.Fatalf("second KillAll() err = %v", err)
	}
	waitFor(t, "listener on "+port+" to close", 5*time.Second, func() bool { return !listening(port) })
	if f := forward(t, eng, spec); f.State != tunnel.Down || f.Pid != 0 {
		t.Errorf("after the second KillAll: %+v, want down, no pid", f)
	}
}

// TestEngine_Spawn_NeverSharesAMaster is SASASH.7.1's regression: the block
// sasash saves carries the ControlMaster trio, and against a real OpenSSH
// that made every tunnel process disappear. With no master alive,
// `ControlMaster auto` + `ControlPersist` fork the master to the background
// and the pid Spawn holds exits 0 (the handshake read it as dead; nothing
// was recorded; the tunnel lived on unrecorded — the human's empty
// forwards:[]). With a master alive, ssh hands the -L to the master and
// exits 0, so the listener was the master's and Kill dropped nothing. The
// argv's -S none is what makes both false; this test pins both halves.
func TestEngine_Spawn_NeverSharesAMaster(t *testing.T) {
	sock := filepath.Join(controlSocketDir(t), "cm.sock")
	var trio []string
	for _, d := range controlmaster.Trio {
		trio = append(trio, d.Keyword+" "+d.Value)
	}
	const templatePath = "ControlPath ~/.ssh/cm/%C"
	if len(trio) != 3 || trio[1] != templatePath {
		t.Fatalf("controlmaster.Trio = %q, want the ControlPath line second", trio)
	}
	trio[1] = "ControlPath " + sock // the template's ~ is the passwd home: not ours to touch
	eng := modelBEngine(t, trio...)
	echo := startEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// The wrapper on PATH pins the block; these drive the interactive master
	// the way the hybrid does (a shell's ControlMaster auto, -O check/exit).
	// Own context per call: the -O exit in cleanup runs after the test's
	// ctx is cancelled, and a master left behind holds sshd's stderr pipe
	// open through its session child — startSshd's teardown would hang.
	ssh := func(extra ...string) (run.Result, error) {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		return run.OS{}.Run(cctx, append(append([]string{"ssh"}, extra...), alias)...)
	}

	// Half one: no master alive. The spawned pid must be the tunnel's own —
	// alive, owning the listener — and must not have become a master.
	portA := freePort(t)
	specA := portA + ":127.0.0.1:" + echo
	if err := eng.Spawn(ctx, alias, "", specA); err != nil {
		t.Fatalf("Spawn(%s) under the trio err = %v", specA, err)
	}
	fa := forward(t, eng, specA)
	if fa.State != tunnel.Up || fa.Pid <= 0 || !(run.OSSpawner{}).Alive(fa.Pid) {
		t.Fatalf("ad-hoc after Spawn: %+v, want up with a live pid", fa)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Errorf("the tunnel process created the control socket %s: it became a master", sock)
	}
	if res, err := ssh("-O", "check"); err != nil || res.OK() {
		t.Fatalf("ssh -O check with only a tunnel up = %v, %v; want no master\n%s", res.Code, err, res.Stderr)
	}
	roundTrip(t, dialThrough(t, portA), "one")

	// Half two: a persisted master IS alive (as after an interactive
	// connect). A new tunnel must still own its listener: killing it drops
	// the port, and the master is not touched.
	if res, err := ssh("-fN"); err != nil || !res.OK() {
		t.Fatalf("ssh -fN (start the interactive master) = %v, %v\n%s", res.Code, err, res.Stderr)
	}
	t.Cleanup(func() { _, _ = ssh("-O", "exit") })
	if res, err := ssh("-O", "check"); err != nil || !res.OK() {
		t.Fatalf("ssh -O check after -fN = %v, %v\n%s", res.Code, err, res.Stderr)
	}
	portB := freePort(t)
	specB := portB + ":127.0.0.1:" + echo
	if err := eng.Spawn(ctx, alias, "", specB); err != nil {
		t.Fatalf("Spawn(%s) beside a live master err = %v", specB, err)
	}
	if fb := forward(t, eng, specB); fb.State != tunnel.Up || fb.Pid <= 0 {
		t.Fatalf("B beside a live master: %+v, want up with a pid", fb)
	}
	if err := eng.Kill(ctx, alias, specB); err != nil {
		t.Fatalf("Kill(B) err = %v", err)
	}
	waitFor(t, "listener on "+portB+" to close", 5*time.Second, func() bool { return !listening(portB) })
	if res, err := ssh("-O", "check"); err != nil || !res.OK() {
		t.Errorf("ssh -O check after Kill(B) = %v, %v; killing a tunnel must not touch the master\n%s", res.Code, err, res.Stderr)
	}
	if fa := forward(t, eng, specA); fa.State != tunnel.Up {
		t.Errorf("A after Kill(B): %+v, want still up", fa)
	}
}
