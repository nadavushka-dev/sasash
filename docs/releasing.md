# Releasing sasash

Distribution is prebuilt binaries (SASASH.8): a `v*` tag triggers
`.github/workflows/release.yml`, which runs `make check` and then GoReleaser
(`.goreleaser.yaml`) to build darwin/linux x amd64/arm64, publish a GitHub Release
with four `.tar.gz` archives plus `checksums.txt`, and push a Homebrew cask when a
tap token is configured. `install.sh` at the repo root is the curl|sh onboarding
path and reads exactly those assets.

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

- `make snapshot` builds all four targets into `dist/` (gitignored) and renders the
  cask into `dist/homebrew/Casks/sasash.rb`. `dist/sasash_darwin_arm64_v8.0/sasash
  version` shows what the binary would report.
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

## Secrets and prerequisites

| what | needed for | notes |
|---|---|---|
| `GITHUB_TOKEN` | the release itself | automatic; the workflow grants `contents: write` |
| `HOMEBREW_TAP_GITHUB_TOKEN` | pushing the cask | optional repo secret: a fine-grained PAT with `contents: write` on `nadavushka-dev/homebrew-tap`. Unset means the cask is rendered but not pushed (`skip_upload` renders `true`; the skip itself is GoReleaser semantics, first exercised by the first real release) |
| `nadavushka-dev/homebrew-tap` repo | Homebrew install | must exist (empty is fine; GoReleaser commits `Casks/sasash.rb`). Users then run `brew install --cask nadavushka-dev/tap/sasash` |

Homebrew casks are macOS-only; Linux users take the curl|sh path. GoReleaser has
deprecated `brews` (a formula that installs a prebuilt binary) in favour of casks,
which is why the tap ships a cask.

## Not yet

- Code signing and notarization. macOS would otherwise quarantine a downloaded
  binary; the cask strips the bit in a post-install hook, and a curl|sh download
  never carries it. Revisit when there is an Apple developer identity.
- Self-update. Re-run the installer or `brew upgrade`.
- A `LICENSE` file: the repo has none, so neither the archives nor the cask
  declare one. Add one before announcing the release.
