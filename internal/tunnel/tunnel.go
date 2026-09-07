// Package tunnel is the M3 engine (SPEC §4, SASASH.7 Model B): one detached
// `ssh -N -L` process per forward, grouped into named sets, and the honest
// three-state status model. No UI — the TUI pane and the CLI both consume
// this package. Contract: .squad/CONTRACT-tunnel.md (ratified).
//
// A forward IS a process: Spawn starts it and records its pid, Kill sends
// it SIGTERM (its listener and its established connections die together,
// nothing else is touched), a set is N independent processes. Nothing on
// the tunnel path uses -O; the interactive control master survives only as
// CheckMaster/Exit, an indicator that never decides a forward's verdict.
//
// The argv strings ARE the product, so every command is table-tested to its
// exact string. State under StateDir is a cache, never truth — on every
// Status call a pid liveness check plus a local dial per forward decide
// up/listening/down, and the state file can attribute or remember a forward
// but never upgrade one.
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/stats"
)

// ErrStateWrite marks an action whose process side SUCCEEDED but whose
// state cache update failed: the tunnel change is real, the record of it
// is not. Callers may surface the message and carry on; errors.Is picks it
// out.
var ErrStateWrite = errors.New("state not recorded")

// pollInterval paces the Spawn handshake and the post-Kill wait. Every
// fake path in the unit tier resolves on the first iteration — except the
// grace wait after an aborted Spawn, which is wall-clock by nature and
// bounded by Engine.Timeout (one 50ms sleep in the suite).
const pollInterval = 50 * time.Millisecond

// Status is the honest three-state model (SPEC §4). Never collapsed.
type Status int

const (
	// Down is anything else: no listener, an unverifiable spec, or a
	// process that runs but serves nothing.
	Down Status = iota
	// Listening means the local port answers but no live sasash process
	// owns it — someone else runs it, or the record was lost.
	Listening
	// Up means the recorded process is alive AND the local port answers.
	Up
)

// String renders a Status for the UI and for test failure messages.
func (s Status) String() string {
	switch s {
	case Up:
		return "up"
	case Listening:
		return "listening"
	case Down:
		return "down"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// Forward is one forward's reconciled view.
type Forward struct {
	Set   string // owning set name; "" = ad-hoc
	Spec  string // verbatim, e.g. "3000:localhost:3000"
	Port  string // parsed local port; "" when unparseable (then State is Down)
	Pid   int    // the live sasash process serving this forward; 0 when none is alive
	State Status
	// Since and DownSince are observed times from the stats store (contract
	// CONTRACT-stats.md): start of the current accepting run, and the first
	// observation of the port dead after being up. Zero = unknown, or no
	// Stats store configured.
	Since     time.Time
	DownSince time.Time
}

// HostStatus is one render's truth for a host.
type HostStatus struct {
	Host string
	// Master reports the INTERACTIVE control master (`ssh -O check`). It is
	// an indicator for enter/spawn/scp reuse and never part of a forward's
	// verdict (hybrid model).
	Master bool
	// Forwards holds desired sets first (meta order), then state-file
	// extras, deduplicated by spec with desired-set attribution winning.
	Forwards []Forward
	// CorruptState reports a state file that existed but could not be
	// read or parsed; it was treated as empty (reality wins regardless).
	CorruptState bool

	// Observed times from the stats store; all zero when Engine.Stats is
	// nil or nothing was ever observed. The zero time means unknown —
	// never invented (CONTRACT-stats.md).
	MasterSince     time.Time
	MasterDownSince time.Time
	MasterBySasash  bool
	LastHandoff     time.Time
	LastSwitch      time.Time
	LastSwitchPath  string
	// CorruptStats reports the stats file was unreadable or unparseable
	// this render; observations restarted from now rather than backdated.
	CorruptStats bool
}

// Engine runs the interactive-master commands through the run seam, every
// tunnel process through the spawn seam, and dials through the probe seam.
// The zero values of Runner, Spawner, Dialer and Timeout mean run.OS{},
// run.OSSpawner{}, probe.Net{} and probe.DefaultTimeout. StateDir is
// required: the cmd layer passes ~/.local/state/sasash, tests a t.TempDir().
//
// Concurrency: state updates are unlocked read-modify-write over a plain
// file — callers must serialize mutating calls per host (the TUI's render
// loop and the CLI both do naturally). Concurrent mutators can lose records
// and a reader racing a write can see a transient CorruptState; both degrade
// honestly (reality still decides every status), but don't do it.
type Engine struct {
	Runner   run.Runner
	Spawner  run.Spawner
	Dialer   probe.Dialer
	StateDir string
	Timeout  time.Duration
	// Stats, when non-nil, records observed timestamps (master/forward
	// up-down transitions on Status and Exit) and enriches HostStatus/
	// Forward with them. nil means no observing and every observed field
	// stays zero.
	Stats *stats.Store
}

func (e Engine) runner() run.Runner {
	if e.Runner != nil {
		return e.Runner
	}
	return run.OS{}
}

func (e Engine) spawner() run.Spawner {
	if e.Spawner != nil {
		return e.Spawner
	}
	return run.OSSpawner{}
}

func (e Engine) dialer() probe.Dialer {
	if e.Dialer != nil {
		return e.Dialer
	}
	return probe.Net{}
}

func (e Engine) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return probe.DefaultTimeout
}

// exec runs argv, mapping a non-zero exit to an error carrying ssh's own
// (trimmed) stderr — these are user actions and ssh's message is the story.
func (e Engine) exec(ctx context.Context, argv ...string) error {
	res, err := e.runner().Run(ctx, argv...)
	if err != nil {
		return err
	}
	if !res.OK() {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = fmt.Sprintf("exit %d", res.Code)
		}
		return fmt.Errorf("%s: %s", strings.Join(argv, " "), msg)
	}
	return nil
}

