#!/usr/bin/env sh
# entrypoint for the caddy+supervisord+riff image.
#
# Responsibilities:
#   1. Apply ${SITE_DOMAIN} default if the operator did not set it.
#      Caddy reads the env var at `caddy run --environ` startup, so
#      it must be exported before exec.
#   2. exec supervisord as PID 1. supervisord is configured to run
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

# exec so supervisord becomes PID 1 and gets the container's
# signals. `nodaemon=true` keeps it in the foreground, which is
# what we want for a container init.
exec /usr/bin/supervisord -c /etc/supervisord/supervisord.conf
