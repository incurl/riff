# Known deviations from upstream Flarum

The forum reimplements Flarum 2.x's contract, not Flarum's PHP code.
This document lists where the two deliberately differ, so nobody has to
rediscover it by diffing the upstream source.

## 1. The frontend source is vendored, not the host's

Upstream Flarum's frontend lives in the `flarum/framework` monorepo
alongside its PHP backend. riff has no Flarum frontend, so the frontend
was vendored from the upstream `flarum/framework` repository (branch
`2.x`) into `pb/flarum/frontend/packages/`:

```
pb/flarum/frontend/packages/core/            the upstream forum/admin/common SPA
pb/flarum/frontend/packages/webpack-config/  the upstream build config
pb/flarum/frontend/packages/tsconfig/        the upstream TypeScript config
pb/flarum/frontend/packages/prettier-config/ the upstream formatting config
```

The source is **unmodified**. The build was adapted in three places,
all in `package.json` and the workspace layout:

- the upstream monorepo is a Yarn workspace; riff drives npm, so the
  `yarn` calls in the build scripts became `npm` calls;
- the upstream build reads `packages/composer.json` for the bundle
  namespace; riff keeps a minimal file with the same `name` field and
  no PHP metadata;
- the upstream build copies the `xslt-polyfill` out of `node_modules`;
  the script is unchanged.

The npm workspace root sits at `pb/flarum/frontend/` (its
`package.json` and `package-lock.json`), one level above `packages/`,
so the upstream workspace globs (`packages/core`, and so on) resolve
unchanged. The Go `frontend` package and the npm workspace share the
directory: the Go side embeds `frontend/ui/`, the npm side builds
`frontend/packages/core/dist/`.

**The upstream frontend is MIT licensed** and is vendored with its
licence and provenance recorded in `pb/flarum/README.md`.

## 2. No PHP runtime, no Composer extensions

Upstream extensions are Composer packages with PHP `extend.php` files.
They cannot run here. A Composer extension's *behaviour* can be
reimplemented as a Go plugin; its code cannot be installed.

## 3. The account store is the host's PocketBase `users`

Upstream Flarum has its own `users` table with its own registration,
verification and password-reset flows. The forum reuses the host's
PocketBase `users` collection and has no account endpoints at all. See
[the shared users boundary](shared-users.md).

## 4. Sessions are cookie + bearer, not Laravel sessions

Upstream uses a Laravel session with a CSRF token in the session store.
The forum uses a PocketBase auth token in an HttpOnly cookie plus a
double-submit CSRF token in a readable cookie, and accepts a bearer
token for API clients. The frontend's `Session` class needs no change:
it reads the CSRF token from the boot payload and echoes it in the
`X-CSRF-Token` header.

## 5. The base path is a first-class mount point

Upstream Flarum assumes it owns the host. The forum is mounted under a
configurable base path, and every route, asset URL and cookie is scoped
to it. Upstream's frontend already supports a `basePath`, so this needs
no frontend change — but it is a shape upstream does not itself deploy.

## 6. Tags and Likes are first-party, not upstream extensions

Upstream ships Tags and Likes as separate Composer extensions. Here they
are Go plugins inside the forum module, built in as the reference
implementation of the three-layer plugin flow. Their collections are
`forum_tags` and `forum_likes` rather than `tags` and `post_likes`.

