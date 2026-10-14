# Vaultwarden alongside riff in the same Fly machine

Status: proposed, not implemented. Documented for later revisit.

## Context

The user maintains a self-hosted Bitwarden-compatible vault at
`vw.1une.cc`, currently running as a single-container
`vaultwarden/server` deployment on separate infrastructure. They
want to migrate it into the same Fly machine that hosts riff
(`ravine.fly.dev`), reverse-proxied as `vw.1une.cc` on the same
Fly edge. The migration is one-shot data copying from the old
vault; the cohabitation is intended to be permanent.

riff is at an early stage and the user does not want to bloat
the deploy image right now. This document captures the design as
it stood on 2026-10-15 so a future implementer can pick it up
without re-deriving it.

## Verified facts (from inspecting `vaultwarden/server:testing-alpine` 1.37.4 locally)

- Single statically-linked Rust binary at `/vaultwarden` (52 MB).
- Static web vault client at `/web-vault` (150 MB) served by
  the same HTTP listener.
- Entrypoint `/start.sh` sources `/etc/vaultwarden.sh` and
  `/etc/vaultwarden.d/*.sh` before `exec /vaultwarden`. Also
  handles the legacy `/etc/bitwarden_rs.sh` and
  `/etc/bitwarden_rs.d/` paths as a deprecation hint.
- Data dir `/data`, declared `VOLUME`. Fresh boot creates
  `db.sqlite3`, `db.sqlite3-shm`, `db.sqlite3-wal`,
  `rsa_key.pem`. The RSA key is load-bearing — without it,
  every existing user token is invalidated.
- HTTP listener binds `0.0.0.0:80` (env `ROCKET_PORT`,
  `ROCKET_ADDRESS`).
- No TLS built in.
- Healthcheck is `GET /alive` (the `healthcheck.sh` script
  curls it; that's also how the smoke test below confirms the
  binary works without configuration).
- Aborts with `ERROR No persistent volume!` and exits 1 if
  `/data` is missing. An empty-but-existing `/data` is enough
  to satisfy this check; the binary then creates a fresh empty
  schema and starts. **This means the migration MUST stop
  vaultwarden before the file copy, not after.**

## Why image-bake is the right primary approach (but deferred)

`COPY --from=vaultwarden/server:testing-alpine` in the
Dockerfile is bit-identical, pinned to the same tag the user
already pulled locally, and avoids an apk/network dependency in
the runtime stage. It adds ~200 MB to the image (binary +
`web-vault`) and ~30 s to local build time.

**Deferred.** The user explicitly flagged image bloat as a
blocker during riff's early development. Alternatives that
would keep the image slim (apk from community repos, runtime
mount of `web-vault`) each have their own trade-offs and are
not yet worth a separate investigation. When this design is
implemented, the build-stage approach is the default.

## Design (the future implementation)

### Process layout

- `supervisord` (PID 1) becomes the parent of **three**
  children: `caddy` (public :80), `riff` (:8090),
  `vaultwarden` (:8080).
- The existing `[group:riff-deploy]` `programs` list gains
  `vaultwarden`.
- `vaultwarden` binds `127.0.0.1:8080` (`ROCKET_PORT=8080`,
  `ROCKET_ADDRESS=127.0.0.1`), not `0.0.0.0:80`. Caddy is the
  only thing on :80; this avoids port-collision and the need
  for `NET_BIND_SERVICE` on the vaultwarden child.
- The vaultwarden child runs as root inside the container
  (matches the upstream image; no `entrypoint.sh`-style
  privilege drop). It is sandboxed by the existing
  `cap_drop: ALL` + `cap_add: NET_BIND_SERVICE` in
  `scripts/compose.yaml`; the same applies in Fly.

### Data layout

- vaultwarden data lives at `/var/opt/riff/vaultwarden/`
  inside the existing `riff_data` volume (no new Fly volume).
  One mount, one line on the bill, and a single
  `fly volumes extend riff_data -s N` covers both.
- `/data` inside the container symlinks to
  `/var/opt/riff/vaultwarden` so the upstream `start.sh` and
  `healthcheck.sh` work unchanged.
- PocketBase (`./pb_data`) and vaultwarden (`./vaultwarden`)
  never touch each other's files; cohabitation on one volume
  is safe.
- **Migrating from the old vault means copying**
  `db.sqlite3`, `db.sqlite3-shm`, `db.sqlite3-wal`, **AND**
  `rsa_key.pem`. The RSA key is critical.

### Dockerfile

Add a new build stage after `backend`:

```
FROM --platform=$BUILDPLATFORM vaultwarden/server:testing-alpine AS vaultwarden
```

The runtime stage gains:

