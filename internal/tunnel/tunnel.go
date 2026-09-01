// Package tunnel is the M3 engine (SPEC §4, SASASH.3.2): OpenSSH
// control-master lifecycle, live forward add/cancel, and the honest
// three-state status model. No UI — the TUI pane and the CLI both consume
// this package. Contract: .squad/CONTRACT-tunnel.md (ratified).
//
// The argv strings ARE the product: a typo in "-O cancel" silently does
// nothing, so every command is table-tested to its exact string. State under
// StateDir is a cache, never truth — on every Status call, `ssh -O check`
// plus a local dial per forward decide up/listening/down, and the state file
// can attribute or remember a forward but never upgrade one.
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
)

// ErrStateWrite marks an action whose ssh side SUCCEEDED but whose state
// cache update failed: the tunnel change is real, the record of it is not.
// Callers may surface the message and carry on; errors.Is picks it out.
var ErrStateWrite = errors.New("state not recorded")

// Status is the honest three-state model (SPEC §4). Never collapsed.
type Status int

const (
	// Down is anything else: no listener, or an unverifiable spec.
	Down Status = iota
	// Listening means the local port accepts but the master is not
	// verified — the forward is not known to work end-to-end.
	Listening
	// Up means the master is alive AND the local port accepts.
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
	State Status
}

// HostStatus is one render's truth for a host.
type HostStatus struct {
	Host   string
	Master bool // ssh -O check succeeded
	// Forwards holds desired sets first (meta order), then state-file
	// extras, deduplicated by spec with desired-set attribution winning.
	Forwards []Forward
	// CorruptState reports a state file that existed but could not be
	// read or parsed; it was treated as empty (reality wins regardless).
	CorruptState bool
}

// Engine shells out through the run seam and dials through the probe seam.
// The zero values of Runner, Dialer and Timeout mean run.OS{}, probe.Net{}
// and probe.DefaultTimeout. StateDir is required: the cmd layer passes
// ~/.local/state/sasash, tests pass a t.TempDir().
//
// Concurrency: state updates are unlocked read-modify-write over a plain
// file — callers must serialize mutating calls per host (the TUI's render
// loop and the CLI both do naturally). Concurrent mutators can lose records
// and a reader racing a write can see a transient CorruptState; both degrade
// honestly (reality still decides every status), but don't do it.
type Engine struct {
	Runner   run.Runner
	Dialer   probe.Dialer
	StateDir string
	Timeout  time.Duration
}

func (e Engine) runner() run.Runner {
	if e.Runner != nil {
		return e.Runner
	}
	return run.OS{}
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

// Start launches the background master: `ssh -fN <host>`. Managed hosts
// carry ControlMaster auto + ControlPath from sasash.conf, so no -M here.
func (e Engine) Start(ctx context.Context, host string) error {
	return e.exec(ctx, "ssh", "-fN", host)
}

// CheckMaster asks `ssh -O check <host>`. A non-zero exit is data — the
// master is down — not an error; error means the run seam itself failed.
func (e Engine) CheckMaster(ctx context.Context, host string) (bool, error) {
	res, err := e.runner().Run(ctx, "ssh", "-O", "check", host)
	if err != nil {
		return false, err
	}
	return res.OK(), nil
}

// Forward injects one forward into the live master:
// `ssh -O forward -L <spec> <host>`, recording it under set on success
// ("" = ad-hoc). A failed state write returns ErrStateWrite-wrapped — the
// ssh side already succeeded.
func (e Engine) Forward(ctx context.Context, host, set, spec string) error {
	if err := e.exec(ctx, "ssh", "-O", "forward", "-L", spec, host); err != nil {
		return err
	}
	return e.record(host, set, spec)
}

// Cancel removes one forward: `ssh -O cancel -L <spec> <host>`. The state
// entry is removed even when ssh refuses (the attempt expresses intent, and
// the cache is never truth).
func (e Engine) Cancel(ctx context.Context, host, spec string) error {
	err := e.exec(ctx, "ssh", "-O", "cancel", "-L", spec, host)
	if serr := e.mutateState(host, "cancel attempted", func(st *hostState) {
		st.remove(func(f stateForward) bool { return f.Spec == spec })
	}); serr != nil && err == nil {
		err = serr
	}
	return err
}

// Exit tears the master down: `ssh -O exit <host>`. Every recorded forward
// for the host is cleared regardless of the exit result — a dead master has
// no forwards either way.
func (e Engine) Exit(ctx context.Context, host string) error {
	err := e.exec(ctx, "ssh", "-O", "exit", host)
	if serr := e.mutateState(host, "master exit requested", func(st *hostState) {
		st.Forwards = nil
	}); serr != nil && err == nil {
		err = serr
	}
	return err
}

// Up ensures the master (Start only when CheckMaster says down), then
// Forwards every spec in order, recording each success. A genuine ssh
// failure aborts and is returned; an ErrStateWrite does not — the tunnel
// is real and only its record failed, so Up keeps going and returns the
// joined state-write errors at the end (contract amendment B1: the cache
// is never truth, so a cache failure must never stop real tunnels).
func (e Engine) Up(ctx context.Context, host, set string, specs []string) error {
	alive, err := e.CheckMaster(ctx, host)
	if err != nil {
		return err
	}
	if !alive {
		if err := e.Start(ctx, host); err != nil {
			return err
		}
	}
	var stateErrs []error
	for _, spec := range specs {
		if err := e.Forward(ctx, host, set, spec); err != nil {
			if errors.Is(err, ErrStateWrite) {
				stateErrs = append(stateErrs, err)
				continue
			}
			return err
		}
	}
	return errors.Join(stateErrs...)
}

// DownSet cancels every spec of the set — the given (desired) specs plus any
// recorded under the set — joining errors. The set's state entries are
// cleared regardless: the cache is never truth, and the next Status
// re-derives reality.
func (e Engine) DownSet(ctx context.Context, host, set string, specs []string) error {
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
	for _, spec := range all {
		if err := e.exec(ctx, "ssh", "-O", "cancel", "-L", spec, host); err != nil {
			errs = append(errs, err)
		}
	}
	if serr := e.mutateState(host, "set cancelled", func(st *hostState) {
		st.remove(func(f stateForward) bool { return f.Set == set })
	}); serr != nil {
		errs = append(errs, serr)
	}
	return errors.Join(errs...)
}

// Status reconciles one host: `ssh -O check`, then a local dial per forward.
// Reality always wins over the state file, in both directions — a recorded
// forward whose port is dead is Down even with the master alive, and a
// desired forward someone else runs still shows Up. The error return is
// reserved for run-seam failure; everything observable degrades to a state.
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
		f.State = e.verdict(ctx, master, f.Port)
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
	return out, nil
}

