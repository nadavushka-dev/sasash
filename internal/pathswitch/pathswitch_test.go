package pathswitch_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/probe"
)

// canonical is a sasash.conf whose variants are already correct for the
// active path, so a switch must change only the main HostName line.
const canonical = `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10
  User dev

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
  User dev
Host ubuntu.ts
  HostName 198.51.100.5
  User dev
`

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sasash.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) failed: %v", path, err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", path, err)
	}
	return string(data)
}

func TestCycle_Table(t *testing.T) {
	t.Parallel()
	lan := probe.Path{Name: "lan", Host: "192.0.2.10"}
	ts := probe.Path{Name: "ts", Host: "198.51.100.5"}
	tests := []struct {
		name string
		in   string
		want string
		res  pathswitch.Result
	}{
		{
			name: "advances to next path touching only hostname",
			in:   canonical,
			want: strings.Replace(canonical, "  HostName 192.0.2.10\n  User dev\n\n", "  HostName 198.51.100.5\n  User dev\n\n", 1),
			res:  pathswitch.Result{Alias: "ubuntu", From: lan, To: ts, Changed: true},
		},
		{
			name: "wraps from last path to first",
			in:   strings.Replace(canonical, "  HostName 192.0.2.10\n  User dev\n\n", "  HostName 198.51.100.5\n  User dev\n\n", 1),
			want: canonical,
			res:  pathswitch.Result{Alias: "ubuntu", From: ts, To: lan, Changed: true},
		},
		{
			name: "hand-edited hostname falls back to first path",
			in:   strings.Replace(canonical, "  HostName 192.0.2.10\n  User dev\n\n", "  HostName 203.0.113.9\n  User dev\n\n", 1),
			want: canonical,
			res:  pathswitch.Result{Alias: "ubuntu", To: lan, Changed: true},
		},
		{
			name: "creates variants with header when none exist",
			in: `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10
  User dev
`,
			want: strings.Replace(canonical, "  HostName 192.0.2.10\n  User dev\n\n", "  HostName 198.51.100.5\n  User dev\n\n", 1),
			res:  pathswitch.Result{Alias: "ubuntu", From: lan, To: ts, Changed: true},
		},
		{
			name: "removes stale variant",
			in: canonical + `Host ubuntu.old
  HostName 203.0.113.7
  User dev
`,
			want: strings.Replace(canonical, "  HostName 192.0.2.10\n  User dev\n\n", "  HostName 198.51.100.5\n  User dev\n\n", 1),
			res:  pathswitch.Result{Alias: "ubuntu", From: lan, To: ts, Changed: true},
		},
		{
			name: "single named path is a no-op",
			in: `# @sasash path lan 192.0.2.10
Host ubuntu
  HostName 192.0.2.10
`,
			want: `# @sasash path lan 192.0.2.10
Host ubuntu
  HostName 192.0.2.10
`,
			res: pathswitch.Result{Alias: "ubuntu", From: lan, To: lan},
		},
		{
			name: "no named paths is a no-op",
			in: `Host ubuntu
  HostName 192.0.2.10
`,
			want: `Host ubuntu
  HostName 192.0.2.10
`,
			res: pathswitch.Result{Alias: "ubuntu"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := write(t, tt.in)
			got, err := pathswitch.Cycle(path, "ubuntu")
			if err != nil {
				t.Fatalf("Cycle() failed: %v", err)
			}
			if !reflect.DeepEqual(got, tt.res) {
				t.Errorf("Cycle() = %+v, want %+v", got, tt.res)
			}
			if gotFile := read(t, path); gotFile != tt.want {
				t.Errorf("Cycle() file =\n%q\nwant\n%q", gotFile, tt.want)
			}
		})
	}
}

