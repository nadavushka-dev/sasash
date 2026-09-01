package sshconf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

func TestFile_LenAndLine(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("", []byte("Host a\n  User nadav\n"))
	if got := f.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
	if got := f.Line(1); got.Keyword != "User" || got.Args != "nadav" || got.Index != 1 {
		t.Errorf("Line(1) = %+v, want User/nadav at index 1", got)
	}
}

func TestHost_GetLine(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("", []byte("Host a\n  User nadav\n  HostName 1.1.1.1\n"))
	h, _ := f.Host("a")

	got, ok := h.GetLine("hostname")
	if !ok {
		t.Fatal("GetLine(hostname) not found")
	}
	if got.Index != 2 || got.Args != "1.1.1.1" {
		t.Errorf("GetLine() = index %d args %q, want index 2 args 1.1.1.1", got.Index, got.Args)
	}
	if _, ok := h.GetLine("Port"); ok {
		t.Error("GetLine(Port) found a line that is not there")
	}
}

// "Host" with no patterns is malformed but must not panic or claim a name.
func TestHost_Name_NoPatterns(t *testing.T) {
	t.Parallel()

	hosts := sshconf.Parse("", []byte("Host\n  User nadav\n")).Hosts()
	if len(hosts) != 1 {
		t.Fatalf("Hosts() = %d, want 1", len(hosts))
	}
	if got := hosts[0].Name(); got != "" {
		t.Errorf("Name() = %q, want empty", got)
	}
}

func TestFile_Host_NotFound(t *testing.T) {
	t.Parallel()

	f := sshconf.Parse("", []byte("Host a\n"))
	if h, ok := f.Host("nope"); ok || h != nil {
		t.Errorf("Host(nope) = %v/%v, want nil/false", h, ok)
	}
}

// A file with no trailing newline must not have its last line run into an
// inserted directive.
func TestHost_Set_InsertsAfterUnterminatedHostLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "host line is the whole file",
			in:   "Host a",
			want: "Host a\n  HostName 1.1.1.1\n",
		},
		{
			name: "host line ends a file with earlier content",
			in:   "Host other\n  User x\n\nHost a",
			want: "Host other\n  User x\n\nHost a\n  HostName 1.1.1.1\n",
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
			h.Set("HostName", "1.1.1.1")

			if got := f.String(); got != tt.want {
				t.Errorf("Set()\n got: %q\nwant: %q", got, tt.want)
			}
			// The result must re-parse to the value we just set.
			h2, _ := sshconf.Parse("", f.Bytes()).Host("a")
			if got, _ := h2.Get("HostName"); got != "1.1.1.1" {
				t.Errorf("re-parsed HostName = %q, want 1.1.1.1", got)
			}
		})
	}
}

func TestWriteAtomic_Errors(t *testing.T) {
	t.Parallel()

	t.Run("parent path is a regular file", func(t *testing.T) {
		t.Parallel()

		blocker := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile() err = %v", err)
		}

		f := sshconf.Parse("", []byte("Host a\n"))
		if err := f.WriteAtomic(filepath.Join(blocker, "sasash.conf")); err == nil {
			t.Fatal("WriteAtomic() err = nil, want error")
		}
	})

	t.Run("unwritable directory", func(t *testing.T) {
		t.Parallel()

		dir := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(dir, 0o500); err != nil {
			t.Fatalf("Mkdir() err = %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		f := sshconf.Parse("", []byte("Host a\n"))
		if err := f.WriteAtomic(filepath.Join(dir, "sasash.conf")); err == nil {
			t.Fatal("WriteAtomic() err = nil, want error")
		}
	})
}

// A File built by Parse with a zero Perm must still land at DefaultPerm.
func TestWriteAtomic_DefaultPerm(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sasash.conf")
	f := sshconf.Parse("", []byte("Host a\n"))
	f.Perm = 0

	if err := f.WriteAtomic(path); err != nil {
		t.Fatalf("WriteAtomic() err = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() err = %v", err)
	}
	if got := info.Mode().Perm(); got != sshconf.DefaultPerm {
		t.Errorf("mode = %o, want %o", got, sshconf.DefaultPerm)
	}
}

func TestWriteAtomic_PreservesExistingMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sasash.conf")
	if err := os.WriteFile(path, []byte("Host a\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	f, err := sshconf.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() err = %v", err)
	}
	if err := f.WriteAtomic(path); err != nil {
		t.Fatalf("WriteAtomic() err = %v", err)
	}

	info, _ := os.Stat(path)
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o, want 644 (the file's existing mode)", got)
	}
}

// A directive with no arguments at all still parses as a directive.
func TestParse_DirectiveWithoutArguments(t *testing.T) {
	t.Parallel()

	line := sshconf.Parse("", []byte("Compression\n")).Line(0)
	if line.Kind != sshconf.KindDirective {
		t.Errorf("Kind = %v, want directive", line.Kind)
	}
	if line.Keyword != "Compression" || line.Args != "" {
		t.Errorf("= %q/%q, want Compression/empty", line.Keyword, line.Args)
	}
}
