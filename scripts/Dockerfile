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
# child programs. Two Caddyfiles ship in the image. The default
# /etc/caddy/Caddyfile binds 80/443, reverse-proxies /community/* to riff on
# the loopback 8090 and serves the baked-in static site from /var/www/site
# for everything else — that is what scripts/compose.yaml runs.
# /etc/caddy/Caddyfile.fly is the plain-HTTP, host-agnostic variant for a
# platform that terminates TLS at its own edge and hands the app the root of
# its hostname (Fly.io; see fly.toml). scripts/entrypoint-supervisord.sh
# selects between them through CADDYFILE, and the caddy program always
# passes an explicit --adapter caddyfile (see scripts/supervisord.conf):
# caddy only infers that adapter from a file whose base name is exactly
# `Caddyfile`, so `Caddyfile.fly` would otherwise abort at startup with
# "ambiguous config file format; please specify adapter (use --adapter)".
# The Jekyll static site is imported
# via a buildkit named context (`COPY --from=ravine`); see the comment block
# above that line for the build invocation.
#
# The OpenAPI document the MCP service reads at startup is copied into the
# image at a path MCP_OPENAPI_SPEC points at, because the riff child runs
# with a working directory of /var/opt/riff (where the data volume lives) and
# would otherwise look for ./proto/gen/openapi.yaml, find nothing, and exit
# through main.go's log.Fatal.
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
#   tmux             3.5a-r0     (main; nicety for debugging)
#   htop             3.3.0-r0    (main; nicety for debugging)
#   ncdu             1.21-r0     (main; nicety for debugging)
#   ca-certificates  20260909-r0 (main; caddy's ACME client + riff TLS)
#   su-exec          0.2-r3      (main; used by scripts/entrypoint.sh)
#
# The runtime also needs tzdata (for log timestamps) and ca-certificates
# (for caddy's ACME client and riff's outbound TLS).
#
# The apk add also needs to handle the case where the default CDN
# (dl-cdn.alpinelinux.org) is unreachable from the build host — for
# example on a builder sitting behind a network egress that times out
# on every dl-cdn.*.alpinelinux.org address. In that situation the
# build fails with exit code 9 (apk "unable to select packages" after
# a stream of "temporary error (try again later)" warnings). The
# buildkit builder cannot fall back on its own, so the Dockerfile
# does it: a tiny probe wget-spiders a fallback mirror (TUNA's
# mirrors.tuna.tsinghua.edu.cn) and substitutes it as the apk
# repository when the primary CDN does not respond within 3 s. TUNA
# mirrors the v3.21 main and community trees bit-for-bit (verified
# 2026-10-17: the APKINDEX.tar.gz files match the official CDN's at
# the same SHA, and every pinned package is present at the same
# version), so a build pulled from TUNA is byte-identical to one
# pulled from dl-cdn.
#
# The probe sends a User-Agent of "libfetch/2.0" rather than busybox
# wget's default. TUNA's CDN edge enforces a UA allow-list that
# accepts canonical `Name/Version` strings (libfetch, curl, Wget,
# libwww-perl) but 403s the bare `Wget` that busybox sends by default.
# libfetch/2.0 is exactly the User-Agent alpine apk's own libfetch
# fetcher sends (apk-tools/libfetch/http.c, _LIBFETCH_VER), so a
# URL the probe can reach is one apk can reach. dl-cdn accepts any
# UA, so the primary probe works without the override; the fallback
# probe only needs it because TUNA enforces it.
#
# Set ALPINE_REPO_PRIMARY=… and/or ALPINE_REPO_FALLBACK=… to
# override the defaults (for example, point both at a private
# mirror).
ARG ALPINE_REPO_PRIMARY=https://dl-cdn.alpinelinux.org/alpine/v3.21
ARG ALPINE_REPO_FALLBACK=https://mirrors.tuna.tsinghua.edu.cn/alpine/v3.21
RUN set -eux; \
    if wget -q -T 3 --tries=1 -U "libfetch/2.0" --spider \
         "${ALPINE_REPO_PRIMARY}/main/x86_64/APKINDEX.tar.gz"; then \
        echo "primary mirror reachable: ${ALPINE_REPO_PRIMARY}"; \
        export ALPINE_REPO="${ALPINE_REPO_PRIMARY}"; \
    elif wget -q -T 3 --tries=1 -U "libfetch/2.0" --spider \
         "${ALPINE_REPO_FALLBACK}/main/x86_64/APKINDEX.tar.gz"; then \
        echo "primary mirror unreachable; using fallback: ${ALPINE_REPO_FALLBACK}" >&2; \
        export ALPINE_REPO="${ALPINE_REPO_FALLBACK}"; \
    else \
        echo "neither ${ALPINE_REPO_PRIMARY} nor ${ALPINE_REPO_FALLBACK} is reachable" >&2; \
        exit 1; \
    fi; \
    apk add --no-cache \
      --repository="${ALPINE_REPO}/main" \
      --repository="${ALPINE_REPO}/community" \
      caddy=2.8.4-r7 \
      supervisor=4.2.5-r5 \
      tzdata=2026e-r0 \
      btop=1.4.0-r0 \
      tmux=3.5a-r0 \
      htop=3.3.0-r0 \
      ncdu=1.21-r0 \
      ca-certificates=20260909-r0 \
      su-exec=0.2-r3

