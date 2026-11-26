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
# The smoke test path is the *sub-path* the SPA is mounted under
# (`/memos/api/health` for the default Caddyfile.fly.community layout).
# It is caddy-routed, exactly like the `[http_service.checks].path`
# setting in fly.toml, so it proves the whole chain: Fly edge -> caddy ->
# prefix strip -> riff -> PocketBase. A root `/api/health` is routed too
# (see smoke test 4), but it exercises a different chain and would not
# catch a broken /memos/* strip.

set -eu

# --- Configuration (env-overridable) -----------------------------------------
: "${RIFF_FLY_APP:=ravine}"
: "${RIFF_FLY_REGISTRY:=registry.fly.io/ravine:dev}"
# `Caddyfile.fly.community` mounts riff under /memos/; the default.
# Override to `https://<app>.fly.dev/api/health` for the root-mounted
# `Caddyfile.fly` layout (see the Caddyfile row in AGENTS.md).
: "${RIFF_FLY_SMOKE_PATH:=/memos/api/health}"
: "${RIFF_FLY_SMOKE_HOST:=${RIFF_FLY_APP}.fly.dev}"
# The forum is mounted at /forum on riff's own listener, so this path is
# the same in EVERY caddy layout (it is a riff mount point, not a caddy
# layout choice). It is a knob only so a future re-mount stays testable.
: "${RIFF_FLY_FORUM_PATH:=/forum/}"
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
# Four checks, all must pass:
#   1. The caddy-routed riff health endpoint (SPA sub-path mount is alive).
#   2. The static site is reachable at the document root. `Caddyfile.fly.community`
#      (the default) serves the baked Jekyll _site/ at /, so a GET / must
#      return 200. A 308 to /memos/ means the @spa exclusion list is
#      missing the bare path and a future Caddyfile edit has regressed the
#      same bug as the "caddy redirects root visits" incident on 2026-10-14.
#
#   3. The forum module answers at /forum/ (a riff mount, so its prefix
#      must reach riff UNCHANGED). A 308 to /memos/forum means the
#      @spa exclusion list in Caddyfile.fly.community lost /forum, and
#      stripping the prefix would 404 every absolute /forum/assets/...
#      bundle URL the SPA shell ships.
#
#   4. PocketBase's OWN endpoints answer at the host ROOT: `/_/` (the
#      dashboard shell) and `/api/health` (the REST API). Both must be
#      200 WITH NO REDIRECT. A 308 to /memos/_/ or /memos/api/health
#      means the root proxy blocks (/`/_`, `/_/*`, `/api`, `/api/*`) are
#      missing or the @spa matcher is not excluding them — the URLs
#      PocketBase's own docs, its first-boot install link and every API
#      client use. A client that does not follow a redirect on POST never
#      reaches the API at all in that state.
#
# Checks 2 and 3 use -m timeout (not -fsS) because `-f` makes curl exit non-zero
# on 3xx and we explicitly want to inspect the 308 to assert which
# redirect target caddy chose. The case-statement below greps
# %{redirect_url} for /memos/ to distinguish the bug from a healthy
# 308 elsewhere (e.g. a future `redir` that canonicalises trailing
# slashes on the static site itself).

echo
echo ">> Smoke test 1/4: curl -fsS -m ${RIFF_FLY_SMOKE_TIMEOUT} https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_SMOKE_PATH}"
curl -fsS -m "${RIFF_FLY_SMOKE_TIMEOUT}" "https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_SMOKE_PATH}"
echo
echo "OK: ${RIFF_FLY_SMOKE_PATH} on ${RIFF_FLY_SMOKE_HOST} is healthy."

echo
echo ">> Smoke test 2/4: curl -sS -m ${RIFF_FLY_SMOKE_TIMEOUT} -o /dev/null -w 'http_code=%{http_code} redirect_url=%{redirect_url}\\n' https://${RIFF_FLY_SMOKE_HOST}/"
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
      *"/memos"*|*"/memos/"*)
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

