// Package setup performs sasash's one-time bootstrap (SPEC.md §2): an
// Include line at the top of ~/.ssh/config, the managed
// ~/.ssh/config.d/sasash.conf, and the ~/.ssh/cm control-socket directory.
//
// Every function takes the home directory explicitly so tests run against
// t.TempDir(). Confirmation lives in the caller; Check and Ensure are pure
// do/check functions and both are idempotent.
package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// IncludeLine is what Ensure inserts at the top of ~/.ssh/config. ssh_config
// is first-value-wins, so the managed file must precede any "Host *" defaults.
const IncludeLine = "Include ~/.ssh/config.d/sasash.conf"

// Header is the first line of a freshly created sasash.conf, from SPEC.md §2.
const Header = "# ─── managed by sasash. hand-editable; unknown lines are preserved verbatim. ───"

// Status reports which bootstrap pieces are already in place.
type Status struct {
	// IncludePresent is true when ~/.ssh/config already includes
	// sasash.conf, in any equivalent spelling, in the global section —
	// before any Host or Match block, where ssh applies it unconditionally.
	IncludePresent bool
	// ConfPresent is true when ~/.ssh/config.d/sasash.conf exists.
	ConfPresent bool
	// CMDirPresent is true when ~/.ssh/cm exists and is a directory.
	CMDirPresent bool
}

// Done reports whether nothing is left for Ensure to do.
func (s Status) Done() bool { return s.IncludePresent && s.ConfPresent && s.CMDirPresent }

// Check inspects home without changing anything.
func Check(home string) (Status, error) {
	var s Status

	data, err := os.ReadFile(configPath(home))
	switch {
	case err == nil:
		s.IncludePresent = hasInclude(home, sshconf.Parse(configPath(home), data))
	case !errors.Is(err, fs.ErrNotExist):
		return Status{}, err
	}

	if _, err := os.Lstat(confPath(home)); err == nil {
		s.ConfPresent = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Status{}, err
	}

	if info, err := os.Stat(cmDir(home)); err == nil {
		s.CMDirPresent = info.IsDir()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Status{}, err
	}

	return s, nil
}

// Ensure brings home to the bootstrapped state. It is idempotent: pieces
// already in place are left byte-for-byte untouched, and an effective
// equivalent Include in ~/.ssh/config means the config is not rewritten.
// The Include target is created before the line that points at it.
func Ensure(home string) error {
	if err := ensureConf(home); err != nil {
		return err
	}
	if err := ensureInclude(home); err != nil {
		return err
	}
	return os.MkdirAll(cmDir(home), 0o700)
}

// ensureInclude puts IncludeLine at the top of ~/.ssh/config. The rewrite
// goes through sshconf's atomic writer and prepends the raw line to the
// original bytes, so the result is byte-identical except the inserted line
// and keeps the file's line-terminator style.
func ensureInclude(home string) error {
	path := configPath(home)

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return sshconf.Parse(path, []byte(IncludeLine+"\n")).WriteAtomic(path)
	}
	if err != nil {
		return err
	}
	if hasInclude(home, sshconf.Parse(path, data)) {
		return nil
	}

	// Write through a symlinked config rather than replacing it: WriteAtomic
	// renames over its target, and the rename must land on the real file so
	// a dotfiles-managed link survives.
	writePath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		writePath = resolved
	}

	f := sshconf.Parse(path, append([]byte(IncludeLine+terminatorStyle(data)), data...))
	if info, err := os.Stat(writePath); err == nil {
		f.Perm = info.Mode().Perm()
	}
	return f.WriteAtomic(writePath)
}

// ensureConf creates sasash.conf with the managed-file header. An existing
// file is never overwritten, whatever it contains; Lstat means even a
// symlink or a broken one counts as existing.
func ensureConf(home string) error {
	path := confPath(home)
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return sshconf.Parse(path, []byte(Header+"\n")).WriteAtomic(path)
}

// hasInclude reports whether an Include directive in f's global section names
// sasash.conf. ssh resolves a relative Include against ~/.ssh, expands a
// leading tilde or %d, and globs, so all equivalent spellings count. An
// Include inside a Host or Match block does not: ssh applies it only when the
// block matches, so the bootstrap is not in effect.
func hasInclude(home string, f *sshconf.File) bool {
	target := filepath.Clean(confPath(home))
	for _, line := range f.Lines() {
		if line.Kind != sshconf.KindDirective {
			continue
		}
		switch strings.ToLower(line.Keyword) {
		case "host", "match":
			return false
		case "include":
			for _, field := range line.Fields {
				p := resolveInclude(home, field)
				if p == target {
					return true
				}
				if ok, _ := filepath.Match(p, target); ok {
					return true
				}
			}
		}
	}
	return false
}

// resolveInclude expands one Include argument the way ssh would.
func resolveInclude(home, field string) string {
	p := strings.Trim(field, `"`)
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	case strings.HasPrefix(p, "%d/"):
		p = filepath.Join(home, p[3:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(home, ".ssh", p)
	}
	return filepath.Clean(p)
}

// terminatorStyle returns the terminator of the file's first line, so an
// inserted line matches a CRLF file. A file with no newline gets "\n": the
// inserted line precedes existing content and must be terminated.
func terminatorStyle(data []byte) string {
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if i > 0 && data[i-1] == '\r' {
			return "\r\n"
		}
		return "\n"
	}
	return "\n"
}

func configPath(home string) string { return filepath.Join(home, ".ssh", "config") }
func confPath(home string) string   { return filepath.Join(home, ".ssh", "config.d", "sasash.conf") }
func cmDir(home string) string      { return filepath.Join(home, ".ssh", "cm") }
