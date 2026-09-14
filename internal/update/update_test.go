package update_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/update"
)

// tarball builds a release archive holding README.md and a sasash
// binary of the given bytes, as GoReleaser lays it out.
func tarball(t *testing.T, bin []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("# sasash\n")}, {"sasash", bin}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// release serves a GitHub-shaped release layout: /latest redirects to
// /tag/<tag>, files live under /download/<tag>/<name>.
func release(t *testing.T, tag string, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/tag/"):
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/download/"+tag+"/"):
			name := strings.TrimPrefix(r.URL.Path, "/download/"+tag+"/")
			body, ok := files[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_Latest(t *testing.T) {
	t.Parallel()
	srv := release(t, "v0.3.1", nil)
	c := update.Client{BaseURL: srv.URL + "/", Backoff: 1}
	got, err := c.Latest(context.Background())
	if err != nil || got != "v0.3.1" {
		t.Errorf("Latest() = %q, %v; want v0.3.1", got, err)
	}
	// No release: GitHub answers 404 on /latest.
	none := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(none.Close)
	if _, err := (update.Client{BaseURL: none.URL, Backoff: 1}).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "no release found") {
		t.Errorf("Latest() without a release err = %v, want no release found", err)
	}
	// A landing that is not a tag page.
	odd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(odd.Close)
	if _, err := (update.Client{BaseURL: odd.URL, Backoff: 1}).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "landed on") {
		t.Errorf("Latest() on a non-tag landing err = %v, want landed on", err)
	}
}

func TestClient_RetriesTransientFailures(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path != "/latest":
			w.WriteHeader(http.StatusOK)
		case calls.Add(1) < 3:
			w.WriteHeader(http.StatusGatewayTimeout)
		default:
			http.Redirect(w, r, "/tag/v1.0.0", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := update.Client{BaseURL: srv.URL, Backoff: 1}
	got, err := c.Latest(context.Background())
	if err != nil || got != "v1.0.0" {
		t.Errorf("Latest() after two 504s = %q, %v; want v1.0.0", got, err)
	}
	// Retries exhausted: the last failure is named with the count.
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(always.Close)
	_, err = (update.Client{BaseURL: always.URL, Retries: 2, Backoff: 1}).Fetch(context.Background(), "v1.0.0", "x")
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("Fetch() against a 502 err = %v, want the attempts and the code", err)
	}
	// A context that ends stops the retries.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (update.Client{BaseURL: always.URL, Backoff: 1}).Fetch(ctx, "v1.0.0", "x"); err == nil {
		t.Errorf("Fetch() with a cancelled context err = nil")
	}
	// A server that is not there at all.
	if _, err := (update.Client{BaseURL: "http://127.0.0.1:1", Retries: 1, Backoff: 1}).Latest(context.Background()); err == nil {
		t.Errorf("Latest() against a closed port err = nil")
	}
}

func TestClient_FetchAndURL(t *testing.T) {
	t.Parallel()
	srv := release(t, "v0.3.1", map[string][]byte{"checksums.txt": []byte("abc  x\n")})
	c := update.Client{BaseURL: srv.URL, Backoff: 1}
	if got := c.URL("v0.3.1", "checksums.txt"); got != srv.URL+"/download/v0.3.1/checksums.txt" {
		t.Errorf("URL() = %q", got)
	}
	body, err := c.Fetch(context.Background(), "v0.3.1", "checksums.txt")
	if err != nil || string(body) != "abc  x\n" {
		t.Errorf("Fetch() = %q, %v", body, err)
	}
	if _, err := c.Fetch(context.Background(), "v0.3.1", "missing.tar.gz"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("Fetch(missing) err = %v, want HTTP 404", err)
	}
	if got := (update.Client{}).URL("v1.0.0", "a"); got != update.DefaultBaseURL+"/download/v1.0.0/a" {
		t.Errorf("default URL() = %q", got)
	}
}

