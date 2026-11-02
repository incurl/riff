# pb/leafwiki — LeafWiki on PocketBase

A port of [LeafWiki](https://github.com/perber/leafwiki) (MIT) onto PocketBase,
served at `/wiki`. It replaces LeafWiki's SQLite databases with PocketBase
collections and keeps the part of LeafWiki that matters most: **the Markdown
files on disk are the source of truth**.

| Upstream | Here |
| --- | --- |
| `cmd/leafwiki` (urfave/cli + gin) | PocketBase app, routes bound from `OnServe` |
| `search.db`, `tags.db`, `properties.db` | `leafwiki_pages` collection (rebuilt from disk) |
| `links.db` | `leafwiki_links` collection (rebuilt from disk) |
| `users.db`, `sessions.db`, `apikeys.db` | PocketBase `users` / `_superusers` + auth tokens |
| settings / branding / SMTP / git backup | not ported (reported as disabled in `/api/config`) |
| revisions, favorites, share links, snapshots | not ported (yet) |
| `<storage>/root/*.md`, `.order.json`, `assets/<pageId>/` | unchanged |

## Storage layout

Pages live in `<DataDir>/leafwiki` (default `pb_data/leafwiki`) in LeafWiki's own
layout, so an existing LeafWiki data directory can be pointed at this module
unchanged:

```
pb_data/leafwiki/
  schema.json               {"version": 5}
  root/
    getting-started.md      frontmatter + Markdown body
    guides/
      index.md              a section's own body
      .order.json           {"ordered_ids": ["<page id>", …]}
      setup.md
  assets/
    <pageId>/screenshot.png
```

Managed frontmatter keys (identical to upstream): `leafwiki_id`,
`leafwiki_title`, `leafwiki_created_at`, `leafwiki_updated_at`,
`leafwiki_creator_id`, `leafwiki_last_author_id`, `leafwiki_pinned`. Every other
key (`tags`, `owner`, nested maps) is preserved verbatim and surfaces as a page
property.

A file dropped into `root/` by an editor is adopted on the next reload: the title
comes from `leafwiki_title`, then the first `# heading`, then the filename, and a
new `leafwiki_id` is generated and written back so the page keeps a stable
identity.

A file or directory whose name is not a route-safe slug is skipped with a log
line rather than aborting the load. A **duplicate** slug or id (a directory and a
`.md` file sharing a name, say) is a structural ambiguity: it fails the load, and
therefore refuses to start the instance, so the operator fixes the directory
instead of silently losing a page.

## Index

`leafwiki_pages` and `leafwiki_links` are **caches**. `Index.Sync` reconciles them
with the tree after every mutation and after every load: changed rows are
upserted, vanished rows deleted, and the link table is rebuilt from scratch
because a link's state depends on the whole tree (creating a page heals every link
that points at it). Dropping both collections loses nothing but search results
until the next load.

That is why a failed index write is logged, never returned: a stale search row
must not stop a page from loading.


## HTTP surface

Everything is under the mount prefix (`/wiki` by default). The paths are
LeafWiki's, so its React SPA works unchanged — with one structural difference:
the `/pages/*` subtree is dispatched by hand (`api_dispatch.go`) because Go's
`http.ServeMux` **panics** on LeafWiki's mixed path shapes
(`/pages/permalink/{id}` and `/pages/{id}/assets` both match
`/pages/permalink/assets`, and neither is more specific).

