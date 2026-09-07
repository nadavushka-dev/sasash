package tunnel_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/stats"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

const (
	specWeb = "3000:localhost:3000"
	specDB  = "5432:localhost:5432"
	argvWeb = "ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -L 3000:localhost:3000 ubuntu"
	argvDB  = "ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -L 5432:localhost:5432 ubuntu"
)

// fakeSpawner is the run.Spawner for the unit tier: pids are handed out
// in sequence from 1000, a spawn keyed by its joined argv can be scripted
// to die at once with a stderr text, Alive is a set the test controls, and
// every Kill is recorded (and, unless stubborn, flips the pid dead). When
// wired to a fakedial (engine() does it), the local port of a live spawn
// starts answering and stops when the pid is killed — the port follows the
// process, as it does for real.
type fakeSpawner struct {
	mu           sync.Mutex
	nextPid      int
	spawnErr     error
	killErr      error
	stubborn     bool              // Kill is recorded but the process lives on
	neverListens bool              // the process runs but its port never answers
	onSpawn      func()            // runs inside Spawn (a test cancels its ctx here)
	exits        map[string]string // argv key -> stderr; the process exits at start
	alive        map[int]bool
	dial         *fakedial.Fake
	ports        map[int]string // pid -> local port it answers on
	spawned      []string
	logPaths     []string
	killed       []int
}

var _ run.Spawner = (*fakeSpawner)(nil)

func newSpawner() *fakeSpawner {
	return &fakeSpawner{nextPid: 999, exits: map[string]string{}, alive: map[int]bool{}, ports: map[int]string{}}
}

// Live marks pid alive, for records a test pre-writes.
func (f *fakeSpawner) Live(pid int) *fakeSpawner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[pid] = true
	return f
}

// Exits scripts argv to die at start with stderr text.
func (f *fakeSpawner) Exits(stderr string, argv ...string) *fakeSpawner {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exits[strings.Join(argv, " ")] = stderr
	return f
}

func (f *fakeSpawner) Spawn(argv []string, stderrPath string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.Join(argv, " ")
	f.spawned = append(f.spawned, key)
	f.logPaths = append(f.logPaths, stderrPath)
	if f.onSpawn != nil {
		f.onSpawn()
	}
	if f.spawnErr != nil {
		return 0, f.spawnErr
	}
	f.nextPid++
	pid := f.nextPid
	if text, dead := f.exits[key]; dead {
		if err := os.WriteFile(stderrPath, []byte(text), 0o600); err != nil {
			return 0, err
		}
		f.alive[pid] = false
		return pid, nil
	}
	f.alive[pid] = true
	if f.dial != nil && !f.neverListens {
		// The spec follows -L; every spec here starts with its local port.
		for i, a := range argv {
			if a == "-L" && i+1 < len(argv) {
				port := strings.SplitN(argv[i+1], ":", 2)[0]
				f.ports[pid] = port
				f.dial.Up("127.0.0.1:"+port, time.Millisecond)
			}
		}
	}
	return pid, nil
}

func (f *fakeSpawner) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[pid]
}

func (f *fakeSpawner) Kill(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, pid)
	if f.killErr != nil {
		return f.killErr
	}
	if !f.stubborn {
		f.alive[pid] = false
		if port := f.ports[pid]; port != "" && f.dial != nil {
			f.dial.Down("127.0.0.1:" + port)
		}
	}
	return nil
}

func (f *fakeSpawner) Spawned() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.spawned...)
}

func (f *fakeSpawner) LogPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logPaths...)
}

func (f *fakeSpawner) Killed() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.killed...)
}

// engine wires the fakes with a permissive runner (the interactive master
// check answers alive unless a test stubs otherwise).
func engine(t *testing.T, s *fakeSpawner, d *fakedial.Fake) tunnel.Engine {
	t.Helper()
	r := fakerun.New()
	r.Unmatched = &fakerun.Reply{Result: run.Result{Code: 0}}
	s.dial = d
	return tunnel.Engine{Runner: r, Spawner: s, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}
}

// writeState pre-writes the cache as a previous sasash would have left it.
func writeState(t *testing.T, e tunnel.Engine, host, content string) {
	t.Helper()
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(e.StateDir, host+".json"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
}

func readState(t *testing.T, e tunnel.Engine, host string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.StateDir, host+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	return string(b)
}

// rows renders Status as "set/spec=state" strings.
func rows(t *testing.T, e tunnel.Engine, host string, desired ...hostmodel.ForwardSet) []string {
	t.Helper()
	st, err := e.Status(context.Background(), host, desired)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	var out []string
	for _, f := range st.Forwards {
		out = append(out, f.Set+"/"+f.Spec+"="+f.State.String())
	}
	return out
}

// The argv strings are the product (SPEC §4): each one exact, or a typo
// silently does nothing. The tunnel command goes to the spawner, the
// interactive-master commands to the runner, and never the other way.
func TestEngine_Argv(t *testing.T) {
	t.Parallel()
	t.Run("spawn", func(t *testing.T) {
		t.Parallel()
		s := newSpawner()
		e := engine(t, s, fakedial.New())
		if err := e.Spawn(context.Background(), "ubuntu", "web", specWeb); err != nil {
			t.Fatalf("Spawn() err = %v", err)
		}
		if got := s.Spawned(); !reflect.DeepEqual(got, []string{argvWeb}) {
			t.Errorf("spawned = %q, want exactly [%q]", got, argvWeb)
		}
		if got := e.Runner.(*fakerun.Fake).CallKeys(); len(got) != 0 {
			t.Errorf("runner saw %q, want nothing on the tunnel path", got)
		}
		if got, want := s.LogPaths(), []string{filepath.Join(e.StateDir, "ubuntu.3000.log")}; !reflect.DeepEqual(got, want) {
			t.Errorf("stderr log = %q, want %q (next to the state)", got, want)
		}
	})
	tests := []struct {
		name string
		call func(e tunnel.Engine) error
		want string
	}{
		{
			name: "check",
			call: func(e tunnel.Engine) error {
				_, err := e.CheckMaster(context.Background(), "ubuntu")
				return err
			},
			want: "ssh -O check ubuntu",
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
			s := newSpawner()
			e := engine(t, s, fakedial.New())
			if err := tt.call(e); err != nil {
				t.Fatalf("%s: err = %v", tt.name, err)
			}
			keys := e.Runner.(*fakerun.Fake).CallKeys()
			if len(keys) != 1 || keys[0] != tt.want {
				t.Errorf("argv = %q, want exactly [%q]", keys, tt.want)
			}
			if got := s.Spawned(); len(got) != 0 {
				t.Errorf("spawned %q, want nothing for a master command", got)
			}
		})
	}
}

