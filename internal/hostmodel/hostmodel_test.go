package hostmodel_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// block parses config text and returns the named Host block.
func block(t *testing.T, config, alias string) *sshconf.Host {
	t.Helper()
	h, ok := sshconf.Parse("", []byte(config)).Host(alias)
	if !ok {
		t.Fatalf("Host(%q) not found in config", alias)
	}
	return h
}

func TestBuild_PathsTagsForwards(t *testing.T) {
	t.Parallel()

	config := `# @sasash path lan 192.0.2.10
# @sasash path ts 198.51.100.20:2222
# @sasash tags dev,tlv
# @sasash forwards web 3000:localhost:3000 3001:localhost:3001
Host ubuntu
  HostName 192.0.2.10
  User alice
`
	got, errs := hostmodel.Build(block(t, config, "ubuntu"))
	if len(errs) != 0 {
		t.Fatalf("Build() errs = %v, want none", errs)
	}

	want := hostmodel.Host{
		Alias: "ubuntu",
		Paths: []probe.Path{
			{Name: "lan", Host: "192.0.2.10"},
			{Name: "ts", Host: "198.51.100.20", Port: "2222"},
		},
		Tags: []string{"dev", "tlv"},
		Forwards: []hostmodel.ForwardSet{
			{Name: "web", Specs: []string{"3000:localhost:3000", "3001:localhost:3001"}},
		},
		HasMeta: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() = %+v, want %+v", got, want)
	}
}

// Regression: a meta block introducing the NEXT host sits inside the previous
// block's line range; it must attach to its own host only. Getting this wrong
// hands a host the following host's address — sasash would connect the user
// to the wrong machine.
func TestBuild_DoesNotClaimNextHostsMeta(t *testing.T) {
	t.Parallel()

	config := `# @sasash path lan 192.0.2.10
Host ubuntu
  HostName 192.0.2.10

# @sasash path vpn 198.51.100.20
# @sasash forwards web 8080:localhost:80
# @sasash tags storage
Host nas
  HostName 198.51.100.20
`
	ubuntu, errs := hostmodel.Build(block(t, config, "ubuntu"))
	if len(errs) != 0 {
		t.Fatalf("Build(ubuntu) errs = %v, want none", errs)
	}
	wantUbuntu := hostmodel.Host{
		Alias:   "ubuntu",
		Paths:   []probe.Path{{Name: "lan", Host: "192.0.2.10"}},
		HasMeta: true,
	}
	if !reflect.DeepEqual(ubuntu, wantUbuntu) {
		t.Errorf("Build(ubuntu) = %+v, want %+v", ubuntu, wantUbuntu)
	}

	nas, errs := hostmodel.Build(block(t, config, "nas"))
	if len(errs) != 0 {
		t.Fatalf("Build(nas) errs = %v, want none", errs)
	}
	wantNas := hostmodel.Host{
		Alias:    "nas",
		Paths:    []probe.Path{{Name: "vpn", Host: "198.51.100.20"}},
		Tags:     []string{"storage"},
		Forwards: []hostmodel.ForwardSet{{Name: "web", Specs: []string{"8080:localhost:80"}}},
		HasMeta:  true,
	}
	if !reflect.DeepEqual(nas, wantNas) {
		t.Errorf("Build(nas) = %+v, want %+v", nas, wantNas)
	}
}

func TestBuild_Fallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config string
		want   []probe.Path
	}{
		{
			"hostname directive",
			"Host a\n  HostName 192.0.2.1\n",
			[]probe.Path{{Host: "192.0.2.1"}},
		},
		{
			"no hostname falls back to alias",
			"Host a\n  User alice\n",
			[]probe.Path{{Host: "a"}},
		},
		{
			"port directive",
			"Host a\n  HostName 192.0.2.1\n  Port 2222\n",
			[]probe.Path{{Host: "192.0.2.1", Port: "2222"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, errs := hostmodel.Build(block(t, tt.config, "a"))
			if len(errs) != 0 {
				t.Fatalf("Build() errs = %v, want none", errs)
			}
			if !reflect.DeepEqual(got.Paths, tt.want) {
				t.Errorf("Build() Paths = %v, want %v", got.Paths, tt.want)
			}
		})
	}
}

