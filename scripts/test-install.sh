#!/bin/sh
# End-to-end test for install.sh with no network and no goreleaser.
#
# Serves a release over file:// in the exact layout install.sh expects and checks:
# os/arch detection across the four targets and two unsupported ones (via a
# uname shim), download + sha256 verify + install, the PATH hint, that a
# tampered checksum is refused, and that an unlisted asset is refused.
#
# The release is the real goreleaser output when dist/ exists (`make snapshot`
# first — that is what pins install.sh's asset naming to .goreleaser.yaml);
# without dist/ it stages a go-built tarball for the host os/arch instead.
# Run by `make check-dist` and the ci.yml dist job.
#
# Needs curl: wget cannot fetch file:// URLs, so the download half of this test
# always goes through install.sh's curl branch (the wget branch is one standard
# `wget -q -O` line; its `latest` redirect parsing was checked by hand against
# a real GitHub repo with curl hidden from PATH).
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

command -v curl >/dev/null 2>&1 || {
	echo "test-install.sh: needs curl (file:// release)" >&2
	exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

pass=0
ok() { pass=$((pass + 1)); }
fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# --- os/arch detection through a uname shim -------------------------------
mkdir -p "$tmp/shim"
cat >"$tmp/shim/uname" <<'SH'
#!/bin/sh
case "$1" in
-s) echo "$FAKE_S" ;;
-m) echo "$FAKE_M" ;;
*) exit 2 ;;
esac
SH
chmod +x "$tmp/shim/uname"

detect() { # detect UNAME_S UNAME_M -> "os=.. arch=.." or non-zero
	FAKE_S=$1 FAKE_M=$2 PATH="$tmp/shim:$PATH" SASASH_VERSION=v9.9.9 \
		sh ./install.sh --dry-run 2>/dev/null | sed -n 's/^os=\(.*\)$/\1/p;s/^arch=\(.*\)$/\1/p' | paste -sd/ -
}

while read -r s m want; do
	got=$(detect "$s" "$m")
	[ "$got" = "$want" ] || fail "detect $s $m = $got, want $want"
	ok
done <<'TABLE'
Linux x86_64 linux/amd64
Linux amd64 linux/amd64
Linux aarch64 linux/arm64
Linux arm64 linux/arm64
Darwin arm64 darwin/arm64
Darwin x86_64 darwin/amd64
TABLE

while read -r s m; do
	if FAKE_S=$s FAKE_M=$m PATH="$tmp/shim:$PATH" SASASH_VERSION=v9.9.9 sh ./install.sh --dry-run >/dev/null 2>&1; then
		fail "detect $s $m succeeded, want unsupported"
	fi
	ok
done <<'TABLE'
FreeBSD amd64
Linux i686
MINGW64_NT-10.0 x86_64
TABLE

# --- version + url resolution ----------------------------------------------
out=$(SASASH_VERSION=0.1.0 SASASH_BASE_URL=https://example.invalid/r sh ./install.sh --dry-run 2>/dev/null)
echo "$out" | grep -q '^version=v0.1.0$' || fail "version without v prefix not normalised: $out"
echo "$out" | grep -q '^url=https://example.invalid/r/download/v0.1.0/sasash_0.1.0_[a-z]*_[a-z0-9]*\.tar\.gz$' || fail "url shape: $out"
ok

# --- stage the release for the host ----------------------------------------
case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "host $(uname -s) is not a sasash target" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "host arch $(uname -m) is not a sasash target" ;;
esac
if [ -f dist/checksums.txt ]; then
	set -- dist/sasash_*_"${os}_${arch}".tar.gz
	[ -f "$1" ] || fail "dist/ has no ${os}/${arch} tarball"
	asset=$(basename "$1")
	ver=${asset#sasash_}
	ver=${ver%"_${os}_${arch}.tar.gz"}
	tag="v$ver"
	rel="$tmp/release/download/$tag"
	mkdir -p "$rel"
	cp "$1" dist/checksums.txt "$rel/"
	source="goreleaser dist/ $tag"
else
	tag=v0.0.0-test
	asset="sasash_0.0.0-test_${os}_${arch}.tar.gz"
	rel="$tmp/release/download/$tag"
	mkdir -p "$rel" "$tmp/build"
	go build -o "$tmp/build/sasash" ./cmd/sasash
	tar -C "$tmp/build" -czf "$rel/$asset" sasash
	if command -v sha256sum >/dev/null 2>&1; then
		sum=$(sha256sum "$rel/$asset" | cut -d' ' -f1)
	else
		sum=$(shasum -a 256 "$rel/$asset" | cut -d' ' -f1)
	fi
	printf '%s  %s\n' "$sum" "$asset" >"$rel/checksums.txt"
	source="go build (no dist/)"
fi

run_install() { # run_install INSTALL_DIR [extra env via caller]
	SASASH_BASE_URL="file://$tmp/release" SASASH_VERSION=$tag SASASH_INSTALL_DIR=$1 SASASH_SETUP=0 \
		sh ./install.sh
}

# --- happy path: download, verify, install, PATH hint ----------------------
inst="$tmp/inst"
log=$(run_install "$inst" 2>&1) || fail "install failed: $log"
[ -x "$inst/sasash" ] || fail "binary not installed at $inst/sasash"
"$inst/sasash" version >/dev/null || fail "installed binary does not run"
echo "$log" | grep -q "installed sasash" || fail "no install confirmation: $log"
echo "$log" | grep -q "is not on your PATH" || fail "expected PATH hint: $log"
echo "$log" | grep -q "run \`sasash setup\`" || fail "expected setup hint: $log"
ok

# --- PATH hint absent when the dir is on PATH; reinstall over itself --------
log=$(PATH="$inst:$PATH" run_install "$inst" 2>&1) || fail "reinstall failed: $log"
echo "$log" | grep -q "is not on your PATH" && fail "unexpected PATH hint: $log"
ok

# --- tampered checksum is refused -------------------------------------------
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$asset" >"$rel/checksums.txt"
if log=$(run_install "$tmp/inst2" 2>&1); then fail "tampered checksum accepted: $log"; fi
echo "$log" | grep -q "sha256 mismatch" || fail "wrong refusal message: $log"
[ ! -e "$tmp/inst2/sasash" ] || fail "binary installed despite bad checksum"
ok

# --- asset missing from checksums.txt is refused ----------------------------
: >"$rel/checksums.txt"
if log=$(run_install "$tmp/inst3" 2>&1); then fail "unlisted asset accepted: $log"; fi
echo "$log" | grep -q "not listed" || fail "wrong refusal message: $log"
ok

# --- missing release asset is a clean failure --------------------------------
if log=$(SASASH_VERSION=v0.0.0-nope SASASH_BASE_URL="file://$tmp/release" SASASH_INSTALL_DIR="$tmp/inst4" SASASH_SETUP=0 sh ./install.sh 2>&1); then
	fail "missing asset accepted: $log"
fi
echo "$log" | grep -q "download failed" || fail "wrong failure message: $log"
ok

echo "install.sh: $pass checks passed ($os/$arch, release from $source)"
