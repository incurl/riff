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
- **The asset layout is the frontend's, not upstream's compiler's.**
  Upstream's PHP asset compiler moves the flat webpack output into
  `js/<extensionId>/`; this port has no such compiler, so the shared
  webpack config writes that layout directly (`chunkFilename` is
  `js/<namespace>/[name].js`). The runtime's chunk url is upstream's
  formula — `${jsChunksBaseUrl}/${namespace}/${urlPath}.js` — so the
  two have to agree, and the namespace comes from the same
  `extensionId.cjs` on both sides.
- **The stylesheets are compiled with the npm `less` package, not
  less.php.** Upstream compiles `less/forum.less` and `less/admin.less`
  with less.php and serves `forum.css` and `admin.css`; this port has
  no PHP, so `packages/core/build-less.cjs` compiles them with the
  `less` npm package. The Less tree itself is vendored unmodified from
  upstream. The compiled stylesheet is the same for both apps.
- **Translations are compiled from the vendored YAML.** Upstream compiles
  `locale/*.yml` into `forum-<locale>.js` / `admin-<locale>.js` with its
  PHP `AddTranslations` step; this port produces the same two bundles from
  `packages/core/build-locale.cjs`, with the same per-frontend key filter
  (`*.forum.*`/`*.lib.*` vs `*.admin.*`/`*.lib.*`) and the same `=> key`
  reference resolution. English only: the boot payload advertises `en`.
- **The session lives in the boot payload, and the caller is a
  resource in it.** The frontend reads `session.userId` and
  `session.csrfToken` and looks the caller up in the store by that id,
  so the payload carries both the session and the caller as a resource.
  `session.userId` is a STRING: the id is the PocketBase record id, and
  the frontend looks the record up by that exact value.
- **A logout does not revoke the token.** PocketBase's auth tokens are
  stateless JWTs with no server-side store, so a captured token keeps
  working until it expires (five days by default). Upstream Flarum's
  tokens are database-backed and are deleted on logout. The host's
  revocable credential is the personal access token.
- **Avatars come from PocketBase, streamed through the forum.** The
  picture is the shared `users` collection's built-in `avatar` file —
  the same one the Memos SPA and the admin UI show — served at
  `{basePath}/avatars/{id}` rather than linked at PocketBase's native
  `/api/files/...` URL, which lives at the listener root the forum
  does not own. A 404 means no picture, and makes the frontend draw
  initials. Upstream stores avatars under its own upload endpoint and
  can edit them from the profile; this port displays the shared file
  but does not implement the editor (`canEditAvatar` is false).
