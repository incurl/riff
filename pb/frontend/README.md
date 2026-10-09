# pb/frontend

This directory is intentionally empty in Phase 1.

In Phase 4–5, the React frontend build (`pnpm release` from `web/`)
will output static assets into this directory. PocketBase will then
serve the contents of this directory at `/` (with `/_/` reserved for
the admin UI).

See `DECISIONS.md` for the routing decision and Phase 5 tasks.