// A host with no HostName at all: the line is inserted directly after the
// Host line, above User, and the first named path becomes active.
func TestCycle_InsertsHostNameAboveUser(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  User dev
`
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10
  User dev

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
  User dev
Host ubuntu.ts
  HostName 198.51.100.5
  User dev
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

func TestCycle_DoubleCycleRoundTrips(t *testing.T) {
	t.Parallel()
	path := write(t, canonical)
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("first Cycle() failed: %v", err)
	}
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("second Cycle() failed: %v", err)
	}
	if got := read(t, path); got != canonical {
		t.Errorf("two Cycle() calls =\n%q\nwant original\n%q", got, canonical)
	}
}

func TestCycle_RereadsHandEdits(t *testing.T) {
	t.Parallel()
	path := write(t, canonical)
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	// The user hand-edits the active addr back to lan between calls; the
	// next cycle must start from the file's state, not sasash's memory.
	edited := strings.Replace(read(t, path), "  HostName 198.51.100.5\n  User dev\n\n", "  HostName 192.0.2.10\n  User dev\n\n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatalf("WriteFile(hand edit) failed: %v", err)
	}
	got, err := pathswitch.Cycle(path, "ubuntu")
	if err != nil {
		t.Fatalf("Cycle() after hand edit failed: %v", err)
	}
	if got.From.Name != "lan" || got.To.Name != "ts" {
		t.Errorf("Cycle() after hand edit = %s->%s, want lan->ts", got.From.Name, got.To.Name)
	}
}

func TestCycle_PortLines(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5:2222
Host web
  HostName 192.0.2.10
  Port 2200
`
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "web"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5:2222
Host web
  HostName 198.51.100.5
  Port 2200

# pinned variants, generated — for when you want to force one path
Host web.lan
  HostName 192.0.2.10
  Port 2200
Host web.ts
  HostName 198.51.100.5
  Port 2222
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

func TestCycle_OtherHostsUntouched(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10

# @sasash path lan 192.0.2.20
Host other
  HostName 192.0.2.20
`
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
Host ubuntu.ts
  HostName 198.51.100.5
# @sasash path lan 192.0.2.20
Host other
  HostName 192.0.2.20
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

// A real host that merely shares the "<alias>." prefix is not a variant: it
// must survive a cycle byte-for-byte, comment and all.
func TestCycle_ForeignPrefixHostSurvives(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 192.0.2.10

# a completely unrelated production host
Host web.staging
  HostName 203.0.113.44
  User deploy
  IdentityFile ~/.ssh/deploy_ed25519
  ProxyJump bastion
`
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "web"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host web.lan
  HostName 192.0.2.10
Host web.ts
  HostName 198.51.100.5
# a completely unrelated production host
Host web.staging
  HostName 203.0.113.44
  User deploy
  IdentityFile ~/.ssh/deploy_ed25519
  ProxyJump bastion
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

// A minimal hand-written host (HostName+User only) is byte-shaped like a
// generated variant; only header anchoring tells them apart. It must survive
// the first cycle (no generated region exists yet) AND the second (a header
// now exists above the generated group, but the chain must not reach it).
func TestCycle_MinimalForeignPrefixHostSurvives(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 192.0.2.10

# the staging box, unrelated to sasash
Host web.staging
  HostName 203.0.113.44
  User deploy
`
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "web"); err != nil {
		t.Fatalf("first Cycle() failed: %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host web.lan
  HostName 192.0.2.10
Host web.ts
  HostName 198.51.100.5
# the staging box, unrelated to sasash
Host web.staging
  HostName 203.0.113.44
  User deploy
`
	if got := read(t, path); got != want {
		t.Errorf("first Cycle() file =\n%q\nwant\n%q", got, want)
	}
	if _, err := pathswitch.Cycle(path, "web"); err != nil {
		t.Fatalf("second Cycle() failed: %v", err)
	}
	wantSecond := strings.Replace(want, "Host web\n  HostName 198.51.100.5\n", "Host web\n  HostName 192.0.2.10\n", 1)
	if got := read(t, path); got != wantSecond {
		t.Errorf("second Cycle() file =\n%q\nwant\n%q", got, wantSecond)
	}
}

// A variant-named Host line carrying extra patterns is not generator-shaped;
// it must survive untouched with all its aliases.
func TestCycle_MultiPatternVariantNotOwned(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 192.0.2.10

Host web.lan web-lan
  HostName 203.0.113.44
`
	path := write(t, in)
	res, err := pathswitch.Cycle(path, "web")
	if err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	if !reflect.DeepEqual(res.Unmanaged, []string{"lan"}) {
		t.Errorf("Cycle() Unmanaged = %v, want [lan]", res.Unmanaged)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host web.ts
  HostName 198.51.100.5
Host web.lan web-lan
  HostName 203.0.113.44
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

// A newly generated variant lands inside the generated group, not below a
// user note that trails it.
func TestCycle_AppendStaysInsideGroup(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10

# unrelated note the user keeps at the bottom of the file
`
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
Host ubuntu.ts
  HostName 198.51.100.5

# unrelated note the user keeps at the bottom of the file
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

// A user block that shadows a live path name is authoritative: it is left
// verbatim and no duplicate variant is generated for that name.
func TestCycle_ForeignBlockShadowingPathName(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 192.0.2.10

Host web.lan
  HostName 203.0.113.44
  IdentityFile ~/.ssh/special_ed25519
`
	path := write(t, in)
	res, err := pathswitch.Cycle(path, "web")
	if err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	if !reflect.DeepEqual(res.Unmanaged, []string{"lan"}) {
		t.Errorf("Cycle() Unmanaged = %v, want [lan]", res.Unmanaged)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host web
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host web.ts
  HostName 198.51.100.5
Host web.lan
  HostName 203.0.113.44
  IdentityFile ~/.ssh/special_ed25519
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

// Header is a persisted on-disk ownership token: anchoring depends on the
// exact bytes, so it must stay identical to SPEC.md's literal forever (a
// change would silently unmanage every group already written).
func TestHeader_PinnedToSpec(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "SPEC.md"))
	if err != nil {
		t.Fatalf("ReadFile(SPEC.md) failed: %v", err)
	}
	if !strings.Contains(string(data), pathswitch.Header+"\n") {
		t.Errorf("Header %q not found in SPEC.md; it cannot change without a migration", pathswitch.Header)
	}
}

// In-block "# @sasash" meta stays inside its host's block; generated variants
// land after it, and the host's paths survive so a second cycle still works.
func TestCycle_InBlockMetaStaysWithHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "mid file",
			in: `Host ubuntu
  HostName 192.0.2.10
  # @sasash path lan 192.0.2.10
  # @sasash path ts 198.51.100.5

Host other
  HostName 192.0.2.20
`,
			want: `Host ubuntu
  HostName 198.51.100.5
  # @sasash path lan 192.0.2.10
  # @sasash path ts 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
Host ubuntu.ts
  HostName 198.51.100.5
Host other
  HostName 192.0.2.20
`,
		},
		{
			name: "last block in file",
			in: `Host ubuntu
  HostName 192.0.2.10
  # @sasash path lan 192.0.2.10
  # @sasash path ts 198.51.100.5
`,
			want: `Host ubuntu
  HostName 198.51.100.5
  # @sasash path lan 192.0.2.10
  # @sasash path ts 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
Host ubuntu.ts
  HostName 198.51.100.5
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := write(t, tt.in)
			if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
				t.Fatalf("first Cycle() failed: %v", err)
			}
			if got := read(t, path); got != tt.want {
				t.Errorf("Cycle() file =\n%q\nwant\n%q", got, tt.want)
			}
			second, err := pathswitch.Cycle(path, "ubuntu")
			if err != nil {
				t.Fatalf("second Cycle() failed: %v", err)
			}
			if !second.Changed || second.To.Name != "lan" {
				t.Errorf("second Cycle() = %+v, want Changed back to lan (paths must survive)", second)
			}
		})
	}
}

