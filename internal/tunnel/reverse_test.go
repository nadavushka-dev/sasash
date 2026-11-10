package tunnel_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Reverse forwards (SASASH.4.41): an `R…` spec is one detached `ssh -N -R`
// process, judged by its liveness alone — there is no local port to dial.
const (
	specRev = "R2222:localhost:22"
	argvRev = "ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -R 2222:localhost:22 ubuntu"
)

// The argv strings are the product: -L unchanged byte for byte, -R with
// the R stripped and every other flag the same.
func TestSpawnArgv_Reverse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec string
		want string
	}{
		{name: "local forward unchanged", spec: specWeb, want: argvWeb},
		{name: "local forward with bind address unchanged", spec: "127.0.0.1:3000:localhost:3000", want: "ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -L 127.0.0.1:3000:localhost:3000 ubuntu"},
		{name: "reverse forward", spec: specRev, want: argvRev},
		{name: "reverse forward with remote bind address", spec: "Rlocalhost:2222:localhost:22", want: "ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -R localhost:2222:localhost:22 ubuntu"},
		{name: "reverse forward to another host", spec: "R8080:192.0.2.10:80", want: "ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -R 8080:192.0.2.10:80 ubuntu"},
		{name: "R with no remote port is not reverse", spec: "Rbox", want: "ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -L Rbox ubuntu"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := strings.Join(tunnel.SpawnArgv("ubuntu", tt.spec), " "); got != tt.want {
				t.Errorf("SpawnArgv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReverse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		spec     string
		wantPort string
		wantOK   bool
	}{
		{name: "port host port", spec: "R2222:localhost:22", wantPort: "2222", wantOK: true},
		{name: "bind port host port", spec: "Rlocalhost:2222:localhost:22", wantPort: "2222", wantOK: true},
		{name: "empty bind", spec: "R:2222:localhost:22", wantPort: "2222", wantOK: true},
		{name: "bare port", spec: "R2222", wantPort: "2222", wantOK: true},
		{name: "local forward", spec: specWeb},
		{name: "local forward with bind", spec: "127.0.0.1:3000:localhost:3000"},
		{name: "R alone", spec: "R"},
		{name: "no remote port", spec: "Rbox:web:22"},
		{name: "lowercase r is not reverse", spec: "r2222:localhost:22"},
		{name: "empty", spec: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			port, ok := tunnel.Reverse(tt.spec)
			if port != tt.wantPort || ok != tt.wantOK {
				t.Errorf("Reverse(%q) = %q, %v, want %q, %v", tt.spec, port, ok, tt.wantPort, tt.wantOK)
			}
		})
	}
}

// Spawn for a reverse spec: no pre-dial, no handshake dial; the process
// surviving the settle window is success, exiting first is failure with
// its stderr, and a ctx that ends first kills it.
func TestEngine_Spawn_Reverse(t *testing.T) {
	t.Parallel()
	recorded := `{"forwards":[{"set":"mac","spec":"R2222:localhost:22","pid":1000}]}`
	tests := []struct {
		name          string
		host          string
		spawner       func(s *fakespawn.Fake)
		cancelOnSpawn bool
		wantErr       string // substring; "" = success
		wantCtxErr    bool
		wantSpawn     bool
		wantKilled    bool
		wantState     string
	}{
		{
			name:      "process survives the settle window: success, pid recorded",
			wantSpawn: true,
			wantState: recorded,
		},
		{
			name: "remote bind refused: the process exits, its stderr is the error",
			spawner: func(s *fakespawn.Fake) {
				s.Exits("Error: remote port forwarding failed for listen port 2222\n", strings.Fields(argvRev)...)
			},
			wantErr:   argvRev + ": Error: remote port forwarding failed for listen port 2222",
			wantSpawn: true,
		},
		{
			name:      "process exits silently: a stand-in message",
			spawner:   func(s *fakespawn.Fake) { s.Exits("", strings.Fields(argvRev)...) },
			wantErr:   "exited before listening",
			wantSpawn: true,
		},
		{
			name:          "context ends while settling: killed, not recorded",
			cancelOnSpawn: true,
			wantErr:       "not settled",
			wantCtxErr:    true,
			wantSpawn:     true,
			wantKilled:    true,
		},
		{
			name:          "context ends and the process survives SIGTERM: recorded",
			spawner:       func(s *fakespawn.Fake) { s.Stubborn = true },
			cancelOnSpawn: true,
			wantErr:       "pid 1000 survived SIGTERM (recorded)",
			wantCtxErr:    true,
			wantSpawn:     true,
			wantKilled:    true,
			wantState:     recorded,
		},
		{
			name:      "spawner failure is reported with the argv",
			spawner:   func(s *fakespawn.Fake) { s.SpawnErr = errors.New("no ssh binary") },
			wantErr:   argvRev + ": no ssh binary",
			wantSpawn: true,
		},
		{
			name:    "host alias that walks out of the dir is refused before spawning",
			host:    "../evil",
			wantErr: "not a valid state file name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := fakespawn.New()
			if tt.spawner != nil {
				tt.spawner(s)
			}
			// A foreign listener on the same port number here must not
			// matter: the reverse listener is on the remote host.
			d := fakedial.New().Up("127.0.0.1:2222", time.Millisecond)
			e := engine(t, s, d)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelOnSpawn {
				s.OnSpawn = cancel
			}
			host := "ubuntu"
			if tt.host != "" {
				host = tt.host
			}
			err := e.Spawn(ctx, host, "mac", specRev)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Spawn() err = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Spawn() err = %v, want containing %q", err, tt.wantErr)
			}
			if tt.wantCtxErr && !errors.Is(err, context.Canceled) {
				t.Errorf("Spawn() err = %v, want wrapping context.Canceled", err)
			}
			if errors.Is(err, tunnel.ErrStateWrite) {
				t.Errorf("Spawn() err = %v reads as ErrStateWrite", err)
			}
			var wantSpawned []string
			if tt.wantSpawn {
				wantSpawned = []string{argvRev}
			}
			if got := s.Spawned(); !reflect.DeepEqual(got, wantSpawned) {
				t.Errorf("spawned = %q, want %q", got, wantSpawned)
			}
			if tt.wantSpawn {
				if got, want := s.LogPaths(), []string{filepath.Join(e.StateDir, "ubuntu.R2222.log")}; !reflect.DeepEqual(got, want) {
					t.Errorf("stderr log = %q, want %q (never a -L forward's ubuntu.2222.log)", got, want)
				}
			}
			if got := len(s.Killed()) > 0; got != tt.wantKilled {
				t.Errorf("killed = %v, want %v", s.Killed(), tt.wantKilled)
			}
			if got := d.Calls(); len(got) != 0 {
				t.Errorf("dialled %q, want nothing: a reverse forward has no local port", got)
			}
			if got := readState(t, e, "ubuntu"); got != tt.wantState {
				t.Errorf("state = %s, want %s", got, tt.wantState)
			}
		})
	}
}

