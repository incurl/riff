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
# pb/frontend/dist before invoking `docker build`; see
# `cd web && pnpm release` in AGENTS.md. The `.dockerignore`
# already excludes `web/`, so a `pnpm install` inside the
# container is not possible.
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

# Use minimal Alpine with security updates
FROM alpine:3.21 AS monolithic

# Install runtime dependencies and create non-root user in single layer
RUN apk add --no-cache tzdata ca-certificates su-exec && \
    addgroup -g 10001 -S nonroot && \
    adduser -u 10001 -S -G nonroot -h /var/opt/riff nonroot && \
    mkdir -p /var/opt/riff /usr/local/riff && \
    chown -R nonroot:nonroot /var/opt/riff

# Copy binary and entrypoint to /usr/local/riff
COPY --from=backend /backend-build/riff /usr/local/riff/riff
COPY --from=backend --chmod=755 /backend-build/scripts/entrypoint.sh /usr/local/riff/entrypoint.sh

# Run as root to fix permissions, entrypoint will drop to nonroot
USER root

# Set working directory to the writable volume
WORKDIR /var/opt/riff

# Data directory. The PocketBase data dir is bound here; on Fly.io
# the persistent volume `riff_data` mounts at this path (see fly.toml).
VOLUME /var/opt/riff

ENV TZ="UTC"

# PocketBase listens on 8090 by default; the AGENTS.md CI Reference
# also documents 8090 as the canonical riff port. Fly.toml sets
# `internal_port = 8090` and `force_https = true` so the public URL
# terminates TLS at the Fly proxy.
EXPOSE 8090

ENTRYPOINT ["/usr/local/riff/entrypoint.sh", "/usr/local/riff/riff"]
