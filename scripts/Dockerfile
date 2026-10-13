FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine AS backend
WORKDIR /backend-build

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files and download dependencies (cached layer)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code (use .dockerignore to exclude unnecessary files)
COPY . .

# Build the riff binary. The frontend must be pre-built into
# server/frontend/dist before invoking `docker build`; see
# `cd web && pnpm release` in AGENTS.md. The `.dockerignore`
# already excludes `web/`, so a `pnpm install` inside the
# container is not possible. The `//go:embed all:dist`
# directive in server/frontend/embed.go bakes the dist tree
# into the riff binary at this `go build` step, so the final
# runtime image does not need a separate `COPY` for the SPA.
#
# VERSION comes from `scripts/release_version.sh
# development-version` for development builds, or from the
# `YY.MM[.N][-rc.N]` release tag for tagged builds. COMMIT is
# the SHA-1 of the build commit.
ARG TARGETOS TARGETARCH VERSION COMMIT=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    : "${VERSION:?Pass VERSION from scripts/release_version.sh development-version or a release tag}" && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build \
      -trimpath \
      -ldflags="-s -w -X github.com/incurl/riff/internal/version.Version=${VERSION} -X github.com/incurl/riff/internal/version.Commit=${COMMIT} -extldflags '-static'" \
      -tags netgo,osusergo \
      -o riff \
      .

# ---------------------------------------------------------------------------
# Runtime image: caddy + supervisord + riff on Alpine 3.21.
#
# Layout: supervisord is PID 1; caddy and the riff entrypoint are its two
# child programs. Caddy binds 80/443 and reverse-proxies /community/* to
# riff on the loopback 8090. The Jekyll static site is imported via a
# buildkit named context (`COPY --from=ravine`) and served by caddy
# from /var/www/site. See the comment block above the `COPY --from=ravine`
# line below for the build invocation.
#
# The caddy + supervisord + reverse-proxy split is a deliberate
# capability trade: caddy needs CAP_NET_BIND_SERVICE to bind 80/443,
# but we drop all other capabilities and run riff under the nonroot
# user via the existing entrypoint.sh. The Fly.io and compose manifests
# add the same single capability.
# ---------------------------------------------------------------------------
FROM alpine:3.21 AS monolithic

# Install caddy and supervisord. caddy lives in the `community`
# repository (alpine:3.21 only enables `main` by default), so we add
# the community repo on the apk add line. We pin the packages to
# the exact versions that exist on Alpine 3.21 today so a rebuild
# three months from now still produces a bit-identical runtime.
#
# Versions are the v3.21 x86_64 values from pkgs.alpinelinux.org
# (verified 2026-10-13):
#   caddy            2.8.4-r7    (community)
#   supervisor       4.2.5-r5    (main; provides the `supervisord` binary)
#   tzdata           2026e-r0    (main)
#   btop             1.4.0-r0    (community; nicety for debugging)
#   ncdu             1.21-r0     (main; nicety for debugging)
#   ca-certificates  20260909-r0 (main; caddy's ACME client + riff TLS)
#   su-exec          0.2-r3      (main; used by scripts/entrypoint.sh)
#
# The runtime also needs tzdata (for log timestamps) and ca-certificates
# (for caddy's ACME client and riff's outbound TLS).
RUN apk add --no-cache \
      --repository=https://dl-cdn.alpinelinux.org/alpine/v3.21/main \
      --repository=https://dl-cdn.alpinelinux.org/alpine/v3.21/community \
      caddy=2.8.4-r7 \
      supervisor=4.2.5-r5 \
      tzdata=2026e-r0 \
      btop=1.4.0-r0 \
      ncdu=1.21-r0 \
      ca-certificates=20260909-r0 \
      su-exec=0.2-r3

