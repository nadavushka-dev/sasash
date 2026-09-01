package sshconf_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// corpus returns every config file in testdata, keyed by base name.
func corpus(t *testing.T) map[string][]byte {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("testdata", "*.conf"))
	if err != nil {
		t.Fatalf("Glob() err = %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no testdata/*.conf files found")
	}

	out := make(map[string][]byte, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ReadFile(%q) err = %v", p, err)
		}
		out[filepath.Base(p)] = data
	}
	return out
}

// The central invariant: parsing and rendering an untouched file must return
// it byte-for-byte. This code writes to the user's real ssh config, so
// anything less is data loss.
func TestParse_RoundTripIsByteExact(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := sshconf.Parse(name, data).String()
			if got != string(data) {
				t.Errorf("round trip changed the file\n--- got ---\n%q\n--- want ---\n%q", got, data)
			}
		})
	}
}

func TestParse_RoundTripEdgeCases(t *testing.T) {
	t.Parallel()

	inputs := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"single newline", "\n"},
		{"no trailing newline", "Host a\n  User b"},
		{"crlf throughout", "Host a\r\n  User b\r\n"},
		{"mixed line endings", "Host a\r\n  User b\n  Port 22\r\n"},
		{"trailing whitespace", "Host a   \n  User b\t\n"},
		{"blank lines everywhere", "\n\n\nHost a\n\n\n  User b\n\n\n"},
		{"only whitespace", "   \n\t\n"},
		{"unknown directive", "Host a\n  SomeFutureOption yes\n"},
		{"utf8 comment", "# ─── managed ───\nHost a\n"},
		{"lone carriage return", "Host a\rHost b\n"},
		{"no newline at all", "Host a"},
	}

	for _, tt := range inputs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sshconf.Parse("", []byte(tt.in)).String(); got != tt.in {
				t.Errorf("round trip = %q, want %q", got, tt.in)
			}
		})
	}
}

// FuzzRoundTrip asserts the invariant over arbitrary input. Seeded from the
// real corpus so the fuzzer starts from realistic shapes.
func FuzzRoundTrip(f *testing.F) {
	paths, _ := filepath.Glob(filepath.Join("testdata", "*.conf"))
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(string(data))
		}
	}
	f.Add("Host a\n  HostName=1.2.3.4\n")
	f.Add("#\n##\n# @sasash\n")
	f.Add("\r\n\r\n")

	f.Fuzz(func(t *testing.T, in string) {
		file := sshconf.Parse("", []byte(in))
		if got := file.String(); got != in {
			t.Fatalf("round trip = %q, want %q", got, in)
		}
		// The derived view must never panic, whatever the input.
		for _, h := range file.Hosts() {
			_ = h.Name()
			_, _ = h.Get("hostname")
			_ = h.Meta()
		}
	})
}

func TestParse_LineKinds(t *testing.T) {
	t.Parallel()

	in := strings.Join([]string{
		"",
		"   ",
		"# a plain comment",
		"# @sasash path lan 192.168.3.141",
		"#@sasash tags dev",
		"#@sasashfoo not metadata",
		"Host ubuntu",
		"  HostName 192.168.3.141",
		"\tHostName=10.0.0.1",
		"  User = nadav",
	}, "\n")

	want := []struct {
		kind    sshconf.Kind
		keyword string
		args    string
		indent  string
	}{
		{kind: sshconf.KindBlank},
		{kind: sshconf.KindBlank, indent: "   "},
		{kind: sshconf.KindComment},
		{kind: sshconf.KindMeta, args: "path lan 192.168.3.141"},
		{kind: sshconf.KindMeta, args: "tags dev"},
		{kind: sshconf.KindComment},
		{kind: sshconf.KindDirective, keyword: "Host", args: "ubuntu"},
		{kind: sshconf.KindDirective, keyword: "HostName", args: "192.168.3.141", indent: "  "},
		{kind: sshconf.KindDirective, keyword: "HostName", args: "10.0.0.1", indent: "\t"},
		{kind: sshconf.KindDirective, keyword: "User", args: "nadav", indent: "  "},
	}

	got := sshconf.Parse("", []byte(in)).Lines()
	if len(got) != len(want) {
		t.Fatalf("parsed %d lines, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Kind != w.kind {
			t.Errorf("line %d kind = %v, want %v (%q)", i, got[i].Kind, w.kind, got[i].Raw)
		}
		if got[i].Keyword != w.keyword {
			t.Errorf("line %d keyword = %q, want %q", i, got[i].Keyword, w.keyword)
		}
		if got[i].Args != w.args {
			t.Errorf("line %d args = %q, want %q", i, got[i].Args, w.args)
		}
		if got[i].Indent != w.indent {
			t.Errorf("line %d indent = %q, want %q", i, got[i].Indent, w.indent)
		}
	}
}

