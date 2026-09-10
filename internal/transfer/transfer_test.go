package transfer_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/run"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/transfer"
)

// The argv strings are the product (SPEC §12): every builder is pinned to
// the exact words scp and ssh receive.

func TestCopyArgv_Exact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec transfer.Spec
		want []string
	}{
		{
			name: "upload a file into a remote dir",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Upload, Local: "/home/u/notes.txt", Remote: "docs"},
			want: []string{"scp", "/home/u/notes.txt", "api:docs"},
		},
		{
			name: "upload a directory adds -r",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Upload, Local: "/home/u/proj", Remote: ".", Recursive: true},
			want: []string{"scp", "-r", "/home/u/proj", "api:."},
		},
		{
			name: "download a file into a local dir",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Download, Local: "/home/u/Downloads", Remote: "/var/log/syslog"},
			want: []string{"scp", "api:/var/log/syslog", "/home/u/Downloads"},
		},
		{
			name: "download a directory adds -r",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Download, Local: "/home/u", Remote: "etc/nginx", Recursive: true},
			want: []string{"scp", "-r", "api:etc/nginx", "/home/u"},
		},
		{
			name: "remote path with a space is passed verbatim, never quoted",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Upload, Local: "/home/u/a b", Remote: "my docs"},
			want: []string{"scp", "/home/u/a b", "api:my docs"},
		},
		{
			name: "a dotted alias is the alias, never a hostname",
			spec: transfer.Spec{Alias: "web.lan", Direction: transfer.Download, Local: "/tmp", Remote: "x"},
			want: []string{"scp", "web.lan:x", "/tmp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := transfer.CopyArgv(tt.spec); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CopyArgv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpec_SourceDest(t *testing.T) {
	t.Parallel()
	up := transfer.Spec{Alias: "api", Direction: transfer.Upload, Local: "/l", Remote: "r"}
	if got, want := up.Source(), "/l"; got != want {
		t.Errorf("Source() = %q, want %q", got, want)
	}
	if got, want := up.Dest(), "api:r"; got != want {
		t.Errorf("Dest() = %q, want %q", got, want)
	}
	down := transfer.Spec{Alias: "api", Direction: transfer.Download, Local: "/l", Remote: "r"}
	if got, want := down.Source(), "api:r"; got != want {
		t.Errorf("Source() = %q, want %q", got, want)
	}
	if got, want := down.Dest(), "/l"; got != want {
		t.Errorf("Dest() = %q, want %q", got, want)
	}
}

func TestDirection_String(t *testing.T) {
	t.Parallel()
	if got := transfer.Upload.String(); got != "upload" {
		t.Errorf("Upload.String() = %q, want upload", got)
	}
	if got := transfer.Download.String(); got != "download" {
		t.Errorf("Download.String() = %q, want download", got)
	}
}

func TestListArgv_Exact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		alias string
		dir   string
		want  []string
	}{
		{name: "home", alias: "api", dir: ".", want: []string{"ssh", "api", "ls", "-1Ap", "--", "."}},
		{name: "relative dir", alias: "api", dir: "etc/nginx", want: []string{"ssh", "api", "ls", "-1Ap", "--", "etc/nginx"}},
		{name: "absolute dir", alias: "api", dir: "/var/log", want: []string{"ssh", "api", "ls", "-1Ap", "--", "/var/log"}},
		{name: "above home", alias: "api", dir: "..", want: []string{"ssh", "api", "ls", "-1Ap", "--", ".."}},
		{name: "leading dash is guarded by --", alias: "api", dir: "-n", want: []string{"ssh", "api", "ls", "-1Ap", "--", "-n"}},
		{name: "a space is quoted for the remote shell", alias: "api", dir: "my docs", want: []string{"ssh", "api", "ls", "-1Ap", "--", "'my docs'"}},
		{name: "a quote is spliced", alias: "api", dir: "it's", want: []string{"ssh", "api", "ls", "-1Ap", "--", `'it'\''s'`}},
		{name: "a glob is quoted", alias: "api", dir: "a*b", want: []string{"ssh", "api", "ls", "-1Ap", "--", "'a*b'"}},
		{name: "empty is an empty word", alias: "api", dir: "", want: []string{"ssh", "api", "ls", "-1Ap", "--", "''"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := transfer.ListArgv(tt.alias, tt.dir); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ListArgv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{name: "plain", in: "/var/log", want: "/var/log"},
		{name: "plain punctuation", in: "a-b_c.d+e@f%g:h,i=j", want: "a-b_c.d+e@f%g:h,i=j"},
		{name: "space", in: "a b", want: "'a b'"},
		{name: "dollar", in: "$HOME", want: "'$HOME'"},
		{name: "backtick", in: "`id`", want: "'`id`'"},
		{name: "semicolon", in: "x;rm", want: "'x;rm'"},
		{name: "unicode", in: "héllo", want: "'héllo'"},
		{name: "single quote", in: "it's", want: `'it'\''s'`},
		{name: "empty", in: "", want: "''"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := transfer.ShellQuote(tt.in); got != tt.want {
				t.Errorf("ShellQuote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		out  string
		want []transfer.Entry
	}{
		{name: "empty dir", out: "", want: nil},
		{name: "files only", out: "a.txt\nb.txt\n", want: []transfer.Entry{{Name: "a.txt"}, {Name: "b.txt"}}},
		{
			name: "dirs first, ls order kept within each group",
			out:  "b.txt\ndocs/\na.txt\n.hidden\nsrc/\n",
			want: []transfer.Entry{{Name: "docs", IsDir: true}, {Name: "src", IsDir: true}, {Name: "b.txt"}, {Name: "a.txt"}, {Name: ".hidden"}},
		},
		{name: "no trailing newline", out: "x/", want: []transfer.Entry{{Name: "x", IsDir: true}}},
		{name: "blank lines skipped", out: "\n\na\n\n", want: []transfer.Entry{{Name: "a"}}},
		{name: "a name with spaces", out: "my docs/\nmy file\n", want: []transfer.Entry{{Name: "my docs", IsDir: true}, {Name: "my file"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := transfer.ParseList([]byte(tt.out)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseList() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDescend_Parent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, dir, child, want, up string
	}{
		{name: "home", dir: transfer.Home, child: "etc", want: "etc", up: ".."},
		{name: "relative", dir: "etc", child: "nginx", want: "etc/nginx", up: "."},
		{name: "nested", dir: "etc/nginx", child: "conf.d", want: "etc/nginx/conf.d", up: "etc"},
		{name: "above home", dir: "..", child: "other", want: "../other", up: "../.."},
		{name: "absolute", dir: "/var", child: "log", want: "/var/log", up: "/"},
		{name: "root stays root", dir: "/", child: "etc", want: "/etc", up: "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := transfer.Descend(tt.dir, tt.child); got != tt.want {
				t.Errorf("Descend(%q, %q) = %q, want %q", tt.dir, tt.child, got, tt.want)
			}
			if got := transfer.Parent(tt.dir); got != tt.up {
				t.Errorf("Parent(%q) = %q, want %q", tt.dir, got, tt.up)
			}
		})
	}
}

func TestLister_List_ParsesAndRecordsArgv(t *testing.T) {
	t.Parallel()
	f := fakerun.New().StubOut("bin/\nnotes.txt\n", "ssh", "api", "ls", "-1Ap", "--", ".")
	got, err := transfer.Lister{Runner: f}.List(context.Background(), "api", transfer.Home)
	if err != nil {
		t.Fatalf("List() err = %v", err)
	}
	want := []transfer.Entry{{Name: "bin", IsDir: true}, {Name: "notes.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %+v, want %+v", got, want)
	}
	if keys := f.CallKeys(); !reflect.DeepEqual(keys, []string{"ssh api ls -1Ap -- ."}) {
		t.Errorf("CallKeys() = %q, want the one listing", keys)
	}
}

// A non-zero exit is data (SPEC §12): the host's refusal comes back as a
// ListError carrying ls's own words, never a crash and never an empty
// directory pretending to be one.
func TestLister_List_NonZeroExitIsListError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		reply  fakerun.Reply
		want   string
		code   int
		isList bool
	}{
		{
			name:   "permission denied",
			reply:  fakerun.Reply{Result: runResult(2, "ls: cannot open directory '/root': Permission denied\n")},
			want:   "ls: cannot open directory '/root': Permission denied",
			code:   2,
			isList: true,
		},
		{
			name:   "no such directory",
			reply:  fakerun.Reply{Result: runResult(2, "ls: cannot access 'nope': No such file or directory\n")},
			want:   "ls: cannot access 'nope': No such file or directory",
			code:   2,
			isList: true,
		},
		{
			name:   "ssh refused with no words",
			reply:  fakerun.Reply{Result: runResult(255, "")},
			want:   "ls exited 255",
			code:   255,
			isList: true,
		},
		{
			name:  "ssh could not run",
			reply: fakerun.Reply{Err: errors.New("exec: \"ssh\": executable file not found")},
			want:  "exec: \"ssh\": executable file not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fakerun.New().Stub(tt.reply, "ssh", "api", "ls", "-1Ap", "--", "/root")
			got, err := transfer.Lister{Runner: f}.List(context.Background(), "api", "/root")
			if got != nil {
				t.Errorf("List() = %+v, want nil entries on failure", got)
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("List() err = %v, want %q", err, tt.want)
			}
			var le *transfer.ListError
			if errors.As(err, &le) != tt.isList {
				t.Errorf("errors.As(ListError) = %v, want %v", !tt.isList, tt.isList)
			}
			if tt.isList && le.Code != tt.code {
				t.Errorf("Code = %d, want %d", le.Code, tt.code)
			}
		})
	}
}

func runResult(code int, stderr string) run.Result {
	return run.Result{Code: code, Stderr: []byte(stderr)}
}
