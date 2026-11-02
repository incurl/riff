package leafwiki

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// indexTokens are the placeholders LeafWiki's SPA build leaves in its
// index.html. The upstream server substitutes them at request time; this port
// does the same so a build of ui/leafwiki-ui works unchanged behind /wiki.
const (
	tokenBasePath = "{{__BASE_PATH__}}"
	tokenTitle    = "{{__PAGE_TITLE__}}"
	tokenFavicon  = "{{__FAVICON_HREF__}}"
	tokenBody     = "{{__BODY__}}"
	tokenSiteName = "{{__SITE_NAME__}}"
)

// spaHandler serves the wiki's front end, falling back to index.html so the
// client-side router handles deep links such as /wiki/guides/setup.
func (s *Server) spaHandler(e *core.RequestEvent) error {
	requestPath := strings.TrimPrefix(pathValue(e, "path"), "/")

	if requestPath != "" && requestPath != "index.html" && s.ui.dist != nil {
		cleaned := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
		if info, err := fs.Stat(s.ui.dist, cleaned); err == nil && !info.IsDir() {
			return e.FileFS(s.ui.dist, cleaned)
		}
	}

	return e.HTML(http.StatusOK, s.renderIndex())
}

// renderIndex reads index.html and substitutes LeafWiki's template tokens.
//
// The substitutions mirror upstream's shell handler (internal/http/shell.go)
// exactly, because the SPA is LeafWiki's own build:
//
//   - {{__BASE_PATH__}} becomes the mount prefix, which the SPA reads from
//     the base-path meta tag and uses for every API call and route;
//   - {{__FAVICON_HREF__}} becomes the prefix + "/favicon.svg";
//   - {{__PAGE_TITLE__}} and {{__SITE_NAME__}} become the wiki's name;
//   - {{__BODY__}} becomes empty (upstream server-renders the shell's
//     contents there; this port has no server-side rendering, and React
//     mounts into the empty div).
//
// The relative-to-absolute asset rewrite is the load-bearing part: Vite is
// configured with base "./", so the built index.html references its chunks as
// "./static/main-<hash>.js". A relative reference resolves against the
// CURRENT URL, so on a deep link such as /wiki/guides/setup the browser
// would ask for /wiki/guides/static/main-<hash>.js and get the SPA shell
// instead of the script — a blank page with no error in the console.
//
// Upstream avoids this by rewriting `"./static/` to `"<basePath>/static/`
// while rendering the shell. This port does the same, which is why the
// built SPA works behind /wiki without a rebuild.
func (s *Server) renderIndex() string {
	rendered := string(s.ui.index())

	rendered = strings.ReplaceAll(rendered, tokenBasePath, s.opts.Prefix)
	rendered = strings.ReplaceAll(rendered, tokenTitle, "Wiki")
	rendered = strings.ReplaceAll(rendered, tokenSiteName, "Wiki")
	rendered = strings.ReplaceAll(rendered, tokenFavicon, s.opts.Prefix+"/favicon.svg")
	rendered = strings.ReplaceAll(rendered, tokenBody, "")

	// The Vite base-path rewrite. The leading quote is part of the match so
	// only attribute values are rewritten, not prose.
	rendered = strings.ReplaceAll(rendered, `"./static/`, `"`+s.opts.Prefix+`/static/`)

	return rendered
}
