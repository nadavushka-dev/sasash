package action_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/action"
)

func writeActions(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "sasash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "actions.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestPath_Layout(t *testing.T) {
	t.Parallel()
	got := action.Path("/home/x")
	want := filepath.Join("/home/x", ".config", "sasash", "actions.toml")
	if got != want {
		t.Errorf("Path() = %v, want %v", got, want)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()
	got, err := action.Load(t.TempDir())
	if got != nil || err != nil {
		t.Errorf("Load() = %v, %v, want nil, nil", got, err)
	}
}

func TestLoad_UnreadableFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// Path(home) exists but is a directory: ReadFile must surface a real error.
	if err := os.MkdirAll(action.Path(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := action.Load(home); err == nil {
		t.Errorf("Load() error = nil, want non-nil for unreadable file")
	}
}

func TestLoad_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    []action.Action
	}{
		{
			name: "spec example",
			content: "# ~/.config/sasash/actions.toml\n" +
				"[rdp]\n" +
				`cmd = "xfreerdp /u:{user} /v:{addr} /dynamic-resolution /cert:ignore"` + "\n" +
				`env = { DISPLAY = "$(launchctl getenv DISPLAY)" }` + "\n",
			want: []action.Action{{
				Name: "rdp",
				Cmd:  "xfreerdp /u:{user} /v:{addr} /dynamic-resolution /cert:ignore",
				Env:  map[string]string{"DISPLAY": "$(launchctl getenv DISPLAY)"},
			}},
		},
		{
			name: "file order preserved",
			content: "[zz]\ncmd = 'z'\n" +
				"[aa]\ncmd = 'a'\n",
			want: []action.Action{{Name: "zz", Cmd: "z"}, {Name: "aa", Cmd: "a"}},
		},
		{
			name:    "literal string and blank lines and crlf",
			content: "\r\n[vnc]\r\n\r\ncmd = 'vncviewer {addr}:{port}'\r\n",
			want:    []action.Action{{Name: "vnc", Cmd: "vncviewer {addr}:{port}"}},
		},
		{
			name:    "basic string escapes",
			content: "[e]\ncmd = \"a\\\\b \\\"c\\\" \\n \\t\"\n",
			want:    []action.Action{{Name: "e", Cmd: "a\\b \"c\" \n \t"}},
		},
		{
			name:    "trailing comment after value",
			content: "[c]\ncmd = 'x' # runs x\n",
			want:    []action.Action{{Name: "c", Cmd: "x"}},
		},
		{
			name:    "trailing comment after table header",
			content: "[rdp] # remote desktop\ncmd = 'x'\n",
			want:    []action.Action{{Name: "rdp", Cmd: "x"}},
		},
		{
			name:    "multi-pair env",
			content: "[m]\ncmd = 'x'\nenv = { B = \"2\", A = \"1\" }\n",
			want:    []action.Action{{Name: "m", Cmd: "x", Env: map[string]string{"A": "1", "B": "2"}}},
		},
		{
			name:    "empty env table",
			content: "[m]\ncmd = 'x'\nenv = { }\n",
			want:    []action.Action{{Name: "m", Cmd: "x", Env: map[string]string{}}},
		},
		{
			name:    "no trailing newline",
			content: "[n]\ncmd = 'x'",
			want:    []action.Action{{Name: "n", Cmd: "x"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := action.Load(writeActions(t, tt.content))
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLoad_Malformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		content  string
		wantLine int
		wantMsg  string
	}{
		{name: "malformed header", content: "[rdp\ncmd = 'x'\n", wantLine: 1, wantMsg: "malformed table header"},
		{name: "quoted table name", content: "[\"r p\"]\ncmd = 'x'\n", wantLine: 1, wantMsg: "bare names only"},
		{name: "duplicate table", content: "[a]\ncmd = 'x'\n[a]\ncmd = 'y'\n", wantLine: 3, wantMsg: "duplicate table [a]"},
		{name: "key outside table", content: "cmd = 'x'\n", wantLine: 1, wantMsg: "outside any [table]"},
		{name: "unsupported key", content: "[a]\ncmd = 'x'\nuser = 'y'\n", wantLine: 3, wantMsg: `unsupported key "user"`},
		{name: "dotted key", content: "[a]\nenv.K = 'x'\n", wantLine: 2, wantMsg: "bare keys only"},
		{name: "not key value", content: "[a]\njunk\n", wantLine: 2, wantMsg: "expected key = value"},
		{name: "duplicate cmd", content: "[a]\ncmd = 'x'\ncmd = 'y'\n", wantLine: 3, wantMsg: "duplicate key cmd"},
		{name: "duplicate env", content: "[a]\ncmd = 'x'\nenv = { A = \"1\" }\nenv = { B = \"2\" }\n", wantLine: 4, wantMsg: "duplicate key env"},
		{name: "non-string value", content: "[a]\ncmd = 42\n", wantLine: 2, wantMsg: "strings only"},
		{name: "array value", content: "[a]\ncmd = ['x']\n", wantLine: 2, wantMsg: "strings only"},
		{name: "unterminated basic string", content: "[a]\ncmd = \"x\n", wantLine: 2, wantMsg: `unterminated " string`},
		{name: "unterminated literal string", content: "[a]\ncmd = 'x\n", wantLine: 2, wantMsg: "unterminated ' string"},
		{name: "trailing backslash", content: "[a]\ncmd = \"x\\\n", wantLine: 2, wantMsg: `unterminated " string`},
		{name: "unsupported escape", content: "[a]\ncmd = \"\\u0041\"\n", wantLine: 2, wantMsg: `unsupported escape \u`},
		{name: "trailing junk after value", content: "[a]\ncmd = 'x' 'y'\n", wantLine: 2, wantMsg: "unexpected trailing content"},
		{name: "empty cmd", content: "[a]\ncmd = ''\n", wantLine: 2, wantMsg: "empty cmd"},
		{name: "whitespace-only cmd", content: "[a]\ncmd = '   '\n", wantLine: 2, wantMsg: "empty cmd"},
		{name: "missing value", content: "[a]\ncmd =\n", wantLine: 2, wantMsg: "expected a string value"},
		{name: "empty table name", content: "[]\ncmd = 'x'\n", wantLine: 1, wantMsg: `unsupported table name ""`},
		{name: "env unterminated value", content: "[a]\ncmd = 'x'\nenv = { A = '\n", wantLine: 3, wantMsg: "unterminated ' string"},
		{name: "missing cmd", content: "[a]\nenv = { A = \"1\" }\n[b]\ncmd = 'x'\n", wantLine: 1, wantMsg: "table [a] has no cmd"},
		{name: "env not inline table", content: "[a]\ncmd = 'x'\nenv = \"1\"\n", wantLine: 3, wantMsg: "inline table"},
		{name: "env unterminated table", content: "[a]\ncmd = 'x'\nenv = { A = \"1\"\n", wantLine: 3, wantMsg: "unterminated inline table"},
		{name: "env missing equals", content: "[a]\ncmd = 'x'\nenv = { A }\n", wantLine: 3, wantMsg: "expected KEY"},
		{name: "env bad key", content: "[a]\ncmd = 'x'\nenv = { \"A B\" = \"1\" }\n", wantLine: 3, wantMsg: "bare keys only"},
		{name: "env duplicate key", content: "[a]\ncmd = 'x'\nenv = { A = \"1\", A = \"2\" }\n", wantLine: 3, wantMsg: `duplicate env key "A"`},
		{name: "env bad separator", content: "[a]\ncmd = 'x'\nenv = { A = \"1\" B = \"2\" }\n", wantLine: 3, wantMsg: "expected , or }"},
		{name: "env trailing junk", content: "[a]\ncmd = 'x'\nenv = { A = \"1\" } junk\n", wantLine: 3, wantMsg: "unexpected trailing content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := action.Load(writeActions(t, tt.content))
			perr, ok := err.(*action.ParseError)
			if !ok {
				t.Fatalf("Load() error = %v (%T), want *ParseError", err, err)
			}
			if perr.Line != tt.wantLine {
				t.Errorf("ParseError.Line = %v, want %v (err: %v)", perr.Line, tt.wantLine, perr)
			}
			if !strings.Contains(perr.Msg, tt.wantMsg) {
				t.Errorf("ParseError.Msg = %q, want containing %q", perr.Msg, tt.wantMsg)
			}
			if !strings.HasPrefix(perr.Error(), "actions.toml:") {
				t.Errorf("ParseError.Error() = %q, want actions.toml:<line>: prefix", perr.Error())
			}
		})
	}
}

func TestAction_Expand_Valid(t *testing.T) {
	t.Parallel()
	subst := action.Subst{Host: "web", User: "dev", Addr: "192.0.2.10", Port: "3389"}
	tests := []struct {
		name string
		a    action.Action
		s    action.Subst
		want action.Invocation
	}{
		{
			name: "spec xfreerdp template",
			a:    action.Action{Name: "rdp", Cmd: "xfreerdp /u:{user} /v:{addr} /dynamic-resolution /cert:ignore"},
			s:    subst,
			want: action.Invocation{Argv: []string{"xfreerdp", "/u:dev", "/v:192.0.2.10", "/dynamic-resolution", "/cert:ignore"}},
		},
		{
			name: "env is substituted sorted and verbatim",
			a: action.Action{Name: "rdp", Cmd: "x {host}", Env: map[string]string{
				"ZZ":      "port={port}",
				"DISPLAY": "$(launchctl getenv DISPLAY)",
			}},
			s: subst,
			want: action.Invocation{
				Argv: []string{"x", "web"},
				Env:  []string{"DISPLAY=$(launchctl getenv DISPLAY)", "ZZ=port=3389"},
			},
		},
		{
			name: "value with spaces stays one argv element",
			a:    action.Action{Name: "a", Cmd: "tool /u:{user}"},
			s:    action.Subst{Host: "h", User: "Ann Lee", Addr: "a", Port: "1"},
			want: action.Invocation{Argv: []string{"tool", "/u:Ann Lee"}},
		},
		{
			name: "quote characters in values are data",
			a:    action.Action{Name: "a", Cmd: "tool {user}"},
			s:    action.Subst{Host: "h", User: `x" 'y`, Addr: "a", Port: "1"},
			want: action.Invocation{Argv: []string{"tool", `x" 'y`}},
		},
		{
			name: "quoted template words group and substitute",
			a:    action.Action{Name: "a", Cmd: `tool "a {host}" '{addr}:{port}' pre"in q"post`},
			s:    subst,
			want: action.Invocation{Argv: []string{"tool", "a web", "192.0.2.10:3389", "prein qpost"}},
		},
		{
			name: "escapes inside double quotes",
			a:    action.Action{Name: "a", Cmd: `tool "say \"hi\" \\ back"`},
			s:    subst,
			want: action.Invocation{Argv: []string{"tool", `say "hi" \ back`}},
		},
		{
			name: "adjacent placeholders and repeated use",
			a:    action.Action{Name: "a", Cmd: "tool {addr}:{port} {addr}"},
			s:    subst,
			want: action.Invocation{Argv: []string{"tool", "192.0.2.10:3389", "192.0.2.10"}},
		},
		{
			name: "tabs separate words",
			a:    action.Action{Name: "a", Cmd: "tool\t{host}"},
			s:    subst,
			want: action.Invocation{Argv: []string{"tool", "web"}},
		},
		{
			name: "empty quotes make an empty argv element",
			a:    action.Action{Name: "a", Cmd: `tool ""`},
			s:    subst,
			want: action.Invocation{Argv: []string{"tool", ""}},
		},
		{
			name: "double braces escape literal braces",
			a:    action.Action{Name: "a", Cmd: "awk '{{print $1}}' {host}"},
			s:    subst,
			want: action.Invocation{Argv: []string{"awk", "{print $1}", "web"}},
		},
		{
			name: "escaped braces mix with placeholders",
			a:    action.Action{Name: "a", Cmd: "tool {{x}}:{port}"},
			s:    subst,
			want: action.Invocation{Argv: []string{"tool", "{x}:3389"}},
		},
		{
			name: "lone closing brace is data",
			a:    action.Action{Name: "a", Cmd: "tool a}b"},
			s:    subst,
			want: action.Invocation{Argv: []string{"tool", "a}b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.a.Expand(tt.s)
			if err != nil {
				t.Fatalf("Expand() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Expand() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAction_Expand_Errors(t *testing.T) {
	t.Parallel()
	full := action.Subst{Host: "h", User: "u", Addr: "a", Port: "1"}
	tests := []struct {
		name    string
		a       action.Action
		s       action.Subst
		wantMsg string
	}{
		{name: "empty cmd", a: action.Action{Name: "a", Cmd: ""}, s: full, wantMsg: "empty cmd"},
		{name: "whitespace cmd", a: action.Action{Name: "a", Cmd: "  \t "}, s: full, wantMsg: "empty cmd"},
		{name: "unknown placeholder", a: action.Action{Name: "a", Cmd: "tool {dpi}"}, s: full, wantMsg: "unknown placeholder {dpi}"},
		{name: "case-sensitive placeholder", a: action.Action{Name: "a", Cmd: "tool {Host}"}, s: full, wantMsg: "unknown placeholder {Host}"},
		{name: "empty subst value", a: action.Action{Name: "a", Cmd: "tool /p:{port}"}, s: action.Subst{Host: "h", User: "u", Addr: "a"}, wantMsg: `placeholder {port} is empty in "/p:{port}"`},
		{name: "empty placeholder name", a: action.Action{Name: "a", Cmd: "tool {}"}, s: full, wantMsg: "unknown placeholder {}"},
		{name: "unclosed placeholder", a: action.Action{Name: "a", Cmd: "tool {host"}, s: full, wantMsg: "unclosed placeholder"},
		{name: "unterminated single quote", a: action.Action{Name: "a", Cmd: "tool 'x"}, s: full, wantMsg: "unterminated ' quote"},
		{name: "unterminated double quote", a: action.Action{Name: "a", Cmd: `tool "x`}, s: full, wantMsg: `unterminated " quote`},
		{name: "env unknown placeholder", a: action.Action{Name: "a", Cmd: "tool", Env: map[string]string{"K": "{nope}"}}, s: full, wantMsg: "env K"},
		{name: "env empty subst value", a: action.Action{Name: "a", Cmd: "tool", Env: map[string]string{"K": "{user}"}}, s: action.Subst{Host: "h", Addr: "a", Port: "1"}, wantMsg: "placeholder {user} is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.a.Expand(tt.s)
			if err == nil {
				t.Fatalf("Expand() error = nil, want containing %q", tt.wantMsg)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Expand() error = %q, want containing %q", err, tt.wantMsg)
			}
			if !strings.Contains(err.Error(), "action a") {
				t.Errorf("Expand() error = %q, want containing action name", err)
			}
		})
	}
}