// A process that dies DURING the settle window is a failure, not a
// success recorded a moment too early.
func TestEngine_Spawn_ReverseDiesWhileSettling(t *testing.T) {
	t.Parallel()
	s := fakespawn.New()
	e := engine(t, s, fakedial.New())
	e.Timeout = 10 * time.Second // a window the kill below lands inside
	errc := make(chan error, 1)
	go func() { errc <- e.Spawn(context.Background(), "ubuntu", "mac", specRev) }()
	for len(s.Spawned()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := s.Kill(1000); err != nil {
		t.Fatalf("Kill() err = %v", err)
	}
	err := <-errc
	if err == nil || !strings.Contains(err.Error(), argvRev) {
		t.Fatalf("Spawn() err = %v, want the argv and its last words", err)
	}
	if got := readState(t, e, "ubuntu"); got != "" {
		t.Errorf("state = %s, want nothing recorded", got)
	}
}

func TestEngine_Spawn_ReverseAlreadyCancelled(t *testing.T) {
	t.Parallel()
	s := fakespawn.New()
	e := engine(t, s, fakedial.New())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Spawn(ctx, "ubuntu", "mac", specRev); !errors.Is(err, context.Canceled) {
		t.Errorf("Spawn() err = %v, want context.Canceled", err)
	}
	if got := s.Spawned(); len(got) != 0 {
		t.Errorf("spawned %q, want nothing", got)
	}
}

func TestEngine_Spawn_ReverseStateDirBlocked(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	s := fakespawn.New()
	e := engine(t, s, fakedial.New())
	e.StateDir = blocker
	if err := e.Spawn(context.Background(), "ubuntu", "mac", specRev); err == nil {
		t.Error("Spawn() err = nil, want the mkdir failure")
	}
	if got := s.Spawned(); len(got) != 0 {
		t.Errorf("spawned %q, want nothing", got)
	}
}

// Status for a reverse spec: Up iff a recorded pid is alive, else Down —
// never Listening, whatever answers on the same port number locally.
func TestEngine_Status_Reverse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		state     string
		live      []int
		foreign   bool // something answers on 127.0.0.1:2222
		wantState tunnel.Status
		wantPid   int
	}{
		{name: "recorded and alive: up", state: `{"forwards":[{"set":"mac","spec":"R2222:localhost:22","pid":41}]}`, live: []int{41}, wantState: tunnel.Up, wantPid: 41},
		{name: "recorded and alive, foreign local listener: still up", state: `{"forwards":[{"set":"mac","spec":"R2222:localhost:22","pid":41}]}`, live: []int{41}, foreign: true, wantState: tunnel.Up, wantPid: 41},
		{name: "recorded, dead: down", state: `{"forwards":[{"set":"mac","spec":"R2222:localhost:22","pid":41}]}`, wantState: tunnel.Down},
		{name: "recorded, dead, foreign local listener: down, never listening", state: `{"forwards":[{"set":"mac","spec":"R2222:localhost:22","pid":41}]}`, foreign: true, wantState: tunnel.Down},
		{name: "no record, foreign local listener: down", foreign: true, wantState: tunnel.Down},
		{name: "no record: down", wantState: tunnel.Down},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := fakespawn.New()
			for _, pid := range tt.live {
				s.Live(pid)
			}
			d := fakedial.New()
			if tt.foreign {
				d.Up("127.0.0.1:2222", time.Millisecond)
			}
			e := engine(t, s, d)
			if tt.state != "" {
				writeState(t, e, "ubuntu", tt.state)
			}
			st, err := e.Status(context.Background(), "ubuntu", []hostmodel.ForwardSet{{Name: "mac", Specs: []string{specRev}}})
			if err != nil {
				t.Fatalf("Status() err = %v", err)
			}
			if len(st.Forwards) != 1 {
				t.Fatalf("Status() forwards = %+v, want one row", st.Forwards)
			}
			f := st.Forwards[0]
			if f.State != tt.wantState || f.Pid != tt.wantPid {
				t.Errorf("Status() = %v pid %d, want %v pid %d", f.State, f.Pid, tt.wantState, tt.wantPid)
			}
			if !f.Reverse || f.Port != "" || f.Spec != specRev {
				t.Errorf("Status() row = %+v, want Reverse, no local port, spec %q", f, specRev)
			}
			if got := d.Calls(); len(got) != 0 {
				t.Errorf("dialled %q, want nothing for a reverse row", got)
			}
		})
	}
}