# Create the runtime tree. /var/www/site holds the baked-in static
# site; /youtube holds the user-facing download tree that caddy serves
# alongside it; /etc/caddy and /etc/supervisord hold the configs; /run
# is where supervisord puts its socket and pid file.
#
# /youtube is created owned by the nonroot user, and it is writable at
# runtime so an operator (or a script that drops files into it) can
# publish downloads without rebuilding the image. It is NOT baked with
# any content: the image ships an empty /youtube/.
RUN addgroup -g 10001 -S nonroot && \
    adduser -u 10001 -S -G nonroot -h /var/opt/riff nonroot && \
    mkdir -p /var/opt/riff /usr/local/riff /var/www/site /youtube /etc/caddy /etc/supervisord /run && \
    chown -R nonroot:nonroot /var/opt/riff && \
    chown -R nonroot:nonroot /youtube

# Copy the riff binary and the original riff entrypoint (which handles
# the UID 0 -> nonroot drop and RIFF_*_FILE env loading). The riff
# program in supervisord invokes entrypoint.sh, so the existing
# privilege-drop semantics are preserved.
COPY --from=backend /backend-build/riff /usr/local/riff/riff
COPY --from=backend --chmod=755 /backend-build/scripts/entrypoint.sh /usr/local/riff/entrypoint.sh

# Copy the supervisord entrypoint (this image's PID 1) and the caddy
# + supervisord configuration files. Both Caddyfiles ship: the entrypoint
# exports CADDYFILE (default `Caddyfile`) and supervisord expands it into
# the caddy command line, so operators switch to the edge-proxy variant by
# setting CADDYFILE=Caddyfile.fly (see fly.toml).
COPY --from=backend --chmod=755 /backend-build/scripts/entrypoint-supervisord.sh /usr/local/riff/entrypoint-supervisord.sh
COPY --from=backend /backend-build/scripts/Caddyfile /etc/caddy/Caddyfile
COPY --from=backend /backend-build/scripts/Caddyfile.fly /etc/caddy/Caddyfile.fly
# Static-site-at-root, riff-at-/community/* variant for an edge proxy
# (Fly.io) — see scripts/Caddyfile.fly.community's header for why this
# is a separate file rather than inverting Caddyfile.fly. fly.toml
# switches to it with CADDYFILE=Caddyfile.fly.community.
COPY --from=backend /backend-build/scripts/Caddyfile.fly.community /etc/caddy/Caddyfile.fly.community
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

# The MCP tool catalog is built from the generated OpenAPI document, which
# has to live at an absolute path inside the image: the riff child's working
# directory is /var/opt/riff (the data volume), so the built-in default of
# ./proto/gen/openapi.yaml resolves to nothing there and a failed
# mcp.Register exits the process (main.go). MCP_OPENAPI_SPEC in the ENV
# block below points at this copy. The source file is committed and
# regenerated by `cd proto && buf generate`, so a missing file fails the
# build here rather than the container at boot.
COPY --from=backend /backend-build/proto/gen/openapi.yaml /usr/local/riff/openapi.yaml

# Run as root to bind privileged ports; caddy drops to its own user
# internally, and the riff program uses entrypoint.sh's UID 0 ->
# nonroot drop before exec.
USER root
WORKDIR /var/opt/riff

# Site domain. The operator overrides SITE_DOMAIN in compose / fly
# secrets / `docker run -e`. Defaulting to example.com keeps a bare
# `docker run` from crashing on an empty Caddyfile interpolation.
#
# CADDYFILE picks which Caddyfile the caddy child loads; the default is
# the ACME variant, and a Fly.io-style deployment sets Caddyfile.fly
# (riff SPA at the root) or Caddyfile.fly.community (static site at the
# root, riff at /community/) — see fly.toml. The child passes an
# explicit --adapter caddyfile either way (see scripts/supervisord.conf)
# so a non-`Caddyfile` base name is not rejected as an "ambiguous config
# file format". MCP_OPENAPI_SPEC is the absolute path of the copied
# OpenAPI document the MCP service needs (see the copy's comment).
ENV SITE_DOMAIN=example.com \
    ACME_EMAIL=ops@example.com \
    CADDYFILE=Caddyfile \
    MCP_OPENAPI_SPEC=/usr/local/riff/openapi.yaml \
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
