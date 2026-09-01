// Package probe answers one question: which addresses for this host are alive
// right now?
//
// sasash never infers location from SSID, default gateway, or the presence of
// a utun interface. Those all lie -- several tunnels can be up at once and none
// of them identify themselves. A TCP connect to port 22 is cheap (tens of
// milliseconds on a LAN) and is the only signal that is actually true. See
// SPEC.md §3.
package probe

import (
	"context"
	"net"
	"sync"
	"time"
)

// DefaultPort is used when a path does not name one.
const DefaultPort = "22"

// DefaultTimeout bounds a single dial. Long enough for a slow WAN, short
// enough that probing every path of a host stays interactive.
const DefaultTimeout = time.Second

// Dialer reports how long it took to open a TCP connection to addr, or why it
// could not. Implementations must respect ctx.
type Dialer interface {
	Dial(ctx context.Context, addr string) (time.Duration, error)
}

// Net is the real Dialer.
type Net struct{}

var _ Dialer = Net{}

// Dial implements Dialer. The connection is closed immediately; we only care
// that the handshake completed.
func (Net) Dial(ctx context.Context, addr string) (time.Duration, error) {
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	_ = conn.Close()
	return elapsed, nil
}

// A Path is one named route to a host, such as "lan" or "ts".
type Path struct {
	Name string // label shown in the UI: lan, ts, public
	Host string // address or hostname
	Port string // optional; DefaultPort when empty
}

// Addr returns the dial target, correctly bracketing IPv6 literals.
func (p Path) Addr() string {
	port := p.Port
	if port == "" {
		port = DefaultPort
	}
	return net.JoinHostPort(p.Host, port)
}

// Status is the result of probing one path.
type Status struct {
	Path    Path
	Latency time.Duration
	Err     error
}

// Alive reports whether the path answered.
func (s Status) Alive() bool { return s.Err == nil }

// Prober probes paths concurrently.
type Prober struct {
	Dialer  Dialer        // defaults to Net
	Timeout time.Duration // defaults to DefaultTimeout
}

func (p Prober) dialer() Dialer {
	if p.Dialer == nil {
		return Net{}
	}
	return p.Dialer
}

func (p Prober) timeout() time.Duration {
	if p.Timeout <= 0 {
		return DefaultTimeout
	}
	return p.Timeout
}

// ProbeAll dials every path in parallel and returns one Status per path, in
// the same order as the input. A dead path is reported, never dropped: the UI
// shows the whole picture, not just what worked.
func (p Prober) ProbeAll(ctx context.Context, paths []Path) []Status {
	out := make([]Status, len(paths))
	if len(paths) == 0 {
		return out
	}

	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialCtx, cancel := context.WithTimeout(ctx, p.timeout())
			defer cancel()

			latency, err := p.dialer().Dial(dialCtx, path.Addr())
			out[i] = Status{Path: path, Latency: latency, Err: err}
		}()
	}
	wg.Wait()
	return out
}

// Select returns the first alive path in configured order.
//
// Order is preference, not latency: the config lists paths the way the user
// wants them tried, and sasash does not second-guess that. Fastest exists for
// when speed is the question being asked.
func Select(statuses []Status) (Status, bool) {
	for _, s := range statuses {
		if s.Alive() {
			return s, true
		}
	}
	return Status{}, false
}

// Fastest returns the alive path with the lowest latency, ties going to the
// earlier path.
func Fastest(statuses []Status) (Status, bool) {
	best, found := Status{}, false
	for _, s := range statuses {
		if !s.Alive() {
			continue
		}
		if !found || s.Latency < best.Latency {
			best, found = s, true
		}
	}
	return best, found
}

// Alive returns only the paths that answered, in input order.
func Alive(statuses []Status) []Status {
	out := make([]Status, 0, len(statuses))
	for _, s := range statuses {
		if s.Alive() {
			out = append(out, s)
		}
	}
	return out
}