Their **behaviour** follows upstream: the Tags plugin mirrors
`flarum/tags`' resource schema and its primary/secondary tag model, and
the Likes plugin mirrors `flarum/likes`' `isLiked` / `likesCount` /
`canLike` attributes, its `likes` relationship (capped at four likers,
the current user's first) and its unique `(post, user)` index.

Their **front ends** are real Flarum extensions, in upstream's layout
under `pb/flarum/frontend/packages/plugins/<name>/` — a `composer.json`
(the plugin's composer name, `riff/tags` / `riff/likes`), a `js/`
directory with `forum.js` and `admin.js` entries and a
`webpack.config.cjs`, and a `style.css`. They build with the vendored
`flarum-webpack-config`, so the bundles share core's externals (a
`flarum/...` import compiles to `flarum.reg.get('core', ...)` and never
duplicates core) and its babel preset (JSX compiles to Mithril's `m`).
Each bundle assigns itself onto `flarum.extensions` under the id
derived from the composer name — `riff-tags` / `riff-likes` — which is
what the boot shell's `app.bootExtensions(flarum.extensions)` reads.
`scripts/build_flarum_spa.sh` builds and stages all three bundles plus
the stylesheets into the embedded tree.

Three front-end deviations are deliberate:

- **Likes gets a button, Tags gets badges.** The Likes bundle extends
  `CommentPost`'s `footerItems` with a like/unlike button; the Tags
  bundle extends `DiscussionListItem`'s `infoItems` with tag badges.
  Everything else upstream ships (the tag picker in the composer, the
  tag index and tag pages, the likes list popover) is not implemented.
- **Tag badges are labels, not links.** There is no tag route in this
  port, so a badge links nowhere rather than to the fallback page.
- **No locale files.** The port has no extension-locale pipeline, and
  the translator renders a missing key as the key itself, so the
  extension strings are literal English rather than `trans()` calls.

## 7. The permission model is Flarum's, but the storage is not

Upstream stores permissions in a `group_permission` table keyed by
group and permission key, with a scope column for resource-scoped
rules. The forum stores the same shape in `forum_permissions`. The
built-in groups have well-known ids rather than upstream's numeric ids,
because the ids travel through a PocketBase relation.

## 8. The Markdown renderer is riff's, not Flarum's

Upstream Flarum formats a post's Markdown server-side and returns
`contentHtml`. The forum does the same, with riff's own markdown engine
(`github.com/incurl/riff/markdown`) rather than a second renderer:

- the forum builds a plain `markdown.Service` and hands it to the API
  server;
- a post's `content_html` is the rendered HTML and `content` stays the
  Markdown source, so the frontend's edit form can send the source
  back;
- the rendered body is produced by the SAME goldmark instance the
  service parses with, so the HTML carries the same extensions (tables,
  strikethrough, task lists, linkify) as the AST.

**riff's `#tag` and `@mention` extensions are not enabled.** They are
riff's memos syntax (a `#tag` becomes a memo tag, an `@user` a memo
mention), and they build AST nodes with no HTML node renderer — so
enabling them makes the HTML renderer emit nothing for the node and
every `#tag` and `@mention` in a post silently vanishes from the
rendered body. In a Flarum post both are literal text, which is what
upstream shows too.

**Raw HTML is escaped.** The frontend inserts `contentHtml` as trusted
HTML (`m.trust(contentHtml)` in flarum/forum/components/Comment.tsx),
so a raw HTML block in a post would be an injection vector. The
renderer therefore escapes raw HTML by default; `markdown/html.Options`
carries an `AllowRawHTML` opt-in for a caller that sanitises the result
afterwards.

**What still differs.** Upstream's formatter is configurable per
extension (a plugin can register its own formatter). The forum's
renderer is fixed, but a plugin can replace it through the container:
the API server depends on the `api.ContentRenderer` interface, not on
the markdown package.

## 9. The SEO shell is minimal

Upstream renders a server-side HTML skeleton with the discussion's
content in a `<noscript>` block. The forum's shell is a static document
that boots the SPA. There is no server-side content rendering.

## 10. The admin surface is partial

Upstream's admin app has a full settings page, a user list, a group
editor, a tag manager and an extension manager. The forum exposes the
settings read/write endpoints and the extension manifest endpoint, and
the admin SPA boots — but the forum does not yet implement every admin
screen's backend.

## 11. Notifications are limited

Upstream's notification system is a per-extension blueprint registry
with a large catalogue of notification types. The forum's
`forum_notifications` collection carries the notification kinds the
first-party plugins produce (`postLiked`, `discussionTagged`,
`mentionedInPost`, `commentOnPost`).

## 12. The deferred items

The following are **roadmap**, not v1:

- the Goja sandbox hook layer (`backendKind: js-sandbox` is accepted by
  the manifest but refused by the manager with an explicit message);
- plugin package signing and marketplace manifest checks;
- the remaining upstream extensions (mentions, subscriptions, sticky,
  lock, suspend, and the rest).

## 13. The frontend build needs npm's legacy peer resolver

The upstream monorepo is a Yarn workspace with hoisting assumptions
that npm's strict resolver rejects. The build therefore runs with
`npm install --legacy-peer-deps`. This is a build-time detail with no
effect on the shipped bundle.
