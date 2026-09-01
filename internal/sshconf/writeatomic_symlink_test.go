package sshconf_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// WriteAtomic must land on a symlink's target, not replace the link — users
// symlink ssh configs into dotfiles repos (SASASH.2.5, QA W4).
func TestWriteAtomic_WritesThroughSymlink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// target returns the link target to create, given the real file's
		// absolute path (relative targets resolve against the link's dir).
		target func(real string) string
		chain  bool // interpose a second link
		// otherDir puts the real file in a sibling directory, as a dotfiles
		// repo would, and makes the LINK's directory read-only. This pins
		// the ordering invariant: the resolve must happen BEFORE the temp
		// directory is derived, or the temp is created next to the LINK —
		// which fails here (read-only dir), and in the real world abandons
		// the same-directory guarantee that makes the rename atomic and
		// fails outright across mounts. A plain leftovers check cannot
		// catch the reorder: on one filesystem the misplaced temp still
		// renames away cleanly (verified by mutation).
		otherDir bool
	}{
		{
			name:   "absolute link",
			target: func(real string) string { return real },
		},
		{
			name:   "relative link",
			target: func(real string) string { return filepath.Base(real) },
		},
		{
			name:   "chain of links",
			target: func(real string) string { return real },
			chain:  true,
		},
		{
			name:     "link into another directory",
			target:   func(real string) string { return real },
			otherDir: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			realDir := dir
			if tt.otherDir {
				realDir = filepath.Join(dir, "dotfiles")
				if err := os.Mkdir(realDir, 0o700); err != nil {
					t.Fatalf("Mkdir(%q) err = %v", realDir, err)
				}
			}
			real := filepath.Join(realDir, "real.conf")
			if err := os.WriteFile(real, []byte("Host a\n  HostName 192.0.2.1\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() err = %v", err)
			}
			link := filepath.Join(dir, "link.conf")
			if err := os.Symlink(tt.target(real), link); err != nil {
				t.Fatalf("Symlink() err = %v", err)
			}
			path := link
			if tt.chain {
				path = filepath.Join(dir, "outer.conf")
				if err := os.Symlink(link, path); err != nil {
					t.Fatalf("Symlink(outer) err = %v", err)
				}
			}
			if tt.otherDir {
				// Read-only link dir: only a temp misplaced beside the
				// link (the reorder defect) would need to write here.
				// Root ignores the chmod, which merely blunts the mutant
				// detection; the test stays green on correct code.
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatalf("Chmod(%q) err = %v", dir, err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}

			f, err := sshconf.ParseFile(path)
			if err != nil {
				t.Fatalf("ParseFile() err = %v", err)
			}
			h, ok := f.Host("a")
			if !ok {
				t.Fatal("Host(a) not found")
			}
			h.Set("HostName", "192.0.2.10")
			if err := f.WriteAtomic(path); err != nil {
				t.Fatalf("WriteAtomic() err = %v", err)
			}

			for _, l := range []string{link, path} {
				info, err := os.Lstat(l)
				if err != nil {
					t.Fatalf("Lstat(%q) err = %v", l, err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Errorf("%q is no longer a symlink", l)
				}
			}
			data, err := os.ReadFile(real)
			if err != nil {
				t.Fatalf("ReadFile(real) err = %v", err)
			}
			if got, want := string(data), "Host a\n  HostName 192.0.2.10\n"; got != want {
				t.Errorf("link target = %q, want %q", got, want)
			}
			info, err := os.Stat(real)
			if err != nil {
				t.Fatalf("Stat(real) err = %v", err)
			}
			if got := info.Mode().Perm(); got != 0o644 {
				t.Errorf("target perm = %o, want %o (preserved through the link)", got, 0o644)
			}
			// Both directories: a temp beside the LINK would mean the
			// resolve ran after the temp dir was derived.
			for _, d := range []string{dir, realDir} {
				leftovers, err := filepath.Glob(filepath.Join(d, ".sasash-*"))
				if err != nil {
					t.Fatalf("Glob(%q) err = %v", d, err)
				}
				if len(leftovers) != 0 {
					t.Errorf("temp files left behind in %q: %v", d, leftovers)
				}
			}
		})
	}
}

// A dangling link does not resolve, so the write lands at the given path and
// the dead link becomes a regular file — documented WriteAtomic behavior.
func TestWriteAtomic_DanglingSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(filepath.Join(dir, "gone.conf"), link); err != nil {
		t.Fatalf("Symlink() err = %v", err)
	}

	f := sshconf.Parse(link, []byte("Host a\n"))
	if err := f.WriteAtomic(link); err != nil {
		t.Fatalf("WriteAtomic() err = %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat() err = %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("dangling link survived; want a regular file in its place")
	}
	data, err := os.ReadFile(link)
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if !strings.Contains(string(data), "Host a") {
		t.Errorf("content = %q, want the written config", data)
	}
}

// A symlinked parent directory is transparent: the file inside stays a
// regular file and the write goes through.
func TestWriteAtomic_SymlinkedDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realDir := filepath.Join(base, "real.d")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "c.conf"), []byte("Host a\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}
	linkDir := filepath.Join(base, "link.d")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("Symlink() err = %v", err)
	}

	path := filepath.Join(linkDir, "c.conf")
	f, err := sshconf.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile() err = %v", err)
	}
	if err := f.WriteAtomic(path); err != nil {
		t.Fatalf("WriteAtomic() err = %v", err)
	}

	info, err := os.Lstat(linkDir)
	if err != nil {
		t.Fatalf("Lstat(dir link) err = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("directory link is no longer a symlink")
	}
	info, err = os.Lstat(filepath.Join(realDir, "c.conf"))
	if err != nil {
		t.Fatalf("Lstat(real file) err = %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("real file mode = %v, want regular", info.Mode())
	}
}
