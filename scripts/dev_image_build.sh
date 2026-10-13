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
#   2. `scripts/ravine` is a symlink (or a path) to the built ravine Jekyll
#      site. The Dockerfile imports it as a buildkit named context under the
#      name `ravine`; the symlink is the convention documented in AGENTS.md
#      so the same command works on every developer's machine.
#   3. `docker buildx` works on the host. The `default` builder is fine.
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

if ! command -v docker >/dev/null 2>&1; then
  echo "docker not on PATH." >&2
  exit 1
fi
if ! docker buildx version >/dev/null 2>&1; then
  echo "docker buildx is not available (install/enable the buildx plugin)." >&2
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
echo "       -t ${RIFF_IMAGE_TAG} --load ."

exec docker buildx build \
  --platform "${RIFF_PLATFORM}" \
  -f scripts/Dockerfile \
  --build-context "ravine=${ravine_ctx}" \
  --build-arg "VERSION=${version}" \
  --build-arg "COMMIT=${commit}" \
  -t "${RIFF_IMAGE_TAG}" \
  --load \
  .
