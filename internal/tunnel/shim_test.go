package tunnel_test

// The temporary Model A bridge (shim.go): stays on the runner with the
// Model A argv, never reaches the spawner, and keeps main's semantics for
// its callers until the consumer rename pass deletes it.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/stats"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

func TestEngine_Bridge_Argv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(e tunnel.Engine) error
		want []string
	}{
		{
			name: "start",
			call: func(e tunnel.Engine) error { return e.Start(context.Background(), "ubuntu") },
			want: []string{"ssh -fN ubuntu"},
		},
		{
			name: "forward",
			call: func(e tunnel.Engine) error { return e.Forward(context.Background(), "ubuntu", "web", specWeb) },
			want: []string{"ssh -O forward -L 3000:localhost:3000 ubuntu"},
		},
		{
			name: "cancel",
			call: func(e tunnel.Engine) error { return e.Cancel(context.Background(), "ubuntu", specWeb) },
			want: []string{"ssh -O cancel -L 3000:localhost:3000 ubuntu"},
		},
		{
			name: "up with the master down starts it then forwards",
			call: func(e tunnel.Engine) error {
				e.Runner.(*fakerun.Fake).StubExit(1, "ssh", "-O", "check", "ubuntu")
				return e.Up(context.Background(), "ubuntu", "web", []string{specWeb, specDB})
			},
			want: []string{"ssh -O check ubuntu", "ssh -fN ubuntu", "ssh -O forward -L 3000:localhost:3000 ubuntu", "ssh -O forward -L 5432:localhost:5432 ubuntu"},
		},
		{
			name: "up with the master alive only forwards",
			call: func(e tunnel.Engine) error { return e.Up(context.Background(), "ubuntu", "web", []string{specWeb}) },
			want: []string{"ssh -O check ubuntu", "ssh -O forward -L 3000:localhost:3000 ubuntu"},
		},
		{
			name: "downset cancels the given specs, deduplicated",
			call: func(e tunnel.Engine) error {
				return e.DownSet(context.Background(), "ubuntu", "web", []string{specWeb, specWeb})
			},
			want: []string{"ssh -O cancel -L 3000:localhost:3000 ubuntu"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSpawner()
			e := engine(t, s, fakedial.New())
			if err := tt.call(e); err != nil {
				t.Fatalf("%s: err = %v", tt.name, err)
			}
			if got := e.Runner.(*fakerun.Fake).CallKeys(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("runner saw %q, want %q", got, tt.want)
			}
			if got := s.Spawned(); len(got) != 0 {
				t.Errorf("spawned %q, want nothing: the bridge never spawns", got)
			}
		})
	}
}

func TestEngine_Bridge_State(t *testing.T) {
	t.Parallel()
	e := engine(t, newSpawner(), fakedial.New())
	ctx := context.Background()
	if err := e.Forward(ctx, "ubuntu", "web", specWeb); err != nil {
		t.Fatalf("Forward() err = %v", err)
	}
	if err := e.Forward(ctx, "ubuntu", "", specDB); err != nil {
		t.Fatalf("Forward() err = %v", err)
	}
	if got, want := readState(t, e, "ubuntu"), `{"forwards":[{"set":"web","spec":"3000:localhost:3000"},{"set":"","spec":"5432:localhost:5432"}]}`; got != want {
		t.Errorf("state after Forward = %s, want %s", got, want)
	}
	if err := e.Cancel(ctx, "ubuntu", specWeb); err != nil {
		t.Fatalf("Cancel() err = %v", err)
	}
	if err := e.Cancel(ctx, "ubuntu", specDB); err != nil {
		t.Fatalf("Cancel(ad-hoc) err = %v", err)
	}
	if got, want := readState(t, e, "ubuntu"), `{"forwards":[{"set":"","spec":"5432:localhost:5432"}]}`; got != want {
		t.Errorf("state after Cancel = %s, want the named record gone, the ad-hoc one kept", got)
	}
	// DownSet: a named set's records go; the ad-hoc set's stay untouched
	// (no write at all).
	if err := e.Forward(ctx, "ubuntu", "web", specWeb); err != nil {
		t.Fatalf("Forward() err = %v", err)
	}
	if err := e.DownSet(ctx, "ubuntu", "web", nil); err != nil {
		t.Fatalf("DownSet() err = %v", err)
	}
	if err := e.DownSet(ctx, "ubuntu", "", []string{specDB}); err != nil {
		t.Fatalf("DownSet(ad-hoc) err = %v", err)
	}
	if got, want := readState(t, e, "ubuntu"), `{"forwards":[{"set":"","spec":"5432:localhost:5432"}]}`; got != want {
		t.Errorf("state after DownSet = %s, want %s", got, want)
	}
}

