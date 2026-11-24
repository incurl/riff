#!/bin/sh
#
# Shell choice: this script is intentionally POSIX sh (NOT bash). It runs
# unchanged under dash, bash, ksh, and zsh's `sh` emulation, so a `sh
# scripts/dev_image_build.sh` invocation from a zsh terminal, the
# `dash`-based default in Alpine / Debian / Ubuntu, or the `/bin/sh`
# that GoLand's "Shell Script" run config picks when it cannot find
# the shebang's interpreter all behave identically. The single bash
# call below (release_version.sh) is invoked explicitly.
#
# Build the riff deploy image locally with `docker buildx build`.
#
# This is the development workflow wrapped as a single command so it can be
# driven from a GoLand "Shell Script" run configuration (see
# .run/Docker__build_riff_dev.xml) and from the terminal alike. The image is
# built for the host architecture by default (faster local dev), with an
# override knob for cross-arch builds. Nothing here pushes the image; the
# companion dev_image_push.sh / .run config does that, and the Fly deploy
# config consumes the pushed reference.
#
# Preconditions:
#   1. `server/frontend/dist/index.html` exists. The Dockerfile's
#      `//go:embed all:dist` directive in server/frontend/embed.go fails at
#      `go build` time without it. Build the SPA first with
#      `cd web && VITE_BASE_PATH=/community/ pnpm release`
#      (or `pnpm release` for the root-mounted layout).
#   2. `pb/flarum/frontend/ui/dist/{forum,admin}.{js,css}` and the locale
#      bundles exist. The forum's
#      `//go:embed all:ui` (pb/flarum/frontend/frontend.go) does NOT fail
#      without them — ui/index.html is tracked, so the embed always compiles —
#      and the binary then serves the dependency-free fallback shell instead of
#      the real front end. The stylesheet is the one that goes unnoticed: the
#      forum still renders, just unstyled, with a 404 for
#      /forum/assets/forum.css in the console.
#      THIS SCRIPT BUILDS IT (see "Front ends" below); the check is kept as
#      verification that the build actually produced the files.
#   3. Optional: `pb/leafwiki/ui/dist` for the wiki. This script builds it too,
#      but a host without a LeafWiki checkout is a warning rather than a
#      failure — the wiki falls back to a tracked reader that works.
#   4. `scripts/ravine` is a symlink (or a path) to the built ravine Jekyll
#      site. The Dockerfile imports it as a buildkit named context under the
#      name `ravine`; the symlink is the convention documented in AGENTS.md
#      so the same command works on every developer's machine.
#   5. `docker buildx` works on the host. The `default` builder is fine.
#
# Outputs:
#   - A local image tagged `riff:dev` (override with RIFF_IMAGE_TAG).
#   - The same layers are pushed by dev_image_push.sh when the deploy step
#     is ready.

set -eu

# --- Configuration (env-overridable) -----------------------------------------
# Match the prior successful build: amd64 covers the Fly target machine and
# most developer laptops. Override with RIFF_PLATFORM=linux/arm64 for
# Apple-silicon dev, etc.
: "${RIFF_PLATFORM:=linux/amd64}"
: "${RIFF_IMAGE_TAG:=riff:dev}"
# The dev ravine symlink under scripts/. AGENTS.md treats it as a pointer
# the build invocation can resolve with `readlink -f`; a real path also
# works.
: "${RIFF_RAVINE_LINK:=scripts/ravine}"
# Alpine mirror URLs the Dockerfile's apk install layer will probe.
# Defaults match the Dockerfile; override for a private mirror or a
# regional CDN when dl-cdn.alpinelinux.org is unreachable from the
# build host. Set RIFF_ALPINE_REPO_FALLBACK="" to disable the
# fallback and force the primary only.
: "${RIFF_ALPINE_REPO_PRIMARY:=https://dl-cdn.alpinelinux.org/alpine/v3.21}"
: "${RIFF_ALPINE_REPO_FALLBACK:=https://mirrors.tuna.tsinghua.edu.cn/alpine/v3.21}"
# Caddy needs /var/opt/riff on the deploy target, but for the local image
# the data dir is `./pb_data` (main.go's DefaultDataDir). Nothing to do here
# — the Dockerfile's CMD path runs `riff serve` with `directory=/var/opt/riff`
# in the supervisord program block, so the data dir only matters at runtime.

# --- Preconditions -----------------------------------------------------------
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "${repo_root}" ]; then
  echo "Not inside a git working tree." >&2
  exit 1
fi
cd "${repo_root}"

# --- Toolchain ----------------------------------------------------------------
# Checked BEFORE the front-end builds below: those take minutes, and a host
# without buildx would otherwise fail at the very end having done all of that
# work first.
if ! command -v docker >/dev/null 2>&1; then
  echo "docker not on PATH." >&2
  exit 1
fi
if ! docker buildx version >/dev/null 2>&1; then
  echo "docker buildx is not available (install/enable the buildx plugin)." >&2
  exit 1
fi

