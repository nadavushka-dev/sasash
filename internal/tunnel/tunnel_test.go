package tunnel_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/stats"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

func engine(t *testing.T, r *fakerun.Fake, d *fakedial.Fake) tunnel.Engine {
	t.Helper()
	return tunnel.Engine{Runner: r, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
}

// The argv strings are the product (SPEC §4): each one exact, or a typo
// silently does nothing.
func TestEngine_Argv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(e tunnel.Engine) error
		want string
	}{
		{
			name: "start",
			call: func(e tunnel.Engine) error { return e.Start(context.Background(), "ubuntu") },
			want: "ssh -fN ubuntu",
		},
		{
			name: "check",
			call: func(e tunnel.Engine) error {
				_, err := e.CheckMaster(context.Background(), "ubuntu")
				return err
			},
			want: "ssh -O check ubuntu",
		},
		{
			name: "forward",
			call: func(e tunnel.Engine) error {
				return e.Forward(context.Background(), "ubuntu", "web", "3000:localhost:3000")
			},
			want: "ssh -O forward -L 3000:localhost:3000 ubuntu",
		},
		{
			name: "cancel",
			call: func(e tunnel.Engine) error {
				return e.Cancel(context.Background(), "ubuntu", "3000:localhost:3000")
			},
			want: "ssh -O cancel -L 3000:localhost:3000 ubuntu",
		},
		{
			name: "exit",
			call: func(e tunnel.Engine) error { return e.Exit(context.Background(), "ubuntu") },
			want: "ssh -O exit ubuntu",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New()
			r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
			e := engine(t, r, fakedial.New())
			if err := tt.call(e); err != nil {
				t.Fatalf("%s: err = %v", tt.name, err)
			}
			keys := r.CallKeys()
			if len(keys) != 1 || keys[0] != tt.want {
				t.Errorf("argv = %q, want exactly [%q]", keys, tt.want)
			}
		})
	}
}

func TestEngine_CheckMaster(t *testing.T) {
	t.Parallel()

	t.Run("exit zero is alive", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
		got, err := engine(t, r, fakedial.New()).CheckMaster(context.Background(), "ubuntu")
		if err != nil || !got {
			t.Errorf("CheckMaster() = %v, %v, want true, nil", got, err)
		}
	})

	t.Run("non-zero exit is data not error", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
		got, err := engine(t, r, fakedial.New()).CheckMaster(context.Background(), "ubuntu")
		if err != nil || got {
			t.Errorf("CheckMaster() = %v, %v, want false, nil", got, err)
		}
	})

	t.Run("seam failure is an error", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().Stub(fakerun.Reply{Err: errors.New("no ssh binary")}, "ssh", "-O", "check", "ubuntu")
		_, err := engine(t, r, fakedial.New()).CheckMaster(context.Background(), "ubuntu")
		if err == nil {
			t.Error("CheckMaster() err = nil, want seam error")
		}
	})
}

func TestEngine_ActionErrors(t *testing.T) {
	t.Parallel()

	t.Run("non-zero exit carries trimmed stderr", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().Stub(
			fakerun.Reply{Result: run.Result{Stderr: []byte("unknown forward\n"), Code: 255}},
			"ssh", "-O", "cancel", "-L", "3000:localhost:3000", "ubuntu")
		err := engine(t, r, fakedial.New()).Cancel(context.Background(), "ubuntu", "3000:localhost:3000")
		if err == nil || !strings.Contains(err.Error(), "unknown forward") {
			t.Errorf("Cancel() err = %v, want ssh's stderr in it", err)
		}
		if !strings.Contains(err.Error(), "ssh -O cancel -L 3000:localhost:3000 ubuntu") {
			t.Errorf("Cancel() err = %v, want the argv in it", err)
		}
	})

	t.Run("empty stderr falls back to exit code", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().StubExit(1, "ssh", "-fN", "ubuntu")
		err := engine(t, r, fakedial.New()).Start(context.Background(), "ubuntu")
		if err == nil || !strings.Contains(err.Error(), "exit 1") {
			t.Errorf("Start() err = %v, want exit code fallback", err)
		}
	})
}

