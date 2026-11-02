#!/bin/sh
# build_wiki_spa.sh builds LeafWiki's SPA and copies it into the wiki
# module's embed directory.
#
# The wiki's front end is LeafWiki's own React SPA (the upstream
# repository's ui/leafwiki-ui). riff does not vendor it: the build is
# produced here and dropped into pb/leafwiki/ui/dist, which the
# //go:embed in pb/leafwiki/ui_embed.go bakes into the binary. A checkout
# without a built bundle still compiles and serves the dependency-free
# fallback reader in pb/leafwiki/ui/index.html.
#
# Usage:
#   scripts/build_wiki_spa.sh [path-to-leafwiki-checkout]
#
# The checkout path defaults to /home/peter/github.com/leafwiki. The
# script refuses to guess anything else: a wrong path would silently
# produce an empty bundle.
#
# Requirements: Node 26 (LeafWiki's .nvmrc), npm, and a LeafWiki checkout
# with its package-lock.json.
set -eu

LEAFWIKI_DIR="${1:-/home/peter/github.com/leafwiki}"
UI_DIR="$LEAFWIKI_DIR/ui/leafwiki-ui"
TARGET_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)/pb/leafwiki/ui/dist"

if [ ! -d "$UI_DIR" ]; then
	echo "build_wiki_spa: no LeafWiki checkout at $UI_DIR" >&2
	echo "build_wiki_spa: pass the path as the first argument" >&2
	exit 1
fi

if [ ! -f "$UI_DIR/package-lock.json" ]; then
	echo "build_wiki_spa: $UI_DIR/package-lock.json is missing" >&2
	exit 1
fi

# Node 26 is required (LeafWiki's .nvmrc). The workstation keeps it
# outside the default PATH of non-interactive shells.
if [ -x "$HOME/.local/bin/node" ]; then
	PATH="$HOME/.local/bin:$PATH"
	export PATH
fi

NODE_VERSION="$(node -v 2>/dev/null || echo unknown)"
echo "build_wiki_spa: using node $NODE_VERSION"

cd "$UI_DIR"

if [ ! -d node_modules ]; then
	echo "build_wiki_spa: installing dependencies"
	npm ci --ignore-scripts
fi

# APP_VERSION short-circuits LeafWiki's own version resolution, which
# shells out to scripts/resolve-version.sh — a file that is present in a
# full checkout but not in the Docker build stage.
APP_VERSION="${APP_VERSION:-dev}" npm run build

if [ ! -f dist/index.html ]; then
	echo "build_wiki_spa: the build produced no dist/index.html" >&2
	exit 1
fi

rm -rf "$TARGET_DIR"
mkdir -p "$(dirname -- "$TARGET_DIR")"
cp -r dist "$TARGET_DIR"

echo "build_wiki_spa: copied $(du -sh "$TARGET_DIR" | cut -f1) into $TARGET_DIR"
echo "build_wiki_spa: rebuild riff to bake it in (go build ./...)"
