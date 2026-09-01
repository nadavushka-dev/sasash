// Package fakedial provides a probe.Dialer for tests.
package fakedial

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe"
)

// ErrUnstubbed is returned for an address with no configured reply, so a
// forgotten stub reads as "down" rather than silently succeeding.
var ErrUnstubbed = errors.New("fakedial: no stub for address")

// Reply is the outcome for one address.
type Reply struct {
	Latency time.Duration
	Err     error
	// Delay makes Dial actually block, for asserting that probing is
	// concurrent and that timeouts are honored.
	Delay time.Duration
}

// Fake is a probe.Dialer backed by a table keyed on address.
type Fake struct {
	Replies map[string]Reply
	Default *Reply

	mu    sync.Mutex
	calls []string
}

var _ probe.Dialer = (*Fake)(nil)

// New returns a Fake with an empty table.
func New() *Fake { return &Fake{Replies: map[string]Reply{}} }

// Up stubs addr as reachable with the given latency.
func (f *Fake) Up(addr string, latency time.Duration) *Fake {
	return f.Stub(addr, Reply{Latency: latency})
}

// Down stubs addr as unreachable.
func (f *Fake) Down(addr string) *Fake {
	return f.Stub(addr, Reply{Err: errors.New("connection refused")})
}

// Stub registers a reply for addr.
func (f *Fake) Stub(addr string, reply Reply) *Fake {
	if f.Replies == nil {
		f.Replies = map[string]Reply{}
	}
	f.Replies[addr] = reply
	return f
}

// Dial implements probe.Dialer.
func (f *Fake) Dial(ctx context.Context, addr string) (time.Duration, error) {
	f.mu.Lock()
	f.calls = append(f.calls, addr)
	f.mu.Unlock()

	reply, ok := f.Replies[addr]
	if !ok {
		if f.Default == nil {
			return 0, ErrUnstubbed
		}
		reply = *f.Default
	}

	if reply.Delay > 0 {
		timer := time.NewTimer(reply.Delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return reply.Latency, reply.Err
}

// Calls returns every address dialled, in the order the calls were recorded.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
