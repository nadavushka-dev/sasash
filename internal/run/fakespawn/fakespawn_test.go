package fakespawn_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
)

func dials(d *fakedial.Fake, addr string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := d.Dial(ctx, addr)
	return err == nil
}

func TestFake_PortFollowsTheProcess(t *testing.T) {
	t.Parallel()
	d := fakedial.New()
	f := fakespawn.New().Dial(d)
	argv := []string{"ssh", "-N", "-L", "3000:localhost:3000", "h"}
	pid, err := f.Spawn(argv, filepath.Join(t.TempDir(), "x.log"))
	if err != nil || pid != 1000 {
		t.Fatalf("Spawn() = %d, %v, want 1000, nil", pid, err)
	}
	if !f.Alive(pid) || !dials(d, "127.0.0.1:3000") {
		t.Errorf("after Spawn: alive %v, port answers %v, want both true", f.Alive(pid), dials(d, "127.0.0.1:3000"))
	}
	if err := f.Kill(pid); err != nil {
		t.Fatalf("Kill() err = %v", err)
	}
	if f.Alive(pid) || dials(d, "127.0.0.1:3000") {
		t.Errorf("after Kill: alive %v, port answers %v, want both false", f.Alive(pid), dials(d, "127.0.0.1:3000"))
	}
	if got := f.Spawned(); !reflect.DeepEqual(got, []string{"ssh -N -L 3000:localhost:3000 h"}) {
		t.Errorf("Spawned() = %q", got)
	}
	if got := f.Killed(); !reflect.DeepEqual(got, []int{1000}) {
		t.Errorf("Killed() = %v", got)
	}
	if got := f.Probed(); !reflect.DeepEqual(got, []int{1000, 1000}) {
		t.Errorf("Probed() = %v, want both Alive calls", got)
	}
}

func TestFake_LiveOn(t *testing.T) {
	t.Parallel()
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	f := fakespawn.New().Dial(d).LiveOn(41, "3000")
	if !f.Alive(41) || !dials(d, "127.0.0.1:3000") {
		t.Fatal("LiveOn: want alive and the port answering")
	}
	if err := f.Kill(41); err != nil || f.Alive(41) || dials(d, "127.0.0.1:3000") {
		t.Errorf("after Kill: err %v, alive %v, port %v; want dead and closed", err, f.Alive(41), dials(d, "127.0.0.1:3000"))
	}
}

func TestFake_Scripts(t *testing.T) {
	t.Parallel()
	t.Run("exits writes the stderr file and is dead", func(t *testing.T) {
		t.Parallel()
		f := fakespawn.New().Exits("nope\n", "ssh", "h")
		log := filepath.Join(t.TempDir(), "x.log")
		pid, err := f.Spawn([]string{"ssh", "h"}, log)
		if err != nil || f.Alive(pid) {
			t.Fatalf("Spawn() = %d, %v, alive %v; want dead", pid, err, f.Alive(pid))
		}
		if b, _ := os.ReadFile(log); string(b) != "nope\n" {
			t.Errorf("stderr file = %q, want %q", b, "nope\n")
		}
		if got := f.LogPaths(); !reflect.DeepEqual(got, []string{log}) {
			t.Errorf("LogPaths() = %q", got)
		}
	})
	t.Run("stubborn and kill errors keep the process alive", func(t *testing.T) {
		t.Parallel()
		f := fakespawn.New().Live(41).Live(42)
		f.StubbornPids = map[int]bool{41: true}
		_ = f.Kill(41)
		_ = f.Kill(42)
		if !f.Alive(41) || f.Alive(42) {
			t.Errorf("alive 41 %v 42 %v, want true false", f.Alive(41), f.Alive(42))
		}
		f.KillErr = errors.New("nope")
		f.Live(43)
		if err := f.Kill(43); err == nil || !f.Alive(43) {
			t.Errorf("Kill() err = %v, alive %v; want error and alive", err, f.Alive(43))
		}
	})
	t.Run("spawn error and never-listens", func(t *testing.T) {
		t.Parallel()
		d := fakedial.New()
		f := fakespawn.New().Dial(d)
		f.NeverListens = true
		called := false
		f.OnSpawn = func() { called = true }
		pid, err := f.Spawn([]string{"ssh", "-L", "3000:localhost:3000", "h"}, filepath.Join(t.TempDir(), "x.log"))
		if err != nil || !called || dials(d, "127.0.0.1:3000") {
			t.Errorf("Spawn() = %d, %v, hook %v, port %v", pid, err, called, dials(d, "127.0.0.1:3000"))
		}
		f.SpawnErr = errors.New("no binary")
		if _, err := f.Spawn([]string{"ssh"}, ""); err == nil {
			t.Error("Spawn() err = nil, want SpawnErr")
		}
	})
}
