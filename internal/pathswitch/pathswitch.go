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
	for i, p := range named {
		if p.Host == cur {
			curIdx = i
			res.From = p
			break
		}
	}

	if len(named) < 2 {
		res.To = res.From
		return res, nil
	}

	if pathName == "" {
		res.To = named[(curIdx+1)%len(named)] // curIdx -1 (no match) lands on 0
	} else {
		idx := -1
		for i, p := range named {
			if p.Name == pathName {
				idx = i
				break
			}
		}
		if idx < 0 {
			names := make([]string, len(named))
			for i, p := range named {
				names[i] = p.Name
			}
			return Result{}, fmt.Errorf("host %q has no path %q (have %s)", alias, pathName, strings.Join(names, ", "))
		}
		res.To = named[idx]
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
// (f's line indexes moved if Set inserted a HostName line). Existing variant
// blocks are rewritten in place, blocks for dropped path names are removed,
// and missing ones are inserted directly after the host's last directive (or
// after the last existing variant), so comment runs that lead into a later
// Host line keep leading into it.
func regenVariants(f *sshconf.File, alias string, named []probe.Path) *sshconf.File {
	f2 := sshconf.Parse(f.Path, f.Bytes())
	main, _ := f2.Host(alias) // present: switchPath found it before the rewrite

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
	lastVariantEnd := -1
	for _, b := range f2.Hosts() {
		suffix := strings.TrimPrefix(b.Name(), prefix)
		if suffix == b.Name() || suffix == "" {
			continue
		}
		end := directivesEnd(f2, &b)
		var repl []string // nil removes a stale variant
		for _, p := range named {
			if p.Name == suffix {
				repl = gen(p)
				have[suffix] = true
				break
			}
		}
		edits = append(edits, edit{b.Start, end, repl})
		if end > lastVariantEnd {
			lastVariantEnd = end
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
		insertAt := lastVariantEnd
		if insertAt < 0 {
			// First variants for this host: a separating blank line and the
			// SPEC §2 header, straight after the block's directives.
			insertAt = directivesEnd(f2, main)
			block = append(block, term, Header+term)
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

// directivesEnd returns the index just past the block's last directive line —
// the point where generated content may be inserted without crossing comment
// runs that belong to whatever follows.
func directivesEnd(f *sshconf.File, h *sshconf.Host) int {
	end := h.Start + 1
	for i := h.Start + 1; i < h.End; i++ {
		if f.Line(i).Kind == sshconf.KindDirective {
			end = i + 1
		}
	}
	return end
}