func TestEngine_Up(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		checkExit int
		wantKeys  []string
	}{
		{
			name:      "master down starts then forwards",
			checkExit: 1,
			wantKeys: []string{
				"ssh -O check ubuntu",
				"ssh -fN ubuntu",
				"ssh -O forward -L 3000:localhost:3000 ubuntu",
				"ssh -O forward -L 3001:localhost:3001 ubuntu",
			},
		},
		{
			name:      "master already up skips start",
			checkExit: 0,
			wantKeys: []string{
				"ssh -O check ubuntu",
				"ssh -O forward -L 3000:localhost:3000 ubuntu",
				"ssh -O forward -L 3001:localhost:3001 ubuntu",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(tt.checkExit, "ssh", "-O", "check", "ubuntu")
			r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
			e := engine(t, r, fakedial.New())
			if err := e.Up(context.Background(), "ubuntu", "web", []string{"3000:localhost:3000", "3001:localhost:3001"}); err != nil {
				t.Fatalf("Up() err = %v", err)
			}
			if got := r.CallKeys(); !reflect.DeepEqual(got, tt.wantKeys) {
				t.Errorf("Up() calls = %q, want %q", got, tt.wantKeys)
			}
		})
	}
}

func TestEngine_Up_FirstErrorAborts(t *testing.T) {
	t.Parallel()
	r := fakerun.New().
		StubExit(0, "ssh", "-O", "check", "ubuntu").
		Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("refused"), Code: 255}},
			"ssh", "-O", "forward", "-L", "3000:localhost:3000", "ubuntu")
	e := engine(t, r, fakedial.New())
	err := e.Up(context.Background(), "ubuntu", "web", []string{"3000:localhost:3000", "3001:localhost:3001"})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("Up() err = %v, want the forward failure", err)
	}
	for _, key := range r.CallKeys() {
		if strings.Contains(key, "3001") {
			t.Errorf("Up() went on to %q after a failure", key)
		}
	}
}

// Contract amendment B1: a cache-write failure must never stop real
// tunnels — Up keeps forwarding and reports the joined ErrStateWrite at
// the end.
func TestEngine_Up_ContinuesOnStateWriteFailure(t *testing.T) {
	t.Parallel()
	r := fakerun.New()
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e := engine(t, r, fakedial.New())
	// Block the host's state-file slot with a directory: every record fails
	// while every ssh call succeeds.
	if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}

	err := e.Up(context.Background(), "ubuntu", "web", []string{"3000:localhost:3000", "3001:localhost:3001"})
	if !errors.Is(err, tunnel.ErrStateWrite) {
		t.Fatalf("Up() err = %v, want joined ErrStateWrite", err)
	}
	want := []string{
		"ssh -O check ubuntu",
		"ssh -O forward -L 3000:localhost:3000 ubuntu",
		"ssh -O forward -L 3001:localhost:3001 ubuntu",
	}
	if got := r.CallKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("Up() calls = %q, want %q (all forwards attempted)", got, want)
	}
}

func TestEngine_DownSet(t *testing.T) {
	t.Parallel()
	r := fakerun.New()
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e := engine(t, r, fakedial.New())
	ctx := context.Background()

	// Record one desired spec, one extra ad-hoc-in-set spec, and one spec
	// in another set that must survive.
	for _, f := range [][2]string{{"web", "3000:localhost:3000"}, {"web", "3005:localhost:3005"}, {"api", "4011:localhost:4011"}} {
		if err := e.Forward(ctx, "ubuntu", f[0], f[1]); err != nil {
			t.Fatalf("Forward(%v) err = %v", f, err)
		}
	}
	r2 := fakerun.New()
	r2.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e.Runner = r2

	if err := e.DownSet(ctx, "ubuntu", "web", []string{"3000:localhost:3000"}); err != nil {
		t.Fatalf("DownSet() err = %v", err)
	}
	want := []string{
		"ssh -O cancel -L 3000:localhost:3000 ubuntu",
		"ssh -O cancel -L 3005:localhost:3005 ubuntu",
	}
	if got := r2.CallKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("DownSet() calls = %q, want %q (desired ∪ recorded, other sets untouched)", got, want)
	}

	// The api set's record must survive; web's must be gone.
	st, err := e.Status(ctx, "ubuntu", nil)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	var specs []string
	for _, f := range st.Forwards {
		specs = append(specs, f.Set+"/"+f.Spec)
	}
	if !reflect.DeepEqual(specs, []string{"api/4011:localhost:4011"}) {
		t.Errorf("state after DownSet = %v, want only api/4011", specs)
	}
}

