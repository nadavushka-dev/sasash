# Releasing sasash

Distribution is prebuilt binaries (SASASH.8): a `v*` tag triggers
`.github/workflows/release.yml`, which runs `make check` and then GoReleaser
(`.goreleaser.yaml`) to build darwin/linux x amd64/arm64, publish a GitHub Release
with four `.tar.gz` archives plus `checksums.txt`. `install.sh` at the repo root is
the curl|sh onboarding path and reads exactly those assets; `go install ...@latest`
from source is the only other route.

## Cutting a release

```sh
git checkout main && git pull
make check && make check-dist      # what CI runs; check-dist needs brew: goreleaser shellcheck actionlint
git tag -a v0.1.0 -m "v0.1.0"
git push origin main v0.1.0        # the tag push starts the release workflow
```

Then watch the `release` run under Actions. When it is green, the release page has
the four tarballs and `checksums.txt`, and `install.sh` works on any macOS or Linux
box. Pre-release tags (`v0.2.0-rc1`) are marked pre-release automatically; the
installer's `latest` skips them, so pin with `SASASH_VERSION` to test one.

The version is not injected: `sasash version` reads `runtime/debug.ReadBuildInfo`,
and Go stamps the main module version from the tag at build time. The release
runner's plain, clean checkout at the tag is exactly what that needs. Locally:
a dirty tree adds `+dirty`, an untagged commit gives a pseudo-version, and inside
a linked `git worktree` Go reads the *primary* checkout's HEAD rather than the
worktree's — so prove a stamp from a plain clone, never from `.worktrees/`.

## Local proof without pushing

- `make snapshot` builds all four targets into `dist/` (gitignored).
  `dist/sasash_darwin_arm64_v8.0/sasash version` shows what the binary would report.
- `make check-dist` runs the snapshot, lints the workflows and the installer, then
  runs `scripts/test-install.sh`: the snapshot's real tarball and `checksums.txt`
  served over `file://`, exercising OS/arch detection, download, sha256
  verification, install, the PATH hint, and the two refusal paths (tampered
  checksum, unlisted asset). That is what ties install.sh's asset naming to
  `.goreleaser.yaml`.
- A throwaway tag in a plain clone proves the version stamp:
  `git clone --branch <branch> . /tmp/sasash-proof && cd /tmp/sasash-proof &&
  git tag v0.0.0-test && make snapshot && dist/sasash_darwin_arm64_v8.0/sasash version`
  prints `v0.0.0-test` for all four targets (check the others with `go version -m`).

## Secrets

Only the automatic `GITHUB_TOKEN`; the release workflow grants it `contents: write`.

## Not yet

- Code signing and notarization. A curl|sh download never carries the macOS
  quarantine bit, so nothing blocks today. Revisit when there is an Apple
  developer identity.
- Homebrew. The human chose prebuilt + curl|sh only; no tap, no cask.

## Self-update

`sasash update` (SASASH.4.38) is the installer in Go: it resolves the latest tag through
the same `releases/latest` redirect, downloads `sasash_<version>_<os>_<arch>.tar.gz` and
`checksums.txt` from the same URLs (`SASASH_BASE_URL` overrides them, as for `install.sh`),
verifies the sha256, extracts the binary and renames it over the running executable —
temp file beside it, mode 0755, rename — after a y/N confirmation (`-y` skips it,
`--check` only reports, `sasash update v0.3.0` pins a tag). A directory it cannot write
sends the user back to the installer, which falls back to sudo. Transient 5xx answers from
GitHub's release path are retried three times, as `install.sh`'s `curl --retry` does.
- A `LICENSE` file: the repo has none, so the archives declare none. Add one
  before announcing the release.
