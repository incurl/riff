# The forum module

The forum is a Flarum-compatible forum module for the riff host
application: a Go backend on PocketBase that speaks Flarum's JSON:API
contract, plus the reused Flarum frontend.

- The design documents are in [`docs/design/flarum/`](../docs/design/flarum/).
- The Go module is in [`pb/flarum/`](flarum/) — the vendored
  frontend is in `pb/flarum/frontend/packages/`.

## Layout

| Path | Holds |
| --- | --- |
| `pb/flarum/` | The Go module: the API, the domain, the plugin framework, the migrations, the frontend serving. |
| `pb/flarum/plugins/tags/` | The first-party Tags plugin. |
| `pb/flarum/plugins/likes/` | The first-party Likes plugin. |
| `pb/flarum/frontend/ui/index.html` | The tracked fallback shell, embedded into the binary. |
| `pb/flarum/frontend/packages/core/` | The vendored Flarum core frontend (Mithril + TypeScript) its Less stylesheets (`less/`, compiled to `forum.css` and `admin.css` by `build-less.cjs`) and its translations (`locale/`, compiled to `forum-en.js` and `admin-en.js` by `build-locale.cjs`). |
| `pb/flarum/frontend/packages/plugins/{tags,likes}/` | The first-party plugins’ front ends: a Flarum extension each (`composer.json` + `js/` entries + `style.css`). |
| `pb/flarum/frontend/ui/dist/` | The built SPA (build output; gitignored). |
| `pb/flarum/frontend/package.json` | The npm workspace root. |
| `pb/flarum/frontend/packages/core/` | The vendored Flarum frontend source (unmodified). |
| `pb/flarum/frontend/packages/webpack-config/` | The vendored upstream build config. |

## Building the frontend

```bash
scripts/build_flarum_spa.sh
```

That installs the npm workspace, builds the SPA and stages the output in
`pb/flarum/frontend/ui/dist`, which is what the Go binary embeds.

The staged build is **optional**: `pb/flarum/frontend/ui/index.html`
is a tracked fallback shell, so a fresh clone compiles and runs without
a front-end build. Without one, the forum serves that fallback — which
explains what to build — instead of a blank page.

The upstream build scripts call `yarn`; riff drives `npm`, so the
scripts were rewritten to `npm` calls and the workspace was laid out for
npm. The upstream **source** is unmodified.

The npm workspace root is `pb/flarum/frontend/`, one level above
`packages/`, so the upstream workspace globs (`packages/core`, and so
on) resolve unchanged.

## Provenance and licence

`pb/flarum/frontend/packages/core`, `pb/flarum/frontend/packages/webpack-config`,
`pb/flarum/frontend/packages/tsconfig` and `pb/flarum/frontend/packages/prettier-config` are
vendored from
[flarum/framework](https://github.com/flarum/framework) (branch `2.x`,
MIT licensed). `pb/flarum/frontend/packages/composer.json` is a riff-authored stub that
keeps the bundle namespace the upstream webpack config reads; it holds
no PHP package metadata.

**The branch name is not a pin, and neither is `version: 0.0.0`.** The exact
upstream commit was never recorded, so `pb/flarum/frontend/UPSTREAM.md` records
what *can* be established — the blob hash of each sampled vendored file and the
resulting bracket (the snapshot contains upstream's 2024-12-07 change to
`common/extend.ts` and predates its 2025-01-03 change to
`DiscussionComposer.js`). Read it before an upgrade: it also lists the steps an
upgrade has to run.

Nothing from the upstream PHP backend is vendored: the Go module
reimplements the contract.
