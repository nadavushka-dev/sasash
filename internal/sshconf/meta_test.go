package sshconf_test

import (
	"reflect"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

func ownVerbs(t *testing.T, config, alias string) []string {
	t.Helper()
	h, ok := sshconf.Parse("", []byte(config)).Host(alias)
	if !ok {
		t.Fatalf("Host(%q) not found", alias)
	}
	var out []string
	for _, m := range h.OwnMeta() {
		out = append(out, m.Verb+" "+m.Args[0])
	}
	return out
}

func TestHost_OwnMeta_ExclusiveAttachment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config string
		wantA  []string // OwnMeta verbs+first arg for host a
		wantB  []string // same for host b
	}{
		{
			"next host's leading meta is not claimed by the previous block",
			"# @sasash path lan 192.0.2.1\nHost a\n  HostName 192.0.2.1\n\n# @sasash path ts 198.51.100.2\n# @sasash forwards web 8080:localhost:80\nHost b\n",
			[]string{"path lan"},
			[]string{"path ts", "forwards web"},
		},
		{
			"blank line keeps in-block meta with its own host",
			"Host a\n  # @sasash path lan 192.0.2.1\n\nHost b\n",
			[]string{"path lan"},
			nil,
		},
		{
			"run touching the next host line attaches to the next host only",
			"Host a\n  # @sasash path lan 192.0.2.1\nHost b\n",
			nil,
			[]string{"path lan"},
		},
		{
			"plain comment does not break the run",
			"Host a\n# a comment\n# @sasash path ts 198.51.100.2\n# also a comment\nHost b\n",
			nil,
			[]string{"path ts"},
		},
		{
			"directive breaks the run",
			"Host a\n  # @sasash path lan 192.0.2.1\n  HostName 192.0.2.1\nHost b\n",
			[]string{"path lan"},
			nil,
		},
		{
			"final block keeps its meta",
			"Host a\nHost b\n  # @sasash path lan 192.0.2.1",
			nil,
			[]string{"path lan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ownVerbs(t, tt.config, "a"); !reflect.DeepEqual(got, tt.wantA) {
				t.Errorf("OwnMeta(a) = %v, want %v", got, tt.wantA)
			}
			if got := ownVerbs(t, tt.config, "b"); !reflect.DeepEqual(got, tt.wantB) {
				t.Errorf("OwnMeta(b) = %v, want %v", got, tt.wantB)
			}
		})
	}
}

func TestHost_OwnMeta_MetaAboveMatchBelongsToNoHost(t *testing.T) {
	t.Parallel()

	config := "Host a\n  HostName 192.0.2.1\n# @sasash path lan 192.0.2.1\nMatch all\n"
	if got := ownVerbs(t, config, "a"); got != nil {
		t.Errorf("OwnMeta(a) = %v, want none: the run leads into a Match line", got)
	}
}

func TestHost_OwnMetaVerb_FiltersCaseInsensitively(t *testing.T) {
	t.Parallel()

	config := "# @sasash PATH lan 192.0.2.1\n# @sasash tags dev\nHost a\n"
	h, ok := sshconf.Parse("", []byte(config)).Host("a")
	if !ok {
		t.Fatal(`Host("a") not found`)
	}
	got := h.OwnMetaVerb("path")
	if len(got) != 1 || got[0].Args[0] != "lan" {
		t.Errorf("OwnMetaVerb(path) = %v, want one entry for lan", got)
	}
}
