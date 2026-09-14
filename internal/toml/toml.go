// Package toml holds the TOML subset sasash reads: the scalar syntax
// shared by actions.toml (internal/action) and config.toml
// (internal/config). It is deliberately not a TOML implementation — no
// arrays, no multiline strings, no dotted keys — because both files are
// read by hand as much as by sasash, and a rejected line must name its
// reason. Line structure (tables, key = value) stays with each caller;
// this package parses one value at a time.
package toml

import (
	"fmt"
	"strings"
)

// Kind is the type of a parsed scalar.
type Kind int

const (
	String Kind = iota
	Bool
)

// Value is one parsed scalar.
type Value struct {
	Kind Kind
	Str  string // Kind == String
	Bool bool   // Kind == Bool
}

// ParseValue consumes one scalar at the start of s — a string or a bool —
// and returns the value and the unconsumed remainder. It is ParseString
// widened by the bool literals; the string message stays ParseString's
// for a string-only caller.
func ParseValue(s string) (Value, string, error) {
	switch {
	case strings.HasPrefix(s, "true") && endsValue(s[4:]):
		return Value{Kind: Bool, Bool: true}, s[4:], nil
	case strings.HasPrefix(s, "false") && endsValue(s[5:]):
		return Value{Kind: Bool, Bool: false}, s[5:], nil
	}
	if s == "" || (s[0] != '"' && s[0] != '\'') {
		return Value{}, "", fmt.Errorf("unsupported value %q (strings and true/false only)", s)
	}
	str, rest, err := ParseString(s)
	if err != nil {
		return Value{}, "", err
	}
	return Value{Kind: String, Str: str}, rest, nil
}

// endsValue reports whether rest can follow a bare literal: nothing, a
// space, a comma, a closing brace or a comment — so "trueish" is not true.
func endsValue(rest string) bool {
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ' ', '\t', '\r', ',', '}', '#':
		return true
	}
	return false
}

// Quote renders s as a TOML basic string, escaping what ParseString
// unescapes — so Quote and ParseString round-trip.
func Quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// ParseString consumes one "basic" or 'literal' string at the start of s and
// returns the value and the unconsumed remainder. Basic strings recognize the
// escapes \\ \" \n \t; any other escape is unsupported.
func ParseString(s string) (val, rest string, err error) {
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

// BareKey reports whether s is a TOML bare key: letters, digits, _ and -.
func BareKey(s string) bool {
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

// TrailingOK allows only whitespace or a comment after a parsed value.
func TrailingOK(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s[0] == '#'
}
