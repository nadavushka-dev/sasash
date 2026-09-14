// Package config reads and writes sasash's own settings file,
// $XDG_CONFIG_HOME/sasash/config.toml (~/.config/sasash/config.toml).
//
// The file is the user's: `sasash setup` creates it with every option
// commented out beside its default, the user edits it by hand, and the
// TUI writes back the choices it offers keys for (the theme, the hidden
// toggle) — one line at a time, losslessly (every other byte rides
// through), through the same temp-file + fsync + rename discipline as
// sasash.conf, re-reading first so a hand edit is never overwritten.
//
// Keys is the one registry: the template, the loader and the docs test
// all read it, so an option cannot exist undocumented.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nadavushka-dev/sasash/internal/toml"
)

// Key describes one option.
type Key struct {
	Name    string
	Kind    toml.Kind
	Default string // the TOML literal the template shows, e.g. `"gruvbox"` or `false`
	Doc     string // one line, above the key in the template and in docs/configuration.md
}

// Keys is every option, in template order.
var Keys = []Key{
	{Name: "theme", Kind: toml.String, Default: `"gruvbox"`,
		Doc: "color theme: gruvbox, mocha, tokyonight or nord — t cycles and o picks (both write it here); --theme and SASASH_THEME override for one run"},
	{Name: "editor", Kind: toml.String, Default: `""`,
		Doc: `editor command for e, split on spaces (e.g. "code -w"); empty means $EDITOR, then vi`},
	{Name: "show_hidden", Kind: toml.Bool, Default: `false`,
		Doc: "list dotfiles in the local file picker of C (copy files); H there flips it and writes it back"},
}

// Header opens a freshly created config.toml.
const Header = "# sasash configuration — see docs/configuration.md\n# Every option is shown commented out with its default; uncomment a line to set it.\n"

// Config is the loaded file. A missing file is the zero value; a zero
// field means "not set" (the caller applies the default).
type Config struct {
	Present    bool // the file exists and was read
	Theme      string
	Editor     string
	ShowHidden bool
}

// Path is the file's location: $XDG_CONFIG_HOME/sasash/config.toml, or
// ~/.config/sasash/config.toml.
func Path(home string, getenv func(string) string) string {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "sasash", "config.toml")
	}
	return filepath.Join(home, ".config", "sasash", "config.toml")
}

// ParseError reports a rejected config.toml with the 1-based line.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("config.toml:%d: %s", e.Line, e.Msg) }

// Load reads path. A missing file is the zero Config with no error. A
// file the parser cannot read line by line is an error (the zero Config
// again — never a partial read). A readable file with an unknown key or
// a value of the wrong type applies the rest and reports each problem
// as a warning naming its line, so an option added by a newer sasash or
// a typo never keeps the TUI from starting.
func Load(path string) (Config, []string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil, nil
	}
	if err != nil {
		return Config{}, nil, err
	}
	lines, err := parse(string(data))
	if err != nil {
		return Config{}, nil, err
	}
	cfg := Config{Present: true}
	var warns []string
	for _, l := range lines {
		if l.key == "" {
			continue
		}
		k, ok := lookup(l.key)
		if !ok {
			warns = append(warns, fmt.Sprintf("config.toml:%d: unknown key %q (ignored)", l.n, l.key))
			continue
		}
		if l.val.Kind != k.Kind {
			warns = append(warns, fmt.Sprintf("config.toml:%d: %s must be %s (ignored)", l.n, k.Name, kindName(k.Kind)))
			continue
		}
		switch k.Name {
		case "theme":
			cfg.Theme = l.val.Str
		case "editor":
			cfg.Editor = l.val.Str
		case "show_hidden":
			cfg.ShowHidden = l.val.Bool
		}
	}
	return cfg, warns, nil
}

// Template is the file setup writes: the header, then each key's doc
// line and the key commented out beside its default.
func Template() string {
	var b strings.Builder
	b.WriteString(Header)
	for _, k := range Keys {
		b.WriteString("\n# " + k.Doc + "\n")
		b.WriteString("# " + k.Name + " = " + k.Default + "\n")
	}
	return b.String()
}

// setMu serializes Set: the TUI's write-backs run as bubbletea commands
// on their own goroutines, and two read-modify-writes in flight at once
// would let one key's value overwrite the other's.
var setMu sync.Mutex

// Set writes one key into the file at path: the file is re-read (a hand
// edit since the last load wins), the key's line rewritten in place
// (Apply), and the result written atomically. A missing file starts
// from Template, so the first `t` on a fresh install yields the file
// setup would have written plus the one live line.
func Set(path, key string, value any) error {
	setMu.Lock()
	defer setMu.Unlock()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		data = []byte(Template())
	case err != nil:
		return err
	}
	out, err := Apply(string(data), key, value)
	if err != nil {
		return err
	}
	return WriteAtomic(path, out)
}

