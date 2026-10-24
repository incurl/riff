#!/usr/bin/env sh
# entrypoint for the caddy+supervisord+riff image.
#
# Responsibilities:
#   1. Apply ${SITE_DOMAIN} default if the operator did not set it.
#      Caddy reads the env var at `caddy run --environ` startup, so
#      it must be exported before exec.
#   2. Apply ${CADDYFILE} default (`Caddyfile`, the ACME variant that
#      scripts/compose.yaml uses; `Caddyfile.fly` selects the
#      plain-HTTP, host-agnostic variant for a platform that
#      terminates TLS at its own edge with the riff SPA at the root;
#      `Caddyfile.fly.community` is the same edge-proxy shape but
#      with the static site at the root and riff under /community/
#      — see fly.toml). supervisord expands the value into the
#      caddy program's command line, and it refuses to start when a
#      %(ENV_X)s token references an unset variable, so the default
#      must be exported before exec.
#   3. exec supervisord as PID 1. supervisord is configured to run
#      in the foreground (`nodaemon=true`), so it stays PID 1 and
#      Docker's signal handling (SIGTERM -> caddy/riff) works.
#
# We do NOT do the UID 0 -> nonroot drop here. The previous
# `entrypoint.sh` does that for the riff-only image, but in this
# layout supervisord must run as root so it can fork caddy (which
# binds 80/443) and then the riff child (which drops via su-exec).
# Dropping caddy/riff to the nonroot user happens in their program
# entries, not at PID 1.

set -e

# SITE_DOMAIN: which public DNS name caddy should issue a Let's
# Encrypt cert for. Defaults to example.com so a fresh `docker
# build && docker run` doesn't crash on an empty value; the
# operator overrides this in compose / fly secrets / `docker run -e`.
: "${SITE_DOMAIN:=example.com}"
export SITE_DOMAIN

# ACME_EMAIL: email caddy registers with Let's Encrypt. Same default
# rationale as SITE_DOMAIN.
: "${ACME_EMAIL:=ops@example.com}"
export ACME_EMAIL

# CADDYFILE: which Caddyfile the caddy program loads. All three files
# are baked into the image at /etc/caddy/, and supervisord
# interpolates this value into the caddy command line. Defaults to
# the ACME variant so a bare `docker run` matches scripts/compose.yaml;
# deployments behind a TLS-terminating edge (Fly.io) set
# CADDYFILE=Caddyfile.fly (riff SPA at the root) or
# CADDYFILE=Caddyfile.fly.community (static site at the root, riff
# at /community/).
: "${CADDYFILE:=Caddyfile}"
export CADDYFILE

# RIFF_FTS: serve memo content search from the SQLite FTS5 index
# instead of a LIKE scan. OFF unless the operator sets it.
#
# No default is exported on purpose, so there is one less place for the
# value to be set implicitly: unset and an explicit "0" are identical
# to envBool, and leaving it unset keeps the override visible in the
# operator's compose file or fly.toml rather than buried here.
#
# The index is always built (migration 1910000000_create_memos_fts),
# so the flag only decides whether it is *queried*. With it off you pay
# the write cost of maintaining the index and get none of the query
# benefit.
#
# The behaviour difference: FTS folds case across the whole Unicode
# range and strips diacritics, so "cafe" finds "café" and "блины"
# finds "Блины" — a LIKE scan folds ASCII only, so those searches
# returned nothing. FTS matches whole tokens rather than substrings,
# so "カフェ" does not match "カフェで" (use "カフェ*"). Negated
# searches and startsWith/endsWith keep using LIKE and are unaffected.
#
# Read once by the riff binary at startup via os.Getenv, so changing it
# needs a container restart, never a rebuild.
#
# : "${RIFF_FTS:=0}"
# export RIFF_FTS

# exec so supervisord becomes PID 1 and gets the container's
# signals. `nodaemon=true` keeps it in the foreground, which is
# what we want for a container init.
exec /usr/bin/supervisord -c /etc/supervisord/supervisord.conf
