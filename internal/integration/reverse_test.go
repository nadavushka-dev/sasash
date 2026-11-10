//go:build integration

package integration_test

// Reverse forwards (SASASH.4.41) against a real OpenSSH: an `R…` spec is one
// detached `ssh -N -R` process. The throwaway sshd runs on this machine, so
// the "remote" listener is on loopback here too and can be dialled to prove
// the tunnel end to end.

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// TestEngine_Spawn_ReverseRoundTripAndKill: the reverse process settles,
// the remote bind carries a connection to the local echo, Status says up
// by liveness, and Kill drops the remote listener.
func TestEngine_Spawn_ReverseRoundTripAndKill(t *testing.T) {
	eng := modelBEngine(t)
	echo := startEcho(t)
	remote := freePort(t)
	spec := "R" + remote + ":127.0.0.1:" + echo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := eng.Spawn(ctx, alias, "web", spec); err != nil {
		t.Fatalf("Spawn(%s) err = %v", spec, err)
	}
	if f := forward(t, eng, spec); f.State != tunnel.Up || !f.Reverse || f.Pid == 0 {
		t.Fatalf("after Spawn, row = %+v, want up, reverse, a pid", f)
	}
	roundTrip(t, dialThrough(t, remote), "through the reverse tunnel")

	if err := eng.Kill(ctx, alias, spec); err != nil {
		t.Fatalf("Kill() err = %v", err)
	}
	if f := forward(t, eng, spec); f.State != tunnel.Down {
		t.Errorf("after Kill, row = %+v, want down", f)
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", remote), time.Second); err == nil {
		c.Close()
		t.Errorf("the remote listener on %s outlived its process", remote)
	}
}

// TestEngine_Spawn_ReverseHeldRemotePortFails: ExitOnForwardFailure makes a
// refused remote bind end the process inside the settle window, and its
// stderr is the error — nothing is recorded.
func TestEngine_Spawn_ReverseHeldRemotePortFails(t *testing.T) {
	eng := modelBEngine(t)
	echo := startEcho(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	defer held.Close()
	_, port, _ := net.SplitHostPort(held.Addr().String())
	spec := "R127.0.0.1:" + port + ":127.0.0.1:" + echo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = eng.Spawn(ctx, alias, "web", spec)
	if err == nil || !strings.Contains(err.Error(), "-R 127.0.0.1:"+port) || !strings.Contains(err.Error(), "forwarding failed") {
		t.Fatalf("Spawn() on a held remote port err = %v, want ssh's remote forwarding failure", err)
	}
	if f := forward(t, eng, spec); f.Pid != 0 || f.State != tunnel.Down {
		t.Errorf("a failed reverse spawn left %+v, want down with no pid", f)
	}
}