func TestEngine_Bridge_Errors(t *testing.T) {
	t.Parallel()
	refuse := fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("nope")}}
	ctx := context.Background()

	t.Run("refusals come back verbatim, cancel still retires", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000"}]}`)
		e.Runner = fakerun.New().
			Stub(refuse, "ssh", "-O", "forward", "-L", specWeb, "ubuntu").
			Stub(refuse, "ssh", "-O", "cancel", "-L", specWeb, "ubuntu").
			Stub(refuse, "ssh", "-fN", "ubuntu")
		for name, call := range map[string]func() error{
			"Forward": func() error { return e.Forward(ctx, "ubuntu", "web", specWeb) },
			"Cancel":  func() error { return e.Cancel(ctx, "ubuntu", specWeb) },
			"Start":   func() error { return e.Start(ctx, "ubuntu") },
		} {
			if err := call(); err == nil || !strings.Contains(err.Error(), "nope") {
				t.Errorf("%s() err = %v, want the refusal", name, err)
			}
		}
		if got := readState(t, e, "ubuntu"); got != `{"forwards":[]}` {
			t.Errorf("state after refused Cancel = %s, want retired anyway", got)
		}
	})

	t.Run("up aborts on the first genuine failure", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		e.Runner = fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").
			StubExit(0, "ssh", "-O", "forward", "-L", specWeb, "ubuntu").
			Stub(refuse, "ssh", "-O", "forward", "-L", specDB, "ubuntu")
		if err := e.Up(ctx, "ubuntu", "web", []string{specWeb, specDB, "8080:localhost:80"}); err == nil {
			t.Fatal("Up() err = nil, want the second forward's refusal")
		}
		if got := len(e.Runner.(*fakerun.Fake).Calls()); got != 3 {
			t.Errorf("Up() made %d calls, want 3 (check, forward, failed forward)", got)
		}
	})

	t.Run("up with a failed master start returns it", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		e.Runner = fakerun.New().StubExit(1, "ssh", "-O", "check", "ubuntu").Stub(refuse, "ssh", "-fN", "ubuntu")
		if err := e.Up(ctx, "ubuntu", "web", []string{specWeb}); err == nil || !strings.Contains(err.Error(), "nope") {
			t.Errorf("Up() err = %v, want the start refusal", err)
		}
	})

	t.Run("up seam failure", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		e.Runner = fakerun.New().Stub(fakerun.Reply{Err: errors.New("boom")}, "ssh", "-O", "check", "ubuntu")
		if err := e.Up(ctx, "ubuntu", "web", nil); err == nil {
			t.Error("Up() err = nil, want seam failure")
		}
	})

	t.Run("up continues on state and stats record failures", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		e.Runner.(*fakerun.Fake).StubExit(1, "ssh", "-O", "check", "ubuntu")
		e.Stats = &stats.Store{Dir: e.StateDir}
		for _, name := range []string{"ubuntu.json", "ubuntu.stats.json"} {
			if err := os.Mkdir(filepath.Join(e.StateDir, name), 0o700); err != nil {
				t.Fatalf("Mkdir() err = %v", err)
			}
		}
		err := e.Up(ctx, "ubuntu", "web", []string{specWeb, specDB})
		if !errors.Is(err, tunnel.ErrStateWrite) {
			t.Fatalf("Up() err = %v, want ErrStateWrite", err)
		}
		if got := len(e.Runner.(*fakerun.Fake).Calls()); got != 4 {
			t.Errorf("Up() made %d calls, want 4 (check, start, two forwards)", got)
		}
	})

	t.Run("downset joins cancel errors and clears the named set anyway", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000"},{"set":"web","spec":"5432:localhost:5432"}]}`)
		e.Runner = fakerun.New().
			Stub(refuse, "ssh", "-O", "cancel", "-L", specWeb, "ubuntu").
			Stub(refuse, "ssh", "-O", "cancel", "-L", specDB, "ubuntu")
		err := e.DownSet(ctx, "ubuntu", "web", nil)
		if err == nil || strings.Count(err.Error(), "nope") != 2 {
			t.Errorf("DownSet() err = %v, want both refusals joined", err)
		}
		if got := readState(t, e, "ubuntu"); got != `{"forwards":[]}` {
			t.Errorf("state = %s, want cleared", got)
		}
	})

	t.Run("downset state failure is ErrStateWrite", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		if err := e.DownSet(ctx, "ubuntu", "web", []string{specWeb}); !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("DownSet() err = %v, want ErrStateWrite", err)
		}
	})

	t.Run("cancel state failure when ssh succeeded", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		if err := e.Cancel(ctx, "ubuntu", specWeb); !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Cancel() err = %v, want ErrStateWrite", err)
		}
	})
}

func TestEngine_Bridge_StartRecordsMasterStart(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	e := engine(t, newSpawner(), fakedial.New())
	e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return t0 }}
	if err := e.Start(context.Background(), "ubuntu"); err != nil {
		t.Fatalf("Start() err = %v", err)
	}
	obs, corrupt := e.Stats.Load("ubuntu")
	if corrupt || !obs.MasterBySasash || !obs.MasterSince.Equal(t0) {
		t.Errorf("Load() = %+v (corrupt %v), want sasash-started at t0", obs, corrupt)
	}

	t.Run("record failure is ErrStateWrite, the master IS running", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		e.Stats = &stats.Store{Dir: e.StateDir}
		if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.stats.json"), 0o700); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		if err := e.Start(context.Background(), "ubuntu"); !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Start() err = %v, want ErrStateWrite", err)
		}
	})
}
