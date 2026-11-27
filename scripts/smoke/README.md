# The forum smoke

A headless-browser run through the four flows that have actually broken. It
exists because of a specific property of this port: **the Flarum frontend is the
specification, and it reports nothing when the backend does not meet it.**

- an attribute it reads comes back `undefined`, so the control simply does not
  render (the tag picker had no confirm button; the tag modal's parent select was
  hidden; `isLiked` was never registered);
- a route it calls does not exist, and the SPA catch-all answers `index.html` with
  a **200**, which the store parses into `null`;
- a lazily chunked library is never fetched, so `this.sortable` is `undefined` and
  the drag handler returns early — with a clean console and no failed request.

None of that is visible to `go test` (375 tests) or to the frontend's Jest suite
(117), and every defect above was found by hand in a browser after both were
green. This script is that hand, automated.

## The four flows

| Flow | What it pins |
| --- | --- |
| Composer: pick a tag → post → see it on the discussion | the picker's confirm control, the `relationships.tags` payload, and the `included` resource that used to wipe the tag out of the store |
| Admin: drag one primary tag above another | the lazily chunked `sortable` library, the nested `{order:[{id, children}]}` payload, the 204, and the store mirror that stops the tag snapping back |
| Admin: the tag modal's "Parent tag" select, then a child created under a primary | that the select is PRESENT (present-but-hidden is indistinguishable from absent) and that the child lands in the tree |
| Wiki: rename the site | that `PUT /wiki/api/branding` answers the configuration rather than a bodyless 200, and that the served shell carries the new name |

## Running it

```sh
# boots its own throwaway instance (needs ./riff or RIFF_SMOKE_BINARY)
node scripts/smoke/forum_smoke.mjs

# against an instance that is already running
RIFF_SMOKE_BASE_URL=http://127.0.0.1:8090 \
RIFF_SMOKE_ADMIN_EMAIL=… RIFF_SMOKE_ADMIN_PASSWORD=… \
  node scripts/smoke/forum_smoke.mjs
```

The first superuser is created through PocketBase's own installer token: a fresh
instance prints `…/_/#/pbinstall/<token>`, and that token is a superuser auth
token for thirty minutes, so no Go seeder and no second boot are needed.

**The composer flow authors as a MEMBER**, not as the administrator. The author
columns are relations to the shared `users` collection and a PocketBase superuser
is not a row there, so authoring as one is refused (403, and the flags say so).

## Environment

| Variable | Meaning |
| --- | --- |
| `RIFF_SMOKE_BASE_URL` | use this instance; do not spawn one (then the admin credentials are required) |
| `RIFF_SMOKE_ADMIN_EMAIL` / `RIFF_SMOKE_ADMIN_PASSWORD` | the administrator to sign in as |
| `RIFF_SMOKE_BINARY` | the binary to spawn (default `./riff`) |
| `RIFF_SMOKE_DATA_DIR` | its data directory (default: a temp dir, removed afterwards) |
| `RIFF_SMOKE_PORT` | port to serve on (default `18099`) |
| `RIFF_SMOKE_TIMEOUT` | per-step timeout in ms (default `15000`) |
| `RIFF_SMOKE_ONLY` | run only the flows whose name contains this text |
| `RIFF_SMOKE_DEBUG=1` | print console errors, failed requests and every non-2xx response |
| `PLAYWRIGHT_CHROMIUM_EXECUTABLE` | launch this chromium build instead of Playwright's |
| `RIFF_SMOKE_PLAYWRIGHT` | path or package name of a playwright/playwright-core to use |
| `RIFF_SMOKE_HEADLESS=0` | show the window (debugging) |

`pnpm install` in this directory provides `playwright` (the lockfile pins it, and
therefore the Chromium build it downloads); `pnpm exec playwright install
--with-deps chromium` fetches the browser. The `forum-smoke` workflow does both,
builds `scripts/build_flarum_spa.sh`, stubs the Memos SPA embed (the smoke never
opens `/`), and runs this script.

On a machine whose cached Chromium does not match the pinned Playwright, point the
launcher at the build you have:

```sh
PLAYWRIGHT_CHROMIUM_EXECUTABLE=/path/to/chrome pnpm exec node forum_smoke.mjs
```

## Extending it

Add a flow to the `flows` array in `forum_smoke.mjs`: `{name, as?, async run({page, auth, admin})}`.
`as: 'member'` signs the browser in as the member account; otherwise it is the
administrator. Seed through `api()`/`createTag()`/`createMember()` rather than
clicking through the UI where the UI is not what is under test, and make names
unique with `unique()` — the tag slug index is unique per instance.

A flow that needs something new should ask "would this have caught one of the
defects above?" first; this is a smoke, not an end-to-end suite.
