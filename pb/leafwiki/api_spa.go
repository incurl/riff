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
func (s *Server) renderIndex() string {
	rendered := string(s.ui.index())
	rendered = strings.ReplaceAll(rendered, tokenBasePath, s.opts.Prefix)
	rendered = strings.ReplaceAll(rendered, tokenTitle, "Wiki")
	rendered = strings.ReplaceAll(rendered, tokenFavicon, s.opts.Prefix+"/favicon.ico")
	rendered = strings.ReplaceAll(rendered, tokenBody, "")
	return rendered
}
