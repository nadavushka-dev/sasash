package pathswitch_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
)

func TestRename_ExactBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain block, meta above stays put",
			in:   "# @sasash forwards web 3000:localhost:3000\nHost web\n  HostName 192.0.2.10\n",
			want: "# @sasash forwards web 3000:localhost:3000\nHost srv\n  HostName 192.0.2.10\n",
		},
		{
			name: "further patterns, tab separator and trailing whitespace kept",
			in:   "Host\tweb web.example  \n  HostName 192.0.2.10\n",
			want: "Host\tsrv web.example  \n  HostName 192.0.2.10\n",
		},
		{
			name: "equals separator",
			in:   "Host=web\n  HostName 192.0.2.10\n",
			want: "Host=srv\n  HostName 192.0.2.10\n",
		},
		{
			name: "spaced equals separator and keyword casing",
			in:   "host = web\n  HostName 192.0.2.10\n",
			want: "host = srv\n  HostName 192.0.2.10\n",
		},
		{
			name: "CRLF",
			in:   "Host web\r\n  HostName 192.0.2.10\r\n",
			want: "Host srv\r\n  HostName 192.0.2.10\r\n",
		},
		{
			name: "Host line is the unterminated last line",
			in:   "# managed\nHost web",
			want: "# managed\nHost srv",
		},
		{
			name: "other hosts and a foreign host sharing the prefix survive",
			in:   "Host a\n  HostName 192.0.2.1\nHost web\n  HostName 192.0.2.10\n# a real host that happens to share the prefix\nHost web.staging\n  HostName 203.0.113.44\n  IdentityFile ~/.ssh/deploy_ed25519\nHost webb\n  HostName 192.0.2.11\n",
			want: "Host a\n  HostName 192.0.2.1\nHost srv\n  HostName 192.0.2.10\n# a real host that happens to share the prefix\nHost web.staging\n  HostName 203.0.113.44\n  IdentityFile ~/.ssh/deploy_ed25519\nHost webb\n  HostName 192.0.2.11\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := write(t, tt.in)
			if _, err := pathswitch.Rename(path, "web", "srv"); err != nil {
				t.Fatalf("Rename() = %v", err)
			}
			if got := read(t, path); got != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRename_VariantsFollow(t *testing.T) {
	t.Parallel()
	// Owned variants follow — live and stale alike, the Host line only —
	// while a hand-authored dotted block after the group, a block demoted
	// below a user note inside the group, and a shadowing block above the
	// header survive verbatim and are reported.
	in := `# @sasash path lan 192.0.2.10
# @sasash path vpn 198.51.100.10
Host web
  HostName 192.0.2.10
  User dev
Host web.vpn
  HostName 198.51.100.10
  User dev

# pinned variants, generated — for when you want to force one path
Host web.lan
  HostName 192.0.2.10
  User dev
Host web.stale
  HostName 203.0.113.9
  User dev
# my note breaks the chain
Host web.demoted
  HostName 203.0.113.10
  User dev
Host web.prod
  HostName 203.0.113.44
  User deploy
  IdentityFile ~/.ssh/deploy_ed25519
Host next
  HostName 192.0.2.11
`
	path := write(t, in)
	res, err := pathswitch.Rename(path, "web", "srv")
	if err != nil {
		t.Fatalf("Rename() = %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path vpn 198.51.100.10
Host srv
  HostName 192.0.2.10
  User dev
Host web.vpn
  HostName 198.51.100.10
  User dev

# pinned variants, generated — for when you want to force one path
Host srv.lan
  HostName 192.0.2.10
  User dev
Host srv.stale
  HostName 203.0.113.9
  User dev
# my note breaks the chain
Host web.demoted
  HostName 203.0.113.10
  User dev
Host web.prod
  HostName 203.0.113.44
  User deploy
  IdentityFile ~/.ssh/deploy_ed25519
Host next
  HostName 192.0.2.11
`
	if got := read(t, path); got != want {
		t.Errorf("file =\n%q\nwant\n%q", got, want)
	}
	wantRes := pathswitch.RenameResult{
		Followed: []string{"lan", "stale"},
		LeftAsIs: []string{"web.vpn", "web.demoted", "web.prod"},
	}
	if !reflect.DeepEqual(res, wantRes) {
		t.Errorf("Rename() = %+v, want %+v", res, wantRes)
	}
	// The renamed group is still the generator's: a switch on the new
	// alias rewrites it in place and removes the stale variant.
	sw, err := pathswitch.Set(path, "srv", "vpn")
	if err != nil {
		t.Fatalf("Set() after rename = %v", err)
	}
	if !sw.Changed || !strings.Contains(read(t, path), "Host srv.vpn\n") || strings.Contains(read(t, path), "srv.stale") {
		t.Errorf("Set() after rename: changed=%v file=%q; want the group regenerated under srv", sw.Changed, read(t, path))
	}
}

func TestRename_NoVariantsResultIsZero(t *testing.T) {
	t.Parallel()
	path := write(t, "Host web\n  HostName 192.0.2.10\n")
	res, err := pathswitch.Rename(path, "web", "srv")
	if err != nil {
		t.Fatalf("Rename() = %v", err)
	}
	if !reflect.DeepEqual(res, pathswitch.RenameResult{}) {
		t.Errorf("Rename() = %+v, want the zero result", res)
	}
}

func TestRename_PreservesPerm(t *testing.T) {
	t.Parallel()
	path := write(t, "Host web\n  HostName 192.0.2.10\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := pathswitch.Rename(path, "web", "srv"); err != nil {
		t.Fatalf("Rename() = %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o640 {
		t.Errorf("perm = %v, want 0640 preserved", perm)
	}
}

func TestRename_RoundTripsThroughHostmodel(t *testing.T) {
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
	in := "# @sasash path lan 192.0.2.10\n# @sasash path vpn 198.51.100.10\n# @sasash forwards web 3000:localhost:3000\nHost web\n  HostName 192.0.2.10\n\n" +
		pathswitch.Header + "\nHost web.lan\n  HostName 192.0.2.10\nHost web.vpn\n  HostName 198.51.100.10\n"
	if err := os.WriteFile(conf, []byte(in), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	before, _, err := hostmodel.Load(root, home)
	if err != nil {
		t.Fatalf("Load() before = %v", err)
	}
	if _, err := pathswitch.Rename(conf, "web", "srv"); err != nil {
		t.Fatalf("Rename() = %v", err)
	}
	after, warnings, err := hostmodel.Load(root, home)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load() after = %v, warnings %v", err, warnings)
	}
	// The listing is the same hosts under the new prefix (variants are
	// listed too), and the renamed host keeps its paths and forwards.
	if got, want := aliases(after), []string{"srv", "srv.lan", "srv.vpn"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Load() aliases after = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(after[0].Paths, before[0].Paths) || !reflect.DeepEqual(after[0].Forwards, before[0].Forwards) {
		t.Errorf("Load() paths/forwards after = %+v/%+v, want unchanged %+v/%+v", after[0].Paths, after[0].Forwards, before[0].Paths, before[0].Forwards)
	}
}

func aliases(hosts []hostmodel.Host) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Alias
	}
	return out
}

func TestRename_Errors(t *testing.T) {
	t.Parallel()
	const in = "Host web\n  HostName 192.0.2.10\nHost db db-alt\n  HostName 192.0.2.11\n\n" +
		pathswitch.Header + "\nHost web.lan\n  HostName 192.0.2.10\n# hand-written\nHost web.prod\n  HostName 203.0.113.44\n  IdentityFile ~/.ssh/k\n"
	tests := []struct {
		name     string
		old, new string
		wantErr  string
	}{
		{name: "unchanged", old: "web", new: "web", wantErr: `alias "web" unchanged`},
		{name: "empty", old: "web", new: "", wantErr: `alias "": want a single plain word`},
		{name: "whitespace", old: "web", new: "my host", wantErr: "want a single plain word"},
		{name: "quote", old: "web", new: `we"b`, wantErr: "want a single plain word"},
		{name: "wildcard star", old: "web", new: "web*", wantErr: "want a single plain word"},
		{name: "wildcard question", old: "web", new: "we?", wantErr: "want a single plain word"},
		{name: "negation", old: "web", new: "!web", wantErr: "want a single plain word"},
		{name: "leading dash", old: "web", new: "-v", wantErr: "want a single plain word"},
		{name: "leading hash", old: "web", new: "#web", wantErr: "want a single plain word"},
		{name: "not managed", old: "nope", new: "srv", wantErr: `host "nope" not managed by sasash`},
		{name: "second pattern is not a managed name", old: "db-alt", new: "srv", wantErr: `host "db-alt" not managed by sasash`},
		{name: "collides with a main block", old: "web", new: "db", wantErr: `alias "db" already exists`},
		{name: "collides with a further pattern", old: "web", new: "db-alt", wantErr: `alias "db-alt" already exists`},
		{name: "collides with an owned variant", old: "db", new: "web.lan", wantErr: `alias "web.lan" already exists`},
		{name: "collides with a hand-written dotted block", old: "db", new: "web.prod", wantErr: `alias "web.prod" already exists`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := write(t, in)
			_, err := pathswitch.Rename(path, tt.old, tt.new)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Rename() = %v, want error containing %q", err, tt.wantErr)
			}
			if got := read(t, path); got != in {
				t.Errorf("file after a rejected Rename = %q, want untouched", got)
			}
		})
	}
}

func TestRename_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := pathswitch.Rename(filepath.Join(t.TempDir(), "sasash.conf"), "web", "srv")
	if err == nil || !os.IsNotExist(err) {
		t.Errorf("Rename() on a missing file = %v, want a not-exist error", err)
	}
}

func TestCacheRenames_Table(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("state", "sasash")
	tests := []struct {
		name     string
		old, new string
		want     []pathswitch.CacheRename
	}{
		{
			name: "tunnel record then stats, in that order",
			old:  "web", new: "srv",
			want: []pathswitch.CacheRename{
				{From: filepath.Join(dir, "web.json"), To: filepath.Join(dir, "srv.json")},
				{From: filepath.Join(dir, "web.stats.json"), To: filepath.Join(dir, "srv.stats.json")},
			},
		},
		{name: "empty old", old: "", new: "srv", want: nil},
		{name: "dot new", old: "web", new: ".", want: nil},
		{name: "dot-dot old", old: "..", new: "srv", want: nil},
		{name: "path separator never walks out of the dir", old: "web", new: "../srv", want: nil},
		{name: "nested new", old: "web", new: "a/b", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pathswitch.CacheRenames(dir, tt.old, tt.new)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CacheRenames() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