// The Spawn handshake: reality decides, not an exit code.
func TestEngine_Spawn_Handshake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		spec          string
		host          string
		dial          func(d *fakedial.Fake)
		spawner       func(s *fakeSpawner)
		cancelOnSpawn bool   // the ctx ends once the process is running
		wantErr       string // substring; "" = success
		wantCtxErr    bool
		wantSpawn     bool
		wantKilled    bool
		wantState     string
	}{
		{
			name:      "port answers once the process runs: success, pid recorded",
			spec:      specWeb,
			dial:      func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			wantSpawn: true,
			wantState: `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":1000}]}`,
		},
		{
			name:      "process exits: error is its stderr, nothing recorded",
			spec:      specWeb,
			dial:      func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			spawner:   func(s *fakeSpawner) { s.Exits("Permission denied (publickey).\n", strings.Fields(argvWeb)...) },
			wantErr:   argvWeb + ": Permission denied (publickey).",
			wantSpawn: true,
		},
		{
			name:      "process exits silently: a stand-in message",
			spec:      specWeb,
			dial:      func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			spawner:   func(s *fakeSpawner) { s.Exits("", strings.Fields(argvWeb)...) },
			wantErr:   "exited before listening",
			wantSpawn: true,
		},
		{
			name:    "a local port that already answers is refused before spawning",
			spec:    specWeb,
			dial:    func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			wantErr: "local port 3000 already answers",
		},
		{
			name:          "context ends while connecting: killed, not recorded",
			spec:          specWeb,
			dial:          func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			spawner:       func(s *fakeSpawner) { s.neverListens = true },
			cancelOnSpawn: true,
			wantErr:       "not listening on 3000",
			wantCtxErr:    true,
			wantSpawn:     true,
			wantKilled:    true,
		},
		{
			name:    "unparseable local port is refused before spawning",
			spec:    "[::1]:3000:localhost:3000",
			wantErr: "no local port to verify",
		},
		{
			name:    "host alias that walks out of the dir is refused before spawning",
			spec:    specWeb,
			host:    "../evil",
			wantErr: "not a valid state file name",
		},
		{
			name:      "spawner failure is reported with the argv",
			spec:      specWeb,
			spawner:   func(s *fakeSpawner) { s.spawnErr = errors.New("no ssh binary") },
			wantErr:   argvWeb + ": no ssh binary",
			wantSpawn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSpawner()
			if tt.spawner != nil {
				tt.spawner(s)
			}
			d := fakedial.New()
			if tt.dial != nil {
				tt.dial(d)
			}
			e := engine(t, s, d)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelOnSpawn {
				s.onSpawn = cancel
			}
			host := "ubuntu"
			if tt.host != "" {
				host = tt.host
			}
			err := e.Spawn(ctx, host, "web", tt.spec)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Spawn() err = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Spawn() err = %v, want containing %q", err, tt.wantErr)
			}
			if tt.wantCtxErr && !errors.Is(err, context.Canceled) {
				t.Errorf("Spawn() err = %v, want wrapping context.Canceled", err)
			}
			if got := len(s.Spawned()) > 0; got != tt.wantSpawn {
				t.Errorf("spawned = %v, want %v", got, tt.wantSpawn)
			}
			if got := len(s.Killed()) > 0; got != tt.wantKilled {
				t.Errorf("killed = %v, want %v", s.Killed(), tt.wantKilled)
			}
			if got := readState(t, e, "ubuntu"); got != tt.wantState {
				t.Errorf("state = %s, want %s", got, tt.wantState)
			}
		})
	}
}

// A cancelled context before anything starts spawns nothing.
func TestEngine_Spawn_AlreadyCancelled(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Spawn(ctx, "ubuntu", "web", specWeb); !errors.Is(err, context.Canceled) {
		t.Errorf("Spawn() err = %v, want context.Canceled", err)
	}
	if got := s.Spawned(); len(got) != 0 {
		t.Errorf("spawned %q, want nothing", got)
	}
}

// A state dir that cannot be created fails before the spawn: the log file
// has nowhere to go.
func TestEngine_Spawn_StateDirBlocked(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	e.StateDir = blocker
	if err := e.Spawn(context.Background(), "ubuntu", "web", specWeb); err == nil {
		t.Error("Spawn() err = nil, want the mkdir failure")
	}
	if got := s.Spawned(); len(got) != 0 {
		t.Errorf("spawned %q, want nothing", got)
	}
}

// The tunnel is running but its record could not be written: the caller
// hears ErrStateWrite and knows the process side succeeded.
func TestEngine_Spawn_ErrStateWrite(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	// A directory where the state file should be: readable as corrupt,
	// unwritable as a file.
	if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	err := e.Spawn(context.Background(), "ubuntu", "web", specWeb)
	if !errors.Is(err, tunnel.ErrStateWrite) {
		t.Errorf("Spawn() err = %v, want ErrStateWrite (the tunnel IS running)", err)
	}
	if got := s.Killed(); len(got) != 0 {
		t.Errorf("killed %v, want nothing: a record failure never ends a real tunnel", got)
	}
}

