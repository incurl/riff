package leafwiki

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// pageRoute is one entry of the hand-written page dispatcher.
//
// shape is a slash-separated template; a "{name}" segment captures a path
// parameter, everything else is a literal. Literal shapes must come before
// parameterised ones, because "{id}" also matches "by-path".
type pageRoute struct {
	method  string
	shape   string
	handler func(*Server, *core.RequestEvent) error
}

// pageRoutes is LeafWiki's page API, verb by verb.
//
// The paths are upstream's, unchanged: the SPA's client hard-codes them, and
// keeping them is what lets a LeafWiki build be dropped into dist/ and work.
var pageRoutes = []pageRoute{
	// --- GET ----------------------------------------------------------
	{method: "GET", shape: "by-path", handler: (*Server).pageByPathHandler},
	{method: "GET", shape: "lookup", handler: (*Server).lookupPathHandler},
	{method: "GET", shape: "slug-suggestion", handler: (*Server).slugSuggestionHandler},
	{method: "GET", shape: "permalink/{id}", handler: (*Server).permalinkHandler},
	{method: "GET", shape: "{id}/assets", handler: (*Server).listAssetsHandler},
	{method: "GET", shape: "{id}/links", handler: (*Server).linkStatusHandler},
	{method: "GET", shape: "{id}", handler: (*Server).pageHandler},

	// --- POST ---------------------------------------------------------
	{method: "POST", shape: "ensure", handler: (*Server).ensurePathHandler},
	{method: "POST", shape: "copy/{id}", handler: (*Server).copyPageHandler},
	{method: "POST", shape: "convert/{id}", handler: (*Server).convertPageHandler},
	{method: "POST", shape: "{id}/refactor/preview", handler: (*Server).refactorPreviewHandler},
	{method: "POST", shape: "{id}/refactor/apply", handler: (*Server).refactorApplyHandler},
	{method: "POST", shape: "{id}/assets", handler: (*Server).uploadAssetHandler},

	// --- PUT ----------------------------------------------------------
	{method: "PUT", shape: "{id}/move", handler: (*Server).movePageHandler},
	{method: "PUT", shape: "{id}/pin", handler: (*Server).pinPageHandler},
	{method: "PUT", shape: "{id}/sort", handler: (*Server).sortPagesHandler},
	{method: "PUT", shape: "{id}/assets/rename", handler: (*Server).renameAssetHandler},
	{method: "PUT", shape: "{id}", handler: (*Server).updatePageHandler},

	// --- DELETE -------------------------------------------------------
	{method: "DELETE", shape: "{id}/assets/{filename}", handler: (*Server).deleteAssetHandler},
	{method: "DELETE", shape: "{id}", handler: (*Server).deletePageHandler},
}

// dispatchPages routes a /wiki/api/pages/* request to its handler.
//
// It exists because Go's http.ServeMux cannot express LeafWiki's mixed path
// shapes: see the comment on the page routes in routes.go.
func (s *Server) dispatchPages(e *core.RequestEvent) error {
	segments := pathSegments(pathValue(e, "path"))

	for _, route := range pageRoutes {
		if route.method != e.Request.Method {
			continue
		}
		params, ok := matchShape(route.shape, segments)
		if !ok {
			continue
		}
		for name, value := range params {
			e.Request.SetPathValue(name, value)
		}
		return route.handler(s, e)
	}

	return e.NotFoundError("unknown page endpoint", nil)
}

// matchShape matches request segments against a route shape and returns the
// captured parameters.
func matchShape(shape string, segments []string) (map[string]string, bool) {
	shapeSegments := pathSegments(shape)
	if len(shapeSegments) != len(segments) {
		return nil, false
	}

	params := map[string]string{}
	for index, expected := range shapeSegments {
		actual := segments[index]
		if strings.HasPrefix(expected, "{") && strings.HasSuffix(expected, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(expected, "{"), "}")
			if actual == "" {
				return nil, false
			}
			params[name] = actual
			continue
		}
		if !strings.EqualFold(expected, actual) {
			return nil, false
		}
	}
	return params, true
}
