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
  The administrator is **a member of the administrator group** (upstream
  derives `isAdmin()` from the group alone), or a PocketBase superuser —
  the union of the two, because riff's superusers are the instance's
  operators. See DECISIONS.md "An administrator is a MEMBER OF THE ADMIN
  GROUP, not a superuser".
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

**riff's `#tag` extension is not enabled.** It is riff's memos syntax
(a `#tag` becomes a memo tag), and it builds AST nodes with no HTML
node renderer — so enabling it makes the HTML renderer emit nothing for
the node and every `#tag` in a post silently vanishes from the rendered
body. In a Flarum post a `#tag` is literal text, which is what upstream
shows too.

**The `@mention` extension IS enabled, and it is the one exception.**
Upstream's `flarum/mentions` renders `@username` as a link to the
account's profile, and the forum does the same. It is enabled through
`markdown.WithMentionLinks`, which does BOTH halves — the parser AND
the HTML node renderer — because a mention node carries no source
segment: a renderer that emits nothing DELETES the mention from the
rendered body. With no href builder the mention renders as a `<span>`
rather than as nothing, so the text is never lost. The URL is the
forum's own user route (`/u/:username`), so the link resolves inside
the mount path. See the Mentions plugin.

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

## 10a. The plugin settings are upstream's keys, but only the ones the server reads

Upstream's extensions register their settings on their extension page,
with upstream's own keys (`flarum-likes.like_own_post`,
`flarum-tags.min_primary_tags`, `fof-polls.maxOptions`, …). An
operator's saved setting therefore carries over, and the setting means
what it means upstream.

This port exposes the settings **the server READS** and no others. The
split is deliberate, and the audit that produced it is worth stating:

| Plugin | Upstream's settings | Exposed here |
| --- | --- | --- |
| `likes` | `like_own_post` | `like_own_post` |
| `mentions` | `allow_username_format` | `allow_username_format` |
| `tags` | `min/max_primary_tags`, `min/max_secondary_tags` | all four |
| `gamification` | about twenty | `allowSelfVotes`, `defaultMetric`, `defaultPeriod` |
| `polls` | eight | none |
| `sticky` | three | none |
| `lock`, `suspend`, `markdown`, `statistics`, `uploads` | none (permissions and custom pages only) | none |

`statistics` declares no settings in either column, and its admin surface is
a CUSTOM PAGE rather than a settings list — upstream's `StatisticsPage`, the
totals, the entity and period selectors and the chart, plus the mini widget
on the dashboard. That is the presentation half; the data half is
`GET /api/statistics`, and `statisticsEntities` travels on the forum document
to administrators only, exactly as upstream's `ForumResource` field does.

The chart is drawn as INLINE SVG rather than with upstream's `frappe-charts`.
The library is not in this port's dependency tree, and adding it for a single
admin page is a dependency decision; the shape is upstream's (two series,
this period against the previous one, the same windows). The one upstream
control that is NOT ported is the chart's PNG export button, because
`frappe-charts` is what implements the export — shipping a button that cannot
save a file would be the dead-affordance problem again.

**A setting nobody reads is a control that changes nothing.** The
`polls` plugin's `maxOptions` and `enableDiscussionPolls` were declared
as constants but never read — this port has no poll CREATION path, so
there is nothing for them to gate — and the gamification's
`firstPostOnly`, `autoUpvotePosts`, `upVotesOnly` and
`viewRankingPage` were declared and never read either. Exposing them
would have put four switches and two number fields on an admin page
that changed nothing, which is worse than their absence: the operator
turns a switch and the forum does not move.

The dead constants were REMOVED rather than exposed. The audit is
pinned by `TestForum_ThePluginSettingsAreTheOnesTheServerReads` and
`TestForum_TheAdminBundlesCarryNoDeadSettings`.

**The tag COUNT RULES are a RANGE, not an exact count.** Upstream
writes the rule as `$min === $max ? "size:$min" : "between:$min,$max"`:
a minimum equal to the maximum is an exact count (one primary tag
means exactly one primary tag), while a minimum different from the
maximum is a range. This port always demanded "exactly the minimum",
so a set of two primary tags was refused even with
`flarum-tags.max_primary_tags` raised to two — the operator's setting
did nothing. The rule now matches upstream's.

**The TAGS extension page IS the tag tree.** Upstream's `flarum/tags`
registers a CUSTOM admin page (`Extend.Admin().page(TagsPage)`), and
the admin application renders a custom page INSTEAD of the plain
settings list. The page has two tabs: the tag TREE — primary and
secondary tags, with create buttons and drag-to-reorder — and the
count settings.

