package fwdset_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/fwdset"
	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sasash.conf")
	// 0640 differs from sshconf.DefaultPerm so the preserved-perm check
	// cannot pass by accident.
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestAdd_ExactBytes(t *testing.T) {
	t.Parallel()
	const line = "# @sasash forwards web 3000:localhost:3000 3001:localhost:3001"
	tests := []struct {
		name  string
		in    string
		alias string
		want  string
	}{
		{
			name:  "host first in file",
			in:    "Host web\n  HostName 192.0.2.10\n",
			alias: "web",
			want:  line + "\nHost web\n  HostName 192.0.2.10\n",
		},
		{
			name:  "existing own meta run grows",
			in:    "# managed\n\n# @sasash path lan 192.0.2.10\n# @sasash tags dev\nHost web\n  HostName 192.0.2.10\n",
			alias: "web",
			want:  "# managed\n\n# @sasash path lan 192.0.2.10\n# @sasash tags dev\n" + line + "\nHost web\n  HostName 192.0.2.10\n",
		},
		{
			name:  "host after another block, odd indent and unknown lines kept",
			in:    "Host a\n\tHostName 192.0.2.1\n  UnknownDirective kept\nHost web\n  HostName 192.0.2.10\nHost c\n  HostName 192.0.2.3\n",
			alias: "web",
			want:  "Host a\n\tHostName 192.0.2.1\n  UnknownDirective kept\n" + line + "\nHost web\n  HostName 192.0.2.10\nHost c\n  HostName 192.0.2.3\n",
		},
		{
			name:  "CRLF file gets a CRLF line",
			in:    "Host web\r\n  HostName 192.0.2.10\r\n",
			alias: "web",
			want:  line + "\r\nHost web\r\n  HostName 192.0.2.10\r\n",
		},
		{
			name:  "host line without a trailing newline",
			in:    "# managed\nHost web",
			alias: "web",
			want:  "# managed\n" + line + "\nHost web",
		},
		{
			name:  "first pattern names the host",
			in:    "Host web web.example\n  HostName 192.0.2.10\n",
			alias: "web",
			want:  line + "\nHost web web.example\n  HostName 192.0.2.10\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, tt.in)
			if err := fwdset.Add(path, tt.alias, "web", []string{"3000:localhost:3000", "3001:localhost:3001"}); err != nil {
				t.Fatalf("Add() = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if perm := fi.Mode().Perm(); perm != 0o640 {
				t.Errorf("perm = %v, want 0640 preserved", perm)
			}
		})
	}
}

func TestAdd_AttachmentIsExclusive(t *testing.T) {
	t.Parallel()
	// The previous block must not gain the line: its OwnMeta is the same
	// before and after, and the target host owns the new set.
	in := "# @sasash tags a\nHost a\n  HostName 192.0.2.1\n# @sasash tags b\nHost b\n  HostName 192.0.2.2\n"
	path := writeConf(t, in)
	before, err := sshconf.ParseFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ha, _ := before.Host("a")
	wantA := ha.OwnMeta()
	if err := fwdset.Add(path, "b", "db", []string{"5432:localhost:5432"}); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	after, err := sshconf.ParseFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ha2, _ := after.Host("a")
	if got := ha2.OwnMeta(); !reflect.DeepEqual(metaText(got), metaText(wantA)) {
		t.Errorf("host a OwnMeta after = %v, want unchanged %v", metaText(got), metaText(wantA))
	}
	hb, _ := after.Host("b")
	if got := hb.OwnMetaVerb("forwards"); len(got) != 1 || !reflect.DeepEqual(got[0].Args, []string{"db", "5432:localhost:5432"}) {
		t.Errorf("host b forwards meta = %+v, want the db set", got)
	}
}

func metaText(ms []sshconf.Meta) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Verb+" "+strings.Join(m.Args, " "))
	}
	return out
}

func TestAdd_RoundTripsThroughHostmodel(t *testing.T) {
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
	if err := os.WriteFile(conf, []byte("# managed\n\n# @sasash forwards web 3000:localhost:3000\nHost web\n  HostName 192.0.2.10\n"), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	if err := fwdset.Add(conf, "web", "api", []string{"4011:localhost:4011", "4012:localhost:4012"}); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("Load() warnings = %v, want none", warnings)
	}
	want := []hostmodel.ForwardSet{
		{Name: "web", Specs: []string{"3000:localhost:3000"}},
		{Name: "api", Specs: []string{"4011:localhost:4011", "4012:localhost:4012"}},
	}
	if len(hosts) != 1 || !reflect.DeepEqual(hosts[0].Forwards, want) {
		t.Errorf("Load() forwards = %+v, want %+v", hosts, want)
	}
}