// A fresh Spawn of a spec already recorded under the set overwrites the
// pid — one entry per set+spec.
func TestEngine_Spawn_RefreshesPid(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":7}]}`)
	if err := e.Spawn(context.Background(), "ubuntu", "web", specWeb); err != nil {
		t.Fatalf("Spawn() err = %v", err)
	}
	want := `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":1000}]}`
	if got := readState(t, e, "ubuntu"); got != want {
		t.Errorf("state = %s, want %s", got, want)
	}
}

func TestEngine_Kill(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		state      string
		live       []int
		spawner    func(s *fakeSpawner)
		ctx        func() context.Context
		wantKilled []int
		wantErr    string
		wantState  string
	}{
		{
			name:       "named record: process signalled, record removed",
			state:      `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"db","spec":"5432:localhost:5432","pid":42}]}`,
			live:       []int{41, 42},
			wantKilled: []int{41},
			wantState:  `{"forwards":[{"set":"db","spec":"5432:localhost:5432","pid":42}]}`,
		},
		{
			name:       "ad-hoc record: process signalled, record kept with pid cleared",
			state:      `{"forwards":[{"set":"","spec":"3000:localhost:3000","pid":41}]}`,
			live:       []int{41},
			wantKilled: []int{41},
			wantState:  `{"forwards":[{"set":"","spec":"3000:localhost:3000"}]}`,
		},
		{
			name:      "no record: nothing to signal, nil",
			state:     `{"forwards":[{"set":"db","spec":"5432:localhost:5432","pid":42}]}`,
			live:      []int{42},
			wantState: `{"forwards":[{"set":"db","spec":"5432:localhost:5432","pid":42}]}`,
		},
		{
			name:      "recorded pid already dead: not signalled, record retired",
			state:     `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`,
			wantState: `{"forwards":[]}`,
		},
		{
			name:       "every record of the spec is signalled, whatever the set",
			state:      `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"","spec":"3000:localhost:3000","pid":43}]}`,
			live:       []int{41, 43},
			wantKilled: []int{41, 43},
			wantState:  `{"forwards":[{"set":"","spec":"3000:localhost:3000"}]}`,
		},
		{
			name:       "signal failure is returned, record still retired",
			state:      `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`,
			live:       []int{41},
			spawner:    func(s *fakeSpawner) { s.killErr = errors.New("operation not permitted") },
			wantKilled: []int{41},
			wantErr:    "pid 41: operation not permitted",
			wantState:  `{"forwards":[]}`,
		},
		{
			name:    "process outlives the wait: error, record kept",
			state:   `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`,
			live:    []int{41},
			spawner: func(s *fakeSpawner) { s.stubborn = true },
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantKilled: []int{41},
			wantErr:    "pid 41 still running after SIGTERM",
			wantState:  `{"forwards":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSpawner()
			for _, pid := range tt.live {
				s.Live(pid)
			}
			if tt.spawner != nil {
				tt.spawner(s)
			}
			e := engine(t, s, fakedial.New())
			writeState(t, e, "ubuntu", tt.state)
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}
			err := e.Kill(ctx, "ubuntu", specWeb)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Kill() err = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Kill() err = %v, want containing %q", err, tt.wantErr)
			}
			if got := s.Killed(); !reflect.DeepEqual(got, tt.wantKilled) {
				t.Errorf("killed = %v, want %v", got, tt.wantKilled)
			}
			if got := readState(t, e, "ubuntu"); got != tt.wantState {
				t.Errorf("state = %s, want %s", got, tt.wantState)
			}
		})
	}
}

// The "still running" case above retires the record anyway? No — re-read
// the table: the record IS retired because the mutation is the intent and
// the cache is never truth; reality (Status: pid alive + port) re-derives
// it. This pins that a Kill error and a state error both surface.
func TestEngine_Kill_JoinsStateError(t *testing.T) {
	t.Parallel()
	s := newSpawner().Live(41)
	s.killErr = errors.New("nope")
	e := engine(t, s, fakedial.New())
	if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	err := e.Kill(context.Background(), "ubuntu", specWeb)
	if !errors.Is(err, tunnel.ErrStateWrite) {
		t.Errorf("Kill() err = %v, want ErrStateWrite joined", err)
	}
}

func TestEngine_KillAll(t *testing.T) {
	t.Parallel()
	s := newSpawner().Live(41).Live(43)
	e := engine(t, s, fakedial.New())
	writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"db","spec":"5432:localhost:5432","pid":42},{"set":"","spec":"8080:localhost:80","pid":43}]}`)
	if err := e.KillAll(context.Background(), "ubuntu"); err != nil {
		t.Fatalf("KillAll() err = %v", err)
	}
	// 42 is dead already: not signalled.
	if got := s.Killed(); !reflect.DeepEqual(got, []int{41, 43}) {
		t.Errorf("killed = %v, want [41 43]", got)
	}
	if got := readState(t, e, "ubuntu"); got != `{"forwards":null}` {
		t.Errorf("state = %s, want everything cleared, ad-hoc included", got)
	}
	// The interactive master is not touched.
	if got := e.Runner.(*fakerun.Fake).CallKeys(); len(got) != 0 {
		t.Errorf("runner saw %q, want nothing", got)
	}
}

func TestEngine_KillAll_ErrStateWrite(t *testing.T) {
	t.Parallel()
	e := engine(t, newSpawner(), fakedial.New())
	if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	if err := e.KillAll(context.Background(), "ubuntu"); !errors.Is(err, tunnel.ErrStateWrite) {
		t.Errorf("KillAll() err = %v, want ErrStateWrite", err)
	}
}

