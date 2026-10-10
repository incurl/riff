# server/frontend

This package embeds the riff SPA (the Memos React app under
`web/`) into the riff binary and serves it from the root of
the PocketBase HTTP listener.

## How it works

`web/package.json`'s `release` script (`pnpm release`) runs
Vite with `--outDir=../server/frontend/dist --emptyOutDir`, so
the build output lands in this directory. The `//go:embed
all:dist` directive in `embed.go` bakes the tree into the riff
binary at compile time, and `Register` mounts a single
`GET /{path...}` route on the PocketBase router at OnServe
time. The route uses PocketBase's `apis.Static` with
`indexFallback=true`, so deep links like `/m/123` fall through
to `index.html` and the SPA's client-side router takes over.

PocketBase's more specific routes (`/_/*` for the admin UI,
`/api/*` for the REST + Connect endpoints, `/mcp` for the MCP
service) take precedence over our `/{path...}` catch-all, so
the SPA only handles what PocketBase does not.

## Build prerequisite

`//go:embed` requires the target directory to exist at
compile time. **Run `cd web && pnpm install && pnpm release`
before `go run .` or `go build`**, otherwise the build fails
with "pattern all:dist: no matching files found". The
`.github/workflows/release.yml` pipeline does the same: the
`build-frontend` job runs `pnpm release` and uploads the
result as a `frontend-dist` artifact, which the
`build-binaries` job downloads into this directory before
`go build` runs.

`server/frontend/dist/` is `.gitignore`d (a tracked
`index.html` placeholder is not used because the real Vite
build always overwrites it on `pnpm release` and committing
the artifact creates churn for every asset hash change). A
fresh clone therefore needs the `pnpm release` step; the
error message on a missing embed is the signal to run it.

## Why not `pb/frontend/`

The early design notes (see `pb/frontend/README.md`) described
the SPA landing in `pb/frontend/dist`. The script, the CI
workflow, and the smoke test all consistently put it in
`server/frontend/dist/` instead, so this package follows what
actually runs. `pb/frontend/` is left as a relic; the script
and CI are the source of truth.
