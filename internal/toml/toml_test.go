package toml_test

import (
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/toml"
)

func TestParseValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		want     toml.Value
		wantRest string
		wantErr  string
	}{
		{name: "basic string", in: `"nord" # c`, want: toml.Value{Kind: toml.String, Str: "nord"}, wantRest: " # c"},
		{name: "literal string", in: `'code -w'`, want: toml.Value{Kind: toml.String, Str: "code -w"}},
		{name: "escapes", in: `"a\"b\\c\n\t"`, want: toml.Value{Kind: toml.String, Str: "a\"b\\c\n\t"}},
		{name: "true", in: "true", want: toml.Value{Kind: toml.Bool, Bool: true}},
		{name: "false with comment", in: "false # off", want: toml.Value{Kind: toml.Bool, Bool: false}, wantRest: " # off"},
		{name: "true in inline table", in: "true}", want: toml.Value{Kind: toml.Bool, Bool: true}, wantRest: "}"},
		{name: "trueish is not a bool", in: "trueish", wantErr: "unsupported value"},
		{name: "bare word", in: "nord", wantErr: "unsupported value"},
		{name: "integer", in: "42", wantErr: "strings and true/false only"},
		{name: "empty", in: "", wantErr: "unsupported value"},
		{name: "unterminated", in: `"x`, wantErr: `unterminated " string`},
		{name: "unsupported escape", in: `"\u0041"`, wantErr: `unsupported escape \u`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, rest, err := toml.ParseValue(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseValue(%q) err = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseValue(%q) err = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseValue(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			if rest != tt.wantRest {
				t.Errorf("ParseValue(%q) rest = %q, want %q", tt.in, rest, tt.wantRest)
			}
		})
	}
}

func TestParseString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		want     string
		wantRest string
		wantErr  string
	}{
		{name: "basic", in: `"a b" rest`, want: "a b", wantRest: " rest"},
		{name: "literal keeps backslashes", in: `'a\n' rest`, want: `a\n`, wantRest: " rest"},
		{name: "empty input", in: "", wantErr: "expected a string value"},
		{name: "unterminated literal", in: "'x", wantErr: "unterminated ' string"},
		{name: "trailing backslash", in: `"x\`, wantErr: `unterminated " string`},
		{name: "not a string", in: "42", wantErr: "strings only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, rest, err := toml.ParseString(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseString(%q) err = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseString(%q) err = %v", tt.in, err)
			}
			if got != tt.want || rest != tt.wantRest {
				t.Errorf("ParseString(%q) = %q, %q, want %q, %q", tt.in, got, rest, tt.want, tt.wantRest)
			}
		})
	}
}

func TestQuote_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "nord", "code -w", `a"b`, `c:\dir`, "two\nlines\twide", "# not a comment"} {
		q := toml.Quote(s)
		got, rest, err := toml.ParseString(q)
		if err != nil || rest != "" || got != s {
			t.Errorf("ParseString(Quote(%q) = %s) = %q, %q, %v; want the input back", s, q, got, rest, err)
		}
	}
}

func TestBareKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "snake", in: "show_hidden", want: true},
		{name: "kebab and digits", in: "k-1", want: true},
		{name: "empty", in: "", want: false},
		{name: "dotted", in: "a.b", want: false},
		{name: "space", in: "a b", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := toml.BareKey(tt.in); got != tt.want {
				t.Errorf("BareKey(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestTrailingOK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty", in: "", want: true},
		{name: "spaces", in: "   ", want: true},
		{name: "comment", in: "  # why", want: true},
		{name: "junk", in: " x", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := toml.TrailingOK(tt.in); got != tt.want {
				t.Errorf("TrailingOK(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