// Up on {two already up, one new} spawns exactly one process and signals
// nothing: adding a port disturbs nothing.
func TestEngine_SpawnSet_AddsOnlyWhatIsMissing(t *testing.T) {
	t.Parallel()
	s := newSpawner().Live(41).Live(42)
	d := fakedial.New().
		Up("127.0.0.1:3000", time.Millisecond).
		Up("127.0.0.1:5432", time.Millisecond)
	e := engine(t, s, d)
	writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"web","spec":"5432:localhost:5432","pid":42}]}`)

	if err := e.SpawnSet(context.Background(), "ubuntu", "web", []string{specWeb, specDB, "8080:localhost:80"}); err != nil {
		t.Fatalf("SpawnSet() err = %v", err)
	}
	want := []string{"ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -L 8080:localhost:80 ubuntu"}
	if got := s.Spawned(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned = %q, want exactly %q", got, want)
	}
	if got := s.Killed(); len(got) != 0 {
		t.Errorf("killed = %v, want nothing", got)
	}
	wantState := `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"web","spec":"5432:localhost:5432","pid":42},{"set":"web","spec":"8080:localhost:80","pid":1000}]}`
	if got := readState(t, e, "ubuntu"); got != wantState {
		t.Errorf("state = %s, want %s", got, wantState)
	}
}

// A recorded process that is alive but whose port is dead, or a remembered
// record without a pid, is NOT "serving": SpawnSet spawns. A port that
// answers with no live process of ours is refused before spawning — the
// pre-check, never a silent remap.
func TestEngine_SpawnSet_RespawnsWhatIsNotServing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		live      bool
		dial      func(d *fakedial.Fake)
		state     string
		wantErr   string
		wantSpawn bool
	}{
		{
			name:      "alive process, dead port: respawned",
			live:      true,
			dial:      func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			state:     `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`,
			wantSpawn: true,
		},
		{
			name:    "dead process, port answers (someone else): refused before spawning",
			dial:    func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			state:   `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`,
			wantErr: "local port 3000 already answers",
		},
		{
			name:      "remembered ad-hoc record without a pid: respawned",
			dial:      func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			state:     `{"forwards":[{"set":"","spec":"3000:localhost:3000"}]}`,
			wantSpawn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSpawner()
			if tt.live {
				s.Live(41)
			}
			d := fakedial.New()
			tt.dial(d)
			e := engine(t, s, d)
			writeState(t, e, "ubuntu", tt.state)
			err := e.SpawnSet(context.Background(), "ubuntu", "web", []string{specWeb})
			if tt.wantErr == "" && err != nil {
				t.Fatalf("SpawnSet() err = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("SpawnSet() err = %v, want containing %q", err, tt.wantErr)
			}
			var wantSpawned []string
			if tt.wantSpawn {
				wantSpawned = []string{argvWeb}
			}
			if got := s.Spawned(); !reflect.DeepEqual(got, wantSpawned) {
				t.Errorf("spawned = %q, want %q", got, wantSpawned)
			}
		})
	}
}

func TestEngine_SpawnSet_FirstErrorAborts(t *testing.T) {
	t.Parallel()
	s := newSpawner().Exits("Permission denied (publickey).", strings.Fields(argvDB)...)
	e := engine(t, s, fakedial.New())
	err := e.SpawnSet(context.Background(), "ubuntu", "web", []string{specWeb, specDB, "8080:localhost:80"})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("SpawnSet() err = %v, want the second spawn's failure", err)
	}
	if got := s.Spawned(); !reflect.DeepEqual(got, []string{argvWeb, argvDB}) {
		t.Errorf("spawned = %q, want the first two only", got)
	}
	// The first process stays: it is real.
	if got := s.Killed(); len(got) != 0 {
		t.Errorf("killed = %v, want nothing", got)
	}
	want := `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":1000}]}`
	if got := readState(t, e, "ubuntu"); got != want {
		t.Errorf("state = %s, want %s", got, want)
	}
}

// B1 survives Model B: a record failure never stops real tunnels.
func TestEngine_SpawnSet_ContinuesOnStateWriteFailure(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	err := e.SpawnSet(context.Background(), "ubuntu", "web", []string{specWeb, specDB})
	if !errors.Is(err, tunnel.ErrStateWrite) {
		t.Fatalf("SpawnSet() err = %v, want ErrStateWrite", err)
	}
	if got := s.Spawned(); !reflect.DeepEqual(got, []string{argvWeb, argvDB}) {
		t.Errorf("spawned = %q, want both despite the record failures", got)
	}
}

func TestEngine_SpawnSet_Empty(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	if err := e.SpawnSet(context.Background(), "ubuntu", "", nil); err != nil {
		t.Errorf("SpawnSet() err = %v, want nil", err)
	}
	if got := s.Spawned(); len(got) != 0 {
		t.Errorf("spawned %q, want nothing", got)
	}
}

