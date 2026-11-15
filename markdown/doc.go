// Package markdown is the riff markdown engine: parsing memo content into
// the AST in markdown/ast, the memos-specific syntax in markdown/extensions
// and markdown/parser (tags, mentions, math, GFM extras), rendering, and
// the derived memo payload (tags, properties, snippet).
//
// Two renderers sit beside each other: markdown/renderer renders the AST
// back to markdown text, and markdown/html renders it to an HTML fragment.
// The forum serves the latter as a post's contentHtml, which the client
// inserts as trusted HTML — so the HTML renderer escapes raw HTML by
// default (see markdown/html.Options.AllowRawHTML).
//
// Layering: markdown depends only on its own subpackages and on
// github.com/yuin/goldmark. It must not import pb/connect, pb/api, pb/hooks,
// pb/migrations, or anything PocketBase-specific. The Property type lives
// here (a riff-local equivalent of upstream's proto MemoPayload_Property)
// so callers can stay decoupled from any proto surface.
package markdown