# Create the runtime tree. /var/www/site holds the baked-in static
# site; /etc/caddy and /etc/supervisord hold the configs; /run is
# where supervisord puts its socket and pid file.
RUN addgroup -g 10001 -S nonroot && \
    adduser -u 10001 -S -G nonroot -h /var/opt/riff nonroot && \
    mkdir -p /var/opt/riff /usr/local/riff /var/www/site /etc/caddy /etc/supervisord /run && \
    chown -R nonroot:nonroot /var/opt/riff

# Copy the riff binary and the original riff entrypoint (which handles
# the UID 0 -> nonroot drop and RIFF_*_FILE env loading). The riff
# program in supervisord invokes entrypoint.sh, so the existing
# privilege-drop semantics are preserved.
COPY --from=backend /backend-build/riff /usr/local/riff/riff
COPY --from=backend --chmod=755 /backend-build/scripts/entrypoint.sh /usr/local/riff/entrypoint.sh

# Copy the supervisord entrypoint (this image's PID 1) and the caddy
# + supervisord configuration files.
COPY --from=backend --chmod=755 /backend-build/scripts/entrypoint-supervisord.sh /usr/local/riff/entrypoint-supervisord.sh
COPY --from=backend /backend-build/scripts/Caddyfile /etc/caddy/Caddyfile
COPY --from=backend /backend-build/scripts/supervisord.conf /etc/supervisord/supervisord.conf

# Copy the static site. The Jekyll output lives in a separate repo
# (`/home/peter/CLionProjects/ravine.1une.cc/website/_site/`) that
# is outside this build context, so we cannot COPY it through the
# build context tar (Docker preserves symlinks pointing outside the
# context as symlinks, and dereferencing them from a later stage
# fails with `not found`). Instead, we import the site as a
# buildkit named build context. The build invocation must pass:
#
#   --build-context ravine=/home/peter/CLionProjects/ravine.1une.cc/website/_site
#
# or use the committed `scripts/ravine` symlink to resolve the path
# on the host:
#
#   --build-context ravine="$(readlink -f scripts/ravine)"
#
# The `scripts/ravine` symlink is committed for the developer's
# convenience (it makes the path discovery one command away); the
# Dockerfile does not read it directly. The user runs
# `cd website && jekyll build` upstream to regenerate the tree
# before invoking docker build.
#
# CI builds (the release.yml workflow) pass an empty ravine context
# so the published image does not fail; the resulting image has an
# empty /var/www/site/, which caddy serves as a 404 (the riff API
# under /community/ is unaffected). When the ravine repo becomes
# fetchable from CI, replace the empty context with a checkout step.
COPY --from=ravine . /var/www/site/

# Run as root to bind privileged ports; caddy drops to its own user
# internally, and the riff program uses entrypoint.sh's UID 0 ->
# nonroot drop before exec.
USER root
WORKDIR /var/opt/riff

# Site domain. The operator overrides SITE_DOMAIN in compose / fly
# secrets / `docker run -e`. Defaulting to example.com keeps a bare
# `docker run` from crashing on an empty Caddyfile interpolation.
ENV SITE_DOMAIN=example.com \
    ACME_EMAIL=ops@example.com \
    TZ=UTC

# Data directory for PocketBase. Same path the previous image used;
# on Fly.io the persistent volume `riff_data` mounts at /var/opt/riff.
VOLUME /var/opt/riff

# caddy binds 80 and 443. The previous image EXPOSEd 8090 (riff);
# in this layout that port stays on the loopback only and caddy
# reverse-proxies to it from the public interfaces. Documenting 80
# + 443 here so `docker inspect` and friends reflect the public
# surface, not the loopback.
EXPOSE 80 443

# supervisord as PID 1, in the foreground. The entrypoint exports
# SITE_DOMAIN / ACME_EMAIL and execs /usr/bin/supervisord, which
# (because nodaemon=true) stays in the foreground and forwards
# signals to its children.
ENTRYPOINT ["/usr/local/riff/entrypoint-supervisord.sh"]
