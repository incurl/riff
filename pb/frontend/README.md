# pb/frontend

This directory is **no longer used**. Earlier design notes
called for the SPA build to land here, but the current `pnpm
release` script (in `web/package.json`), the CI workflow
(`.github/workflows/release.yml`), and the release smoke test
(`scripts/release_smoke_test.sh`) all consistently put the
build in `server/frontend/dist/` so it can be `//go:embed`-baked
into the riff binary by `server/frontend/embed.go`.

See `server/frontend/README.md` for the actual flow and
`server/frontend/embed.go` for the serving wiring. The
directory is left in place to avoid a noisy `git rm` in this
shape-fixing commit; a follow-up can delete it once the
riff-on-PocketBase Phase 4-5 design is final.
