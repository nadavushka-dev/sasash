// Package sshconf reads and edits OpenSSH client config files without
// disturbing anything it does not understand.
//
// The parser is lossless by construction: every line is retained verbatim,
// including its terminator, so String() returns the input byte-for-byte until
// something is deliberately changed. Structure (Host blocks, directives,
// "# @sasash" metadata) is a derived view over those lines, and an edit
// rewrites only the lines it touches.
//
// That matters because sasash writes to a file the user also edits by hand.
// See SPEC.md §2.
package sshconf

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MetaPrefix marks a comment as sasash metadata.
const MetaPrefix = "@sasash"

// Kind classifies a line.
type Kind int

const (
	// KindBlank is an empty or whitespace-only line.
	KindBlank Kind = iota
	// KindComment is a comment with no sasash metadata.
	KindComment
	// KindMeta is a "# @sasash ..." metadata comment.
	KindMeta
	// KindDirective is a keyword/argument line such as "HostName 10.0.0.1".
	KindDirective
)

// File is a parsed config file.
type File struct {
	// Path is where the file was read from; it may be empty.
	Path string
	// Perm is the mode to preserve on write. Zero means DefaultPerm.
	Perm fs.FileMode

	lines []string // each element includes its line terminator, if any
}

// DefaultPerm is used for a file sasash creates. ssh config files hold no
// secrets but are user-private by convention.
const DefaultPerm fs.FileMode = 0o600

// Parse builds a File from raw bytes. It cannot fail: any line the parser does
// not recognise is preserved as-is.
func Parse(path string, data []byte) *File {
	return &File{Path: path, Perm: DefaultPerm, lines: splitLines(string(data))}
}

// ParseFile reads and parses path, preserving its mode for a later write.
func ParseFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := Parse(path, data)
	if info, err := os.Stat(path); err == nil {
		f.Perm = info.Mode().Perm()
	}
	return f, nil
}

// String renders the file. For an unmodified File it equals the input exactly.
func (f *File) String() string { return strings.Join(f.lines, "") }

// Bytes is String as a byte slice.
func (f *File) Bytes() []byte { return []byte(f.String()) }

// Len returns the number of lines.
func (f *File) Len() int { return len(f.lines) }

// WriteAtomic writes the file to path via a temporary file and a rename, so a
// crash or a full disk can never leave a half-written ssh config behind.
//
// A path that is a symlink is resolved first, so the rename lands on the real
// file and the link survives — dotfiles repos symlink ssh configs, and a
// write must not sever that. A path that does not resolve (a new file, a
// dangling link, or a symlink cycle) is used as given; a dangling link is
// thus replaced by a regular file.
func (f *File) WriteAtomic(path string) error {
	perm := f.Perm
	if perm == 0 {
		perm = DefaultPerm
	}

	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".sasash-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.WriteString(f.String()); err != nil {
		tmp.Close()
		return err
	}
	// Durability before visibility: fsync, then rename.
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

// Line is one parsed line of the file.
type Line struct {
	Index   int    // position in the file
	Raw     string // verbatim, including any terminator
	Kind    Kind
	Indent  string   // leading whitespace
	Keyword string   // original casing; empty unless KindDirective
	Args    string   // everything after the keyword and its separator
	Fields  []string // Args split on whitespace
}

// Line returns the parsed form of line i.
func (f *File) Line(i int) Line { return parseLine(i, f.lines[i]) }

// Lines returns every parsed line.
func (f *File) Lines() []Line {
	out := make([]Line, len(f.lines))
	for i := range f.lines {
		out[i] = parseLine(i, f.lines[i])
	}
	return out
}

// Host is a "Host" block: the Host line plus every line up to the next Host or
// Match line.
type Host struct {
	// Patterns are the host patterns on the Host line.
	Patterns []string
	// Start is the index of the Host line; End is exclusive.
	Start, End int

	f *File
}