func TestAdd_Errors(t *testing.T) {
	t.Parallel()
	const in = "# @sasash forwards web 3000:localhost:3000\nHost web\n  HostName 192.0.2.10\n"
	tests := []struct {
		name    string
		alias   string
		set     string
		specs   []string
		wantErr string
	}{
		{name: "unknown host", alias: "nope", set: "db", specs: []string{"5432:localhost:5432"}, wantErr: `host "nope" not managed by sasash`},
		{name: "set already declared, case-insensitively", alias: "web", set: "WEB", specs: []string{"5432:localhost:5432"}, wantErr: `forward set "WEB" already declared for "web"`},
		{name: "empty name", alias: "web", set: "", specs: []string{"5432:localhost:5432"}, wantErr: "want a single word"},
		{name: "name with space", alias: "web", set: "my db", specs: []string{"5432:localhost:5432"}, wantErr: "want a single word"},
		{name: "name with hash", alias: "web", set: "db#1", specs: []string{"5432:localhost:5432"}, wantErr: "want a single word"},
		{name: "no specs", alias: "web", set: "db", specs: nil, wantErr: `forward set "db": no specs`},
		{name: "spec without a local port", alias: "web", set: "db", specs: []string{"nonsense"}, wantErr: `spec "nonsense": no local port`},
		{name: "IPv6 bind address hides the port from the engine too", alias: "web", set: "db", specs: []string{"[::1]:5432:localhost:5432"}, wantErr: "no local port"},
		{name: "spec with whitespace", alias: "web", set: "db", specs: []string{"5432 :localhost:5432"}, wantErr: "no local port"},
		{name: "repeated spec", alias: "web", set: "db", specs: []string{"5432:localhost:5432", "5432:localhost:5432"}, wantErr: `spec "5432:localhost:5432" repeated`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, in)
			err := fwdset.Add(path, tt.alias, tt.set, tt.specs)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Add() = %v, want error containing %q", err, tt.wantErr)
			}
			// Nothing written on any error.
			got, rerr := os.ReadFile(path)
			if rerr != nil || string(got) != in {
				t.Errorf("file after a rejected Add = %q, want untouched", got)
			}
		})
	}
}

func TestAdd_ThroughSymlink(t *testing.T) {
	t.Parallel()
	// A dotfiles-managed sasash.conf is a symlink: the link survives and
	// the target gets the bytes.
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "sasash.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("Host web\n  HostName 192.0.2.10\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "sasash.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := fwdset.Add(link, "web", "db", []string{"5432:localhost:5432"}); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link after Add: %v, %v; want still a symlink", fi, err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if want := "# @sasash forwards db 5432:localhost:5432\nHost web\n  HostName 192.0.2.10\n"; string(got) != want {
		t.Errorf("target = %q, want %q", got, want)
	}
}

func TestAdd_LocalPortRuleMatchesEngine(t *testing.T) {
	t.Parallel()
	// The writer's "no local port" rule is the engine's (CONTRACT-tunnel
	// §5): whatever the engine can judge by a port, Add accepts, and only
	// that. Pinned against Engine.Status's Port for each spec shape.
	specs := []string{
		"8080:localhost:80",
		"127.0.0.1:8080:localhost:80",
		"*:8080:localhost:80",
		"[::1]:8080:localhost:80",
		"localhost:8080:localhost:80",
		"localhost:5432",
		"nonsense",
		":8080:localhost:80",
	}
	for _, spec := range specs {
		t.Run(strconv.Quote(spec), func(t *testing.T) {
			t.Parallel()
			r := fakerun.New().StubExit(0, "ssh", "-O", "check", "web")
			eng := tunnel.Engine{Runner: r, Dialer: fakedial.New(), StateDir: t.TempDir(), Timeout: time.Millisecond}
			st, err := eng.Status(context.Background(), "web", []hostmodel.ForwardSet{{Name: "s", Specs: []string{spec}}})
			if err != nil {
				t.Fatalf("Status() = %v", err)
			}
			engineAccepts := len(st.Forwards) == 1 && st.Forwards[0].Port != ""
			path := writeConf(t, "Host web\n  HostName 192.0.2.10\n")
			addErr := fwdset.Add(path, "web", "s", []string{spec})
			if (addErr == nil) != engineAccepts {
				t.Errorf("Add(%q) error = %v; engine judges it by a port = %v — the rules diverge", spec, addErr, engineAccepts)
			}
		})
	}
}