// Apply returns data with key set to value (a string or a bool): a live
// `key = …` line is rewritten in place — its indent and any trailing
// comment kept — else the template's commented `# key = <value>` line
// (a comment that would parse as the key if uncommented; prose that
// merely starts with the key's name is not one) becomes live where it
// stands, indent kept, else the line is appended with the file's own
// terminator. Every other byte is returned as it was. A file the parser
// rejects is left alone (the error names the line): sasash never
// rewrites what it cannot read.
func Apply(data, key string, value any) (string, error) {
	k, ok := lookup(key)
	if !ok {
		return "", fmt.Errorf("config: unknown key %q", key)
	}
	lit, err := literal(k, value)
	if err != nil {
		return "", err
	}
	lines, err := parse(data)
	if err != nil {
		return "", err
	}
	raw := strings.SplitAfter(data, "\n")
	if raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1] // SplitAfter's empty tail after a final "\n"
	}
	commented := -1
	for _, l := range lines {
		i := l.n - 1
		if l.key == key {
			body, term := splitTerm(raw[i])
			indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
			raw[i] = indent + key + " = " + lit + l.trailing + term
			return strings.Join(raw, ""), nil
		}
		if commented < 0 && l.commentedKey == key {
			commented = i
		}
	}
	line := key + " = " + lit
	if commented >= 0 {
		body, term := splitTerm(raw[commented])
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		raw[commented] = indent + line + term
		return strings.Join(raw, ""), nil
	}
	term := "\n"
	if len(raw) > 0 {
		if _, t := splitTerm(raw[0]); t != "" {
			term = t // the file's own style, so a CRLF file stays CRLF
		}
	}
	out := strings.Join(raw, "")
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += term
	}
	return out + line + term, nil
}

// WriteAtomic writes data to path through a temp file in the same
// directory, fsynced and renamed into place (SPEC: never truncate a
// file the user hand-edits). A symlinked path is written through, so a
// dotfiles-managed link survives; an existing file keeps its mode; the
// directory is created as needed.
func WriteAtomic(path, data string) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sasash-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// line is one parsed line: a key = value line carries key, val and the
// text after the value (a trailing comment, kept on rewrite); a
// commented-out `# key = …` line carries commentedKey so Apply can
// bring the template's line to life in place.
type line struct {
	n            int
	key          string
	val          toml.Value
	trailing     string
	commentedKey string
}

// parse reads data line by line: blanks, comments and `key = value`
// lines. Anything else — a [table], a dotted key, a value the toml
// subset does not know — is a ParseError naming the line.
func parse(data string) ([]line, error) {
	var out []line
	seen := map[string]int{}
	for n, raw := range strings.Split(data, "\n") {
		ln := n + 1
		s := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if s == "" {
			continue
		}
		if s[0] == '#' {
			out = append(out, line{n: ln, commentedKey: commentedKey(s)})
			continue
		}
		if s[0] == '[' {
			return nil, &ParseError{ln, "tables are not used in config.toml (top-level keys only)"}
		}
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			return nil, &ParseError{ln, fmt.Sprintf("expected key = value, got %q", s)}
		}
		key = strings.TrimSpace(key)
		if !toml.BareKey(key) {
			return nil, &ParseError{ln, fmt.Sprintf("unsupported key %q (bare keys only)", key)}
		}
		val, rem, err := toml.ParseValue(strings.TrimSpace(rest))
		if err != nil {
			return nil, &ParseError{ln, err.Error()}
		}
		if !toml.TrailingOK(rem) {
			return nil, &ParseError{ln, fmt.Sprintf("unexpected trailing content %q", strings.TrimSpace(rem))}
		}
		if first, dup := seen[key]; dup {
			return nil, &ParseError{ln, fmt.Sprintf("duplicate key %q (first on line %d)", key, first)}
		}
		seen[key] = ln
		out = append(out, line{n: ln, key: key, val: val, trailing: strings.TrimRight(rem, " \t")})
	}
	return out, nil
}

// commentedKey reads the key of a `# key = value` line — one that would
// parse as that key, with a value of its kind, if uncommented — and ""
// for any other comment, so only the template's own shape is revived
// in place and a user's prose that starts with a key's name is kept.
func commentedKey(s string) string {
	body := strings.TrimSpace(strings.TrimLeft(s, "#"))
	key, rest, ok := strings.Cut(body, "=")
	if !ok {
		return ""
	}
	key = strings.TrimSpace(key)
	k, known := lookup(key)
	if !known {
		return ""
	}
	val, rem, err := toml.ParseValue(strings.TrimSpace(rest))
	if err != nil || val.Kind != k.Kind || !toml.TrailingOK(rem) {
		return ""
	}
	return key
}

// splitTerm separates a raw line from its terminator.
func splitTerm(raw string) (body, term string) {
	for _, t := range []string{"\r\n", "\n"} {
		if strings.HasSuffix(raw, t) {
			return strings.TrimSuffix(raw, t), t
		}
	}
	return raw, ""
}

// literal renders value as the TOML literal for k, refusing a type the
// key does not take — the ONE place a value is turned into file text.
func literal(k Key, value any) (string, error) {
	switch v := value.(type) {
	case string:
		if k.Kind != toml.String {
			return "", fmt.Errorf("config: %s takes %s, not a string", k.Name, kindName(k.Kind))
		}
		return toml.Quote(v), nil
	case bool:
		if k.Kind != toml.Bool {
			return "", fmt.Errorf("config: %s takes %s, not a bool", k.Name, kindName(k.Kind))
		}
		if v {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("config: %s: unsupported value %T", k.Name, value)
}

func lookup(name string) (Key, bool) {
	for _, k := range Keys {
		if k.Name == name {
			return k, true
		}
	}
	return Key{}, false
}

func kindName(k toml.Kind) string {
	if k == toml.Bool {
		return "true or false"
	}
	return "a quoted string"
}
