// Package transfer builds the scp and remote-ls invocations behind the
// TUI's C key (SASASH.4.33): copying a file or directory between the local
// machine and a configured host, both ends browsed before anything runs.
//
// Everything here names the host by its ssh_config ALIAS and nothing
// else: scp and ssh read the same config sasash displays — the active
// HostName, ProxyJump, and the managed ControlMaster trio — so a copy
// rides whatever path the alias dials right now and reuses a live master
// without a second authentication (SPEC §2, §4). No HostName is resolved
// here; that would be a second place connection info lives.
//
// The argv builders are pure. The one process this package runs — the
// remote listing — goes through run.Runner, so it is testable with
// fakerun and never dials in a unit test.
package transfer

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/nadavushka-dev/sasash/internal/run"
)

// Direction says which end is the source.
type Direction int

const (
	// Upload copies a local path to the host.
	Upload Direction = iota
	// Download copies a remote path to the local machine.
	Download
)

// String is the direction's one word, as the TUI shows it.
func (d Direction) String() string {
	if d == Download {
		return "download"
	}
	return "upload"
}

// Spec is one copy: the alias, the direction, the path at each end, and
// whether the source is a directory (then scp needs -r). Local is a local
// filesystem path; Remote is the path as the host sees it — relative to
// the login home unless absolute, exactly what scp is given.
type Spec struct {
	Alias     string
	Direction Direction
	Local     string
	Remote    string
	Recursive bool
}

// Source and Dest are the two scp operands in copy order, the remote one
// as alias:path.
func (s Spec) Source() string {
	if s.Direction == Download {
		return s.Alias + ":" + s.Remote
	}
	return s.Local
}

// Dest is the second scp operand.
func (s Spec) Dest() string {
	if s.Direction == Download {
		return s.Local
	}
	return s.Alias + ":" + s.Remote
}

// CopyArgv is the exact scp invocation for one Spec (SPEC §4):
//
//	scp [-r] <source> <dest>
//
// -r is present exactly when the source is a directory. The remote
// operand is alias:path with the path verbatim — scp hands it to the
// host's sftp server (OpenSSH 9+) or shell as typed, so quoting here
// would become part of the name.
func CopyArgv(s Spec) []string {
	argv := []string{"scp"}
	if s.Recursive {
		argv = append(argv, "-r")
	}
	return append(argv, s.Source(), s.Dest())
}

// ListArgv is the exact remote listing the browser runs (SPEC §4):
//
//	ssh <alias> ls -1Ap -- <path>
//
// -1 one name per line, -A every entry but . and .., -p a trailing slash
// on each directory (the one fact the browser needs — never `ls -l`, whose
// columns are locale- and platform-shaped), -- so a path starting with
// a dash is a path. The path is quoted for the remote shell only when it
// needs to be: the command line after the alias is what that shell runs.
func ListArgv(alias, dir string) []string {
	return []string{"ssh", alias, "ls", "-1Ap", "--", ShellQuote(dir)}
}

// ShellQuote makes s one word for a POSIX shell: unchanged when it is
// already plain, single-quoted otherwise (a single quote inside closes
// the word, is backslash-escaped, and reopens it, so it survives).
func ShellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !plainRune(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func plainRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("_-./+@%:,=", r)
}

// Entry is one row of a listing.
type Entry struct {
	Name  string
	IsDir bool
}

// ParseList reads `ls -1Ap` output: one name per line, directories
// marked by their trailing slash (stripped here). Directories come
// first, each group in the order ls printed it (ls sorts by name in the
// host's locale), so a browser reads like the local picker does.
func ParseList(out []byte) []Entry {
	var dirs, files []Entry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if name, ok := strings.CutSuffix(line, "/"); ok {
			dirs = append(dirs, Entry{Name: name, IsDir: true})
			continue
		}
		files = append(files, Entry{Name: line})
	}
	return append(dirs, files...)
}

// Home is where the remote browser starts: the login directory, as scp
// and ls see a bare "." — never a guessed absolute path.
const Home = "."

// Descend is the remote path of name inside dir, slash-joined and
// cleaned the way the host's shell would read it ("." + "etc" is "etc",
// "etc" + ".." is "." again, "." + ".." is "..").
func Descend(dir, name string) string {
	return path.Join(dir, name)
}

// Parent is the directory above dir — "." goes to "..", and further up
// keeps counting, since the login home's absolute path is not known here.
func Parent(dir string) string {
	return path.Join(dir, "..")
}

// ListError is a listing the host refused: ls ran and exited non-zero
// (no such directory, permission denied). Stderr is its reason, whole.
type ListError struct {
	Code   int
	Stderr string
}

func (e *ListError) Error() string {
	if reason := strings.TrimSpace(e.Stderr); reason != "" {
		return reason
	}
	return fmt.Sprintf("ls exited %d", e.Code)
}

// Lister runs ListArgv through a Runner.
type Lister struct {
	Runner run.Runner
}

// List returns dir's entries on alias. A non-zero exit is a *ListError
// carrying ls's own words; any other error means ssh could not run.
func (l Lister) List(ctx context.Context, alias, dir string) ([]Entry, error) {
	res, err := l.Runner.Run(ctx, ListArgv(alias, dir)...)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, &ListError{Code: res.Code, Stderr: string(res.Stderr)}
	}
	return ParseList(res.Stdout), nil
}
