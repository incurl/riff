package leafwiki

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// mountStatic registers the routes the built SPA needs: its chunks under
// /static and its favicon.
//
// The chunks are a separate route (not a wildcard under the SPA handler)
// because LeafWiki's own build references them as "/<prefix>/static/…"
// after the shell rewrite, and because they must be served with the right
// content type and caching headers rather than as index.html.
func (s *Server) mountStatic(r *router.Router[*core.RequestEvent]) {
	p := s.opts.Prefix

	r.GET(p+"/static/{path...}", s.staticAssetHandler)
	r.GET(p+"/favicon.svg", s.faviconHandler)
}

// staticAssetHandler serves a file from the built SPA's static/ directory.
func (s *Server) staticAssetHandler(e *core.RequestEvent) error {
	if s.ui.dist == nil {
		return e.NotFoundError("static assets are not bundled in this build", nil)
	}

	requestPath := strings.TrimPrefix(pathValue(e, "path"), "/")
	cleaned := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if cleaned == "" || cleaned == "." {
		return e.NotFoundError("asset not found", nil)
	}

	info, err := fs.Stat(s.ui.dist, "static/"+cleaned)
	if err != nil || info.IsDir() {
		return e.NotFoundError("asset not found", nil)
	}
	return e.FileFS(s.ui.dist, "static/"+cleaned)
}

// faviconHandler serves the SPA's favicon.
//
// LeafWiki serves a branding-configured favicon when the admin uploaded one
// and falls back to the default leaf SVG otherwise. This port has no branding
// upload, so the built-in icon is served; the file comes from the SPA build
// (Vite copies public/ into dist/).
func (s *Server) faviconHandler(e *core.RequestEvent) error {
	if s.ui.dist != nil {
		if raw, err := fs.ReadFile(s.ui.dist, "favicon.svg"); err == nil && len(raw) > 0 {
			e.Response.Header().Set("Content-Type", "image/svg+xml")
			e.Response.Header().Set("Cache-Control", "public, max-age=3600")
			e.Response.WriteHeader(http.StatusOK)
			_, _ = e.Response.Write(raw)
			return nil
		}
	}
	return e.NotFoundError("favicon not found", nil)
}
