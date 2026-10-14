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

# --- Smoke tests -------------------------------------------------------------
# Two checks, both must pass:
#   1. The caddy-routed riff health endpoint (SPA sub-path mount is alive).
#   2. The static site is reachable at the document root. `Caddyfile.fly.community`
#      (the default) serves the baked Jekyll _site/ at /, so a GET / must
#      return 200. A 308 to /community/ means the @spa exclusion list is
#      missing the bare path and a future Caddyfile edit has regressed the
#      same bug as the "caddy redirects root visits" incident on 2026-10-14.
#
# Check 2 uses -m timeout (not -fsS) because `-f` makes curl exit non-zero
# on 3xx and we explicitly want to inspect the 308 to assert which
# redirect target caddy chose. The case-statement below greps
# %{redirect_url} for /community/ to distinguish the bug from a healthy
# 308 elsewhere (e.g. a future `redir` that canonicalises trailing
# slashes on the static site itself).

echo
echo ">> Smoke test 1/2: curl -fsS -m ${RIFF_FLY_SMOKE_TIMEOUT} https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_SMOKE_PATH}"
curl -fsS -m "${RIFF_FLY_SMOKE_TIMEOUT}" "https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_SMOKE_PATH}"
echo
echo "OK: ${RIFF_FLY_SMOKE_PATH} on ${RIFF_FLY_SMOKE_HOST} is healthy."

echo
echo ">> Smoke test 2/2: curl -sS -m ${RIFF_FLY_SMOKE_TIMEOUT} -o /dev/null -w 'http_code=%{http_code} redirect_url=%{redirect_url}\\n' https://${RIFF_FLY_SMOKE_HOST}/"
_root_probe=$(curl -sS -m "${RIFF_FLY_SMOKE_TIMEOUT}" -o /dev/null -w 'http_code=%{http_code} redirect_url=%{redirect_url}' "https://${RIFF_FLY_SMOKE_HOST}/" || true)
echo "${_root_probe}"
_root_code=$(echo "${_root_probe}" | sed -n 's/^http_code=\([0-9][0-9][0-9]\).*/\1/p')
_root_redirect=$(echo "${_root_probe}" | sed -n 's/^.*redirect_url=\(.*\)$/\1/p')
case "${_root_code}" in
  200)
    echo "OK: / on ${RIFF_FLY_SMOKE_HOST} serves the static site."
    ;;
  3[0-9][0-9])
    case "${_root_redirect}" in
      *"/community"*|*"/community/"*)
        echo "FAIL: / on ${RIFF_FLY_SMOKE_HOST} is being redirected to the SPA." >&2
        echo "      The Caddyfile.fly.community @spa matcher is missing the bare" >&2
        echo "      path '/' from its 'not path' exclusion list. Add it next to" >&2
        echo "      /index.html (see scripts/Caddyfile.fly.community line 144)." >&2
        exit 1
        ;;
      *)
        echo "FAIL: / on ${RIFF_FLY_SMOKE_HOST} returned ${_root_code} -> ${_root_redirect}" >&2
        echo "      (expected 200 with the static site body, or a static-site-canonicalising 308)." >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "FAIL: / on ${RIFF_FLY_SMOKE_HOST} returned ${_root_code}" >&2
    echo "      (expected 200 with the static site body)." >&2
    exit 1
    ;;
esac
