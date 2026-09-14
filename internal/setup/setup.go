// Package setup performs sasash's one-time bootstrap (SPEC.md §2): an
// Include line at the top of ~/.ssh/config, the managed
// ~/.ssh/config.d/sasash.conf, the ~/.ssh/cm control-socket directory,
// and sasash's own config.toml with every option commented out
// (SASASH.4.36).
//
// Every function takes an Env — the home directory and the config.toml
// path — explicitly so tests run against t.TempDir() and setup never
// reads the environment itself. Confirmation lives in the caller; Check
// and Ensure are pure do/check functions and both are idempotent.
package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/config"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// Env names what setup inspects and writes. Config is config.toml's
// path, resolved by the cmd layer ($XDG_CONFIG_HOME moves it). Legacy
// lists the settings files that predate config.toml (SASASH.4.36); the
// cmd layer reads them, since it knows their formats.
type Env struct {
	Home   string
	Config string
	Legacy []Legacy
}

// Legacy is one pre-config settings file to move into config.toml:
// while Path exists, Check lists what Ensure will do with it (a Move)
// and Ensure does exactly that — writes Value as Key, unless config.toml
// already sets Key in effect (the user's choice, kept), then removes the
// file. A nil Value (the file names nothing sasash can keep, a stale
// theme say) is removed without a write. config.toml is then the one
// place settings live.
type Legacy struct {
	Path  string
	Key   string
	Value any
}

// Move is what Ensure does with one legacy file that still exists:
// write Value as Key and remove the file — or, Value nil, remove it
// alone, Reason saying why nothing is written. Check and Ensure derive
// the moves from the same function, so what is announced is what
// happens.
type Move struct {
	Path   string
	Key    string
	Value  any
	Reason string
}

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
	// ConfigPresent is true when config.toml exists.
	ConfigPresent bool
	// Legacy holds the moves Ensure has yet to make, one per Env.Legacy
	// file still present.
	Legacy []Move
}

// Done reports whether nothing is left for Ensure to do.
func (s Status) Done() bool {
	return s.IncludePresent && s.ConfPresent && s.CMDirPresent && s.ConfigPresent && len(s.Legacy) == 0
}

// Check inspects env without changing anything.
func Check(env Env) (Status, error) {
	var s Status
	home := env.Home

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

	if _, err := os.Lstat(env.Config); err == nil {
		s.ConfigPresent = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Status{}, err
	}

	if s.Legacy, err = legacyMoves(env); err != nil {
		return Status{}, err
	}

	return s, nil
}

// legacyMoves resolves what Ensure will do with each Env.Legacy file
// still present. config.toml is read (config.Live) only when a file
// carries a value, so a file the parser rejects blocks setup only when
// there is something to move into it.
func legacyMoves(env Env) ([]Move, error) {
	var moves []Move
	var live map[string]bool
	for _, l := range env.Legacy {
		if _, err := os.Lstat(l.Path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		m := Move{Path: l.Path, Key: l.Key, Value: l.Value}
		if l.Value == nil {
			m.Reason = "it names nothing sasash can keep"
		} else {
			if live == nil {
				var err error
				if live, err = config.Live(env.Config); err != nil {
					return nil, err
				}
			}
			if live[l.Key] {
				m.Value, m.Reason = nil, l.Key+" is already set in config.toml"
			}
		}
		moves = append(moves, m)
	}
	return moves, nil
}

// Ensure brings home to the bootstrapped state. It is idempotent: pieces
// already in place are left byte-for-byte untouched, and an effective
// equivalent Include in ~/.ssh/config means the config is not rewritten.
// The Include target is created before the line that points at it.
func Ensure(env Env) error {
	if err := ensureConf(env.Home); err != nil {
		return err
	}
	if err := ensureInclude(env.Home); err != nil {
		return err
	}
	if err := os.MkdirAll(cmDir(env.Home), 0o700); err != nil {
		return err
	}
	if err := ensureConfig(env.Config); err != nil {
		return err
	}
	return moveLegacy(env)
}

// ensureConfig creates config.toml from the template — every option
// commented out beside its default. An existing file is never
// overwritten, whatever it contains.
func ensureConfig(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return config.WriteAtomic(path, config.Template())
}

// moveLegacy performs the moves Check announced (see Move). The write
// goes through config.SetIfUnset — the TUI's lossless line rewrite,
// under its lock, so a value set meanwhile still wins — and the file is
// removed only once the write is durable.
func moveLegacy(env Env) error {
	moves, err := legacyMoves(env)
	if err != nil {
		return err
	}
	for _, m := range moves {
		if m.Value != nil {
			if _, err := config.SetIfUnset(env.Config, m.Key, m.Value); err != nil {
				return err
			}
		}
		if err := os.Remove(m.Path); err != nil {
			return err
		}
	}
	return nil
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