func TestEngine_KillSet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		set        string
		specs      []string
		state      string
		live       []int
		wantKilled []int
		wantState  string
	}{
		{
			name:       "named set: desired plus recorded killed, records cleared, others untouched",
			set:        "web",
			specs:      []string{specWeb, specWeb},
			state:      `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"web","spec":"5432:localhost:5432","pid":42},{"set":"db","spec":"6000:localhost:6000","pid":43}]}`,
			live:       []int{41, 42, 43},
			wantKilled: []int{41, 42},
			wantState:  `{"forwards":[{"set":"db","spec":"6000:localhost:6000","pid":43}]}`,
		},
		{
			name:       "ad-hoc set: killed, records kept with pids cleared",
			set:        "",
			specs:      []string{specWeb},
			state:      `{"forwards":[{"set":"","spec":"3000:localhost:3000","pid":41},{"set":"","spec":"5432:localhost:5432","pid":42}]}`,
			live:       []int{41, 42},
			wantKilled: []int{41, 42},
			wantState:  `{"forwards":[{"set":"","spec":"3000:localhost:3000"},{"set":"","spec":"5432:localhost:5432"}]}`,
		},
		{
			name:      "nothing recorded and nothing alive: no signals",
			set:       "web",
			specs:     []string{specWeb},
			state:     "",
			wantState: `{"forwards":null}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSpawner()
			for _, pid := range tt.live {
				s.Live(pid)
			}
			e := engine(t, s, fakedial.New())
			if tt.state != "" {
				writeState(t, e, "ubuntu", tt.state)
			}
			if err := e.KillSet(context.Background(), "ubuntu", tt.set, tt.specs); err != nil {
				t.Fatalf("KillSet() err = %v", err)
			}
			if got := s.Killed(); !reflect.DeepEqual(got, tt.wantKilled) {
				t.Errorf("killed = %v, want %v", got, tt.wantKilled)
			}
			if got := readState(t, e, "ubuntu"); got != tt.wantState {
				t.Errorf("state = %s, want %s", got, tt.wantState)
			}
		})
	}
}

func TestEngine_KillSet_JoinsErrors(t *testing.T) {
	t.Parallel()
	s := newSpawner().Live(41).Live(42)
	s.killErr = errors.New("operation not permitted")
	e := engine(t, s, fakedial.New())
	writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"web","spec":"5432:localhost:5432","pid":42}]}`)
	err := e.KillSet(context.Background(), "ubuntu", "web", nil)
	if err == nil || !strings.Contains(err.Error(), "pid 41") || !strings.Contains(err.Error(), "pid 42") {
		t.Errorf("KillSet() err = %v, want both failures joined", err)
	}
	if got := readState(t, e, "ubuntu"); got != `{"forwards":[]}` {
		t.Errorf("state = %s, want the set's records cleared regardless", got)
	}

	t.Run("state failure surfaces as ErrStateWrite", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		if err := e.KillSet(context.Background(), "ubuntu", "", []string{specWeb}); !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("KillSet() err = %v, want ErrStateWrite", err)
		}
	})
}

// The ad-hoc set's record is its declaration: a down keeps the row (Down),
// and Up brings it back with a fresh process.
func TestEngine_KillSet_AdhocRecordSurvivesAndReups(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	ctx := context.Background()

	if err := e.SpawnSet(ctx, "ubuntu", "", []string{specWeb}); err != nil {
		t.Fatalf("SpawnSet() err = %v", err)
	}
	if got := rows(t, e, "ubuntu"); !reflect.DeepEqual(got, []string{"/3000:localhost:3000=up"}) {
		t.Fatalf("rows after SpawnSet = %v", got)
	}
	if err := e.KillSet(ctx, "ubuntu", "", []string{specWeb}); err != nil {
		t.Fatalf("KillSet() err = %v", err)
	}
	if got := rows(t, e, "ubuntu"); !reflect.DeepEqual(got, []string{"/3000:localhost:3000=down"}) {
		t.Errorf("rows after ad-hoc down = %v, want the row kept, down", got)
	}
	if err := e.SpawnSet(ctx, "ubuntu", "", []string{specWeb}); err != nil {
		t.Fatalf("second SpawnSet() err = %v", err)
	}
	if got := rows(t, e, "ubuntu"); !reflect.DeepEqual(got, []string{"/3000:localhost:3000=up"}) {
		t.Errorf("rows after re-up = %v, want up again", got)
	}
	if got := s.Spawned(); len(got) != 2 {
		t.Errorf("spawned %d processes, want 2 (one per SpawnSet)", len(got))
	}
	// Forget purges it; the row is gone.
	if err := e.Forget("ubuntu", specWeb); err != nil {
		t.Fatalf("Forget() err = %v", err)
	}
	if got := rows(t, e, "ubuntu"); got != nil {
		t.Errorf("rows after Forget = %v, want none", got)
	}
}

// Re-plumb (the E key): Kill old, Spawn new, Forget old — without the
// Forget the old ad-hoc spec lingers as a dead row.
func TestEngine_Replumb_ForgetDropsTheOldAdhocSpec(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	ctx := context.Background()
	if err := e.Spawn(ctx, "ubuntu", "", specWeb); err != nil {
		t.Fatalf("Spawn() err = %v", err)
	}
	if err := e.Kill(ctx, "ubuntu", specWeb); err != nil {
		t.Fatalf("Kill() err = %v", err)
	}
	if err := e.Spawn(ctx, "ubuntu", "", "3001:localhost:3000"); err != nil {
		t.Fatalf("Spawn(new) err = %v", err)
	}
	if got := rows(t, e, "ubuntu"); len(got) != 2 {
		t.Fatalf("rows before Forget = %v, want the old spec lingering", got)
	}
	if err := e.Forget("ubuntu", specWeb); err != nil {
		t.Fatalf("Forget() err = %v", err)
	}
	if got := rows(t, e, "ubuntu"); !reflect.DeepEqual(got, []string{"/3001:localhost:3000=up"}) {
		t.Errorf("rows after Forget = %v, want only the new spec", got)
	}
}