func TestAsset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tag  string
		goos string
		arch string
		want string
	}{
		{name: "darwin arm64", tag: "v0.3.0", goos: "darwin", arch: "arm64", want: "sasash_0.3.0_darwin_arm64.tar.gz"},
		{name: "linux amd64 pre-release", tag: "v1.0.0-rc1", goos: "linux", arch: "amd64", want: "sasash_1.0.0-rc1_linux_amd64.tar.gz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := update.Asset(tt.tag, tt.goos, tt.arch); got != tt.want {
				t.Errorf("Asset() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	data := []byte("release bytes")
	sums := []byte(sum(data) + "  sasash_0.3.0_darwin_arm64.tar.gz\n" + sum([]byte("other")) + "  sasash_0.3.0_linux_amd64.tar.gz\n")
	tests := []struct {
		name    string
		data    []byte
		asset   string
		wantErr string
	}{
		{name: "listed and matching", data: data, asset: "sasash_0.3.0_darwin_arm64.tar.gz"},
		{name: "listed, mismatch", data: []byte("tampered"), asset: "sasash_0.3.0_darwin_arm64.tar.gz", wantErr: "sha256 mismatch"},
		{name: "unlisted", data: data, asset: "sasash_0.3.0_windows_amd64.tar.gz", wantErr: "not listed in checksums.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := update.Verify(tt.data, sums, tt.asset)
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("Verify() err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestExtract(t *testing.T) {
	t.Parallel()
	bin := []byte("#!/bin/sh\necho new\n")
	got, err := update.Extract(tarball(t, bin))
	if err != nil || !bytes.Equal(got, bin) {
		t.Errorf("Extract() = %q, %v; want the binary", got, err)
	}
	if _, err := update.Extract([]byte("not gzip")); err == nil || !strings.Contains(err.Error(), "not a gzip tarball") {
		t.Errorf("Extract(garbage) err = %v", err)
	}
	// A tarball without the binary.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if _, err := update.Extract(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "no sasash binary") {
		t.Errorf("Extract(no binary) err = %v", err)
	}
}

func TestReplace(t *testing.T) {
	t.Parallel()
	t.Run("atomic swap keeps the path and sets the mode", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sasash")
		if err := os.WriteFile(path, []byte("old"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := update.Replace(path, []byte("new")); err != nil {
			t.Fatalf("Replace() = %v", err)
		}
		got, _ := os.ReadFile(path)
		info, _ := os.Stat(path)
		if string(got) != "new" || info.Mode().Perm() != 0o755 {
			t.Errorf("after Replace: content %q, mode %o; want new, 0755", got, info.Mode().Perm())
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".sasash-update-*")); len(left) != 0 {
			t.Errorf("temp files left: %v", left)
		}
	})
	t.Run("a symlink is written through", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		real := filepath.Join(dir, "store", "sasash")
		if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(real, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "sasash")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if err := update.Replace(link, []byte("new")); err != nil {
			t.Fatalf("Replace() = %v", err)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link was replaced, want it kept")
		}
		if got, _ := os.ReadFile(real); string(got) != "new" {
			t.Errorf("target = %q, want new", got)
		}
	})
	t.Run("an unwritable directory is an error naming the path", func(t *testing.T) {
		t.Parallel()
		if os.Getuid() == 0 {
			t.Skip("root writes everywhere")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "sasash")
		if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		err := update.Replace(path, []byte("new"))
		resolved, _ := filepath.EvalSymlinks(path) // macOS: /var is /private/var
		if err == nil || !strings.Contains(err.Error(), "cannot write beside "+resolved) {
			t.Errorf("Replace() err = %v, want cannot write beside", err)
		}
		if got, _ := os.ReadFile(path); string(got) != "old" {
			t.Errorf("binary changed on a failed replace: %q", got)
		}
	})
}

func TestCompare(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{name: "equal", a: "v0.3.0", b: "v0.3.0", want: 0},
		{name: "patch older", a: "v0.3.0", b: "v0.3.1", want: -1},
		{name: "minor newer", a: "v0.4.0", b: "v0.3.9", want: 1},
		{name: "major wins", a: "v1.0.0", b: "v0.99.99", want: 1},
		{name: "numeric not lexical", a: "v0.10.0", b: "v0.9.0", want: 1},
		{name: "pre-release precedes its release", a: "v1.0.0-rc1", b: "v1.0.0", want: -1},
		{name: "release follows its pre-release", a: "v1.0.0", b: "v1.0.0-rc1", want: 1},
		{name: "pre-releases order by name", a: "v1.0.0-rc1", b: "v1.0.0-rc2", want: -1},
		{name: "equal pre-releases", a: "v1.0.0-rc1", b: "v1.0.0-rc1", want: 0},
		{name: "a dev build is older than any release", a: "dev", b: "v0.0.1", want: -1},
		{name: "a pseudo-version is older than any release", a: "v0.2.1-0.20260914125442-1526a7bb3941+dirty", b: "v0.2.0", want: -1},
		{name: "a release is newer than a dev build", a: "v0.3.0", b: "dev", want: 1},
		{name: "two non-releases are equal", a: "dev", b: "(devel)", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := update.Compare(tt.a, tt.b); got != tt.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsRelease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    string
		want bool
	}{
		{name: "release", v: "v0.3.0", want: true},
		{name: "pre-release", v: "v1.0.0-rc1", want: true},
		{name: "no v", v: "0.3.0", want: false},
		{name: "two parts", v: "v0.3", want: false},
		{name: "leading zero", v: "v0.03.0", want: false},
		{name: "letters", v: "v0.x.0", want: false},
		{name: "build metadata", v: "v0.3.0+dirty", want: false},
		{name: "pseudo-version", v: "v0.2.1-0.20260914125442-1526a7bb3941", want: true}, // shaped like a pre-release; Compare puts it before v0.2.1
		{name: "dev", v: "dev", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := update.IsRelease(tt.v); got != tt.want {
				t.Errorf("IsRelease(%q) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

func TestClient_EdgeCases(t *testing.T) {
	t.Parallel()
	srv := release(t, "v0.3.1", map[string][]byte{"x": []byte("y")})
	// The defaults — no client, no backoff — serve a clean release without a wait.
	if got, err := (update.Client{BaseURL: srv.URL}).Latest(context.Background()); err != nil || got != "v0.3.1" {
		t.Errorf("Latest() with defaults = %q, %v", got, err)
	}
	// A caller-supplied HTTP client is the one used.
	custom := update.Client{HTTP: srv.Client(), BaseURL: srv.URL, Backoff: 1}
	if got, err := custom.Latest(context.Background()); err != nil || got != "v0.3.1" {
		t.Errorf("Latest() with a custom client = %q, %v", got, err)
	}
	// A base URL no request can be built from.
	bad := update.Client{BaseURL: "http://[::1]:namedport", Backoff: 1}
	if _, err := bad.Latest(context.Background()); err == nil {
		t.Errorf("Latest() with a malformed base err = nil")
	}
	if _, err := bad.Fetch(context.Background(), "v1", "x"); err == nil {
		t.Errorf("Fetch() with a malformed base err = nil")
	}
	// A gzip stream that is not a tar archive.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte("this is not a tar archive at all, just bytes"))
	gz.Close()
	if _, err := update.Extract(buf.Bytes()); err == nil {
		t.Errorf("Extract(gzip of non-tar) err = nil")
	}
	// Compare: the later pre-release is newer.
	if got := update.Compare("v1.0.0-rc2", "v1.0.0-rc1"); got != 1 {
		t.Errorf("Compare(rc2, rc1) = %d, want 1", got)
	}
	if got := update.Compare("v1.0.0", "v0.9.0-rc1"); got != 1 {
		t.Errorf("Compare(release, older pre) = %d, want 1", got)
	}
}

func TestReplace_RenameFailure(t *testing.T) {
	t.Parallel()
	// The target is a non-empty directory: the temp file is written, the
	// rename refused, and the temp file cleaned up.
	dir := t.TempDir()
	target := filepath.Join(dir, "sasash")
	if err := os.MkdirAll(filepath.Join(target, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := update.Replace(target, []byte("new")); err == nil {
		t.Errorf("Replace(over a directory) err = nil")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".sasash-update-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}
