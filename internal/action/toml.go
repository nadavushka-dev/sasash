package action

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Load parses actions.toml under home. A missing file is (nil, nil): a fresh
// install has no actions and no error. A malformed or unsupported file is
// (nil, *ParseError) — never a panic, never a partial result.
//
// Load accepts exactly the SPEC §6 shape and nothing more: [name] tables,
// cmd = "basic string" or 'literal string', env = { KEY = "value", … },
// comments, blank lines. Arrays, multiline strings, dotted or nested tables,
// and non-string values are ParseErrors naming the line.
func Load(home string) ([]Action, error) {
	data, err := os.ReadFile(Path(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseActions(string(data))
}

// ParseError reports a rejected actions.toml with the 1-based line.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("actions.toml:%d: %s", e.Line, e.Msg)
}

func parseActions(data string) ([]Action, error) {
	var actions []Action
	cur := -1 // index into actions of the open table
	seen := map[string]bool{}
	for n, line := range strings.Split(data, "\n") {
		ln := n + 1
		s := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if s == "" || s[0] == '#' {
			continue
		}
		if s[0] == '[' {
			// Table names are bare-key only, so a # on a header line can
			// only start a trailing comment.
			if i := strings.IndexByte(s, '#'); i >= 0 {
				s = strings.TrimSpace(s[:i])
			}
			if !strings.HasSuffix(s, "]") {
				return nil, &ParseError{ln, "malformed table header"}
			}
			name := strings.TrimSpace(s[1 : len(s)-1])
			if !bareKey(name) {
				return nil, &ParseError{ln, fmt.Sprintf("unsupported table name %q (bare names only)", name)}
			}
			if seen[name] {
				return nil, &ParseError{ln, fmt.Sprintf("duplicate table [%s]", name)}
			}
			seen[name] = true
			actions = append(actions, Action{Name: name})
			cur = len(actions) - 1
			continue
		}
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			return nil, &ParseError{ln, fmt.Sprintf("expected key = value, got %q", s)}
		}
		key = strings.TrimSpace(key)
		rest = strings.TrimSpace(rest)
		if !bareKey(key) {
			return nil, &ParseError{ln, fmt.Sprintf("unsupported key %q (bare keys only)", key)}
		}
		if cur < 0 {
			return nil, &ParseError{ln, fmt.Sprintf("key %q outside any [table]", key)}
		}
		a := &actions[cur]
		switch key {
		case "cmd":
			if a.Cmd != "" {
				return nil, &ParseError{ln, fmt.Sprintf("duplicate key cmd in [%s]", a.Name)}
			}
			val, rem, err := parseString(rest)
			if err != nil {
				return nil, &ParseError{ln, err.Error()}
			}
			if !trailingOK(rem) {
				return nil, &ParseError{ln, fmt.Sprintf("unexpected trailing content %q", rem)}
			}
			if strings.TrimSpace(val) == "" {
				return nil, &ParseError{ln, fmt.Sprintf("empty cmd in [%s]", a.Name)}
			}
			a.Cmd = val
		case "env":
			if a.Env != nil {
				return nil, &ParseError{ln, fmt.Sprintf("duplicate key env in [%s]", a.Name)}
			}
			env, err := parseInlineTable(rest)
			if err != nil {
				return nil, &ParseError{ln, err.Error()}
			}
			a.Env = env
		default:
			return nil, &ParseError{ln, fmt.Sprintf("unsupported key %q (only cmd and env)", key)}
		}
	}
	for _, a := range actions {
		if a.Cmd == "" {
			return nil, &ParseError{lineOfTable(data, a.Name), fmt.Sprintf("table [%s] has no cmd", a.Name)}
		}
	}
	return actions, nil
}

// parseString consumes one "basic" or 'literal' string at the start of s and
// returns the value and the unconsumed remainder. Basic strings recognize the
// escapes \\ \" \n \t; any other escape is unsupported.
func parseString(s string) (val, rest string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("expected a string value")
	}
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", "", fmt.Errorf("unterminated ' string")
		}
		return s[1 : 1+end], s[end+2:], nil
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				if i+1 >= len(s) {
					return "", "", fmt.Errorf(`unterminated " string`)
				}
				switch s[i+1] {
				case '\\':
					b.WriteByte('\\')
				case '"':
					b.WriteByte('"')
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					return "", "", fmt.Errorf(`unsupported escape \%c`, s[i+1])
				}
				i++
			case '"':
				return b.String(), s[i+1:], nil
			default:
				b.WriteByte(s[i])
			}
		}
		return "", "", fmt.Errorf(`unterminated " string`)
	default:
		return "", "", fmt.Errorf("unsupported value %q (strings only)", s)
	}
}

// parseInlineTable parses { KEY = "value", … } of string pairs, on one line.
func parseInlineTable(s string) (map[string]string, error) {
	if !strings.HasPrefix(s, "{") {
		return nil, fmt.Errorf("env must be an inline table { KEY = \"value\" }")
	}
	env := map[string]string{}
	s = strings.TrimSpace(s[1:])
	for {
		if s == "" {
			return nil, fmt.Errorf("unterminated inline table")
		}
		if s[0] == '}' {
			if !trailingOK(s[1:]) {
				return nil, fmt.Errorf("unexpected trailing content %q", s[1:])
			}
			return env, nil
		}
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("expected KEY = \"value\" in inline table")
		}
		key = strings.TrimSpace(key)
		if !bareKey(key) {
			return nil, fmt.Errorf("unsupported env key %q (bare keys only)", key)
		}
		if _, dup := env[key]; dup {
			return nil, fmt.Errorf("duplicate env key %q", key)
		}
		val, rem, err := parseString(strings.TrimSpace(rest))
		if err != nil {
			return nil, err
		}
		env[key] = val
		rem = strings.TrimSpace(rem)
		if strings.HasPrefix(rem, ",") {
			rem = strings.TrimSpace(rem[1:])
		} else if rem != "" && rem[0] != '}' {
			return nil, fmt.Errorf("expected , or } after env value, got %q", rem)
		}
		s = rem
	}
}

func bareKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// trailingOK allows only whitespace or a comment after a parsed value.
func trailingOK(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s[0] == '#'
}

// lineOfTable finds the header line for the missing-cmd error. Parsing has
// already succeeded structurally, so the header is guaranteed to exist.
func lineOfTable(data, name string) int {
	for n, line := range strings.Split(data, "\n") {
		s := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") && strings.TrimSpace(s[1:len(s)-1]) == name {
			return n + 1
		}
	}
	return 0
}
