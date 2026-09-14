#!/bin/sh
# install.sh — install a prebuilt sasash release (SASASH.8).
#
#   curl -fsSL https://raw.githubusercontent.com/nadavushka-dev/sasash/main/install.sh | sh
#
# `sh install.sh --help` lists the environment knobs and flags. No release exists
# until the first v* tag is pushed (docs/releasing.md); until then only a file://
# SASASH_BASE_URL works, and only with curl — see scripts/test-install.sh.
set -eu

REPO="nadavushka-dev/sasash"
BASE_URL="${SASASH_BASE_URL:-https://github.com/$REPO/releases}"

say() { printf 'sasash-install: %s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
	cat <<'EOT'
install.sh — install a prebuilt sasash release

  curl -fsSL https://raw.githubusercontent.com/nadavushka-dev/sasash/main/install.sh | sh

Picks the release asset for this machine (darwin/linux x amd64/arm64), verifies
its sha256 against the release's checksums.txt, installs the binary to
~/.local/bin (falling back to /usr/local/bin via sudo), prints a PATH hint when
needed, then offers `sasash setup`.

Environment:
  SASASH_VERSION      release tag to install, e.g. v0.1.0 (default: latest release)
  SASASH_INSTALL_DIR  install directory (default: ~/.local/bin)
  SASASH_BASE_URL     release base URL (default: https://github.com/nadavushka-dev/sasash/releases)
  SASASH_SETUP        1 = run `sasash setup -y` after installing, 0 = never ask, just hint
                      (default: ask on a terminal, otherwise print the hint)

Arguments:
  --dry-run   print the resolved target and URL, install nothing
  --help      this text

Needs curl or wget, tar, and sha256sum / shasum / openssl.
EOT
}

detect_os() {
	case "$(uname -s)" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	*) die "unsupported OS $(uname -s): sasash runs on macOS and Linux (on Windows, use WSL)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported architecture $(uname -m): amd64 and arm64 only" ;;
	esac
}

fetch() { # fetch URL DEST
	if have curl; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif have wget; then
		wget -q -O "$2" "$1"
	else
		die "need curl or wget"
	fi
}

# GitHub redirects <releases>/latest to <releases>/tag/<tag>; following the
# redirect avoids the API and its rate limit. --retry, as fetch has it: the
# release path answers 504 now and then, and one retry is all it takes.
latest_tag() {
	url=
	if have curl; then
		url=$(curl -fsSIL --retry 3 -o /dev/null -w '%{url_effective}' "$BASE_URL/latest") || url=
	elif have wget; then
		url=$(wget -q -S --spider "$BASE_URL/latest" 2>&1 | sed -n 's/^ *Location: *//p' | tail -n 1)
	fi
	case "$url" in
	*/tag/*) echo "${url##*/tag/}" ;;
	*) die "no release found at $BASE_URL/latest (set SASASH_VERSION to pin one)" ;;
	esac
}

resolve_tag() {
	case "${SASASH_VERSION:-latest}" in
	latest) latest_tag ;;
	v*) echo "$SASASH_VERSION" ;;
	*) echo "v$SASASH_VERSION" ;;
	esac
}

sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | cut -d' ' -f1
	elif have shasum; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif have openssl; then
		openssl dgst -sha256 "$1" | sed 's/.*= //'
	else
		die "need sha256sum, shasum, or openssl to verify the download"
	fi
}

verify_sha256() { # verify_sha256 FILE CHECKSUMS NAME
	want=$(awk -v n="$3" '$2 == n { print $1 }' "$2")
	[ -n "$want" ] || die "$3 is not listed in checksums.txt"
	got=$(sha256_of "$1")
	[ "$got" = "$want" ] || die "sha256 mismatch for $3: want $want, got $got"
}

writable_dir() { mkdir -p "$1" 2>/dev/null && [ -w "$1" ]; }

as_root() {
	if [ -n "$SUDO" ]; then sudo "$@"; else "$@"; fi
}

# install_bin SRC: places SRC at $dir/sasash, atomically (tmp + mv). Falls back
# from the default ~/.local/bin to /usr/local/bin, and to sudo when even that is
# not writable. Sets $dir to where the binary ended up.
install_bin() {
	SUDO=
	if ! writable_dir "$dir"; then
		if [ -z "${SASASH_INSTALL_DIR:-}" ]; then
			say "$dir is not writable; falling back to /usr/local/bin"
			dir=/usr/local/bin
		fi
		if ! writable_dir "$dir"; then
			have sudo || die "$dir is not writable and sudo is not installed; set SASASH_INSTALL_DIR"
			say "$dir is not writable; using sudo"
			SUDO=sudo
		fi
	fi
	as_root mkdir -p "$dir"
	as_root cp "$1" "$dir/.sasash.tmp"
	as_root chmod 0755 "$dir/.sasash.tmp"
	as_root mv -f "$dir/.sasash.tmp" "$dir/sasash"
}

on_path() {
	case ":$PATH:" in
	*":$1:"*) return 0 ;;
	esac
	return 1
}

path_hint() {
	on_path "$1" && return 0
	say "note: $1 is not on your PATH; add it to your shell rc:"
	say "  export PATH=\"$1:\$PATH\""
}

# offer_setup BIN: `sasash setup` is the one-time bootstrap (a single confirmed
# Include line in ~/.ssh/config). stdin is the piped script under curl|sh, so the
# prompt and the setup confirmation both talk to /dev/tty.
offer_setup() {
	case "${SASASH_SETUP:-}" in
	1)
		"$1" setup -y
		return
		;;
	0)
		say "next: run \`sasash setup\`"
		return
		;;
	esac
	if [ -t 2 ] && { : </dev/tty; } 2>/dev/null; then
		printf 'Run "sasash setup" now (adds one Include line to ~/.ssh/config, asks first)? [y/N] ' >/dev/tty
		read -r answer </dev/tty || answer=
		case "$answer" in
		y | Y | yes | YES) "$1" setup </dev/tty ;;
		*) say "next: run \`sasash setup\`" ;;
		esac
	else
		say "next: run \`sasash setup\`"
	fi
}

main() {
	dry_run=0
	for arg in "$@"; do
		case "$arg" in
		--dry-run) dry_run=1 ;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "unknown argument $arg (try --help)" ;;
		esac
	done

	os=$(detect_os) || exit 1
	arch=$(detect_arch) || exit 1
	tag=$(resolve_tag) || exit 1
	asset="sasash_${tag#v}_${os}_${arch}.tar.gz"
	url="$BASE_URL/download/$tag/$asset"
	dir="${SASASH_INSTALL_DIR:-$HOME/.local/bin}"

	if [ "$dry_run" = 1 ]; then
		printf 'os=%s\narch=%s\nversion=%s\nurl=%s\ninstall_dir=%s\n' "$os" "$arch" "$tag" "$url" "$dir"
		exit 0
	fi

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t sasash)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "downloading $url"
	fetch "$url" "$tmp/$asset" || die "download failed: $url"
	fetch "$BASE_URL/download/$tag/checksums.txt" "$tmp/checksums.txt" || die "download failed: checksums.txt"
	verify_sha256 "$tmp/$asset" "$tmp/checksums.txt" "$asset"
	tar -xzf "$tmp/$asset" -C "$tmp" sasash

	install_bin "$tmp/sasash"
	ver=$("$dir/sasash" version) || die "$dir/sasash does not run on this machine"
	say "installed sasash $ver to $dir/sasash"
	path_hint "$dir"
	offer_setup "$dir/sasash"
}

main "$@"