func TestEngine_DownSet_JoinsCancelErrors(t *testing.T) {
	t.Parallel()
	r := fakerun.New().
		Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("no such forward"), Code: 255}},
			"ssh", "-O", "cancel", "-L", "3000:localhost:3000", "ubuntu").
		StubExit(0, "ssh", "-O", "cancel", "-L", "3001:localhost:3001", "ubuntu")
	e := engine(t, r, fakedial.New())
	err := e.DownSet(context.Background(), "ubuntu", "web", []string{"3000:localhost:3000", "3001:localhost:3001"})
	if err == nil || !strings.Contains(err.Error(), "no such forward") {
		t.Fatalf("DownSet() err = %v, want the cancel failure", err)
	}
	if got := len(r.CallKeys()); got != 2 {
		t.Errorf("DownSet() made %d calls, want 2 (keeps going after a failure)", got)
	}
}

func TestEngine_StateLifecycle(t *testing.T) {
	t.Parallel()
	r := fakerun.New()
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e := engine(t, r, fakedial.New())
	ctx := context.Background()

	specs := func() []string {
		st, err := e.Status(ctx, "ubuntu", nil)
		if err != nil {
			t.Fatalf("Status() err = %v", err)
		}
		var out []string
		for _, f := range st.Forwards {
			out = append(out, f.Spec)
		}
		return out
	}

	if err := e.Forward(ctx, "ubuntu", "web", "3000:localhost:3000"); err != nil {
		t.Fatalf("Forward() err = %v", err)
	}
	if err := e.Forward(ctx, "ubuntu", "web", "3000:localhost:3000"); err != nil {
		t.Fatalf("repeat Forward() err = %v", err)
	}
	if got := specs(); !reflect.DeepEqual(got, []string{"3000:localhost:3000"}) {
		t.Errorf("state after duplicate Forward = %v, want one entry", got)
	}

	// Cancel removes the record even when ssh refuses.
	e.Runner = fakerun.New().
		Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("nope"), Code: 255}},
			"ssh", "-O", "cancel", "-L", "3000:localhost:3000", "ubuntu").
		StubExit(0, "ssh", "-O", "check", "ubuntu")
	if err := e.Cancel(ctx, "ubuntu", "3000:localhost:3000"); err == nil {
		t.Fatal("Cancel() err = nil, want ssh's refusal")
	}
	if got := specs(); got != nil {
		t.Errorf("state after failed Cancel = %v, want empty (attempt removes)", got)
	}

	// Exit clears everything even when ssh fails.
	e.Runner = r
	if err := e.Forward(ctx, "ubuntu", "", "4014:localhost:4014"); err != nil {
		t.Fatalf("Forward() err = %v", err)
	}
	e.Runner = fakerun.New().
		Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("not running"), Code: 255}}, "ssh", "-O", "exit", "ubuntu").
		StubExit(0, "ssh", "-O", "check", "ubuntu")
	if err := e.Exit(ctx, "ubuntu"); err == nil {
		t.Fatal("Exit() err = nil, want ssh's failure")
	}
	if got := specs(); got != nil {
		t.Errorf("state after Exit = %v, want empty", got)
	}
}