func TestEngine_Forget(t *testing.T) {
	t.Parallel()
	t.Run("drops every record of the spec, any set, signals nothing", func(t *testing.T) {
		t.Parallel()
		s := newSpawner().Live(41)
		e := engine(t, s, fakedial.New())
		writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"","spec":"3000:localhost:3000"},{"set":"db","spec":"5432:localhost:5432","pid":42}]}`)
		if err := e.Forget("ubuntu", specWeb); err != nil {
			t.Fatalf("Forget() err = %v", err)
		}
		if got := readState(t, e, "ubuntu"); got != `{"forwards":[{"set":"db","spec":"5432:localhost:5432","pid":42}]}` {
			t.Errorf("state = %s", got)
		}
		if got := s.Killed(); len(got) != 0 {
			t.Errorf("killed = %v, want nothing", got)
		}
	})
	t.Run("state failure wraps ErrStateWrite", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.json"), 0o700); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		if err := e.Forget("ubuntu", specWeb); !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Forget() err = %v, want ErrStateWrite", err)
		}
	})
	t.Run("host alias that walks out of the dir", func(t *testing.T) {
		t.Parallel()
		e := engine(t, newSpawner(), fakedial.New())
		if err := e.Forget("../evil", specWeb); !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Forget() err = %v, want ErrStateWrite", err)
		}
		if _, statErr := os.Lstat(filepath.Join(e.StateDir, "..", "evil.json")); statErr == nil {
			t.Error("state file escaped StateDir")
		}
	})
}

func TestEngine_CheckMaster(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reply   fakerun.Reply
		want    bool
		wantErr bool
	}{
		{name: "exit zero is alive", reply: fakerun.Reply{Result: run.Result{Code: 0}}, want: true},
		{name: "non-zero exit is data not error", reply: fakerun.Reply{Result: run.Result{Code: 255}}},
		{name: "seam failure is an error", reply: fakerun.Reply{Err: errors.New("no ssh binary")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := engine(t, newSpawner(), fakedial.New())
			e.Runner = fakerun.New().Stub(tt.reply, "ssh", "-O", "check", "ubuntu")
			got, err := e.CheckMaster(context.Background(), "ubuntu")
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("CheckMaster() = %v, %v, want %v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// Exit is the interactive master's: ssh's refusal comes back verbatim, and
// tunnel records are never touched either way.
func TestEngine_Exit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reply   fakerun.Reply
		wantErr string
	}{
		{name: "success", reply: fakerun.Reply{Result: run.Result{Code: 0}}},
		{name: "refusal carries ssh's stderr", reply: fakerun.Reply{Result: run.Result{Code: 255, Stderr: []byte("  not running\n")}}, wantErr: "ssh -O exit ubuntu: not running"},
		{name: "silent refusal names the code", reply: fakerun.Reply{Result: run.Result{Code: 3}}, wantErr: "ssh -O exit ubuntu: exit 3"},
		{name: "seam failure", reply: fakerun.Reply{Err: errors.New("boom")}, wantErr: "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := engine(t, newSpawner(), fakedial.New())
			e.Runner = fakerun.New().Stub(tt.reply, "ssh", "-O", "exit", "ubuntu")
			const state = `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`
			writeState(t, e, "ubuntu", state)
			err := e.Exit(context.Background(), "ubuntu")
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Exit() err = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("Exit() err = %v, want %q", err, tt.wantErr)
			}
			if got := readState(t, e, "ubuntu"); got != state {
				t.Errorf("state = %s, want untouched", got)
			}
		})
	}
}

// The reconciliation matrix (CONTRACT-tunnel §6): pid alive x port dial.
func TestEngine_Status_Matrix(t *testing.T) {
	t.Parallel()
	web := []hostmodel.ForwardSet{{Name: "web", Specs: []string{specWeb}}}
	const recorded = `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`
	tests := []struct {
		name    string
		live    []int
		dial    func(d *fakedial.Fake)
		desired []hostmodel.ForwardSet
		state   string // raw state file content; "" = no file
		want    []tunnel.Forward
		corrupt bool
	}{
		{
			name:    "pid alive and port answers is up, with the pid",
			live:    []int{41},
			dial:    func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired: web,
			state:   recorded,
			want:    []tunnel.Forward{{Set: "web", Spec: specWeb, Port: "3000", Pid: 41, State: tunnel.Up}},
		},
		{
			name:    "pid dead and port answers is listening: not ours",
			dial:    func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired: web,
			state:   recorded,
			want:    []tunnel.Forward{{Set: "web", Spec: specWeb, Port: "3000", State: tunnel.Listening}},
		},
		{
			name:    "pid alive but port dead is down: the process serves nothing",
			live:    []int{41},
			dial:    func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			desired: web,
			state:   recorded,
			want:    []tunnel.Forward{{Set: "web", Spec: specWeb, Port: "3000", State: tunnel.Down}},
		},
		{
			name:    "pid dead and port dead is down",
			dial:    func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			desired: web,
			state:   recorded,
			want:    []tunnel.Forward{{Set: "web", Spec: specWeb, Port: "3000", State: tunnel.Down}},
		},
		{
			name:    "no state: a desired forward someone runs is listening, never up",
			dial:    func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired: web,
			want:    []tunnel.Forward{{Set: "web", Spec: specWeb, Port: "3000", State: tunnel.Listening}},
		},
		{
			name:    "no state and port dead is down",
			dial:    func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			desired: web,
			want:    []tunnel.Forward{{Set: "web", Spec: specWeb, Port: "3000", State: tunnel.Down}},
		},
		{
			name:    "unparseable local port is down and never dialed",
			dial:    func(d *fakedial.Fake) {},
			desired: []hostmodel.ForwardSet{{Name: "web", Specs: []string{"[::1]:3000:localhost:3000"}}},
			want:    []tunnel.Forward{{Set: "web", Spec: "[::1]:3000:localhost:3000", State: tunnel.Down}},
		},
		{
			name:    "bind-address spec parses the second field",
			live:    []int{41},
			dial:    func(d *fakedial.Fake) { d.Up("127.0.0.1:3000", time.Millisecond) },
			desired: []hostmodel.ForwardSet{{Name: "web", Specs: []string{"127.0.0.1:3000:localhost:3000"}}},
			state:   `{"forwards":[{"set":"web","spec":"127.0.0.1:3000:localhost:3000","pid":41}]}`,
			want:    []tunnel.Forward{{Set: "web", Spec: "127.0.0.1:3000:localhost:3000", Port: "3000", Pid: 41, State: tunnel.Up}},
		},
		{
			name: "desired first in meta order, then extras attributed to their set, deduplicated",
			live: []int{41, 43},
			dial: func(d *fakedial.Fake) {
				d.Up("127.0.0.1:3000", time.Millisecond).Down("127.0.0.1:5432").Up("127.0.0.1:8080", time.Millisecond)
			},
			desired: []hostmodel.ForwardSet{{Name: "web", Specs: []string{specWeb}}, {Name: "db", Specs: []string{specDB}}},
			state:   `{"forwards":[{"set":"","spec":"8080:localhost:80","pid":43},{"set":"old","spec":"3000:localhost:3000","pid":41}]}`,
			want: []tunnel.Forward{
				{Set: "web", Spec: specWeb, Port: "3000", Pid: 41, State: tunnel.Up},
				{Set: "db", Spec: specDB, Port: "5432", State: tunnel.Down},
				{Set: "", Spec: "8080:localhost:80", Port: "8080", Pid: 43, State: tunnel.Up},
			},
		},
		{
			name:  "remembered ad-hoc record without a pid is a down row",
			dial:  func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			state: `{"forwards":[{"set":"","spec":"3000:localhost:3000"}]}`,
			want:  []tunnel.Forward{{Set: "", Spec: specWeb, Port: "3000", State: tunnel.Down}},
		},
		{
			name:    "corrupt state is empty state plus the flag",
			dial:    func(d *fakedial.Fake) { d.Down("127.0.0.1:3000") },
			desired: web,
			state:   "{nope",
			want:    []tunnel.Forward{{Set: "web", Spec: specWeb, Port: "3000", State: tunnel.Down}},
			corrupt: true,
		},
		{
			name: "nothing desired and nothing recorded",
			dial: func(d *fakedial.Fake) {},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newSpawner()
			for _, pid := range tt.live {
				s.Live(pid)
			}
			d := fakedial.New()
			tt.dial(d)
			e := engine(t, s, d)
			if tt.state != "" {
				writeState(t, e, "ubuntu", tt.state)
			}
			got, err := e.Status(context.Background(), "ubuntu", tt.desired)
			if err != nil {
				t.Fatalf("Status() err = %v", err)
			}
			if got.Host != "ubuntu" || !got.Master {
				t.Errorf("Status() host/master = %q/%v, want ubuntu/true (the runner says alive)", got.Host, got.Master)
			}
			if got.CorruptState != tt.corrupt {
				t.Errorf("Status().CorruptState = %v, want %v", got.CorruptState, tt.corrupt)
			}
			if !reflect.DeepEqual(got.Forwards, tt.want) {
				t.Errorf("Status().Forwards = %+v, want %+v", got.Forwards, tt.want)
			}
			// Status is read-only over the tunnel state.
			if after := readState(t, e, "ubuntu"); after != tt.state {
				t.Errorf("state after Status = %s, want untouched %s", after, tt.state)
			}
		})
	}
}

// The interactive master is an indicator only: the same forwards read the
// same with it down.
func TestEngine_Status_MasterIsOnlyAnIndicator(t *testing.T) {
	t.Parallel()
	s := newSpawner().Live(41)
	e := engine(t, s, fakedial.New().Up("127.0.0.1:3000", time.Millisecond))
	e.Runner = fakerun.New().StubExit(255, "ssh", "-O", "check", "ubuntu")
	writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`)
	got, err := e.Status(context.Background(), "ubuntu", nil)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if got.Master {
		t.Error("Status().Master = true, want false")
	}
	if len(got.Forwards) != 1 || got.Forwards[0].State != tunnel.Up {
		t.Errorf("Status().Forwards = %+v, want up regardless of the master", got.Forwards)
	}
}

