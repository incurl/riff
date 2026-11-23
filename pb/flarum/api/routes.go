package api

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// Mount registers the JSON:API routes on a router.
//
// Every pattern carries an explicit method. Go's http.ServeMux (which
// this router wraps) panics when two overlapping patterns are registered
// without one being strictly more specific, and the riff SPA catch-all
// is a method-less "/{path...}". A method-less "/forum/api/{path...}"
// would be a sibling of that pattern, not a child, and the process would
// not start.
func (s *Server) Mount(r *router.Router[*core.RequestEvent]) {
	api := s.apiPath

	// --- the forum document -------------------------------------------
	// The frontend's boot sequence fetches this to learn the API URL,
	// the base path and the caller.
	//
	// The trailing-slash form is a SUBTREE match in Go's http.ServeMux
	// ("/" acts as an anonymous "...\/" wildcard), so registering it
	// would answer EVERY unrouted path under /forum/api/ with the forum
	// document. That is worse than a 404: the document's `data` is a
	// single object rather than an array, so a caller that asked for a
	// list (the frontend's `store.find`) gets one object back, and the
	// `Promise.all([...])` that destructures the answer fails with
	// "e is not iterable".
	//
	// The forum document is served at the exact path instead, and the
	// API's own catch-all answers everything else with a JSON:API 404.
	r.GET(api, s.showForumHandler)

	// --- discussions ---------------------------------------------------
	r.GET(api+"/discussions", s.listDiscussionsHandler)
	r.POST(api+"/discussions", s.createDiscussionHandler)
	r.GET(api+"/discussions/{id}", s.showDiscussionHandler)
	r.PATCH(api+"/discussions/{id}", s.updateDiscussionHandler)
	r.DELETE(api+"/discussions/{id}", s.deleteDiscussionHandler)

	// --- posts ---------------------------------------------------------
	r.GET(api+"/posts", s.listPostsHandler)
	r.POST(api+"/posts", s.createPostHandler)
	r.GET(api+"/posts/{id}", s.showPostHandler)
	r.PATCH(api+"/posts/{id}", s.updatePostHandler)
	r.DELETE(api+"/posts/{id}", s.deletePostHandler)

	// --- users ---------------------------------------------------------
	r.GET(api+"/users", s.listUsersHandler)
	r.GET(api+"/users/{id}", s.showUserHandler)
	r.PATCH(api+"/users/{id}", s.updateUserHandler)
	// The reset is registered for POST as well as DELETE, because the
	// frontend does not send a DELETE: it turns every method other than
	// GET and POST into a POST with an `X-HTTP-Method-Override` header
	// (see the core's `Application.request`). The handler resolves the
	// effective method itself.
	//
	// The route carries an explicit method on purpose. A method-less
	// pattern conflicts with the SPA's method-less catch-all in
	// http.ServeMux, and that panic takes the process down.
	r.DELETE(api+"/users/{id}/notification-preferences", s.resetNotificationPreferencesHandler)
	r.POST(api+"/users/{id}/notification-preferences", s.resetNotificationPreferencesHandler)

	// --- groups --------------------------------------------------------
	r.GET(api+"/groups", s.listGroupsHandler)
	r.GET(api+"/groups/{id}", s.showGroupHandler)
	// The reorder is registered for POST as well, because the frontend
	// does not send a PATCH: it turns every method other than GET and
	// POST into a POST with an `X-HTTP-Method-Override` header (see
	// the core's `Application.request`). The handler resolves the
	// effective method itself.
	r.POST(api+"/groups/order", s.orderGroupsHandler)

	// --- notifications -------------------------------------------------
	r.GET(api+"/notifications", s.listNotificationsHandler)
	r.POST(api+"/notifications/read", s.readAllNotificationsHandler)
	r.PATCH(api+"/notifications/{id}", s.readNotificationHandler)

	// --- settings ------------------------------------------------------
	// The settings surface is NOT a JSON:API resource: upstream's
	// admin application posts a bare settings map and expects an empty
	// 204, so the handlers read a flat object rather than a resource
	// document. See pb/flarum/api/handlers_admin.go.
	//
	// The reset is registered for POST as well as DELETE, for the same
	// `X-HTTP-Method-Override` reason as the group reorder above. The
	// POST route resolves the effective method itself, because
	// PocketBase's router matches on the real method before any handler
	// runs, so a POST-with-override never reaches the DELETE route.
	r.POST(api+"/settings", s.settingsHandler)
	r.DELETE(api+"/settings", s.deleteSettingsHandler)

	// --- permissions ---------------------------------------------------
	// The permission grid's save. The body names the permission and the
	// groups that hold it, and the answer is an empty 204 — upstream's
	// `SetPermissionController`.
	r.POST(api+"/permission", s.setPermissionHandler)

	// --- mail ----------------------------------------------------------
	// The admin application's mail page fetches this on mount, so the
	// route has to exist: a 404 leaves `response.data` undefined and the
	// page throws on it.
	r.GET(api+"/mail/settings", s.showMailSettingsHandler)

	// --- access tokens --------------------------------------------------
	// The user security page lists the caller's access tokens. The
	// route has to exist: the page destructures the answer with
	// `Promise.all([...])`, and anything that is not an iterable fails
	// with "e is not iterable".
	r.GET(api+"/access-tokens", s.listAccessTokensHandler)

	// --- the API's own catch-all -----------------------------------------
	// Everything under /forum/api/ that no route above claims answers
	// with a JSON:API 404.
	//
	// The catch-all is NOT optional. Without it, an unrouted API path
	// falls through to the memos SPA's catch-all, which answers with
	// the SPA's index.html and a 200 — and the frontend's request
	// helper throws only on a non-2xx, so the caller parses a page of
	// markup as if it were data.
	//
	// It is registered per method, because a method-less pattern
	// conflicts with the SPA's method-less catch-all in http.ServeMux
	// and the panic takes the process down.
	r.GET(api+"/{path...}", s.notFoundHandler)
	r.POST(api+"/{path...}", s.notFoundHandler)
	r.PATCH(api+"/{path...}", s.notFoundHandler)
	r.PUT(api+"/{path...}", s.notFoundHandler)
	r.DELETE(api+"/{path...}", s.notFoundHandler)
}