// Two named paths sharing an addr: cycling matches the LAST of the run so it
// advances past the group instead of stalling between the twins.
func TestCycle_SharedAddrAdvances(t *testing.T) {
	t.Parallel()
	in := `# @sasash path direct 192.0.2.10:22
# @sasash path jump 192.0.2.10:2222
# @sasash path ts 198.51.100.5
Host u
  HostName 192.0.2.10
`
	path := write(t, in)
	got, err := pathswitch.Cycle(path, "u")
	if err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	if got.From.Name != "jump" || got.To.Name != "ts" {
		t.Errorf("Cycle() = %s->%s, want jump->ts", got.From.Name, got.To.Name)
	}
}

func TestCycle_NoopDoesNotWrite(t *testing.T) {
	t.Parallel()
	path := write(t, "# @sasash path lan 192.0.2.10\nHost ubuntu\n  HostName 192.0.2.10\n")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() failed: %v", err)
	}
	got, err := pathswitch.Cycle(path, "ubuntu")
	if err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	if got.Changed {
		t.Errorf("Cycle() = %+v, want Changed false", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() failed: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("no-op Cycle() rewrote the file (mtime changed)")
	}
}

func TestCycle_PreservesCommentsBetweenVariants(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
# my note about ts
Host ubuntu.ts
  HostName 198.51.100.5
`
	path := write(t, in)
	res, err := pathswitch.Cycle(path, "ubuntu")
	if err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	got := read(t, path)
	if !strings.Contains(got, "# my note about ts\nHost ubuntu.ts\n") {
		t.Errorf("Cycle() dropped the user comment between variants:\n%q", got)
	}
	// The comment breaks the anchored chain: the ts block below it is left
	// as-is and reported, so the TUI can tell the user the pin is theirs now.
	if !reflect.DeepEqual(res.Unmanaged, []string{"ts"}) {
		t.Errorf("Cycle() Unmanaged = %v, want [ts]", res.Unmanaged)
	}
}

func TestCycle_CRLF(t *testing.T) {
	t.Parallel()
	in := strings.ReplaceAll(`# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10
`, "\n", "\r\n")
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	want := strings.ReplaceAll(`# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
Host ubuntu.ts
  HostName 198.51.100.5
`, "\n", "\r\n")
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

func TestCycle_UnterminatedLastLine(t *testing.T) {
	t.Parallel()
	in := "# @sasash path lan 192.0.2.10\n# @sasash path ts 198.51.100.5\nHost ubuntu\n  HostName 192.0.2.10"
	path := write(t, in)
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	want := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 198.51.100.5

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
Host ubuntu.ts
  HostName 198.51.100.5
`
	if got := read(t, path); got != want {
		t.Errorf("Cycle() file =\n%q\nwant\n%q", got, want)
	}
}

