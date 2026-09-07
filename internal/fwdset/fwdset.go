// Package fwdset declares durable forward sets: it adds exactly one
// `# @sasash forwards <name> <spec>...` line to a host's own meta run in
// sasash.conf (CONTRACT-fwdset), rewrites one spec of it, or removes the
// line again. It writes DESIRE only — starting the tunnel is space / `+` /
// `sasash up`'s job — and follows the write invariants: re-read at call
// time, atomic write, byte-identity outside the touched line.
package fwdset

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

// Add inserts the set's meta line immediately above the `Host <alias>`
// line in the file at confPath. By the attachment rule the unbroken
// comment run above a Host line belongs to that host, so the line lands
// in the host's own meta run whether one exists already or not, and the
// previous block's OwnMeta cannot claim it.
func Add(confPath, alias, name string, specs []string) error {
	if err := validate(name, specs); err != nil {
		return err
	}
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return err
	}
	h, ok := f.Host(alias)
	if !ok {
		return fmt.Errorf("host %q not managed by sasash", alias)
	}
	for _, m := range h.OwnMetaVerb("forwards") {
		if len(m.Args) > 0 && strings.EqualFold(m.Args[0], name) {
			return fmt.Errorf("forward set %q already declared for %q", name, alias)
		}
	}
	line := "# @sasash forwards " + name + " " + strings.Join(specs, " ") + lineTerm(f.Line(h.Start).Raw)
	raw := make([]string, 0, f.Len()+1)
	for i := 0; i < f.Len(); i++ {
		if i == h.Start {
			raw = append(raw, line)
		}
		raw = append(raw, f.Line(i).Raw)
	}
	out := sshconf.Parse(confPath, []byte(strings.Join(raw, "")))
	out.Perm = f.Perm
	return out.WriteAtomic(confPath)
}

// validate rejects what cannot be written as one meta line, or what the
// engine could never verify (CONTRACT-tunnel §5: a spec needs an all-digit
// local port among its first two ':'-fields).
func validate(name string, specs []string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n#") {
		return fmt.Errorf("forward set name %q: want a single word", name)
	}
	if len(specs) == 0 {
		return fmt.Errorf("forward set %q: no specs", name)
	}
	seen := make(map[string]bool)
	for _, s := range specs {
		if strings.ContainsAny(s, " \t\r\n") || !hasLocalPort(s) {
			return fmt.Errorf("spec %q: no local port", s)
		}
		if seen[s] {
			return fmt.Errorf("spec %q repeated", s)
		}
		seen[s] = true
	}
	return nil
}

func hasLocalPort(spec string) bool {
	fields := strings.SplitN(spec, ":", 3)
	for i := 0; i < len(fields) && i < 2; i++ {
		if fields[i] != "" && strings.Trim(fields[i], "0123456789") == "" {
			return true
		}
	}
	return false
}

// lineTerm is the Host line's own terminator, so the inserted line
// matches the file's style; a last line without one gets a newline.
func lineTerm(raw string) string {
	switch {
	case strings.HasSuffix(raw, "\r\n"):
		return "\r\n"
	default:
		return "\n"
	}
}

// Update rewrites one spec of an existing set in place (CONTRACT-fwdset
// A1): the token equal to old on the set's own meta line becomes new;
// every other byte of the line — and of the file — survives verbatim.
// Config only: a live forward is the caller's to re-plumb.
func Update(confPath, alias, name, old, new string) error {
	if old == new {
		return fmt.Errorf("spec %q unchanged", old)
	}
	if err := validate(name, []string{new}); err != nil {
		return err
	}
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return err
	}
	h, ok := f.Host(alias)
	if !ok {
		return fmt.Errorf("host %q not managed by sasash", alias)
	}
	line, ambiguous := declaringLine(h.OwnMetaVerb("forwards"), name)
	if ambiguous {
		return fmt.Errorf("forward set %q on %q is ambiguous: several lines declare it under different casing", name, alias)
	}
	if line == nil || !contains(line.Args[1:], old) {
		return fmt.Errorf("forward set %q on %q has no spec %q", name, alias, old)
	}
	if contains(line.Args[1:], new) {
		return fmt.Errorf("spec %q already in set %q", new, name)
	}
	raw := replaceSpecToken(f.Line(line.Index).Raw, old, new)
	rawLines := make([]string, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		if i == line.Index {
			rawLines = append(rawLines, raw)
			continue
		}
		rawLines = append(rawLines, f.Line(i).Raw)
	}
	out := sshconf.Parse(confPath, []byte(strings.Join(rawLines, "")))
	out.Perm = f.Perm
	return out.WriteAtomic(confPath)
}

// Remove deletes the one meta line declaring the set (CONTRACT-fwdset
// A2): the line goes whole, terminator included; every other byte of
// the file survives verbatim. Config only: a live set is the caller's to
// bring down first. A second line declaring the same set stays, so the
// reader then sees that line's specs — what the file said all along.
func Remove(confPath, alias, name string) error {
	f, err := sshconf.ParseFile(confPath)
	if err != nil {
		return err
	}
	h, ok := f.Host(alias)
	if !ok {
		return fmt.Errorf("host %q not managed by sasash", alias)
	}
	line, ambiguous := declaringLine(h.OwnMetaVerb("forwards"), name)
	if ambiguous {
		return fmt.Errorf("forward set %q on %q is ambiguous: several lines declare it under different casing", name, alias)
	}
	if line == nil {
		return fmt.Errorf("forward set %q not declared for %q", name, alias)
	}
	rawLines := make([]string, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		if i == line.Index {
			continue
		}
		rawLines = append(rawLines, f.Line(i).Raw)
	}
	out := sshconf.Parse(confPath, []byte(strings.Join(rawLines, "")))
	out.Perm = f.Perm
	return out.WriteAtomic(confPath)
}

// declaringLine is the FIRST line declaring the set (first-value-wins,
// as the reader). The reader keys sets on the exact name, so "WEB" and
// "web" are two sets: the exact match wins; a case-insensitive match is
// taken only when it is the single candidate, so a rewrite can never
// land on a set the user did not edit — several folded candidates and
// no exact one is ambiguous.
func declaringLine(metas []sshconf.Meta, name string) (line *sshconf.Meta, ambiguous bool) {
	var folded *sshconf.Meta
	foldedCount := 0
	for i := range metas {
		m := &metas[i]
		if len(m.Args) == 0 {
			continue
		}
		if m.Args[0] == name {
			return m, false
		}
		if strings.EqualFold(m.Args[0], name) {
			foldedCount++
			if folded == nil {
				folded = m
			}
		}
	}
	switch foldedCount {
	case 0:
		return nil, false
	case 1:
		return folded, false
	}
	return nil, true
}

func contains(specs []string, spec string) bool {
	for _, s := range specs {
		if s == spec {
			return true
		}
	}
	return false
}

// replaceSpecToken swaps the first whole token equal to old that follows
// the "forwards <name>" tokens, leaving every other byte in place.
func replaceSpecToken(raw, old, new string) string {
	pastName := 0 // whole tokens seen after the "forwards" verb
	for _, span := range tokenRe.FindAllStringIndex(raw, -1) {
		tok := raw[span[0]:span[1]]
		switch {
		case pastName == 0 && strings.EqualFold(tok, "forwards"):
			pastName = 1
		case pastName == 1:
			pastName = 2 // the set name
		case pastName == 2 && tok == old:
			return raw[:span[0]] + new + raw[span[1]:]
		}
	}
	return raw
}

var tokenRe = regexp.MustCompile(`\S+`)