// Name is the first pattern, which is the alias sasash uses to identify a host.
func (h *Host) Name() string {
	if len(h.Patterns) == 0 {
		return ""
	}
	return h.Patterns[0]
}

// Hosts returns every Host block in file order.
func (f *File) Hosts() []Host {
	var hosts []Host
	for i := range f.lines {
		line := parseLine(i, f.lines[i])
		if line.Kind != KindDirective {
			continue
		}
		switch strings.ToLower(line.Keyword) {
		case "host":
			if len(hosts) > 0 {
				hosts[len(hosts)-1].End = i
			}
			hosts = append(hosts, Host{Patterns: line.Fields, Start: i, End: len(f.lines), f: f})
		case "match":
			// A Match block ends the preceding Host block. sasash does not
			// manage Match blocks, but it must not read through one.
			if len(hosts) > 0 && hosts[len(hosts)-1].End == len(f.lines) {
				hosts[len(hosts)-1].End = i
			}
		}
	}
	return hosts
}

// Host returns the block whose first pattern is name.
//
// If the same alias is declared twice, the first wins, matching ssh's
// first-value-wins resolution.
func (f *File) Host(name string) (*Host, bool) {
	hosts := f.Hosts()
	for i := range hosts {
		if hosts[i].Name() == name {
			return &hosts[i], true
		}
	}
	return nil, false
}

// Get returns the value of the first occurrence of keyword in the block.
// Keyword matching is case-insensitive, as in ssh_config.
func (h *Host) Get(keyword string) (string, bool) {
	if line, ok := h.find(keyword); ok {
		return line.Args, true
	}
	return "", false
}

// GetLine is Get plus the index of the line the value came from.
func (h *Host) GetLine(keyword string) (Line, bool) { return h.find(keyword) }

func (h *Host) find(keyword string) (Line, bool) {
	want := strings.ToLower(keyword)
	for i := h.Start + 1; i < h.End; i++ {
		line := parseLine(i, h.f.lines[i])
		if line.Kind == KindDirective && strings.ToLower(line.Keyword) == want {
			return line, true
		}
	}
	return Line{}, false
}

// Set writes keyword = value in the block.
//
// An existing directive is rewritten in place, keeping its indent, its
// original keyword casing, and its line terminator, so a diff shows only the
// value changing. A missing directive is inserted directly after the Host
// line, indented to match the block.
func (h *Host) Set(keyword, value string) {
	if line, ok := h.find(keyword); ok {
		h.f.lines[line.Index] = line.Indent + line.Keyword + " " + value + terminatorOf(line.Raw)
		return
	}

	// Match the terminator style of the Host line, so inserting into a CRLF
	// file does not introduce a lone LF. The Host line can also be the last
	// line of a file that ends without a newline; terminate it first, or the
	// inserted directive would run onto the end of it.
	term := terminatorOf(h.f.lines[h.Start])
	if term == "" {
		term = "\n"
		h.f.lines[h.Start] += term
	}

	insertAt := h.Start + 1
	raw := h.blockIndent() + keyword + " " + value + term
	h.f.lines = append(h.f.lines[:insertAt], append([]string{raw}, h.f.lines[insertAt:]...)...)
	h.End++
}

// Unset removes every occurrence of keyword from the block, reporting how many
// lines were removed.
func (h *Host) Unset(keyword string) int {
	want := strings.ToLower(keyword)
	removed := 0
	for i := h.End - 1; i > h.Start; i-- {
		line := parseLine(i, h.f.lines[i])
		if line.Kind == KindDirective && strings.ToLower(line.Keyword) == want {
			h.f.lines = append(h.f.lines[:i], h.f.lines[i+1:]...)
			h.End--
			removed++
		}
	}
	return removed
}

// blockIndent guesses the indent for an inserted directive from the lines
// already in the block, falling back to two spaces.
func (h *Host) blockIndent() string {
	for i := h.Start + 1; i < h.End; i++ {
		line := parseLine(i, h.f.lines[i])
		if line.Kind == KindDirective {
			return line.Indent
		}
	}
	return "  "
}

