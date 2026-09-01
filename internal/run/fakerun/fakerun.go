// Package fakerun provides a Runner for tests: it answers from a table keyed
// by the joined argv and records every call.
package fakerun

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/nadavushka-dev/sasash/internal/run"
)

// Reply is what the fake returns for one argv.
type Reply struct {
	Result run.Result
	Err    error
}

// Fake is a run.Runner backed by a lookup table.
//
// Keys are the argv joined by a single space. A call with no matching key
// returns Unmatched if set, otherwise an error naming the argv, so a test that
// forgets to stub a command fails loudly instead of silently seeing exit 0.
type Fake struct {
	Replies   map[string]Reply
	Unmatched *Reply

	mu    sync.Mutex
	calls [][]string
}

var _ run.Runner = (*Fake)(nil)

// New returns a Fake with an empty table.
func New() *Fake { return &Fake{Replies: map[string]Reply{}} }

// Stub registers a reply for an exact argv.
func (f *Fake) Stub(reply Reply, argv ...string) *Fake {
	if f.Replies == nil {
		f.Replies = map[string]Reply{}
	}
	f.Replies[Key(argv...)] = reply
	return f
}

// StubExit registers a reply that is just an exit code.
func (f *Fake) StubExit(code int, argv ...string) *Fake {
	return f.Stub(Reply{Result: run.Result{Code: code}}, argv...)
}

// StubOut registers a reply with stdout and exit 0.
func (f *Fake) StubOut(stdout string, argv ...string) *Fake {
	return f.Stub(Reply{Result: run.Result{Stdout: []byte(stdout)}}, argv...)
}

// Run implements run.Runner.
func (f *Fake) Run(ctx context.Context, argv ...string) (run.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return run.Result{}, err
	}
	if reply, ok := f.Replies[Key(argv...)]; ok {
		return reply.Result, reply.Err
	}
	if f.Unmatched != nil {
		return f.Unmatched.Result, f.Unmatched.Err
	}
	return run.Result{}, fmt.Errorf("fakerun: no stub for %q", Key(argv...))
}

// Calls returns every argv the fake has seen, in order.
func (f *Fake) Calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = append([]string(nil), c...)
	}
	return out
}

// CallKeys returns Calls flattened to the same keys used by Stub, which is
// what argv-construction tests assert against.
func (f *Fake) CallKeys() []string {
	calls := f.Calls()
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = Key(c...)
	}
	return out
}

// Key is the table key for an argv.
func Key(argv ...string) string { return strings.Join(argv, " ") }