func TestEngine_ErrStateWrite(t *testing.T) {
	t.Parallel()

	t.Run("blocked state dir", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile() err = %v", err)
		}
		r := fakerun.New()
		r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
		e := tunnel.Engine{Runner: r, Dialer: fakedial.New(), StateDir: blocker}
		err := e.Forward(context.Background(), "ubuntu", "web", "3000:localhost:3000")
		if !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Forward() err = %v, want ErrStateWrite (ssh side succeeded)", err)
		}
	})

	t.Run("host alias that walks out of the dir", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New()
		r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
		e := engine(t, r, fakedial.New())
		err := e.Forward(context.Background(), "../evil", "web", "3000:localhost:3000")
		if !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Forward() err = %v, want ErrStateWrite", err)
		}
		if _, statErr := os.Lstat(filepath.Join(e.StateDir, "..", "evil.json")); statErr == nil {
			t.Error("state file escaped StateDir")
		}
	})
}

// The SPEC §12.3 reconciliation matrix. Reality always wins.
func TestEngine_Status_Matrix(t *testing.T) {
	t.Parallel()
	web := []hostmodel.ForwardSet{{Name: "web", Specs: []string{"3000:localhost:3000"}}}
	tests := []struct {
		name      string
		checkExit int
		dial      func(d *fakedial.Fake)
		desired   []hostmodel.ForwardSet
		state     string // raw state file content; "" = no file
		want      []tunnel.Forward
		corrupt   bool
	}{
		{
			name:      "master alive and port open is up",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired:   web,
			want:      []tunnel.Forward{{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Up}},
		},
		{
			name:      "master alive but forward gone is down",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			desired:   web,
			state:     `{"forwards":[{"set":"web","spec":"3000:localhost:3000"}]}`,
			want:      []tunnel.Forward{{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Down}},
		},
		{
			name:      "master dead but port open is listening never up",
			checkExit: 1,
			dial:      func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired:   web,
			state:     `{"forwards":[{"set":"web","spec":"3000:localhost:3000"}]}`,
			want:      []tunnel.Forward{{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Listening}},
		},
		{
			name:      "master dead and port closed is down",
			checkExit: 1,
			dial:      func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			desired:   web,
			want:      []tunnel.Forward{{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Down}},
		},
		{
			name:      "state file missing reconciles desired only",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired:   web,
			want:      []tunnel.Forward{{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Up}},
		},
		{
			name:      "corrupt state is empty plus flag",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired:   web,
			state:     `{not json`,
			want:      []tunnel.Forward{{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Up}},
			corrupt:   true,
		},
		{
			name:      "recorded ad-hoc appends after desired",
			checkExit: 0,
			dial: func(d *fakedial.Fake) {
				d.Up("127.0.0.1:3000", time.Millisecond)
				d.Down("127.0.0.1:4014")
			},
			desired: web,
			state:   `{"forwards":[{"set":"","spec":"4014:localhost:4014"}]}`,
			want: []tunnel.Forward{
				{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Up},
				{Set: "", Spec: "4014:localhost:4014", Port: "4014", State: tunnel.Down},
			},
		},
		{
			name:      "duplicate spec keeps desired attribution",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired:   web,
			state:     `{"forwards":[{"set":"","spec":"3000:localhost:3000"}]}`,
			want:      []tunnel.Forward{{Set: "web", Spec: "3000:localhost:3000", Port: "3000", State: tunnel.Up}},
		},
		{
			name:      "unparseable spec is down without a dial",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) {},
			desired:   []hostmodel.ForwardSet{{Name: "weird", Specs: []string{"localhost:remote"}}},
			want:      []tunnel.Forward{{Set: "weird", Spec: "localhost:remote", Port: "", State: tunnel.Down}},
		},
		{
			name:      "digits past the second field do not count",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) {},
			desired:   []hostmodel.ForwardSet{{Name: "weird", Specs: []string{"a:b:3000"}}},
			want:      []tunnel.Forward{{Set: "weird", Spec: "a:b:3000", Port: "", State: tunnel.Down}},
		},
		{
			name:      "bind address spec uses second field",
			checkExit: 0,
			dial:      func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired:   []hostmodel.ForwardSet{{Name: "web", Specs: []string{"127.0.0.1:3000:localhost:3000"}}},
			want:      []tunnel.Forward{{Set: "web", Spec: "127.0.0.1:3000:localhost:3000", Port: "3000", State: tunnel.Up}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(tt.checkExit, "ssh", "-O", "check", "ubuntu")
			d := fakedial.New()
			tt.dial(d)
			e := engine(t, r, d)
			if tt.state != "" {
				if err := os.WriteFile(filepath.Join(e.StateDir, "ubuntu.json"), []byte(tt.state), 0o600); err != nil {
					t.Fatalf("WriteFile(state) err = %v", err)
				}
			}
			got, err := e.Status(context.Background(), "ubuntu", tt.desired)
			if err != nil {
				t.Fatalf("Status() err = %v", err)
			}
			if got.Master != (tt.checkExit == 0) {
				t.Errorf("Status().Master = %v, want %v", got.Master, tt.checkExit == 0)
			}
			if got.CorruptState != tt.corrupt {
				t.Errorf("Status().CorruptState = %v, want %v", got.CorruptState, tt.corrupt)
			}
			if !reflect.DeepEqual(got.Forwards, tt.want) {
				t.Errorf("Status().Forwards = %+v, want %+v", got.Forwards, tt.want)
			}
		})
	}
}

