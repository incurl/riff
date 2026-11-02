// Package leafwiki is a self-contained port of LeafWiki
// (https://github.com/perber/leafwiki, MIT licensed) onto PocketBase.
//
// LeafWiki is a tree-structured Markdown wiki whose Markdown files on disk
// are the source of truth and whose SQLite databases are derived, rebuildable
// indexes. This package keeps the first half of that contract untouched and
// replaces the second half:
//
//   - Markdown pages stay on disk, in LeafWiki's own layout, under
//     <DataDir>/leafwiki. Because the layout is unchanged, a LeafWiki data
//     directory can be pointed at this module as-is.
//
//   - LeafWiki's SQLite files (pages, search, tags, links, properties,
//     favorites, share links, users, sessions, settings) are replaced by two
//     PocketBase collections, leafwiki_pages and leafwiki_links. They are
//     derived from the Markdown tree and are rebuilt on every Load, so a
//     missing, stale or half-written row can never lose content.
//
//   - LeafWiki's own user/session/API-key tables are replaced by PocketBase's
//     auth collections. A PocketBase superuser is the wiki's "admin"; any
//     authenticated `users` record is an "editor"; an anonymous caller is a
//     reader (read-only).
//
// The HTTP surface is mounted under /wiki by [Register]:
//
//	GET    /wiki/                                        SPA (see ui/)
//	GET    /wiki/api/config                              client bootstrap config
//	GET    /wiki/api/health                              liveness + index state
//	GET    /wiki/api/tree                                full navigation tree
//	GET    /wiki/api/pages/{id}                          page (content + children)
//	GET    /wiki/api/pages/by-path?path=...               page by route path
//	GET    /wiki/api/pages/permalink/{id}                stable id -> path
//	GET    /wiki/api/pages/slug-suggestion                unique slug helper
//	GET    /wiki/api/pages/lookup?path=...                path existence probe
//	POST   /wiki/api/pages                               create page/section
//	POST   /wiki/api/pages/ensure                        create a path if missing
//	POST   /wiki/api/pages/copy/{id}                     copy a page
//	POST   /wiki/api/pages/convert/{id}                  page <-> section
//	POST   /wiki/api/pages/{id}/refactor/preview         rename/move preview
//	POST   /wiki/api/pages/{id}/refactor/apply           rename/move + rewrite
//	PUT    /wiki/api/pages/{id}                          save content
//	PUT    /wiki/api/pages/{id}/move                     move under another parent
//	PUT    /wiki/api/pages/{id}/pin                      pin/unpin in the sidebar
//	PUT    /wiki/api/pages/{id}/sort                     reorder children
//	DELETE /wiki/api/pages/{id}                          delete page/subtree
//	GET    /wiki/api/pages/{id}/assets                   list page attachments
//	POST   /wiki/api/pages/{id}/assets                   upload an attachment
//	PUT    /wiki/api/pages/{id}/assets/rename            rename an attachment
//	DELETE /wiki/api/pages/{id}/assets/{filename}        delete an attachment
//	GET    /wiki/assets/{pageId}/{filename}              serve an attachment
//	GET    /wiki/api/search                              full-text + tag search
//	GET    /wiki/api/search/status                       index status
//	GET    /wiki/api/tags                                tag facets
//	GET    /wiki/api/tags/pages                          pages for a tag set
//	GET    /wiki/api/pages/{id}/links                    backlinks + outgoings
//	GET    /wiki/api/links/broken                        broken-link audit
//	POST   /wiki/api/auth/login                          username/email + password
//	POST   /wiki/api/auth/refresh-token                  re-mint the session cookie
//	GET    /wiki/api/auth/me                             current user (or null)
//	POST   /wiki/api/auth/logout                         clear the session cookie
//
// Every mutating route requires an authenticated caller unless the operator
// opted into anonymous editing with [Options.PublicWrite] (env
// RIFF_WIKI_PUBLIC_WRITE=1). Reads are public: the wiki is a wiki.
//
// The package imports no other riff package at runtime; it can be lifted into
// another PocketBase project together with its migration.
package leafwiki
