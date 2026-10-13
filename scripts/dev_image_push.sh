#!/bin/sh
#
# Shell choice: this script is intentionally POSIX sh (NOT bash). It runs
# unchanged under dash, bash, ksh, and zsh's `sh` emulation, so a `sh
# scripts/dev_image_push.sh` invocation from a zsh terminal, the
# `dash`-based default in Alpine / Debian / Ubuntu, or the `/bin/sh`
# that GoLand's "Shell Script" run config picks when it cannot find
# the shebang's interpreter all behave identically. There are no
# bash-only entry points inside.
#
# Tag the locally-built `riff:dev` image and push it to the Fly container
# registry, ready for `fly deploy --image …`. The previous build must have
# produced the `riff:dev` tag locally (run scripts/dev_image_build.sh
# first, or the matching GoLand config).
#
# `fly auth docker` is run before the push so docker is logged in to the
# Fly registry with a short-lived token. The token expires after the
# session, so re-run the script (or just the auth line) if the push is
# rejected with "unauthorized".

set -eu

# --- Configuration (env-overridable) -----------------------------------------
: "${RIFF_IMAGE_TAG:=riff:dev}"
: "${RIFF_FLY_REGISTRY:=registry.fly.io/ravine:dev}"
: "${RIFF_FLY_APP:=ravine}"

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "${repo_root}" ]; then
  echo "Not inside a git working tree." >&2
  exit 1
fi
cd "${repo_root}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker not on PATH." >&2
  exit 1
fi
# The official Fly installer drops `flyctl` at ~/.fly/bin/flyctl, which is
# NOT on the default PATH that GoLand / CI / cron / non-interactive shells
# inherit (it's only set in interactive shell rc files like ~/.zshrc).
# Pick it up from there so the script works regardless of how it's invoked.
if ! command -v fly >/dev/null 2>&1; then
  for _fly_bin in "$HOME/.fly/bin" "$HOME/bin" "/opt/homebrew/bin" "/usr/local/bin"; do
    if [ -x "$_fly_bin/fly" ]; then
      export PATH="$_fly_bin:$PATH"
      break
    fi
  done
fi
if ! command -v fly >/dev/null 2>&1; then
  echo "fly (flyctl) not on PATH; install it from https://fly.io/docs/flyctl/install/." >&2
  exit 1
fi

if ! docker image inspect "${RIFF_IMAGE_TAG}" >/dev/null 2>&1; then
  cat >&2 <<EOF
Local image ${RIFF_IMAGE_TAG} does not exist. Run scripts/dev_image_build.sh
(or its GoLand config) to build it first.
EOF
  exit 1
fi

echo ">> fly auth docker  # short-lived token for ${RIFF_FLY_REGISTRY}"
fly auth docker

echo ">> docker tag ${RIFF_IMAGE_TAG} ${RIFF_FLY_REGISTRY}"
docker tag "${RIFF_IMAGE_TAG}" "${RIFF_FLY_REGISTRY}"

echo ">> docker push ${RIFF_FLY_REGISTRY}"
docker push "${RIFF_FLY_REGISTRY}"

echo
echo "Image pushed. Deploy with:"
echo "    fly deploy -a ${RIFF_FLY_APP} --image ${RIFF_FLY_REGISTRY} --wait-timeout 60 -y"