func TestFile_Hosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		file string
		want []string
	}{
		{"simple.conf", "simple.conf", []string{"ubuntu", "spp"}},
		{"managed.conf", "managed.conf", []string{"ubuntu", "ubuntu.lan", "ubuntu.ts"}},
		{"wildcard is a host too", "wildcard.conf", []string{"*", "ubuntu"}},
		{"match does not create a host", "match.conf", []string{"ubuntu", "after-match"}},
		{"no hosts", "comments-only.conf", nil},
		{"empty file", "empty.conf", nil},
	}

	all := corpus(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, h := range sshconf.Parse(tt.file, all[tt.file]).Hosts() {
				got = append(got, h.Name())
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Hosts() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFile_Hosts_MultiplePatterns(t *testing.T) {
	t.Parallel()

	hosts := sshconf.Parse("", []byte("Host ubuntu ubuntu.lan u\n  User nadav\n")).Hosts()
	if len(hosts) != 1 {
		t.Fatalf("Hosts() = %d blocks, want 1", len(hosts))
	}
	want := []string{"ubuntu", "ubuntu.lan", "u"}
	if !reflect.DeepEqual(hosts[0].Patterns, want) {
		t.Errorf("Patterns = %v, want %v", hosts[0].Patterns, want)
	}
	if hosts[0].Name() != "ubuntu" {
		t.Errorf("Name() = %q, want ubuntu", hosts[0].Name())
	}
}

// A Match block must terminate the preceding Host block, or Get would read a
// value that ssh would only apply conditionally.
func TestHost_Get_StopsAtMatch(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("match.conf", corpus(t)["match.conf"])
	h, ok := f.Host("ubuntu")
	if !ok {
		t.Fatal("Host(ubuntu) not found")
	}
	got, _ := h.Get("HostName")
	if got != "192.168.3.141" {
		t.Errorf("HostName = %q, want 192.168.3.141 (must not read through the Match block)", got)
	}
}

func TestHost_Get(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("managed.conf", corpus(t)["managed.conf"])
	h, ok := f.Host("ubuntu")
	if !ok {
		t.Fatal("Host(ubuntu) not found")
	}

	tests := []struct {
		keyword string
		want    string
		wantOK  bool
	}{
		{"HostName", "192.168.3.141", true},
		{"hostname", "192.168.3.141", true}, // keywords are case-insensitive
		{"HOSTNAME", "192.168.3.141", true},
		{"ControlPath", "~/.ssh/cm/%C", true},
		{"User", "nadav", true},
		{"Port", "", false},
	}
	for _, tt := range tests {
		got, ok := h.Get(tt.keyword)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("Get(%q) = %q/%v, want %q/%v", tt.keyword, got, ok, tt.want, tt.wantOK)
		}
	}
}

// ssh resolves the first value it sees; Get must agree.
func TestFile_Host_DuplicateAliasFirstWins(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("duplicate.conf", corpus(t)["duplicate.conf"])
	h, ok := f.Host("dup")
	if !ok {
		t.Fatal("Host(dup) not found")
	}
	if got, _ := h.Get("HostName"); got != "first" {
		t.Errorf("HostName = %q, want first", got)
	}
}

func TestHost_Set(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		keyword string
		value   string
		want    string
	}{
		{
			name:    "rewrites in place, keeping indent and casing",
			in:      "Host a\n  HostName 192.168.3.141\n  User nadav\n",
			keyword: "HostName", value: "100.105.178.123",
			want: "Host a\n  HostName 100.105.178.123\n  User nadav\n",
		},
		{
			name:    "matches keyword case-insensitively but keeps the file's casing",
			in:      "Host a\n  hostname 192.168.3.141\n",
			keyword: "HostName", value: "10.0.0.1",
			want: "Host a\n  hostname 10.0.0.1\n",
		},
		{
			name:    "preserves tab indent",
			in:      "Host a\n\tHostName 192.168.3.141\n",
			keyword: "HostName", value: "10.0.0.1",
			want: "Host a\n\tHostName 10.0.0.1\n",
		},
		{
			name:    "normalises an equals separator only on the line it touches",
			in:      "Host a\n  HostName=192.168.3.141\n  User=nadav\n",
			keyword: "HostName", value: "10.0.0.1",
			want: "Host a\n  HostName 10.0.0.1\n  User=nadav\n",
		},
		{
			name:    "inserts after the Host line when absent",
			in:      "Host a\n  User nadav\n",
			keyword: "HostName", value: "10.0.0.1",
			want: "Host a\n  HostName 10.0.0.1\n  User nadav\n",
		},
		{
			name:    "inserted line matches the block's indent",
			in:      "Host a\n\t\tUser nadav\n",
			keyword: "HostName", value: "10.0.0.1",
			want: "Host a\n\t\tHostName 10.0.0.1\n\t\tUser nadav\n",
		},
		{
			name:    "defaults to two-space indent in an empty block",
			in:      "Host a\n",
			keyword: "HostName", value: "10.0.0.1",
			want: "Host a\n  HostName 10.0.0.1\n",
		},
		{
			name:    "keeps CRLF terminators",
			in:      "Host a\r\n  HostName 1.1.1.1\r\n",
			keyword: "HostName", value: "2.2.2.2",
			want: "Host a\r\n  HostName 2.2.2.2\r\n",
		},
		{
			name:    "inserts with CRLF in a CRLF file",
			in:      "Host a\r\n  User nadav\r\n",
			keyword: "HostName", value: "2.2.2.2",
			want: "Host a\r\n  HostName 2.2.2.2\r\n  User nadav\r\n",
		},
		{
			name:    "keeps a missing final newline missing",
			in:      "Host a\n  HostName 1.1.1.1",
			keyword: "HostName", value: "2.2.2.2",
			want: "Host a\n  HostName 2.2.2.2",
		},
		{
			name:    "only the first occurrence is rewritten",
			in:      "Host a\n  HostName 1.1.1.1\n  HostName 3.3.3.3\n",
			keyword: "HostName", value: "2.2.2.2",
			want: "Host a\n  HostName 2.2.2.2\n  HostName 3.3.3.3\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := sshconf.Parse("", []byte(tt.in))
			h, ok := f.Host("a")
			if !ok {
				t.Fatal("Host(a) not found")
			}
			h.Set(tt.keyword, tt.value)

			if got := f.String(); got != tt.want {
				t.Errorf("Set(%q, %q)\n got: %q\nwant: %q", tt.keyword, tt.value, got, tt.want)
			}
		})
	}
}

