package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// fakeTunnel records the contract calls and fails on demand, keyed by set
// name — the CLI's decisions are argv parsing and exit codes, so the fake
// needs no argv or state-file machinery.
type fakeTunnel struct {
	upCalls   []string         // "<host>/<set>=<spec,spec>"
	exitCalls []string         // KillAll targets
	upErr     map[string]error // by set name
	exitErr   error
}

func (f *fakeTunnel) SpawnSet(_ context.Context, host, set string, specs []string) error {
	f.upCalls = append(f.upCalls, host+"/"+set+"="+strings.Join(specs, ","))
	return f.upErr[set]
}

func (f *fakeTunnel) KillAll(_ context.Context, host string) error {
	f.exitCalls = append(f.exitCalls, host)
	return f.exitErr
}

func writeUpDownConfig(t *testing.T) (root, home string) {
	t.Helper()
	config := `# @sasash forwards web 3000:localhost:3000 3001:localhost:3001
# @sasash forwards api 4011:localhost:4011
Host ubuntu
  HostName 192.0.2.10

Host plain
  HostName 192.0.2.5
`
	home = t.TempDir()
	root = filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	if err := os.WriteFile(root, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	return root, home
}

func TestRunUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		upErr       map[string]error
		wantCode    int
		wantUpCalls []string
		wantStderr  []string // substrings; nil means stderr must be EMPTY
	}{
		{
			name:     "default applies every declared set in meta order",
			args:     []string{"ubuntu"},
			wantCode: 0,
			wantUpCalls: []string{
				"ubuntu/web=3000:localhost:3000,3001:localhost:3001",
				"ubuntu/api=4011:localhost:4011",
			},
		},
		{
			name:        "named set applies only that set",
			args:        []string{"ubuntu", "api"},
			wantCode:    0,
			wantUpCalls: []string{"ubuntu/api=4011:localhost:4011"},
		},
		{
			name:       "unknown set lists the declared ones",
			args:       []string{"ubuntu", "bogus"},
			wantCode:   3,
			wantStderr: []string{`unknown forward set "bogus"`, "declared: web, api"},
		},
		{
			name:       "unknown set with none declared",
			args:       []string{"plain", "bogus"},
			wantCode:   3,
			wantStderr: []string{"declared: none"},
		},
		{
			name:       "host without sets has nothing to start",
			args:       []string{"plain"},
			wantCode:   3,
			wantStderr: []string{`no forward sets declared for "plain"`},
		},
		{
			name:        "first failing set aborts the rest",
			args:        []string{"ubuntu"},
			upErr:       map[string]error{"web": errors.New("ssh: connect refused")},
			wantCode:    1,
			wantUpCalls: []string{"ubuntu/web=3000:localhost:3000,3001:localhost:3001"},
			wantStderr:  []string{`set "web": ssh: connect refused`},
		},
		{
			name: "state write failure warns but is success",
			args: []string{"ubuntu"},
			upErr: map[string]error{
				"web": fmt.Errorf("forward added but state not recorded: %w", tunnel.ErrStateWrite),
			},
			wantCode: 0,
			wantUpCalls: []string{
				"ubuntu/web=3000:localhost:3000,3001:localhost:3001",
				"ubuntu/api=4011:localhost:4011",
			},
			wantStderr: []string{"warning: forward added but state not recorded"},
		},
		{
			name:       "unknown host",
			args:       []string{"bogus"},
			wantCode:   3,
			wantStderr: []string{`unknown host "bogus"`},
		},
		{
			name:       "no args",
			args:       nil,
			wantCode:   2,
			wantStderr: []string{"usage: sasash up"},
		},
		{
			name:       "too many args",
			args:       []string{"a", "b", "c"},
			wantCode:   2,
			wantStderr: []string{"usage: sasash up"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root, home := writeUpDownConfig(t)
			fake := &fakeTunnel{upErr: tt.upErr}
			var stdout, stderr bytes.Buffer
			got := runUp(tt.args, &stdout, &stderr, root, home, fake)
			if got != tt.wantCode {
				t.Errorf("runUp(%v) = %v, want %v (stderr: %q)", tt.args, got, tt.wantCode, stderr.String())
			}
			if !reflect.DeepEqual(fake.upCalls, tt.wantUpCalls) {
				t.Errorf("runUp(%v) Up calls = %v, want %v", tt.args, fake.upCalls, tt.wantUpCalls)
			}
			if stdout.String() != "" {
				t.Errorf("runUp(%v) stdout = %q, want empty", tt.args, stdout.String())
			}
			if len(tt.wantStderr) == 0 && stderr.String() != "" {
				t.Errorf("runUp(%v) stderr = %q, want empty", tt.args, stderr.String())
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("runUp(%v) stderr = %q, want substring %q", tt.args, stderr.String(), want)
				}
			}
		})
	}
}

