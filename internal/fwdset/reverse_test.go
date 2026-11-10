package fwdset_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/fwdset"
	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Reverse forwards (SASASH.4.41): an `R…` spec is declared, rewritten and
// removed like any other; the R is part of the token.

func TestAdd_ReverseSpecs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		specs []string
		want  string
	}{
		{
			name:  "one reverse spec",
			specs: []string{"R2222:localhost:22"},
			want:  "# @sasash forwards mac R2222:localhost:22\nHost web\n  HostName 192.0.2.10\n",
		},
		{
			name:  "mixed with local specs",
			specs: []string{"3000:localhost:3000", "R2222:localhost:22", "Rlocalhost:8022:localhost:22"},
			want:  "# @sasash forwards mac 3000:localhost:3000 R2222:localhost:22 Rlocalhost:8022:localhost:22\nHost web\n  HostName 192.0.2.10\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, "Host web\n  HostName 192.0.2.10\n")
			if err := fwdset.Add(path, "web", "mac", tt.specs); err != nil {
				t.Fatalf("Add() = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
		})
	}
}

// Validation: valid reverse specs pass, malformed ones are refused, and
// every -L case keeps exactly the verdict it had.
func TestAdd_ReverseValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		spec    string
		wantErr string // "" = accepted
	}{
		{name: "reverse port host port", spec: "R2222:localhost:22"},
		{name: "reverse with remote bind", spec: "Rlocalhost:2222:localhost:22"},
		{name: "reverse with empty bind", spec: "R:2222:localhost:22"},
		{name: "reverse to a LAN host", spec: "R8080:192.0.2.10:80"},
		{name: "reverse bare port", spec: "R2222"},
		{name: "R alone", spec: "R", wantErr: `spec "R": no remote port`},
		{name: "reverse without a remote port", spec: "Rbox:web:22", wantErr: `spec "Rbox:web:22": no remote port`},
		{name: "reverse IPv6 bind hides the port", spec: "R[::1]:2222:localhost:22", wantErr: "no remote port"},
		{name: "lowercase r is not reverse", spec: "r2222:localhost:22", wantErr: `spec "r2222:localhost:22": no local port`},
		{name: "local unchanged: plain", spec: "3000:localhost:3000"},
		{name: "local unchanged: bind", spec: "127.0.0.1:3000:localhost:3000"},
		{name: "local unchanged: nonsense", spec: "nonsense", wantErr: `spec "nonsense": no local port`},
		{name: "local unchanged: IPv6 bind", spec: "[::1]:3000:localhost:3000", wantErr: "no local port"},
		{name: "local unchanged: whitespace", spec: "3000 :localhost:3000", wantErr: "no local port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, "Host web\n  HostName 192.0.2.10\n")
			err := fwdset.Add(path, "web", "s", []string{tt.spec})
			if tt.wantErr == "" && err != nil {
				t.Errorf("Add(%q) = %v, want accepted", tt.spec, err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("Add(%q) = %v, want error containing %q", tt.spec, err, tt.wantErr)
			}
		})
	}
}

// The writer's reverse rule is the engine's: whatever tunnel.Reverse
// classifies as reverse, Add accepts — and an R spec it does not, Add
// refuses.
func TestAdd_ReverseRuleMatchesEngine(t *testing.T) {
	t.Parallel()
	specs := []string{"R2222:localhost:22", "Rlocalhost:2222:localhost:22", "R:2222:x:22", "R2222", "R", "Rbox", "Rbox:web:22", "R[::1]:2222:x:22"}
	for _, spec := range specs {
		t.Run(strconv.Quote(spec), func(t *testing.T) {
			t.Parallel()
			_, engineReverse := tunnel.Reverse(spec)
			path := writeConf(t, "Host web\n  HostName 192.0.2.10\n")
			err := fwdset.Add(path, "web", "s", []string{spec})
			if (err == nil) != engineReverse {
				t.Errorf("Add(%q) = %v; tunnel.Reverse ok = %v — the rules diverge", spec, err, engineReverse)
			}
		})
	}
}

func TestUpdate_ReverseSpecs(t *testing.T) {
	t.Parallel()
	const in = "# @sasash forwards mac 3000:localhost:3000 R2222:localhost:22\nHost web\n  HostName 192.0.2.10\n"
	tests := []struct {
		name     string
		old, new string
		want     string
		wantErr  string
	}{
		{name: "reverse to reverse", old: "R2222:localhost:22", new: "R2223:localhost:22", want: "# @sasash forwards mac 3000:localhost:3000 R2223:localhost:22\nHost web\n  HostName 192.0.2.10\n"},
		{name: "local to reverse", old: "3000:localhost:3000", new: "R3000:localhost:3000", want: "# @sasash forwards mac R3000:localhost:3000 R2222:localhost:22\nHost web\n  HostName 192.0.2.10\n"},
		{name: "reverse to local", old: "R2222:localhost:22", new: "2222:localhost:22", want: "# @sasash forwards mac 3000:localhost:3000 2222:localhost:22\nHost web\n  HostName 192.0.2.10\n"},
		{name: "the R is part of the identity", old: "2222:localhost:22", new: "2223:localhost:22", wantErr: `has no spec "2222:localhost:22"`},
		{name: "malformed reverse refused", old: "R2222:localhost:22", new: "Rbox", wantErr: `spec "Rbox": no remote port`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, in)
			err := fwdset.Update(path, "web", "mac", tt.old, tt.new)
			want := tt.want
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Update() = %v, want error containing %q", err, tt.wantErr)
				}
				want = in
			} else if err != nil {
				t.Fatalf("Update() = %v", err)
			}
			got, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("read: %v", rerr)
			}
			if string(got) != want {
				t.Errorf("file = %q, want %q", got, want)
			}
		})
	}
}

func TestRemove_ReverseSet(t *testing.T) {
	t.Parallel()
	path := writeConf(t, "# @sasash forwards web 3000:localhost:3000\n# @sasash forwards mac R2222:localhost:22\nHost web\n  HostName 192.0.2.10\n")
	if err := fwdset.Remove(path, "web", "mac"); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "# @sasash forwards web 3000:localhost:3000\nHost web\n  HostName 192.0.2.10\n"; string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestAdd_ReverseRoundTripsThroughHostmodel(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "config.d"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root := filepath.Join(sshDir, "config")
	conf := filepath.Join(sshDir, "config.d", "sasash.conf")
	if err := os.WriteFile(root, []byte("Include ~/.ssh/config.d/sasash.conf\n"), 0o600); err != nil {
		t.Fatalf("write root: %v", err)
	}
	if err := os.WriteFile(conf, []byte("Host web\n  HostName 192.0.2.10\n"), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	if err := fwdset.Add(conf, "web", "mac", []string{"3000:localhost:3000", "R2222:localhost:22"}); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load() = %v, warnings %v", err, warnings)
	}
	want := []hostmodel.ForwardSet{{Name: "mac", Specs: []string{"3000:localhost:3000", "R2222:localhost:22"}}}
	if len(hosts) != 1 || !reflect.DeepEqual(hosts[0].Forwards, want) {
		t.Errorf("Load() forwards = %+v, want %+v", hosts, want)
	}
}