// An edit must not disturb neighbouring blocks, comments, or metadata.
func TestHost_Set_LeavesTheRestOfTheFileAlone(t *testing.T) {
	t.Parallel()

	original := string(corpus(t)["managed.conf"])
	f := sshconf.Parse("managed.conf", []byte(original))

	h, ok := f.Host("ubuntu")
	if !ok {
		t.Fatal("Host(ubuntu) not found")
	}
	h.Set("HostName", "100.105.178.123")

	got := f.String()
	wantLine := "  HostName 100.105.178.123\n"
	if !strings.Contains(got, wantLine) {
		t.Fatalf("edited file missing %q", wantLine)
	}

	// Exactly one line differs from the original.
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(original, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("line count changed: %d -> %d", len(wantLines), len(gotLines))
	}
	diffs := 0
	for i := range gotLines {
		if gotLines[i] != wantLines[i] {
			diffs++
		}
	}
	if diffs != 1 {
		t.Errorf("%d lines changed, want exactly 1", diffs)
	}
	// The pinned variant for the other path must not be touched.
	if !strings.Contains(got, "Host ubuntu.lan\n  HostName 192.168.3.141\n") {
		t.Error("Set() modified the pinned ubuntu.lan block")
	}
}

func TestHost_Set_ThenGetRoundTrips(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("", []byte("Host a\n  User nadav\n"))
	h, _ := f.Host("a")
	h.Set("HostName", "10.0.0.1")

	// Re-parse: the edit must be visible to a fresh reader, not just in memory.
	reparsed := sshconf.Parse("", f.Bytes())
	h2, ok := reparsed.Host("a")
	if !ok {
		t.Fatal("Host(a) not found after re-parse")
	}
	if got, _ := h2.Get("HostName"); got != "10.0.0.1" {
		t.Errorf("HostName = %q, want 10.0.0.1", got)
	}
}

func TestHost_Unset(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("", []byte("Host a\n  HostName 1.1.1.1\n  User nadav\n  HostName 2.2.2.2\n"))
	h, _ := f.Host("a")

	if got := h.Unset("hostname"); got != 2 {
		t.Errorf("Unset() removed %d lines, want 2", got)
	}
	if want := "Host a\n  User nadav\n"; f.String() != want {
		t.Errorf("after Unset = %q, want %q", f.String(), want)
	}
	if got := h.Unset("Port"); got != 0 {
		t.Errorf("Unset(absent) = %d, want 0", got)
	}
}

func TestHost_Meta(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("managed.conf", corpus(t)["managed.conf"])
	h, ok := f.Host("ubuntu")
	if !ok {
		t.Fatal("Host(ubuntu) not found")
	}

	got := h.Meta()
	want := []struct {
		verb string
		args []string
	}{
		{"path", []string{"lan", "192.168.3.141"}},
		{"path", []string{"ts", "100.105.178.123"}},
		{"tags", []string{"dev,tlv"}},
		{"forwards", []string{"web", "3000:localhost:3000", "3001:localhost:3001"}},
	}
	if len(got) != len(want) {
		t.Fatalf("Meta() = %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Verb != w.verb || !reflect.DeepEqual(got[i].Args, w.args) {
			t.Errorf("Meta()[%d] = %q %v, want %q %v", i, got[i].Verb, got[i].Args, w.verb, w.args)
		}
	}
}

