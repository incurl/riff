# The vendored upstream frontend, and what it is pinned to

`packages/` holds upstream Flarum's frontend — `framework/core/js` from
[flarum/framework](https://github.com/flarum/framework) (MIT), plus its
`js-packages/webpack-config`, `tsconfig` and `prettier-config`. The source is
unmodified; only the build wiring differs (see
`DECISIONS.md` "The forum module vendors the upstream Flarum frontend").

**This file is the pin of record.** The frontend is the *specification* the Go
backend implements: every attribute, relationship, route and boot-payload field
it reads at runtime is a contract the backend has to meet. Without a recorded
revision, "are we behind upstream?" cannot be answered, and an upgrade is
archaeology.

## What is recorded, and what is not

When the tree was vendored, only the **branch name** was written down (`2.x`),
and `packages/core/package.json` carries upstream's `version: 0.0.0`. A branch is
not a pin — it moves. The exact upstream commit was therefore never recorded,
and it cannot be recovered from the working tree.

What *can* be established, and is recorded here, is the snapshot's position by
**blob identity**: for each sampled file, the git blob hash of our copy is
compared with upstream's version at the commits that touched that file. That
gives a bracket, which is enough to diff an upgrade against.

## The bracket (measured 2026-11-27)

The snapshot **contains** the change to `common/extend.ts` committed
2024-12-07 (`5d281b9471`) and **predates** the change to
`forum/components/DiscussionComposer.js` committed 2025-01-03 (`3294941226`).

So the vendored tree is upstream `2.x` somewhere in
**[2024-12-07, 2025-01-03)** — i.e. roughly two years behind `2.x` at the time
this file was written. That is worth knowing: the port is not tracking a recent
`2.x`, and features added upstream since then are neither implemented nor
expected by this frontend.

## The manifest

`git hash-object <path>` reproduces the left column; `git hash-object` of a file
fetched from upstream reproduces the right one.

| Vendored file | Our blob | Upstream evidence |
| --- | --- | --- |
| `packages/core/src/common/Model.ts` | `c116c053706f0ed1e8f4d9194d5f4c5ec7626503` | matches `cf9d88f334` (2024-11-02), differs from `1ead69e9b6` (2024-10-25) |
| `packages/core/src/common/extend.ts` | `6ee151d91af75da8dcb1715fabdd655c525da06c` | matches `5d281b9471` (2024-12-07) |
| `packages/core/src/common/components/Modal.tsx` | `0806c932e11f4a4c05dd61ed7845e90943ba9409` | matches `257be2b9db` (2024-09-28), differs from `b02f8190ea` (2024-05-03) |
| `packages/core/src/forum/components/DiscussionComposer.js` | `999d80b1a884ebef45f0c566324621be6292dfe7` | **differs** from `3294941226` (2025-01-03) — this is the upper bound |
| `packages/core/src/admin/components/ExtensionPage.tsx` | `8cb691c7eace9d6f1a79843b3c2a685e430904e4` | sampled; unmodified by the port |
| `packages/core/src/forum/states/PostStreamState.ts` | `b8560d259ad9e5ad6bb7f954665814faf2ea41c2` | sampled; unmodified by the port |

A file whose hash matches the *newest* upstream change to it means we contain
that change; a file whose hash differs from the newest means we predate it. Both
directions are needed for a bracket.

## How to verify a file

```sh
# our copy
git hash-object pb/flarum/frontend/packages/core/src/common/extend.ts

# upstream at a commit that touched it (find candidates with the commits API)
curl -s "https://api.github.com/repos/flarum/framework/commits?path=framework/core/js/src/common/extend.ts&sha=2.x&per_page=5"
curl -s "https://raw.githubusercontent.com/flarum/framework/<sha>/framework/core/js/src/common/extend.ts" -o /tmp/up.ts
git hash-object /tmp/up.ts
```

## What an upgrade has to do

1. Re-vendor `framework/core/js` (and the three `js-packages`) from a **commit**,
   not a branch, and record that commit plus a fresh manifest here.
2. `bash scripts/build_flarum_spa.sh` — the bundles are build output and are
   embedded into the binary (see `scripts/build_flarum_spa.sh`).
3. Run the browser smoke (`node scripts/smoke/forum_smoke.mjs`, or the
   `forum-smoke` workflow): the four flows that have actually broken — the
   composer's tag picker, admin drag-to-reorder, the tag modal's parent select,
   and the wiki's branding — are cheap and catch the class of defect the Go suite
   cannot see (a missing attribute or route degrades to silence, not an error).
4. Run the vendored frontend's own Jest suite
   (`cd pb/flarum/frontend/packages/core && npx jest --config jest.config.local.cjs`)
   and the Go suite (`go test ./pb/flarum/...`).
5. Diff the new bundle against the API: every `attribute('…')`, relationship and
   `app.request(...)` path the SPA reads must exist on the Go side. This is the
   step that has no automation yet, and the one that pays for itself.
