#!/bin/sh
# Render per-branch visual review artifacts for the current branch.
#
# Runs the demo tape against the built binary and copies the results into
# out/<branch-slug>/ so a reviewer sees the UI this branch produces without
# running it. out/ is self-gitignored; nothing tracked is touched (the
# committed demo/demo.gif is deliberately NOT updated — that is `make demo`).
#
# Unlike `make demo`, a missing vhs is an error here: whoever asks for review
# artifacts needs them.
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

if ! command -v vhs >/dev/null 2>&1; then
	echo "error: vhs not installed (brew install vhs)" >&2
	exit 1
fi

branch=$(git rev-parse --abbrev-ref HEAD)
case $branch in HEAD) branch=$(git rev-parse --short HEAD);; esac
slug=$(printf '%s' "$branch" | tr '/' '-')
dest="out/$slug"

make build
rm -rf demo/out
vhs demo/demo.tape

mkdir -p out
printf '*\n' > out/.gitignore
rm -rf "$dest"
cp -R demo/out "$dest"

echo
echo "review artifacts for $branch:"
ls "$PWD/$dest"/*
