// Package pathswitch implements sasash's first real write path (SPEC §3,
// SASASH.2.2): making one of a host's named paths active by rewriting the
// HostName line in sasash.conf, and regenerating the pinned "<alias>.<path>"
// variant blocks (SPEC §2). Every ssh-aware tool on the machine follows the
// rewrite immediately.
//
// Both entry points take the config file's path and re-read it at call time —
// a hand-edited file is never fought — and write through sshconf's atomic
// writer. Outside the HostName line and the variant region the output is
// byte-for-byte the input. Contract: .squad/CONTRACT-pathswitch.md.
package pathswitch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// Header precedes a freshly generated variant group, from SPEC §2.
const Header = "# pinned variants, generated — for when you want to force one path"

// Result describes one path-switch outcome.
type Result struct {
	Alias string
	// From is the path active before the switch; the zero Path when the
	// current HostName matched no named path.
	From probe.Path
	// To is the path active after the switch. Equal to From when Changed
	// is false.
	To probe.Path
	// Changed is false when the host has fewer than two named paths; no
	// write happens then, and the caller renders the "only one path" note.
	Changed bool
}

// Cycle advances alias to its next named path: the one after the path whose
// addr equals the current HostName, wrapping. If no path matches (the file
// was hand-edited to an unknown addr), the first named path becomes active.
func Cycle(confPath, alias string) (Result, error) {
	return switchPath(confPath, alias, "")
}

// Set makes the named path active for alias. Setting the already-active path
// still counts as a change: the variants regenerate and the file is written.
func Set(confPath, alias, pathName string) (Result, error) {
	if pathName == "" {
		return Result{}, fmt.Errorf("pathswitch: empty path name for host %q", alias)
	}
	return switchPath(confPath, alias, pathName)
}

// switchPath is the shared read-modify-write; pathName == "" means cycle.
func switchPath(confPath, alias, pathName string) (Result, error) {
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return Result{}, err
	}
	h, ok := f.Host(alias)
	if !ok {
		return Result{}, fmt.Errorf("host %q is not managed by sasash: no Host block in %s", alias, confPath)
	}

	// Meta warnings never block a switch; the valid paths are enough.
	model, _ := hostmodel.Build(h)
	var named []probe.Path
	for _, p := range model.Paths {
		if p.Name != "" {
			named = append(named, p)
		}
	}

	res := Result{Alias: alias}
	cur, _ := h.Get("HostName")
	curIdx := -1
	// Last match, not first: when several named paths share the current addr,
	// cycling must advance past the whole run instead of stalling inside it.
	for i, p := range named {
		if strings.EqualFold(p.Host, cur) {
			curIdx = i
			res.From = p
		}
	}

	// Validate Set's argument before the small-host no-op so a bogus path
	// name never succeeds silently.
	setIdx := -1
	if pathName != "" {
		for i, p := range named {
			if p.Name == pathName {
				setIdx = i
				break
			}
		}
		if setIdx < 0 {
			names := make([]string, len(named))
			for i, p := range named {
				names[i] = p.Name
			}
			return Result{}, fmt.Errorf("host %q has no path %q (have %s)", alias, pathName, strings.Join(names, ", "))
		}
	}

	if len(named) < 2 {
		res.To = res.From
		return res, nil
	}

	if pathName == "" {
		res.To = named[(curIdx+1)%len(named)] // curIdx -1 (no match) lands on 0
	} else {
		res.To = named[setIdx]
	}

	h.Set("HostName", res.To.Host)

	out := regenVariants(f, alias, named)
	if err := out.WriteAtomic(confPath); err != nil {
		return Result{}, err
	}
	res.Changed = true
	return res, nil
}