echo
echo ">> Smoke test 3/4: curl -sS -m ${RIFF_FLY_SMOKE_TIMEOUT} -o /dev/null -w 'http_code=%{http_code} redirect_url=%{redirect_url}\n' https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_FORUM_PATH}"
_forum_probe=$(curl -sS -m "${RIFF_FLY_SMOKE_TIMEOUT}" -o /dev/null -w 'http_code=%{http_code} redirect_url=%{redirect_url}' "https://${RIFF_FLY_SMOKE_HOST}${RIFF_FLY_FORUM_PATH}" || true)
echo "${_forum_probe}"
_forum_code=$(echo "${_forum_probe}" | sed -n 's/^http_code=\([0-9][0-9][0-9]\).*/\1/p')
_forum_redirect=$(echo "${_forum_probe}" | sed -n 's/^.*redirect_url=\(.*\)$/\1/p')
case "${_forum_code}" in
  200)
    echo "OK: ${RIFF_FLY_FORUM_PATH} on ${RIFF_FLY_SMOKE_HOST} serves the forum shell."
    ;;
  3[0-9][0-9])
    case "${_forum_redirect}" in
      *"/memos"*)
        echo "FAIL: ${RIFF_FLY_FORUM_PATH} on ${RIFF_FLY_SMOKE_HOST} is being redirected into the SPA." >&2
        echo "      The Caddyfile.fly.community @spa matcher is missing /forum and" >&2
        echo "      /forum/* from its 'not path' exclusion list, so the forum sibling" >&2
        echo "      is 308'd to the wrong app. Add them next to /wiki and /wiki/*." >&2
        exit 1
        ;;
      *)
        echo "FAIL: ${RIFF_FLY_FORUM_PATH} on ${RIFF_FLY_SMOKE_HOST} returned ${_forum_code} -> ${_forum_redirect}" >&2
        echo "      (expected 200; the forum is a verbatim-proxied sibling of /memos/)." >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "FAIL: ${RIFF_FLY_FORUM_PATH} on ${RIFF_FLY_SMOKE_HOST} returned ${_forum_code}" >&2
    echo "      (expected 200). Check the caddy /forum matcher and that riff's" >&2
    echo "      forum module is registered (RIFF_FORUM_BASE_PATH defaults to /forum)." >&2
    exit 1
    ;;
esac

echo
echo ">> Smoke test 4/4: curl -sS -m ${RIFF_FLY_SMOKE_TIMEOUT} -o /dev/null -w 'http_code=%{http_code} redirect_url=%{redirect_url}\\n' https://${RIFF_FLY_SMOKE_HOST}/_/ and /api/health"

# PocketBase's own surface, at the ROOT of the host. Both paths are
# served VERBATIM by the caddy blocks for /_ , /_/* , /api and /api/* ,
# so a redirect here means those blocks are missing or the @spa matcher
# is not excluding them.
for _pb_path in /_/ /api/health; do
  _pb_probe=$(curl -sS -m "${RIFF_FLY_SMOKE_TIMEOUT}" -o /dev/null -w 'http_code=%{http_code} redirect_url=%{redirect_url}' "https://${RIFF_FLY_SMOKE_HOST}${_pb_path}" || true)
  echo "${_pb_path} -> ${_pb_probe}"
  _pb_code=$(echo "${_pb_probe}" | sed -n 's/^http_code=\([0-9][0-9][0-9]\).*/\1/p')
  _pb_redirect=$(echo "${_pb_probe}" | sed -n 's/^.*redirect_url=\(.*\)$/\1/p')
  case "${_pb_code}" in
    200)
      ;;
    3[0-9][0-9])
      echo "FAIL: ${_pb_path} on ${RIFF_FLY_SMOKE_HOST} is being redirected -> ${_pb_redirect}" >&2
      echo "      PocketBase's own endpoints must answer at the ROOT of the host," >&2
      echo "      verbatim. Add the /_ , /_/* , /api and /api/* handle blocks to" >&2
      echo "      scripts/Caddyfile.fly.community AND those four paths to the @spa" >&2
      echo "      matcher's 'not path' exclusion list, or the SPA redirect wins." >&2
      exit 1
      ;;
    *)
      echo "FAIL: ${_pb_path} on ${RIFF_FLY_SMOKE_HOST} returned ${_pb_code}" >&2
      echo "      (expected 200: the PocketBase dashboard / REST API at the root)." >&2
      exit 1
      ;;
  esac
done
echo "OK: /_/ and /api/health on ${RIFF_FLY_SMOKE_HOST} reach PocketBase at the root."
