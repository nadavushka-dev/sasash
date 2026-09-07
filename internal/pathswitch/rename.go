package pathswitch

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// This file renames a managed host (SASASH.4.16, CONTRACT-rename). It lives
// here because the only hard part of a rename is which "<old>.<x>" blocks
// follow, and that is anchoredStarts' two-witness rule — the same set a
// switch would rewrite.

// RenameResult describes one rename's fan-out.
type RenameResult struct {
	// Followed lists the path names whose generator-owned "<old>.<name>"
	// block became "<new>.<name>", in file order; nil without owned variants.
	Followed []string
	// LeftAsIs lists the full names of "<old>.<x>" blocks the generator does
	// not own — hand-authored, or demoted below a break in the group — left
	// verbatim; nil when none. Callers surface it ("N dotted blocks left as-is").
	LeftAsIs []string
}

// Rename renames the managed host old to new in the file at confPath in one
// atomic write: the block's Host line (first pattern only) and the Host line
// of every generator-owned "<old>.<name>" variant. Owned variants follow
// whether or not their path is still live — a stale one stays attached to
// its host for the next switch to clean up instead of orphaning under the
// old prefix. @sasash meta stays put (attachment is positional). On each
// rewritten line only the alias token changes; every other byte of the
// file survives verbatim. The package sees only sasash.conf: a collision
// with a host from another file is the caller's to refuse first.
func Rename(confPath, old, new string) (RenameResult, error) {
	if old == new {
		return RenameResult{}, fmt.Errorf("alias %q unchanged", new)
	}
	// A leading dash would make `ssh <alias>` parse the alias as an option;
	// a leading # would comment the Host line out; wildcards make a pattern,
	// not a name (the 2.3 save rules). Whitespace is sshconf's notion —
	// unicode.IsSpace, the rule strings.Fields splits patterns on — so a
	// pasted no-break space cannot become two patterns.
	if new == "" || strings.HasPrefix(new, "-") || strings.HasPrefix(new, "#") ||
		strings.IndexFunc(new, unicode.IsSpace) >= 0 || strings.ContainsAny(new, "\"*?!") {
		return RenameResult{}, fmt.Errorf("alias %q: want a single plain word", new)
	}
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return RenameResult{}, err
	}
	// First block wins, as ssh resolves it; a duplicate `Host <old>` further
	// down is not this rename's (the reader never showed it either).
	main, ok := f.Host(old)
	if !ok {
		return RenameResult{}, fmt.Errorf("host %q not managed by sasash", old)
	}
	for _, b := range f.Hosts() {
		for _, p := range b.Patterns {
			if p == new {
				return RenameResult{}, fmt.Errorf("alias %q already exists", new)
			}
		}
	}

	raw := make([]string, f.Len())
	for i := range raw {
		raw[i] = f.Line(i).Raw
	}
	raw[main.Start] = renameToken(f.Line(main.Start), old, new)

	var res RenameResult
	prefix := old + "."
	anchored := anchoredStarts(f, prefix)
	for _, b := range f.Hosts() {
		suffix := strings.TrimPrefix(b.Name(), prefix)
		if suffix == b.Name() {
			continue
		}
		if !anchored[b.Start] {
			res.LeftAsIs = append(res.LeftAsIs, b.Name())
			continue
		}
		// The names the fan-out creates are checked like new itself: a
		// generated "<new>.<name>" above a user's block of that name would
		// shadow it under first-value-wins — a touched host by a semantic
		// route, which byte-identity alone cannot see.
		if _, taken := f.Host(new + "." + suffix); taken {
			return RenameResult{}, fmt.Errorf("alias %q already exists", new+"."+suffix)
		}
		raw[b.Start] = renameToken(f.Line(b.Start), b.Name(), new+"."+suffix)
		res.Followed = append(res.Followed, suffix)
	}

	out := sshconf.Parse(confPath, []byte(strings.Join(raw, "")))
	out.Perm = f.Perm
	if err := out.WriteAtomic(confPath); err != nil {
		return RenameResult{}, err
	}
	return res, nil
}

// renameToken swaps the first argument of a directive line — the token
// right where its Args begin — leaving indent, keyword, separator, further
// arguments, trailing whitespace and terminator in place. Args is the
// trimmed tail of the line, so it starts where the trimmed body minus
// Args's length does; the first field is at its head.
func renameToken(line sshconf.Line, old, new string) string {
	body := strings.TrimRight(line.Raw, "\r\n")
	term := line.Raw[len(body):]
	// Args was right-trimmed with unicode.IsSpace; the body must be too, or
	// a trailing \v or no-break space shifts the splice into the alias.
	start := len(strings.TrimRightFunc(body, unicode.IsSpace)) - len(line.Args)
	return body[:start] + new + body[start+len(old):] + term
}

// CacheRename is one state-cache file that follows a rename.
type CacheRename struct{ From, To string }

// CacheRenames lists the state-cache files that follow a rename, for the
// caller to move: the tunnel record (<host>.json) and the observed stats
// (<host>.stats.json), both under stateDir. Pure; a missing From is the
// caller's to skip. Nil when either alias is not a plain file name — tunnel
// and stats refuse to write such a file, so there is nothing to move, and
// nothing may walk out of stateDir.
func CacheRenames(stateDir, old, new string) []CacheRename {
	for _, a := range []string{old, new} {
		if a == "" || a == "." || a == ".." || a != filepath.Base(a) {
			return nil
		}
	}
	var out []CacheRename
	for _, suffix := range []string{".json", ".stats.json"} {
		out = append(out, CacheRename{
			From: filepath.Join(stateDir, old+suffix),
			To:   filepath.Join(stateDir, new+suffix),
		})
	}
	return out
}