This port carries that page: `TagsPage` and `EditTagModal` live under
`pb/flarum/frontend/packages/plugins/tags/js/admin/`, and the four
count settings are on the page's second tab rather than in the plain
registry. A plugin that registers a custom page must NOT also register
plain settings: the two would fight over the same page.

TWO FIELDS OF THE MODAL ARE ABSENT, because the server has no
counterpart: the icon IMAGE (upstream's `IconImageInput`, which uploads
a picture) and the tag's PARENT (upstream picks one in the modal). The
port's tag resource carries no icon-image attributes, so the input
would upload to an endpoint that does not exist.

**The gamification and polls SETTINGS PAGES are not ported.** Upstream's
`fof/gamification` uses a custom page with a rank editor and an image
uploader, and `fof/polls` groups its settings into four fieldsets.
Those two plugins register their settings with the plain registry, so
the form is the standard settings list rather than upstream's bespoke
page. The upstream `sticky` settings are not exposed at all:
`pin_sticky_on_all_discussions` and `only_sticky_unread_discussions`
gate a query this port does not run (the discussion list does not pin
sticky discussions to the top), and `enable_display_excerpt` needs a
`firstPostExcerpt` attribute and an `excerptDisplayEnabled` forum
attribute, neither of which exists here.

**The admin payload carries the SORT MAPS.** The tags page lists a
tag's DEFAULT SORT from `app.data.sortMaps`, so that a sort an
extension added can be chosen for a tag without that extension knowing
anything about tags. The map is keyed by resource and maps the sort's
URL key to the API sort behind it — the same shape the forum's own
`DiscussionListState.sortMap()` builds. An empty map is not a harmless
omission: the dropdown would offer only "No preference".

**The settings are STRINGS in the admin payload.** Upstream's settings
column is TEXT, so `Settings::all()` hands the admin application a map
of strings and the application types every setting as `Stream<string>`.
This port's `forum_settings.value` column is a JSON field, so a stored
value arrives as the JSON type it was written as — a number as
`float64`, a boolean as `bool`, and in practice as raw JSON bytes
(`types.JSONRaw`). The admin payload normalises every setting to a
string, so the form sees what upstream shows it. Without that, a number
lands in a stream typed as a string and the form saves it back as a
number, and a boolean switch reads `value !== '0'` against a value
that is neither `"0"` nor `0`.

**The two halves of the contract are separate.** The Go side declares
the settings the server reads (`plugin.AddSettings`), and the plugin's
admin bundle registers the form that edits them
(`Extend.Admin().setting(...)`). The admin application renders a
setting that a bundle registered and nothing else, so a declared
setting with no bundle is a setting the operator cannot reach. Both
halves must agree on the KEY, and the test pins that they do.

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
`mentionedInPost`, `commentOnPost`, `userMentioned`).