func TestBuild_PortDirectiveFillsMetaPaths(t *testing.T) {
	t.Parallel()

	config := `# @sasash path lan 192.0.2.1
# @sasash path ts 198.51.100.2:2200
Host a
  Port 2222
`
	got, errs := hostmodel.Build(block(t, config, "a"))
	if len(errs) != 0 {
		t.Fatalf("Build() errs = %v, want none", errs)
	}
	want := []probe.Path{
		{Name: "lan", Host: "192.0.2.1", Port: "2222"},
		{Name: "ts", Host: "198.51.100.2", Port: "2200"},
	}
	if !reflect.DeepEqual(got.Paths, want) {
		t.Errorf("Build() Paths = %v, want %v", got.Paths, want)
	}
}

func TestBuild_AddrForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		want probe.Path
	}{
		{"bare ipv4", "192.0.2.1", probe.Path{Name: "p", Host: "192.0.2.1"}},
		{"ipv4 with port", "192.0.2.1:2222", probe.Path{Name: "p", Host: "192.0.2.1", Port: "2222"}},
		{"hostname", "gw.example.com", probe.Path{Name: "p", Host: "gw.example.com"}},
		{"bare ipv6", "2001:db8::1", probe.Path{Name: "p", Host: "2001:db8::1"}},
		{"bracketed ipv6", "[2001:db8::1]", probe.Path{Name: "p", Host: "2001:db8::1"}},
		{"bracketed ipv6 with port", "[2001:db8::1]:2222", probe.Path{Name: "p", Host: "2001:db8::1", Port: "2222"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := "# @sasash path p " + tt.addr + "\nHost a\n"
			got, errs := hostmodel.Build(block(t, config, "a"))
			if len(errs) != 0 {
				t.Fatalf("Build() errs = %v, want none", errs)
			}
			if !reflect.DeepEqual(got.Paths, []probe.Path{tt.want}) {
				t.Errorf("Build() Paths = %v, want %v", got.Paths, []probe.Path{tt.want})
			}
		})
	}
}

