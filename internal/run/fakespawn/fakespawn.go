// Package fakespawn provides a run.Spawner for tests: pids are handed out
// in sequence from 1000, a spawn keyed by its joined argv can be scripted
// to die at once with a stderr text, Alive is a set the test controls,
// and every Spawn, Alive and Kill is recorded.
//
// Wired to a fakedial (Dial), the local port of a live spawn starts
// answering and stops when the pid is killed — the port follows the
// process, as it does for real.
package fakespawn

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run"
)

// Fake is a run.Spawner backed by scripted behaviour.
type Fake struct {
	SpawnErr     error        // every Spawn fails with it
	KillErr      error        // every Kill fails with it (the process lives on)
	Stubborn     bool         // Kill is recorded but the process lives on
	StubbornPids map[int]bool // the same, per pid
	NeverListens bool         // a live process never answers on its port
	OnSpawn      func()       // runs inside Spawn (a test cancels its ctx here)

	mu       sync.Mutex
	nextPid  int
	exits    map[string]string // argv key -> stderr; the process exits at start
	alive    map[int]bool
	dial     *fakedial.Fake
	ports    map[int]string // pid -> the local port it answers on
	spawned  []string
	logPaths []string
	killed   []int
	probed   []int
}

var _ run.Spawner = (*Fake)(nil)

// New returns a Fake with nothing alive and nothing scripted.
func New() *Fake {
	return &Fake{nextPid: 999, exits: map[string]string{}, alive: map[int]bool{}, ports: map[int]string{}}
}

// Dial wires d so a live spawn's local port answers and a kill closes it.
func (f *Fake) Dial(d *fakedial.Fake) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dial = d
	return f
}

// Live marks pid alive, for records a test pre-writes.
func (f *Fake) Live(pid int) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[pid] = true
	return f
}

// LiveOn marks pid alive and serving port: a kill then closes the port
// on the wired dialer, as ending a real process frees its listener.
func (f *Fake) LiveOn(pid int, port string) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[pid] = true
	f.ports[pid] = port
	return f
}

// Exits scripts argv to die at start with stderr text.
func (f *Fake) Exits(stderr string, argv ...string) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exits[strings.Join(argv, " ")] = stderr
	return f
}

// Spawn implements run.Spawner.
func (f *Fake) Spawn(argv []string, stderrPath string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.Join(argv, " ")
	f.spawned = append(f.spawned, key)
	f.logPaths = append(f.logPaths, stderrPath)
	if f.OnSpawn != nil {
		f.OnSpawn()
	}
	if f.SpawnErr != nil {
		return 0, f.SpawnErr
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
	if f.dial != nil && !f.NeverListens {
		// The spec follows -L; specs here start with their local port.
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

// Alive implements run.Spawner.
func (f *Fake) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probed = append(f.probed, pid)
	return f.alive[pid]
}

// Kill implements run.Spawner.
func (f *Fake) Kill(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, pid)
	if f.KillErr != nil {
		return f.KillErr
	}
	if !f.Stubborn && !f.StubbornPids[pid] {
		f.alive[pid] = false
		if port := f.ports[pid]; port != "" && f.dial != nil {
			f.dial.Down("127.0.0.1:" + port)
		}
	}
	return nil
}

// Spawned lists every spawned argv (joined with spaces), in order.
func (f *Fake) Spawned() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.spawned...)
}

// LogPaths lists the stderr path handed to each Spawn, in order.
func (f *Fake) LogPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logPaths...)
}

// Killed lists every pid Kill was called with, in order.
func (f *Fake) Killed() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.killed...)
}

// Probed lists every pid Alive was asked about, in order.
func (f *Fake) Probed() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.probed...)
}
