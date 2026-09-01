package fakerun_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
)

func TestFake_StubbedReplies(t *testing.T) {
	t.Parallel()

	f := fakerun.New().
		StubOut("Master running (pid=123)\n", "ssh", "-O", "check", "ubuntu").
		StubExit(255, "ssh", "-O", "check", "spp")

	got, err := f.Run(context.Background(), "ssh", "-O", "check", "ubuntu")
	if err != nil {
		t.Fatalf("Run() err = %v", err)
	}
	if !got.OK() || string(got.Stdout) != "Master running (pid=123)\n" {
		t.Errorf("Run() = %+v, want ok with master output", got)
	}

	got, err = f.Run(context.Background(), "ssh", "-O", "check", "spp")
	if err != nil {
		t.Fatalf("Run() err = %v", err)
	}
	if got.Code != 255 {
		t.Errorf("Code = %d, want 255", got.Code)
	}
}

// An unstubbed command must fail loudly. A fake that silently returns exit 0
// would let a test claim a tunnel is up when nothing ran.
func TestFake_UnstubbedFailsLoudly(t *testing.T) {
	t.Parallel()

	f := fakerun.New()
	if _, err := f.Run(context.Background(), "ssh", "-O", "exit", "ubuntu"); err == nil {
		t.Fatal("Run() err = nil, want error naming the unstubbed argv")
	}
}

func TestFake_UnmatchedFallback(t *testing.T) {
	t.Parallel()

	f := fakerun.New()
	f.Unmatched = &fakerun.Reply{Result: run.Result{Code: 1}}

	got, err := f.Run(context.Background(), "ssh", "anything")
	if err != nil {
		t.Fatalf("Run() err = %v", err)
	}
	if got.Code != 1 {
		t.Errorf("Code = %d, want 1", got.Code)
	}
}

func TestFake_PropagatesStubbedError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	f := fakerun.New().Stub(fakerun.Reply{Err: sentinel}, "ssh", "ubuntu")

	if _, err := f.Run(context.Background(), "ssh", "ubuntu"); !errors.Is(err, sentinel) {
		t.Errorf("Run() err = %v, want %v", err, sentinel)
	}
}

func TestFake_RecordsCallsInOrder(t *testing.T) {
	t.Parallel()

	f := fakerun.New().StubExit(0, "ssh", "-O", "check", "ubuntu").StubExit(0, "ssh", "-fN", "ubuntu")
	ctx := context.Background()
	_, _ = f.Run(ctx, "ssh", "-O", "check", "ubuntu")
	_, _ = f.Run(ctx, "ssh", "-fN", "ubuntu")

	want := []string{"ssh -O check ubuntu", "ssh -fN ubuntu"}
	if got := f.CallKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("CallKeys() = %v, want %v", got, want)
	}
}

// Calls returns copies: a caller mutating the result must not corrupt the log.
func TestFake_CallsAreCopies(t *testing.T) {
	t.Parallel()

	f := fakerun.New().StubExit(0, "ssh", "ubuntu")
	_, _ = f.Run(context.Background(), "ssh", "ubuntu")

	f.Calls()[0][1] = "tampered"
	if got := f.CallKeys(); got[0] != "ssh ubuntu" {
		t.Errorf("CallKeys() = %v, want untampered", got)
	}
}

func TestFake_RespectsCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := fakerun.New().StubExit(0, "ssh", "ubuntu")
	if _, err := f.Run(ctx, "ssh", "ubuntu"); err == nil {
		t.Fatal("Run() err = nil, want context error")
	}
}