func TestCycle_PreservesPerm(t *testing.T) {
	t.Parallel()
	path := write(t, canonical)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() failed: %v", err)
	}
	if _, err := pathswitch.Cycle(path, "ubuntu"); err != nil {
		t.Fatalf("Cycle() failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("perm = %o, want %o", got, 0o644)
	}
}

func TestCycle_Errors(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		if _, err := pathswitch.Cycle(filepath.Join(t.TempDir(), "nope.conf"), "ubuntu"); err == nil {
			t.Error("Cycle() err = nil, want error")
		}
	})

	t.Run("alias not in file", func(t *testing.T) {
		t.Parallel()
		path := write(t, canonical)
		_, err := pathswitch.Cycle(path, "bogus")
		if err == nil {
			t.Fatal("Cycle() err = nil, want error")
		}
		if !strings.Contains(err.Error(), "not managed by sasash") {
			t.Errorf("Cycle() err = %q, want mention of not managed", err)
		}
	})
}

func TestSet_AlreadyActiveRegeneratesVariants(t *testing.T) {
	t.Parallel()
	in := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.5
Host ubuntu
  HostName 192.0.2.10
  User dev

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.0.2.10
  User dev
`
	path := write(t, in)
	got, err := pathswitch.Set(path, "ubuntu", "lan")
	if err != nil {
		t.Fatalf("Set() failed: %v", err)
	}
	if !got.Changed || got.To.Name != "lan" {
		t.Errorf("Set() = %+v, want Changed to lan", got)
	}
	if gotFile := read(t, path); gotFile != canonical {
		t.Errorf("Set() file =\n%q\nwant\n%q", gotFile, canonical)
	}
}

func TestSet_Errors(t *testing.T) {
	t.Parallel()

	t.Run("unknown path name lists known ones", func(t *testing.T) {
		t.Parallel()
		path := write(t, canonical)
		_, err := pathswitch.Set(path, "ubuntu", "wifi")
		if err == nil {
			t.Fatal("Set() err = nil, want error")
		}
		if !strings.Contains(err.Error(), "lan, ts") {
			t.Errorf("Set() err = %q, want the known path names", err)
		}
	})

	t.Run("unknown path name errors even on a single-path host", func(t *testing.T) {
		t.Parallel()
		path := write(t, "# @sasash path lan 192.0.2.10\nHost ubuntu\n  HostName 192.0.2.10\n")
		_, err := pathswitch.Set(path, "ubuntu", "bogus")
		if err == nil {
			t.Fatal("Set() err = nil, want error")
		}
		if !strings.Contains(err.Error(), "lan") {
			t.Errorf("Set() err = %q, want the known path name", err)
		}
	})

	t.Run("empty path name", func(t *testing.T) {
		t.Parallel()
		path := write(t, canonical)
		if _, err := pathswitch.Set(path, "ubuntu", ""); err == nil {
			t.Error("Set() err = nil, want error")
		}
	})
}