// Meta is one "# @sasash <verb> <args...>" line.
type Meta struct {
	Verb  string
	Args  []string
	Index int
}

// Meta returns the sasash metadata attached to the host: the "# @sasash" lines
// in the unbroken comment block immediately above the Host line, plus any
// inside the block itself.
func (h *Host) Meta() []Meta {
	var above []Meta
	for i := h.Start - 1; i >= 0; i-- {
		line := parseLine(i, h.f.lines[i])
		if line.Kind == KindMeta {
			if m, ok := metaOf(line); ok {
				above = append(above, m)
			}
			continue
		}
		if line.Kind == KindComment {
			continue // a plain comment does not break the block
		}
		break
	}
	// Walking upward reversed them; restore file order.
	for i, j := 0, len(above)-1; i < j; i, j = i+1, j-1 {
		above[i], above[j] = above[j], above[i]
	}

	out := above
	for i := h.Start + 1; i < h.End; i++ {
		line := parseLine(i, h.f.lines[i])
		if line.Kind != KindMeta {
			continue
		}
		if m, ok := metaOf(line); ok {
			out = append(out, m)
		}
	}
	return out
}

// MetaVerb returns every metadata entry with the given verb.
func (h *Host) MetaVerb(verb string) []Meta {
	var out []Meta
	for _, m := range h.Meta() {
		if strings.EqualFold(m.Verb, verb) {
			out = append(out, m)
		}
	}
	return out
}

func metaOf(line Line) (Meta, bool) {
	if len(line.Fields) == 0 {
		return Meta{}, false
	}
	return Meta{Verb: line.Fields[0], Args: line.Fields[1:], Index: line.Index}, true
}

// parseLine derives structure from a raw line without altering it.
func parseLine(index int, raw string) Line {
	line := Line{Index: index, Raw: raw}
	body := strings.TrimRight(raw, "\r\n")

	trimmed := strings.TrimLeft(body, " \t")
	line.Indent = body[:len(body)-len(trimmed)]

	switch {
	case trimmed == "":
		line.Kind = KindBlank
		return line
	case strings.HasPrefix(trimmed, "#"):
		rest := strings.TrimLeft(strings.TrimPrefix(trimmed, "#"), " \t")
		if after, ok := strings.CutPrefix(rest, MetaPrefix); ok {
			// Require a separator so "@sasashfoo" is not treated as metadata.
			if after == "" || after[0] == ' ' || after[0] == '\t' {
				line.Kind = KindMeta
				line.Args = strings.TrimSpace(after)
				line.Fields = strings.Fields(line.Args)
				return line
			}
		}
		line.Kind = KindComment
		return line
	}

	line.Kind = KindDirective
	keyword, args := splitDirective(trimmed)
	line.Keyword, line.Args = keyword, args
	line.Fields = strings.Fields(args)
	return line
}

// splitDirective separates a keyword from its arguments. ssh_config allows
// whitespace, "=", or both as the separator.
func splitDirective(s string) (keyword, args string) {
	i := strings.IndexAny(s, " \t=")
	if i < 0 {
		return s, ""
	}
	keyword = s[:i]
	rest := strings.TrimLeft(s[i:], " \t")
	rest = strings.TrimPrefix(rest, "=")
	return keyword, strings.TrimSpace(rest)
}

func terminatorOf(raw string) string {
	switch {
	case strings.HasSuffix(raw, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(raw, "\n"):
		return "\n"
	default:
		return ""
	}
}

// splitLines splits s into lines that each retain their terminator, so
// joining them reproduces s exactly. CRLF and a missing final newline both
// survive.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// String renders a Kind for test failure messages.
func (k Kind) String() string {
	switch k {
	case KindBlank:
		return "blank"
	case KindComment:
		return "comment"
	case KindMeta:
		return "meta"
	case KindDirective:
		return "directive"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}
