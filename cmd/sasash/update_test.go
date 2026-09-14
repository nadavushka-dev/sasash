// Internal test package, like setup_test.go: updateBinary is not exported.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/update"
)

// fakeRelease serves tag with one asset for linux/amd64 holding bin.
func fakeRelease(t *testing.T, tag string, bin []byte, tamper bool) (*httptest.Server, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "sasash", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	tw.Write(bin)
	tw.Close()
	gz.Close()
	asset := update.Asset(tag, "linux", "amd64")
	sum := sha256.Sum256(buf.Bytes())
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	if tamper {
		sums = strings.Repeat("0", 64) + "  " + asset + "\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
		case "/tag/" + tag:
			w.WriteHeader(http.StatusOK)
		case "/download/" + tag + "/" + asset:
			w.Write(buf.Bytes())
		case "/download/" + tag + "/checksums.txt":
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, asset
}

func TestUpdateBinary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		current     string
		want        string // a pinned tag
		yes, check  bool
		stdin       string
		tamper      bool
		wantCode    int
		wantStdout  []string
		wantStderr  string
		wantReplace bool
	}{
		{name: "check reports and installs nothing", current: "v0.3.0", check: true, wantCode: 0, wantStdout: []string{"sasash v0.3.0 → v0.3.1", "sasash_0.3.1_linux_amd64.tar.gz", "replaces "}},
		{name: "up to date exits without asking", current: "v0.3.1", wantCode: 0, wantStdout: []string{"sasash v0.3.1 is up to date"}},
		{name: "confirmed replaces the binary", current: "v0.3.0", stdin: "y\n", wantCode: 0, wantStdout: []string{"sasash v0.3.0 → v0.3.1", "updated "}, wantReplace: true},
		{name: "-y skips the prompt", current: "v0.3.0", yes: true, wantCode: 0, wantStdout: []string{"updated "}, wantReplace: true},
		{name: "declined changes nothing", current: "v0.3.0", stdin: "n\n", wantCode: 1, wantStdout: []string{"aborted; nothing changed"}},
		{name: "a dev build is told and offered the release", current: "dev", yes: true, wantCode: 0, wantStdout: []string{"not a release build (dev); the latest release is v0.3.1", "updated "}, wantReplace: true},
		{name: "a newer local build is told the release is older", current: "v0.9.0", check: true, wantCode: 0, wantStdout: []string{"sasash v0.9.0 → v0.3.1 (older)"}},
		{name: "a pinned tag is installed even when current", current: "v0.3.1", want: "v0.3.1", yes: true, wantCode: 0, wantStdout: []string{"is up to date", "updated "}, wantReplace: true},
		{name: "a pinned non-tag is refused", current: "v0.3.0", want: "latest-ish", wantCode: 2, wantStderr: "is not a release tag"},
		{name: "a checksum mismatch refuses and keeps the binary", current: "v0.3.0", yes: true, tamper: true, wantCode: 1, wantStderr: "sha256 mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := fakeRelease(t, "v0.3.1", []byte("new binary"), tt.tamper)
			exe := filepath.Join(t.TempDir(), "sasash")
			if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			o := updateOpts{client: update.Client{BaseURL: srv.URL, Backoff: 1}, exe: exe, current: tt.current, want: tt.want, yes: tt.yes, check: tt.check, goos: "linux", goarch: "amd64"}
			got := updateBinary(o, strings.NewReader(tt.stdin), &stdout, &stderr)
			if got != tt.wantCode {
				t.Errorf("updateBinary() = %d, want %d (stdout %q, stderr %q)", got, tt.wantCode, stdout.String(), stderr.String())
			}
			for _, w := range tt.wantStdout {
				if !strings.Contains(stdout.String(), w) {
					t.Errorf("stdout = %q, want substring %q", stdout.String(), w)
				}
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
			data, _ := os.ReadFile(exe)
			if replaced := string(data) == "new binary"; replaced != tt.wantReplace {
				t.Errorf("binary replaced = %v, want %v (content %q)", replaced, tt.wantReplace, data)
			}
		})
	}
}

func TestUpdateBinary_NoRelease(t *testing.T) {
	t.Parallel()
	none := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(none.Close)
	exe := filepath.Join(t.TempDir(), "sasash")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	o := updateOpts{client: update.Client{BaseURL: none.URL, Backoff: 1}, exe: exe, current: "v0.3.0", goos: "linux", goarch: "amd64"}
	if got := updateBinary(o, strings.NewReader(""), &stdout, &stderr); got != 1 || !strings.Contains(stderr.String(), "no release found") {
		t.Errorf("updateBinary() = %d, stderr %q; want 1 and no release found", got, stderr.String())
	}
	// A release whose asset is missing for this platform.
	srv, _ := fakeRelease(t, "v0.3.1", []byte("new"), false)
	o = updateOpts{client: update.Client{BaseURL: srv.URL, Backoff: 1}, exe: exe, current: "v0.3.0", yes: true, goos: "plan9", goarch: "mips"}
	stderr.Reset()
	if got := updateBinary(o, strings.NewReader(""), &stdout, &stderr); got != 1 || !strings.Contains(stderr.String(), "HTTP 404") {
		t.Errorf("updateBinary() for an unbuilt platform = %d, stderr %q; want 1 and HTTP 404", got, stderr.String())
	}
	if data, _ := os.ReadFile(exe); string(data) != "old" {
		t.Errorf("binary changed on a failed update: %q", data)
	}
}

func TestRunUpdate_Flags(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if got := runUpdate([]string{"-bogus"}, strings.NewReader(""), &stdout, &stderr); got != 2 {
		t.Errorf("runUpdate(-bogus) = %d, want 2", got)
	}
	if got := runUpdate([]string{"v0.1.0", "v0.2.0"}, strings.NewReader(""), &stdout, &stderr); got != 2 || !strings.Contains(stderr.String(), "at most one tag") {
		t.Errorf("runUpdate(two tags) = %d, stderr %q; want 2", got, stderr.String())
	}
}
