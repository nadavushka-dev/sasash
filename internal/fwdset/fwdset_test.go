package fwdset_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/fwdset"
	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
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

func TestAdd_MissingFile(t *testing.T) {
	t.Parallel()
	err := fwdset.Add(filepath.Join(t.TempDir(), "sasash.conf"), "web", "db", []string{"5432:localhost:5432"})
	if err == nil || !os.IsNotExist(err) {
		t.Errorf("Add() on a missing file = %v, want a not-exist error", err)
	}
}
