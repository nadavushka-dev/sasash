package sshconf

// This file resolves Include directives: enumerating hosts across a config
// file and everything it Includes, the way ssh evaluates them. Read-only — no
// write ever flows through include resolution; sasash writes exactly one file
// (SPEC §2).

import (
	"os"
	"path/filepath"
	"strings"
)

// IncludeHosts returns the Host blocks of the config at rootPath plus every
// file it Includes, recursively, in ssh evaluation order: an Include is
// expanded at the point it appears, so first-value-wins resolution over the
// returned slice matches ssh's.
//
// home resolves "~/" prefixes and relative Include patterns, which ssh takes
// relative to home/.ssh; home == "" means os.UserHomeDir(). Glob patterns are
// allowed. A pattern that matches nothing is silently skipped, as ssh does; a
// matched file that cannot be read is an error. Each file is loaded at most
// once, which also makes include cycles harmless.
func IncludeHosts(rootPath, home string) ([]Host, error) {
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = h
	}
	return includeHosts(rootPath, home, make(map[string]bool))
}

func includeHosts(path, home string, visited map[string]bool) ([]Host, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if visited[abs] {
		return nil, nil
	}
	visited[abs] = true

	f, err := ParseFile(path)
	if err != nil {
		return nil, err
	}

	// Walk lines once, emitting each Host block at its Host line and splicing
	// included files in at their Include line. A Host block that spans an
	// Include still comes first: its Host line does.
	hosts := f.Hosts()
	var out []Host
	next := 0
	for i := 0; i < f.Len(); i++ {
		if next < len(hosts) && hosts[next].Start == i {
			out = append(out, hosts[next])
			next++
			continue
		}
		line := f.Line(i)
		if line.Kind != KindDirective || !strings.EqualFold(line.Keyword, "include") {
			continue
		}
		for _, pattern := range line.Fields {
			matches, err := expandPattern(pattern, home)
			if err != nil {
				return nil, err
			}
			for _, match := range matches {
				sub, err := includeHosts(match, home, visited)
				if err != nil {
					return nil, err
				}
				out = append(out, sub...)
			}
		}
	}
	return out, nil
}

// expandPattern turns one Include argument into the files it names, applying
// ssh's path rules: "~" is home, a relative pattern is relative to home/.ssh.
// Directories a glob happens to match are dropped.
func expandPattern(pattern, home string) ([]string, error) {
	switch {
	case pattern == "~":
		pattern = home
	case strings.HasPrefix(pattern, "~/"):
		pattern = filepath.Join(home, pattern[2:])
	case !filepath.IsAbs(pattern):
		pattern = filepath.Join(home, ".ssh", pattern)
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	files := matches[:0]
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() {
			continue
		}
		files = append(files, m)
	}
	return files, nil
}