// SpawnArgv is the one tunnel command (CONTRACT-tunnel §3), exported so a
// preview shows exactly what Spawn runs — one owner of the literal.
// BatchMode: no tty exists to prompt on, so auth that needs one fails at
// once with ssh's reason instead of hanging. ExitOnForwardFailure: a -L
// that cannot bind ends the process with "Address already in use" on its
// stderr instead of leaving a live process that serves nothing. No -f:
// sasash detaches the process itself, so the pid it records is the
// tunnel's own.
func SpawnArgv(host, spec string) []string {
	return []string{"ssh", "-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-L", spec, host}
}

// Spawn starts one detached tunnel process for spec and waits for reality
// to say it works: the local port answers (success, pid recorded under set;
// "" = ad-hoc), the process exits (failure carrying its stderr), or ctx
// ends (the process is killed — a tunnel that never came up is not left
// dangling). Two refusals come before anything starts: a spec whose local
// port cannot be parsed (what cannot be verified must not be claimed), and
// a local port that already answers (a foreign listener would make the
// handshake's dial lie — SPEC §4: say so, never silently remap). A success
// whose record write fails returns an ErrStateWrite-wrapped error — the
// tunnel IS running.
//
// Residual window: a foreign listener that appears between the pre-check
// and ssh's bind ends our process (ExitOnForwardFailure) and can beat the
// handshake by one poll; the next Status reconciles it (pid dead + port
// answering = Listening).
func (e Engine) Spawn(ctx context.Context, host, set, spec string) error {
	port := localPort(spec)
	if port == "" {
		return fmt.Errorf("spec %q: no local port to verify; refusing to start what cannot be checked", spec)
	}
	logPath, err := e.hostFile(host, "."+port+".log")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.dialOK(ctx, port) {
		return fmt.Errorf("spec %q: local port %s already answers; refusing to start a tunnel that could not bind it", spec, port)
	}
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		return err
	}
	argv := SpawnArgv(host, spec)
	sp := e.spawner()
	pid, err := sp.Spawn(argv, logPath)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	for {
		accepting := e.dialOK(ctx, port)
		alive := sp.Alive(pid)
		if accepting && alive {
			return e.record(host, set, spec, pid)
		}
		if !alive {
			return fmt.Errorf("%s: %s", strings.Join(argv, " "), e.lastWords(logPath))
		}
		select {
		case <-ctx.Done():
			_ = sp.Kill(pid)
			if e.grace(pid) {
				return fmt.Errorf("%s: not listening on %s: %w", strings.Join(argv, " "), port, ctx.Err())
			}
			// It survived the signal (auth may have finished a beat after it):
			// record it, so KillAll can reach what would otherwise be an orphan.
			// The record failure rides along as text, never as ErrStateWrite:
			// a consumer reading "only the cache failed" would take a tunnel
			// that is not listening for a working one.
			what := "recorded"
			if rerr := e.record(host, set, spec, pid); rerr != nil {
				what = "NOT recorded: " + rerr.Error()
			}
			return fmt.Errorf("%s: not listening on %s, and pid %d survived SIGTERM (%s): %w", strings.Join(argv, " "), port, pid, what, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// grace waits one dial-timeout's worth for pid to die after a signal,
// reaping it; false means it is still alive. Wall-clock, not ctx: the
// caller's ctx is already over.
func (e Engine) grace(pid int) bool {
	sp := e.spawner()
	deadline := time.Now().Add(e.timeout())
	for sp.Alive(pid) {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
	return true
}

// lastWords is a dead process's stderr, trimmed, or a stand-in when it
// said nothing.
func (Engine) lastWords(logPath string) string {
	data, _ := os.ReadFile(logPath)
	if msg := strings.TrimSpace(string(data)); msg != "" {
		return msg
	}
	return "exited before listening"
}

// Kill ends the process serving spec: SIGTERM to every recorded pid of the
// spec (one port has one process, whatever set recorded it), then a wait
// until it is gone or ctx ends. A named set's record of the spec is
// removed; an ad-hoc record stays with its pid cleared — for the ad-hoc
// set the record IS the declaration, so the row survives a down and
// SpawnSet brings it back; only Forget or KillAll purges it. No recorded
// pid, or a pid already dead, means nothing to signal: the state is updated
// and nil returned. A process that could not be signalled or outlives the
// wait is an error and ITS record is kept, pid and all — the next Kill or
// KillAll must still be able to reach it; a pid that REFUSES the signal is
// not ours (reused since a reboot) and its record goes with the error. A
// state failure wraps ErrStateWrite.
func (e Engine) Kill(ctx context.Context, host, spec string) error {
	st, _ := e.loadState(host)
	survivors, err := e.killPids(ctx, st.pids(spec))
	if serr := e.mutateState(host, "kill attempted", func(st *hostState) {
		st.retire(spec, survivors)
	}); serr != nil {
		err = errors.Join(err, serr)
	}
	return err
}

// KillAll ends every recorded process of the host and clears every record,
// ad-hoc included — the explicit "everything off". A process that survives
// keeps its record (see Kill). The interactive master is not touched; that
// is Exit's, and a consumer wanting both calls both.
func (e Engine) KillAll(ctx context.Context, host string) error {
	st, _ := e.loadState(host)
	var pids []int
	for _, f := range st.Forwards {
		if f.Pid > 0 {
			pids = append(pids, f.Pid)
		}
	}
	survivors, err := e.killPids(ctx, pids)
	if serr := e.mutateState(host, "kill attempted", func(st *hostState) {
		st.remove(func(f stateForward) bool { return !contains(survivors, f.Pid) })
	}); serr != nil {
		err = errors.Join(err, serr)
	}
	return err
}

// killPids signals EVERY live pid first, then waits for each to die; a
// ctx that ends mid-wait never leaves a later pid unsignalled. It returns
// the pids that outlived the wait, with the joined errors; their records
// must be kept (ours, not dead yet). A REFUSED signal is reported but not
// returned as a survivor: a pid we may not signal is not ours to manage
// (a number reused since a reboot), and its record is garbage.
func (e Engine) killPids(ctx context.Context, pids []int) (survivors []int, err error) {
	sp := e.spawner()
	var errs []error
	var signalled []int
	for _, pid := range pids {
		if !sp.Alive(pid) {
			continue
		}
		if kerr := sp.Kill(pid); kerr != nil {
			errs = append(errs, fmt.Errorf("pid %d: %w", pid, kerr))
			continue
		}
		signalled = append(signalled, pid)
	}
	for _, pid := range signalled {
		if !e.waitDead(ctx, pid) {
			errs = append(errs, fmt.Errorf("pid %d still running after SIGTERM: %w", pid, ctx.Err()))
			survivors = append(survivors, pid)
		}
	}
	return survivors, errors.Join(errs...)
}

// waitDead polls until pid is gone (true) or ctx ends (false).
func (e Engine) waitDead(ctx context.Context, pid int) bool {
	sp := e.spawner()
	for sp.Alive(pid) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollInterval):
		}
	}
	return true
}

func contains(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}

// Forget drops every record of spec from the cache without signalling
// anything: the explicit purge of a remembered ad-hoc forward, and a
// lifecycle delete's last step. A live forward is the caller's to Kill
// first. Errors wrap ErrStateWrite.
func (e Engine) Forget(host, spec string) error {
	return e.mutateState(host, "forget requested", func(st *hostState) {
		st.remove(func(f stateForward) bool { return f.Spec == spec })
	})
}

// CheckMaster asks `ssh -O check <host>` about the INTERACTIVE control
// master. A non-zero exit is data — no master — not an error; error means
// the run seam itself failed. Never part of a forward's verdict.
func (e Engine) CheckMaster(ctx context.Context, host string) (bool, error) {
	res, err := e.runner().Run(ctx, "ssh", "-O", "check", host)
	if err != nil {
		return false, err
	}
	return res.OK(), nil
}

// Exit tears the INTERACTIVE control master down: `ssh -O exit <host>`.
// Tunnel processes are their own connections and are not affected; a
// consumer wanting everything off calls KillAll too. On success the master
// is observed dead in the stats store; a record failure follows the
// ErrStateWrite convention.
func (e Engine) Exit(ctx context.Context, host string) error {
	if err := e.exec(ctx, "ssh", "-O", "exit", host); err != nil {
		return err
	}
	if e.Stats != nil {
		if _, _, oerr := e.Stats.ObserveHost(host, false, nil); oerr != nil {
			return fmt.Errorf("master exited but %w: %v", ErrStateWrite, oerr)
		}
	}
	return nil
}

// SpawnSet brings a set up one process per spec, disturbing nothing that
// already works: a spec whose recorded process is alive AND whose port
// answers is skipped (no kill, no respawn — this IS "add a port without
// touching the others"); every other spec is Spawned in order. A genuine
// spawn failure aborts and is returned (the earlier processes stay — they
// are real); an ErrStateWrite does not abort — the tunnel is real and only
// its record failed, so SpawnSet keeps going and returns the joined
// state-write errors at the end.
func (e Engine) SpawnSet(ctx context.Context, host, set string, specs []string) error {
	st, _ := e.loadState(host)
	var stateErrs []error
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		// A spec listed twice is one process: the second pass would find
		// its own port answering and refuse it as foreign.
		if seen[spec] || e.serving(ctx, st, spec) {
			continue
		}
		seen[spec] = true
		if err := e.Spawn(ctx, host, set, spec); err != nil {
			if errors.Is(err, ErrStateWrite) {
				stateErrs = append(stateErrs, err)
				continue
			}
			return err
		}
	}
	return errors.Join(stateErrs...)
}

