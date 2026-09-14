// Package update replaces the running sasash binary with a released one
// (SASASH.4.38): the same assets, the same checks and the same
// destination discipline as install.sh, in Go. It resolves the latest
// tag by following the releases/latest redirect (no API, no rate
// limit; GitHub's latest skips pre-releases), downloads
// sasash_<version>_<os>_<arch>.tar.gz and checksums.txt, verifies the
// sha256, extracts the binary, and renames it over the executable —
// temp file in the same directory, mode 0755, rename — so the swap is
// atomic and a failed download never leaves a half-written sasash.
//
// This is the one place outside internal/probe that touches the
// network, through Client so tests serve a release layout from
// httptest. Nothing here runs a process.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is where releases live; SASASH_BASE_URL overrides it
// (mirrors, tests), as it does for install.sh.
const DefaultBaseURL = "https://github.com/nadavushka-dev/sasash/releases"

// Client fetches release files. HTTP is the default; a test wires an
// httptest server through BaseURL. A transient answer — a 5xx, a
// connection that fails — is retried Retries more times (default 3,
// the installer's curl --retry) after Backoff (default a second).
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Retries int
	Backoff time.Duration
}

// do sends req, retrying the transient failures GitHub's release path
// is known to throw (a 504 on releases/latest or on an asset).
func (c Client) do(req *http.Request) (*http.Response, error) {
	retries, backoff := c.Retries, c.Backoff
	if retries == 0 {
		retries = 3
	}
	if backoff == 0 {
		backoff = time.Second
	}
	var last error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(backoff):
			}
		}
		resp, err := c.http().Do(req)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			last = fmt.Errorf("%s: HTTP %d", req.URL, resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("after %d attempts: %w", retries+1, last)
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{}
}

func (c Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

// Latest is the newest release tag: GitHub answers <base>/latest with a
// redirect to <base>/tag/<tag>, and that tag is the answer — the way
// install.sh reads it, without the API.
func (c Client) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.base()+"/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	final := resp.Request.URL.String()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("no release found at %s/latest (HTTP %d)", c.base(), resp.StatusCode)
	}
	if _, tag, ok := strings.Cut(final, "/tag/"); ok && tag != "" {
		return tag, nil
	}
	return "", fmt.Errorf("no release found at %s/latest (landed on %s)", c.base(), final)
}

// Asset is the tarball name for a tag on this OS and architecture —
// .goreleaser.yaml's name_template, as install.sh derives it too.
func Asset(tag, goos, goarch string) string {
	return "sasash_" + strings.TrimPrefix(tag, "v") + "_" + goos + "_" + goarch + ".tar.gz"
}

// URL is where a tag's file is downloaded from.
func (c Client) URL(tag, name string) string {
	return c.base() + "/download/" + tag + "/" + name
}

// Fetch downloads one release file whole.
func (c Client) Fetch(ctx context.Context, tag, name string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(tag, name), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: %s (HTTP %d)", c.URL(tag, name), resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Verify checks data against the checksums.txt line naming it — the
// `<sha256>  <name>` lines GoReleaser writes. An unlisted name is
// refused as firmly as a mismatch: an asset the release does not vouch
// for is not installed.
func Verify(data, checksums []byte, name string) error {
	want := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = fields[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in checksums.txt", name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("sha256 mismatch for %s: want %s, got %s", name, want, got)
	}
	return nil
}

// Extract returns the sasash binary from a release tarball.
func Extract(tarball []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("not a gzip tarball: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the tarball holds no sasash binary")
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == "sasash" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}

// Replace puts bin at path atomically: written beside it as a temp
// file, mode 0755, then renamed over it — the running process keeps
// its old inode and the next invocation is the new binary. A symlink
// is written through, so a link into a dotfiles tree survives.
func Replace(path string, bin []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sasash-update-*")
	if err != nil {
		return fmt.Errorf("cannot write beside %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Compare orders two release tags: -1 when a is older than b, 0 when
// equal, +1 when newer. It reads vMAJOR.MINOR.PATCH with an optional
// -prerelease (older than the release it precedes); anything else — a
// pseudo-version, "dev", "+dirty" — is not a release and compares as
// older than every release, so an update is offered.
func Compare(a, b string) int {
	ar, aok := parse(a)
	br, bok := parse(b)
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return -1
	case !bok:
		return 1
	}
	for i := range 3 {
		if ar.n[i] != br.n[i] {
			if ar.n[i] < br.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case ar.pre == br.pre:
		return 0
	case ar.pre == "":
		return 1
	case br.pre == "":
		return -1
	case ar.pre < br.pre:
		return -1
	}
	return 1
}

// IsRelease reports whether v is a release tag Compare understands.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

type release struct {
	n   [3]int
	pre string
}

func parse(v string) (release, bool) {
	var r release
	s, ok := strings.CutPrefix(v, "v")
	if !ok {
		return r, false
	}
	s, r.pre, _ = strings.Cut(s, "-")
	if strings.Contains(s, "+") || strings.Contains(r.pre, "+") {
		return r, false // a build suffix: a local build, never a release
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return r, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return r, false
		}
		r.n[i] = n
	}
	return r, true
}