```
COPY --from=vaultwarden /vaultwarden     /vaultwarden
COPY --from=vaultwarden /web-vault       /web-vault
COPY --from=vaultwarden /start.sh        /start.sh
COPY --from=vaultwarden /healthcheck.sh  /healthcheck.sh
ln -s /var/opt/riff/vaultwarden /data
```

The binary stays at `/vaultwarden` (not
`/usr/local/vaultwarden/vaultwarden`) because `/start.sh`
hard-codes the absolute path. Moving the binary would require
also editing `start.sh`, which loses the "binary identical to
upstream" property.

### Supervisord

New `[program:vaultwarden]` block in
`scripts/supervisord.conf`:

```ini
[program:vaultwarden]
command=/start.sh
environment=ROCKET_ADDRESS="127.0.0.1",ROCKET_PORT="8080",DATA_FOLDER="/data",ROCKET_PROFILE="release",DOMAIN="https://vw.1une.cc"
directory=/var/opt/riff/vaultwarden
autostart=true
autorestart=unexpected
startsecs=5
startretries=10
stdout_logfile=/dev/stdout
stdout_logfile_maxbytes=0
stderr_logfile=/dev/stdout
stderr_logfile_maxbytes=0
```

`[group:riff-deploy]` `programs=caddy,riff,vaultwarden` (was
`caddy,riff`).

The `environment=` block overrides the image defaults:
`ROCKET_PORT=80` (baked into the image) becomes `8080`, and
`ROCKET_ADDRESS=0.0.0.0` becomes `127.0.0.1` so the listener
stays on the loopback.

### Caddy

`scripts/Caddyfile.fly.community` (the active variant per
`CADDYFILE=Caddyfile.fly.community` in `fly.toml`) gains:

```
@vaultwarden host vw.1une.cc
handle @vaultwarden {
    reverse_proxy 127.0.0.1:8080 {
        header_up X-Forwarded-For {http.request.header.X-Forwarded-For}, {remote_host}
        header_up X-Forwarded-Proto https
    }
}
```

- `@vaultwarden host` (not a path matcher) because vaultwarden
  has no concept of a base path and the Bitwarden clients
  hard-code the document root. Putting it on its own hostname
  rather than `/vaultwarden/` is the standard vaultwarden
  deployment pattern.
- `X-Forwarded-For` and `X-Forwarded-Proto` mirror the
  existing `(proxy)` snippet so any future rate-limit /
  client-IP logic on the vaultwarden side sees the real
  client.

Same handle block added to `scripts/Caddyfile.fly` and
`scripts/Caddyfile` (the ACME variant) for symmetry, with the
local compose variant using `SITE_DOMAIN=vw.localhost` in
`scripts/compose.yaml` so a `docker compose up` exercises
both hostnames.

### `fly.toml`

- New `[env]` entries:
  - `VAULTWARDEN_DATA_DIR=/var/opt/riff/vaultwarden`
  - `VAULTWARDEN_DOMAIN=https://vw.1une.cc`
  - `ROCKET_PORT=8080` (also overrideable from the
    supervisord program env block; setting both is
    belt-and-braces).
- `[http_service.checks]`: **no new check.** Fly's check
  sends the app hostname (`ravine.fly.dev`) as the `Host:`
  header; it cannot be set to `vw.1une.cc` without a
  `headers = { Host = "..." }` override that Fly's check
  machinery does not currently support. A vaultwarden child
  dying manifests as 502s on `vw.1une.cc` instead of being
  silent. Acceptable; revisit if Fly adds a per-check Host
  header override.
- Volume size: the existing `riff_data` (1 GB) may be too
  small after the migration. The operator runs
  `fly volumes extend riff_data -s 3` (or higher) before the
  migration. `fly.toml` cannot change an existing volume's
  size; only the `fly volumes` CLI does.
- Memory: **unchanged at 256 MB** — see the risk section
  below.

## Risks and known issues

### Memory: flagged, deferred

vaultwarden is a Rust binary, expected ~50–100 MB RSS at idle.
The current single-machine sizing is `shared-cpu-1x / 256mb /
512mb swap` (`fly.toml`). Adding vaultwarden on top of riff
(PocketBase on SQLite, plus caddy) is risky; 256 MB + 512 MB
swap may swap-thrash under even modest load. The user
explicitly chose to defer the memory bump to 1 GB until the
design is actually implemented. The revisit checklist below
includes "bump memory to 1 GB when this is implemented".

### Shared volume: `db.sqlite3` corruption on copy

If the vaultwarden child is running while the operator copies
the old vault's `db.sqlite3` over the network, SQLite WAL
writes race the file copy and the database corrupts. The
migration recipe MUST `supervisorctl stop vaultwarden` first
and `supervisorctl start vaultwarden` after.