- **The admin app boots but cannot administer.** It gets an admin
  shell (the `#admin-navigation` column and the `sideNavOffset`
  content) and an admin payload (the driver lists its boot walks, the
  forum's own plugin list as its extensions, the enabled-plugin list,
  the user count, and the forum's own settings). Settings and
  extensions are read-only here: riff's administration surface is
  PocketBase's at `/_/`, so those forms have nothing behind them.
- **The admin application is served only to an administrator**, exactly
  as upstream serves it (upstream's admin routes carry the
  `RequireAdministrateAbility` middleware). A non-administrator gets a
  403 rather than the shell. This is load-bearing: the admin app
  dereferences `app.session.user!.email()` at boot, so an anonymous
  visitor served the shell would get a blank page instead of a sign-in
  prompt.
- **The admin shell is served with `Cache-Control: max-age=0,
  no-store`**, the same header upstream sends (its admin middleware
  carries `DisableBrowserCache`). The shell carries the boot payload,
  and the payload changes with the instance, so a cached copy would
  replay a stale one — which reads as a blank page. The forum shell
  keeps its own caching behaviour.
- **The mail page renders, but the forum has no mail driver.** riff
  sends its mail through PocketBase's own SMTP configuration, so
  `GET /api/mail/settings` answers with upstream's shape but an empty
  driver-field set, and the bounce-reporting flags are false. The
  endpoint exists because the page fetches it on mount, and a 404
  would leave `response.data` undefined and throw.
- **An extender cannot touch `app.forum` or `app.session`.** The boot
  shell runs `app.load(payload)`, then `app.bootExtensions(...)`, and
  only then `app.boot()` — and `app.boot()` is what assigns them. An
  extender that reads `app.forum` throws at boot, so the bundles defer
  every request to `app.beforeMount`.

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

## 11. The permission rows cannot express ownership

Upstream Flarum's policies take the actor AND the subject, and the
POLICY is what ties a group row to a subject: `Gate::allows()` asks
the policies first and falls back to `isAdmin() || hasPermission()`
only when no policy covered the question.

This port keeps the rows and the policy shape, and the policies are
ported one for one — the core's `DiscussionPolicy::rename()` and
`PostPolicy::edit()`, and the tags extension's
`DiscussionPolicy::tag()`, each with its `allow_*` setting. What the
permission service alone cannot express is the OWNERSHIP half ("your
own post"), so the handlers ask the policy question themselves.

The rules are therefore upstream's:

| action | the author | anyone else |
| --- | --- | --- |
| edit a post | while `allow_post_editing` permits | `discussion.editPosts` (the moderator group) |
| rename a discussion | while `allow_renaming` permits | `discussion.rename` (the moderator group) |
| change a discussion's tags | while `allow_tag_change` permits | nobody — the `tag` ability is a policy, not a group permission |
| delete a post or discussion | always | the moderator group |

Two differences from upstream remain, both deliberate:

- the permission KEY names differ (`editPost` here, `discussion
  .editPosts` upstream), because the forum's keys are flat. The ADMIN
  surface translates between the two dialects
  (`pb/flarum/domain/permission_keys.go`): the admin application is
  upstream's own frontend and speaks upstream's names, while the
  forum's handlers and seeded rows use the flat names. Without the
  translation the permission grid writes keys no handler reads, so
  granting a permission would change nothing;
- the settings table starts EMPTY, where upstream writes its defaults
  at install. The readers fall back to upstream's installed values, so
  the policies behave the same.

The admin surface's WIRE FORMAT is upstream's, and it is not JSON:API:
`POST /settings` takes the settings map itself and answers an empty
204, `DELETE /settings` takes `{keys, extensionId}`, `POST /permission`
takes `{permission, groupIds}`, and `POST /groups/order` takes
`{order}`. There is no `GET /settings`: upstream has none, and the
settings reach the admin application through the boot payload.

## 12. The access-token list holds the current session only

Upstream lists every access token of the account: the session tokens
and the developer tokens, with their title, device, last IP address and
last activity.

This port cannot. A PocketBase auth token is a **stateless JWT** — the
whole payload is signed, and there is no stored token row to enumerate
— and the forum does not mint developer tokens at all. So the list
holds the caller's current session and nothing else, and the developer
list is empty.

The endpoint still exists and still answers with a JSON:API **array**:
the frontend joins the session and developer answers with
`Promise.all([...]).then(([sessions, developerTokens]) => …)`, and a
single object is not iterable, so the destructuring fails with "e is
not iterable".

## 13. The API honours `X-HTTP-Method-Override`

Upstream's frontend rewrites every method other than GET and POST into
a POST with the intended method in the `X-HTTP-Method-Override` header,
and upstream's API carries a `FakeHttpMethods` middleware that reads it
back. This port does the same for the routes the frontend calls with a
non-POST method: the route is registered for both verbs and the
handler resolves the effective method.

The middleware itself is not ported: upstream's is a PSR-15 layer, and
PocketBase's router matches on the request method before any
middleware runs, so the override has to be resolved inside the
handler rather than in front of the router.

**An API path that does not exist answers with the memos SPA's
`index.html` and a 200, not with a 404.** The SPA's catch-all is a
method-less `/{path...}`, and it is the least specific route in the
mux, so it answers everything the forum does not. The frontend's
request helper throws on a non-2xx, so a 200 with an HTML body is the
worst outcome: the caller tries to parse a page of markup as data.
Every forum API path the frontend calls has to exist.

## 14. Attachments are a first-party plugin, because upstream's core has none

Upstream Flarum v2.0.0's **core has no attachment support at all**:
the `post_attachments` table and the `Post->upload()` relationship
were removed from the core, and the core frontend carries no attachment
code either. Attachments live in the `fof/upload` extension — the
de-facto standard for Flarum 2.x and the successor to
`flagrow/upload`.

The forum implements them as the first-party **Uploads plugin**
(`pb/flarum/plugins/uploads/`), mirroring that extension's contract:
the resource type is `files`, a file carries `url`, `size`,
`humanSize`, `imageWidth`, `imageHeight`, `hidden`, `baseName` and
`type`, and a file hangs off a post through the post's `files`
relationship.

Three differences from the extension, all deliberate:

- **THE BYTES ARE SERVED BY THE FORUM'S OWN ROUTE** at
  `{basePath}/files/{id}`. Upstream's `fof/upload` serves the bytes
  from its own storage at a URL the extension owns; this port keeps
  the bytes in PocketBase's filesystem (so the existing storage
  bridge and its S3 support apply) and streams them through the forum,
  because PocketBase's native file URL lives at the root of the
  listener the forum does not own.
- **THERE IS NO THUMBNAIL.** Upstream's extension generates a WebP
  thumbnail at upload time and stores `thumbnail_url`; the port stores
  the image's dimensions instead, which is what the frontend needs to
  lay the image out before it loads. The memo attachment surface
  already generates thumbnails on demand (`?thumbnail=true`), and the
  forum's file route could adopt the same mechanism — but it does not
  yet.
- **THERE IS NO WATERMARKING, RESIZING OR ADAPTER LAYER.** Upstream's
  extension has a storage-adapter abstraction (local, S3, Qiniu, …)
  and an image pipeline. This port relies on PocketBase's own
  filesystem and its storage bridge, and stores the file as it was
  uploaded.

## 15. Notifications are limited

Upstream's notification system is a per-extension blueprint registry
with a large catalogue of notification types. The forum's
`forum_notifications` collection carries the notification kinds the
first-party plugins produce (`postLiked`, `discussionTagged`,
`mentionedInPost`, `commentOnPost`).

## 16. The deferred items

The following are **roadmap**, not v1:

- the Goja sandbox hook layer (`backendKind: js-sandbox` is accepted by
  the manifest but refused by the manager with an explicit message);
- plugin package signing and marketplace manifest checks;
- the remaining upstream extensions (mentions, subscriptions, sticky,
  lock, suspend, and the rest).

## 17. The frontend build needs npm's legacy peer resolver

The upstream monorepo is a Yarn workspace with hoisting assumptions
that npm's strict resolver rejects. The build therefore runs with
`npm install --legacy-peer-deps`. This is a build-time detail with no
effect on the shipped bundle.
