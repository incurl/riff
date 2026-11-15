#!/bin/sh
# Build the forum's front end and stage it for the Go binary.
#
# The Flarum front end is vendored from upstream (see pb/flarum/README.md).
# It is a Yarn workspace upstream; riff drives npm, which needs the
# legacy peer resolver for the upstream hoisting assumptions.
#
# The build output is copied into pb/flarum/frontend/ui/dist, which is
# what the Go binary embeds. The directory is gitignored (it is build
# output), so the tracked fallback shell in pb/flarum/frontend/ui is
# what a fresh clone compiles against.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FRONTEND="$ROOT/pb/flarum/frontend"
TARGET="$ROOT/pb/flarum/frontend/ui/dist"

export PATH="$HOME/.local/bin:$PATH"

cd "$FRONTEND"
npm install --legacy-peer-deps --no-audit --no-fund
npm run build

rm -rf "$TARGET"
mkdir -p "$TARGET"
cp -R "$FRONTEND/packages/core/dist/." "$TARGET/"

echo "The forum front end is staged in $TARGET"