func TestAdd_MissingFile(t *testing.T) {
	t.Parallel()
	err := fwdset.Add(filepath.Join(t.TempDir(), "sasash.conf"), "web", "db", []string{"5432:localhost:5432"})
	if err == nil || !os.IsNotExist(err) {
		t.Errorf("Add() on a missing file = %v, want a not-exist error", err)
	}
}

func TestUpdate_ExactBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		set  string
		want string
	}{
		{
			name: "middle spec of three, spacing kept",
			in:   "#@sasash  forwards   web  3000:localhost:3000   3001:localhost:3001 3002:localhost:3002\nHost web\n  HostName 192.0.2.10\n",
			set:  "web",
			want: "#@sasash  forwards   web  3000:localhost:3000   3001:localhost:3002 3002:localhost:3002\nHost web\n  HostName 192.0.2.10\n",
		},
		{
			name: "CRLF and casing kept, other lines untouched",
			in:   "# managed\r\n# @sasash Forwards WEB 3001:localhost:3001\r\n# @sasash forwards api 3001:localhost:3001\r\nHost web\r\n  HostName 192.0.2.10\r\n",
			set:  "web",
			want: "# managed\r\n# @sasash Forwards WEB 3001:localhost:3002\r\n# @sasash forwards api 3001:localhost:3001\r\nHost web\r\n  HostName 192.0.2.10\r\n",
		},
		{
			// In-block meta followed by a directive (a run leading straight
			// into the next Host line would belong to that host).
			name: "meta inside the block, indented",
			in:   "Host web\n  # @sasash forwards web 3001:localhost:3001\n  HostName 192.0.2.10\nHost other\n  HostName 192.0.2.11\n",
			set:  "web",
			want: "Host web\n  # @sasash forwards web 3001:localhost:3002\n  HostName 192.0.2.10\nHost other\n  HostName 192.0.2.11\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, tt.in)
			if err := fwdset.Update(path, "web", tt.set, "3001:localhost:3001", "3001:localhost:3002"); err != nil {
				t.Fatalf("Update() = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
			if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o640 {
				t.Errorf("perm = %v, %v; want 0640 preserved", fi.Mode().Perm(), err)
			}
		})
	}
}

func TestUpdate_RoundTripsThroughHostmodel(t *testing.T) {
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
	if err := os.WriteFile(conf, []byte("# @sasash forwards web 3000:localhost:3000 3001:localhost:3001\nHost web\n  HostName 192.0.2.10\n"), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	if err := fwdset.Update(conf, "web", "web", "3001:localhost:3001", "3001:localhost:9001"); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load() = %v, warnings %v", err, warnings)
	}
	want := []hostmodel.ForwardSet{{Name: "web", Specs: []string{"3000:localhost:3000", "3001:localhost:9001"}}}
	if len(hosts) != 1 || !reflect.DeepEqual(hosts[0].Forwards, want) {
		t.Errorf("Load() forwards = %+v, want %+v", hosts, want)
	}
}

func TestUpdate_Errors(t *testing.T) {
	t.Parallel()
	const in = "# @sasash forwards web 3000:localhost:3000 3001:localhost:3001\nHost web\n  HostName 192.0.2.10\n"
	tests := []struct {
		name       string
		alias, set string
		old, new   string
		wantErr    string
	}{
		{name: "unknown host", alias: "nope", set: "web", old: "3000:localhost:3000", new: "3000:localhost:1", wantErr: `host "nope" not managed by sasash`},
		{name: "unknown set", alias: "web", set: "api", old: "3000:localhost:3000", new: "3000:localhost:1", wantErr: `forward set "api" on "web" has no spec "3000:localhost:3000"`},
		{name: "old spec absent", alias: "web", set: "web", old: "3009:localhost:3009", new: "3000:localhost:1", wantErr: `has no spec "3009:localhost:3009"`},
		{name: "new already present", alias: "web", set: "web", old: "3000:localhost:3000", new: "3001:localhost:3001", wantErr: `spec "3001:localhost:3001" already in set "web"`},
		{name: "new without a local port", alias: "web", set: "web", old: "3000:localhost:3000", new: "nonsense", wantErr: `spec "nonsense": no local port`},
		{name: "unchanged", alias: "web", set: "web", old: "3000:localhost:3000", new: "3000:localhost:3000", wantErr: `spec "3000:localhost:3000" unchanged`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeConf(t, in)
			err := fwdset.Update(path, tt.alias, tt.set, tt.old, tt.new)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Update() = %v, want error containing %q", err, tt.wantErr)
			}
			if got, rerr := os.ReadFile(path); rerr != nil || string(got) != in {
				t.Errorf("file after a rejected Update = %q, want untouched", got)
			}
		})
	}
}
