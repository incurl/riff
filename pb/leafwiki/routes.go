package leafwiki

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// mount registers every route of the wiki on the app's router.
//
// All patterns carry an explicit method. Go's http.ServeMux (which this router
// wraps) panics when two overlapping patterns are registered without one being
// strictly more specific, and the riff SPA catch-all is a method-less
// "/{path...}". A method-less "/wiki/{path...}" would be a sibling of that
// pattern, not a child, and the process would not start — the same trap
// pb/bark documents for /bark/mcp.
func (s *Server) mount(r *router.Router[*core.RequestEvent]) {
	p := s.opts.Prefix
	api := p + "/api"

	// --- bootstrap ----------------------------------------------------
	r.GET(api+"/config", s.configHandler)
	r.GET(api+"/health", s.healthHandler)

	// --- sessions -----------------------------------------------------
	r.POST(api+"/auth/login", s.loginHandler)
	r.POST(api+"/auth/logout", s.logoutHandler)
	r.POST(api+"/auth/refresh-token", s.refreshTokenHandler)
	r.GET(api+"/auth/me", s.meHandler)

	// --- tree ---------------------------------------------------------
	r.GET(api+"/tree", s.treeHandler)

	// --- pages --------------------------------------------------------
	// The page tree is dispatched by hand rather than registered as one
	// pattern per verb. LeafWiki's paths mix a literal verb BEFORE the id
	// ("/pages/permalink/{id}", "/pages/copy/{id}") with one AFTER it
	// ("/pages/{id}/assets", "/pages/{id}/links"), and Go's http.ServeMux
	// panics on that combination: "/pages/{id}/assets" and
	// "/pages/permalink/{id}" both match "/pages/permalink/assets", and
	// neither is more specific. Keeping the upstream paths (the SPA's API
	// client hard-codes them) therefore means dispatching inside one
	// handler. See dispatchPages.
	r.POST(api+"/pages", s.createPageHandler)
	r.GET(api+"/pages/{path...}", s.dispatchPages)
	r.POST(api+"/pages/{path...}", s.dispatchPages)
	r.PUT(api+"/pages/{path...}", s.dispatchPages)
	r.DELETE(api+"/pages/{path...}", s.dispatchPages)

	// --- attachment bytes ---------------------------------------------
	r.GET(p+"/assets/{pageId}/{filename}", s.serveAssetHandler)

	// --- SPA build ----------------------------------------------------
	// The built SPA's chunks and favicon. See mountStatic.
	s.mountStatic(r)

	// --- search, tags, links ------------------------------------------
	r.GET(api+"/search", s.searchHandler)
	r.GET(api+"/search/status", s.searchStatusHandler)
	r.GET(api+"/tags", s.tagsHandler)
	r.GET(api+"/tags/pages", s.pagesByTagsHandler)
	r.GET(api+"/links/broken", s.brokenLinksHandler)

	// --- SPA ----------------------------------------------------------
	// GET-only: see the comment at the top of mount.
	r.GET(p+"/{path...}", s.spaHandler)

	// Redirect the bare prefix once, so /wiki (no trailing slash) does not
	// look like a broken page. apis.Static does the same for directories.
	r.GET(p, func(e *core.RequestEvent) error {
		return e.Redirect(http.StatusMovedPermanently, p+"/")
	})
}

// fail maps a service error onto an HTTP response.
func (*Server) fail(e *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return e.NotFoundError(err.Error(), nil)
	case errors.Is(err, ErrAlreadyExists):
		return e.BadRequestError(err.Error(), nil)
	case errors.Is(err, ErrVersionConflict):
		return e.Error(http.StatusConflict, err.Error(), nil)
	case errors.Is(err, ErrInvalidArgument):
		return e.BadRequestError(err.Error(), nil)
	default:
		return e.InternalServerError(err.Error(), nil)
	}
}

// requireWriter authorises a mutating request and returns its actor.
func (s *Server) requireWriter(e *core.RequestEvent) (Actor, error) {
	record := s.currentUser(e)
	if record == nil {
		if s.opts.PublicWrite {
			return systemActor(), nil
		}
		return Actor{}, e.UnauthorizedError("authentication is required to modify the wiki", nil)
	}
	if !verifyCSRF(e) {
		return Actor{}, e.ForbiddenError("missing or invalid CSRF token", nil)
	}
	return Actor{ID: record.Id}, nil
}

// decodeBody decodes a JSON request body. An empty body is not an error: the
// handler validates the zero value, which is what makes a bodyless DELETE or
// sort call work.
func decodeBody(e *core.RequestEvent, target any) error {
	if e.Request.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 4<<20))
	err := decoder.Decode(target)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return errors.Wrap(ErrInvalidArgument, "invalid JSON body: "+err.Error())
}

// pathValue returns a path parameter.
func pathValue(e *core.RequestEvent, name string) string {
	return strings.TrimSpace(e.Request.PathValue(name))
}