func TestEngine_SeamErrors(t *testing.T) {
	t.Parallel()

	t.Run("action propagates runner failure", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().Stub(fakerun.Reply{Err: errors.New("no ssh binary")}, "ssh", "-fN", "ubuntu")
		if err := engine(t, r, fakedial.New()).Start(context.Background(), "ubuntu"); err == nil {
			t.Error("Start() err = nil, want seam failure")
		}
	})

	t.Run("up aborts on check seam failure", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().Stub(fakerun.Reply{Err: errors.New("boom")}, "ssh", "-O", "check", "ubuntu")
		err := engine(t, r, fakedial.New()).Up(context.Background(), "ubuntu", "web", []string{"3000:localhost:3000"})
		if err == nil {
			t.Fatal("Up() err = nil, want seam failure")
		}
		if got := len(r.CallKeys()); got != 1 {
			t.Errorf("Up() made %d calls, want 1", got)
		}
	})

	t.Run("up aborts when start fails", func(t *testing.T) {
		t.Parallel()
		r := fakerun.New().
			StubExit(1, "ssh", "-O", "check", "ubuntu").
			Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("auth denied"), Code: 255}}, "ssh", "-fN", "ubuntu")
		err := engine(t, r, fakedial.New()).Up(context.Background(), "ubuntu", "web", []string{"3000:localhost:3000"})
		if err == nil || !strings.Contains(err.Error(), "auth denied") {
			t.Fatalf("Up() err = %v, want the start failure", err)
		}
		for _, key := range r.CallKeys() {
			if strings.Contains(key, "forward") {
				t.Errorf("Up() forwarded after start failed: %q", key)
			}
		}
	})
}

func TestEngine_StateWriteAfterSSHSuccess(t *testing.T) {
	t.Parallel()
	// ssh succeeds but the state file's slot is blocked by a directory:
	// Cancel and Exit must still report the write failure as ErrStateWrite.
	newEngine := func(t *testing.T) tunnel.Engine {
		t.Helper()
		r := fakerun.New()
		r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
		e := engine(t, r, fakedial.New())
		if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		return e
	}

	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		err := newEngine(t).Cancel(context.Background(), "ubuntu", "3000:localhost:3000")
		if !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Cancel() err = %v, want ErrStateWrite", err)
		}
	})

	t.Run("exit", func(t *testing.T) {
		t.Parallel()
		err := newEngine(t).Exit(context.Background(), "ubuntu")
		if !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Exit() err = %v, want ErrStateWrite", err)
		}
	})
}