// Metadata belongs to the host it sits above, and must not leak to the next one.
func TestHost_Meta_DoesNotLeakAcrossBlocks(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("managed.conf", corpus(t)["managed.conf"])
	h, ok := f.Host("ubuntu.lan")
	if !ok {
		t.Fatal("Host(ubuntu.lan) not found")
	}
	if got := h.Meta(); len(got) != 0 {
		t.Errorf("Meta() = %+v, want none (the metadata above belongs to ubuntu)", got)
	}
}

func TestHost_Meta_Attachment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "directly above the Host line",
			in:   "# @sasash path lan 1.1.1.1\nHost a\n",
			want: []string{"path"},
		},
		{
			name: "plain comments interleaved do not break attachment",
			in:   "# @sasash path lan 1.1.1.1\n# a note\n# @sasash tags dev\nHost a\n",
			want: []string{"path", "tags"},
		},
		{
			name: "a blank line breaks attachment",
			in:   "# @sasash path lan 1.1.1.1\n\nHost a\n",
			want: nil,
		},
		{
			name: "a preceding directive breaks attachment",
			in:   "Host other\n# @sasash path lan 1.1.1.1\nHost a\n",
			want: []string{"path"},
		},
		{
			name: "metadata inside the block also counts",
			in:   "Host a\n  User nadav\n  # @sasash tags dev\n",
			want: []string{"tags"},
		},
		{
			name: "bare marker with no verb is ignored",
			in:   "# @sasash\nHost a\n",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := sshconf.Parse("", []byte(tt.in))
			h, ok := f.Host("a")
			if !ok {
				t.Fatal("Host(a) not found")
			}
			var got []string
			for _, m := range h.Meta() {
				got = append(got, m.Verb)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Meta() verbs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHost_MetaVerb(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("managed.conf", corpus(t)["managed.conf"])
	h, _ := f.Host("ubuntu")

	paths := h.MetaVerb("path")
	if len(paths) != 2 {
		t.Fatalf("MetaVerb(path) = %d, want 2", len(paths))
	}
	if paths[0].Args[0] != "lan" || paths[1].Args[0] != "ts" {
		t.Errorf("MetaVerb(path) = %v, want lan then ts", paths)
	}
	if got := h.MetaVerb("nope"); got != nil {
		t.Errorf("MetaVerb(nope) = %v, want nil", got)
	}
}

func TestParseFile_And_WriteAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sasash.conf")
	original := "Host a\n  HostName 1.1.1.1\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	f, err := sshconf.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() err = %v", err)
	}
	h, _ := f.Host("a")
	h.Set("HostName", "2.2.2.2")

	if err := f.WriteAtomic(path); err != nil {
		t.Fatalf("WriteAtomic() err = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if want := "Host a\n  HostName 2.2.2.2\n"; string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() err = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	// No temp files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() err = %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only sasash.conf", names)
	}
}

func TestWriteAtomic_CreatesMissingDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.d", "sasash.conf")
	f := sshconf.Parse(path, []byte("Host a\n"))

	if err := f.WriteAtomic(path); err != nil {
		t.Fatalf("WriteAtomic() err = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if string(got) != "Host a\n" {
		t.Errorf("file = %q, want %q", got, "Host a\n")
	}
}

func TestParseFile_Missing(t *testing.T) {
	t.Parallel()

	if _, err := sshconf.ParseFile(filepath.Join(t.TempDir(), "nope.conf")); err == nil {
		t.Fatal("ParseFile(missing) err = nil, want error")
	}
}

// Reading a file and writing it straight back must be a no-op on disk.
func TestParseFile_WriteBackIsIdentity(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("WriteFile() err = %v", err)
			}

			f, err := sshconf.ParseFile(path)
			if err != nil {
				t.Fatalf("ParseFile() err = %v", err)
			}
			if err := f.WriteAtomic(path); err != nil {
				t.Fatalf("WriteAtomic() err = %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() err = %v", err)
			}
			if string(got) != string(data) {
				t.Errorf("write-back changed the file\n got: %q\nwant: %q", got, data)
			}
		})
	}
}

func TestKind_String(t *testing.T) {
	t.Parallel()

	tests := map[sshconf.Kind]string{
		sshconf.KindBlank:     "blank",
		sshconf.KindComment:   "comment",
		sshconf.KindMeta:      "meta",
		sshconf.KindDirective: "directive",
		sshconf.Kind(99):      "Kind(99)",
	}
	for kind, want := range tests {
		if got := kind.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}