### Vaultwarden dies on missing `/data`

The upstream binary aborts on first boot with
`ERROR No persistent volume!` if `/data` doesn't exist. The
empty-but-existing `/data` (from the symlink target) is
enough to satisfy this check; the binary then creates a fresh
empty schema and starts. The migration MUST stop vaultwarden
BEFORE the file copy, not after, or the binary will overwrite
the operator's incoming `db.sqlite3` with a fresh empty
schema.

### Image bloat

~200 MB added to the deploy image. The user has flagged this
as the blocker; see the Status line at the top of this
document. Revisit when image size is no longer load-bearing.

### Two hostnames on one machine

Fly's `[http_service.checks]` sends a single Host header
(`ravine.fly.dev`) for all checks, so a vaultwarden health
check needs to be either piggybacked on a different
mechanism (TCP probe, custom path) or accepted as
"user-visible 502 is the signal". The chosen design picks the
latter.

## Migration recipe (operator action, not code)

1. **Build and push the new image:**
   ```
   cd web && pnpm release && cd ..
   docker buildx build --platform linux/amd64 \
     -f scripts/Dockerfile \
     --build-context ravine="$(readlink -f scripts/ravine)" \
     --build-arg VERSION="$(bash scripts/release_version.sh development-version)" \
     --build-arg COMMIT="$(git rev-parse HEAD)" \
     -t riff:dev --load .
   flyctl auth docker
   docker tag riff:dev registry.fly.io/ravine:dev
   docker push registry.fly.io/ravine:dev
   flyctl deploy -a ravine
   ```
2. **Extend the volume** (if needed): `fly volumes extend
   riff_data -s 3`.
3. **Stop vaultwarden on the new machine** (it's the third
   child and may have already auto-started, creating an empty
   fresh schema):
   `fly ssh console -C "supervisorctl stop vaultwarden"`.
4. **Copy the old vault's data** to
   `/var/opt/riff/vaultwarden/`. Required files:
   - `db.sqlite3`
   - `db.sqlite3-shm`
   - `db.sqlite3-wal`
   - `rsa_key.pem` (critical — token signing key)
   - `config.json` (if present on the old host — preserves
     `domain`, `invitation_org_name`, etc.)
   - `attachments/` (if used — usually not; vaultwarden
     stores attachments inline in the DB by default since
     1.25, but the old instance may have them on disk)

   Recommended transport: `fly sftp` for a small set of files,
   or a `tar` pipe through `fly ssh console` for the whole
   tree. The `vaultwarden backup` command (or `kill -USR1`)
   produces a consistent snapshot to copy from.
5. **Start vaultwarden:**
   `fly ssh console -C "supervisorctl start vaultwarden"`.
6. **Smoke (Fly side):** `curl -fsS -H "Host: vw.1une.cc"
   https://vw.1une.cc/alive` returns 200 once the DNS A
   record is attached.
7. **DNS:** point `vw.1une.cc` A and AAAA at Fly's edge IPs
   (or attach the app via `fly certs add vw.1une.cc`).
8. **Verify the client side:** the Bitwarden browser
   extension and the mobile apps can sign in with an existing
   user. If they cannot, the most likely cause is a missing
   `rsa_key.pem` from step 4.

## When to revisit

- riff reaches a stable image size (the user's signal that
  "early stage" is over).
- The user runs a single Bitwarden-client smoke test on
  `vw.1une.cc` and is satisfied.
- A second persistent workload on the same machine makes
  the 1 GB memory bump inevitable anyway.

## Open questions (for the implementer)

- Does the upstream `/start.sh` work when the binary lives
  at `/vaultwarden`? Verified against the local image: yes,
  `start.sh` does `exec /vaultwarden "$@"` with a hard-coded
  absolute path. Relocating the binary would require editing
  `start.sh` too, which loses the "binary identical to
  upstream" property. Plan keeps the binary at `/vaultwarden`.
- Does the user have a custom `/etc/vaultwarden.sh` to
  preserve? The local image was inspected and the file does
  not exist. If the user's old vault had one (for `DOMAIN`,
  `SMTP_*`, `SIGNUPS_ALLOWED=false`, `INVITATIONS_ALLOWED`,
  etc.) it would be sourced at runtime and need to be baked
  in. The plan adds a `/etc/vaultwarden.d/` mountpoint in
  the image so per-deploy overrides (e.g. a `SMTP` script)
  can be added without a rebuild, but does not commit any
  specific env-var set.
- Does the user's old instance have a `config.json`? The
  vaultwarden wiki recommends env-var-only configuration in
  1.30+, but older instances used `config.json`. The
  migration recipe includes it; if absent, the new instance
  uses the image's env-var defaults plus the supervisord
  `environment=` overrides.
