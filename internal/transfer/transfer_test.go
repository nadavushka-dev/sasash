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
			name: "upload a file into a remote directory",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Upload, Sources: []string{"/home/u/notes.txt"}, Dest: "docs"},
			want: []string{"scp", "/home/u/notes.txt", "api:docs"},
		},
		{
			name: "upload a directory is -r",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Upload, Sources: []string{"/home/u/proj"}, Dest: ".", Recursive: true},
			want: []string{"scp", "-r", "/home/u/proj", "api:."},
		},
		{
			name: "download a file into a local directory",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Download, Sources: []string{"/var/log/syslog"}, Dest: "/home/u/Downloads"},
			want: []string{"scp", "api:/var/log/syslog", "/home/u/Downloads"},
		},
		{
			name: "download a directory is -r",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Download, Sources: []string{"etc/nginx"}, Dest: "/home/u", Recursive: true},
			want: []string{"scp", "-r", "api:etc/nginx", "/home/u"},
		},
		{
			name: "paths with spaces are single argv words, never quoted",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Upload, Sources: []string{"/home/u/a b"}, Dest: "my docs"},
			want: []string{"scp", "/home/u/a b", "api:my docs"},
		},
		{
			name: "a dotted alias is used verbatim",
			spec: transfer.Spec{Alias: "web.lan", Direction: transfer.Download, Sources: []string{"x"}, Dest: "/tmp"},
			want: []string{"scp", "web.lan:x", "/tmp"},
		},
		{
			name: "several sources go in one run, in order",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Upload, Sources: []string{"/home/u/b.txt", "/home/u/a.txt"}, Dest: "docs"},
			want: []string{"scp", "/home/u/b.txt", "/home/u/a.txt", "api:docs"},
		},
		{
			name: "several remote sources each carry the alias, -r when any is a directory",
			spec: transfer.Spec{Alias: "api", Direction: transfer.Download, Sources: []string{"readme", "docs"}, Dest: "/home/u", Recursive: true},
			want: []string{"scp", "-r", "api:readme", "api:docs", "/home/u"},
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

func TestSpec_Operands(t *testing.T) {
	t.Parallel()
	up := transfer.Spec{Alias: "api", Direction: transfer.Upload, Sources: []string{"/l", "/m"}, Dest: "r"}
	if got, want := up.SourceOperands(), []string{"/l", "/m"}; !reflect.DeepEqual(got, want) {
		t.Errorf("upload SourceOperands() = %q, want %q", got, want)
	}
	if got := up.DestOperand(); got != "api:r" {
		t.Errorf("upload DestOperand() = %q, want api:r", got)
	}
	down := transfer.Spec{Alias: "api", Direction: transfer.Download, Sources: []string{"r", "s"}, Dest: "/l"}
	if got, want := down.SourceOperands(), []string{"api:r", "api:s"}; !reflect.DeepEqual(got, want) {
		t.Errorf("download SourceOperands() = %q, want %q", got, want)
	}
	if got := down.DestOperand(); got != "/l" {
		t.Errorf("download DestOperand() = %q, want /l", got)
	}
	if got := (transfer.Spec{}).SourceOperands(); len(got) != 0 {
		t.Errorf("empty SourceOperands() = %q, want none", got)
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
		{name: "home", alias: "api", dir: ".", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "."}},
		{name: "relative dir", alias: "api", dir: "etc/nginx", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "etc/nginx"}},
		{name: "absolute dir", alias: "api", dir: "/var/log", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "/var/log"}},
		{name: "above home", alias: "api", dir: "..", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", ".."}},
		{name: "leading dash is guarded by --", alias: "api", dir: "-n", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "-n"}},
		{name: "a space is quoted for the remote shell", alias: "api", dir: "my docs", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "'my docs'"}},
		{name: "a quote is spliced", alias: "api", dir: "it's", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", `'it'\''s'`}},
		{name: "a glob is quoted", alias: "api", dir: "a*b", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "'a*b'"}},
		{name: "empty is an empty word", alias: "api", dir: "", want: []string{"ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "''"}},
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
	f := fakerun.New().StubOut("bin/\nnotes.txt\n", "ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", ".")
	got, err := transfer.Lister{Runner: f}.List(context.Background(), "api", transfer.Home)
	if err != nil {
		t.Fatalf("List() err = %v", err)
	}
	want := []transfer.Entry{{Name: "bin", IsDir: true}, {Name: "notes.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %+v, want %+v", got, want)
	}
	if keys := f.CallKeys(); !reflect.DeepEqual(keys, []string{"ssh -o BatchMode=yes api ls -1Ap -- ."}) {
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
			f := fakerun.New().Stub(tt.reply, "ssh", "-o", "BatchMode=yes", "api", "ls", "-1Ap", "--", "/root")
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