func TestEngine_Status_UnreadableState(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("chmod 000 does not stop root")
	}
	e := engine(t, newSpawner(), fakedial.New().Up("127.0.0.1:3000", time.Millisecond))
	if err := os.WriteFile(filepath.Join(e.StateDir, "ubuntu.json"), []byte(`{"forwards":[]}`), 0o000); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	got, err := e.Status(context.Background(), "ubuntu", []hostmodel.ForwardSet{{Name: "web", Specs: []string{specWeb}}})
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if !got.CorruptState {
		t.Error("Status().CorruptState = false, want true for an unreadable file")
	}
	if len(got.Forwards) != 1 || got.Forwards[0].State != tunnel.Listening {
		t.Errorf("Status().Forwards = %+v, want desired reconciled from reality", got.Forwards)
	}
}

func TestEngine_Status_InvalidHostHasNoState(t *testing.T) {
	t.Parallel()
	got, err := engine(t, newSpawner(), fakedial.New()).Status(context.Background(), "../evil", nil)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if got.CorruptState || got.Forwards != nil {
		t.Errorf("Status() = %+v, want empty state for an invalid alias", got)
	}
}

// Zero-value Spawner, Dialer and Timeout fall back to the real ones: our
// own pid is alive, a pid no process has is dead, and a just-freed local
// port reads as refused.
func TestEngine_Status_Defaults(t *testing.T) {
	t.Parallel()
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
	writeState(t, e, "ubuntu", fmt.Sprintf(`{"forwards":[{"set":"web","spec":%q,"pid":%d},{"set":"gone","spec":"1:localhost:1","pid":%d}]}`, spec, os.Getpid(), math.MaxInt32))
	got, err := e.Status(context.Background(), "ubuntu", nil)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	want := []tunnel.Forward{
		{Set: "web", Spec: spec, Port: fmt.Sprint(port), State: tunnel.Down},
		{Set: "gone", Spec: "1:localhost:1", Port: "1", State: tunnel.Down},
	}
	if !reflect.DeepEqual(got.Forwards, want) {
		t.Errorf("Status().Forwards = %+v, want %+v", got.Forwards, want)
	}

	// The real spawner sees our own pid as alive: with the port answering
	// (a listener we hold) that is Up.
	l2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	defer l2.Close()
	port2 := l2.Addr().(*net.TCPAddr).Port
	spec2 := fmt.Sprintf("%d:localhost:%d", port2, port2)
	writeState(t, e, "ubuntu", fmt.Sprintf(`{"forwards":[{"set":"web","spec":%q,"pid":%d}]}`, spec2, os.Getpid()))
	got, err = e.Status(context.Background(), "ubuntu", nil)
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	if len(got.Forwards) != 1 || got.Forwards[0].State != tunnel.Up || got.Forwards[0].Pid != os.Getpid() {
		t.Errorf("Status().Forwards = %+v, want up with our pid", got.Forwards)
	}
}