```
GET    /wiki/                                     SPA (or the fallback reader)
GET    /wiki/api/config                           client bootstrap config
GET    /wiki/api/health                           liveness + index state
GET    /wiki/api/tree                             navigation tree
GET    /wiki/api/pages/{id}                       page, content and children
GET    /wiki/api/pages/by-path?path=…              page by route path
GET    /wiki/api/pages/permalink/{id}              stable id → current path
GET    /wiki/api/pages/slug-suggestion             unique slug helper
GET    /wiki/api/pages/lookup?path=…               path existence probe
GET    /wiki/api/pages/{id}/assets                 attachments
GET    /wiki/api/pages/{id}/links                  backlinks + outgoings
POST   /wiki/api/pages                             create page or section
POST   /wiki/api/pages/ensure                      create a missing path
POST   /wiki/api/pages/copy/{id}                   copy a page/subtree
POST   /wiki/api/pages/convert/{id}                page <-> section
POST   /wiki/api/pages/{id}/refactor/preview       rename/move impact
POST   /wiki/api/pages/{id}/refactor/apply         rename/move + link rewrite
POST   /wiki/api/pages/{id}/assets                 upload an attachment
PUT    /wiki/api/pages/{id}                        save content/metadata
PUT    /wiki/api/pages/{id}/move                   move under another parent
PUT    /wiki/api/pages/{id}/pin                    pin/unpin
PUT    /wiki/api/pages/{id}/sort                   reorder children
PUT    /wiki/api/pages/{id}/assets/rename          rename an attachment
DELETE /wiki/api/pages/{id}                        delete page/subtree
DELETE /wiki/api/pages/{id}/assets/{filename}      delete an attachment
GET    /wiki/assets/{pageId}/{filename}            attachment bytes
GET    /wiki/api/search?q=…&tags=…                 full-text + tag search
GET    /wiki/api/search/status                     index status
GET    /wiki/api/tags                              tag facets
GET    /wiki/api/tags/pages?tags=…                 pages for a tag set
GET    /wiki/api/links/broken                      broken-link audit
POST   /wiki/api/auth/login                        username/email + password
POST   /wiki/api/auth/logout                       clear the session cookie
POST   /wiki/api/auth/refresh-token                re-mint the session cookie
GET    /wiki/api/auth/me                           current user, or null

## Authentication

There is no second user table:

- a PocketBase **superuser** is an `admin`,
- any authenticated `users` record is an `editor`,
- an anonymous caller is a reader.

Reads are public. Writes accept either an `Authorization: Bearer <token>` header
(the PocketBase auth token, or any client that already holds one) or the session
cookie that `POST /api/auth/login` sets. Cookie-authenticated writes must echo
the `leafwiki_csrf` cookie in `X-CSRF-Token` (double-submit); a bearer-token
caller is exempt.

`RIFF_WIKI_PUBLIC_WRITE=1` turns the wiki into LeafWiki's "open editing without
login" mode: anonymous writes are accepted and `/api/config` reports
`authDisabled: true` so the SPA hides the login UI.

## Front end

`ui/index.html` is a small, dependency-free **fallback reader** (tree + raw
Markdown), tracked in git so `go build` never needs a Node toolchain. To use the
real UI, drop LeafWiki's build into `ui/dist/` (`dist/` is gitignored because it
is build output):

```bash
# once, in the LeafWiki checkout
cd ui/leafwiki-ui && npm ci && npm run build

# then copy the build next to the fallback
rm -rf pb/leafwiki/ui/dist && cp -r ui/leafwiki-ui/dist pb/leafwiki/ui/dist
```

`index.html` is served with LeafWiki's template tokens substituted
(`{{__BASE_PATH__}}` -> `/wiki`, `{{__PAGE_TITLE__}}`, `{{__FAVICON_HREF__}}`,
`{{__BODY__}}`), so a build made for a base-path deployment works unchanged.

## Not ported

Revisions, favorites, share links, snapshots/restore, git backup, SMTP (password
reset/invites), TOTP, API-key management, avatars, branding uploads, page imports
and the resync job have no equivalent yet. `/api/config` reports them as disabled
rather than letting the SPA discover a 404. A Markdown import is still possible by
copying files into `root/`: they are adopted on the next reload.

## Tests

```bash
go test -race -count=1 ./pb/leafwiki/            # unit + HTTP integration
go test -count=1 -run TestCreateLeafWikiTables ./pb/migrations/
```

The HTTP tests boot a real PocketBase app with riff's migrations, mount the wiki
on a router and drive it over `httptest`, so the index, the collections and the
auth flow are exercised for real.

```

Mutation responses carry the updated `Page`, including the `version` token the
next save must send. A stale version answers **409**, so two editors cannot
silently overwrite each other.
