package sshconf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

func TestHost_File_LinksTheDeclaringFile(t *testing.T) {
	t.Parallel()
	f := sshconf.Parse("/x/config", []byte("Host a\n  HostName 192.0.2.1\n"))
	h, ok := f.Host("a")
	if !ok || h.File() != f || h.File().Path != "/x/config" {
		t.Errorf("Host(a).File() = %v, want the parsed file at /x/config", h.File())
	}
	// Through an Include, each block links the file it was read from.
	dir := t.TempDir()
	root, inc := filepath.Join(dir, "config"), filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(inc, []byte("Host b\n  HostName 192.0.2.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("Include "+inc+"\nHost a\n  HostName 192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := sshconf.IncludeHosts(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for i := range hosts {
		got[hosts[i].Name()] = hosts[i].File().Path
	}
	if got["a"] != root || got["b"] != inc {
		t.Errorf("File().Path per host = %v, want a→%s, b→%s", got, root, inc)
	}
}