// verdict is the whole reconciliation matrix: port accepting AND master
// alive => Up; port accepting alone => Listening; anything else => Down.
//
// Known limits (ratified contract §5; revisit with 3.4's port-conflict
// work): the dial is always to 127.0.0.1, so a spec bound to a specific
// non-loopback interface is judged by whoever holds the loopback port; and
// two different specs sharing a local port are each judged by that one
// port, so a stale recorded spec can ride a live desired one's listener.
func (e Engine) verdict(ctx context.Context, master bool, port string) Status {
	if port == "" {
		return Down
	}
	dctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	if _, err := e.dialer().Dial(dctx, net.JoinHostPort("127.0.0.1", port)); err != nil {
		return Down
	}
	if master {
		return Up
	}
	return Listening
}

// localPort extracts the local port to dial: the first all-digit ':'-field
// among the spec's first two fields ("3000:localhost:3000" -> 3000,
// "127.0.0.1:3000:localhost:3000" -> 3000). No match means the forward
// cannot be verified, so it must not be claimed: Port "" reads as Down.
// Bracketed or bare IPv6 bind addresses therefore always read as Down
// (fail-safe under-claim), and spec validation itself is M3's, not ours —
// a malformed spec ssh would reject can still yield a dialable port here.
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

// statePath refuses a host alias that is not a plain file name — an alias
// like "../x" must not walk out of StateDir.
func (e Engine) statePath(host string) (string, error) {
	if host == "" || host == "." || host == ".." || host != filepath.Base(host) {
		return "", fmt.Errorf("host %q is not a valid state file name", host)
	}
	return filepath.Join(e.StateDir, host+".json"), nil
}

// loadState reads the cache. Missing means empty; a file that exists but
// cannot be read or parsed also means empty, with corrupt reported so the
// caller can say so (reality decides the statuses either way).
func (e Engine) loadState(host string) (st hostState, corrupt bool) {
	path, err := e.statePath(host)
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

// record adds one injected forward to the cache (idempotent per set+spec).
func (e Engine) record(host, set, spec string) error {
	return e.mutateState(host, "forward added", func(st *hostState) {
		for _, f := range st.Forwards {
			if f.Set == set && f.Spec == spec {
				return
			}
		}
		st.Forwards = append(st.Forwards, stateForward{Set: set, Spec: spec})
	})
}

// mutateState is load-modify-save; any failure wraps ErrStateWrite, prefixed
// with what the ssh side already accomplished — by the time state changes,
// the action is real and the caller deserves to know that.
func (e Engine) mutateState(host, action string, mutate func(*hostState)) error {
	fail := func(err error) error {
		return fmt.Errorf("%s but %w: %v", action, ErrStateWrite, err)
	}
	path, err := e.statePath(host)
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