func TestEngine_Status_SeamFailure(t *testing.T) {
	t.Parallel()
	e := engine(t, newSpawner(), fakedial.New())
	e.Runner = fakerun.New().Stub(fakerun.Reply{Err: errors.New("boom")}, "ssh", "-O", "check", "ubuntu")
	if _, err := e.Status(context.Background(), "ubuntu", nil); err == nil {
		t.Error("Status() err = nil, want seam failure")
	}
}

// Stats integration (CONTRACT-stats.md): Status observes and enriches;
// Exit observes the master's down transition.
func TestEngine_Status_ObservedTimes(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	now := t0
	web := []hostmodel.ForwardSet{{Name: "web", Specs: []string{specWeb}}}

	s := newSpawner().Live(41)
	e := engine(t, s, fakedial.New().Up("127.0.0.1:3000", time.Millisecond))
	e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return now }}
	writeState(t, e, "ubuntu", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`)
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

	// A minute later the process died: the forward flips to down-since.
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

	// Another minute later the interactive master is gone too.
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

// Listening counts as port-answered: a forward whose port accepts without
// a live process of ours still gets its Since stamped.
func TestEngine_Status_ListeningStampsSince(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	e := engine(t, newSpawner(), fakedial.New().Up("127.0.0.1:3000", time.Millisecond))
	e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return t0 }}
	got, err := e.Status(context.Background(), "ubuntu", []hostmodel.ForwardSet{{Name: "web", Specs: []string{specWeb}}})
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	f := got.Forwards[0]
	if f.State != tunnel.Listening || !f.Since.Equal(t0) {
		t.Errorf("Status() forward = %+v, want listening with Since t0", f)
	}
}

func TestEngine_Exit_ObservesMasterDead(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	observedAlive := func(t *testing.T) (tunnel.Engine, *time.Time) {
		t.Helper()
		now := t0
		e := engine(t, newSpawner(), fakedial.New())
		e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return now }}
		if _, err := e.Status(context.Background(), "ubuntu", nil); err != nil { // alive at t0
			t.Fatalf("Status() err = %v", err)
		}
		now = t0.Add(time.Minute)
		return e, &now
	}

	t.Run("a successful exit stamps the down transition", func(t *testing.T) {
		t.Parallel()
		e, now := observedAlive(t)
		if err := e.Exit(context.Background(), "ubuntu"); err != nil {
			t.Fatalf("Exit() err = %v", err)
		}
		obs, _ := e.Stats.Load("ubuntu")
		if !obs.MasterDownSince.Equal(*now) || !obs.MasterSince.IsZero() {
			t.Errorf("Load() after Exit = %+v, want down since t0+1m", obs)
		}
	})

	// A FAILED exit must observe nothing: the master is still running, and
	// stamping a down-time for it would be the lie SPEC §4 forbids.
	t.Run("a refused exit observes nothing", func(t *testing.T) {
		t.Parallel()
		e, _ := observedAlive(t)
		e.Runner = fakerun.New().Stub(fakerun.Reply{Result: run.Result{Stderr: []byte("not running"), Code: 255}}, "ssh", "-O", "exit", "ubuntu")
		if err := e.Exit(context.Background(), "ubuntu"); err == nil {
			t.Fatal("Exit() err = nil, want the refusal")
		}
		obs, _ := e.Stats.Load("ubuntu")
		if !obs.MasterSince.Equal(t0) || !obs.MasterDownSince.IsZero() {
			t.Errorf("Load() after failed Exit = %+v, want alive-run at t0 untouched", obs)
		}
	})
}

// CONTRACT-stats §2: an ObserveHost write error must not fail Status — the
// view is still enriched for this render. Exit's stats failure follows
// ErrStateWrite.
func TestEngine_StatsWriteFailureConventions(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	newEngine := func(t *testing.T) tunnel.Engine {
		t.Helper()
		e := engine(t, newSpawner(), fakedial.New())
		e.Stats = &stats.Store{Dir: e.StateDir, Clock: func() time.Time { return t0 }}
		if err := os.Mkdir(filepath.Join(e.StateDir, "ubuntu.stats.json"), 0o700); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		return e
	}
	t.Run("status never fails over a stale stats cache", func(t *testing.T) {
		t.Parallel()
		got, err := newEngine(t).Status(context.Background(), "ubuntu", nil)
		if err != nil {
			t.Fatalf("Status() err = %v, want nil (best-effort write)", err)
		}
		if !got.MasterSince.Equal(t0) {
			t.Errorf("Status() = %+v, want the view still enriched at t0", got)
		}
	})
	t.Run("exit reports the stats failure as ErrStateWrite", func(t *testing.T) {
		t.Parallel()
		err := newEngine(t).Exit(context.Background(), "ubuntu")
		if !errors.Is(err, tunnel.ErrStateWrite) {
			t.Errorf("Exit() err = %v, want ErrStateWrite (the master IS down)", err)
		}
	})
}

func TestEngine_Status_CorruptStats(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	e := engine(t, newSpawner(), fakedial.New())
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

// An unparseable spec is never "serving": Up hands it to Spawn, which
// refuses it before anything starts.
func TestEngine_SpawnSet_UnparseableSpecIsRefused(t *testing.T) {
	t.Parallel()
	s := newSpawner()
	e := engine(t, s, fakedial.New())
	err := e.SpawnSet(context.Background(), "ubuntu", "web", []string{"[::1]:3000:localhost:3000"})
	if err == nil || !strings.Contains(err.Error(), "no local port to verify") {
		t.Errorf("SpawnSet() err = %v, want the refusal", err)
	}
	if got := s.Spawned(); len(got) != 0 {
		t.Errorf("spawned %q, want nothing", got)
	}
}