func TestEngine_Status_UnreadableState(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not stop root")
	}
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	e := engine(t, r, d)
	path := filepath.Join(e.StateDir, "ubuntu.json")
	if err := os.WriteFile(path, []byte(`{"forwards":[]}`), 0o000); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	got, err := e.Status(context.Background(), "ubuntu", []hostmodel.ForwardSet{{Name: "web", Specs: []string{"3000:localhost:3000"}}})
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if !got.CorruptState {
		t.Error("Status().CorruptState = false, want true for an unreadable file")
	}
	if len(got.Forwards) != 1 || got.Forwards[0].State != tunnel.Up {
		t.Errorf("Status().Forwards = %+v, want desired reconciled from reality", got.Forwards)
	}
}

func TestEngine_Status_InvalidHostHasNoState(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "../evil")
	got, err := engine(t, r, fakedial.New()).Status(context.Background(), "../evil", nil)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if got.CorruptState || got.Forwards != nil {
		t.Errorf("Status() = %+v, want empty state for an invalid alias", got)
	}
}

// Zero-value Dialer and Timeout fall back to the real net dialer with the
// default timeout: a dial to a just-freed local port reads as Down.
func TestEngine_Status_DefaultDialer(t *testing.T) {
	t.Parallel()
	// A port the OS just handed out and we closed is guaranteed refused,
	// unlike a fixed low port that a firewall might silently drop.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("Close() err = %v", err)
	}

	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	e := tunnel.Engine{Runner: r, StateDir: t.TempDir()}
	spec := fmt.Sprintf("%d:localhost:%d", port, port)
	got, err := e.Status(context.Background(), "ubuntu", []hostmodel.ForwardSet{{Name: "web", Specs: []string{spec}}})
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if len(got.Forwards) != 1 || got.Forwards[0].State != tunnel.Down {
		t.Errorf("Status().Forwards = %+v, want the freed port Down", got.Forwards)
	}
}

func TestEngine_Status_SeamFailure(t *testing.T) {
	t.Parallel()
	r := fakerun.New().Stub(fakerun.Reply{Err: errors.New("boom")}, "ssh", "-O", "check", "ubuntu")
	if _, err := engine(t, r, fakedial.New()).Status(context.Background(), "ubuntu", nil); err == nil {
		t.Error("Status() err = nil, want seam failure")
	}
}

// Stats integration (CONTRACT-stats.md): Status observes and enriches;
// Start records sasash-started masters; Exit observes the down transition.
func TestEngine_Status_ObservedTimes(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	now := t0
	web := []hostmodel.ForwardSet{{Name: "web", Specs: []string{"3000:localhost:3000"}}}

	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	d := fakedial.New().Up("127.0.0.1:3000", time.Millisecond)
	e := engine(t, r, d)
	e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return now }}
	ctx := context.Background()

	got, err := e.Status(ctx, "ubuntu", web)
	if err != nil {
		t.Fatalf("first Status() err = %v", err)
	}
	if !got.MasterSince.Equal(t0) || got.MasterBySasash || !got.MasterDownSince.IsZero() {
		t.Errorf("first Status() master times = %+v, want run started at t0, not by sasash", got)
	}
	if !got.Forwards[0].Since.Equal(t0) || !got.Forwards[0].DownSince.IsZero() {
		t.Errorf("first Status() forward times = %+v, want since t0", got.Forwards[0])
	}

	// A minute later the forward's port dies while the master lives: the
	// master run keeps its start, the forward flips to down-since.
	now = t0.Add(time.Minute)
	e.Dialer = fakedial.New().Down("127.0.0.1:3000")
	got, err = e.Status(ctx, "ubuntu", web)
	if err != nil {
		t.Fatalf("second Status() err = %v", err)
	}
	if !got.MasterSince.Equal(t0) {
		t.Errorf("second Status() MasterSince = %v, want unchanged t0", got.MasterSince)
	}
	if !got.Forwards[0].DownSince.Equal(now) || !got.Forwards[0].Since.IsZero() {
		t.Errorf("second Status() forward times = %+v, want down since t0+1m", got.Forwards[0])
	}

	// Another minute later the master is dead too.
	now = t0.Add(2 * time.Minute)
	e.Runner = fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	got, err = e.Status(ctx, "ubuntu", web)
	if err != nil {
		t.Fatalf("third Status() err = %v", err)
	}
	if !got.MasterDownSince.Equal(now) || !got.MasterSince.IsZero() {
		t.Errorf("third Status() master times = %+v, want down since t0+2m", got)
	}
}

