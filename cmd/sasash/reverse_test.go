package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Reverse forwards through the CLI (SASASH.4.41): `up` spawns a mixed
// -L/-R set through the real engine — the R spec as `-R`, R stripped —
// and `down` ends both processes; the semantics are unchanged.
func TestRunUpDown_ReverseSet(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	root := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	config := "# @sasash forwards mac 3000:localhost:3000 R2222:localhost:22\nHost ubuntu\n  HostName 192.0.2.10\n"
	if err := os.WriteFile(root, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	runner := fakerun.New()
	runner.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	// Something else answers on local 2222: irrelevant to a reverse forward.
	d := fakedial.New().Up("127.0.0.1:2222", time.Millisecond)
	sp := fakespawn.New().Dial(d)
	eng := tunnel.Engine{Runner: runner, Spawner: sp, Dialer: d, StateDir: t.TempDir(), Timeout: 10 * time.Millisecond}

	var stdout, stderr bytes.Buffer
	if got := runUp([]string{"ubuntu", "mac"}, &stdout, &stderr, root, home, eng); got != 0 {
		t.Fatalf("runUp() = %v, want 0 (stderr: %q)", got, stderr.String())
	}
	want := []string{
		"ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -L 3000:localhost:3000 ubuntu",
		"ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -R 2222:localhost:22 ubuntu",
	}
	if got := sp.Spawned(); !reflect.DeepEqual(got, want) {
		t.Errorf("runUp() spawned = %v, want %v", got, want)
	}
	desired := []hostmodel.ForwardSet{{Name: "mac", Specs: []string{"3000:localhost:3000", "R2222:localhost:22"}}}
	st, err := eng.Status(context.Background(), "ubuntu", desired)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	for _, f := range st.Forwards {
		if f.State != tunnel.Up {
			t.Errorf("after up, %s = %v, want up", f.Spec, f.State)
		}
	}

	// up again starts nothing: both forwards are serving.
	if got := runUp([]string{"ubuntu"}, &stdout, &stderr, root, home, eng); got != 0 {
		t.Fatalf("runUp() again = %v, want 0 (stderr: %q)", got, stderr.String())
	}
	if got := len(sp.Spawned()); got != 2 {
		t.Errorf("runUp() again spawned %d in total, want 2", got)
	}

	if got := runDown([]string{"ubuntu"}, &stdout, &stderr, root, home, eng); got != 0 {
		t.Fatalf("runDown() = %v, want 0 (stderr: %q)", got, stderr.String())
	}
	if got, want := sp.Killed(), []int{1000, 1001}; !reflect.DeepEqual(got, want) {
		t.Errorf("runDown() killed = %v, want %v", got, want)
	}
	st, err = eng.Status(context.Background(), "ubuntu", desired)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	for _, f := range st.Forwards {
		if f.State != tunnel.Down {
			t.Errorf("after down, %s = %v, want down (never listening for the reverse one)", f.Spec, f.State)
		}
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}