// regenVariants rebuilds the pinned "<alias>.<name>" blocks on a reparse of f
// (f's line indexes moved if Set inserted a HostName line). Only blocks the
// generator provably owns — nothing in them but HostName, User and Port —
// are rewritten in place or, when their path name is gone, removed; a real
// host that merely shares the "<alias>." prefix is left verbatim, and if it
// shadows a live path name no duplicate block is generated for it (the
// user's block is authoritative under first-value-wins). Missing variants
// are inserted at the end of the host's block, stopping short of a comment
// run that leads into a following Host line so that host keeps its meta.
func regenVariants(f *sshconf.File, alias string, named []probe.Path) *sshconf.File {
	f2 := sshconf.Parse(f.Path, f.Bytes())
	main, ok := f2.Host(alias)
	if !ok {
		// Unreachable: switchPath found the block before the rewrite. Keep
		// the HostName edit rather than dereferencing nil if that changes.
		return f
	}

	term := "\n"
	if f2.Len() > 0 && strings.HasSuffix(f2.Line(0).Raw, "\r\n") {
		term = "\r\n"
	}
	user, hasUser := main.Get("User")
	gen := func(p probe.Path) []string {
		lines := []string{
			"Host " + alias + "." + p.Name + term,
			"  HostName " + p.Host + term,
		}
		if hasUser {
			lines = append(lines, "  User "+user+term)
		}
		if p.Port != "" {
			lines = append(lines, "  Port "+p.Port+term)
		}
		return lines
	}

	raw := make([]string, f2.Len())
	for i := range raw {
		raw[i] = f2.Line(i).Raw
	}

	// A variant's rewrite range is Host line through last directive; trailing
	// comments and blank lines between blocks are not touched.
	type edit struct {
		start, end int
		repl       []string
	}
	var edits []edit
	have := make(map[string]bool)
	prefix := alias + "."
	lastOwnedEnd := -1
	for _, b := range f2.Hosts() {
		suffix := strings.TrimPrefix(b.Name(), prefix)
		if suffix == b.Name() || suffix == "" {
			continue
		}
		live := false
		for _, p := range named {
			if p.Name == suffix {
				live = true
				break
			}
		}
		if !generatorOwned(f2, &b) {
			// A real host that happens to share the prefix, or a variant the
			// user extended by hand: never rewrite, never delete. When it
			// shadows a live path name, skip generating a duplicate.
			if live {
				have[suffix] = true
			}
			continue
		}
		var repl []string // nil removes a stale variant
		if live {
			for _, p := range named {
				if p.Name == suffix {
					repl = gen(p)
					break
				}
			}
			have[suffix] = true
		}
		edits = append(edits, edit{b.Start, directivesEnd(f2, &b), repl})
		if e := insertEnd(f2, &b); e > lastOwnedEnd {
			lastOwnedEnd = e
		}
	}

	var missing []string
	for _, p := range named {
		if !have[p.Name] {
			missing = append(missing, gen(p)...)
		}
	}
	if len(missing) > 0 {
		var block []string
		insertAt := lastOwnedEnd
		if insertAt < 0 {
			// First variants for this host: the SPEC §2 header at the end of
			// the block, with a separating blank unless one is already there.
			insertAt = insertEnd(f2, main)
			if insertAt > 0 && f2.Line(insertAt-1).Kind != sshconf.KindBlank {
				block = append(block, term)
			}
			block = append(block, Header+term)
		}
		if insertAt > 0 && !strings.HasSuffix(raw[insertAt-1], "\n") {
			// The line above the insertion point ends the file without a
			// newline; terminate it or the new block runs onto it.
			edits = append(edits, edit{insertAt - 1, insertAt, []string{raw[insertAt-1] + term}})
		}
		edits = append(edits, edit{insertAt, insertAt, append(block, missing...)})
	}

	// Highest start first, insertion before a replacement ending at the same
	// index: lower indexes stay valid while each edit is applied.
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start > edits[j].start
		}
		return edits[i].end > edits[j].end
	})
	for _, e := range edits {
		raw = append(raw[:e.start], append(append([]string(nil), e.repl...), raw[e.end:]...)...)
	}

	out := sshconf.Parse(f.Path, []byte(strings.Join(raw, "")))
	out.Perm = f.Perm
	return out
}

// directivesEnd returns the index just past the block's last directive line:
// the range a generated variant may be rewritten or removed over. Trailing
// comments and blanks are never part of it.
func directivesEnd(f *sshconf.File, h *sshconf.Host) int {
	end := h.Start + 1
	for i := h.Start + 1; i < h.End; i++ {
		if f.Line(i).Kind == sshconf.KindDirective {
			end = i + 1
		}
	}
	return end
}

// insertEnd returns where generated blocks may be inserted after h: the end
// of its block, minus a trailing comment run that leads straight into a
// following Host line — that run is the next host's meta (the boundary
// sshconf.OwnMeta draws), and in-block meta above it stays with h. A run
// glued to a following Match line is not walked back over (a Match owns no
// meta); meta sitting at the very bottom of a block directly against a Match
// line is a known residual edge.
func insertEnd(f *sshconf.File, h *sshconf.Host) int {
	end := h.End
	if end < f.Len() {
		next := f.Line(end)
		if next.Kind == sshconf.KindDirective && strings.EqualFold(next.Keyword, "Host") {
			for end > h.Start+1 {
				k := f.Line(end - 1).Kind
				if k != sshconf.KindComment && k != sshconf.KindMeta {
					break
				}
				end--
			}
		}
	}
	return end
}

// generatorOwned reports whether a "<alias>.<name>" block is provably the
// generator's to rewrite or remove: its directives are nothing but HostName,
// User and Port, no comment sits between them, and no "# @sasash" meta lives
// anywhere in the block. Anything else means the user's hand has been in it.
func generatorOwned(f *sshconf.File, h *sshconf.Host) bool {
	dirEnd := directivesEnd(f, h)
	for i := h.Start + 1; i < h.End; i++ {
		switch line := f.Line(i); line.Kind {
		case sshconf.KindMeta:
			return false
		case sshconf.KindComment:
			if i < dirEnd {
				return false
			}
		case sshconf.KindDirective:
			switch strings.ToLower(line.Keyword) {
			case "hostname", "user", "port":
			default:
				return false
			}
		}
	}
	return true
}