// serving reports whether a recorded process for spec is alive and its
// port answers — the one case SpawnSet leaves alone.
func (e Engine) serving(ctx context.Context, st hostState, spec string) bool {
	port := localPort(spec)
	if port == "" {
		return false
	}
	sp := e.spawner()
	for _, pid := range st.pids(spec) {
		if sp.Alive(pid) && e.dialOK(ctx, port) {
			return true
		}
	}
	return false
}

// KillSet kills every spec of the set — the given (desired) specs plus any
// recorded under the set — joining errors, then updates the records under
// Kill's rule: a named set's records go, ad-hoc records stay with their
// pid cleared (they are the ad-hoc set's declaration, so the rows survive
// a down and SpawnSet brings them back; only Forget or KillAll purges them).
func (e Engine) KillSet(ctx context.Context, host, set string, specs []string) error {
	st, _ := e.loadState(host)
	seen := make(map[string]bool)
	all := make([]string, 0, len(specs))
	for _, s := range specs {
		if !seen[s] {
			seen[s] = true
			all = append(all, s)
		}
	}
	for _, f := range st.Forwards {
		if f.Set == set && !seen[f.Spec] {
			seen[f.Spec] = true
			all = append(all, f.Spec)
		}
	}

	var errs []error
	var survivors []int
	for _, spec := range all {
		alive, err := e.killPids(ctx, st.pids(spec))
		survivors = append(survivors, alive...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if serr := e.mutateState(host, "set killed", func(st *hostState) {
		for _, spec := range all {
			st.retire(spec, survivors)
		}
	}); serr != nil {
		errs = append(errs, serr)
	}
	return errors.Join(errs...)
}

// Status reconciles one host: the interactive master check (indicator
// only), then per forward a liveness check of its recorded pid and a local
// dial. Reality always wins over the state file, in both directions — a
// recorded process that died is Down, and a desired forward someone else
// runs shows Listening. The error return is reserved for run-seam failure;
// everything observable degrades to a state.
func (e Engine) Status(ctx context.Context, host string, desired []hostmodel.ForwardSet) (HostStatus, error) {
	master, err := e.CheckMaster(ctx, host)
	if err != nil {
		return HostStatus{Host: host}, err
	}
	st, corrupt := e.loadState(host)
	out := HostStatus{Host: host, Master: master, CorruptState: corrupt}

	seen := make(map[string]bool)
	add := func(set, spec string) {
		if seen[spec] {
			return
		}
		seen[spec] = true
		f := Forward{Set: set, Spec: spec, Port: localPort(spec)}
		f.Pid, f.State = e.verdict(ctx, st.pids(spec), f.Port)
		out.Forwards = append(out.Forwards, f)
	}
	for _, ds := range desired {
		for _, spec := range ds.Specs {
			add(ds.Name, spec)
		}
	}
	for _, sf := range st.Forwards {
		add(sf.Set, sf.Spec)
	}

	if e.Stats != nil {
		accepting := make(map[string]bool, len(out.Forwards))
		for _, f := range out.Forwards {
			// Up and Listening both mean the local port answered.
			accepting[f.Spec] = f.State != Down
		}
		// Best-effort persistence: the returned view is correct for this
		// render even when the write failed, and a render must not break
		// over a stale cache (CONTRACT-stats §2).
		obs, statsCorrupt, _ := e.Stats.ObserveHost(host, master, accepting)
		out.MasterSince = obs.MasterSince
		out.MasterDownSince = obs.MasterDownSince
		out.MasterBySasash = obs.MasterBySasash
		out.LastHandoff = obs.LastHandoff
		out.LastSwitch = obs.LastSwitch
		out.LastSwitchPath = obs.LastSwitchPath
		out.CorruptStats = statsCorrupt
		for i := range out.Forwards {
			fo := obs.Forwards[out.Forwards[i].Spec]
			out.Forwards[i].Since = fo.Since
			out.Forwards[i].DownSince = fo.DownSince
		}
	}
	return out, nil
}

// verdict is the whole reconciliation matrix (CONTRACT-tunnel §6, with
// amendment M1 #2: the pid returned is the LIVE one and only on Up): a
// recorded pid alive AND the port answering => Up (that pid returned);
// the port answering alone => Listening; anything else => Down, pid 0.
//
// Known limits: the dial is always to 127.0.0.1, so a spec bound to a
// specific non-loopback interface is judged by whoever holds the loopback
// port; and two different specs sharing a local port are each judged by
// that one port.
func (e Engine) verdict(ctx context.Context, pids []int, port string) (int, Status) {
	if port == "" {
		return 0, Down
	}
	alive := 0
	sp := e.spawner()
	for _, pid := range pids {
		if sp.Alive(pid) {
			alive = pid
			break
		}
	}
	if !e.dialOK(ctx, port) {
		return 0, Down
	}
	if alive > 0 {
		return alive, Up
	}
	return 0, Listening
}

// dialOK reports whether 127.0.0.1:port accepts within the engine timeout.
func (e Engine) dialOK(ctx context.Context, port string) bool {
	dctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	_, err := e.dialer().Dial(dctx, net.JoinHostPort("127.0.0.1", port))
	return err == nil
}

// localPort extracts the local port to dial: the first all-digit ':'-field
// among the spec's first two fields ("3000:localhost:3000" -> 3000,
// "127.0.0.1:3000:localhost:3000" -> 3000). No match means the forward
// cannot be verified, so it must not be claimed: Port "" reads as Down and
// Spawn refuses it. Bracketed or bare IPv6 bind addresses therefore always
// read as Down (fail-safe under-claim), and spec validation itself is
// M3's, not ours — a malformed spec ssh would reject can still yield a
// dialable port here.
func localPort(spec string) string {
	fields := strings.SplitN(spec, ":", 3)
	for i, f := range fields {
		if i > 1 {
			break
		}
		if allDigits(f) {
			return f
		}
	}
	return ""
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// ---- state cache: StateDir/<host>.json — a cache, never truth ----

type stateForward struct {
	Set  string `json:"set"`
	Spec string `json:"spec"`
	Pid  int    `json:"pid,omitempty"` // 0 = remembered, not running
}

type hostState struct {
	Forwards []stateForward `json:"forwards"`
}

func (st *hostState) remove(drop func(stateForward) bool) {
	kept := st.Forwards[:0]
	for _, f := range st.Forwards {
		if !drop(f) {
			kept = append(kept, f)
		}
	}
	st.Forwards = kept
}

// pids lists every recorded (non-zero) pid of spec, whatever set recorded
// it: one port has one process, so any of them is the one to check or end.
func (st hostState) pids(spec string) []int {
	var out []int
	for _, f := range st.Forwards {
		if f.Spec == spec && f.Pid > 0 {
			out = append(out, f.Pid)
		}
	}
	return out
}

// retire applies the after-kill rule to spec: a named set's record goes
// (its desire lives in config), an ad-hoc record stays with pid cleared
// (the record is its declaration). A record whose pid survived is left
// exactly as it is — the process is real and must stay reachable.
func (st *hostState) retire(spec string, survivors []int) {
	st.remove(func(f stateForward) bool {
		return f.Spec == spec && f.Set != "" && !contains(survivors, f.Pid)
	})
	for i := range st.Forwards {
		if st.Forwards[i].Spec == spec && !contains(survivors, st.Forwards[i].Pid) {
			st.Forwards[i].Pid = 0
		}
	}
}

// hostFile refuses a host alias that is not a plain file name — an alias
// like "../x" must not walk out of StateDir — and returns StateDir/host+suffix.
func (e Engine) hostFile(host, suffix string) (string, error) {
	if host == "" || host == "." || host == ".." || host != filepath.Base(host) {
		return "", fmt.Errorf("host %q is not a valid state file name", host)
	}
	return filepath.Join(e.StateDir, host+suffix), nil
}

// loadState reads the cache. Missing means empty; a file that exists but
// cannot be read or parsed also means empty, with corrupt reported so the
// caller can say so (reality decides the statuses either way).
func (e Engine) loadState(host string) (st hostState, corrupt bool) {
	path, err := e.hostFile(host, ".json")
	if err != nil {
		return hostState{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return hostState{}, !errors.Is(err, fs.ErrNotExist)
	}
	if json.Unmarshal(data, &st) != nil {
		return hostState{}, true
	}
	return st, false
}

// record adds one spawned forward to the cache (one entry per set+spec;
// the pid is overwritten by a fresh spawn).
func (e Engine) record(host, set, spec string, pid int) error {
	return e.mutateState(host, "tunnel started", func(st *hostState) {
		for i, f := range st.Forwards {
			if f.Set == set && f.Spec == spec {
				st.Forwards[i].Pid = pid
				return
			}
		}
		st.Forwards = append(st.Forwards, stateForward{Set: set, Spec: spec, Pid: pid})
	})
}

// mutateState is load-modify-save; any failure wraps ErrStateWrite, prefixed
// with what the process side already accomplished — by the time state
// changes, the action is real and the caller deserves to know that.
func (e Engine) mutateState(host, action string, mutate func(*hostState)) error {
	fail := func(err error) error {
		return fmt.Errorf("%s but %w: %v", action, ErrStateWrite, err)
	}
	path, err := e.hostFile(host, ".json")
	if err != nil {
		return fail(err)
	}
	st, _ := e.loadState(host)
	mutate(&st)
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		return fail(err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fail(err)
	}
	return nil
}
