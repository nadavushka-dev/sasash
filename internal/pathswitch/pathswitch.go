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

// Header precedes a generated variant group (SPEC §2). It is not cosmetic:
// anchoredStarts uses this exact line as the on-disk ownership token, so
// changing the text — even the dash — silently unmanages every group already
// written to users' configs; it cannot change without a migration. A test
// pins it against SPEC.md's literal.
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
	// Unmanaged lists live path names whose "<alias>.<name>" block was left
	// as-is because the generator does not own it — hand-edited, shadowed by
	// a user block, or below a break in the generated group. Those pinned
	// variants may hold stale addresses sasash will not correct; callers
	// should surface them ("N pinned variants left as-is"). Nil when every
	// variant is managed — and always nil when Changed is false, since a
	// no-op inspects nothing; check Changed before reading nil as "all good".
	Unmanaged []string
}

// Cycle advances alias to its next named path: the one after the path whose
// addr equals the current HostName, wrapping. If no path matches (the file
// was hand-edited to an unknown addr), the first named path becomes active.
// When several named paths share the current addr, Result.From reports the
// last of them — the file stores only the addr, so the specific twin is
// unknowable; callers should not render From as authoritative in that case.
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
		if len(named) == 0 {
			return Result{}, fmt.Errorf("host %q has no named paths", alias)
		}
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

	out, unmanaged := regenVariants(f, alias, named)
	if err := out.WriteAtomic(confPath); err != nil {
		return Result{}, err
	}
	res.Changed = true
	res.Unmanaged = unmanaged
	return res, nil
}

// regenVariants rebuilds the pinned "<alias>.<name>" blocks on a reparse of f
// (f's line indexes moved if Set inserted a HostName line). Only blocks the
// generator provably wrote — generated shape AND anchored to its own Header
// line, see anchoredStarts — are rewritten in place or, when their path name
// is gone, removed; every other block is left verbatim, and one that shadows
// a live path name also suppresses the duplicate (the user's block is
// authoritative under first-value-wins). A fresh group is inserted at the
// end of the host's block, stopping short of a comment run that leads into a
// following Host line so that host keeps its meta; appends to an existing
// group stay contiguous with it. unmanaged reports the live path names whose
// blocks were left as-is, in file order.
func regenVariants(f *sshconf.File, alias string, named []probe.Path) (out *sshconf.File, unmanaged []string) {
	f2 := sshconf.Parse(f.Path, f.Bytes())
	main, ok := f2.Host(alias)
	if !ok {
		// Unreachable: switchPath found the block before the rewrite. Keep
		// the HostName edit rather than dereferencing nil if that changes.
		return f, nil
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
	anchored := anchoredStarts(f2, prefix)
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
		if !anchored[b.Start] {
			// A real host that happens to share the prefix, a variant the
			// user extended by hand, or any block outside the region the
			// generator provably wrote: never rewrite, never delete. When
			// it shadows a live path name, skip generating a duplicate —
			// first-value-wins makes the user's block authoritative — and
			// report it so the caller can say the pin was left as-is.
			// The !have guard is not just dedup: walking in file order, it
			// attributes each name to the block ssh actually resolves, so a
			// managed block ABOVE a foreign duplicate reports nothing (ssh
			// uses the managed one) while a foreign block above a managed
			// one reports the name. Do not "simplify" it away.
			if live && !have[suffix] {
				have[suffix] = true
				unmanaged = append(unmanaged, suffix)
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
		// Append point for missing variants: right after this block's last
		// directive, so the generated group stays contiguous and a user note
		// below it stays below.
		if e := directivesEnd(f2, &b); e > lastOwnedEnd {
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

	out = sshconf.Parse(f.Path, []byte(strings.Join(raw, "")))
	out.Perm = f.Perm
	return out, unmanaged
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

// insertEnd returns where a freshly created variant group is inserted: the
// end of the host's block, minus a trailing comment run that leads straight
// into a following Host line — that run is the next host's meta (the boundary
// sshconf.OwnMeta draws), and in-block meta above it stays with h. A run
// glued to a following Match line is not walked back over (a Match owns no
// meta, so the run stays with its host either way).
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

// anchoredStarts returns the Start indexes of the blocks the generator
// provably wrote: runs of owned "<prefix><name>" blocks sitting exactly where
// the generator puts them — the first immediately below a Header comment
// line, each next immediately after the previous block's last directive. A
// generated shape alone is not enough (a hand-written minimal host is
// indistinguishable from one); position is the second witness. Any user
// insertion breaks the chain, demoting the blocks below it to unmanaged —
// fail-safe: a stale variant left behind, never a deleted user block.
func anchoredStarts(f *sshconf.File, prefix string) map[int]bool {
	hosts := f.Hosts()
	byStart := make(map[int]*sshconf.Host, len(hosts))
	for i := range hosts {
		byStart[hosts[i].Start] = &hosts[i]
	}
	out := make(map[int]bool)
	for i := 0; i < f.Len(); i++ {
		line := f.Line(i)
		if line.Kind != sshconf.KindComment || strings.TrimSpace(strings.TrimRight(line.Raw, "\r\n")) != Header {
			continue
		}
		for next := i + 1; ; {
			b, ok := byStart[next]
			if !ok {
				break
			}
			suffix := strings.TrimPrefix(b.Name(), prefix)
			if suffix == b.Name() || suffix == "" || !generatorOwned(f, b) {
				break
			}
			out[b.Start] = true
			next = directivesEnd(f, b)
		}
	}
	return out
}

// generatorOwned reports whether a "<alias>.<name>" block has exactly the
// shape the generator emits: a single host pattern, directives that are
// nothing but HostName, User and Port, no comment between them, and no
// "# @sasash" meta anywhere in the block.
func generatorOwned(f *sshconf.File, h *sshconf.Host) bool {
	if len(h.Patterns) != 1 {
		return false
	}
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