**The kind is the `type` field, and the collection is the authority on
what it may be.** A `SelectField` refuses a value it does not allow, so
a kind the collection does not carry cannot be written at all. Each
plugin that produces a kind its upstream blueprint names differently
WIDENS the collection, idempotently — the Mentions plugin adds
`userMentioned` (upstream's `UserMentionedBlueprint::getType()`) and
keeps the `mentionedInPost` the collection already carried.

**The kind is PUBLISHED as `contentType`, not as `type`.** The
vendored SPA looks a notification's component up by that name
(`NotificationType.tsx` reads `notification.contentType()` and uses it
as the key into `app.notificationComponents`), so a notification that
carries the kind under any other name renders an EMPTY ROW — while the
unread badge still counts it, because the badge reads `isRead`. The
symptom is a badge with a blank list. `contentType` and `content` are
published ALONGSIDE the old names rather than in place of them, so the
rename is additive.

## 16. The deferred items

The following are **roadmap**, not v1:

- the Goja sandbox hook layer (`backendKind: js-sandbox` is accepted by
  the manifest but refused by the manager with an explicit message);
- plugin package signing and marketplace manifest checks;
- upstream's **subscriptions** extension. Mentions, tags, likes, lock,
  sticky, suspend, polls, gamification, statistics, markdown and uploads
  are all built; subscriptions is the remaining first-party extension.

### 16b. What `tags` implements in the SPA, and what it does not

Upstream's `flarum/tags` is the largest first-party front end here, and it was
the least aligned: the port registered ONE extender where upstream registers
six plus a route model. The gap is now closed for navigation, labelling and
filtering, and still open for authoring.

**Built** (upstream's names, adapted where the port's core differs):

- `Extend.Routes()` — `tags` at `/tags` (the taxonomy as tiles) and `tag` at
  `/t/:slug` (the ordinary index page filtered to one tag), plus `helper('tag')`
  so every link uses the pretty URL. Before this there was no tag page at all,
  and the label was deliberately a non-link.
- `addTagLabels()` — all three halves: labels on a list item, a linked tag stack
  above a discussion's title, and the hero's colour from the first tag.
- `addTagFilter()` — the route param reaches the search state, the request sends
  `filter[tag]`, and the page wears the tag's hero, title and button colour.
- `Extend.Model(Discussion).hasMany('tags').attribute('canTag')` — so
  `discussion.tags()` is the model method upstream's code calls.
- The `allow_tag_change` setting, which the server READ but nothing could set.
- `addTagComposer()` — the composer's tag picker, the tags travelling with
  the discussion, a tag page's tag preselected, and the minimums enforced
  before submitting.
- `addTagControl()` — the discussion page's "Edit tags" button, which opens
  the same picker seeded with the discussion's CURRENT tags and saves the
  result as the `tags` relationship.
- The four COUNT settings on the forum document (`minPrimaryTags` and its
  three siblings), so the picker knows the limits before an author chooses
  anything. The server enforces the same counts, so a picker that ignored
  them would let an author compose a discussion the server then refuses.

**Not built**, and why each is a real absence rather than an oversight:

- `addTagControl()` — the tag button on a discussion page, and the save that
  carries the new set.
- `discussionTagged` PostTypes — a rename or tag change is stored as a post of
  that type, and the port renders no component for it, so a stream shows those
  events as ordinary posts.
- `app.tagList` (`TagListState`) — a shared, sorted, indexed tag list. The port
  reads the store directly, which is equivalent for the sizes involved.
- `extendRealtime` — there is no realtime extension to hook.
- `addTagsPermissionScope` / `addTagsHomePageOption` / `addTagSlugDriverLabels`
  — no per-tag scope on the admin permission grid (the Go layer enforces
  scopes; the grid cannot grant one), no "Tags" home-page option, and no slug
  driver labels.

Two adaptations worth recording, because the upstream code would NOT work here
if copied:

1. **The hero colour.** Upstream's `addTagLabels` writes `view.attrs.style` and
   mutates `view.attrs.className`. This port's `Hero.view()` renders
   `style={this.style()}` and `classList('Hero', this.className())`, so the
   attrs are never consulted — the extension overrides `style()` and
   `className()` instead.
2. **`include=tags`.** Upstream pushes `tags,parent` into the list request's
   `include`. This port's `included` array only builds the resources the CORE
   knows (users, posts, groups); a tag resource belongs to the plugin's own
   serialiser. The store is filled by `GET /api/tags` instead, which loads every
   tag once per page load, and `discussion.tags()` resolves against it. Asking
   for an include the server cannot answer would be a silent half-answer —
   which is exactly what an earlier attempt at `filter[tag]` produced.

That last one is the reason `discussion.tags()` is guarded: `Model.hasMany` maps
a relationship's identifiers through the store WITHOUT dropping the ones that
have not arrived, so `sortTags` filters falsy entries rather than throwing on
`position()` and taking the whole list down with it.

Two more adaptations, both forced by the composer:

3. **THE CHOSEN TAGS LIVE OUTSIDE `composer.fields`.** Upstream puts them
   there (`this.composer.fields.tags`), but this port's `fields` map is typed
   `Record<string, Stream<any>>` — a plain array in it would be read as a
   Stream, and every read would be `undefined`.
4. **CREATING A DISCUSSION WITH TAGS IS A DIFFERENT QUESTION FROM CHANGING
   ONE.** Upstream's `DiscussionPolicy::tag()` governs changing the tags of a
   discussion that EXISTS: the author may, while `allow_tag_change` permits
   it, and a moderator may through the `discussion.tag` permission. A
   discussion being CREATED has no author to be "the author who may still
   change it", and `allow_tag_change` is explicitly about changing tags AFTER
   posting. The port's relationship writer asked the changing question on
   create, so every tagged create answered 422 — the composer could offer a
   picker whose result the server refused. The writer now branches on
   `req.Creating` and asks the ordinary question instead: may this actor
   start a discussion with this tag (`canStartDiscussion`, the same answer
   the picker used to decide whether to offer it).

## 16a. The endpoints the admin and forum call that are not routed

The reused Flarum frontend calls a fixed set of endpoints, and an
unrouted path does not answer 404: the SPA catch-all answers it with
`index.html` and a 200, which the request helper then tries to parse as
data. Every path the frontend calls therefore has to exist, or the
affordance that calls it has to be absent.

An audit of every path the vendored frontend builds against upstream's
`Api/routes.php` and `Forum/routes.php` found four groups.

### Routed now, because the settings existed and nothing could write them

The Appearance page's five uploads — `POST`/`DELETE` on `/logo`,
`/logo-dark-mode`, `/email-logo`, `/favicon` and `/app-icon`. The
settings they write (`logo_path` and friends) were already read into
the forum document's `logoUrl`, `faviconUrl` and so on, so without the
routes an administrator could see the buttons, upload a file, and get a
404 — and the logo could never change. The bytes live in PocketBase's
filesystem under `forum_assets/`, and are served at
`{basePath}/branding/{name}`: NOT under `/assets/`, which is the BUILT
BUNDLE tree, and not at PocketBase's native `/api/files/...` URL,
which lives at the listener root the forum does not own. Pinned by
`assets_test.go`.

### Removed, because the forum cannot honour them

- **the login modal's "Forgot password?" link** and the **settings
  page's "Change password" button**. Both open a form that posts to
  `/forgot`, and the forum has no such endpoint: registration, email
  verification and password reset stay with the host's auth layer.
  PocketBase owns the reset flow, and it is not callable from Go, so
  the alternative would be a second implementation of it. The Memos
  SPA's account settings change the password, and they are the same
  account.
- **the security page's "Log out everywhere"**. It posts to
  `/global-logout`, which cannot be implemented: a PocketBase auth
  token is a STATELESS JWT, so there is no session row to terminate,
  and the only way to invalidate every token is to rotate the
  account's `tokenKey` — a reserved auth field the forum must never
  write.

  The "terminate all other sessions" button beside it is left in
  place and is ALWAYS DISABLED, because the session list holds only the
  current session (deviation 12): `hasOtherActiveSessions()` is false,
  so the button is inert rather than broken.

### Not reachable, so not routes

- **`/register`** — the sign-up link is behind `allowSignUp`, which the
  forum document sets to false.
- the queue, announcements, mail-test, bounce and webhook routes — the
  admin payload reports `queueDriver: "sync"`,
  `announcementsDisabled: true` and `canReceiveWebhooks: false`, so the
  widgets that call them never render.
- `/extension-bisect`, `/extensions/abandoned/sync`, `/system-info`,
  `/sessions` (admin) and `/opensearch/suggestions` — one-shot admin
  commands whose buttons are reachable but whose answers are an empty
  204 or a count. They are inert in this port; a click reports
  nothing rather than failing a save.

### Tracked, not implemented

- **mark-as-read** (`POST /discussions/read`). The forum's "Mark all as
  read" button and the discussion list's scroll handler both call it,
  and the server tracks no read state at all: there is no
  `lastReadPostNumber` and no per-discussion unread count. Implementing
  it is a subsystem (a `discussion_user` table, a `marked_all_as_read_at`
  column, the unread counts, and the read-state filters), not a route.

### The notification kinds that are allowed but never produced

The `forum_notifications` collection allows `postLiked`,
`discussionTagged`, `mentionedInPost` and `commentOnPost`, and the
Mentions plugin widens it with `userMentioned`. Only `postLiked` and
`userMentioned` are ever written: `discussionTagged`,
`mentionedInPost` and `commentOnPost` are values the collection
accepts and nothing produces, and upstream's own names for the mention
kinds are `userMentioned` / `postMentioned` / `groupMentioned` —
`mentionedInPost` is not one of them. The three unused values are
therefore **dead**: a kind in the schema that no writer emits is a row
that cannot exist.

**The `postLiked` kind had no frontend component.** The server produced
it and the SPA looked its component up by `contentType` and found
none, so the notification rendered as an EMPTY ROW while the unread
badge still counted it — the exact symptom deviation 15 warns about.
The likes bundle now registers `PostLikedNotification`, and the
`post` relationship and `excerpt` attribute the component reads.

## 17. The frontend build needs npm's legacy peer resolver

The upstream monorepo is a Yarn workspace with hoisting assumptions
that npm's strict resolver rejects. The build therefore runs with
`npm install --legacy-peer-deps`. This is a build-time detail with no
effect on the shipped bundle.
