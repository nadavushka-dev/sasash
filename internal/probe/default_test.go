package probe_test

import (
	"context"
	"net"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/probe"
)

// A zero-value Prober must fall back to the real dialer and the default
// timeout rather than panicking on a nil Dialer.
func TestProber_ZeroValueUsesRealDialer(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() err = %v", err)
	}

	got := probe.Prober{}.ProbeAll(context.Background(), []probe.Path{
		{Name: "local", Host: host, Port: port},
	})
	if len(got) != 1 || !got[0].Alive() {
		t.Errorf("ProbeAll() = %+v, want one alive status", got)
	}
}