func TestBuild_MalformedMeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    string
		wantPaths []probe.Path
		wantErrs  []string // one substring per expected error
	}{
		{
			"path missing addr",
			"# @sasash path lan\nHost a\n  HostName 192.0.2.1\n",
			[]probe.Path{{Host: "192.0.2.1"}}, // skipped meta -> fallback
			[]string{"missing addr"},
		},
		{
			"duplicate path name first wins",
			"# @sasash path lan 192.0.2.1\n# @sasash path lan 198.51.100.2\nHost a\n",
			[]probe.Path{{Name: "lan", Host: "192.0.2.1"}},
			[]string{"duplicate name"},
		},
		{
			"bad port",
			"# @sasash path lan 192.0.2.1:ssh\nHost a\n  HostName 192.0.2.1\n",
			[]probe.Path{{Host: "192.0.2.1"}},
			[]string{"bad addr"},
		},
		{
			"forwards without specs",
			"# @sasash path lan 192.0.2.1\n# @sasash forwards web\nHost a\n",
			[]probe.Path{{Name: "lan", Host: "192.0.2.1"}},
			[]string{"missing specs"},
		},
		{
			"duplicate forwards name first wins",
			"# @sasash path lan 192.0.2.1\n# @sasash forwards web 3000:localhost:3000\n# @sasash forwards web 4000:localhost:4000\nHost a\n",
			[]probe.Path{{Name: "lan", Host: "192.0.2.1"}},
			[]string{"duplicate name"},
		},
		{
			"one bad meta does not hide the good ones",
			"# @sasash path lan 192.0.2.1\n# @sasash path ts\n# @sasash path pub 203.0.113.9\nHost a\n",
			[]probe.Path{{Name: "lan", Host: "192.0.2.1"}, {Name: "pub", Host: "203.0.113.9"}},
			[]string{"missing addr"},
		},
		{
			"unknown verb ignored silently",
			"# @sasash path lan 192.0.2.1\n# @sasash color purple\nHost a\n",
			[]probe.Path{{Name: "lan", Host: "192.0.2.1"}},
			nil,
		},
		{
			"empty host before colon",
			"# @sasash path lan :2222\nHost a\n  HostName 192.0.2.1\n",
			[]probe.Path{{Host: "192.0.2.1"}},
			[]string{"bad addr"},
		},
		{
			"colon with no port",
			"# @sasash path lan 192.0.2.1:\nHost a\n  HostName 192.0.2.1\n",
			[]probe.Path{{Host: "192.0.2.1"}},
			[]string{"bad addr"},
		},
		{
			"non-numeric Port directive dropped with warning",
			"# @sasash path lan 192.0.2.1\nHost a\n  Port ssh\n",
			[]probe.Path{{Name: "lan", Host: "192.0.2.1"}},
			[]string{"not a number"},
		},
		{
			"valid duplicate replaces an invalid first",
			"# @sasash path lan 192.0.2.1:bad\n# @sasash path lan 192.0.2.1\nHost a\n",
			[]probe.Path{{Name: "lan", Host: "192.0.2.1"}},
			[]string{"bad addr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, errs := hostmodel.Build(block(t, tt.config, "a"))
			if !reflect.DeepEqual(got.Paths, tt.wantPaths) {
				t.Errorf("Build() Paths = %v, want %v", got.Paths, tt.wantPaths)
			}
			if len(errs) != len(tt.wantErrs) {
				t.Fatalf("Build() errs = %v, want %d errors", errs, len(tt.wantErrs))
			}
			for i, want := range tt.wantErrs {
				if !strings.Contains(errs[i].Error(), want) {
					t.Errorf("Build() errs[%d] = %q, want substring %q", i, errs[i], want)
				}
			}
		})
	}
}

func TestBuild_Tags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config string
		want   []string
	}{
		{
			"comma separated",
			"# @sasash tags dev,tlv\nHost a\n",
			[]string{"dev", "tlv"},
		},
		{
			"comma with space",
			"# @sasash tags dev, tlv\nHost a\n",
			[]string{"dev", "tlv"},
		},
		{
			"space separated",
			"# @sasash tags dev tlv\nHost a\n",
			[]string{"dev", "tlv"},
		},
		{
			"several tags lines concatenate",
			"# @sasash tags dev\n# @sasash tags tlv\nHost a\n",
			[]string{"dev", "tlv"},
		},
		{
			"no tags",
			"Host a\n",
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, errs := hostmodel.Build(block(t, tt.config, "a"))
			if len(errs) != 0 {
				t.Fatalf("Build() errs = %v, want none", errs)
			}
			if !reflect.DeepEqual(got.Tags, tt.want) {
				t.Errorf("Build() Tags = %v, want %v", got.Tags, tt.want)
			}
		})
	}
}

func TestLoad_FollowsIncludesAndFilters(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	root := `Include managed.conf
Host dup
  HostName 203.0.113.99
Host b
  HostName 198.51.100.2
Host *
  User alice
`
	managed := `# @sasash path lan 192.0.2.1
# @sasash path lan 192.0.2.2
Host dup
  HostName 192.0.2.1
`
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(root), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "managed.conf"), []byte(managed), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	hosts, warnings, err := hostmodel.Load(filepath.Join(sshDir, "config"), home)
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}

	// "dup" resolves to the included block (first value wins), "Host *" is
	// filtered, and the duplicate path meta surfaces as one warning.
	wantAliases := []string{"dup", "b"}
	gotAliases := make([]string, len(hosts))
	for i, h := range hosts {
		gotAliases[i] = h.Alias
	}
	if !reflect.DeepEqual(gotAliases, wantAliases) {
		t.Fatalf("Load() aliases = %v, want %v", gotAliases, wantAliases)
	}
	wantPaths := []probe.Path{{Name: "lan", Host: "192.0.2.1"}}
	if !reflect.DeepEqual(hosts[0].Paths, wantPaths) {
		t.Errorf("Load() dup Paths = %v, want %v", hosts[0].Paths, wantPaths)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "duplicate name") {
		t.Errorf("Load() warnings = %v, want one duplicate-name warning", warnings)
	}
}

