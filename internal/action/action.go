// Package action loads ~/.config/sasash/actions.toml and expands its command
// templates. It is pure parse + expand: it never starts a process (execution
// stays behind run.Runner / tea.ExecProcess on the consumer side) and never
// evaluates shell syntax — env values pass to the child verbatim.
package action

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Action is one entry from actions.toml, in file order.
type Action struct {
	Name string            // TOML table name, e.g. "rdp"
	Cmd  string            // raw template, verbatim from the file
	Env  map[string]string // optional; raw values, verbatim (never shell-evaluated)
}

// Subst carries the values for the four placeholders {host} {user} {addr}
// {port}. The caller resolves them from the host model.
type Subst struct{ Host, User, Addr, Port string }

// Invocation is ready for the executor.
type Invocation struct {
	Argv []string // Argv[0] is the tool; never empty on success
	Env  []string // "KEY=VALUE" entries to append to the parent environment; nil when none
}

// Path returns the actions.toml location under home.
func Path(home string) string {
	return filepath.Join(home, ".config", "sasash", "actions.toml")
}

// Expand tokenizes Cmd (quote-aware), substitutes placeholders in each token
// and in each env value, and returns the invocation. Tokenization happens
// BEFORE substitution: a value containing spaces stays one argv element, and
// quote characters inside values are data, never re-parsed.
func (a Action) Expand(s Subst) (Invocation, error) {
	words, err := tokenize(a.Cmd)
	if err != nil {
		return Invocation{}, fmt.Errorf("action %s: %w", a.Name, err)
	}
	if len(words) == 0 {
		return Invocation{}, fmt.Errorf("action %s: empty cmd", a.Name)
	}
	argv := make([]string, len(words))
	for i, w := range words {
		sub, err := substitute(w, s)
		if err != nil {
			return Invocation{}, fmt.Errorf("action %s: %w", a.Name, err)
		}
		argv[i] = sub
	}
	var env []string
	if len(a.Env) > 0 {
		keys := make([]string, 0, len(a.Env))
		for k := range a.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		env = make([]string, len(keys))
		for i, k := range keys {
			sub, err := substitute(a.Env[k], s)
			if err != nil {
				return Invocation{}, fmt.Errorf("action %s: env %s: %w", a.Name, k, err)
			}
			env[i] = k + "=" + sub
		}
	}
	return Invocation{Argv: argv, Env: env}, nil
}

// tokenize splits a command template into words on unquoted spaces and tabs.
// '…' is literal; "…" groups, recognizing the escapes \" and \\.
func tokenize(cmd string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch c {
		case ' ', '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case '\'':
			inWord = true
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("unterminated ' quote")
			}
			cur.WriteString(cmd[i+1 : i+1+end])
			i += end + 1
		case '"':
			inWord = true
			i++
			closed := false
			for ; i < len(cmd); i++ {
				if cmd[i] == '\\' && i+1 < len(cmd) && (cmd[i+1] == '"' || cmd[i+1] == '\\') {
					cur.WriteByte(cmd[i+1])
					i++
					continue
				}
				if cmd[i] == '"' {
					closed = true
					break
				}
				cur.WriteByte(cmd[i])
			}
			if !closed {
				return nil, fmt.Errorf(`unterminated " quote`)
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// substitute replaces {host} {user} {addr} {port} in one word; {{ and }}
// are escapes for literal braces (for brace-using tools like docker or awk).
// An unknown placeholder, an unclosed {, or a referenced placeholder whose
// Subst value is empty is an error — never a silently broken word.
func substitute(word string, s Subst) (string, error) {
	if !strings.Contains(word, "{") && !strings.Contains(word, "}") {
		return word, nil
	}
	var out strings.Builder
	for i := 0; i < len(word); i++ {
		switch c := word[i]; c {
		case '}':
			if i+1 < len(word) && word[i+1] == '}' {
				i++
			}
			out.WriteByte('}')
		case '{':
			if i+1 < len(word) && word[i+1] == '{' {
				out.WriteByte('{')
				i++
				continue
			}
			clos := strings.IndexByte(word[i+1:], '}')
			if clos < 0 {
				return "", fmt.Errorf("unclosed placeholder in %q", word)
			}
			name := word[i+1 : i+1+clos]
			i += clos + 1
			var val string
			switch name {
			case "host":
				val = s.Host
			case "user":
				val = s.User
			case "addr":
				val = s.Addr
			case "port":
				val = s.Port
			default:
				return "", fmt.Errorf("unknown placeholder {%s} in %q", name, word)
			}
			if val == "" {
				return "", fmt.Errorf("placeholder {%s} is empty in %q", name, word)
			}
			out.WriteString(val)
		default:
			out.WriteByte(c)
		}
	}
	return out.String(), nil
}
