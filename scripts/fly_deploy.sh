#!/bin/sh
#
# Shell choice: this script is intentionally POSIX sh (NOT bash). It runs
# unchanged under dash, bash, ksh, and zsh's `sh` emulation, so a `sh
# scripts/fly_deploy.sh` invocation from a zsh terminal, the
# `dash`-based default in Alpine / Debian / Ubuntu, or the `/bin/sh`
# that GoLand's "Shell Script" run config picks when it cannot find
# the shebang's interpreter all behave identically. There are no
# bash-only entry points inside.
#
# Deploy the most-recently-pushed `registry.fly.io/ravine:dev` image to
# the `ravine` Fly app, then run a smoke test against the caddy-routed
# health endpoint. The image must already be in the Fly registry
# (run scripts/dev_image_push.sh first, or the matching GoLand config).
#
# `fly deploy --image …` resolves the reference on the Fly side and
# never reads the local docker daemon — a local-only tag like
# `riff:dev` would fail with `Could not find image
# "docker.io/library/riff:dev"`. The push step is therefore load-bearing
# and not silently absorbed here.
#
# The smoke test path is caddy-routed (the
# `[http_service.checks].path` setting in fly.toml also points at it);
# `/api/health` at the root would 404 against caddy's `file_server` and
# 503 the whole edge.

set -eu

# --- Configuration (env-overridable) -----------------------------------------
: "${RIFF_FLY_APP:=ravine}"
: "${RIFF_FLY_REGISTRY:=registry.fly.io/ravine:dev}"
# `Caddyfile.fly.community` mounts riff under /community/; the default.
# Override to `https://<app>.fly.dev/api/health` for the root-mounted
# `Caddyfile.fly` layout (see the Caddyfile row in AGENTS.md).
: "${RIFF_FLY_SMOKE_PATH:=/community/api/health}"
: "${RIFF_FLY_SMOKE_HOST:=${RIFF_FLY_APP}.fly.dev}"
: "${RIFF_FLY_SMOKE_TIMEOUT:=10}"

# --- Preconditions -----------------------------------------------------------
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "${repo_root}" ]; then
  echo "Not inside a git working tree." >&2
  exit 1
fi
cd "${repo_root}"

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

# --- Deploy ------------------------------------------------------------------
echo ">> fly deploy -a ${RIFF_FLY_APP} --image ${RIFF_FLY_REGISTRY} -y"
fly deploy -a "${RIFF_FLY_APP}" --image "${RIFF_FLY_REGISTRY}" -y

# --- Smoke test --------------------------------------------------------------
echo
echo ">> Smoke test: curl -fsS -m ${RIFF_FLY_SMOKE_TIMEOUT} https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_SMOKE_PATH}"
curl -fsS -m "${RIFF_FLY_SMOKE_TIMEOUT}" "https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_SMOKE_PATH}"
echo
echo "OK: ${RIFF_FLY_SMOKE_PATH} on ${RIFF_FLY_SMOKE_HOST} is healthy."