// A mixed set: SpawnSet starts one process per spec, -L and -R alike, and
// a second pass leaves both alone; a dead reverse process is respawned
// alone; KillSet ends both.
func TestEngine_SpawnSet_MixedLocalAndReverse(t *testing.T) {
	t.Parallel()
	s := fakespawn.New()
	e := engine(t, s, fakedial.New())
	specs := []string{specWeb, specRev}
	ctx := context.Background()
	if err := e.SpawnSet(ctx, "ubuntu", "mix", specs); err != nil {
		t.Fatalf("SpawnSet() err = %v", err)
	}
	if got, want := s.Spawned(), []string{argvWeb, argvRev}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spawned = %q, want %q", got, want)
	}
	want := []string{"mix/3000:localhost:3000=up", "mix/R2222:localhost:22=up"}
	desired := hostmodel.ForwardSet{Name: "mix", Specs: specs}
	if got := rows(t, e, "ubuntu", desired); !reflect.DeepEqual(got, want) {
		t.Errorf("Status() = %q, want %q", got, want)
	}

	if err := e.SpawnSet(ctx, "ubuntu", "mix", specs); err != nil {
		t.Fatalf("SpawnSet() again err = %v", err)
	}
	if got := len(s.Spawned()); got != 2 {
		t.Errorf("spawned %d processes after a second SpawnSet, want 2 (both serving)", got)
	}

	// The reverse process dies on its own: only it is respawned.
	if err := s.Kill(1001); err != nil {
		t.Fatalf("Kill() err = %v", err)
	}
	if got := rows(t, e, "ubuntu", desired); !reflect.DeepEqual(got, []string{"mix/3000:localhost:3000=up", "mix/R2222:localhost:22=down"}) {
		t.Errorf("Status() after the reverse died = %q", got)
	}
	if err := e.SpawnSet(ctx, "ubuntu", "mix", specs); err != nil {
		t.Fatalf("SpawnSet() respawn err = %v", err)
	}
	if got, want := s.Spawned(), []string{argvWeb, argvRev, argvRev}; !reflect.DeepEqual(got, want) {
		t.Errorf("spawned = %q, want %q", got, want)
	}

	if err := e.KillSet(ctx, "ubuntu", "mix", specs); err != nil {
		t.Fatalf("KillSet() err = %v", err)
	}
	if got, want := s.Killed(), []int{1001, 1000, 1002}; !reflect.DeepEqual(got, want) {
		t.Errorf("killed = %v, want %v", got, want)
	}
	if got := rows(t, e, "ubuntu", desired); !reflect.DeepEqual(got, []string{"mix/3000:localhost:3000=down", "mix/R2222:localhost:22=down"}) {
		t.Errorf("Status() after KillSet = %q", got)
	}
	if got := readState(t, e, "ubuntu"); got != `{"forwards":[]}` {
		t.Errorf("state = %s, want every named record retired", got)
	}
}