# --- Front ends ---------------------------------------------------------------
# Both front ends are gitignored build output that the binary bakes in with
# //go:embed, and both fall back to a TRACKED shell when the build is absent.
# What the fallback costs is what makes the two different:
#
#   - the forum's is not a working forum. pb/flarum/frontend/ui/index.html is a
#     dependency-free fallback, so the image serves that instead of the real
#     SPA — no plugin bundles, no stylesheet — and nothing fails at build time.
#     That is why the forum build is REQUIRED here.
#   - the wiki's is a working reader (pb/leafwiki/ui/index.html), so a host
#     without a LeafWiki checkout can still produce a usable image. That is why
#     the wiki build is BEST EFFORT.
#
# Set RIFF_SKIP_FRONTEND_BUILDS=1 to skip both, when this run follows builds you
# have just done by hand. The checks below still verify the output either way.
if [ "${RIFF_SKIP_FRONTEND_BUILDS:-0}" != "1" ]; then
  echo ">> building the forum front end"
  sh scripts/build_flarum_spa.sh

  echo ">> building the wiki front end"
  wiki_built=1
  # build_wiki_spa.sh takes the LeafWiki checkout as its only argument; forward
  # the override when one is set so the two knobs cannot disagree.
  if [ -n "${RIFF_LEAFWIKI_DIR:-}" ]; then
    sh scripts/build_wiki_spa.sh "${RIFF_LEAFWIKI_DIR}" || wiki_built=0
  else
    sh scripts/build_wiki_spa.sh || wiki_built=0
  fi

  if [ "${wiki_built}" != "1" ]; then
    cat >&2 <<EOF
warning: the wiki SPA was not built (scripts/build_wiki_spa.sh failed).

pb/leafwiki/ui/dist is absent, so the image bakes in the tracked fallback
reader (pb/leafwiki/ui/index.html) instead of LeafWiki's own SPA. That is a
working wiki, just not the real front end.

Point the build at a LeafWiki checkout to include it, or build it once with:

    RIFF_LEAFWIKI_DIR=/path/to/leafwiki \$0
    sh scripts/build_wiki_spa.sh /path/to/leafwiki
EOF
  fi
fi

if [ ! -f server/frontend/dist/index.html ]; then
  cat >&2 <<EOF
server/frontend/dist/index.html is missing — the SPA has not been built yet.

The Dockerfile bakes server/frontend/dist into the riff binary via
//go:embed all:dist (server/frontend/embed.go), so the build will fail
without it. Build the SPA first:

    cd web && pnpm release                  # root-mounted layout
    cd web && VITE_BASE_PATH=/community/ pnpm release   # caddy /community/ sub-path

Then re-run this script.
EOF
  exit 1
fi

# The forum front end. Its embed never fails without these files (ui/index.html
# is tracked, so //go:embed all:ui always compiles), so a missing or stale
# ui/dist produces an image that serves the fallback shell — or, worse, a
# real front end with no stylesheet.
forum_prefix="pb/flarum/frontend/ui/dist"
forum_missing=""
for f in forum.js forum-en.js admin.js admin-en.js forum.css admin.css; do
  [ -f "$forum_prefix/$f" ] || forum_missing="$forum_missing $f"
done
if [ -n "$forum_missing" ]; then
  cat >&2 <<EOF
pb/flarum/frontend/ui/dist is missing:$forum_missing

The forum front end has not been built (or was built before the stylesheets
were added). The Dockerfile bakes pb/flarum/frontend/ui/dist into the riff
binary via //go:embed all:ui (pb/flarum/frontend/frontend.go), and that embed
does NOT fail when the directory is incomplete: ui/index.html is tracked, so
the build succeeds and the image serves the fallback shell — the forum then
renders unstyled and /forum/assets/forum.css answers 404.

This script RUNS that build (see "Front ends" above), so reaching this point
means it ran and did NOT produce the files — most likely it failed part-way.
Run it on its own to see why:

    sh scripts/build_flarum_spa.sh

Or re-run this script without RIFF_SKIP_FRONTEND_BUILDS set.
EOF
  exit 1
fi

if [ ! -e "${RIFF_RAVINE_LINK}" ]; then
  cat >&2 <<EOF
${RIFF_RAVINE_LINK} does not exist. The Dockerfile imports the ravine
Jekyll site as a buildkit named context 'ravine'; the link (or any path)
must point at the built _site tree. The committed \`scripts/ravine\` is a
symlink to /home/peter/CLionProjects/ravine.1une.cc/website/_site on
the original developer's machine; on yours, either repoint it with
\`ln -sfn <your-_site> scripts/ravine\` or override:

    RIFF_RAVINE_LINK=/absolute/path/to/_site $0
EOF
  exit 1
fi
ravine_ctx="$(readlink -f "${RIFF_RAVINE_LINK}")"
if [ ! -d "${ravine_ctx}" ]; then
  echo "${RIFF_RAVINE_LINK} resolves to ${ravine_ctx}, which is not a directory." >&2
  exit 1
fi

# --- Compute build args ------------------------------------------------------
version="$(bash scripts/release_version.sh development-version)"
commit="$(git rev-parse HEAD)"

# --- Build -------------------------------------------------------------------
echo ">> docker buildx build \\"
echo "       --platform ${RIFF_PLATFORM} \\"
echo "       -f scripts/Dockerfile \\"
echo "       --build-context ravine=${ravine_ctx} \\"
echo "       --build-arg VERSION=${version} \\"
echo "       --build-arg COMMIT=${commit} \\"
echo "       --build-arg ALPINE_REPO_PRIMARY=${RIFF_ALPINE_REPO_PRIMARY} \\"
echo "       --build-arg ALPINE_REPO_FALLBACK=${RIFF_ALPINE_REPO_FALLBACK} \\"
echo "       -t ${RIFF_IMAGE_TAG} --load ."

exec docker buildx build \
  --platform "${RIFF_PLATFORM}" \
  -f scripts/Dockerfile \
  --build-context "ravine=${ravine_ctx}" \
  --build-arg "VERSION=${version}" \
  --build-arg "COMMIT=${commit}" \
  --build-arg "ALPINE_REPO_PRIMARY=${RIFF_ALPINE_REPO_PRIMARY}" \
  --build-arg "ALPINE_REPO_FALLBACK=${RIFF_ALPINE_REPO_FALLBACK}" \
  -t "${RIFF_IMAGE_TAG}" \
  --load \
  .