// Unlike addr, up surfaces config warnings on SUCCESS too — these are
// interactive actions, stderr is free (same policy as status).
func TestRunUp_WarningsOnSuccess(t *testing.T) {
	t.Parallel()

	config := `# @sasash forwards broken
# @sasash forwards web 3000:localhost:3000
Host ubuntu
  HostName 192.0.2.10
`
	home := t.TempDir()
	root := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	if err := os.WriteFile(root, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	fake := &fakeTunnel{}
	var stdout, stderr bytes.Buffer
	got := runUp([]string{"ubuntu"}, &stdout, &stderr, root, home, fake)
	if got != 0 {
		t.Fatalf("runUp() = %v, want 0 (stderr: %q)", got, stderr.String())
	}
	if want := []string{"ubuntu/web=3000:localhost:3000"}; !reflect.DeepEqual(fake.upCalls, want) {
		t.Errorf("runUp() Up calls = %v, want %v", fake.upCalls, want)
	}
	if !strings.Contains(stderr.String(), "warning") || !strings.Contains(stderr.String(), "missing specs") {
		t.Errorf("runUp() stderr = %q, want the malformed-meta warning on success", stderr.String())
	}
}

// One end-to-end pin through the REAL engine: runUp's set iteration composed
// with Engine.SpawnSet must spawn exactly the SPEC §4 process per forward,
// in meta order, and run nothing else — the runner sees no call at all.
func TestRunUp_DrivesRealEngineArgv(t *testing.T) {
	t.Parallel()

	root, home := writeUpDownConfig(t)
	runner := fakerun.New()
	d := fakedial.New()
	sp := fakespawn.New().Dial(d)
	eng := tunnel.Engine{Runner: runner, Spawner: sp, Dialer: d, StateDir: t.TempDir(), Timeout: 50 * time.Millisecond}

	var stdout, stderr bytes.Buffer
	if got := runUp([]string{"ubuntu"}, &stdout, &stderr, root, home, eng); got != 0 {
		t.Fatalf("runUp() = %v, want 0 (stderr: %q)", got, stderr.String())
	}
	want := []string{
		"ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -L 3000:localhost:3000 ubuntu",
		"ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -L 3001:localhost:3001 ubuntu",
		"ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -L 4011:localhost:4011 ubuntu",
	}
	if got := sp.Spawned(); !reflect.DeepEqual(got, want) {
		t.Errorf("runUp() spawned = %v, want %v", got, want)
	}
	if got := runner.CallKeys(); len(got) != 0 {
		t.Errorf("runUp() ran %v through the runner, want nothing", got)
	}
}

func TestRunUp_MissingConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	fake := &fakeTunnel{}
	var stdout, stderr bytes.Buffer
	got := runUp([]string{"ubuntu"}, &stdout, &stderr, filepath.Join(home, ".ssh", "config"), home, fake)
	if got != 3 {
		t.Errorf("runUp() = %v, want 3", got)
	}
	if len(fake.upCalls) != 0 {
		t.Errorf("runUp() Up calls = %v, want none", fake.upCalls)
	}
	if !strings.Contains(stderr.String(), "no such file") {
		t.Errorf("runUp() stderr = %q, want the missing-config error", stderr.String())
	}
}

func TestRunDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		args          []string
		exitErr       error
		wantCode      int
		wantExitCalls []string
		wantStderr    []string // substrings; nil means stderr must be EMPTY
	}{
		{
			name:          "kills every tunnel process of the host",
			args:          []string{"ubuntu"},
			wantCode:      0,
			wantExitCalls: []string{"ubuntu"},
		},
		{
			// CONTRACT-tunnel: the processes really went down; only the
			// cache clear failed. Mirrors up's handling.
			name:          "state write failure warns but is success",
			args:          []string{"ubuntu"},
			exitErr:       fmt.Errorf("kill attempted but state not recorded: %w", tunnel.ErrStateWrite),
			wantCode:      0,
			wantExitCalls: []string{"ubuntu"},
			wantStderr:    []string{"warning: kill attempted but state not recorded"},
		},
		{
			name:          "a process that survives is exit 1 with the reason",
			args:          []string{"ubuntu"},
			exitErr:       errors.New("pid 4242 still running after SIGTERM: context deadline exceeded"),
			wantCode:      1,
			wantExitCalls: []string{"ubuntu"},
			wantStderr:    []string{"pid 4242 still running"},
		},
		{
			name:       "unknown host",
			args:       []string{"bogus"},
			wantCode:   3,
			wantStderr: []string{`unknown host "bogus"`},
		},
		{
			name:       "no args",
			args:       nil,
			wantCode:   2,
			wantStderr: []string{"usage: sasash down"},
		},
		{
			name:       "too many args",
			args:       []string{"a", "b"},
			wantCode:   2,
			wantStderr: []string{"usage: sasash down"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root, home := writeUpDownConfig(t)
			fake := &fakeTunnel{exitErr: tt.exitErr}
			var stdout, stderr bytes.Buffer
			got := runDown(tt.args, &stdout, &stderr, root, home, fake)
			if got != tt.wantCode {
				t.Errorf("runDown(%v) = %v, want %v (stderr: %q)", tt.args, got, tt.wantCode, stderr.String())
			}
			if !reflect.DeepEqual(fake.exitCalls, tt.wantExitCalls) {
				t.Errorf("runDown(%v) Exit calls = %v, want %v", tt.args, fake.exitCalls, tt.wantExitCalls)
			}
			if stdout.String() != "" {
				t.Errorf("runDown(%v) stdout = %q, want empty", tt.args, stdout.String())
			}
			if len(tt.wantStderr) == 0 && stderr.String() != "" {
				t.Errorf("runDown(%v) stderr = %q, want empty", tt.args, stderr.String())
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("runDown(%v) stderr = %q, want substring %q", tt.args, stderr.String(), want)
				}
			}
		})
	}
}
