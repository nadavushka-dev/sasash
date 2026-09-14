// The update subcommand (SASASH.4.38): replace this binary with the
// latest release, the way install.sh would — same assets, same sha256
// check — confirming first like setup.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/nadavushka-dev/sasash/internal/update"
)

func init() {
	register("update", "replace this binary with the latest release (asks first; -y, --check, or a tag)", func(args []string, stdout, stderr io.Writer) int {
		return runUpdate(args, os.Stdin, stdout, stderr)
	})
}

// updateTimeout bounds the whole exchange: a mirror that hangs must not
// hold the shell.
const updateTimeout = 2 * time.Minute

func runUpdate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	yes := flags.Bool("y", false, "answer yes to the confirmation prompt")
	check := flags.Bool("check", false, "report the latest release and exit; install nothing")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	want := ""
	if rest := flags.Args(); len(rest) == 1 {
		want = rest[0]
	} else if len(rest) > 1 {
		fmt.Fprintln(stderr, "sasash update: at most one tag, e.g. `sasash update v0.3.0`")
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "sasash update: %v\n", err)
		return 1
	}
	client := update.Client{BaseURL: os.Getenv("SASASH_BASE_URL")}
	return updateBinary(updateOpts{client: client, exe: exe, current: version(), want: want, yes: *yes, check: *check, goos: runtime.GOOS, goarch: runtime.GOARCH}, stdin, stdout, stderr)
}

// updateOpts is everything runUpdate decides from the environment, so a
// test drives updateBinary against an httptest release and a temp
// binary.
type updateOpts struct {
	client       update.Client
	exe          string // the binary to replace
	current      string // what version() says
	want         string // a tag to install, "" for the latest
	yes, check   bool
	goos, goarch string
}

func updateBinary(o updateOpts, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	tag := o.want
	if tag == "" {
		var err error
		if tag, err = o.client.Latest(ctx); err != nil {
			fmt.Fprintf(stderr, "sasash update: %v\n", err)
			return 1
		}
	} else if !update.IsRelease(tag) {
		fmt.Fprintf(stderr, "sasash update: %q is not a release tag (vX.Y.Z)\n", tag)
		return 2
	}
	asset := update.Asset(tag, o.goos, o.goarch)
	switch {
	case !update.IsRelease(o.current):
		fmt.Fprintf(stdout, "this is not a release build (%s); the latest release is %s\n", o.current, tag)
	case update.Compare(o.current, tag) == 0:
		fmt.Fprintf(stdout, "sasash %s is up to date\n", o.current)
		if !o.check && o.want == "" {
			return 0
		}
	case update.Compare(o.current, tag) < 0:
		fmt.Fprintf(stdout, "sasash %s → %s\n", o.current, tag)
	default:
		fmt.Fprintf(stdout, "sasash %s → %s (older)\n", o.current, tag)
	}
	fmt.Fprintf(stdout, "  %s\n  replaces %s\n", o.client.URL(tag, asset), o.exe)
	if o.check {
		return 0
	}
	if !o.yes && !confirm(stdin, stdout) {
		fmt.Fprintln(stdout, "aborted; nothing changed")
		return 1
	}
	tarball, err := o.client.Fetch(ctx, tag, asset)
	if err != nil {
		fmt.Fprintf(stderr, "sasash update: %v\n", err)
		return 1
	}
	sums, err := o.client.Fetch(ctx, tag, "checksums.txt")
	if err != nil {
		fmt.Fprintf(stderr, "sasash update: %v\n", err)
		return 1
	}
	if err := update.Verify(tarball, sums, asset); err != nil {
		fmt.Fprintf(stderr, "sasash update: %v\n", err)
		return 1
	}
	bin, err := update.Extract(tarball)
	if err != nil {
		fmt.Fprintf(stderr, "sasash update: %v\n", err)
		return 1
	}
	if err := update.Replace(o.exe, bin); err != nil {
		fmt.Fprintf(stderr, "sasash update: %v — re-run the installer (it falls back to sudo), or install to a writable directory\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "updated %s to %s\n", o.exe, tag)
	return 0
}