func TestLoad_MissingRootErrors(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	hosts, warnings, err := hostmodel.Load(filepath.Join(home, ".ssh", "config"), home)
	if err == nil {
		t.Error("Load() err = nil, want error for missing root")
	}
	if hosts != nil || warnings != nil {
		t.Errorf("Load() hosts = %v, warnings = %v, want nil on error", hosts, warnings)
	}
}

func TestBuild_FileMetaHide(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		config   string
		wantMeta bool
		wantHide bool
		wantErrs int
	}{
		{name: "plain block has no meta", config: "Host a\n  HostName 192.0.2.1\n"},
		{name: "any verb counts as meta", config: "# @sasash tags x\nHost a\n  HostName 192.0.2.1\n", wantMeta: true},
		{name: "unknown verb still counts as meta", config: "# @sasash whatever\nHost a\n  HostName 192.0.2.1\n", wantMeta: true},
		{name: "hide", config: "# @sasash hide\nHost a\n  HostName 192.0.2.1\n", wantMeta: true, wantHide: true},
		{name: "hide with an argument is malformed, not hidden", config: "# @sasash hide me\nHost a\n  HostName 192.0.2.1\n", wantMeta: true, wantErrs: 1},
		{name: "the next host's meta is not this one's", config: "Host a\n  HostName 192.0.2.1\n# @sasash hide\nHost b\n  HostName 192.0.2.2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, errs := hostmodel.Build(block(t, tt.config, "a"))
			if got.HasMeta != tt.wantMeta || got.Hidden != tt.wantHide || len(errs) != tt.wantErrs {
				t.Errorf("Build() HasMeta %v, Hidden %v, errs %v; want %v, %v, %d", got.HasMeta, got.Hidden, errs, tt.wantMeta, tt.wantHide, tt.wantErrs)
			}
			if got.File != "" {
				t.Errorf("Build() File = %q for a path-less parse, want empty", got.File)
			}
		})
	}
}

func TestLoad_FileIsTheDeclaringFile(t *testing.T) {
	t.Parallel()
	// The facts a tiered list needs: which file declares each host, and
	// whether it carries meta — across the root, sasash.conf, and another
	// Included file.
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(ssh, "config.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ssh, "config")
	managed := filepath.Join(ssh, "config.d", "sasash.conf")
	extra := filepath.Join(ssh, "work.conf")
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(root, "Include ~/.ssh/config.d/sasash.conf\nInclude work.conf\n# @sasash tags home\nHost described\n  HostName 192.0.2.3\nHost plain\n  HostName 192.0.2.4\n")
	write(managed, "# @sasash path lan 192.0.2.1\nHost web\n  HostName 192.0.2.1\nHost bare\n  HostName 192.0.2.5\n")
	write(extra, "# @sasash hide\nHost secret\n  HostName 192.0.2.2\nHost office\n  HostName 192.0.2.6\n")
	hosts, warnings, err := hostmodel.Load(root, home)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load() warnings %v, err %v", warnings, err)
	}
	type fact struct {
		file   string
		meta   bool
		hidden bool
	}
	got := map[string]fact{}
	for _, h := range hosts {
		got[h.Alias] = fact{h.File, h.HasMeta, h.Hidden}
	}
	want := map[string]fact{
		"web":       {managed, true, false},  // declared in sasash.conf, with meta
		"bare":      {managed, false, false}, // declared in sasash.conf, no meta of its own
		"secret":    {extra, true, true},     // an Included file, hidden
		"office":    {extra, false, false},   // an Included file, ambient
		"described": {root, true, false},     // ~/.ssh/config, meta makes it described
		"plain":     {root, false, false},    // ~/.ssh/config, ambient
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load() facts = %v, want %v", got, want)
	}
}