func TestEngine_Start_RecordsMasterStart(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	r := fakerun.New()
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e := engine(t, r, fakedial.New())
	e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return t0 }}

	if err := e.Start(context.Background(), "ubuntu"); err != nil {
		t.Fatalf("Start() err = %v", err)
	}
	obs, corrupt := e.Stats.Load("ubuntu")
	if corrupt || !obs.MasterBySasash || !obs.MasterSince.Equal(t0) {
		t.Errorf("Load() = %+v (corrupt %v), want sasash-started at t0", obs, corrupt)
	}
}

func TestEngine_Start_StatsRecordFailure(t *testing.T) {
	t.Parallel()
	r := fakerun.New()
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e := engine(t, r, fakedial.New())
	e.Stats = &stats.Store{Dir: e.StateDir}
	if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.stats.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	err := e.Start(context.Background(), "ubuntu")
	if !errors.Is(err, tunnel.ErrStateWrite) {
		t.Errorf("Start() err = %v, want ErrStateWrite (master IS running)", err)
	}
}

// B1 extends to Start's stats record: Up keeps forwarding when only the
// master-start record failed.
func TestEngine_Up_ContinuesOnStatsRecordFailure(t *testing.T) {
	t.Parallel()
	r := fakerun.New().StubExit(1, "ssh", "-O", "check", "ubuntu")
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e := engine(t, r, fakedial.New())
	e.Stats = &stats.Store{Dir: e.StateDir}
	if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.stats.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}

	err := e.Up(context.Background(), "ubuntu", "web", []string{"3000:localhost:3000"})
	if !errors.Is(err, tunnel.ErrStateWrite) {
		t.Fatalf("Up() err = %v, want joined ErrStateWrite", err)
	}
	want := []string{
		"ssh -O check ubuntu",
		"ssh -fN ubuntu",
		"ssh -O forward -L 3000:localhost:3000 ubuntu",
	}
	if got := r.CallKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("Up() calls = %q, want %q (forwarding continued)", got, want)
	}
}

func TestEngine_Exit_ObservesMasterDead(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	now := t0
	r := fakerun.New()
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	e := engine(t, r, fakedial.New())
	e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return now }}
	ctx := context.Background()

	if _, err := e.Status(ctx, "ubuntu", nil); err != nil { // observes alive at t0
		t.Fatalf("Status() err = %v", err)
	}
	now = t0.Add(time.Minute)
	if err := e.Exit(ctx, "ubuntu"); err != nil {
		t.Fatalf("Exit() err = %v", err)
	}
	obs, _ := e.Stats.Load("ubuntu")
	if !obs.MasterDownSince.Equal(now) || !obs.MasterSince.IsZero() {
		t.Errorf("Load() after Exit = %+v, want down since t0+1m", obs)
	}
}

func TestEngine_Status_CorruptStats(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	r := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu")
	e := engine(t, r, fakedial.New())
	e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return t0 }}
	if err := os.WriteFile(filepath.Join(e.StateDir, "ubuntu.stats.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	got, err := e.Status(context.Background(), "ubuntu", nil)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if !got.CorruptStats {
		t.Error("Status().CorruptStats = false, want true")
	}
	// The fresh run starts now — observed, never backdated.
	if !got.MasterSince.Equal(t0) || !got.MasterDownSince.IsZero() {
		t.Errorf("Status() master times = %+v, want fresh run at t0", got)
	}
}

func TestStatus_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   tunnel.Status
		want string
	}{
		{name: "up", in: tunnel.Up, want: "up"},
		{name: "listening", in: tunnel.Listening, want: "listening"},
		{name: "down", in: tunnel.Down, want: "down"},
		{name: "unknown", in: tunnel.Status(9), want: "Status(9)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
